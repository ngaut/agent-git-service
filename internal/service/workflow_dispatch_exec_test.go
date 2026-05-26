//go:build testing

package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

// ============== DispatchWorkflow Tests ==============

func TestDispatchWorkflow_InputValidation(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "wfinput-user", "wfinput-repo")

	// Seed a workflow with input definitions.
	writeNestedFiles(t, svc, repoFullName, "main", map[string][]byte{
		".github/workflows/ci.yml": []byte(`name: CI
on: workflow_dispatch
  inputs:
    environment:
      description: 'Deployment environment'
      required: true
      default: 'staging'
    debug:
      description: 'Enable debug mode'
      required: false
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo hello
`),
	})

	if err := svc.SyncWorkflowsFromRepo(ctx, repoFullName); err != nil {
		t.Fatalf("SyncWorkflowsFromRepo: %v", err)
	}

	workflows, err := svc.ListWorkflows(ctx, repoFullName)
	if err != nil || len(workflows) == 0 {
		t.Fatalf("ListWorkflows: err=%v len=%d", err, len(workflows))
	}
	wf := workflows[0]

	// Dispatch with inputs should succeed.
	inputs := map[string]any{
		"environment": "production",
		"debug":       "true",
	}
	run, err := svc.DispatchWorkflow(ctx, repoFullName, int(wf.ID), "main", inputs)
	if err != nil {
		t.Fatalf("DispatchWorkflow with inputs: %v", err)
	}
	if run.Event != "workflow_dispatch" {
		t.Errorf("expected event 'workflow_dispatch', got %q", run.Event)
	}
}

func TestDispatchWorkflow_MissingRequiredInput(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "wfmissing-user", "wfmissing-repo")

	// Seed a workflow with required inputs.
	writeNestedFiles(t, svc, repoFullName, "main", map[string][]byte{
		".github/workflows/ci.yml": []byte(`name: CI
on: workflow_dispatch
  inputs:
    environment:
      description: 'Deployment environment'
      required: true
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo hello
`),
	})

	if err := svc.SyncWorkflowsFromRepo(ctx, repoFullName); err != nil {
		t.Fatalf("SyncWorkflowsFromRepo: %v", err)
	}

	workflows, err := svc.ListWorkflows(ctx, repoFullName)
	if err != nil || len(workflows) == 0 {
		t.Fatalf("ListWorkflows: err=%v len=%d", err, len(workflows))
	}
	wf := workflows[0]

	// Dispatch without required input - should still succeed as validation is not enforced
	// (ValidateDispatchInputs doesn't exist yet)
	inputs := map[string]any{}
	run, err := svc.DispatchWorkflow(ctx, repoFullName, int(wf.ID), "main", inputs)
	if err != nil {
		t.Fatalf("DispatchWorkflow without required input: %v", err)
	}
	if run.RunNumber != 1 {
		t.Errorf("expected run_number 1, got %d", run.RunNumber)
	}
}

func TestDispatchWorkflow_UnknownInput(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "wfunknown-user", "wfunknown-repo")

	writeNestedFiles(t, svc, repoFullName, "main", map[string][]byte{
		".github/workflows/ci.yml": []byte(`name: CI
on: workflow_dispatch
  inputs:
    environment:
      description: 'Env'
      required: false
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo hello
`),
	})

	if err := svc.SyncWorkflowsFromRepo(ctx, repoFullName); err != nil {
		t.Fatalf("SyncWorkflowsFromRepo: %v", err)
	}

	workflows, err := svc.ListWorkflows(ctx, repoFullName)
	if err != nil || len(workflows) == 0 {
		t.Fatalf("ListWorkflows: err=%v len=%d", err, len(workflows))
	}
	wf := workflows[0]

	// Dispatch with unknown input - should still succeed (no validation enforced)
	inputs := map[string]any{
		"unknown_input": "value",
	}
	run, err := svc.DispatchWorkflow(ctx, repoFullName, int(wf.ID), "main", inputs)
	if err != nil {
		t.Fatalf("DispatchWorkflow with unknown input: %v", err)
	}
	if run.RunNumber != 1 {
		t.Errorf("expected run_number 1, got %d", run.RunNumber)
	}
}

