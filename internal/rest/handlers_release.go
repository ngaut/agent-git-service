package rest

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"gh-server/internal/rest/respond"
	"gh-server/internal/rest/transform"
	"gh-server/internal/service"
)

// --- Releases ---

// ListReleases handles GET /api/v3/repos/{owner}/{repo}/releases
func (d *Deps) ListReleases(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	page, perPage := parsePagination(r)
	releases, err := d.Svc.ListReleases(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(releases))
	for i, rel := range releases {
		out[i] = transform.Release(rel)
	}
	respond.JSON(w, 200, paginate(w, r, d.Svc.BaseURL, out, page, perPage))
}

// CreateRelease handles POST /api/v3/repos/{owner}/{repo}/releases
func (d *Deps) CreateRelease(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	var body struct {
		TagName              string `json:"tag_name"`
		TargetCommitish      string `json:"target_commitish"`
		Name                 string `json:"name"`
		Body                 string `json:"body"`
		Draft                bool   `json:"draft"`
		PreRelease           bool   `json:"prerelease"`
		GenerateReleaseNotes bool   `json:"generate_release_notes"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	if body.GenerateReleaseNotes && body.Body == "" {
		_, notes, err := d.Svc.GenerateReleaseNotes(r.Context(), full, body.TagName, "")
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		body.Body = notes
	}
	rel, err := d.Svc.CreateRelease(r.Context(), full, service.CreateReleaseInput{
		TagName:         body.TagName,
		Name:            body.Name,
		Body:            body.Body,
		Draft:           body.Draft,
		PreRelease:      body.PreRelease,
		TargetCommitish: body.TargetCommitish,
	})
	if err != nil {
		respond.ValidationFailed(w, err.Error())
		return
	}
	respond.JSON(w, 201, transform.Release(rel))
}

// GetRelease handles GET /api/v3/repos/{owner}/{repo}/releases/{release_id}
func (d *Deps) GetRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "release_id")
	if !ok {
		return
	}
	rel, err := d.Svc.GetRelease(r.Context(), uint(id))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.Release(rel))
}

// DeleteRelease handles DELETE /api/v3/repos/{owner}/{repo}/releases/{release_id}
func (d *Deps) DeleteRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "release_id")
	if !ok {
		return
	}
	if err := d.Svc.DeleteRelease(r.Context(), uint(id)); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// GetReleaseByTag handles GET /api/v3/repos/{owner}/{repo}/releases/tags/{tag}
func (d *Deps) GetReleaseByTag(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	tag := chi.URLParam(r, "tag")
	rel, err := d.Svc.GetReleaseByTag(r.Context(), full, tag)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.Release(rel))
}

// GetLatestRelease handles GET /api/v3/repos/{owner}/{repo}/releases/latest
func (d *Deps) GetLatestRelease(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	rel, err := d.Svc.GetLatestRelease(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.Release(rel))
}

// UpdateRelease handles PATCH /api/v3/repos/{owner}/{repo}/releases/{release_id}
func (d *Deps) UpdateRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "release_id")
	if !ok {
		return
	}
	var body struct {
		TagName    *string `json:"tag_name"`
		Name       *string `json:"name"`
		Body       *string `json:"body"`
		Draft      *bool   `json:"draft"`
		PreRelease *bool   `json:"prerelease"`
	}
	decodeBody(r, &body)
	rel, err := d.Svc.UpdateRelease(r.Context(), uint(id), service.UpdateReleaseInput{
		TagName: body.TagName, Name: body.Name, Body: body.Body, Draft: body.Draft, PreRelease: body.PreRelease,
	})
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.Release(rel))
}

// HeadReleaseByTag handles HEAD /api/v3/repos/{owner}/{repo}/releases/tags/{tag}
func (d *Deps) HeadReleaseByTag(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	tag := chi.URLParam(r, "tag")
	_, err := d.Svc.GetReleaseByTag(r.Context(), full, tag)
	if err != nil {
		w.WriteHeader(404)
		return
	}
	w.WriteHeader(200)
}

// GenerateReleaseNotes handles POST /api/v3/repos/{owner}/{repo}/releases/generate-notes
func (d *Deps) GenerateReleaseNotes(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	var body struct {
		TagName         string `json:"tag_name"`
		PreviousTagName string `json:"previous_tag_name"`
	}
	decodeBody(r, &body)
	name, notes, err := d.Svc.GenerateReleaseNotes(r.Context(), full, body.TagName, body.PreviousTagName)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, map[string]any{
		"name": name,
		"body": notes,
	})
}

// DownloadArchiveByTag handles GET /api/v3/repos/{owner}/{repo}/archive/refs/tags/{tagfile}
// e.g., /repos/testorg/myrepo/archive/refs/tags/v1.2.3.zip
func (d *Deps) DownloadArchiveByTag(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")
	tagfile := chi.URLParam(r, "tagfile") // e.g., "v1.2.3.zip" or "v1.2.3.tar.gz"
	fullName := owner + "/" + repo

	var tag, format string
	switch {
	case strings.HasSuffix(tagfile, ".tar.gz"):
		tag = strings.TrimSuffix(tagfile, ".tar.gz")
		format = "tar.gz"
	case strings.HasSuffix(tagfile, ".zip"):
		tag = strings.TrimSuffix(tagfile, ".zip")
		format = "zip"
	default:
		tag = tagfile
		format = "zip"
	}

	tags, err := d.Svc.Git.ListTags(r.Context(), fullName)
	if err != nil {
		respond.NotFound(w)
		return
	}
	tagFound := false
	for _, t := range tags {
		if t.Name == tag {
			tagFound = true
			break
		}
	}
	if !tagFound {
		respond.NotFound(w)
		return
	}

	repoName := repo + "-" + strings.TrimPrefix(tag, "v")

	if format == "zip" {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", "attachment; filename="+repoName+".zip")
	} else {
		w.Header().Set("Content-Type", "application/x-gzip")
		w.Header().Set("Content-Disposition", "attachment; filename="+repoName+".tar.gz")
	}

	w.WriteHeader(http.StatusOK)
	_ = d.Svc.Git.Archive(r.Context(), fullName, format, tag, repoName, w)
}

// UploadReleaseAsset handles POST /api/v3/repos/{owner}/{repo}/releases/{release_id}/assets
func (d *Deps) UploadReleaseAsset(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	releaseID, ok := mustIntParam(w, r, "release_id")
	if !ok {
		return
	}
	name := r.URL.Query().Get("name")
	label := r.URL.Query().Get("label")
	if name == "" {
		respond.ValidationFailed(w, "name is required")
		return
	}
	asset, err := d.Svc.UploadReleaseAsset(r.Context(), uint(releaseID), name, label, r.Header.Get("Content-Type"), r.Body)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 201, transform.ReleaseAsset(asset, full))
}

// GetReleaseAsset handles GET /api/v3/repos/{owner}/{repo}/releases/assets/{asset_id}
func (d *Deps) GetReleaseAsset(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	assetID, ok := mustIntParam(w, r, "asset_id")
	if !ok {
		return
	}
	asset, err := d.Svc.GetReleaseAsset(r.Context(), uint(assetID))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	// gh release download sends Accept: application/octet-stream to download binary content.
	if strings.Contains(r.Header.Get("Accept"), "application/octet-stream") {
		ct := asset.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, strings.ReplaceAll(asset.Name, `"`, `\"`)))
		w.WriteHeader(200)
		_, _ = w.Write(asset.Content)
		return
	}
	respond.JSON(w, 200, transform.ReleaseAsset(asset, full))
}

