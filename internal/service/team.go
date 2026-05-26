package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func normalizeTeamPrivacy(_ string) string {
	return db.TeamPrivacyClosed
}

// NormalizeSlug converts a team name into a URL-safe slug.
// - Lowercase
// - Spaces/whitespace → single hyphen
// - Remove non-alphanumeric except hyphens
// - Trim leading/trailing hyphens
func NormalizeSlug(name string) string {
	// Convert to lowercase
	slug := strings.ToLower(name)

	// Replace any whitespace (spaces, tabs, newlines) with a single hyphen
	spaceRe := regexp.MustCompile(`\s+`)
	slug = spaceRe.ReplaceAllString(slug, "-")

	// Remove non-alphanumeric characters except hyphens
	nonAlphaRe := regexp.MustCompile(`[^a-z0-9-]`)
	slug = nonAlphaRe.ReplaceAllString(slug, "")

	// Replace multiple consecutive hyphens with a single hyphen
	multiHyphenRe := regexp.MustCompile(`-+`)
	slug = multiHyphenRe.ReplaceAllString(slug, "-")

	// Trim leading and trailing hyphens
	slug = strings.Trim(slug, "-")

	return slug
}

func buildDefaultRepoShareTeamName(repoName string, attempt int) string {
	base := strings.Trim(NormalizeSlug(repoName)+"-share", "-")
	if base == "" {
		base = "repo-share"
	}

	suffix := ""
	if attempt > 0 {
		suffix = fmt.Sprintf("-%04x", attempt)
	}
	maxBaseLength := 39 - len(suffix)
	if maxBaseLength < 1 {
		maxBaseLength = 1
	}
	if len(base) > maxBaseLength {
		base = strings.TrimRight(base[:maxBaseLength], "-")
	}
	if base == "" {
		base = "repo-share"
	}
	return strings.TrimRight(base+suffix, "-")
}

func findRepoShareTeamTx(tx *gorm.DB, orgID uint, repoName string) (db.Team, bool, error) {
	defaultName := buildDefaultRepoShareTeamName(repoName, 0)

	var team db.Team
	err := tx.
		Where(
			"organization_id = ? AND (slug = ? OR name = ? OR slug LIKE ? OR name LIKE ?)",
			orgID,
			defaultName,
			defaultName,
			defaultName+"-%",
			defaultName+"-%",
		).
		Order("name ASC").
		First(&team).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return db.Team{}, false, nil
	}
	return team, err == nil, err
}

func ensureRepoShareTeamTx(tx *gorm.DB, orgID uint, repoName string) (db.Team, error) {
	if existing, found, err := findRepoShareTeamTx(tx, orgID, repoName); err != nil {
		return db.Team{}, err
	} else if found {
		return existing, nil
	}

	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		teamName := buildDefaultRepoShareTeamName(repoName, attempt)
		team := db.Team{
			OrganizationID: orgID,
			Name:           teamName,
			Slug:           teamName,
			Privacy:        db.TeamPrivacyClosed,
		}
		if err := tx.Create(&team).Error; err != nil {
			lastErr = err
			if isDuplicateErr(err) {
				continue
			}
			return db.Team{}, err
		}
		return team, nil
	}
	if lastErr != nil {
		return db.Team{}, lastErr
	}
	return db.Team{}, ErrConflict
}

func wrapTeamWriteErr(err error, name string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) || isDuplicateErr(err) {
		return fmt.Errorf("%w: team %q already exists in this organization", ErrConflict, name)
	}
	return wrapErr(err)
}

// CreateTeam creates a new team for an organization.
func (s *Service) CreateTeam(ctx context.Context, orgID uint, name, slug, description, privacy string) (db.Team, error) {
	privacy = normalizeTeamPrivacy(privacy)

	// If slug is empty or same as name, generate it from name using NormalizeSlug
	if slug == "" || slug == name {
		slug = NormalizeSlug(name)
	}

	t := db.Team{
		OrganizationID: orgID,
		Name:           name,
		Slug:           slug,
		Description:    description,
		Privacy:        privacy,
	}
	if err := s.DBForCtx(ctx).Create(&t).Error; err != nil {
		return db.Team{}, wrapTeamWriteErr(err, name)
	}
	return t, nil
}