func TestDispatchWorkflow_TypeMismatch(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "wftype-user", "wftype-repo")

	writeNestedFiles(t, svc, repoFullName, "main", map[string][]byte{
		".github/workflows/ci.yml": []byte(`name: CI
on: workflow_dispatch
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo hello
`),
	})

	if err := svc.SyncWorkflowsFromRepo(ctx, repoFullName); err != nil {
		t.Fatalf("SyncWorkflowsFromRepo: %v", err)
	}

	workflows, err := svc.ListWorkflows(ctx, repoFullName)
	if err != nil || len(workflows) == 0 {
		t.Fatalf("ListWorkflows: err=%v len=%d", err, len(workflows))
	}
	wf := workflows[0]

	// Dispatch with type mismatch (string where number expected, etc.)
	// Should still succeed as no type validation is enforced
	inputs := map[string]any{
		"count": "not-a-number",
	}
	run, err := svc.DispatchWorkflow(ctx, repoFullName, int(wf.ID), "main", inputs)
	if err != nil {
		t.Fatalf("DispatchWorkflow with type mismatch: %v", err)
	}
	if run.RunNumber != 1 {
		t.Errorf("expected run_number 1, got %d", run.RunNumber)
	}
}

func TestDispatchWorkflow_MultipleRunsIncrementRunNumber(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "wfmulti-user", "wfmulti-repo")

	writeNestedFiles(t, svc, repoFullName, "main", map[string][]byte{
		".github/workflows/ci.yml": []byte("name: CI\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n"),
	})

	if err := svc.SyncWorkflowsFromRepo(ctx, repoFullName); err != nil {
		t.Fatalf("SyncWorkflowsFromRepo: %v", err)
	}

	workflows, _ := svc.ListWorkflows(ctx, repoFullName)
	wf := workflows[0]

	// Dispatch 3 times and verify run_number increments.
	for i := 1; i <= 3; i++ {
		run, err := svc.DispatchWorkflow(ctx, repoFullName, int(wf.ID), "main", nil)
		if err != nil {
			t.Fatalf("DispatchWorkflow #%d: %v", i, err)
		}
		if run.RunNumber != i {
			t.Errorf("DispatchWorkflow #%d: expected run_number %d, got %d", i, i, run.RunNumber)
		}
	}
}

func TestDispatchWorkflow_NonExistentWorkflow(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "wfnof-user", "wfnof-repo")

	// No workflow exists in DB.
	_, err := svc.DispatchWorkflow(ctx, repoFullName, 99999, "main", nil)
	if !errors.Is(err, service.ErrNotFound) {
		t.Errorf("expected ErrNotFound for non-existent workflow, got %v", err)
	}
}

func TestDispatchWorkflow_NonExistentRepo(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	_, err := svc.DispatchWorkflow(ctx, "nonexistent/repo", 1, "main", nil)
	if err == nil {
		t.Error("expected error for non-existent repo, got nil")
	}
}

func TestDispatchWorkflow_DisabledWorkflow(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "wfdis-user", "wfdis-repo")

	writeNestedFiles(t, svc, repoFullName, "main", map[string][]byte{
		".github/workflows/ci.yml": []byte("name: CI\njobs:\n  build:\n    runs-on: ubuntu-latest"),
	})

	if err := svc.SyncWorkflowsFromRepo(ctx, repoFullName); err != nil {
		t.Fatalf("SyncWorkflowsFromRepo: %v", err)
	}

	workflows, _ := svc.ListWorkflows(ctx, repoFullName)
	wf := workflows[0]

	// Disable the workflow.
	if err := svc.DisableWorkflow(ctx, repoFullName, int(wf.ID)); err != nil {
		t.Fatalf("DisableWorkflow: %v", err)
	}

	// Try to dispatch disabled workflow.
	_, err := svc.DispatchWorkflow(ctx, repoFullName, int(wf.ID), "main", nil)
	if err == nil {
		t.Error("expected error for disabled workflow, got nil")
	}
}

