package rest

import (
	"net/http"

	"gh-server/internal/rest/respond"
	"gh-server/internal/rest/transform"
)

// ListOutsideCollaborators handles GET /api/v3/orgs/{org}/outside_collaborators.
func (d *Deps) ListOutsideCollaborators(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	if !d.requireOrgAdmin(w, r, org) {
		return
	}

	rows, err := d.Svc.ListOutsideCollaborators(r.Context(), org.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	out := make([]any, 0, len(rows))
	for _, row := range rows {
		userMap := transform.User(row.User)
		userMap["organization_member"] = false
		userMap["outside_collaborator"] = true
		out = append(out, userMap)
	}
	if out == nil {
		out = []any{}
	}
	respond.JSON(w, http.StatusOK, out)
}
