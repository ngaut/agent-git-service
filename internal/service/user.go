package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"gh-server/internal/db"

	"gorm.io/gorm"
)

const viewerRepoListQuery = `
SELECT
	result.repo_id,
	result.perm_level
FROM (
	SELECT
		perms.repo_id,
		perms.updated_at,
		CASE
			WHEN perms.site_admin THEN 3
			WHEN perms.owner_id = perms.user_id THEN 3
			WHEN perms.org_base_level >= perms.collab_level AND perms.org_base_level >= perms.team_level THEN perms.org_base_level
			WHEN perms.collab_level >= perms.team_level THEN perms.collab_level
			ELSE perms.team_level
		END AS perm_level
	FROM (
		SELECT
			r.id AS repo_id,
			r.updated_at AS updated_at,
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
		FROM repositories r
		JOIN users u ON u.id = ?
		JOIN users owner ON owner.id = r.owner_id
		LEFT JOIN collaborators c ON c.repository_id = r.id AND c.user_id = u.id
		LEFT JOIN team_members tm ON tm.user_id = u.id
		LEFT JOIN team_repositories tr ON tr.team_id = tm.team_id AND tr.repository_id = r.id
		LEFT JOIN organization_members om ON om.organization_id = r.owner_id AND om.user_id = u.id
		GROUP BY r.id, r.updated_at, u.id, u.site_admin, r.owner_id
	) AS perms
) AS result
WHERE result.perm_level > 0
ORDER BY result.updated_at DESC, result.repo_id DESC
LIMIT ?
`

// contextKey is an unexported type for context keys defined in this package.
type contextKey int

const ctxKeyUser contextKey = iota

// ContextWithUser returns a copy of ctx carrying the authenticated user.
func ContextWithUser(ctx context.Context, u db.User) context.Context {
	return context.WithValue(ctx, ctxKeyUser, u)
}

// UserFromContext extracts the authenticated user set by middleware.
// Returns the user and true if present, or the zero value and false otherwise.
func UserFromContext(ctx context.Context) (db.User, bool) {
	u, ok := ctx.Value(ctxKeyUser).(db.User)
	return u, ok
}

// ─── User & Organisation ───────────────────────────────────────────────────────

func (s *Service) bootstrapCreatedOrgTx(ctx context.Context, tx *gorm.DB, orgID uint, viewer db.User) error {
	if viewer.ID == 0 || viewer.Type == db.TypeOrganization {
		return nil
	}
	if _, err := ensureOrgMembershipTx(tx, orgID, viewer.ID, db.OrganizationRoleOwner); err != nil {
		return err
	}

	team, err := ensureAdminsTeamTx(tx, orgID)
	if err != nil {
		return err
	}
	if err := ensureAdminsTeamMemberTx(tx, team.ID, viewer.ID); err != nil {
		return err
	}

	if viewer.UserKind != db.UserKindAgent {
		return nil
	}

	humanID, ok, err := boundHumanIDForAgentQuery(tx, viewer.ID)
	if err != nil || !ok || humanID == 0 || humanID == viewer.ID {
		return err
	}
	if _, err := ensureOrgMembershipTx(tx, orgID, humanID, db.OrganizationRoleOwner); err != nil {
		return err
	}
	return ensureAdminsTeamMemberTx(tx, team.ID, humanID)
}

// EnsureOrg returns the org user, creating it if it doesn't exist. New runtime
// code should prefer CreateOrg for explicit org creation and use this helper for
// legacy/test setup only.
func (s *Service) EnsureOrg(ctx context.Context, login string) (db.User, error) {
	var u db.User
	err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&u, "login = ?", login).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			u = db.User{Login: login, Name: login, Type: db.TypeOrganization}
			if err := tx.Create(&u).Error; err != nil {
				// Handle race: another goroutine may have created it between First and Create.
				if tx.First(&u, "login = ?", login).Error == nil {
					return nil
				}
				return err
			}
			if viewer, ok := UserFromContext(ctx); ok {
				if err := s.bootstrapCreatedOrgTx(ctx, tx, u.ID, viewer); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return db.User{}, wrapErr(err)
	}
	return u, nil
}

