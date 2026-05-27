// Package rest — wiki REST endpoints.
package rest

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/service"
)

func wikiSlugParam(r *http.Request) string {
	return pathParam(r, "slug")
}

func wikiCompactionJobIDParam(r *http.Request) string {
	return pathParam(r, "jobID")
}

type wikiV2StateResponse struct {
	RepositoryID         uint       `json:"repository_id"`
	IndexedCommitSHA     string     `json:"indexed_commit_sha"`
	IndexedAt            *time.Time `json:"indexed_at,omitempty"`
	ReconcileRequestedAt *time.Time `json:"reconcile_requested_at,omitempty"`
	ReconcilerLeaseUntil *time.Time `json:"reconciler_lease_until,omitempty"`
	PageCount            int        `json:"page_count"`
}

// ListWikiV2Tree handles GET /api/v3/repos/{owner}/{repo}/wiki-v2/tree
func (d *Deps) ListWikiV2Tree(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	if d.mustGetRepo(w, r) == nil {
		return
	}
	tree, err := d.Svc.ListWikiTreeAtRef(
		r.Context(),
		full,
		strings.TrimSpace(r.URL.Query().Get("path")),
		strings.TrimSpace(r.URL.Query().Get("ref")),
	)
	if err != nil {
		d.respondWikiReadError(w, r, full, err)
		return
	}
	d.setWikiMigrationInProgressHeaderForRequest(w, r, full)
	out := make([]any, 0, len(tree))
	for _, entry := range tree {
		out = append(out, transform.WikiV2TreeEntry(full, entry))
	}
	respond.JSON(w, http.StatusOK, out)
}

func wikiLabelFiltersFromQuery(q url.Values) (labels, excludeLabels []string) {
	labels = append(labels, splitCommaQueryValues(q["label"])...)
	labels = append(labels, splitCommaQueryValues(q["labels"])...)
	excludeLabels = append(excludeLabels, splitCommaQueryValues(q["exclude_label"])...)
	excludeLabels = append(excludeLabels, splitCommaQueryValues(q["exclude_labels"])...)
	return labels, excludeLabels
}

func splitCommaQueryValues(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func (d *Deps) setWikiMigrationInProgressHeaderForRequest(w http.ResponseWriter, r *http.Request, full string) {
	ctx := context.Background()
	if r != nil {
		ctx = r.Context()
	}
	if d.Svc.IsWikiBackgroundMigrationRunning(ctx, full) {
		w.Header().Set("X-Wiki-Migration-In-Progress", "true")
	}
}

func (d *Deps) respondWikiReadError(w http.ResponseWriter, r *http.Request, full string, err error) {
	d.setWikiMigrationInProgressHeaderForRequest(w, r, full)
	respond.ServiceErrorRequest(r, w, err)
}

// SearchWikiPages handles GET /api/v3/repos/{owner}/{repo}/wiki/search
func (d *Deps) SearchWikiPages(w http.ResponseWriter, r *http.Request) {
	d.searchWikiPages(w, r, false)
}

// SearchWikiV2Pages handles GET /api/v3/repos/{owner}/{repo}/wiki-v2/search
func (d *Deps) SearchWikiV2Pages(w http.ResponseWriter, r *http.Request) {
	d.searchWikiPages(w, r, true)
}

func (d *Deps) searchWikiPages(w http.ResponseWriter, r *http.Request, useV2Transform bool) {
	full := repoFullName(r)
	if d.mustGetRepo(w, r) == nil {
		return
	}
	limit, offset := 20, 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			respond.ValidationFailed(w, "invalid limit query parameter")
			return
		}
		limit = parsed
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			respond.ValidationFailed(w, "invalid offset query parameter")
			return
		}
		offset = parsed
	}
	labels, excludeLabels := wikiLabelFiltersFromQuery(r.URL.Query())
	resp, err := d.Svc.SearchWikiPagesWithOptions(r.Context(), full, r.URL.Query().Get("q"), service.WikiSearchOptions{
		Limit:         limit,
		Offset:        offset,
		Labels:        labels,
		ExcludeLabels: excludeLabels,
	})
	if err != nil {
		d.respondWikiReadError(w, r, full, err)
		return
	}
	d.setWikiMigrationInProgressHeaderForRequest(w, r, full)
	if useV2Transform {
		respond.JSON(w, http.StatusOK, transform.WikiV2SearchResponse(full, resp))
		return
	}
	respond.JSON(w, http.StatusOK, transform.WikiSearchResponse(full, resp))
}