// GetTeam returns a team by its slug within an organization.
func (s *Service) GetTeam(ctx context.Context, orgID uint, slug string) (db.Team, error) {
	var t db.Team
	err := s.DBForCtx(ctx).First(&t, "organization_id = ? AND slug = ?", orgID, slug).Error
	return t, wrapErr(err)
}

// GetTeamByID returns a team by its primary key.
func (s *Service) GetTeamByID(ctx context.Context, teamID uint) (db.Team, error) {
	var t db.Team
	err := s.DBForCtx(ctx).First(&t, teamID).Error
	return t, wrapErr(err)
}

// ListOrgTeams returns all teams for an organization.
func (s *Service) ListOrgTeams(ctx context.Context, orgID uint) ([]db.Team, error) {
	var teams []db.Team
	if err := s.DBForCtx(ctx).Where("organization_id = ?", orgID).Order("name ASC").Find(&teams).Error; err != nil {
		return nil, wrapErr(err)
	}
	if len(teams) == 0 {
		return teams, nil
	}

	teamIDs := make([]uint, 0, len(teams))
	for _, team := range teams {
		teamIDs = append(teamIDs, team.ID)
	}

	type countRow struct {
		TeamID uint
		Count  int64
	}

	memberCounts := map[uint]int64{}
	var memberRows []countRow
	if err := s.DBForCtx(ctx).
		Model(&db.TeamMember{}).
		Select("team_id, COUNT(*) AS count").
		Where("team_id IN ?", teamIDs).
		Group("team_id").
		Scan(&memberRows).Error; err != nil {
		return nil, wrapErr(err)
	}
	for _, row := range memberRows {
		memberCounts[row.TeamID] = row.Count
	}

	repoCounts := map[uint]int64{}
	var repoRows []countRow
	if err := s.DBForCtx(ctx).
		Model(&db.TeamRepository{}).
		Select("team_id, COUNT(*) AS count").
		Where("team_id IN ?", teamIDs).
		Group("team_id").
		Scan(&repoRows).Error; err != nil {
		return nil, wrapErr(err)
	}
	for _, row := range repoRows {
		repoCounts[row.TeamID] = row.Count
	}

	for i := range teams {
		teams[i].MembersCount = memberCounts[teams[i].ID]
		teams[i].ReposCount = repoCounts[teams[i].ID]
	}
	return teams, nil
}

// UpdateTeam updates a team's properties.
// If the team name has changed, the slug will be automatically normalized and updated.
func (s *Service) UpdateTeam(ctx context.Context, t *db.Team) error {
	// Fetch the existing team to check if name changed
	var existing db.Team
	if err := s.DBForCtx(ctx).First(&existing, t.ID).Error; err != nil {
		return wrapErr(err)
	}

	// If name changed, update slug
	if existing.Name != t.Name {
		t.Slug = NormalizeSlug(t.Name)
	}
	t.Privacy = normalizeTeamPrivacy(t.Privacy)

	return wrapTeamWriteErr(s.DBForCtx(ctx).Save(t).Error, t.Name)
}

// DeleteTeam removes a team entirely.
func (s *Service) DeleteTeam(ctx context.Context, teamID uint) error {
	return wrapErr(s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("team_id = ?", teamID).Delete(&db.TeamRepository{}).Error; err != nil {
			return err
		}
		if err := tx.Where("team_id = ?", teamID).Delete(&db.TeamMember{}).Error; err != nil {
			return err
		}
		return tx.Delete(&db.Team{}, teamID).Error
	}))
}

