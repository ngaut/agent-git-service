package rest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
	"gh-server/internal/testharness"
)

func TestDispatchWorkflow_InvalidRef_Returns422(t *testing.T) {
	h := testharness.New(t)

	ctx := context.Background()
	_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wf-invalid-ref-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	repo, err := h.Svc.GetRepo(ctx, h.User.Login+"/wf-invalid-ref-repo")
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	// Seed a workflow directly in the DB.
	wf := db.Workflow{
		RepositoryID: repo.ID,
		Name:         "CI",
		Path:         ".github/workflows/ci.yml",
		State:        db.WorkflowActive,
	}
	if err := h.DB.Create(&wf).Error; err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	// POST dispatch with a non-existent ref should return 422.
	path := fmt.Sprintf("/api/v3/repos/%s/wf-invalid-ref-repo/actions/workflows/%d/dispatches", h.User.Login, wf.ID)
	w := h.DoRESTJSON(t, "POST", path, map[string]any{"ref": "non-existent-branch"})

	if w.Code != 422 {
		t.Errorf("expected 422, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	message, _ := body["message"].(string)
	if !strings.Contains(message, "ref") || !strings.Contains(message, "not found") {
		t.Fatalf("expected invalid ref message, got %q", message)
	}

	// Verify no run was created.
	var count int64
	h.DB.Model(&db.WorkflowRun{}).Where("workflow_id = ?", wf.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 runs for invalid ref, got %d", count)
	}
}

func TestDispatchWorkflow_DisabledReturns422(t *testing.T) {
	h := testharness.New(t)

	ctx := context.Background()
	_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wf-dispatch-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	repo, err := h.Svc.GetRepo(ctx, h.User.Login+"/wf-dispatch-repo")
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	// Seed a disabled workflow.
	wf := db.Workflow{
		RepositoryID: repo.ID,
		Name:         "CI",
		Path:         ".github/workflows/ci.yml",
		State:        db.WorkflowDisabled,
	}
	if err := h.DB.Create(&wf).Error; err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	// POST dispatch on a disabled workflow should return 422.
	path := fmt.Sprintf("/api/v3/repos/%s/wf-dispatch-repo/actions/workflows/%d/dispatches", h.User.Login, wf.ID)
	w := h.DoRESTJSON(t, "POST", path, map[string]any{"ref": "main"})

	if w.Code != 422 {
		t.Errorf("expected 422, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	message, _ := body["message"].(string)
	if !strings.Contains(message, "disabled") {
		t.Fatalf("expected disabled message, got %q", message)
	}
}

func TestCreateRepositoryDispatch_AcceptsStringClientPayload(t *testing.T) {
	h := testharness.New(t)

	ctx := context.Background()
	_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wf-repo-dispatch-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	path := fmt.Sprintf("/api/v3/repos/%s/wf-repo-dispatch-repo/dispatches", h.User.Login)
	w := h.DoREST(t, "POST", path, strings.NewReader(`{"event_type":"test_event","client_payload":"{\"key\":\"value\"}"}`))

	if w.Code != 204 {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}