// ListWikiPages handles GET /api/v3/repos/{owner}/{repo}/wiki/pages
func (d *Deps) ListWikiPages(w http.ResponseWriter, r *http.Request) {
	d.listWikiPages(w, r, false)
}

// ListWikiV2Pages handles GET /api/v3/repos/{owner}/{repo}/wiki-v2/pages
func (d *Deps) ListWikiV2Pages(w http.ResponseWriter, r *http.Request) {
	d.listWikiPages(w, r, true)
}

func (d *Deps) listWikiPages(w http.ResponseWriter, r *http.Request, useV2Transform bool) {
	full := repoFullName(r)
	if d.mustGetRepo(w, r) == nil {
		return
	}
	page, perPage := parsePagination(r)
	recursive := true
	if raw := r.URL.Query().Get("recursive"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			respond.ValidationFailed(w, "invalid recursive query parameter")
			return
		}
		recursive = parsed
	}
	labels, excludeLabels := wikiLabelFiltersFromQuery(r.URL.Query())
	pages, err := d.Svc.ListWikiPages(r.Context(), full, service.ListWikiPagesOptions{
		Path:          r.URL.Query().Get("path"),
		Recursive:     recursive,
		Labels:        labels,
		ExcludeLabels: excludeLabels,
	})
	if err != nil {
		d.respondWikiReadError(w, r, full, err)
		return
	}
	d.setWikiMigrationInProgressHeaderForRequest(w, r, full)
	pages = paginate(w, r, d.Svc.BaseURL, pages, page, perPage)
	out := make([]any, 0, len(pages))
	for _, p := range pages {
		if useV2Transform {
			out = append(out, transform.WikiV2PageSummary(full, p))
		} else {
			out = append(out, transform.WikiPageSummary(full, p))
		}
	}
	respond.JSON(w, 200, out)
}

// GetWikiV2State handles GET /api/v3/repos/{owner}/{repo}/wiki-v2/state
func (d *Deps) GetWikiV2State(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	if d.mustGetRepo(w, r) == nil {
		return
	}
	state, err := d.Svc.GetWikiV2State(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusOK, wikiV2StateResponse{
		RepositoryID:         state.RepositoryID,
		IndexedCommitSHA:     state.IndexedCommitSHA,
		IndexedAt:            state.IndexedAt,
		ReconcileRequestedAt: state.ReconcileRequestedAt,
		ReconcilerLeaseUntil: state.ReconcilerLeaseUntil,
		PageCount:            state.PageCount,
	})
}

// RequestWikiV2Reconcile handles POST /api/v3/repos/{owner}/{repo}/wiki-v2/reconcile/request
func (d *Deps) RequestWikiV2Reconcile(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	result, err := d.Svc.KickWikiV2Reconcile(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusAccepted, map[string]any{
		"repository_id":      result.RepositoryID,
		"indexed_commit_sha": result.IndexedCommitSHA,
		"requested_at":       result.RequestedAt,
	})
}

// ReconcileWikiV2 handles POST /api/v3/repos/{owner}/{repo}/wiki-v2/reconcile
func (d *Deps) ReconcileWikiV2(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	result, err := d.Svc.ReconcileWikiV2(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{
		"repository_id":      result.RepositoryID,
		"indexed_commit_sha": result.IndexedCommitSHA,
		"page_count":         result.PageCount,
		"reconciled":         result.Reconciled,
	})
}

// ListWikiPageLabels handles GET /api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/labels
func (d *Deps) ListWikiPageLabels(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	slug := wikiSlugParam(r)
	d.listWikiPageLabels(w, r, full, slug)
}

// AddWikiPageLabels handles POST /api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/labels
func (d *Deps) AddWikiPageLabels(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	slug := wikiSlugParam(r)
	d.addWikiPageLabels(w, r, full, slug)
}

