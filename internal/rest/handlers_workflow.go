package rest

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"

	"gh-server/internal/rest/respond"
	"gh-server/internal/rest/transform"
)

// ─── Workflow scanning ─────────────────────────────────────────────────────

// ─── Workflow endpoints ────────────────────────────────────────────────────

// ListWorkflows handles GET /repos/{owner}/{repo}/actions/workflows
func (d *Deps) ListWorkflows(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}

	workflows, err := d.Svc.ListWorkflows(r.Context(), repo.FullName)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	nodes := make([]map[string]any, len(workflows))
	for i, wf := range workflows {
		nodes[i] = transform.Workflow(wf, repo.FullName)
	}
	respond.JSON(w, 200, map[string]any{"total_count": len(nodes), "workflows": nodes})
}

// GetWorkflow handles GET /repos/{owner}/{repo}/actions/workflows/{workflow_id}
// workflow_id can be a numeric ID or a filename (e.g., "ci.yml")
func (d *Deps) GetWorkflow(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	workflowIDOrName := pathParam(r, "workflow_id")
	if workflowIDOrName == "" {
		respond.ValidationFailed(w, "workflow_id is required")
		return
	}

	wf, err := d.Svc.FindWorkflow(r.Context(), repo.FullName, workflowIDOrName)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.Workflow(*wf, repo.FullName))
}