// AddTeamMember adds a user to a team.
func (s *Service) AddTeamMember(ctx context.Context, teamID, userID uint, role string) error {
	if role == "" {
		role = "member"
	}
	var team db.Team
	if err := s.DBForCtx(ctx).Select("id", "organization_id").First(&team, "id = ?", teamID).Error; err != nil {
		return wrapErr(err)
	}
	isMember, err := s.IsOrgMember(ctx, team.OrganizationID, userID)
	if err != nil {
		return err
	}
	if !isMember {
		return fmt.Errorf("%w: organization membership required before joining team", ErrForbidden)
	}
	member := db.TeamMember{
		TeamID: teamID,
		UserID: userID,
		Role:   role,
	}
	return wrapErr(s.DBForCtx(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "team_id"}, {Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"role"}),
	}).Create(&member).Error)
}

// TeamMembershipStateView describes REST-facing team membership state.
type TeamMembershipStateView struct {
	Role  string
	State string
}

// CanManageTeamMembership reports whether viewer can manage membership for a team.
// canInviteUnaffiliated is true only for org admins/owners.
func (s *Service) CanManageTeamMembership(ctx context.Context, orgID, teamID, viewerID uint) (canManage bool, canInviteUnaffiliated bool, err error) {
	if orgID == 0 || teamID == 0 || viewerID == 0 {
		return false, false, nil
	}

	isAdmin, err := s.IsOrgAdmin(ctx, orgID, viewerID)
	if err != nil {
		return false, false, err
	}
	if isAdmin {
		return true, true, nil
	}

	var count int64
	err = s.DBForCtx(ctx).
		Model(&db.TeamMember{}).
		Where("team_id = ? AND user_id = ? AND role = ?", teamID, viewerID, "maintainer").
		Count(&count).Error
	if err != nil {
		return false, false, wrapErr(err)
	}
	return count > 0, false, nil
}

// AddOrInviteTeamMember adds an org member directly, or creates/updates a pending
// org invitation with the requested team membership for non-members.
func (s *Service) AddOrInviteTeamMember(ctx context.Context, orgID, teamID, inviteeID, inviterID uint, role string) (TeamMembershipStateView, error) {
	role, ok := normalizeTeamMemberRoleValue(role)
	if !ok {
		return TeamMembershipStateView{}, ErrValidation
	}
	if orgID == 0 || teamID == 0 || inviteeID == 0 {
		return TeamMembershipStateView{}, ErrValidation
	}

	canManage, canInviteUnaffiliated, err := s.CanManageTeamMembership(ctx, orgID, teamID, inviterID)
	if err != nil {
		return TeamMembershipStateView{}, err
	}
	if !canManage {
		return TeamMembershipStateView{}, fmt.Errorf("%w: team membership admin permission required", ErrForbidden)
	}

	isMember, err := s.IsOrgMember(ctx, orgID, inviteeID)
	if err != nil {
		return TeamMembershipStateView{}, err
	}
	if isMember {
		if err := s.AddTeamMember(ctx, teamID, inviteeID, role); err != nil {
			return TeamMembershipStateView{}, err
		}
		return TeamMembershipStateView{Role: role, State: "active"}, nil
	}

	if !canInviteUnaffiliated {
		return TeamMembershipStateView{}, fmt.Errorf("%w: org admin access required for unaffiliated invite", ErrForbidden)
	}

	if err := s.UpsertTeamPendingOrganizationInvitation(ctx, orgID, teamID, inviteeID, inviterID, role); err != nil {
		return TeamMembershipStateView{}, err
	}
	return TeamMembershipStateView{Role: role, State: "pending"}, nil
}

// GetTeamMembershipState returns active membership from TeamMember, or pending
// membership inferred from an active org invitation that includes the team.
func (s *Service) GetTeamMembershipState(ctx context.Context, orgID, teamID, userID uint) (TeamMembershipStateView, error) {
	member, err := s.GetTeamMember(ctx, teamID, userID)
	if err == nil {
		role, ok := normalizeTeamMemberRoleValue(member.Role)
		if !ok {
			role = "member"
		}
		return TeamMembershipStateView{Role: role, State: "active"}, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return TeamMembershipStateView{}, err
	}

	role, found, err := s.PendingTeamMembershipRole(ctx, orgID, teamID, userID)
	if err != nil {
		return TeamMembershipStateView{}, err
	}
	if !found {
		return TeamMembershipStateView{}, ErrNotFound
	}
	return TeamMembershipStateView{Role: role, State: "pending"}, nil
}

