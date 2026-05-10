package rest_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/service"
	"gh-server/internal/testharness"
)

func seedCheckRunsRepo(t *testing.T, h *testharness.Harness, name string) (db.Repository, string) {
	t.Helper()

	ctx := context.Background()
	repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       name,
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	headSHA, err := h.Svc.Git.HeadSHA(ctx, repo.FullName, repo.DefaultBranch)
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	return repo, headSHA
}

func seedWorkflowCheckRun(t *testing.T, h *testharness.Harness, repo db.Repository, headSHA string) (db.WorkflowRun, db.WorkflowRunJob) {
	t.Helper()

	wf := db.Workflow{
		RepositoryID: repo.ID,
		Name:         "CI",
		Path:         ".github/workflows/ci.yml",
		State:        db.WorkflowActive,
	}
	if err := h.DB.Create(&wf).Error; err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	run := db.WorkflowRun{
		RepositoryID: repo.ID,
		WorkflowID:   wf.ID,
		Name:         wf.Name,
		HeadBranch:   repo.DefaultBranch,
		HeadSHA:      headSHA,
		Status:       db.RunCompleted,
		Conclusion:   db.ConclusionSuccess,
		Event:        "push",
		RunNumber:    1,
		RunAttempt:   1,
	}
	if err := h.DB.Create(&run).Error; err != nil {
		t.Fatalf("create workflow run: %v", err)
	}

	startedAt := time.Date(2026, time.April, 30, 3, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(2 * time.Minute)
	job := db.WorkflowRunJob{
		RunID:       run.ID,
		Name:        "build",
		Status:      db.RunCompleted,
		Conclusion:  db.ConclusionSuccess,
		StartedAt:   startedAt,
		CompletedAt: completedAt,
	}
	if err := h.DB.Create(&job).Error; err != nil {
		t.Fatalf("create workflow run job: %v", err)
	}

	return run, job
}

func assertCheckRunShape(t *testing.T, checkRun map[string]any, repoFullName string, run db.WorkflowRun, job db.WorkflowRunJob, headSHA string) {
	t.Helper()

	assertFieldsPresent(t, checkRun, map[string]string{
		"id":           "number",
		"node_id":      "string",
		"external_id":  "string",
		"head_sha":     "string",
		"name":         "string",
		"status":       "string",
		"conclusion":   "string",
		"started_at":   "string",
		"completed_at": "string",
		"url":          "string",
		"details_url":  "string",
		"html_url":     "string",
		"check_suite":  "object",
		"app":          "object",
		"output":       "object",
	})

	if got, _ := checkRun["head_sha"].(string); got != headSHA {
		t.Fatalf("head_sha: got %q, want %q", got, headSHA)
	}
	if got, _ := checkRun["external_id"].(string); got != fmt.Sprintf("workflow-run/%d/job/%d", run.ID, job.ID) {
		t.Fatalf("external_id: got %q", got)
	}
	if got, _ := checkRun["details_url"].(string); got != fmt.Sprintf("https://localhost:8080/%s/actions/runs/%d/job/%d", repoFullName, run.ID, job.ID) {
		t.Fatalf("details_url: got %q", got)
	}

	checkSuite, _ := checkRun["check_suite"].(map[string]any)
	if got := int(checkSuite["id"].(float64)); got != int(run.ID) {
		t.Fatalf("check_suite.id: got %d, want %d", got, run.ID)
	}

	app, _ := checkRun["app"].(map[string]any)
	if got, _ := app["slug"].(string); got != "gh-server-actions" {
		t.Fatalf("app.slug: got %q", got)
	}
	if got, _ := app["name"].(string); got != "gh-server Actions" {
		t.Fatalf("app.name: got %q", got)
	}

	output, _ := checkRun["output"].(map[string]any)
	if got, _ := output["annotations_count"].(float64); got != 0 {
		t.Fatalf("annotations_count: got %v", got)
	}
	if got, _ := output["annotations_url"].(string); got != fmt.Sprintf("http://localhost:8080/api/v3/repos/%s/check-runs/%d/annotations", repoFullName, job.ID) {
		t.Fatalf("annotations_url: got %q", got)
	}
}

func TestListCheckRunsForRef_BranchAndSHAParity(t *testing.T) {
	h := testharness.New(t)
	repo, headSHA := seedCheckRunsRepo(t, h, "check-runs-parity")
	run, job := seedWorkflowCheckRun(t, h, repo, headSHA)

	for _, ref := range []string{repo.DefaultBranch, headSHA} {
		t.Run(ref, func(t *testing.T) {
			path := fmt.Sprintf("/api/v3/repos/%s/commits/%s/check-runs", repo.FullName, ref)
			w := h.DoREST(t, "GET", path, nil)
			assertStatusCode(t, w, 200)

			body := testharness.DecodeJSON(t, w)
			if got := int(body["total_count"].(float64)); got != 1 {
				t.Fatalf("total_count: got %d, want 1", got)
			}

			checkRuns, _ := body["check_runs"].([]any)
			if len(checkRuns) != 1 {
				t.Fatalf("check_runs length: got %d, want 1", len(checkRuns))
			}

			checkRun, _ := checkRuns[0].(map[string]any)
			assertCheckRunShape(t, checkRun, repo.FullName, run, job, headSHA)
		})
	}
}

func TestListCheckRunsForRef_ValidRefWithNoRunsReturnsEmptySuccess(t *testing.T) {
	h := testharness.New(t)
	repo, _ := seedCheckRunsRepo(t, h, "check-runs-empty")

	path := fmt.Sprintf("/api/v3/repos/%s/commits/%s/check-runs", repo.FullName, repo.DefaultBranch)
	w := h.DoREST(t, "GET", path, nil)
	assertStatusCode(t, w, 200)

	body := testharness.DecodeJSON(t, w)
	if got := int(body["total_count"].(float64)); got != 0 {
		t.Fatalf("total_count: got %d, want 0", got)
	}
	checkRuns, _ := body["check_runs"].([]any)
	if len(checkRuns) != 0 {
		t.Fatalf("check_runs length: got %d, want 0", len(checkRuns))
	}
}

func TestListCheckRunsForRef_MissingRepoReturns404(t *testing.T) {
	h := testharness.New(t)

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/does-not-exist/commits/main/check-runs", nil)
	assertStatusCode(t, w, 404)
}

func TestListCheckRunsForRef_MissingBranchReturns404(t *testing.T) {
	h := testharness.New(t)
	repo, _ := seedCheckRunsRepo(t, h, "check-runs-missing-branch")

	path := fmt.Sprintf("/api/v3/repos/%s/commits/%s/check-runs", repo.FullName, "does-not-exist")
	w := h.DoREST(t, "GET", path, nil)
	assertStatusCode(t, w, 404)
}

func TestListCheckRunsForRef_UnresolvedRefWithoutGitBackendReturns501(t *testing.T) {
	h := testharness.New(t)
	repo, _ := seedCheckRunsRepo(t, h, "check-runs-no-git")

	h.Svc.Git = nil

	path := fmt.Sprintf("/api/v3/repos/%s/commits/%s/check-runs", repo.FullName, repo.DefaultBranch)
	w := h.DoREST(t, "GET", path, nil)
	assertStatusCode(t, w, 501)
}

func TestListCheckRunsForRef_SHAResolutionBackendFailureReturns501(t *testing.T) {
	h := testharness.New(t)
	repo, headSHA := seedCheckRunsRepo(t, h, "check-runs-sha-backend-failure")

	repoPath, err := h.Svc.Git.GetRepoPath(context.Background(), repo.FullName)
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	if err := os.RemoveAll(repoPath); err != nil {
		t.Fatalf("RemoveAll(%s): %v", repoPath, err)
	}

	path := fmt.Sprintf("/api/v3/repos/%s/commits/%s/check-runs", repo.FullName, headSHA)
	w := h.DoREST(t, "GET", path, nil)
	assertStatusCode(t, w, 501)
}
