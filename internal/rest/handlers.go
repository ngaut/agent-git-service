// Package rest implements all GitHub REST API v3 handlers.
// Handlers are thin: decode request → call service → encode response via transform.
//
// The handlers are organized into domain-specific files:
//   - handlers.go           — Deps struct, meta, rate-limit, app/installations
//   - handlers_user.go      — /user, /users/{username}
//   - handlers_repo.go      — /repos, /orgs, branches, commits
//   - handlers_issue.go     — /issues, /issues/{number}/comments
//   - handlers_pr.go        — /pulls, /pulls/{number}/merge
//   - handlers_label.go     — /labels
//   - handlers_release.go   — /releases, /releases/assets (upload / download / archive)
//   - handlers_keys.go      — /user/keys, /user/gpg_keys, /repos/{owner}/{repo}/keys
//   - handlers_actions.go   — /actions/variables, /actions/secrets
//   - handlers_search.go    — /search/repositories, /search/issues
package rest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ngaut/agent-git-service/internal/authn"
	"github.com/ngaut/agent-git-service/internal/db"
	applog "github.com/ngaut/agent-git-service/internal/logging"
	"github.com/ngaut/agent-git-service/internal/ratelimit"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/service"
)

// mustIntParam extracts a numeric URL parameter and writes a 422 response
// if the value is missing or not a valid integer. Returns (0, false) on failure.
func mustIntParam(w http.ResponseWriter, r *http.Request, key string) (int, bool) {
	raw := pathParam(r, key)
	n, err := strconv.Atoi(raw)
	if err != nil {
		respond.ValidationFailed(w, key+" must be a number")
		return 0, false
	}
	return n, true
}

// mustIntParamAny extracts the first present numeric URL parameter from keys.
// This is useful for compatibility endpoints that historically used different
// wildcard names for the same path segment.
func mustIntParamAny(w http.ResponseWriter, r *http.Request, keys ...string) (int, bool) {
	for _, key := range keys {
		raw := pathParam(r, key)
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			respond.ValidationFailed(w, key+" must be a number")
			return 0, false
		}
		return n, true
	}
	if len(keys) == 0 {
		respond.ValidationFailed(w, "missing numeric path parameter")
		return 0, false
	}
	respond.ValidationFailed(w, keys[0]+" must be a number")
	return 0, false
}

// mustUintParam extracts a non-negative numeric URL parameter and writes a 422
// response on failure. Prefer this over mustIntParam followed by a uint cast —
// the cast silently wraps negative values on two's-complement systems.
func mustUintParam(w http.ResponseWriter, r *http.Request, key string) (uint, bool) {
	raw := pathParam(r, key)
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		respond.ValidationFailed(w, key+" must be a non-negative number")
		return 0, false
	}
	return uint(n), true
}

// decodeBodyStrict decodes JSON from the request body into dst, returning any error.
func decodeBodyStrict(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}

// decodeBodyStrictOptional decodes JSON from the request body into dst.
// An empty body is treated as success so handlers can preserve their default-value behavior.
func decodeBodyStrictOptional(r *http.Request, dst any) error {
	if err := decodeBodyStrict(r, dst); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func pathParam(r *http.Request, key string) string {
	raw := chi.URLParam(r, key)
	if r.URL.RawPath != "" {
		decoded, err := url.PathUnescape(raw)
		if err == nil {
			return decoded
		}
	}
	return raw
}

// repoFullName extracts the "owner/repo" path from the request URL params.
func repoFullName(r *http.Request) string {
	fullName := pathParam(r, "owner") + "/" + pathParam(r, "repo")
	if fullName != "/" {
		applog.AddAttrs(r.Context(), slog.String("repo", fullName))
	}
	return fullName
}

// Deps holds server-wide dependencies passed to handlers.
type Deps struct {
	Svc            *service.Service
	Router         authn.TokenResolver
	ConsoleBaseURL string
	Presence       *PresenceHandlers
}

// --- Meta ---

// GetMeta handles GET /api/v3/
func (d *Deps) GetMeta(w http.ResponseWriter, r *http.Request) {
	b := d.Svc.BaseURL
	apiBase := transform.APIPrefix()
	respond.JSON(w, 200, map[string]any{
		"current_user_url":                   b + apiBase + "/user",
		"repository_url":                     b + apiBase + "/repos/{owner}/{repo}",
		"user_url":                           b + apiBase + "/users/{user}",
		"organization_url":                   b + apiBase + "/orgs/{org}",
		"openapi_url":                        b + apiBase + "/openapi.json",
		"verifiable_password_authentication": true,
	})
}

// GetServerMeta handles GET /api/v3/meta (used by gh auth setup-git)
func (d *Deps) GetServerMeta(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, 200, map[string]any{
		"verifiable_password_authentication": true,
		"github_services_sha":                "3b6a86e2b43209da8e8f4e981bc50d8969c79e2a",
		"installed_version":                  "3.11.0",
		"hooks":                              []string{},
		"git":                                []string{"localhost"},
		"pages":                              []string{},
		"importer":                           []string{},
		"actions":                            []string{},
		"packages":                           []string{},
	})
}

