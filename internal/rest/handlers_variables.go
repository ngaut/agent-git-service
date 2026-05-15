package rest

import (
	"net/http"

	"gh-server/internal/rest/respond"
	"gh-server/internal/rest/transform"
)

// ─── Repo Variables ────────────────────────────────────────────────────────

// ListRepoVariables handles GET /repos/{owner}/{repo}/actions/variables
func (d *Deps) ListRepoVariables(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	d.listVariables(w, r, repo.ID, "")
}

// CreateRepoVariable handles POST /repos/{owner}/{repo}/actions/variables
func (d *Deps) CreateRepoVariable(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	d.createVariable(w, r, &repo.ID, repo.OwnerID, "")
}

// GetRepoVariable handles GET /repos/{owner}/{repo}/actions/variables/{name}
func (d *Deps) GetRepoVariable(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	d.getVariable(w, r, repo.ID, "")
}

// UpdateRepoVariable handles PATCH /repos/{owner}/{repo}/actions/variables/{name}
func (d *Deps) UpdateRepoVariable(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	d.updateVariable(w, r, repo.ID, "")
}

// DeleteRepoVariable handles DELETE /repos/{owner}/{repo}/actions/variables/{name}
func (d *Deps) DeleteRepoVariable(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	d.deleteVariable(w, r, repo.ID, "")
}

// ─── Org Variables ─────────────────────────────────────────────────────────

// ListOrgVariables handles GET /orgs/{org}/actions/variables
func (d *Deps) ListOrgVariables(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	d.listOrgVariables(w, r, org.ID)
}

// CreateOrgVariable handles POST /orgs/{org}/actions/variables
func (d *Deps) CreateOrgVariable(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	d.createVariable(w, r, nil, org.ID, "")
}

// GetOrgVariable handles GET /orgs/{org}/actions/variables/{name}
func (d *Deps) GetOrgVariable(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	d.getOrgVariable(w, r, org.ID)
}

// UpdateOrgVariable handles PATCH /orgs/{org}/actions/variables/{name}
func (d *Deps) UpdateOrgVariable(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	d.updateOrgVariable(w, r, org.ID)
}

// DeleteOrgVariable handles DELETE /orgs/{org}/actions/variables/{name}
func (d *Deps) DeleteOrgVariable(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	d.deleteOrgVariable(w, r, org.ID)
}

// ─── Env Variables ─────────────────────────────────────────────────────────

// ListEnvVariables handles GET /repos/{owner}/{repo}/environments/{env}/variables
func (d *Deps) ListEnvVariables(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	env := pathParam(r, "environment_name")
	d.listVariables(w, r, repo.ID, env)
}

// CreateEnvVariable handles POST /repos/{owner}/{repo}/environments/{env}/variables
func (d *Deps) CreateEnvVariable(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	env := pathParam(r, "environment_name")
	d.createVariable(w, r, &repo.ID, repo.OwnerID, env)
}

// GetEnvVariable handles GET /repos/{owner}/{repo}/environments/{env}/variables/{name}
func (d *Deps) GetEnvVariable(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	env := pathParam(r, "environment_name")
	d.getVariable(w, r, repo.ID, env)
}

// UpdateEnvVariable handles PATCH /repos/{owner}/{repo}/environments/{env}/variables/{name}
func (d *Deps) UpdateEnvVariable(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	env := pathParam(r, "environment_name")
	d.updateVariable(w, r, repo.ID, env)
}

// DeleteEnvVariable handles DELETE /repos/{owner}/{repo}/environments/{env}/variables/{name}
func (d *Deps) DeleteEnvVariable(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	env := pathParam(r, "environment_name")
	d.deleteVariable(w, r, repo.ID, env)
}

// ─── Variable internals ────────────────────────────────────────────────────

func (d *Deps) listVariables(w http.ResponseWriter, r *http.Request, repoID uint, env string) {
	vars, err := d.Svc.ListVariables(r.Context(), repoID, env)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]map[string]any, len(vars))
	for i, v := range vars {
		out[i] = transform.Variable(v)
	}
	respond.JSON(w, 200, map[string]any{"total_count": len(out), "variables": out})
}

func (d *Deps) listOrgVariables(w http.ResponseWriter, r *http.Request, orgID uint) {
	vars, err := d.Svc.ListOrgVariables(r.Context(), orgID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]map[string]any, len(vars))
	for i, v := range vars {
		out[i] = transform.Variable(v)
	}
	respond.JSON(w, 200, map[string]any{"total_count": len(out), "variables": out})
}

func (d *Deps) createVariable(w http.ResponseWriter, r *http.Request, repoID *uint, ownerID uint, env string) {
	var body struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	v, err := d.Svc.CreateVariable(r.Context(), ownerID, repoID, env, body.Name, body.Value)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusCreated, transform.Variable(v))
}

func (d *Deps) getVariable(w http.ResponseWriter, r *http.Request, repoID uint, env string) {
	name := pathParam(r, "name")
	v, err := d.Svc.GetVariable(r.Context(), repoID, env, name)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.Variable(v))
}

func (d *Deps) getOrgVariable(w http.ResponseWriter, r *http.Request, orgID uint) {
	name := pathParam(r, "name")
	v, err := d.Svc.GetOrgVariable(r.Context(), orgID, name)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.Variable(v))
}

func (d *Deps) updateVariable(w http.ResponseWriter, r *http.Request, repoID uint, env string) {
	name := pathParam(r, "name")
	var body struct {
		Value string `json:"value"`
	}
	decodeBody(r, &body)
	if err := d.Svc.UpdateVariable(r.Context(), repoID, env, name, body.Value); err != nil {
		// Not found is the most likely error if update failed
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

func (d *Deps) updateOrgVariable(w http.ResponseWriter, r *http.Request, orgID uint) {
	name := pathParam(r, "name")
	var body struct {
		Value string `json:"value"`
	}
	decodeBody(r, &body)
	if err := d.Svc.UpdateOrgVariable(r.Context(), orgID, name, body.Value); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

func (d *Deps) deleteVariable(w http.ResponseWriter, r *http.Request, repoID uint, env string) {
	name := pathParam(r, "name")
	if err := d.Svc.DeleteVariable(r.Context(), repoID, env, name); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

func (d *Deps) deleteOrgVariable(w http.ResponseWriter, r *http.Request, orgID uint) {
	name := pathParam(r, "name")
	if err := d.Svc.DeleteOrgVariable(r.Context(), orgID, name); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}