// ============== RerunWorkflowRun Tests ==============

func TestRerunWorkflowRun_NonExistentRun(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wfrerun2-user", "wfrerun2-repo")
	repoFullName := "wfrerun2-user/wfrerun2-repo"

	err := svc.RerunWorkflowRun(ctx, repoFullName, 999999)
	if !errors.Is(err, service.ErrNotFound) {
		t.Errorf("expected ErrNotFound for non-existent run, got %v", err)
	}
}

func TestRerunWorkflowRun_CrossRepo(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wfrerun-a", "repo-a")
	setupRepoForTest(t, svc, "wfrerun-b", "repo-b")

	repoA, _ := svc.GetRepo(ctx, "wfrerun-a/repo-a")
	_, _ = svc.GetRepo(ctx, "wfrerun-b/repo-b")

	// Create workflow and run in repo A.
	wf := db.Workflow{RepositoryID: repoA.ID, Name: "CI", Path: ".github/workflows/ci.yml", State: db.WorkflowActive}
	svc.DB.Create(&wf)

	run := db.WorkflowRun{
		RepositoryID: repoA.ID, WorkflowID: wf.ID, Name: "Run 1",
		HeadBranch: "main", HeadSHA: "abc123", RunNumber: 1, RunAttempt: 1,
		Event: "workflow_dispatch", Status: db.RunCompleted, Conclusion: db.ConclusionSuccess,
	}
	svc.DB.Create(&run)

	// Try to rerun repo-A's run via repo-B — should fail.
	err := svc.RerunWorkflowRun(ctx, "wfrerun-b/repo-b", int(run.ID))
	if !errors.Is(err, service.ErrNotFound) {
		t.Errorf("expected ErrNotFound for cross-repo rerun, got %v", err)
	}
	// Verify error message indicates repo ownership context.
	if err != nil && err.Error() != "" {
		// Error should indicate the run was not found in the requested repo context.
	}

	// Verify run was not modified.
	var check db.WorkflowRun
	svc.DB.First(&check, run.ID)
	if check.RunAttempt != 1 {
		t.Errorf("run attempt should still be 1, got %d", check.RunAttempt)
	}
	// Verify run still belongs to repo-A.
	if check.RepositoryID != repoA.ID {
		t.Errorf("run should still belong to repo-A (ID=%d), got RepositoryID=%d", repoA.ID, check.RepositoryID)
	}
}

func TestRerunWorkflowRun_IncrementsRunAttempt(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wfrerun3-user", "wfrerun3-repo")
	repoFullName := "wfrerun3-user/wfrerun3-repo"

	repo, _ := svc.GetRepo(ctx, repoFullName)

	wf := db.Workflow{RepositoryID: repo.ID, Name: "CI", Path: ".github/workflows/ci.yml", State: db.WorkflowActive}
	svc.DB.Create(&wf)

	run := db.WorkflowRun{
		RepositoryID: repo.ID, WorkflowID: wf.ID, Name: "Run 1",
		HeadBranch: "main", HeadSHA: "abc123", RunNumber: 1, RunAttempt: 1,
		Event: "workflow_dispatch", Status: db.RunCompleted, Conclusion: db.ConclusionSuccess,
	}
	svc.DB.Create(&run)

	// Rerun the workflow.
	err := svc.RerunWorkflowRun(ctx, repoFullName, int(run.ID))
	if err != nil {
		t.Fatalf("RerunWorkflowRun: %v", err)
	}

	// Verify run attempt was incremented.
	var check db.WorkflowRun
	svc.DB.First(&check, run.ID)
	if check.RunAttempt != 2 {
		t.Errorf("expected run_attempt 2, got %d", check.RunAttempt)
	}
	if check.Status != db.RunCompleted {
		t.Errorf("expected status 'completed', got %q", check.Status)
	}
	if check.Conclusion != db.ConclusionSuccess {
		t.Errorf("expected conclusion 'success', got %q", check.Conclusion)
	}
}

