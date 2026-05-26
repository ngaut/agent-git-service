package rest

import (
	"archive/zip"
	"bytes"
	"fmt"
	"math"
	"net/http"

	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
)

// ─── Jobs & Artifacts ──────────────────────────────────────────────────────
// GetWorkflowJob handles GET /repos/{owner}/{repo}/actions/jobs/{job_id}
func (d *Deps) GetWorkflowJob(w http.ResponseWriter, r *http.Request) {
	jobID, ok := mustUintParam(w, r, "job_id")
	if !ok {
		return
	}
	job, err := d.Svc.GetWorkflowRunJob(r.Context(), jobID)
	if err != nil {
		respond.NotFound(w)
		return
	}
	respond.JSON(w, 200, transform.WorkflowRunJob(job, repoFullName(r)))
}

// GetWorkflowJobLogs handles GET /repos/{owner}/{repo}/actions/jobs/{job_id}/logs
func (d *Deps) GetWorkflowJobLogs(w http.ResponseWriter, r *http.Request) {
	jobID, ok := mustUintParam(w, r, "job_id")
	if !ok {
		return
	}

	job, err := d.Svc.GetWorkflowRunJob(r.Context(), jobID)
	if err != nil {
		respond.NotFound(w)
		return
	}

	logContent := job.Logs
	if len(logContent) == 0 {
		logContent = []byte("(no logs)\n")
	}

	w.Header().Set("Content-Type", "text/plain;charset=utf-8")
	_, _ = w.Write(logContent)
}

// ListRepoArtifacts handles GET /repos/{owner}/{repo}/actions/artifacts
func (d *Deps) ListRepoArtifacts(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	artifacts, err := d.Svc.ListRepoArtifacts(r.Context(), repo.FullName)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	nodes := make([]map[string]any, len(artifacts))
	for i, art := range artifacts {
		nodes[i] = transform.Artifact(art, repo.FullName)
	}
	respond.JSON(w, 200, map[string]any{"total_count": len(nodes), "artifacts": nodes})
}

// ListWorkflowRunArtifacts handles GET /repos/{owner}/{repo}/actions/runs/{run_id}/artifacts
func (d *Deps) ListWorkflowRunArtifacts(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	runID, ok := mustIntParam(w, r, "run_id")
	if !ok {
		return
	}

	artifacts, err := d.Svc.ListWorkflowRunArtifacts(r.Context(), repo.FullName, runID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	nodes := make([]map[string]any, len(artifacts))
	for i, art := range artifacts {
		nodes[i] = transform.Artifact(art, repo.FullName)
	}
	respond.JSON(w, 200, map[string]any{"total_count": len(nodes), "artifacts": nodes})
}

// DownloadArtifact handles GET /repos/{owner}/{repo}/actions/artifacts/{artifact_id}/zip
func (d *Deps) DownloadArtifact(w http.ResponseWriter, r *http.Request) {
	fullName := repoFullName(r)
	artifactID, ok := mustIntParam(w, r, "artifact_id")
	if !ok {
		return
	}

	art, err := d.Svc.GetArtifact(r.Context(), fullName, artifactID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s.zip", art.Name))

	if len(art.Content) > 0 {
		_, _ = w.Write(art.Content)
		return
	}

	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)
	_ = zipWriter.Close()
	_, _ = w.Write(buf.Bytes())
}

// ListWorkflowRunJobs handles GET /repos/{owner}/{repo}/actions/runs/{run_id}/jobs
func (d *Deps) ListWorkflowRunJobs(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	runID, ok := mustIntParam(w, r, "run_id")
	if !ok {
		return
	}

	jobs, err := d.Svc.ListWorkflowRunJobs(r.Context(), repo.FullName, runID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	nodes := make([]map[string]any, len(jobs))
	for i, j := range jobs {
		nodes[i] = transform.WorkflowRunJob(j, repo.FullName)
	}
	respond.JSON(w, 200, map[string]any{"total_count": len(nodes), "jobs": nodes})
}

// ForceCancelWorkflowRun handles POST /repos/{owner}/{repo}/actions/runs/{run_id}/force-cancel
func (d *Deps) ForceCancelWorkflowRun(w http.ResponseWriter, r *http.Request) {
	// Force-cancel uses the same logic as cancel on our local server.
	d.CancelWorkflowRun(w, r)
}

// RerunWorkflowRunJob handles POST /repos/{owner}/{repo}/actions/jobs/{job_id}/rerun
func (d *Deps) RerunWorkflowRunJob(w http.ResponseWriter, r *http.Request) {
	// On our local server, rerunning a job reruns the entire run.
	jobID, ok := mustUintParam(w, r, "job_id")
	if !ok {
		return
	}
	job, err := d.Svc.GetWorkflowRunJob(r.Context(), jobID)
	if err != nil {
		respond.NotFound(w)
		return
	}
	fullName := repoFullName(r)
	runID, err := safeUintToInt(job.RunID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	err = d.Svc.RerunWorkflowRun(r.Context(), fullName, runID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// GetWorkflowRunByAttempt handles GET /repos/{owner}/{repo}/actions/runs/{run_id}/attempts/{attempt_number}
func (d *Deps) GetWorkflowRunByAttempt(w http.ResponseWriter, r *http.Request) {
	// Our server stores a single attempt per run, so delegate to the standard handler.
	d.GetWorkflowRun(w, r)
}

// ListWorkflowRunJobsByAttempt handles GET /repos/{owner}/{repo}/actions/runs/{run_id}/attempts/{attempt_number}/jobs
func (d *Deps) ListWorkflowRunJobsByAttempt(w http.ResponseWriter, r *http.Request) {
	// Our server stores a single attempt per run, so delegate to the standard handler.
	d.ListWorkflowRunJobs(w, r)
}

// GetWorkflowRunLogsByAttempt handles GET /repos/{owner}/{repo}/actions/runs/{run_id}/attempts/{attempt_number}/logs
func (d *Deps) GetWorkflowRunLogsByAttempt(w http.ResponseWriter, r *http.Request) {
	// Our server stores a single attempt per run, so delegate to the standard handler.
	d.GetWorkflowRunLogs(w, r)
}

// ─── Helpers ───────────────────────────────────────────────────────────────

// safeUintToInt converts uint to int with bounds checking.
// Returns an error if the value exceeds max int.
func safeUintToInt(u uint) (int, error) {
	if u > math.MaxInt {
		return 0, fmt.Errorf("value %d exceeds max int %d", u, math.MaxInt)
	}
	return int(u), nil
}
