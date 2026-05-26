package service

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/db"
)

const repoAccessQuery = `
SELECT result.perm_level
FROM (
	SELECT
		CASE
			WHEN perms.site_admin THEN 3
			WHEN perms.owner_id = perms.user_id THEN 3
			WHEN perms.org_base_level >= perms.collab_level AND perms.org_base_level >= perms.team_level THEN perms.org_base_level
			WHEN perms.collab_level >= perms.team_level THEN perms.collab_level
			ELSE perms.team_level
		END AS perm_level
	FROM (
		SELECT
			u.id AS user_id,
			u.site_admin AS site_admin,
			r.owner_id AS owner_id,
			COALESCE(MAX(CASE
				WHEN c.permission = 'admin' THEN 3
				WHEN c.permission IN ('maintain', 'write', 'push') THEN 2
				WHEN c.permission IN ('triage', 'read', 'pull') THEN 1
				ELSE 0
			END), 0) AS collab_level,
			COALESCE(MAX(CASE
				WHEN tr.permission = 'admin' THEN 3
				WHEN tr.permission IN ('maintain', 'write', 'push') THEN 2
				WHEN tr.permission IN ('triage', 'read', 'pull') THEN 1
				ELSE 0
			END), 0) AS team_level,
			COALESCE(MAX(CASE
				WHEN owner.type = 'Organization' AND om.user_id IS NOT NULL THEN CASE
					WHEN owner.default_repository_permission = 'admin' THEN 3
					WHEN owner.default_repository_permission IN ('maintain', 'write', 'push') THEN 2
					WHEN owner.default_repository_permission IN ('triage', 'read', 'pull') THEN 1
					ELSE 0
				END
				ELSE 0
			END), 0) AS org_base_level
		FROM users u
		JOIN repositories r ON r.id = ?
		JOIN users owner ON owner.id = r.owner_id
		LEFT JOIN collaborators c ON c.repository_id = r.id AND c.user_id = u.id
		LEFT JOIN team_members tm ON tm.user_id = u.id
		LEFT JOIN team_repositories tr ON tr.team_id = tm.team_id AND tr.repository_id = r.id
		LEFT JOIN organization_members om ON om.organization_id = r.owner_id AND om.user_id = u.id
		WHERE u.id = ?
		GROUP BY u.id, u.site_admin, r.owner_id
	) AS perms
) AS result
`

// HasRepoAccess returns the effective permission a user has on a repository.
// Permission precedence is: site admin → repo owner → max(org base, collaborator, team grants).
func (s *Service) HasRepoAccess(ctx context.Context, repoID, userID uint) (RepoPermission, error) {
	if repoID == 0 || userID == 0 {
		return RepoPermissionNone, nil
	}

	if err := s.maybeBackfillAdminsForRepo(ctx, repoID, userID); err != nil {
		slog.Warn("repo access backfill failed", "repo_id", repoID, "user_id", userID, "error", err)
	}

	row := s.DBForCtx(ctx).Raw(repoAccessQuery, repoID, userID).Row()
	var level int
	if err := row.Scan(&level); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RepoPermissionNone, ErrNotFound
		}
		if isMissingTableErr(err) {
			return s.hasRepoAccessFallback(ctx, repoID, userID)
		}
		return RepoPermissionNone, err
	}
	return repoPermissionFromLevel(level), nil
}

func (s *Service) maybeBackfillAdminsForRepo(ctx context.Context, repoID, userID uint) error {
	if repoID == 0 || userID == 0 {
		return nil
	}

	var viewer db.User
	if ctxViewer, ok := UserFromContext(ctx); ok && ctxViewer.ID == userID {
		viewer = ctxViewer
	} else {
		if err := s.DBForCtx(ctx).Select("id", "user_kind").First(&viewer, "id = ?", userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
	}
	if viewer.UserKind != db.UserKindHuman {
		return nil
	}

	var agentIDs []uint
	if err := s.DBForCtx(ctx).
		Table("agent_bindings").
		Select("agent_user_id").
		Where("human_user_id = ?", viewer.ID).
		Pluck("agent_user_id", &agentIDs).Error; err != nil {
		if isMissingTableErr(err) {
			return nil
		}
		return err
	}
	if len(agentIDs) == 0 {
		return nil
	}

	var repo db.Repository
	if err := s.DBForCtx(ctx).Select("id", "owner_id").First(&repo, "id = ?", repoID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}

	var owner db.User
	if err := s.DBForCtx(ctx).Select("id", "type").First(&owner, "id = ?", repo.OwnerID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if owner.Type != db.TypeOrganization {
		return nil
	}

	return s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		var adminsTeam db.Team
		teamErr := tx.First(&adminsTeam, "organization_id = ? AND slug = ?", owner.ID, adminsTeamSlug).Error
		if teamErr != nil && !errors.Is(teamErr, gorm.ErrRecordNotFound) {
			return teamErr
		}

		var eligibleAgentIDs []uint
		if teamErr == nil {
			if err := tx.Table("team_members").
				Select("user_id").
				Where("team_id = ? AND user_id IN ?", adminsTeam.ID, agentIDs).
				Pluck("user_id", &eligibleAgentIDs).Error; err != nil {
				return err
			}
		}
		if len(eligibleAgentIDs) == 0 {
			if err := tx.Table("collaborators").
				Select("user_id").
				Where("repository_id = ? AND user_id IN ? AND permission = ?", repoID, agentIDs, "admin").
				Pluck("user_id", &eligibleAgentIDs).Error; err != nil {
				return err
			}
		}
		if len(eligibleAgentIDs) == 0 {
			return nil
		}

		principalIDs := append([]uint{viewer.ID}, eligibleAgentIDs...)
		adminsTeam, err := ensureAdminsPrincipalsTx(tx, owner.ID, principalIDs...)
		if err != nil {
			return err
		}
		return ensureAdminsTeamRepoTx(tx, adminsTeam.ID, repoID)
	})
}

