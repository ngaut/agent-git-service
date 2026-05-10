package rest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"gh-server/internal/db"
	"gh-server/internal/rest/respond"
	"gh-server/internal/rest/transform"
	"gh-server/internal/service"
)

// repoStats computes the RepoStats for a repository.
func (d *Deps) repoStats(r *http.Request, rep db.Repository) transform.RepoStats {
	ctx := r.Context()
	stats := d.repoPermissionStats(ctx, rep.ID)
	aggregates := d.Svc.LoadRepoAggregates(ctx, rep.ID)
	stats.ForksCount = aggregates.ForksCount
	stats.OpenIssuesCount = aggregates.OpenIssuesCount
	stats.StargazersCount = aggregates.StargazersCount
	stats.Size = d.Svc.RepoDiskUsageKB(ctx, rep)
	return stats
}

func createdRepoStats() transform.RepoStats {
	return transform.RepoStats{
		HasPermissions: true,
		Permissions:    repoPermissionsFor(service.RepoPermissionAdmin),
	}
}

type optionalStringPatch struct {
	Set   bool
	Value *string
}

func (p *optionalStringPatch) UnmarshalJSON(data []byte) error {
	p.Set = true
	if bytes.Equal(data, []byte("null")) {
		p.Value = nil
		return nil
	}
	var v string
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	p.Value = &v
	return nil
}

// --- Repo ---

// GetRepo handles GET /api/v3/repos/{owner}/{repo}
func (d *Deps) GetRepo(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	rep, err := d.Svc.GetRepo(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.Repo(rep, d.repoStats(r, rep)))
}

// CreateUserRepo handles POST /api/v3/user/repos
func (d *Deps) CreateUserRepo(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	d.createRepo(w, r, u.Login)
}

// CreateOrgRepo handles POST /api/v3/orgs/{org}/repos
func (d *Deps) CreateOrgRepo(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	if !d.requireOrgAdmin(w, r, org) {
		return
	}
	d.createRepo(w, r, org.Login)
}

func (d *Deps) createRepo(w http.ResponseWriter, r *http.Request, ownerLogin string) {
	var body struct {
		Name                string `json:"name"`
		Description         string `json:"description"`
		Visibility          string `json:"visibility"`
		Private             bool   `json:"private"`
		Homepage            string `json:"homepage"`
		HasIssues           *bool  `json:"has_issues"`
		HasProjects         *bool  `json:"has_projects"`
		HasWiki             *bool  `json:"has_wiki"`
		HasDownloads        *bool  `json:"has_downloads"`
		HasDiscussions      *bool  `json:"has_discussions"`
		IsTemplate          bool   `json:"is_template"`
		LicenseTemplate     string `json:"license_template"`
		AutoInit            bool   `json:"auto_init"`
		AddReadme           bool   `json:"add_readme"` // gh-server extension for --add-readme flag
		DefaultBranch       string `json:"default_branch"`
		AllowMergeCommit    *bool  `json:"allow_merge_commit"`
		AllowSquashMerge    *bool  `json:"allow_squash_merge"`
		AllowRebaseMerge    *bool  `json:"allow_rebase_merge"`
		AllowAutoMerge      *bool  `json:"allow_auto_merge"`
		DeleteBranchOnMerge *bool  `json:"delete_branch_on_merge"`
	}
	if err := decodeBodyStrict(r, &body); err != nil || body.Name == "" {
		respond.ValidationFailed(w, "name is required")
		return
	}
	visibility := strings.ToLower(strings.TrimSpace(body.Visibility))
	private := body.Private
	switch visibility {
	case "":
	case "public":
		private = false
	case "private":
		private = true
	case "internal":
		private = true
	default:
		respond.ValidationFailed(w, "visibility must be public, private, or internal")
		return
	}
	in := service.CreateRepoInput{
		OwnerLogin:          ownerLogin,
		Name:                body.Name,
		Description:         body.Description,
		Visibility:          visibility,
		Private:             private,
		Homepage:            body.Homepage,
		HasProjects:         body.HasProjects,
		HasDownloads:        body.HasDownloads,
		HasDiscussions:      body.HasDiscussions,
		IsTemplate:          body.IsTemplate,
		License:             body.LicenseTemplate,
		AutoInit:            body.AutoInit,
		AddReadme:           body.AddReadme,
		DefaultBranch:       body.DefaultBranch,
		AllowMergeCommit:    body.AllowMergeCommit,
		AllowSquashMerge:    body.AllowSquashMerge,
		AllowRebaseMerge:    body.AllowRebaseMerge,
		AllowAutoMerge:      body.AllowAutoMerge,
		DeleteBranchOnMerge: body.DeleteBranchOnMerge,
	}
	if body.HasIssues != nil {
		in.HasIssues = *body.HasIssues
		in.HasIssuesSet = true
	} else {
		in.HasIssues = true
	}
	if body.HasWiki != nil {
		in.HasWiki = *body.HasWiki
		in.HasWikiSet = true
	} else {
		in.HasWiki = true
	}
	rep, err := d.Svc.CreateRepo(r.Context(), in)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 201, transform.Repo(rep, createdRepoStats()))
}

