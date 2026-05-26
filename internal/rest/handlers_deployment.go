package rest

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
)

// deploymentJSON transforms db.Deployment into the GitHub API response shape.
func (d *Deps) deploymentJSON(r *http.Request, dep db.Deployment) map[string]any {
	var payload map[string]any
	if dep.PayloadJSON != "" {
		if err := json.Unmarshal([]byte(dep.PayloadJSON), &payload); err != nil {
			slog.Warn("failed to unmarshal PayloadJSON", "error", err, "deployment_id", dep.ID)
		}
	} else {
		payload = map[string]any{}
	}
	var creator any
	if dep.Creator.ID != 0 {
		creator = transform.User(dep.Creator)
	}

	sha := dep.Ref
	if d.Svc.Git != nil {
		if s, err := d.Svc.Git.HeadSHA(r.Context(), dep.Repository.FullName, dep.Ref); err == nil && s != "" {
			sha = s
		}
	}

	repoURL := fmt.Sprintf("%s/repos/%s", transform.APIBase(), dep.Repository.FullName)
	return map[string]any{
		"id":                     dep.ID,
		"sha":                    sha, // Resolved via Git service, falling back to ref
		"ref":                    dep.Ref,
		"task":                   dep.Task,
		"payload":                payload,
		"environment":            dep.Environment,
		"description":            dep.Description,
		"creator":                creator,
		"created_at":             dep.CreatedAt.Format(time.RFC3339),
		"updated_at":             dep.UpdatedAt.Format(time.RFC3339),
		"statuses_url":           fmt.Sprintf("%s/deployments/%d/statuses", repoURL, dep.ID),
		"repository_url":         repoURL,
		"transient_environment":  dep.Transient,
		"production_environment": dep.Production,
	}
}

// deploymentStatusJSON transforms db.DeploymentStatus into the standard shape.
func deploymentStatusJSON(s db.DeploymentStatus) map[string]any {
	var creator any
	if s.Creator.ID != 0 {
		creator = transform.User(s.Creator)
	}
	repoURL := fmt.Sprintf("%s/repos/%s", transform.APIBase(), s.Deployment.Repository.FullName)
	return map[string]any{
		"id":              s.ID,
		"state":           s.State,
		"creator":         creator,
		"description":     s.Description,
		"environment_url": s.EnvironmentURL,
		"log_url":         s.LogURL,
		"deployment_url":  fmt.Sprintf("%s/deployments/%d", repoURL, s.DeploymentID),
		"repository_url":  repoURL,
		"created_at":      s.CreatedAt.Format(time.RFC3339),
		"updated_at":      s.UpdatedAt.Format(time.RFC3339),
	}
}

// CreateDeployment handles POST /api/v3/repos/{owner}/{repo}/deployments
func (d *Deps) CreateDeployment(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	user, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.Unauthorized(w, "Bad credentials")
		return
	}

	var body struct {
		Ref                   string         `json:"ref"`
		Task                  string         `json:"task"`
		Environment           string         `json:"environment"`
		Description           string         `json:"description"`
		Payload               map[string]any `json:"payload"`
		TransientEnvironment  bool           `json:"transient_environment"`
		ProductionEnvironment bool           `json:"production_environment"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid JSON")
		return
	}
	if body.Ref == "" {
		respond.ValidationFailed(w, "ref is required")
		return
	}

	task := body.Task
	if task == "" {
		task = "deploy"
	}
	env := body.Environment
	if env == "" {
		env = "production"
	}
	payloadJSON := ""
	if body.Payload != nil {
		if b, err := json.Marshal(body.Payload); err == nil {
			payloadJSON = string(b)
		}
	}

	dep := db.Deployment{
		RepositoryID: repo.ID,
		Repository:   *repo,
		Ref:          body.Ref,
		Task:         task,
		Environment:  env,
		Description:  body.Description,
		PayloadJSON:  payloadJSON,
		Transient:    body.TransientEnvironment,
		Production:   body.ProductionEnvironment,
		CreatorID:    user.ID,
		Creator:      user,
	}

	if err := d.Svc.CreateDeployment(r.Context(), &dep); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, d.deploymentJSON(r, dep))
}

// ListDeployments handles GET /api/v3/repos/{owner}/{repo}/deployments
func (d *Deps) ListDeployments(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	deps, err := d.Svc.ListDeployments(r.Context(), repo.ID)
	if err != nil {
		respond.JSON(w, 200, []any{})
		return
	}
	var out []any
	for _, dep := range deps {
		dep.Repository = *repo
		out = append(out, d.deploymentJSON(r, dep))
	}
	if out == nil {
		out = []any{}
	}
	respond.JSON(w, 200, out)
}

// CreateDeploymentStatus handles POST /api/v3/repos/{owner}/{repo}/deployments/{deployment_id}/statuses
func (d *Deps) CreateDeploymentStatus(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	user, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.Unauthorized(w, "Bad credentials")
		return
	}
	depID, ok := mustIntParam(w, r, "deployment_id")
	if !ok {
		return
	}

	// Verify deployment belongs to this repo
	dep, err := d.Svc.GetDeployment(r.Context(), repo.ID, uint(depID))
	if err != nil {
		respond.NotFound(w)
		return
	}
	dep.Repository = *repo

	var body struct {
		State          string `json:"state"`
		LogURL         string `json:"log_url"`
		Description    string `json:"description"`
		EnvironmentURL string `json:"environment_url"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid JSON")
		return
	}
	if body.State == "" {
		respond.ValidationFailed(w, "state is required")
		return
	}

	ds := db.DeploymentStatus{
		DeploymentID:   dep.ID,
		Deployment:     *dep,
		State:          body.State,
		LogURL:         body.LogURL,
		Description:    body.Description,
		EnvironmentURL: body.EnvironmentURL,
		CreatorID:      user.ID,
		Creator:        user,
	}
	if err := d.Svc.CreateDeploymentStatus(r.Context(), &ds); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	respond.JSON(w, 201, deploymentStatusJSON(ds))
}

// ListDeploymentStatuses handles GET /api/v3/repos/{owner}/{repo}/deployments/{deployment_id}/statuses
func (d *Deps) ListDeploymentStatuses(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	depID, ok := mustIntParam(w, r, "deployment_id")
	if !ok {
		return
	}
	// Verify deployment belongs to this repo
	dep, err := d.Svc.GetDeployment(r.Context(), repo.ID, uint(depID))
	if err != nil {
		respond.NotFound(w)
		return
	}
	dep.Repository = *repo

	statuses, err := d.Svc.ListDeploymentStatuses(r.Context(), repo.ID, dep.ID)
	if err != nil {
		respond.JSON(w, 200, []any{})
		return
	}
	var out []any
	for _, s := range statuses {
		s.Deployment = *dep
		out = append(out, deploymentStatusJSON(s))
	}
	if out == nil {
		out = []any{}
	}
	respond.JSON(w, 200, out)
}