func (s *Service) hasRepoAccessFallback(ctx context.Context, repoID, userID uint) (RepoPermission, error) {
	var user db.User
	if err := s.DBForCtx(ctx).Select("id", "site_admin").First(&user, "id = ?", userID).Error; err != nil {
		return RepoPermissionNone, wrapErrf(err, "user id %d", userID)
	}
	if user.SiteAdmin {
		return RepoPermissionAdmin, nil
	}

	var rep db.Repository
	if err := s.DBForCtx(ctx).Select("id", "owner_id").First(&rep, "id = ?", repoID).Error; err != nil {
		return RepoPermissionNone, wrapErrf(err, "repository id %d", repoID)
	}
	if rep.OwnerID == userID {
		return RepoPermissionAdmin, nil
	}

	best := RepoPermissionNone

	var collab db.Collaborator
	if err := s.DBForCtx(ctx).Select("permission").
		First(&collab, "repository_id = ? AND user_id = ?", repoID, userID).Error; err == nil {
		best = ParseRepoPermission(collab.Permission).Effective()
	} else if !errors.Is(err, gorm.ErrRecordNotFound) && !isMissingTableErr(err) {
		return RepoPermissionNone, err
	}

	type teamGrantRow struct {
		Permission string
	}
	var teamGrants []teamGrantRow
	if err := s.DBForCtx(ctx).
		Table("team_members").
		Select("team_repositories.permission").
		Joins("JOIN team_repositories ON team_repositories.team_id = team_members.team_id").
		Where("team_members.user_id = ? AND team_repositories.repository_id = ?", userID, repoID).
		Scan(&teamGrants).Error; err == nil {
		for _, grant := range teamGrants {
			if perm := ParseRepoPermission(grant.Permission).Effective(); repoPermissionDecisionLevel(perm) > repoPermissionDecisionLevel(best) {
				best = perm
			}
		}
	} else if !isMissingTableErr(err) {
		return RepoPermissionNone, err
	}

	var owner db.User
	if err := s.DBForCtx(ctx).Select("id", "type", "default_repository_permission").
		First(&owner, "id = ?", rep.OwnerID).Error; err != nil {
		return RepoPermissionNone, wrapErrf(err, "owner id %d", rep.OwnerID)
	}
	if owner.Type == db.TypeOrganization {
		var count int64
		if err := s.DBForCtx(ctx).
			Model(&db.OrganizationMember{}).
			Where("organization_id = ? AND user_id = ?", owner.ID, userID).
			Count(&count).Error; err == nil {
			if count > 0 {
				if perm := ParseRepoPermission(owner.DefaultRepositoryPermission).Effective(); repoPermissionDecisionLevel(perm) > repoPermissionDecisionLevel(best) {
					best = perm
				}
			}
		} else if !isMissingTableErr(err) {
			return RepoPermissionNone, err
		}
	}

	return best, nil
}

func isMissingTableErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such table") || (strings.Contains(msg, "doesn't exist") && strings.Contains(msg, "table"))
}

func (s *Service) requireRepoPermission(ctx context.Context, repoID uint, required RepoPermission) error {
	viewer, ok := UserFromContext(ctx)
	if !ok || viewer.ID == 0 {
		// Anonymous HTTP request — only allow read access to public repos.
		if IsAnonRequest(ctx) {
			if required == RepoPermissionRead && s.isPublicRepo(ctx, repoID) {
				return nil
			}
			return ErrNotFound
		}
		// Internal service call (no HTTP user context) — skip auth.
		return nil
	}
	perm, err := s.HasRepoAccess(ctx, repoID, viewer.ID)
	if err != nil {
		return err
	}
	if !perm.AtLeast(required) {
		// Authenticated user with no explicit grant — public repos are still readable.
		if required == RepoPermissionRead && s.isPublicRepo(ctx, repoID) {
			return nil
		}
		return ErrNotFound
	}
	return nil
}

// isPublicRepo returns true if the repository with the given ID exists and
// is public. A missing repo returns false — otherwise a nonexistent ID
// would be treated as public since Scan leaves priv=false with no error.
func (s *Service) isPublicRepo(ctx context.Context, repoID uint) bool {
	if repoID == 0 {
		return false
	}
	var priv bool
	res := s.DBForCtx(ctx).
		Model(&db.Repository{}).
		Select("private").
		Where("id = ?", repoID).
		Scan(&priv)
	if res.Error != nil || res.RowsAffected == 0 {
		return false
	}
	return !priv
}

// IsRepoPublic reports whether the repository exists and is public.
func (s *Service) IsRepoPublic(ctx context.Context, repoID uint) bool {
	return s.isPublicRepo(ctx, repoID)
}