// DeleteRepo handles DELETE /api/v3/repos/{owner}/{repo}
func (d *Deps) DeleteRepo(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	if err := d.Svc.DeleteRepo(r.Context(), full); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// UpdateRepo handles PATCH /api/v3/repos/{owner}/{repo}
func (d *Deps) UpdateRepo(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	var body struct {
		Name                *string             `json:"name"`
		Description         *string             `json:"description"`
		Private             *bool               `json:"private"`
		Homepage            optionalStringPatch `json:"homepage"`
		HasIssues           *bool               `json:"has_issues"`
		HasProjects         *bool               `json:"has_projects"`
		HasWiki             *bool               `json:"has_wiki"`
		HasDownloads        *bool               `json:"has_downloads"`
		HasDiscussions      *bool               `json:"has_discussions"`
		DefaultBranch       *string             `json:"default_branch"`
		Archived            *bool               `json:"archived"`
		AllowMergeCommit    *bool               `json:"allow_merge_commit"`
		AllowSquashMerge    *bool               `json:"allow_squash_merge"`
		AllowRebaseMerge    *bool               `json:"allow_rebase_merge"`
		AllowAutoMerge      *bool               `json:"allow_auto_merge"`
		AllowUpdateBranch   *bool               `json:"allow_update_branch"`
		DeleteBranchOnMerge *bool               `json:"delete_branch_on_merge"`
	}
	decodeBody(r, &body)
	in := service.UpdateRepoInput{
		Name:                body.Name,
		Description:         body.Description,
		Private:             body.Private,
		Homepage:            service.OptionalStringUpdate{Set: body.Homepage.Set, Value: body.Homepage.Value},
		HasIssues:           body.HasIssues,
		HasProjects:         body.HasProjects,
		HasWiki:             body.HasWiki,
		HasDownloads:        body.HasDownloads,
		HasDiscussions:      body.HasDiscussions,
		DefaultBranch:       body.DefaultBranch,
		Archived:            body.Archived,
		AllowMergeCommit:    body.AllowMergeCommit,
		AllowSquashMerge:    body.AllowSquashMerge,
		AllowRebaseMerge:    body.AllowRebaseMerge,
		AllowAutoMerge:      body.AllowAutoMerge,
		AllowUpdateBranch:   body.AllowUpdateBranch,
		DeleteBranchOnMerge: body.DeleteBranchOnMerge,
	}
	rep, err := d.Svc.UpdateRepo(r.Context(), full, in)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.Repo(rep, d.repoStats(r, rep)))
}

// ListOrgRepos handles GET /api/v3/orgs/{org}/repos
func (d *Deps) ListOrgRepos(w http.ResponseWriter, r *http.Request) {
	org := chi.URLParam(r, "org")
	repos, err := d.Svc.ListUserRepos(r.Context(), org)
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

// GetOrg handles GET /api/v3/orgs/{org}
// Returns 404 if the org does not exist or if the account is not an Organization.
func (d *Deps) GetOrg(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetUser(r.Context(), chi.URLParam(r, "org"))
	if err != nil {
		respond.NotFound(w)
		return
	}
	// Only return 200 for Organization accounts
	if u.Type != db.TypeOrganization {
		respond.NotFound(w)
		return
	}
	respond.JSON(w, 200, transform.User(u))
}

// GetRepoTopics handles GET /api/v3/repos/{owner}/{repo}/topics
func (d *Deps) GetRepoTopics(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	repo, err := d.Svc.GetRepo(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	topics := transform.RepoTopics(repo.Topics)
	respond.JSON(w, 200, map[string]any{"names": topics})
}

// ReplaceRepoTopics handles PUT /api/v3/repos/{owner}/{repo}/topics
func (d *Deps) ReplaceRepoTopics(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	var body struct {
		Names []string `json:"names"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	err := d.Svc.UpdateRepoTopics(r.Context(), full, strings.Join(body.Names, ","))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, map[string]any{"names": body.Names})
}

// GetRepoLanguages handles GET /api/v3/repos/{owner}/{repo}/languages
func (d *Deps) GetRepoLanguages(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if repo.Language == "" {
		respond.JSON(w, 200, map[string]any{})
		return
	}
	respond.JSON(w, 200, map[string]any{repo.Language: 1})
}

// ForkRepo handles POST /api/v3/repos/{owner}/{repo}/forks
func (d *Deps) ForkRepo(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	var in struct {
		Organization string `json:"organization"`
		Name         string `json:"name"`
	}
	if err := decodeBodyStrictOptional(r, &in); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}

	targetOwner := u.Login
	if in.Organization != "" {
		org, err := d.Svc.GetUser(r.Context(), in.Organization)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		if org.Type != db.TypeOrganization {
			respond.NotFound(w)
			return
		}
		if !d.requireOrgAdmin(w, r, &org) {
			return
		}
		targetOwner = in.Organization
	}

	fork, err := d.Svc.ForkRepo(r.Context(), full, targetOwner, in.Name)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 202, transform.Repo(fork, d.repoStats(r, fork)))
}

// ListRepoForks handles GET /api/v3/repos/{owner}/{repo}/forks
func (d *Deps) ListRepoForks(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	rep, err := d.Svc.GetRepo(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	forks, err := d.Svc.ListForks(r.Context(), rep.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(forks))
	for i, f := range forks {
		out[i] = transform.Repo(f, d.repoPermissionStats(r.Context(), f.ID))
	}
	respond.JSON(w, 200, out)
}

// TransferRepo handles POST /api/v3/repos/{owner}/{repo}/transfer
func (d *Deps) TransferRepo(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	var body struct {
		NewOwner string `json:"new_owner"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	if body.NewOwner == "" {
		respond.ValidationFailed(w, "new_owner is required")
		return
	}
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionAdmin) {
		return
	}
	target, err := d.Svc.GetUser(r.Context(), body.NewOwner)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if target.Type == db.TypeOrganization && !d.requireOrgAdmin(w, r, &target) {
		return
	}
	rep, err := d.Svc.TransferRepo(r.Context(), full, body.NewOwner)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 202, transform.Repo(rep, d.repoPermissionStats(r.Context(), rep.ID))) // GitHub usually returns 202 Accepted, but 200 is fine too. Using 202.
}
