// Package rest — audit log REST surface.
package rest

import (
	"net/http"
	"strconv"
	"time"

	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/service"
)

// ListOrgAuditLog handles GET /api/v3/orgs/{org}/audit-log
//
// Initial scope is org membership add/remove events; future PRs add
// more event sources without changing this surface. Supports the
// GitHub-compatible filters: phrase, after, before, order, per_page.
// Requires org admin (mirrors GitHub's audit-log access policy).
func (d *Deps) ListOrgAuditLog(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	if !d.requireOrgAdmin(w, r, org) {
		return
	}

	q := r.URL.Query()
	filters := service.AuditLogFilters{
		Phrase:  q.Get("phrase"),
		Order:   q.Get("order"),
		PerPage: parseIntDefault(q.Get("per_page"), 0),
	}
	if v := q.Get("after"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filters.After = t
		}
	}
	if v := q.Get("before"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filters.Before = t
		}
	}

	entries, err := d.Svc.ListOrgAuditLog(r.Context(), org.ID, filters)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(entries))
	for i, e := range entries {
		out[i] = transform.AuditLogEntry(e, org.Login)
	}
	respond.JSON(w, 200, out)
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}