// GetRateLimit handles GET /api/v3/rate_limit
func (d *Deps) GetRateLimit(w http.ResponseWriter, r *http.Request) {
	report := ratelimit.ReportForContext(r.Context(), time.Now().UTC())
	respond.JSON(w, 200, map[string]any{
		"resources": report.ResourcesBody(),
		"rate":      report.Rate.ResourceBody(),
	})
}

// --- Auth Helpers (used by gh auth setup-git) ---

// GetInstallations handles GET /api/v3/app/installations (gh checks this)
func (d *Deps) GetInstallations(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, 200, []any{})
}

// ─── Shared handler helpers ─────────────────────────────────────────────────

// mustGetRepo retrieves a repository by owner/repo URL params.
// Returns nil (and writes a 404 response) if the repo does not exist.
func (d *Deps) mustGetRepo(w http.ResponseWriter, r *http.Request) *db.Repository {
	full := repoFullName(r)
	repo, err := d.Svc.GetRepo(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return nil
	}
	return &repo
}

func (d *Deps) requireRepoPermission(w http.ResponseWriter, r *http.Request, repoID uint, required service.RepoPermission) bool {
	viewer, ok := service.UserFromContext(r.Context())
	if !ok {
		respond.NotFound(w)
		return false
	}
	perm, err := d.Svc.HasRepoAccess(r.Context(), repoID, viewer.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return false
	}
	if !perm.AtLeast(required) {
		respond.ServiceErrorRequest(r, w, service.ErrNotFound)
		return false
	}
	return true
}

func repoPermissionsFor(permission service.RepoPermission) transform.RepoPermissions {
	switch permission.Effective() {
	case service.RepoPermissionAdmin:
		return transform.RepoPermissions{
			Pull:     true,
			Triage:   true,
			Push:     true,
			Maintain: true,
			Admin:    true,
		}
	case service.RepoPermissionWrite:
		return transform.RepoPermissions{
			Pull: true,
			Push: true,
		}
	case service.RepoPermissionRead:
		return transform.RepoPermissions{
			Pull: true,
		}
	default:
		return transform.RepoPermissions{}
	}
}

func repoPermissionMapFor(permission service.RepoPermission) map[string]any {
	perms := repoPermissionsFor(permission)
	return map[string]any{
		"admin":    perms.Admin,
		"maintain": perms.Maintain,
		"push":     perms.Push,
		"triage":   perms.Triage,
		"pull":     perms.Pull,
	}
}

func (d *Deps) repoPermissionStats(ctx context.Context, repoID uint) transform.RepoStats {
	withPages := func(stats transform.RepoStats) transform.RepoStats {
		if _, err := d.Svc.GetPagesConfig(ctx, repoID); err == nil {
			stats.HasPages = true
		}
		return stats
	}
	viewer, ok := service.UserFromContext(ctx)
	if !ok || viewer.ID == 0 {
		return withPages(transform.RepoStats{
			HasPermissions: true,
			Permissions: transform.RepoPermissions{
				Pull:   true,
				Triage: true,
			},
		})
	}

	if perm, ok := d.Svc.CachedRepoPermission(ctx, repoID); ok {
		return withPages(transform.RepoStats{
			HasPermissions: true,
			Permissions:    repoPermissionsFor(perm),
		})
	}

	perm, err := d.Svc.HasRepoAccess(ctx, repoID, viewer.ID)
	if err != nil {
		logErr(ctx, "repo permissions", err)
		return withPages(transform.RepoStats{
			HasPermissions: true,
			Permissions:    transform.RepoPermissions{},
		})
	}

	return withPages(transform.RepoStats{
		HasPermissions: true,
		Permissions:    repoPermissionsFor(perm),
	})
}