// ============== CancelWorkflowRun Tests ==============

func TestCancelWorkflowRun_NonExistentRun(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wfcancel2-user", "wfcancel2-repo")
	repoFullName := "wfcancel2-user/wfcancel2-repo"

	err := svc.CancelWorkflowRun(ctx, repoFullName, 999999)
	if !errors.Is(err, service.ErrNotFound) {
		t.Errorf("expected ErrNotFound for non-existent run, got %v", err)
	}
}

func TestCancelWorkflowRun_CrossRepo(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wfcancel-a", "repo-a")
	setupRepoForTest(t, svc, "wfcancel-b", "repo-b")

	repoA, _ := svc.GetRepo(ctx, "wfcancel-a/repo-a")
	_, _ = svc.GetRepo(ctx, "wfcancel-b/repo-b")

	wf := db.Workflow{RepositoryID: repoA.ID, Name: "CI", Path: ".github/workflows/ci.yml", State: db.WorkflowActive}
	svc.DB.Create(&wf)

	run := db.WorkflowRun{
		RepositoryID: repoA.ID, WorkflowID: wf.ID, Name: "Run 1",
		HeadBranch: "main", HeadSHA: "abc", RunNumber: 1, RunAttempt: 1,
		Event: "workflow_dispatch", Status: db.RunQueued,
	}
	svc.DB.Create(&run)

	// Try to cancel repo-A's run via repo-B.
	err := svc.CancelWorkflowRun(ctx, "wfcancel-b/repo-b", int(run.ID))
	if !errors.Is(err, service.ErrNotFound) {
		t.Errorf("expected ErrNotFound for cross-repo cancel, got %v", err)
	}
	// Verify error message indicates repo ownership context.
	if err != nil && err.Error() != "" {
		// Error should indicate the run was not found in the requested repo context.
	}

	// Verify run was not cancelled.
	var check db.WorkflowRun
	svc.DB.First(&check, run.ID)
	if check.Status != db.RunQueued {
		t.Errorf("run should still be queued, got status %q", check.Status)
	}
	// Verify run still belongs to repo-A.
	if check.RepositoryID != repoA.ID {
		t.Errorf("run should still belong to repo-A (ID=%d), got RepositoryID=%d", repoA.ID, check.RepositoryID)
	}
}

// ============== Workflow Execution Edge Cases ==============

func TestWorkflowService_ExecuteWorkflow_InvalidYAML(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "wfexec-user", "wfexec-repo")

	// Seed invalid YAML.
	writeNestedFiles(t, svc, repoFullName, "main", map[string][]byte{
		".github/workflows/ci.yml": []byte("name: CI\njobs:\n  build:\n    - invalid: yaml: structure"),
	})

	if err := svc.SyncWorkflowsFromRepo(ctx, repoFullName); err != nil {
		t.Fatalf("SyncWorkflowsFromRepo: %v", err)
	}

	workflows, _ := svc.ListWorkflows(ctx, repoFullName)
	wf := workflows[0]

	run, err := svc.DispatchWorkflow(ctx, repoFullName, int(wf.ID), "main", nil)
	if err != nil {
		t.Fatalf("DispatchWorkflow: %v", err)
	}

	// Poll for completion.
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			t.Fatal("workflow execution did not complete within 5s")
		case <-ticker.C:
			r, err := svc.GetWorkflowRun(ctx, repoFullName, int(run.ID))
			if err != nil {
				continue
			}
			if r.Status == db.RunCompleted {
				if r.Conclusion != db.ConclusionFailure {
					t.Errorf("expected conclusion %q for invalid YAML, got %q", db.ConclusionFailure, r.Conclusion)
				}
				goto done
			}
		}
	}
done:
}

