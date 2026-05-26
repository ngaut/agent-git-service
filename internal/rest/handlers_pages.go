// Package rest — GitHub Pages REST endpoints.
package rest

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/service"
)

// GetPages handles GET /api/v3/repos/{owner}/{repo}/pages
func (d *Deps) GetPages(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	cfg, err := d.Svc.GetPagesConfig(r.Context(), repo.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.PagesConfig(full, cfg))
}

// EnablePages handles POST /api/v3/repos/{owner}/{repo}/pages
func (d *Deps) EnablePages(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionAdmin) {
		return
	}
	var body struct {
		Source struct {
			Branch string `json:"branch"`
			Path   string `json:"path"`
		} `json:"source"`
		BuildType     string `json:"build_type"`
		HTTPSEnforced bool   `json:"https_enforced"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	cfg, err := d.Svc.EnablePages(r.Context(), repo.ID, service.EnablePagesInput{
		Source:        service.PagesSource{Branch: body.Source.Branch, Path: body.Source.Path},
		BuildType:     body.BuildType,
		HTTPSEnforced: body.HTTPSEnforced,
	})
	if err != nil {
		if errors.Is(err, service.ErrConflict) {
			respond.Error(w, 409, "Pages is already enabled")
			return
		}
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 201, transform.PagesConfig(full, cfg))
}

// UpdatePages handles PUT /api/v3/repos/{owner}/{repo}/pages
func (d *Deps) UpdatePages(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionAdmin) {
		return
	}
	var body struct {
		CNAME         *string `json:"cname"`
		HTTPSEnforced *bool   `json:"https_enforced"`
		BuildType     *string `json:"build_type"`
		Source        *struct {
			Branch string `json:"branch"`
			Path   string `json:"path"`
		} `json:"source"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	in := service.UpdatePagesInput{
		CNAME:         body.CNAME,
		HTTPSEnforced: body.HTTPSEnforced,
		BuildType:     body.BuildType,
	}
	if body.Source != nil {
		in.Source = &service.PagesSource{Branch: body.Source.Branch, Path: body.Source.Path}
	}
	if err := d.Svc.UpdatePages(r.Context(), repo.ID, in); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// DisablePages handles DELETE /api/v3/repos/{owner}/{repo}/pages
func (d *Deps) DisablePages(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionAdmin) {
		return
	}
	if err := d.Svc.DisablePages(r.Context(), repo.ID); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// CreatePagesBuild handles POST /api/v3/repos/{owner}/{repo}/pages/builds
//
// v1 records the trigger but does not run a real build pipeline; the
// build row is created with status="queued" and stays there unless an
// external worker advances it.
func (d *Deps) CreatePagesBuild(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	var pusher, sha string
	if u, ok := service.UserFromContext(r.Context()); ok {
		pusher = u.Login
	}
	if d.Svc.Git != nil {
		if resolved, err := d.Svc.Git.HeadSHA(r.Context(), full, repo.DefaultBranch); err == nil {
			sha = resolved
		}
	}
	build, err := d.Svc.RecordPagesBuild(r.Context(), repo.ID, pusher, sha)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 201, transform.PagesBuild(full, build))
}

// ListPagesBuilds handles GET /api/v3/repos/{owner}/{repo}/pages/builds
func (d *Deps) ListPagesBuilds(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	// perPage 0 lets the service apply its default; service clamps invalid input.
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	builds, err := d.Svc.ListPagesBuilds(r.Context(), repo.ID, perPage)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(builds))
	for i, b := range builds {
		out[i] = transform.PagesBuild(full, b)
	}
	respond.JSON(w, 200, out)
}
