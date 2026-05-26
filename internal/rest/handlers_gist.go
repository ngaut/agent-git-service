package rest

import (
	"encoding/json"
	"net/http"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/randutil"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
)

// CreateGist handles POST /gists
func (d *Deps) CreateGist(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Description string                       `json:"description"`
		Public      bool                         `json:"public"`
		Files       map[string]map[string]string `json:"files"`
	}
	if err := decodeBodyStrict(r, &payload); err != nil {
		respond.Error(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	filesJSON, _ := json.Marshal(payload.Files)
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, "Requires authentication")
		return
	}

	gist := db.Gist{
		ID:          randutil.Hex(20),
		OwnerID:     u.ID,
		Description: payload.Description,
		Public:      payload.Public,
		Files:       string(filesJSON),
	}
	if err := d.Svc.CreateGist(r.Context(), &gist); err != nil {
		respond.Error(w, 500, err.Error())
		return
	}
	gist.Owner = u
	respond.JSON(w, 201, transform.Gist(gist))
}

// GetGist handles GET /gists/{gist_id}
func (d *Deps) GetGist(w http.ResponseWriter, r *http.Request) {
	gist, err := d.Svc.GetGist(r.Context(), pathParam(r, "gist_id"))
	if err != nil {
		respond.NotFound(w)
		return
	}
	respond.JSON(w, 200, transform.Gist(gist))
}

// UpdateGist handles PATCH|POST /gists/{gist_id}
func (d *Deps) UpdateGist(w http.ResponseWriter, r *http.Request) {
	gist, err := d.Svc.GetGist(r.Context(), pathParam(r, "gist_id"))
	if err != nil {
		respond.NotFound(w)
		return
	}

	var payload struct {
		Description *string                      `json:"description"`
		Files       map[string]map[string]string `json:"files"`
	}
	if err := decodeBodyStrict(r, &payload); err != nil {
		respond.Error(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if err := d.Svc.UpdateGist(r.Context(), &gist, payload.Description, payload.Files); err != nil {
		respond.Error(w, 500, err.Error())
		return
	}
	respond.JSON(w, 200, transform.Gist(gist))
}

// DeleteGist handles DELETE /gists/{gist_id}
func (d *Deps) DeleteGist(w http.ResponseWriter, r *http.Request) {
	if err := d.Svc.DeleteGist(r.Context(), pathParam(r, "gist_id")); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// ListGists handles GET /gists
func (d *Deps) ListGists(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	gists, err := d.Svc.ListGistsByOwner(r.Context(), u.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	result := make([]map[string]any, len(gists))
	for i, g := range gists {
		result[i] = transform.Gist(g)
	}
	respond.JSON(w, 200, result)
}