// SetWikiPageLabels handles PUT /api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/labels
func (d *Deps) SetWikiPageLabels(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	slug := wikiSlugParam(r)
	d.setWikiPageLabels(w, r, full, slug)
}

// RemoveWikiPageLabel handles DELETE /api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/labels/{name}
func (d *Deps) RemoveWikiPageLabel(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	slug := wikiSlugParam(r)
	name := pathParam(r, "name")
	d.removeWikiPageLabel(w, r, full, slug, name)
}

// RemoveAllWikiPageLabels handles DELETE /api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/labels
func (d *Deps) RemoveAllWikiPageLabels(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	slug := wikiSlugParam(r)
	d.removeAllWikiPageLabels(w, r, full, slug)
}

func (d *Deps) listWikiPageLabels(w http.ResponseWriter, r *http.Request, full, slug string) {
	if d.mustGetRepo(w, r) == nil {
		return
	}
	labels, err := d.Svc.ListWikiPageLabels(r.Context(), full, slug)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusOK, transform.WikiLabels(labels))
}

func (d *Deps) addWikiPageLabels(w http.ResponseWriter, r *http.Request, full, slug string) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("ref")) != "" {
		respond.Error(w, http.StatusBadRequest, "ref query parameter is not supported for wiki writes")
		return
	}
	var body struct {
		Labels []string `json:"labels"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	labels, err := d.Svc.AddWikiPageLabels(r.Context(), full, slug, body.Labels)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusOK, transform.WikiLabels(labels))
}

func (d *Deps) setWikiPageLabels(w http.ResponseWriter, r *http.Request, full, slug string) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("ref")) != "" {
		respond.Error(w, http.StatusBadRequest, "ref query parameter is not supported for wiki writes")
		return
	}
	var body struct {
		Labels []string `json:"labels"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	labels, err := d.Svc.SetWikiPageLabels(r.Context(), full, slug, body.Labels)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusOK, transform.WikiLabels(labels))
}

func (d *Deps) removeWikiPageLabel(w http.ResponseWriter, r *http.Request, full, slug, name string) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("ref")) != "" {
		respond.Error(w, http.StatusBadRequest, "ref query parameter is not supported for wiki writes")
		return
	}
	labels, err := d.Svc.RemoveWikiPageLabel(r.Context(), full, slug, name)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusOK, transform.WikiLabels(labels))
}

func (d *Deps) removeAllWikiPageLabels(w http.ResponseWriter, r *http.Request, full, slug string) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("ref")) != "" {
		respond.Error(w, http.StatusBadRequest, "ref query parameter is not supported for wiki writes")
		return
	}
	if err := d.Svc.RemoveAllWikiPageLabels(r.Context(), full, slug); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// MoveWikiPagePrefix handles POST /api/v3/repos/{owner}/{repo}/wiki/move
