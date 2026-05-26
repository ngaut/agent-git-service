package rest

import (
	"net/http"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/service"
)

// ─── Environments ───────────────────────────────────────────────────────────

// CreateOrUpdateEnvironment handles PUT /repos/{owner}/{repo}/environments/{env}
func (d *Deps) CreateOrUpdateEnvironment(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	d.upsertEnvironment(w, r, repo)
}

// ListEnvironments handles GET /repos/{owner}/{repo}/environments
func (d *Deps) ListEnvironments(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	d.listEnvironments(w, r, repo)
}

// GetEnvironment handles GET /repos/{owner}/{repo}/environments/{env}
func (d *Deps) GetEnvironment(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	d.getEnvironment(w, r, repo)
}

// DeleteEnvironment handles DELETE /repos/{owner}/{repo}/environments/{env}
func (d *Deps) DeleteEnvironment(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	d.deleteEnvironment(w, r, repo)
}

// ─── Env endpoints via /repositories/{repo_id} ─────────────────────────────

// ListEnvVariablesByRepoID handles GET /api/v3/repositories/{repo_id}/environments/{environment_name}/variables
// and lists environment variables for the repo and environment.
func (d *Deps) ListEnvVariablesByRepoID(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepoByID(w, r)
	if repo == nil {
		return
	}
	env := pathParam(r, "environment_name")
	d.listVariables(w, r, repo.ID, env)
}

// CreateEnvVariableByRepoID handles POST /api/v3/repositories/{repo_id}/environments/{environment_name}/variables
// and creates a new environment variable for the repo and environment.
func (d *Deps) CreateEnvVariableByRepoID(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepoByID(w, r)
	if repo == nil {
		return
	}
	env := pathParam(r, "environment_name")
	d.createVariable(w, r, &repo.ID, repo.OwnerID, env)
}

// GetEnvVariableByRepoID handles GET /api/v3/repositories/{repo_id}/environments/{environment_name}/variables/{name}
// and returns a single environment variable by name.
func (d *Deps) GetEnvVariableByRepoID(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepoByID(w, r)
	if repo == nil {
		return
	}
	env := pathParam(r, "environment_name")
	d.getVariable(w, r, repo.ID, env)
}

// UpdateEnvVariableByRepoID handles PATCH /api/v3/repositories/{repo_id}/environments/{environment_name}/variables/{name}
// and updates the named environment variable.
func (d *Deps) UpdateEnvVariableByRepoID(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepoByID(w, r)
	if repo == nil {
		return
	}
	env := pathParam(r, "environment_name")
	d.updateVariable(w, r, repo.ID, env)
}

// DeleteEnvVariableByRepoID handles DELETE /api/v3/repositories/{repo_id}/environments/{environment_name}/variables/{name}
// and deletes the named environment variable.
func (d *Deps) DeleteEnvVariableByRepoID(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepoByID(w, r)
	if repo == nil {
		return
	}
	env := pathParam(r, "environment_name")
	d.deleteVariable(w, r, repo.ID, env)
}

func (d *Deps) ListEnvSecretsByRepoID(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepoByID(w, r)
	if repo == nil {
		return
	}
	env := pathParam(r, "environment_name")
	d.listSecrets(w, r, repo.ID, env)
}

func (d *Deps) GetEnvPublicKeyByRepoID(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, 200, publicKeyJSON())
}

func (d *Deps) CreateOrUpdateEnvSecretByRepoID(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepoByID(w, r)
	if repo == nil {
		return
	}
	env := pathParam(r, "environment_name")
	d.upsertSecret(w, r, &repo.ID, repo.OwnerID, env)
}

func (d *Deps) DeleteEnvSecretByRepoID(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepoByID(w, r)
	if repo == nil {
		return
	}
	env := pathParam(r, "environment_name")
	d.deleteSecret(w, r, repo.ID, env)
}

func (d *Deps) CreateOrUpdateEnvironmentByRepoID(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepoByID(w, r)
	if repo == nil {
		return
	}
	d.upsertEnvironment(w, r, repo)
}

func (d *Deps) ListEnvironmentsByRepoID(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepoByID(w, r)
	if repo == nil {
		return
	}
	d.listEnvironments(w, r, repo)
}

func (d *Deps) GetEnvironmentByRepoID(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepoByID(w, r)
	if repo == nil {
		return
	}
	d.getEnvironment(w, r, repo)
}

func (d *Deps) DeleteEnvironmentByRepoID(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepoByID(w, r)
	if repo == nil {
		return
	}
	d.deleteEnvironment(w, r, repo)
}

func (d *Deps) listEnvironments(w http.ResponseWriter, r *http.Request, repo *db.Repository) {
	envs, err := d.Svc.ListEnvironments(r.Context(), repo.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]map[string]any, len(envs))
	for i, env := range envs {
		out[i] = transform.Environment(env, repo.FullName)
	}
	respond.JSON(w, http.StatusOK, map[string]any{
		"total_count":  len(out),
		"environments": out,
	})
}

func (d *Deps) getEnvironment(w http.ResponseWriter, r *http.Request, repo *db.Repository) {
	envName := pathParam(r, "environment_name")
	env, err := d.Svc.GetEnvironment(r.Context(), repo.ID, envName)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusOK, transform.Environment(env, repo.FullName))
}

func (d *Deps) deleteEnvironment(w http.ResponseWriter, r *http.Request, repo *db.Repository) {
	envName := pathParam(r, "environment_name")
	if err := d.Svc.DeleteEnvironment(r.Context(), repo.ID, envName); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

func (d *Deps) upsertEnvironment(w http.ResponseWriter, r *http.Request, repo *db.Repository) {
	envName := pathParam(r, "environment_name")
	var body struct {
		WaitTimer              *int                       `json:"wait_timer"`
		Reviewers              []map[string]any           `json:"reviewers"`
		DeploymentBranchPolicy *service.EnvironmentPolicy `json:"deployment_branch_policy"`
	}
	if r.ContentLength > 0 {
		if err := decodeBodyStrict(r, &body); err != nil {
			respond.ValidationFailed(w, "invalid body")
			return
		}
	}
	protectionRules := []map[string]any{}
	if body.WaitTimer != nil {
		protectionRules = append(protectionRules, map[string]any{
			"type":       "wait_timer",
			"wait_timer": *body.WaitTimer,
		})
	}
	if len(body.Reviewers) > 0 {
		protectionRules = append(protectionRules, map[string]any{
			"type":      "required_reviewers",
			"reviewers": body.Reviewers,
		})
	}
	env, _, err := d.Svc.UpsertEnvironment(r.Context(), service.UpsertEnvironmentInput{
		RepositoryID:           repo.ID,
		Name:                   envName,
		ProtectionRules:        protectionRules,
		DeploymentBranchPolicy: body.DeploymentBranchPolicy,
	})
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusOK, transform.Environment(env, repo.FullName))
}