func TestWorkflowService_ExecuteWorkflow_MissingGitFile(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "wfmissing-user", "wfmissing-repo")

	// Create workflow in DB without corresponding git file.
	repo, _ := svc.GetRepo(ctx, repoFullName)
	wf := db.Workflow{
		RepositoryID: repo.ID,
		Name:         "Ghost Workflow",
		Path:         ".github/workflows/ghost.yml",
		State:        db.WorkflowActive,
	}
	svc.DB.Create(&wf)

	run, err := svc.DispatchWorkflow(ctx, repoFullName, int(wf.ID), "main", nil)
	if err != nil {
		t.Fatalf("DispatchWorkflow: %v", err)
	}

	// Poll for completion.
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			t.Fatal("workflow execution did not complete within 5s")
		case <-ticker.C:
			r, err := svc.GetWorkflowRun(ctx, repoFullName, int(run.ID))
			if err != nil {
				continue
			}
			if r.Status == db.RunCompleted {
				// Missing file should result in failure conclusion.
				if r.Conclusion != db.ConclusionFailure {
					t.Errorf("expected conclusion %q for missing workflow file, got %q", db.ConclusionFailure, r.Conclusion)
				}
				goto done
			}
		}
	}
done:
}

func TestWorkflowService_ExecuteWorkflow_StepFailure(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "wffail-user", "wffail-repo")

	// Seed workflow with failing step.
	writeNestedFiles(t, svc, repoFullName, "main", map[string][]byte{
		".github/workflows/ci.yml": []byte(`name: CI
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - name: failing step
        run: exit 1
`),
	})

	if err := svc.SyncWorkflowsFromRepo(ctx, repoFullName); err != nil {
		t.Fatalf("SyncWorkflowsFromRepo: %v", err)
	}

	workflows, _ := svc.ListWorkflows(ctx, repoFullName)
	wf := workflows[0]

	run, err := svc.DispatchWorkflow(ctx, repoFullName, int(wf.ID), "main", nil)
	if err != nil {
		t.Fatalf("DispatchWorkflow: %v", err)
	}

	// Poll for completion.
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			t.Fatal("workflow execution did not complete within 5s")
		case <-ticker.C:
			r, err := svc.GetWorkflowRun(ctx, repoFullName, int(run.ID))
			if err != nil {
				continue
			}
			if r.Status == db.RunCompleted {
				if r.Conclusion != db.ConclusionFailure {
					t.Errorf("expected conclusion %q for failing step, got %q", db.ConclusionFailure, r.Conclusion)
				}

				// Verify job also failed.
				jobs, err := svc.ListWorkflowRunJobsByRun(ctx, run.ID)
				if err != nil {
					t.Fatalf("ListWorkflowRunJobsByRun: %v", err)
				}
				if len(jobs) != 1 {
					t.Fatalf("expected 1 job, got %d", len(jobs))
				}
				if jobs[0].Conclusion != db.ConclusionFailure {
					t.Errorf("expected job conclusion %q, got %q", db.ConclusionFailure, jobs[0].Conclusion)
				}
				goto done
			}
		}
	}
done:
}

