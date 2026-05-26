package transform

import (
	"fmt"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
)

// Workflow converts a db.Workflow to GitHub REST API JSON.
func Workflow(wf db.Workflow, repoFullName string) map[string]any {
	return map[string]any{
		"id":         wf.ID,
		"node_id":    nodeID("Workflow", wf.ID),
		"name":       wf.Name,
		"path":       wf.Path,
		"state":      wf.State,
		"created_at": wf.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at": wf.UpdatedAt.UTC().Format(time.RFC3339),
		"url":        fmt.Sprintf("%s/repos/%s/actions/workflows/%d", apiBase(), repoFullName, wf.ID),
	}
}

// WorkflowRun converts a db.WorkflowRun to GitHub REST API JSON.
func WorkflowRun(run db.WorkflowRun, repoFullName string, workflowPath ...string) map[string]any {
	var wfPath string
	if len(workflowPath) > 0 {
		wfPath = workflowPath[0]
	}
	runURL := actionRunURL(repoFullName, run.ID)
	parts := strings.SplitN(repoFullName, "/", 2)
	owner, name := parts[0], parts[1]
	return map[string]any{
		"id":             run.ID,
		"name":           run.Name,
		"node_id":        nodeID("WorkflowRun", run.ID),
		"head_branch":    run.HeadBranch,
		"head_sha":       run.HeadSHA,
		"run_number":     run.RunNumber,
		"run_attempt":    run.RunAttempt,
		"event":          run.Event,
		"status":         run.Status,
		"conclusion":     run.Conclusion,
		"workflow_id":    run.WorkflowID,
		"url":            runURL,
		"html_url":       fmt.Sprintf("%s/%s/actions/runs/%d", htmlBase(), repoFullName, run.ID),
		"created_at":     run.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":     run.UpdatedAt.UTC().Format(time.RFC3339),
		"run_started_at": run.CreatedAt.UTC().Format(time.RFC3339),
		"jobs_url":       runURL + "/jobs",
		"logs_url":       runURL + "/logs",
		"artifacts_url":  runURL + "/artifacts",
		"cancel_url":     runURL + "/cancel",
		"rerun_url":      runURL + "/rerun",
		"head_commit": map[string]any{
			"id":      run.HeadSHA,
			"message": run.Name,
		},
		"head_repository": map[string]any{
			"id":        run.RepositoryID,
			"name":      name,
			"full_name": repoFullName,
			"owner":     map[string]any{"login": owner},
		},
		"repository": map[string]any{
			"id":        run.RepositoryID,
			"name":      name,
			"full_name": repoFullName,
			"owner":     map[string]any{"login": owner},
		},
		"display_title": run.Name,
		"path":          wfPath,
	}
}

// WorkflowRunJob converts a db.WorkflowRunJob to GitHub REST API JSON.
func WorkflowRunJob(j db.WorkflowRunJob, repoFullName ...string) map[string]any {
	var htmlURL string
	if len(repoFullName) > 0 && repoFullName[0] != "" {
		htmlURL = fmt.Sprintf("%s/%s/actions/runs/%d/job/%d", htmlBase(), repoFullName[0], j.RunID, j.ID)
	}
	return map[string]any{
		"id":           j.ID,
		"run_id":       j.RunID,
		"name":         j.Name,
		"status":       j.Status,
		"conclusion":   j.Conclusion,
		"html_url":     htmlURL,
		"started_at":   j.StartedAt.UTC().Format(time.RFC3339),
		"completed_at": j.CompletedAt.UTC().Format(time.RFC3339),
		"steps": []map[string]any{
			{
				"name":         j.Name,
				"status":       j.Status,
				"conclusion":   j.Conclusion,
				"number":       1,
				"started_at":   j.StartedAt.UTC().Format(time.RFC3339),
				"completed_at": j.CompletedAt.UTC().Format(time.RFC3339),
			},
		},
	}
}

// Artifact converts a db.Artifact to GitHub REST API JSON.
func Artifact(art db.Artifact, repoFullName string) map[string]any {
	return map[string]any{
		"id":                   art.ID,
		"node_id":              nodeID("Artifact", art.ID),
		"name":                 art.Name,
		"size_in_bytes":        art.SizeInBytes,
		"expired":              art.Expired,
		"created_at":           art.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":           art.UpdatedAt.UTC().Format(time.RFC3339),
		"archive_download_url": fmt.Sprintf("%s/repos/%s/actions/artifacts/%d/zip", apiBase(), repoFullName, art.ID),
	}
}

// ActionCache converts a db.ActionCache to GitHub REST API JSON.
func ActionCache(c db.ActionCache) map[string]any {
	return map[string]any{
		"id":               c.ID,
		"ref":              c.Ref,
		"key":              c.Key,
		"version":          c.Version,
		"size_in_bytes":    c.SizeInBytes,
		"created_at":       c.CreatedAt.UTC().Format(time.RFC3339),
		"last_accessed_at": c.LastAccessedAt.UTC().Format(time.RFC3339),
	}
}
