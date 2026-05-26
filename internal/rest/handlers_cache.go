package rest

import (
	"net/http"

	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
)

// ListActionsCaches handles GET /repos/{owner}/{repo}/actions/caches
func (d *Deps) ListActionsCaches(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}

	caches, err := d.Svc.ListActionCaches(r.Context(), repo.FullName)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	nodes := make([]map[string]any, len(caches))
	for i, c := range caches {
		nodes[i] = transform.ActionCache(c)
	}
	respond.JSON(w, 200, map[string]any{"total_count": len(nodes), "actions_caches": nodes})
}

// DeleteActionsCaches handles DELETE /repos/{owner}/{repo}/actions/caches?key=...
func (d *Deps) DeleteActionsCaches(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	key := r.URL.Query().Get("key")

	if key == "" {
		respond.Error(w, http.StatusBadRequest, "Missing key parameter")
		return
	}

	err := d.Svc.DeleteActionCaches(r.Context(), repo.FullName, key)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// DeleteActionsCacheByID handles DELETE /repos/{owner}/{repo}/actions/caches/{cache_id}
func (d *Deps) DeleteActionsCacheByID(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "cache_id")
	if !ok {
		return
	}
	if err := d.Svc.DeleteActionCacheByID(r.Context(), uint(id)); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// GetCacheUsage handles GET /repos/{owner}/{repo}/actions/cache/usage
func (d *Deps) GetCacheUsage(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, 200, map[string]any{
		"full_name":                   repoFullName(r),
		"active_caches_size_in_bytes": 1024,
		"active_caches_count":         1,
	})
}
