package rest

import (
	"context"
	"net/http"
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/service"
)

// --- Auth / User ---

// GetAuthenticatedUser handles GET /api/v3/user
func (d *Deps) GetAuthenticatedUser(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.UserPrivate(u))
}

// GetUser handles GET /api/v3/users/{username}
func (d *Deps) GetUser(w http.ResponseWriter, r *http.Request) {
	login := pathParam(r, "username")
	u, err := d.Svc.GetUser(r.Context(), login)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.User(u))
}

// ListUserRepos handles GET /api/v3/users/{username}/repos and
// GET /api/v3/user/repos (when the username param is absent, falls back to
// the authenticated user).
func (d *Deps) ListUserRepos(w http.ResponseWriter, r *http.Request) {
	login := pathParam(r, "username")
	if login == "" {
		repos, err := d.Svc.ListViewerRepos(r.Context())
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		out := make([]any, len(repos))
		for i, repo := range repos {
			out[i] = transform.Repo(repo.Repository, transform.RepoStats{
				HasPermissions: true,
				Permissions:    repoPermissionsFor(repo.Permission),
			})
		}
		respond.JSON(w, 200, out)
		return
	}
	repos, err := d.Svc.ListUserRepos(r.Context(), login)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(repos))
	for i, rep := range repos {
		out[i] = transform.Repo(rep, d.repoPermissionStats(r.Context(), rep.ID))
	}
	respond.JSON(w, 200, out)
}

// ListUserOrgs handles GET /api/v3/user/orgs
func (d *Deps) ListUserOrgs(w http.ResponseWriter, r *http.Request) {
	orgs, err := d.Svc.ListOrgs(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(orgs))
	for i, o := range orgs {
		out[i] = transform.User(o)
	}
	respond.JSON(w, 200, out)
}

// CreateUserOrg handles POST /api/v3/user/orgs
func (d *Deps) CreateUserOrg(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Login                       string `json:"login"`
		Name                        string `json:"name"`
		DefaultRepositoryPermission string `json:"default_repository_permission"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "login is required")
		return
	}
	body.Login = strings.TrimSpace(body.Login)
	body.Name = strings.TrimSpace(body.Name)
	if body.Login == "" {
		respond.ValidationFailed(w, "login is required")
		return
	}
	if _, ok := service.NormalizeOrganizationBasePermission(body.DefaultRepositoryPermission); !ok {
		respond.ValidationFailed(w, service.OrganizationBasePermissionValidationMessage)
		return
	}

	org, err := d.Svc.CreateOrg(r.Context(), body.Login, body.Name, body.DefaultRepositoryPermission)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusCreated, transform.User(org))
}

// ListStarredRepos handles GET /api/v3/user/starred
func (d *Deps) ListStarredRepos(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	repos, err := d.Svc.ListStarred(r.Context(), u.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(repos))
	for i, rep := range repos {
		out[i] = transform.Repo(rep, d.repoPermissionStats(r.Context(), rep.ID))
	}
	respond.JSON(w, 200, out)
}

// ListUserStarredRepos handles GET /api/v3/users/{username}/starred.
func (d *Deps) ListUserStarredRepos(w http.ResponseWriter, r *http.Request) {
	login := pathParam(r, "username")
	u, err := d.Svc.GetUser(r.Context(), login)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	repos, err := d.Svc.ListStarred(r.Context(), u.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	viewer, hasViewer := service.UserFromContext(r.Context())
	if hasViewer && viewer.ID == u.ID {
		out := make([]any, len(repos))
		for i, rep := range repos {
			out[i] = transform.Repo(rep, d.repoPermissionStats(r.Context(), rep.ID))
		}
		respond.JSON(w, 200, out)
		return
	}
	out := make([]any, 0, len(repos))
	for _, rep := range repos {
		if !d.canViewRepository(r.Context(), rep.ID, viewer.ID, hasViewer) {
			continue
		}
		out = append(out, transform.Repo(rep, d.repoPermissionStats(r.Context(), rep.ID)))
	}
	respond.JSON(w, 200, out)
}

func (d *Deps) canViewRepository(ctx context.Context, repoID, viewerID uint, hasViewer bool) bool {
	if hasViewer {
		perm, err := d.Svc.HasRepoAccess(ctx, repoID, viewerID)
		if err == nil && perm.AtLeast(service.RepoPermissionRead) {
			return true
		}
	}
	return d.Svc.IsRepoPublic(ctx, repoID)
}

// StarRepo handles PUT /api/v3/user/starred/{owner}/{repo}
func (d *Deps) StarRepo(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	full := repoFullName(r)
	if err := d.Svc.StarRepo(r.Context(), full, u.Login); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// UnstarRepo handles DELETE /api/v3/user/starred/{owner}/{repo}
func (d *Deps) UnstarRepo(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	full := repoFullName(r)
	if err := d.Svc.UnstarRepo(r.Context(), full, u.Login); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// IsRepoStarred handles GET /api/v3/user/starred/{owner}/{repo}
func (d *Deps) IsRepoStarred(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	full := repoFullName(r)
	starred, err := d.Svc.IsStarred(r.Context(), u.ID, full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if starred {
		respond.NoContent(w)
	} else {
		respond.NotFound(w)
	}
}

// ListCollaborators handles GET /api/v3/repos/{owner}/{repo}/collaborators
func (d *Deps) ListCollaborators(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	rep, err := d.Svc.GetRepo(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	collabs, err := d.Svc.ListCollaborators(r.Context(), rep.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	var out []any

	// Always include the repository owner as an admin collaborator
	ownerMap := transform.User(rep.Owner)
	ownerMap["permissions"] = repoPermissionMapFor(service.RepoPermissionAdmin)
	out = append(out, ownerMap)

	for _, c := range collabs {
		userMap := transform.User(c.User)
		userMap["permissions"] = repoPermissionMapFor(service.ParseRepoPermission(c.Permission))
		if rep.Owner.Type == db.TypeOrganization {
			isMember, err := d.Svc.IsOrgMember(r.Context(), rep.OwnerID, c.UserID)
			if err != nil {
				respond.ServiceErrorRequest(r, w, err)
				return
			}
			isOutside, err := d.Svc.IsOutsideCollaborator(r.Context(), rep.OwnerID, c.UserID)
			if err != nil {
				respond.ServiceErrorRequest(r, w, err)
				return
			}
			userMap["organization_member"] = isMember
			userMap["outside_collaborator"] = isOutside
		}
		out = append(out, userMap)
	}

	respond.JSON(w, 200, out)
}