// CreateOrg explicitly creates a new organization and records the creator as an owner.
func (s *Service) CreateOrg(ctx context.Context, login, name, defaultRepositoryPermission string) (db.User, error) {
	viewer, ok := UserFromContext(ctx)
	if !ok || viewer.ID == 0 || viewer.Type == db.TypeOrganization {
		return db.User{}, ErrUnauthorized
	}
	defaultRepositoryPermission, ok = NormalizeOrganizationBasePermission(defaultRepositoryPermission)
	if !ok {
		return db.User{}, fmt.Errorf("%w: %s", ErrValidation, OrganizationBasePermissionValidationMessage)
	}
	if name == "" {
		name = login
	}

	var org db.User
	err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		var existing db.User
		if err := tx.First(&existing, "login = ?", login).Error; err == nil {
			return ErrConflict
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		org = db.User{
			Login:                       login,
			Name:                        name,
			Type:                        db.TypeOrganization,
			DefaultRepositoryPermission: defaultRepositoryPermission,
		}
		if err := tx.Create(&org).Error; err != nil {
			if isDuplicateErr(err) {
				return ErrConflict
			}
			return err
		}
		return s.bootstrapCreatedOrgTx(ctx, tx, org.ID, viewer)
	})
	if err != nil {
		return db.User{}, wrapErr(err)
	}
	return org, nil
}

// GetUser returns a user by login.
func (s *Service) GetUser(ctx context.Context, login string) (db.User, error) {
	var u db.User
	err := s.DBForCtx(ctx).First(&u, "login = ?", login).Error
	if err != nil {
		return u, wrapErr(err)
	}
	return u, nil
}

// GetUsersByLogins resolves many logins in a single round-trip. Returns a map
// keyed by login for direct lookup; logins that don't match any row are
// absent from the result rather than being reported as an error, so callers
// iterating a user-supplied list can decide per-element how to present the
// gap (e.g. REST emits a stub User; GraphQL emits a placeholder node). Empty
// and whitespace logins are filtered before querying.
func (s *Service) GetUsersByLogins(ctx context.Context, logins []string) map[string]db.User {
	if len(logins) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(logins))
	cleaned := make([]string, 0, len(logins))
	for _, l := range logins {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if _, dup := seen[l]; dup {
			continue
		}
		seen[l] = struct{}{}
		cleaned = append(cleaned, l)
	}
	if len(cleaned) == 0 {
		return nil
	}
	var rows []db.User
	if err := s.DBForCtx(ctx).Where("login IN ?", cleaned).Find(&rows).Error; err != nil {
		slog.WarnContext(ctx, "GetUsersByLogins", "error", err, "logins", len(cleaned))
		return nil
	}
	out := make(map[string]db.User, len(rows))
	for _, u := range rows {
		out[u.Login] = u
	}
	return out
}

// GetUserByID returns a user by their numeric DB ID (as a string, e.g. "42").
func (s *Service) GetUserByID(ctx context.Context, idStr string) (db.User, error) {
	var u db.User
	err := s.DBForCtx(ctx).First(&u, "id = ?", idStr).Error
	if err != nil {
		return u, wrapErr(err)
	}
	return u, nil
}

// GetCurrentUser returns the authenticated user from the request context.
// If no user was injected by middleware (e.g. internal/GraphQL calls), it falls
// back to the first admin user for backward compatibility.
func (s *Service) GetCurrentUser(ctx context.Context) (db.User, error) {
	if u, ok := UserFromContext(ctx); ok {
		return u, nil
	}
	// Fallback: first admin user (backward compat for GraphQL/internal calls).
	var u db.User
	err := s.DBForCtx(ctx).First(&u, "type = ? AND site_admin = ?", db.TypeUser, true).Error
	return u, err
}