// userResolver builds a per-request user lookup function for assignee expansion.
func (d *Deps) userResolver(ctx context.Context) transform.UserResolver {
	if d == nil || d.Svc == nil {
		return nil
	}
	cache := map[string]db.User{}
	return func(login string) (db.User, error) {
		if u, ok := cache[login]; ok {
			return u, nil
		}
		u, err := d.Svc.GetUser(ctx, login)
		if err != nil {
			return db.User{}, err
		}
		cache[login] = u
		return u, nil
	}
}

// batchUserResolver preloads users once and returns a resolver backed by that
// request-local map. Missing users intentionally fall back to transform stubs.
func (d *Deps) batchUserResolver(ctx context.Context, logins []string) transform.UserResolver {
	if d == nil || d.Svc == nil || len(logins) == 0 {
		return nil
	}
	users := d.Svc.GetUsersByLogins(ctx, logins)
	if len(users) == 0 {
		return nil
	}
	return func(login string) (db.User, error) {
		login = strings.TrimSpace(login)
		if login == "" {
			return db.User{}, service.ErrNotFound
		}
		if u, ok := users[login]; ok {
			return u, nil
		}
		return db.User{}, service.ErrNotFound
	}
}

// authorAssociationChecks builds per-request association checks for a repository.
func (d *Deps) authorAssociationChecks(ctx context.Context, repo db.Repository) transform.AuthorAssociationChecks {
	if d == nil || d.Svc == nil {
		return transform.AuthorAssociationChecks{}
	}

	var (
		collabLoaded bool
		collabIDs    map[uint]struct{}
	)
	collabCheck := func(userID uint) bool {
		if !collabLoaded {
			userIDs, err := d.Svc.ListCollaboratorUserIDs(ctx, repo.ID)
			if err != nil {
				logErr(ctx, "authorAssociation: list collaborators", err)
				collabLoaded = true
				return false
			}
			collabIDs = make(map[uint]struct{}, len(userIDs))
			for _, id := range userIDs {
				collabIDs[id] = struct{}{}
			}
			collabLoaded = true
		}
		_, ok := collabIDs[userID]
		return ok
	}

	var orgID uint
	if repo.Owner.Type == db.TypeOrganization {
		orgID = repo.OwnerID
	}
	var (
		memberCache = map[uint]bool{}
		memberCheck func(uint) bool
	)
	if orgID != 0 {
		memberCheck = func(userID uint) bool {
			if val, ok := memberCache[userID]; ok {
				return val
			}
			isMember, err := d.Svc.IsOrgMember(ctx, orgID, userID)
			if err != nil {
				logErr(ctx, "authorAssociation: org member", err)
				memberCache[userID] = false
				return false
			}
			memberCache[userID] = isMember
			return isMember
		}
	}

	return transform.AuthorAssociationChecks{
		IsCollaborator: collabCheck,
		IsOrgMember:    memberCheck,
	}
}

// mustGetOrg retrieves an org user by the {org} URL param.
// Returns nil (and writes a 404 response) if the org does not exist or is not an Organization.
func (d *Deps) mustGetOrg(w http.ResponseWriter, r *http.Request) *db.User {
	org := pathParam(r, "org")
	u, err := d.Svc.GetUser(r.Context(), org)
	if err != nil {
		respond.NotFound(w)
		return nil
	}
	// Only return org if it's an Organization type
	if u.Type != db.TypeOrganization {
		respond.NotFound(w)
		return nil
	}
	return &u
}

// logErr logs a non-nil error from a service call that would otherwise be swallowed.
func logErr(ctx context.Context, op string, err error, attrs ...any) {
	if err == nil || isContextCanceled(err) {
		return
	}
	attrs = append(attrs, "error", err)
	slog.ErrorContext(ctx, op, attrs...)
}

func isContextCanceled(err error) bool {
	return errors.Is(err, context.Canceled)
}

// decodeBody decodes JSON from the request body into dst.
// Logs non-EOF decode errors but does not reject the request,
// preserving PATCH/PUT partial-update behavior.
func decodeBody(r *http.Request, dst any) {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		slog.WarnContext(r.Context(), "decode body failed", "path", r.URL.Path, "error", err)
	}
}

// mustGetRepoByID retrieves a repository by its numeric {repo_id} URL param.
// Returns nil (and writes a 404 response) if the repo does not exist.
func (d *Deps) mustGetRepoByID(w http.ResponseWriter, r *http.Request) *db.Repository {
	idStr := pathParam(r, "repo_id")
	repo, err := d.Svc.GetRepoByID(r.Context(), idStr)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return nil
	}
	return &repo
}