func (d *Deps) MoveWikiPagePrefix(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("ref")) != "" {
		respond.Error(w, http.StatusBadRequest, "ref query parameter is not supported for wiki writes")
		return
	}
	var body struct {
		From    string            `json:"from"`
		To      string            `json:"to"`
		Message string            `json:"message"`
		IfMatch map[string]string `json:"if_match"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}

	result, err := d.Svc.MoveWikiPagePrefix(r.Context(), full, body.From, body.To, body.IfMatch, body.Message)
	if err != nil {
		var notFound *service.WikiBulkMoveNotFoundError
		if errors.As(err, &notFound) {
			respond.JSON(w, http.StatusNotFound, map[string]any{
				"error":             err.Error(),
				"message":           err.Error(),
				"code":              notFound.Code(),
				"documentation_url": "https://docs.github.com/rest",
				"from":              notFound.From,
			})
			return
		}

		var validation *service.WikiBulkMoveValidationError
		if errors.As(err, &validation) {
			respond.JSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error":             err.Error(),
				"message":           err.Error(),
				"code":              validation.Code(),
				"documentation_url": "https://docs.github.com/rest",
				"missing_slugs":     validation.MissingSlugs,
			})
			return
		}

		var conflict *service.WikiBulkMoveConflictError
		if errors.As(err, &conflict) {
			conflicts := make([]any, 0, len(conflict.Conflicts))
			for _, item := range conflict.Conflicts {
				row := map[string]any{
					"from":    item.From,
					"to":      item.To,
					"code":    item.Code,
					"message": item.Message,
				}
				if item.CurrentSHA != "" {
					row["current_sha"] = item.CurrentSHA
				}
				if item.ConflictsWith != "" {
					row["conflicts_with"] = item.ConflictsWith
				}
				conflicts = append(conflicts, row)
			}
			respond.JSON(w, http.StatusConflict, map[string]any{
				"error":             err.Error(),
				"message":           err.Error(),
				"documentation_url": "https://docs.github.com/rest",
				"conflicts":         conflicts,
			})
			return
		}

		respond.ServiceErrorRequest(r, w, err)
		return
	}

	respond.JSON(w, http.StatusOK, transform.WikiBulkMoveResult(full, result))
}

// MoveWikiPage handles POST /api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/move
func (d *Deps) MoveWikiPage(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	slug := wikiSlugParam(r)
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("ref")) != "" {
		respond.Error(w, http.StatusBadRequest, "ref query parameter is not supported for wiki writes")
		return
	}
	var body struct {
		NewSlug string `json:"new_slug"`
		Message string `json:"message"`
		IfMatch string `json:"if_match"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	result, err := d.Svc.MoveWikiPage(r.Context(), full, slug, body.NewSlug, body.IfMatch, body.Message)
	if err != nil {
		var coded interface{ Code() string }
		if errors.As(err, &coded) {
			respond.JSON(w, http.StatusConflict, map[string]any{
				"error":             err.Error(),
				"message":           err.Error(),
				"code":              coded.Code(),
				"documentation_url": "https://docs.github.com/rest",
			})
			return
		}
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusOK, transform.WikiMoveResult(full, result))
}

// GetWikiPage handles GET /api/v3/repos/{owner}/{repo}/wiki/pages/{slug}
func (d *Deps) GetWikiPage(w http.ResponseWriter, r *http.Request) {
	d.getWikiPage(w, r, false)
}

// GetWikiV2Page handles GET /api/v3/repos/{owner}/{repo}/wiki-v2/pages/{slug}
func (d *Deps) GetWikiV2Page(w http.ResponseWriter, r *http.Request) {
	d.getWikiPage(w, r, true)
}

func (d *Deps) getWikiPage(w http.ResponseWriter, r *http.Request, useV2Transform bool) {
	full := repoFullName(r)
	slug := wikiSlugParam(r)
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if d.mustGetRepo(w, r) == nil {
		return
	}
	page, err := d.Svc.GetWikiPageAtRef(r.Context(), full, slug, ref)
	if err != nil {
		d.respondWikiReadError(w, r, full, err)
		return
	}
	d.setWikiMigrationInProgressHeaderForRequest(w, r, full)
	if useV2Transform {
		respond.JSON(w, 200, transform.WikiV2Page(full, page))
		return
	}
	respond.JSON(w, 200, transform.WikiPage(full, page))
}

// ListWikiPageHistory handles GET /api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/history
func (d *Deps) ListWikiPageHistory(w http.ResponseWriter, r *http.Request) {
	d.listWikiPageHistoryRoute(w, r, false)
}

// ListWikiV2PageHistory handles GET /api/v3/repos/{owner}/{repo}/wiki-v2/pages/{slug}/history
func (d *Deps) ListWikiV2PageHistory(w http.ResponseWriter, r *http.Request) {
	d.listWikiPageHistoryRoute(w, r, true)
}

func (d *Deps) listWikiPageHistoryRoute(w http.ResponseWriter, r *http.Request, _ bool) {
	full := repoFullName(r)
	slug := wikiSlugParam(r)
	if d.mustGetRepo(w, r) == nil {
		return
	}
	d.listWikiPageHistory(w, r, full, slug)
}