// GetActiveSiteAdmin returns the first site admin user eligible for legacy
// local-dev OAuth flows.
func (s *Service) GetActiveSiteAdmin(ctx context.Context) (db.User, error) {
	var u db.User
	if err := s.DBForCtx(ctx).
		Where("type = ? AND site_admin = ?", db.TypeUser, true).
		Order("id ASC").
		First(&u).Error; err != nil {
		return db.User{}, wrapErr(err)
	}
	if !isUserStatusActive(u.Status) {
		return db.User{}, ErrForbidden
	}
	return u, nil
}

// ListOrgs returns all organisation-type users that the authenticated user belongs to.
func (s *Service) ListOrgs(ctx context.Context) ([]db.User, error) {
	viewer, ok := UserFromContext(ctx)
	if !ok {
		return []db.User{}, nil
	}

	// Site admins can see all orgs
	if viewer.SiteAdmin {
		var orgs []db.User
		err := s.DBForCtx(ctx).Where("type = ?", db.TypeOrganization).Find(&orgs).Error
		return orgs, err
	}

	return s.ListOrgsForUser(ctx, viewer.ID)
}

// ListOrgsForUser returns organization accounts that the given user explicitly belongs to.
func (s *Service) ListOrgsForUser(ctx context.Context, userID uint) ([]db.User, error) {
	if userID == 0 {
		return []db.User{}, nil
	}

	var orgs []db.User
	err := s.DBForCtx(ctx).
		Table("users").
		Joins("JOIN organization_members ON organization_members.organization_id = users.id").
		Where("users.type = ? AND organization_members.user_id = ?", db.TypeOrganization, userID).
		Distinct().
		Find(&orgs).Error
	return orgs, err
}

// ListAllUsers returns all non-org users. Used by the GraphQL repo/assignable shape.
func (s *Service) ListAllUsers(ctx context.Context) ([]db.User, error) {
	var users []db.User
	err := s.DBForCtx(ctx).Where("type = ?", db.TypeUser).Find(&users).Error
	return users, err
}

type userSearchQuery struct {
	terms                 []string
	accountType           string
	fields                []string
	hasUnsupportedFilters bool
}

func parseUserSearchQuery(query string) userSearchQuery {
	var sq userSearchQuery
	for _, token := range strings.Fields(query) {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}

		inner := token
		negated := false
		if strings.HasPrefix(inner, "-") && len(inner) > 1 && strings.Contains(inner, ":") {
			negated = true
			inner = inner[1:]
		}

		parts := strings.SplitN(inner, ":", 2)
		if len(parts) != 2 {
			if !negated {
				sq.terms = append(sq.terms, strings.Trim(token, `"`))
			}
			continue
		}

		key := strings.ToLower(parts[0])
		value := strings.Trim(parts[1], `"`)
		if negated {
			continue
		}
		switch key {
		case "type":
			sq.accountType = strings.ToLower(value)
		case "in":
			for _, field := range strings.Split(value, ",") {
				field = strings.ToLower(strings.TrimSpace(field))
				if field != "" {
					sq.fields = append(sq.fields, field)
				}
			}
		case "followers", "repos", "repositories", "language", "location", "created":
			// Recognized GitHub user-search qualifiers that are not modeled locally.
			// Return an empty result set rather than broadening the search.
			sq.hasUnsupportedFilters = true
			continue
		default:
			sq.terms = append(sq.terms, strings.Trim(token, `"`))
		}
	}
	return sq
}

func userSearchColumns(fields []string) []string {
	if len(fields) == 0 {
		return []string{"login", "name", "email", "bio"}
	}
	seen := map[string]struct{}{}
	cols := make([]string, 0, len(fields))
	for _, field := range fields {
		var col string
		switch strings.ToLower(strings.TrimSpace(field)) {
		case "login":
			col = "login"
		case "fullname", "name":
			col = "name"
		case "email":
			col = "email"
		case "bio":
			col = "bio"
		default:
			continue
		}
		if _, ok := seen[col]; ok {
			continue
		}
		seen[col] = struct{}{}
		cols = append(cols, col)
	}
	if len(cols) == 0 {
		return []string{"login", "name", "email", "bio"}
	}
	return cols
}