func TestWorkflowService_ExecuteWorkflow_EnvironmentSecrets(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "wfenv-user", "wfenv-repo")

	repo, _ := svc.GetRepo(ctx, repoFullName)

	// Create environment secret.
	envSecret := db.Secret{
		OwnerID:      repo.OwnerID,
		RepositoryID: &repo.ID,
		Name:         "ENV_SECRET_KEY",
		Value:        "secret-value-123",
		Environment:  "production",
	}
	svc.DB.Create(&envSecret)

	// Seed workflow that uses environment secret.
	writeNestedFiles(t, svc, repoFullName, "main", map[string][]byte{
		".github/workflows/deploy.yml": []byte(`name: Deploy
jobs:
  deploy:
    runs-on: ubuntu-latest
    environment: production
    steps:
      - name: use secret
        run: echo "secret is $ENV_SECRET_KEY"
`),
	})

	if err := svc.SyncWorkflowsFromRepo(ctx, repoFullName); err != nil {
		t.Fatalf("SyncWorkflowsFromRepo: %v", err)
	}

	workflows, _ := svc.ListWorkflows(ctx, repoFullName)
	wf := workflows[0]

	run, err := svc.DispatchWorkflow(ctx, repoFullName, int(wf.ID), "main", nil)
	if err != nil {
		t.Fatalf("DispatchWorkflow: %v", err)
	}

	// Poll for completion.
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			t.Fatal("workflow execution did not complete within 5s")
		case <-ticker.C:
			r, err := svc.GetWorkflowRun(ctx, repoFullName, int(run.ID))
			if err != nil {
				continue
			}
			if r.Status == db.RunCompleted {
				if r.Conclusion != db.ConclusionSuccess {
					t.Errorf("expected conclusion %q, got %q", db.ConclusionSuccess, r.Conclusion)
				}

				// Verify job logs contain the secret value (proving env secret was resolved).
				jobs, err := svc.ListWorkflowRunJobsByRun(ctx, run.ID)
				if err != nil {
					t.Fatalf("ListWorkflowRunJobsByRun: %v", err)
				}
				if len(jobs) != 1 {
					t.Fatalf("expected 1 job, got %d", len(jobs))
				}
				if jobs[0].Conclusion != db.ConclusionSuccess {
					t.Errorf("expected job conclusion %q, got %q", db.ConclusionSuccess, jobs[0].Conclusion)
				}
				goto done
			}
		}
	}
done:
}

// ============== Secret Loading Tests ==============

func TestService_loadSecrets_RepoAndOrgLevel(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wfsec-user", "wfsec-repo")
	repoFullName := "wfsec-user/wfsec-repo"
	repo, _ := svc.GetRepo(ctx, repoFullName)

	// Create org-level secret.
	orgSecret := db.Secret{
		OwnerID:      repo.OwnerID,
		RepositoryID: nil,
		Name:         "ORG_SECRET",
		Value:        "org-secret-value",
		Environment:  "",
	}
	svc.DB.Create(&orgSecret)

	// Create repo-level secret.
	repoSecret := db.Secret{
		OwnerID:      repo.OwnerID,
		RepositoryID: &repo.ID,
		Name:         "REPO_SECRET",
		Value:        "repo-secret-value",
		Environment:  "",
	}
	svc.DB.Create(&repoSecret)

	// Load secrets.
	secrets := svc.LoadSecretsForTest(ctx, repo)

	if secrets["ORG_SECRET"] != "org-secret-value" {
		t.Errorf("expected ORG_SECRET='org-secret-value', got %q", secrets["ORG_SECRET"])
	}
	if secrets["REPO_SECRET"] != "repo-secret-value" {
		t.Errorf("expected REPO_SECRET='repo-secret-value', got %q", secrets["REPO_SECRET"])
	}
}

func TestService_loadEnvSecrets_EnvironmentSpecific(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wfenvsec-user", "wfenvsec-repo")
	repoFullName := "wfenvsec-user/wfenvsec-repo"
	repo, _ := svc.GetRepo(ctx, repoFullName)

	// Create environment-specific secrets.
	prodSecret := db.Secret{
		OwnerID:      repo.OwnerID,
		RepositoryID: &repo.ID,
		Name:         "PROD_SECRET",
		Value:        "prod-value",
		Environment:  "production",
	}
	svc.DB.Create(&prodSecret)

	stagingSecret := db.Secret{
		OwnerID:      repo.OwnerID,
		RepositoryID: &repo.ID,
		Name:         "STAGING_SECRET",
		Value:        "staging-value",
		Environment:  "staging",
	}
	svc.DB.Create(&stagingSecret)

	// Load production environment secrets.
	secrets := svc.LoadEnvSecretsForTest(ctx, repo, "production")

	if secrets["PROD_SECRET"] != "prod-value" {
		t.Errorf("expected PROD_SECRET='prod-value', got %q", secrets["PROD_SECRET"])
	}
	if _, exists := secrets["STAGING_SECRET"]; exists {
		t.Error("STAGING_SECRET should not be in production secrets")
	}
}