// EnableWorkflow handles PUT /repos/{owner}/{repo}/actions/workflows/{workflow_id}/enable
// workflow_id can be a numeric ID or a filename (e.g., "ci.yml")
func (d *Deps) EnableWorkflow(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	workflowIDOrName := pathParam(r, "workflow_id")
	if workflowIDOrName == "" {
		respond.ValidationFailed(w, "workflow_id is required")
		return
	}

	wf, err := d.Svc.FindWorkflow(r.Context(), repo.FullName, workflowIDOrName)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	err = d.Svc.EnableWorkflow(r.Context(), repo.FullName, int(wf.ID))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DisableWorkflow handles PUT /repos/{owner}/{repo}/actions/workflows/{workflow_id}/disable
// workflow_id can be a numeric ID or a filename (e.g., "ci.yml")
func (d *Deps) DisableWorkflow(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	workflowIDOrName := pathParam(r, "workflow_id")
	if workflowIDOrName == "" {
		respond.ValidationFailed(w, "workflow_id is required")
		return
	}

	wf, err := d.Svc.FindWorkflow(r.Context(), repo.FullName, workflowIDOrName)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	err = d.Svc.DisableWorkflow(r.Context(), repo.FullName, int(wf.ID))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DispatchWorkflow handles POST /repos/{owner}/{repo}/actions/workflows/{workflow_id}/dispatches
// workflow_id can be a numeric ID or a filename (e.g., "ci.yml")
func (d *Deps) DispatchWorkflow(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	workflowIDOrName := pathParam(r, "workflow_id")
	if workflowIDOrName == "" {
		respond.ValidationFailed(w, "workflow_id is required")
		return
	}

	wf, err := d.Svc.FindWorkflow(r.Context(), repo.FullName, workflowIDOrName)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	var payload struct {
		Ref    string         `json:"ref"`
		Inputs map[string]any `json:"inputs"`
	}
	if err := decodeBodyStrict(r, &payload); err != nil {
		respond.Error(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// In a real app we would use payload.Inputs but for acceptance tests this is enough.
	_, err = d.Svc.DispatchWorkflow(r.Context(), repo.FullName, int(wf.ID), payload.Ref, payload.Inputs)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// CreateRepositoryDispatch handles POST /api/v3/repos/{owner}/{repo}/dispatches
func (d *Deps) CreateRepositoryDispatch(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}

	var payload struct {
		EventType     string `json:"event_type"`
		ClientPayload any    `json:"client_payload"`
	}
	if err := decodeBodyStrict(r, &payload); err != nil || payload.EventType == "" {
		respond.ValidationFailed(w, "event_type is required")
		return
	}

	// Not fully implemented on the backend as workflow engine just mocks right now
	// but standard GitHub returns 204 No Content for a successful dispatch
	w.WriteHeader(http.StatusNoContent)
}

// ─── Run endpoints ─────────────────────────────────────────────────────────

// ListWorkflowRuns handles GET /repos/{owner}/{repo}/actions/runs
func (d *Deps) ListWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}

	runs, err := d.Svc.ListWorkflowRuns(r.Context(), repo.FullName, 0) // 0 for workflowID means list all runs for the repo
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	nodes := make([]map[string]any, len(runs))
	for i, run := range runs {
		nodes[i] = transform.WorkflowRun(run, repo.FullName)
	}
	respond.JSON(w, 200, map[string]any{"total_count": len(nodes), "workflow_runs": nodes})
}

// ListWorkflowRunsByWorkflow handles GET /repos/{owner}/{repo}/actions/workflows/{workflow_id}/runs
func (d *Deps) ListWorkflowRunsByWorkflow(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	workflowID, ok := mustIntParam(w, r, "workflow_id")
	if !ok {
		return
	}

	runs, err := d.Svc.ListWorkflowRuns(r.Context(), repo.FullName, workflowID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	nodes := make([]map[string]any, len(runs))
	for i, run := range runs {
		nodes[i] = transform.WorkflowRun(run, repo.FullName)
	}
	respond.JSON(w, 200, map[string]any{"total_count": len(nodes), "workflow_runs": nodes})
}

// GetWorkflowRun handles GET /repos/{owner}/{repo}/actions/runs/{run_id}
func (d *Deps) GetWorkflowRun(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	runID, ok := mustIntParam(w, r, "run_id")
	if !ok {
		return
	}

	run, err := d.Svc.GetWorkflowRun(r.Context(), repo.FullName, runID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.WorkflowRun(run, repo.FullName))
}

// CancelWorkflowRun handles POST /repos/{owner}/{repo}/actions/runs/{run_id}/cancel
func (d *Deps) CancelWorkflowRun(w http.ResponseWriter, r *http.Request) {
	fullName := repoFullName(r)
	runID, ok := mustIntParam(w, r, "run_id")
	if !ok {
		return
	}

	err := d.Svc.CancelWorkflowRun(r.Context(), fullName, runID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 202, map[string]any{})
}

// DeleteWorkflowRun handles DELETE /repos/{owner}/{repo}/actions/runs/{run_id}
func (d *Deps) DeleteWorkflowRun(w http.ResponseWriter, r *http.Request) {
	fullName := repoFullName(r)
	runID, ok := mustIntParam(w, r, "run_id")
	if !ok {
		return
	}

	err := d.Svc.DeleteWorkflowRun(r.Context(), fullName, runID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// RerunWorkflowRun handles POST /repos/{owner}/{repo}/actions/runs/{run_id}/rerun
func (d *Deps) RerunWorkflowRun(w http.ResponseWriter, r *http.Request) {
	fullName := repoFullName(r)
	runID, ok := mustIntParam(w, r, "run_id")
	if !ok {
		return
	}

	err := d.Svc.RerunWorkflowRun(r.Context(), fullName, runID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// GetWorkflowRunLogs handles GET /repos/{owner}/{repo}/actions/runs/{run_id}/logs
// The zip format expected by the gh CLI is:
//
//	{ordinal}_{jobname}.txt   — whole-job log (e.g. "0_build.txt")
func (d *Deps) GetWorkflowRunLogs(w http.ResponseWriter, r *http.Request) {
	fullName := repoFullName(r)
	runID, ok := mustIntParam(w, r, "run_id")
	if !ok {
		return
	}

	_, err := d.Svc.GetWorkflowRun(r.Context(), fullName, runID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	jobs, err := d.Svc.ListWorkflowRunJobs(r.Context(), fullName, runID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i, j := range jobs {
		name := fmt.Sprintf("%d_%s.txt", i, j.Name)
		entry, _ := zw.Create(name)
		logContent := j.Logs
		if len(logContent) == 0 {
			logContent = []byte("(no logs)\n")
		}
		_, _ = entry.Write(logContent)
	}
	_ = zw.Close()

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=logs.zip")
	_, _ = w.Write(buf.Bytes())
}