// SearchUsers searches user and organization accounts using GitHub's
// /search/users envelope semantics. It supports core text matching plus the
// high-value type:user/type:org and in: qualifiers; ranking/count qualifiers are
// intentionally approximated until those data sources are modeled.
func (s *Service) SearchUsers(ctx context.Context, query, sort, order string) ([]db.User, error) {
	sq := parseUserSearchQuery(query)
	if sq.hasUnsupportedFilters {
		return []db.User{}, nil
	}
	dbq := s.DBForCtx(ctx).Model(&db.User{})

	switch sq.accountType {
	case "", "all":
	case "user":
		dbq = dbq.Where("type = ?", db.TypeUser)
	case "org", "organization":
		dbq = dbq.Where("type = ?", db.TypeOrganization)
	default:
		return []db.User{}, nil
	}

	cols := userSearchColumns(sq.fields)
	for _, term := range sq.terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		like := "%" + strings.ToLower(escapeLike(term)) + "%"
		clauses := make([]string, 0, len(cols))
		args := make([]any, 0, len(cols))
		for _, col := range cols {
			clauses = append(clauses, "LOWER("+col+") LIKE ?")
			args = append(args, like)
		}
		dbq = dbq.Where("("+strings.Join(clauses, " OR ")+")", args...)
	}

	dir := "desc"
	if strings.EqualFold(order, "asc") {
		dir = "asc"
	}
	switch strings.ToLower(strings.TrimSpace(sort)) {
	case "joined":
		dbq = dbq.Order("created_at " + dir).Order("login asc")
	default:
		dbq = dbq.Order("login asc")
	}

	var users []db.User
	if err := dbq.Limit(defaultListLimit).Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

// ListUserRepos returns all repositories owned by the user with the given login.
func (s *Service) ListUserRepos(ctx context.Context, login string) ([]db.Repository, error) {
	u, err := s.GetUser(ctx, login)
	if err != nil {
		return nil, err
	}
	var repos []db.Repository
	if err := preloadRepoMinimal(s.DBForCtx(ctx)).Where("owner_id = ?", u.ID).Order("updated_at DESC").Limit(defaultListLimit).Find(&repos).Error; err != nil {
		return nil, err
	}
	return repos, nil
}

// ListViewerRepos returns all repositories visible to the authenticated viewer,
// along with the viewer's effective permission on each repository.
func (s *Service) ListViewerRepos(ctx context.Context) ([]RepoWithPermission, error) {
	viewer, err := s.GetCurrentUser(ctx)
	if err != nil {
		return nil, err
	}

	type viewerRepoRow struct {
		RepoID    uint
		PermLevel int
	}

	var rows []viewerRepoRow
	if err := s.DBForCtx(ctx).Raw(viewerRepoListQuery, viewer.ID, defaultListLimit).Scan(&rows).Error; err != nil {
		if isMissingTableErr(err) {
			return s.listViewerReposFallback(ctx, viewer)
		}
		return nil, err
	}
	if len(rows) == 0 {
		return []RepoWithPermission{}, nil
	}

	repoIDs := make([]uint, 0, len(rows))
	for _, row := range rows {
		repoIDs = append(repoIDs, row.RepoID)
	}

	var repos []db.Repository
	if err := preloadRepoMinimal(s.DBForCtx(ctx)).
		Where("id IN ?", repoIDs).
		Find(&repos).Error; err != nil {
		return nil, err
	}

	reposByID := make(map[uint]db.Repository, len(repos))
	for _, repo := range repos {
		reposByID[repo.ID] = repo
	}

	out := make([]RepoWithPermission, 0, len(rows))
	for _, row := range rows {
		repo, ok := reposByID[row.RepoID]
		if !ok {
			continue
		}
		out = append(out, RepoWithPermission{
			Repository: repo,
			Permission: repoPermissionFromLevel(row.PermLevel),
		})
	}
	return out, nil
}