func (d *Deps) listWikiPageHistory(w http.ResponseWriter, r *http.Request, full, slug string) {
	page, perPage := parsePagination(r)
	history, total, err := d.Svc.ListWikiPageHistoryPage(r.Context(), full, slug, page, perPage)
	if err != nil {
		d.respondWikiReadError(w, r, full, err)
		return
	}
	d.setWikiMigrationInProgressHeaderForRequest(w, r, full)
	setLinkHeader(w, r, d.Svc.BaseURL, total, page, perPage)
	out := make([]any, 0, len(history))
	for _, entry := range history {
		out = append(out, transform.WikiPageHistoryEntry(entry))
	}
	respond.JSON(w, 200, out)
}

// CompactWikiHistory handles POST /api/v3/repos/{owner}/{repo}/wiki/compact
func (d *Deps) CompactWikiHistory(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionAdmin) {
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("ref")) != "" {
		respond.Error(w, http.StatusBadRequest, "ref query parameter is not supported for wiki writes")
		return
	}
	var body struct {
		Before string `json:"before"`
	}
	if err := decodeBodyStrictOptional(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	if strings.TrimSpace(body.Before) != "" {
		respond.ValidationFailed(w, "before is not supported for wiki compact")
		return
	}
	job, err := d.Svc.StartWikiCompaction(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	statusURL := "/api/v3/repos/" + full + "/wiki/compact/" + job.ID
	w.Header().Set("Location", statusURL)
	respond.JSON(w, http.StatusAccepted, wikiCompactionJobResponse(job, statusURL))
}

// GetWikiCompactionJob handles GET /api/v3/repos/{owner}/{repo}/wiki/compact/{jobID}
func (d *Deps) GetWikiCompactionJob(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionAdmin) {
		return
	}
	job, err := d.Svc.GetWikiCompactionJob(r.Context(), full, wikiCompactionJobIDParam(r))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusOK, wikiCompactionJobResponse(job, "/api/v3/repos/"+full+"/wiki/compact/"+job.ID))
}

// RepairWikiLocks handles POST /api/v3/admin/wiki/repos/{owner}/{repo}/repair-locks
func (d *Deps) RepairWikiLocks(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionAdmin) {
		return
	}
	var body struct {
		Force bool `json:"force"`
	}
	if err := decodeBodyStrictOptional(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	result, err := d.Svc.RepairWikiRefLocks(r.Context(), full, body.Force)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{
		"ref":         result.Ref,
		"lock_path":   result.LockPath,
		"present":     result.Present,
		"cleared":     result.Cleared,
		"force":       result.Force,
		"age_seconds": result.AgeSeconds,
	})
}

func wikiCompactionJobResponse(job db.WikiCompactionJob, statusURL string) map[string]any {
	resp := map[string]any{
		"job_id":      job.ID,
		"status":      job.Status,
		"status_url":  statusURL,
		"location":    statusURL,
		"started_at":  nil,
		"finished_at": nil,
	}
	if startedAt := job.StartedAt; startedAt != nil {
		resp["started_at"] = startedAt.Format(time.RFC3339)
	}
	if finishedAt := job.FinishedAt; finishedAt != nil {
		resp["finished_at"] = finishedAt.Format(time.RFC3339)
	}
	if previousHead := job.PreviousHead; previousHead != "" {
		resp["previous_head"] = previousHead
	}
	if newHead := job.NewHead; newHead != "" {
		resp["new_head"] = newHead
	}
	if compactedBefore := job.CompactedBefore; compactedBefore != nil {
		resp["compacted_before"] = compactedBefore.Format(time.RFC3339)
	}
	if job.Pages > 0 || job.Status == service.WikiCompactionJobSucceeded {
		resp["pages"] = job.Pages
	}
	if job.CommitsRemoved > 0 || job.Status == service.WikiCompactionJobSucceeded {
		resp["commits_removed"] = job.CommitsRemoved
	}
	if errorMessage := job.ErrorMessage; errorMessage != "" {
		resp["error"] = errorMessage
	}
	return resp
}

// ListWikiBacklinks handles GET /api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/backlinks
func (d *Deps) ListWikiBacklinks(w http.ResponseWriter, r *http.Request) {
	d.listWikiBacklinks(w, r, false)
}

