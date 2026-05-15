package rest

import (
	"net/http"

	"gh-server/internal/rest/respond"
	"gh-server/internal/rest/transform"
)

// --- Deploy Keys ---

// ListDeployKeys handles GET /api/v3/repos/{owner}/{repo}/keys
func (d *Deps) ListDeployKeys(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	page, perPage := parsePagination(r)
	keys, err := d.Svc.ListDeployKeys(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = transform.DeployKey(k, full)
	}
	respond.JSON(w, 200, paginate(w, r, d.Svc.BaseURL, out, page, perPage))
}

// CreateDeployKey handles POST /api/v3/repos/{owner}/{repo}/keys
func (d *Deps) CreateDeployKey(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	var body struct {
		Title    string `json:"title"`
		Key      string `json:"key"`
		ReadOnly bool   `json:"read_only"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	dk, err := d.Svc.CreateDeployKey(r.Context(), full, body.Title, body.Key, body.ReadOnly)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 201, transform.DeployKey(dk, full))
}

// DeleteDeployKey handles DELETE /api/v3/repos/{owner}/{repo}/keys/{key_id}
func (d *Deps) DeleteDeployKey(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "key_id")
	if !ok {
		return
	}
	if err := d.Svc.DeleteDeployKey(r.Context(), uint(id)); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// --- SSH Keys ---

// ListSSHKeys handles GET /api/v3/user/keys
func (d *Deps) ListSSHKeys(w http.ResponseWriter, r *http.Request) {
	page, perPage := parsePagination(r)
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	keys, err := d.Svc.ListSSHKeys(r.Context(), u.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = transform.SSHKey(k)
	}
	respond.JSON(w, 200, paginate(w, r, d.Svc.BaseURL, out, page, perPage))
}

// CreateSSHKey handles POST /api/v3/user/keys
func (d *Deps) CreateSSHKey(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	var body struct {
		Title string `json:"title"`
		Key   string `json:"key"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	k, err := d.Svc.CreateSSHKey(r.Context(), u.ID, body.Title, body.Key)
	if err != nil {
		respond.ValidationFailed(w, err.Error())
		return
	}
	respond.JSON(w, 201, transform.SSHKey(k))
}

// DeleteSSHKey handles DELETE /api/v3/user/keys/{key_id}
func (d *Deps) DeleteSSHKey(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "key_id")
	if !ok {
		return
	}
	if err := d.Svc.DeleteSSHKey(r.Context(), uint(id)); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// GetSSHKey handles GET /api/v3/user/keys/{key_id}
func (d *Deps) GetSSHKey(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "key_id")
	if !ok {
		return
	}
	k, err := d.Svc.GetSSHKey(r.Context(), uint(id))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.SSHKey(k))
}

// --- GPG Keys ---

// ListGPGKeys handles GET /api/v3/user/gpg_keys
func (d *Deps) ListGPGKeys(w http.ResponseWriter, r *http.Request) {
	page, perPage := parsePagination(r)
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	keys, err := d.Svc.ListGPGKeys(r.Context(), u.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = transform.GPGKey(k)
	}
	respond.JSON(w, 200, paginate(w, r, d.Svc.BaseURL, out, page, perPage))
}

// CreateGPGKey handles POST /api/v3/user/gpg_keys
func (d *Deps) CreateGPGKey(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	var body struct {
		ArmoredPublicKey string `json:"armored_public_key"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	k, err := d.Svc.CreateGPGKey(r.Context(), u.ID, body.ArmoredPublicKey)
	if err != nil {
		respond.ValidationFailed(w, err.Error())
		return
	}
	respond.JSON(w, 201, transform.GPGKey(k))
}

// DeleteGPGKey handles DELETE /api/v3/user/gpg_keys/{gpg_key_id}
func (d *Deps) DeleteGPGKey(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "gpg_key_id")
	if !ok {
		return
	}
	if err := d.Svc.DeleteGPGKey(r.Context(), uint(id)); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// --- SSH Signing Keys ---

// ListSSHSigningKeys handles GET /api/v3/user/ssh_signing_keys
func (d *Deps) ListSSHSigningKeys(w http.ResponseWriter, r *http.Request) {
	page, perPage := parsePagination(r)
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	keys, err := d.Svc.ListSSHSigningKeys(r.Context(), u.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = transform.SSHSigningKey(k)
	}
	respond.JSON(w, 200, paginate(w, r, d.Svc.BaseURL, out, page, perPage))
}

// CreateSSHSigningKey handles POST /api/v3/user/ssh_signing_keys
func (d *Deps) CreateSSHSigningKey(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	var body struct {
		Title string `json:"title"`
		Key   string `json:"key"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	k, err := d.Svc.CreateSSHSigningKey(r.Context(), u.ID, body.Title, body.Key)
	if err != nil {
		respond.ValidationFailed(w, err.Error())
		return
	}
	respond.JSON(w, 201, transform.SSHSigningKey(k))
}

// GetSSHSigningKey handles GET /api/v3/user/ssh_signing_keys/{ssh_signing_key_id}
func (d *Deps) GetSSHSigningKey(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "ssh_signing_key_id")
	if !ok {
		return
	}
	k, err := d.Svc.GetSSHSigningKey(r.Context(), uint(id))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.SSHSigningKey(k))
}

// DeleteSSHSigningKey handles DELETE /api/v3/user/ssh_signing_keys/{ssh_signing_key_id}
func (d *Deps) DeleteSSHSigningKey(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "ssh_signing_key_id")
	if !ok {
		return
	}
	if err := d.Svc.DeleteSSHSigningKey(r.Context(), uint(id)); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// --- Other-User Key Listings ---

// ListUserPublicKeys handles GET /api/v3/users/{username}/keys
func (d *Deps) ListUserPublicKeys(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetUser(r.Context(), pathParam(r, "username"))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	keys, err := d.Svc.ListSSHKeys(r.Context(), u.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = transform.SSHKey(k)
	}
	respond.JSON(w, 200, out)
}

// ListUserSigningKeys handles GET /api/v3/users/{username}/ssh_signing_keys
func (d *Deps) ListUserSigningKeys(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetUser(r.Context(), pathParam(r, "username"))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	keys, err := d.Svc.ListSSHSigningKeys(r.Context(), u.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = transform.SSHSigningKey(k)
	}
	respond.JSON(w, 200, out)
}

// ListUserGPGKeys handles GET /api/v3/users/{username}/gpg_keys
func (d *Deps) ListUserGPGKeys(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetUser(r.Context(), pathParam(r, "username"))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	keys, err := d.Svc.ListGPGKeys(r.Context(), u.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = transform.GPGKey(k)
	}
	respond.JSON(w, 200, out)
}