func (s *Service) listViewerReposFallback(ctx context.Context, viewer db.User) ([]RepoWithPermission, error) {
	type fallbackRow struct {
		RepoID     uint
		Permission RepoPermission
	}

	rowsByID := map[uint]fallbackRow{}

	appendRows := func(repoIDs []uint, permission RepoPermission) {
		permission = permission.Effective()
		if permission == RepoPermissionNone {
			return
		}
		for _, repoID := range repoIDs {
			existing, ok := rowsByID[repoID]
			if ok && existing.Permission.AtLeast(permission) {
				continue
			}
			rowsByID[repoID] = fallbackRow{RepoID: repoID, Permission: permission}
		}
	}

	if viewer.SiteAdmin {
		var repoIDs []uint
		if err := s.DBForCtx(ctx).
			Model(&db.Repository{}).
			Order("updated_at DESC").
			Limit(defaultListLimit).
			Pluck("id", &repoIDs).Error; err != nil {
			return nil, err
		}
		appendRows(repoIDs, RepoPermissionAdmin)
	} else {
		var ownerRepoIDs []uint
		if err := s.DBForCtx(ctx).
			Model(&db.Repository{}).
			Where("owner_id = ?", viewer.ID).
			Order("updated_at DESC").
			Limit(defaultListLimit).
			Pluck("id", &ownerRepoIDs).Error; err != nil {
			return nil, err
		}
		appendRows(ownerRepoIDs, RepoPermissionAdmin)

		type collaboratorRow struct {
			RepositoryID uint
			Permission   string
		}

		var collabs []collaboratorRow
		if err := s.DBForCtx(ctx).
			Model(&db.Collaborator{}).
			Select("repository_id", "permission").
			Where("user_id = ?", viewer.ID).
			Limit(defaultListLimit).
			Scan(&collabs).Error; err == nil {
			for _, collab := range collabs {
				appendRows([]uint{collab.RepositoryID}, ParseRepoPermission(collab.Permission))
			}
		} else if !isMissingTableErr(err) {
			return nil, err
		}

		type teamGrantRow struct {
			RepositoryID uint
			Permission   string
		}

		var teamGrants []teamGrantRow
		if err := s.DBForCtx(ctx).
			Table("team_members").
			Select("team_repositories.repository_id", "team_repositories.permission").
			Joins("JOIN team_repositories ON team_repositories.team_id = team_members.team_id").
			Where("team_members.user_id = ?", viewer.ID).
			Limit(defaultListLimit).
			Scan(&teamGrants).Error; err == nil {
			for _, grant := range teamGrants {
				appendRows([]uint{grant.RepositoryID}, ParseRepoPermission(grant.Permission))
			}
		} else if !isMissingTableErr(err) {
			return nil, err
		}

		type orgBaseGrantRow struct {
			RepositoryID uint
			Permission   string
		}

		var orgBaseGrants []orgBaseGrantRow
		if err := s.DBForCtx(ctx).
			Table("organization_members").
			Select("repositories.id AS repository_id", "users.default_repository_permission AS permission").
			Joins("JOIN users ON users.id = organization_members.organization_id").
			Joins("JOIN repositories ON repositories.owner_id = users.id").
			Where("organization_members.user_id = ? AND users.type = ?", viewer.ID, db.TypeOrganization).
			Limit(defaultListLimit).
			Scan(&orgBaseGrants).Error; err == nil {
			for _, grant := range orgBaseGrants {
				appendRows([]uint{grant.RepositoryID}, ParseRepoPermission(grant.Permission))
			}
		} else if !isMissingTableErr(err) {
			return nil, err
		}
	}

	if len(rowsByID) == 0 {
		return []RepoWithPermission{}, nil
	}

	repoIDs := make([]uint, 0, len(rowsByID))
	for repoID := range rowsByID {
		repoIDs = append(repoIDs, repoID)
	}

	var repos []db.Repository
	if err := preloadRepoMinimal(s.DBForCtx(ctx)).
		Where("id IN ?", repoIDs).
		Order("updated_at DESC").
		Find(&repos).Error; err != nil {
		return nil, err
	}

	out := make([]RepoWithPermission, 0, len(repos))
	for _, repo := range repos {
		row := rowsByID[repo.ID]
		out = append(out, RepoWithPermission{
			Repository: repo,
			Permission: row.Permission,
		})
	}
	return out, nil
}