// ListWikiV2Backlinks handles GET /api/v3/repos/{owner}/{repo}/wiki-v2/pages/{slug}/backlinks
func (d *Deps) ListWikiV2Backlinks(w http.ResponseWriter, r *http.Request) {
	d.listWikiBacklinks(w, r, true)
}

func (d *Deps) listWikiBacklinks(w http.ResponseWriter, r *http.Request, useV2Transform bool) {
	full := repoFullName(r)
	slug := wikiSlugParam(r)
	if d.mustGetRepo(w, r) == nil {
		return
	}
	backlinks, err := d.Svc.ListWikiBacklinks(r.Context(), full, slug)
	if err != nil {
		d.respondWikiReadError(w, r, full, err)
		return
	}
	d.setWikiMigrationInProgressHeaderForRequest(w, r, full)
	out := make([]any, 0, len(backlinks))
	for _, backlink := range backlinks {
		if useV2Transform {
			out = append(out, transform.WikiV2Backlink(full, backlink))
		} else {
			out = append(out, transform.WikiBacklink(full, backlink))
		}
	}
	respond.JSON(w, 200, out)
}

// PutWikiPage handles PUT /api/v3/repos/{owner}/{repo}/wiki/pages/{slug}
func (d *Deps) PutWikiPage(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	slug := wikiSlugParam(r)
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("ref")) != "" {
		respond.Error(w, http.StatusBadRequest, "ref query parameter is not supported for wiki writes")
		return
	}
	var body struct {
		Body    string `json:"body"`
		Message string `json:"message"`
		SHA     string `json:"sha"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	bodySHA := strings.TrimSpace(body.SHA)
	headerSHA, err := parseIfMatchSHA(r.Header.Get("If-Match"))
	if err != nil {
		respond.ValidationFailed(w, err.Error())
		return
	}
	if bodySHA != "" && headerSHA != "" && !strings.EqualFold(bodySHA, headerSHA) {
		respond.ValidationFailed(w, "sha precondition in body and If-Match header must match")
		return
	}
	expectedSHA := bodySHA
	if expectedSHA == "" {
		expectedSHA = headerSHA
	}
	page, err := d.Svc.PutWikiPage(r.Context(), full, slug, body.Body, body.Message, expectedSHA)
	if err != nil {
		var conflict *service.WikiConflictError
		if errors.As(err, &conflict) {
			payload := map[string]any{
				"message":           err.Error(),
				"documentation_url": "https://docs.github.com/rest",
				"current_page":      nil,
			}
			if conflict.CurrentPage != nil {
				payload["current_page"] = transform.WikiPage(full, *conflict.CurrentPage)
			}
			respond.JSON(w, http.StatusConflict, payload)
			return
		}
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.WikiPage(full, page))
}

// DeleteWikiPage handles DELETE /api/v3/repos/{owner}/{repo}/wiki/pages/{slug}
func (d *Deps) DeleteWikiPage(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	slug := wikiSlugParam(r)
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("ref")) != "" {
		respond.Error(w, http.StatusBadRequest, "ref query parameter is not supported for wiki writes")
		return
	}
	var body struct {
		Message string `json:"message"`
	}
	_ = decodeBodyStrictOptional(r, &body)
	if err := d.Svc.DeleteWikiPage(r.Context(), full, slug, body.Message); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

func parseIfMatchSHA(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if raw == "*" {
		return "", errors.New("If-Match '*' is not supported for wiki page writes")
	}
	parts := strings.Split(raw, ",")
	if len(parts) != 1 {
		return "", errors.New("If-Match must contain exactly one sha value")
	}
	sha := strings.TrimSpace(parts[0])
	if strings.HasPrefix(sha, "W/") {
		return "", errors.New("weak If-Match validators are not supported for wiki page writes")
	}
	if len(sha) >= 2 && sha[0] == '"' && sha[len(sha)-1] == '"' {
		sha = sha[1 : len(sha)-1]
	}
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return "", errors.New("If-Match must contain a non-empty sha value")
	}
	return sha, nil
}