// DownloadReleaseAssetContent handles GET /api/v3/repos/{owner}/{repo}/releases/assets/{asset_id}/download
func (d *Deps) DownloadReleaseAssetContent(w http.ResponseWriter, r *http.Request) {
	assetID, ok := mustIntParam(w, r, "asset_id")
	if !ok {
		return
	}
	asset, err := d.Svc.GetReleaseAsset(r.Context(), uint(assetID))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	ct := asset.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, strings.ReplaceAll(asset.Name, `"`, `\"`)))
	w.WriteHeader(200)
	_, _ = w.Write(asset.Content)
}

// DownloadReleaseArchive handles GET /api/v3/repos/{owner}/{repo}/releases/{release_id}/archive/{format}
func (d *Deps) DownloadReleaseArchive(w http.ResponseWriter, r *http.Request) {
	repo := chi.URLParam(r, "repo")
	owner := chi.URLParam(r, "owner")
	fullName := owner + "/" + repo
	format := chi.URLParam(r, "format") // zipball or tarball
	releaseID, ok := mustIntParam(w, r, "release_id")
	if !ok {
		return
	}
	rel, err := d.Svc.GetReleaseForArchive(r.Context(), uint(releaseID))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	gitFormat := format
	if format == "zipball" {
		gitFormat = "zip"
	} else {
		gitFormat = "tar.gz"
	}

	repoName := repo + "-" + strings.TrimPrefix(rel.TagName, "v")

	if gitFormat == "zip" {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", "attachment; filename="+repoName+".zip")
	} else {
		w.Header().Set("Content-Type", "application/x-gzip")
		w.Header().Set("Content-Disposition", "attachment; filename="+repoName+".tar.gz")
	}

	w.WriteHeader(http.StatusOK)
	_ = d.Svc.Git.Archive(r.Context(), fullName, gitFormat, rel.TagName, repoName, w)
}

// ListReleaseAssets handles GET /api/v3/repos/{owner}/{repo}/releases/{release_id}/assets
func (d *Deps) ListReleaseAssets(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	page, perPage := parsePagination(r)
	releaseID, ok := mustIntParam(w, r, "release_id")
	if !ok {
		return
	}
	assets, err := d.Svc.ListReleaseAssets(r.Context(), uint(releaseID))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(assets))
	for i, a := range assets {
		out[i] = transform.ReleaseAsset(a, full)
	}
	respond.JSON(w, 200, paginate(w, r, d.Svc.BaseURL, out, page, perPage))
}

// DeleteReleaseAsset handles DELETE /api/v3/repos/{owner}/{repo}/releases/assets/{asset_id}
func (d *Deps) DeleteReleaseAsset(w http.ResponseWriter, r *http.Request) {
	assetID, ok := mustIntParam(w, r, "asset_id")
	if !ok {
		return
	}
	if err := d.Svc.DeleteReleaseAsset(r.Context(), uint(assetID)); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}
