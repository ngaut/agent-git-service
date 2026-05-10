// Package rest — wiki REST endpoints.
package rest

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"gh-server/internal/rest/respond"
	"gh-server/internal/rest/transform"
	"gh-server/internal/service"
)

func wikiSlugParam(r *http.Request) string {
	if slug := chi.URLParam(r, "slug"); slug != "" {
		return slug
	}
	return strings.TrimPrefix(chi.URLParam(r, "*"), "/")
}

func wikiWildcardSubresource(r *http.Request, suffix string) (string, bool) {
	if chi.URLParam(r, "slug") != "" {
		return "", false
	}
	slug := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	if !strings.HasSuffix(slug, "/"+suffix) {
		return "", false
	}
	trimmed := strings.TrimSuffix(slug, "/"+suffix)
	// Reserve wildcard subresource dispatch for nested wiki paths such as
	// guides/setup/backlinks; two-segment paths like guides/backlinks remain
	// addressable as ordinary page slugs.
	if strings.Count(trimmed, "/") < 1 {
		return "", false
	}
	return trimmed, true
}

func wikiWildcardLabelNameSubresource(r *http.Request) (string, string, bool, error) {
	if chi.URLParam(r, "slug") != "" {
		return "", "", false, nil
	}
	raw := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	marker := "/labels/"
	idx := strings.LastIndex(raw, marker)
	if idx < 0 {
		return "", "", false, nil
	}
	slug := raw[:idx]
	if strings.Count(slug, "/") < 1 {
		return "", "", false, nil
	}
	nameRaw := raw[idx+len(marker):]
	if nameRaw == "" {
		return "", "", false, nil
	}
	name, err := url.PathUnescape(nameRaw)
	if err != nil {
		return "", "", true, err
	}
	return slug, name, true, nil
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

// SearchWikiPages handles GET /api/v3/repos/{owner}/{repo}/wiki/search
func (d *Deps) SearchWikiPages(w http.ResponseWriter, r *http.Request) {
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
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusOK, transform.WikiSearchResponse(full, resp))
}

// ListWikiPages handles GET /api/v3/repos/{owner}/{repo}/wiki/pages
func (d *Deps) ListWikiPages(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	if d.mustGetRepo(w, r) == nil {
		return
	}
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
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, 0, len(pages))
	for _, p := range pages {
		out = append(out, transform.WikiPageSummary(full, p))
	}
	respond.JSON(w, 200, out)
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
	name, err := url.PathUnescape(chi.URLParam(r, "name"))
	if err != nil {
		respond.ValidationFailed(w, "invalid label name encoding")
		return
	}
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
	if chi.URLParam(r, "slug") == "" {
		raw := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
		if strings.HasSuffix(raw, "/labels") {
			wildcardSlug := strings.TrimSuffix(raw, "/labels")
			if strings.Count(wildcardSlug, "/") >= 1 {
				d.addWikiPageLabels(w, r, full, wildcardSlug)
				return
			}
		}
	}
	if wildcardSlug, ok := wikiWildcardSubresource(r, "move"); ok {
		slug = wildcardSlug
	} else if chi.URLParam(r, "slug") == "" {
		respond.NotFound(w)
		return
	}
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	if chi.URLParam(r, "slug") == "" {
		if _, err := d.Svc.GetWikiPage(r.Context(), full, slug); err != nil && !errors.Is(err, service.ErrNotFound) {
			respond.ServiceErrorRequest(r, w, err)
			return
		} else if errors.Is(err, service.ErrNotFound) {
			if wildcardSlug, ok := wikiWildcardSubresource(r, "labels"); ok {
				d.addWikiPageLabels(w, r, full, wildcardSlug)
				return
			}
		}
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
	full := repoFullName(r)
	slug := wikiSlugParam(r)
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if d.mustGetRepo(w, r) == nil {
		return
	}
	if chi.URLParam(r, "slug") == "" {
		page, err := d.Svc.GetWikiPageAtRef(r.Context(), full, slug, ref)
		switch {
		case err == nil:
			respond.JSON(w, 200, transform.WikiPage(full, page))
			return
		case !errors.Is(err, service.ErrNotFound):
			respond.ServiceErrorRequest(r, w, err)
			return
		}
	}
	if wildcardSlug, ok := wikiWildcardSubresource(r, "backlinks"); ok {
		slug = wildcardSlug
		backlinks, err := d.Svc.ListWikiBacklinks(r.Context(), full, slug)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		out := make([]any, 0, len(backlinks))
		for _, backlink := range backlinks {
			out = append(out, transform.WikiBacklink(full, backlink))
		}
		respond.JSON(w, 200, out)
		return
	}
	if wildcardSlug, ok := wikiWildcardSubresource(r, "labels"); ok {
		d.listWikiPageLabels(w, r, full, wildcardSlug)
		return
	}
	page, err := d.Svc.GetWikiPageAtRef(r.Context(), full, slug, ref)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.WikiPage(full, page))
}

// ListWikiPageHistory handles GET /api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/history
func (d *Deps) ListWikiPageHistory(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	slug := chi.URLParam(r, "slug")
	if d.mustGetRepo(w, r) == nil {
		return
	}
	page, perPage := parsePagination(r)
	history, total, err := d.Svc.ListWikiPageHistoryPage(r.Context(), full, slug, page, perPage)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	setLinkHeader(w, r, d.Svc.BaseURL, total, page, perPage)
	out := make([]any, 0, len(history))
	for _, entry := range history {
		out = append(out, transform.WikiPageHistoryEntry(entry))
	}
	respond.JSON(w, 200, out)
}

// ListWikiBacklinks handles GET /api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/backlinks
func (d *Deps) ListWikiBacklinks(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	slug := wikiSlugParam(r)
	if d.mustGetRepo(w, r) == nil {
		return
	}
	if page, err := d.Svc.GetWikiPage(r.Context(), full, slug+"/backlinks"); err == nil {
		respond.JSON(w, 200, transform.WikiPage(full, page))
		return
	} else if !errors.Is(err, service.ErrNotFound) {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	backlinks, err := d.Svc.ListWikiBacklinks(r.Context(), full, slug)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, 0, len(backlinks))
	for _, backlink := range backlinks {
		out = append(out, transform.WikiBacklink(full, backlink))
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
	if chi.URLParam(r, "slug") == "" {
		if _, err := d.Svc.GetWikiPage(r.Context(), full, slug); err != nil && !errors.Is(err, service.ErrNotFound) {
			respond.ServiceErrorRequest(r, w, err)
			return
		} else if errors.Is(err, service.ErrNotFound) {
			if wildcardSlug, ok := wikiWildcardSubresource(r, "labels"); ok {
				d.setWikiPageLabels(w, r, full, wildcardSlug)
				return
			}
		}
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
	if chi.URLParam(r, "slug") == "" {
		if _, err := d.Svc.GetWikiPage(r.Context(), full, slug); err != nil && !errors.Is(err, service.ErrNotFound) {
			respond.ServiceErrorRequest(r, w, err)
			return
		} else if errors.Is(err, service.ErrNotFound) {
			if wildcardSlug, labelName, ok, err := wikiWildcardLabelNameSubresource(r); err != nil {
				respond.ValidationFailed(w, "invalid label name encoding")
				return
			} else if ok {
				d.removeWikiPageLabel(w, r, full, wildcardSlug, labelName)
				return
			}
			if wildcardSlug, ok := wikiWildcardSubresource(r, "labels"); ok {
				d.removeAllWikiPageLabels(w, r, full, wildcardSlug)
				return
			}
		}
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