// EnableRepoTeamSharing ensures the repo has a default share team, upgrades an
// existing org member to team maintainer, and grants the team read access to
// the repo. It must not be used to bootstrap org membership.
func (s *Service) EnableRepoTeamSharing(ctx context.Context, repoID, userID uint) (db.Team, error) {
	if repoID == 0 || userID == 0 {
		return db.Team{}, ErrNotFound
	}

	var team db.Team
	err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		var repo db.Repository
		if err := tx.Preload("Owner").First(&repo, repoID).Error; err != nil {
			return err
		}
		if repo.Owner.Type != db.TypeOrganization || repo.OwnerID == 0 {
			return ErrNotFound
		}
		var membershipCount int64
		if err := tx.Model(&db.OrganizationMember{}).
			Where("organization_id = ? AND user_id = ?", repo.OwnerID, userID).
			Count(&membershipCount).Error; err != nil {
			return err
		}
		if membershipCount == 0 {
			return fmt.Errorf("%w: organization membership required before enabling team sharing", ErrForbidden)
		}

		provisioned, err := ensureRepoShareTeamTx(tx, repo.OwnerID, repo.Name)
		if err != nil {
			return err
		}

		member := db.TeamMember{
			TeamID: provisioned.ID,
			UserID: userID,
			Role:   "maintainer",
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "team_id"}, {Name: "user_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"role"}),
		}).Create(&member).Error; err != nil {
			return err
		}

		teamRepo := db.TeamRepository{
			TeamID:       provisioned.ID,
			RepositoryID: repo.ID,
			Permission:   "read",
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "team_id"}, {Name: "repository_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"permission"}),
		}).Create(&teamRepo).Error; err != nil {
			return err
		}

		provisioned.Organization = repo.Owner
		team = provisioned
		return nil
	})
	return team, wrapErr(err)
}

// RemoveTeamMember removes a user from a team.
func (s *Service) RemoveTeamMember(ctx context.Context, teamID, userID uint) error {
	return wrapErr(s.DBForCtx(ctx).
		Where("team_id = ? AND user_id = ?", teamID, userID).
		Delete(&db.TeamMember{}).Error)
}

// ListTeamMembers returns all members of a team.
func (s *Service) ListTeamMembers(ctx context.Context, teamID uint) ([]db.User, error) {
	var members []db.TeamMember
	if err := s.DBForCtx(ctx).
		Where("team_id = ?", teamID).
		Preload("User").
		Find(&members).Error; err != nil {
		return nil, wrapErr(err)
	}
	users := make([]db.User, 0, len(members))
	for _, member := range members {
		users = append(users, member.User)
	}
	return users, nil
}

// GetTeamMember returns a team membership record by team/user.
func (s *Service) GetTeamMember(ctx context.Context, teamID, userID uint) (db.TeamMember, error) {
	var member db.TeamMember
	err := s.DBForCtx(ctx).
		Where("team_id = ? AND user_id = ?", teamID, userID).
		First(&member).Error
	return member, wrapErr(err)
}

// AddTeamRepo associates a repository with a team.
func (s *Service) AddTeamRepo(ctx context.Context, teamID, repoID uint, permission string) error {
	normalized, ok := NormalizeGrantPermission(permission)
	if !ok {
		return fmt.Errorf("%w: %s", ErrValidation, GrantPermissionValidationMessage)
	}
	tr := db.TeamRepository{
		TeamID:       teamID,
		RepositoryID: repoID,
		Permission:   normalized,
	}
	return wrapErr(s.DBForCtx(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "team_id"}, {Name: "repository_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"permission"}),
	}).Create(&tr).Error)
}

// RemoveTeamRepo removes a repository from a team.
func (s *Service) RemoveTeamRepo(ctx context.Context, teamID, repoID uint) error {
	return wrapErr(s.DBForCtx(ctx).
		Where("team_id = ? AND repository_id = ?", teamID, repoID).
		Delete(&db.TeamRepository{}).Error)
}

