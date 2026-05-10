package rest_test

import (
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/testharness"
)

// ─── Workflow Run GET Response Fields ───────────────────────────────────────

func TestCompat_WorkflowRunGet_ResponseFields(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "compat-wf")

	// Seed a workflow run via DB directly.
	var repo db.Repository
	h.DB.First(&repo, "full_name = ?", "testuser/compat-wf")

	wf := db.Workflow{
		RepositoryID: repo.ID,
		Name:         "CI",
		Path:         ".github/workflows/ci.yml",
		State:        "active",
	}
	h.DB.Create(&wf)

	run := db.WorkflowRun{
		WorkflowID:   wf.ID,
		RepositoryID: repo.ID,
		Name:         "CI",
		HeadBranch:   "main",
		HeadSHA:      "abc123",
		Event:        "push",
		Status:       "completed",
		Conclusion:   "success",
	}
	h.DB.Create(&run)

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-wf/actions/runs", nil)
	assertStatusCode(t, w, 200)
	body := testharness.DecodeJSON(t, w)

	assertFieldPresent(t, body, "total_count", "number")
	assertFieldPresent(t, body, "workflow_runs", "array")

	runs, _ := body["workflow_runs"].([]any)
	if len(runs) == 0 {
		t.Fatal("expected at least 1 workflow run")
	}
	wfRun, _ := runs[0].(map[string]any)
	assertFieldsPresent(t, wfRun, map[string]string{
		"id":          "number",
		"name":        "string",
		"head_branch": "string",
		"head_sha":    "string",
		"status":      "string",
		"conclusion":  "string",
		"event":       "string",
		"url":         "string",
		"html_url":    "string",
		"created_at":  "string",
		"updated_at":  "string",
	})
}