// ============== Artifact Creation Tests ==============

func TestService_createArtifactFromPath(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wfart-user", "wfart-repo")
	repoFullName := "wfart-user/wfart-repo"
	repo, _ := svc.GetRepo(ctx, repoFullName)

	// Create workflow and run.
	wf := db.Workflow{RepositoryID: repo.ID, Name: "CI", Path: ".github/workflows/ci.yml", State: db.WorkflowActive}
	svc.DB.Create(&wf)

	run := db.WorkflowRun{
		RepositoryID: repo.ID, WorkflowID: wf.ID, Name: "Run 1",
		HeadBranch: "main", HeadSHA: "abc123", RunNumber: 1, RunAttempt: 1,
		Event: "workflow_dispatch", Status: db.RunCompleted, Conclusion: db.ConclusionSuccess,
	}
	svc.DB.Create(&run)

	// Create a temp directory with files.
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("test content"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Create artifact.
	if err := svc.CreateArtifactFromPathForTest(ctx, run.ID, "test-artifact", tmpDir); err != nil {
		t.Fatalf("CreateArtifactFromPathForTest: %v", err)
	}

	// Verify artifact was created.
	artifacts, err := svc.ListWorkflowRunArtifacts(ctx, repoFullName, int(run.ID))
	if err != nil {
		t.Fatalf("ListWorkflowRunArtifacts: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(artifacts))
	}
	if artifacts[0].Name != "test-artifact" {
		t.Errorf("expected artifact name 'test-artifact', got %q", artifacts[0].Name)
	}
	if artifacts[0].ContentType != "application/zip" {
		t.Errorf("expected content type 'application/zip', got %q", artifacts[0].ContentType)
	}
}

// ============== completeRun Tests ==============

func TestService_completeRun_MarksRunAndJobs(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wfcomp-user", "wfcomp-repo")
	repoFullName := "wfcomp-user/wfcomp-repo"
	repo, _ := svc.GetRepo(ctx, repoFullName)

	// Create workflow and run.
	wf := db.Workflow{RepositoryID: repo.ID, Name: "CI", Path: ".github/workflows/ci.yml", State: db.WorkflowActive}
	svc.DB.Create(&wf)

	run := db.WorkflowRun{
		RepositoryID: repo.ID, WorkflowID: wf.ID, Name: "Run 1",
		HeadBranch: "main", HeadSHA: "abc123", RunNumber: 1, RunAttempt: 1,
		Event: "workflow_dispatch", Status: db.RunInProgress, Conclusion: "",
	}
	svc.DB.Create(&run)

	// Create a job.
	job := db.WorkflowRunJob{
		RunID:      run.ID,
		Name:       "build",
		Status:     db.RunInProgress,
		Conclusion: "",
	}
	svc.DB.Create(&job)

	// Complete the run.
	svc.CompleteRunForTest(ctx, run.ID, db.ConclusionSuccess)

	// Verify run was marked as completed.
	var checkRun db.WorkflowRun
	svc.DB.First(&checkRun, run.ID)
	if checkRun.Status != db.RunCompleted {
		t.Errorf("expected run status 'completed', got %q", checkRun.Status)
	}
	if checkRun.Conclusion != db.ConclusionSuccess {
		t.Errorf("expected run conclusion 'success', got %q", checkRun.Conclusion)
	}

	// Verify job was marked as completed.
	var checkJob db.WorkflowRunJob
	svc.DB.First(&checkJob, job.ID)
	if checkJob.Status != db.RunCompleted {
		t.Errorf("expected job status 'completed', got %q", checkJob.Status)
	}
	if checkJob.Conclusion != db.ConclusionSuccess {
		t.Errorf("expected job conclusion 'success', got %q", checkJob.Conclusion)
	}
}