// ListTeamRepos returns all repositories for a team with permissions.
func (s *Service) ListTeamRepos(ctx context.Context, teamID uint) ([]db.TeamRepository, error) {
	var repos []db.TeamRepository
	err := s.DBForCtx(ctx).
		Where("team_id = ?", teamID).
		Preload("Repository", func(tx *gorm.DB) *gorm.DB {
			return preloadRepoFull(tx)
		}).
		Find(&repos).Error
	return repos, wrapErr(err)
}

// ListPendingTeamInvitations returns active org invitations that include the team.
func (s *Service) ListPendingTeamInvitations(ctx context.Context, orgID, teamID uint) ([]db.OrganizationInvitation, error) {
	if orgID == 0 || teamID == 0 {
		return []db.OrganizationInvitation{}, nil
	}

	var invitations []db.OrganizationInvitation
	err := preloadOrganizationInvitation(activeOrganizationInvitationQuery(s.DBForCtx(ctx))).
		Where("organization_id = ?", orgID).
		Order("created_at DESC, id DESC").
		Find(&invitations).Error
	if err != nil {
		return nil, wrapErr(err)
	}

	filtered := make([]db.OrganizationInvitation, 0, len(invitations))
	for _, inv := range invitations {
		teamIDs, err := decodeOrganizationInvitationTeamIDs(inv.TeamIDsJSON)
		if err != nil {
			// Skip malformed legacy payloads instead of failing the entire listing.
			continue
		}
		for _, invitedTeamID := range teamIDs {
			if invitedTeamID == teamID {
				filtered = append(filtered, inv)
				break
			}
		}
	}
	return filtered, nil
}

func (s *Service) orgMembershipQuery(ctx context.Context, orgID, userID uint) *gorm.DB {
	return s.DBForCtx(ctx).
		Model(&db.OrganizationMember{}).
		Where("organization_id = ? AND user_id = ?", orgID, userID)
}

// IsOrgMember checks if a user belongs to any team in the organization.
func (s *Service) IsOrgMember(ctx context.Context, orgID, userID uint) (bool, error) {
	if orgID == 0 || userID == 0 {
		return false, nil
	}

	var count int64
	err := s.orgMembershipQuery(ctx, orgID, userID).Count(&count).Error
	return count > 0, wrapErr(err)
}

// ListOrgMemberUserIDs returns the subset of userIDs that are members of orgID.
func (s *Service) ListOrgMemberUserIDs(ctx context.Context, orgID uint, userIDs []uint) (map[uint]struct{}, error) {
	members := make(map[uint]struct{})
	if orgID == 0 || len(userIDs) == 0 {
		return members, nil
	}
	seen := make(map[uint]struct{}, len(userIDs))
	cleaned := make([]uint, 0, len(userIDs))
	for _, id := range userIDs {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		cleaned = append(cleaned, id)
	}
	if len(cleaned) == 0 {
		return members, nil
	}
	var rows []uint
	if err := s.DBForCtx(ctx).Model(&db.OrganizationMember{}).
		Where("organization_id = ? AND user_id IN ?", orgID, cleaned).
		Pluck("user_id", &rows).Error; err != nil {
		return nil, wrapErr(err)
	}
	for _, id := range rows {
		members[id] = struct{}{}
	}
	return members, nil
}

// IsOrgAdmin checks whether the user can administer teams for an organization.
// Site admins are always allowed. Otherwise the user must be an org owner.
func (s *Service) IsOrgAdmin(ctx context.Context, orgID, userID uint) (bool, error) {
	if orgID == 0 || userID == 0 {
		return false, nil
	}

	var user db.User
	if err := s.DBForCtx(ctx).Select("id", "site_admin").First(&user, "id = ?", userID).Error; err != nil {
		return false, wrapErr(err)
	}
	if user.SiteAdmin {
		return true, nil
	}

	var count int64
	err := s.orgMembershipQuery(ctx, orgID, userID).
		Where("role = ?", db.OrganizationRoleOwner).
		Count(&count).Error
	return count > 0, wrapErr(err)
}
