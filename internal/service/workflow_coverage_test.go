package service_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func TestWorkflowService_StateTransitions(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wfstate-user", "wfstate-repo")
	repoFullName := "wfstate-user/wfstate-repo"
	repo, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	wf := createWorkflow(t, svc, repo.ID, "CI", ".github/workflows/ci.yml", db.WorkflowActive)

	if err := svc.SetWorkflowState(ctx, repoFullName, int(wf.ID), "paused"); err != nil {
		t.Fatalf("SetWorkflowState custom: %v", err)
	}
	got, err := svc.GetWorkflow(ctx, repoFullName, int(wf.ID))
	if err != nil {
		t.Fatalf("GetWorkflow after custom: %v", err)
	}
	if got.State != "paused" {
		t.Errorf("expected custom state %q, got %q", "paused", got.State)
	}

	if err := svc.EnableWorkflow(ctx, repoFullName, int(wf.ID)); err != nil {
		t.Fatalf("EnableWorkflow: %v", err)
	}
	got, err = svc.GetWorkflow(ctx, repoFullName, int(wf.ID))
	if err != nil {
		t.Fatalf("GetWorkflow after enable: %v", err)
	}
	if got.State != db.WorkflowActive {
		t.Errorf("expected state %q, got %q", db.WorkflowActive, got.State)
	}

	if err := svc.DisableWorkflow(ctx, repoFullName, int(wf.ID)); err != nil {
		t.Fatalf("DisableWorkflow: %v", err)
	}
	got, err = svc.GetWorkflow(ctx, repoFullName, int(wf.ID))
	if err != nil {
		t.Fatalf("GetWorkflow after disable: %v", err)
	}
	if got.State != db.WorkflowDisabled {
		t.Errorf("expected state %q, got %q", db.WorkflowDisabled, got.State)
	}

	if err := svc.SetWorkflowState(ctx, repoFullName, 9999, db.WorkflowActive); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing workflow, got %v", err)
	}

	setupRepoForTest(t, svc, "wfstate-user2", "wfstate-repo2")
	if err := svc.SetWorkflowState(ctx, "wfstate-user2/wfstate-repo2", int(wf.ID), db.WorkflowActive); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for cross-repo update, got %v", err)
	}
	got, err = svc.GetWorkflow(ctx, repoFullName, int(wf.ID))
	if err != nil {
		t.Fatalf("GetWorkflow after cross-repo attempt: %v", err)
	}
	if got.State != db.WorkflowDisabled {
		t.Errorf("expected state to remain %q, got %q", db.WorkflowDisabled, got.State)
	}
}

func TestWorkflowService_GetWorkflowAndByID(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wfget-user", "wfget-repo")
	setupRepoForTest(t, svc, "wfget-user2", "wfget-repo2")
	repoFullName := "wfget-user/wfget-repo"
	otherRepoFullName := "wfget-user2/wfget-repo2"

	repo, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	wf := createWorkflow(t, svc, repo.ID, "Build", ".github/workflows/build.yml", db.WorkflowActive)

	got, err := svc.GetWorkflow(ctx, repoFullName, int(wf.ID))
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	if got.ID != wf.ID || got.RepositoryID != repo.ID {
		t.Fatalf("GetWorkflow returned wrong workflow: %+v", got)
	}

	byID, err := svc.GetWorkflowByID(ctx, wf.ID)
	if err != nil {
		t.Fatalf("GetWorkflowByID: %v", err)
	}
	if byID.ID != wf.ID {
		t.Fatalf("GetWorkflowByID returned wrong workflow: %+v", byID)
	}

	if _, err := svc.GetWorkflow(ctx, otherRepoFullName, int(wf.ID)); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for cross-repo workflow, got %v", err)
	}

	if _, err := svc.GetWorkflowByID(ctx, wf.ID+9999); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing workflow by ID, got %v", err)
	}
}

func TestWorkflowService_GetWorkflowRunJob(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wfjob-user", "wfjob-repo")
	repoFullName := "wfjob-user/wfjob-repo"
	repo, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	wf := createWorkflow(t, svc, repo.ID, "Build", ".github/workflows/build.yml", db.WorkflowActive)
	run := createWorkflowRun(t, svc, repo.ID, wf.ID, 1)
	job := createWorkflowJob(t, svc, run.ID, "unit-tests")

	got, err := svc.GetWorkflowRunJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRunJob: %v", err)
	}
	if got.ID != job.ID || got.RunID != run.ID {
		t.Fatalf("GetWorkflowRunJob returned wrong job: %+v", got)
	}

	if _, err := svc.GetWorkflowRunJob(ctx, job.ID+9999); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing job, got %v", err)
	}
}

func TestWorkflowService_ListRepoArtifacts(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wfart-user", "wfart-repo")
	setupRepoForTest(t, svc, "wfart-user2", "wfart-repo2")
	setupRepoForTest(t, svc, "wfart-user3", "wfart-empty")

	repoFullName := "wfart-user/wfart-repo"
	otherRepoFullName := "wfart-user2/wfart-repo2"
	emptyRepoFullName := "wfart-user3/wfart-empty"

	repo, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	otherRepo, err := svc.GetRepo(ctx, otherRepoFullName)
	if err != nil {
		t.Fatalf("GetRepo other: %v", err)
	}

	wf := createWorkflow(t, svc, repo.ID, "CI", ".github/workflows/ci.yml", db.WorkflowActive)
	otherWf := createWorkflow(t, svc, otherRepo.ID, "CI", ".github/workflows/ci.yml", db.WorkflowActive)

	run1 := createWorkflowRun(t, svc, repo.ID, wf.ID, 1)
	run2 := createWorkflowRun(t, svc, repo.ID, wf.ID, 2)
	otherRun := createWorkflowRun(t, svc, otherRepo.ID, otherWf.ID, 1)

	createArtifact(t, svc, run1.ID, "run1-art1")
	createArtifact(t, svc, run1.ID, "run1-art2")
	createArtifact(t, svc, run2.ID, "run2-art1")
	createArtifact(t, svc, otherRun.ID, "other-repo-art")

	artifacts, err := svc.ListRepoArtifacts(ctx, repoFullName)
	if err != nil {
		t.Fatalf("ListRepoArtifacts: %v", err)
	}
	if len(artifacts) != 3 {
		t.Fatalf("expected 3 artifacts for repo, got %d", len(artifacts))
	}
	names := map[string]bool{}
	for _, art := range artifacts {
		names[art.Name] = true
		if art.RunID != run1.ID && art.RunID != run2.ID {
			t.Fatalf("artifact %s has unexpected run ID %d", art.Name, art.RunID)
		}
	}
	if names["other-repo-art"] {
		t.Error("ListRepoArtifacts should not include artifacts from other repos")
	}

	otherArtifacts, err := svc.ListRepoArtifacts(ctx, otherRepoFullName)
	if err != nil {
		t.Fatalf("ListRepoArtifacts other repo: %v", err)
	}
	if len(otherArtifacts) != 1 || otherArtifacts[0].Name != "other-repo-art" {
		t.Fatalf("expected 1 artifact for other repo, got %+v", otherArtifacts)
	}

	emptyArtifacts, err := svc.ListRepoArtifacts(ctx, emptyRepoFullName)
	if err != nil {
		t.Fatalf("ListRepoArtifacts empty repo: %v", err)
	}
	if len(emptyArtifacts) != 0 {
		t.Fatalf("expected 0 artifacts for empty repo, got %d", len(emptyArtifacts))
	}
}

func TestWorkflowService_ActionCacheDeletion(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wfcache-user", "wfcache-repo")
	setupRepoForTest(t, svc, "wfcache-user2", "wfcache-repo2")

	repoFullName := "wfcache-user/wfcache-repo"
	otherRepoFullName := "wfcache-user2/wfcache-repo2"

	repo, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	otherRepo, err := svc.GetRepo(ctx, otherRepoFullName)
	if err != nil {
		t.Fatalf("GetRepo other: %v", err)
	}

	cache1 := createActionCache(t, svc, repo.ID, "node-cache", "refs/heads/main", "v1")
	cache2 := createActionCache(t, svc, repo.ID, "node-cache", "refs/heads/dev", "v1")
	cache3 := createActionCache(t, svc, repo.ID, "other-cache", "refs/heads/main", "v2")
	otherCache := createActionCache(t, svc, otherRepo.ID, "node-cache", "refs/heads/main", "v1")

	if err := svc.DeleteActionCacheByID(ctx, cache3.ID); err != nil {
		t.Fatalf("DeleteActionCacheByID: %v", err)
	}
	caches, err := svc.ListActionCaches(ctx, repoFullName)
	if err != nil {
		t.Fatalf("ListActionCaches after delete: %v", err)
	}
	if len(caches) != 2 {
		t.Fatalf("expected 2 caches after delete-by-id, got %d", len(caches))
	}
	remaining := map[uint]bool{}
	for _, cache := range caches {
		remaining[cache.ID] = true
	}
	if !remaining[cache1.ID] || !remaining[cache2.ID] {
		t.Fatalf("expected caches %d and %d to remain, got %+v", cache1.ID, cache2.ID, remaining)
	}

	if err := svc.DeleteActionCacheByID(ctx, cache3.ID); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing cache ID, got %v", err)
	}

	if err := svc.DeleteActionCaches(ctx, repoFullName, "node-cache"); err != nil {
		t.Fatalf("DeleteActionCaches: %v", err)
	}
	caches, err = svc.ListActionCaches(ctx, repoFullName)
	if err != nil {
		t.Fatalf("ListActionCaches after delete-by-key: %v", err)
	}
	if len(caches) != 0 {
		t.Fatalf("expected 0 caches after delete-by-key, got %d", len(caches))
	}

	otherCaches, err := svc.ListActionCaches(ctx, otherRepoFullName)
	if err != nil {
		t.Fatalf("ListActionCaches other repo: %v", err)
	}
	if len(otherCaches) != 1 || otherCaches[0].ID != otherCache.ID {
		t.Fatalf("expected other repo cache to remain, got %+v", otherCaches)
	}

	if err := svc.DeleteActionCaches(ctx, repoFullName, "missing-key"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing cache key, got %v", err)
	}
}

func TestService_LoadEnvSecrets_CrossRepo(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wfenv-user", "wfenv-repo")
	setupRepoForTest(t, svc, "wfenv-user2", "wfenv-repo2")

	repoFullName := "wfenv-user/wfenv-repo"
	otherRepoFullName := "wfenv-user2/wfenv-repo2"

	repo, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	otherRepo, err := svc.GetRepo(ctx, otherRepoFullName)
	if err != nil {
		t.Fatalf("GetRepo other: %v", err)
	}

	secrets := []db.Secret{
		{OwnerID: repo.OwnerID, RepositoryID: &repo.ID, Environment: "production", Name: "PROD_TOKEN", Value: "prod-value"},
		{OwnerID: repo.OwnerID, RepositoryID: &repo.ID, Environment: "staging", Name: "STAGE_TOKEN", Value: "stage-value"},
		{OwnerID: repo.OwnerID, RepositoryID: &repo.ID, Environment: "production", Name: "EMPTY_TOKEN", Value: ""},
		{OwnerID: otherRepo.OwnerID, RepositoryID: &otherRepo.ID, Environment: "production", Name: "OTHER_TOKEN", Value: "other-value"},
	}
	for _, sec := range secrets {
		if err := svc.DB.Create(&sec).Error; err != nil {
			t.Fatalf("Create secret %s: %v", sec.Name, err)
		}
	}

	prodSecrets := svc.LoadEnvSecretsForTest(ctx, repo, "production")
	if prodSecrets["PROD_TOKEN"] != "prod-value" {
		t.Errorf("expected PROD_TOKEN=prod-value, got %q", prodSecrets["PROD_TOKEN"])
	}
	if _, exists := prodSecrets["STAGE_TOKEN"]; exists {
		t.Error("STAGE_TOKEN should not be included in production secrets")
	}
	if _, exists := prodSecrets["OTHER_TOKEN"]; exists {
		t.Error("OTHER_TOKEN should not be included for other repo")
	}
	if _, exists := prodSecrets["EMPTY_TOKEN"]; exists {
		t.Error("EMPTY_TOKEN should not be included when value is empty")
	}

	missingSecrets := svc.LoadEnvSecretsForTest(ctx, repo, "qa")
	if len(missingSecrets) != 0 {
		t.Fatalf("expected no secrets for missing environment, got %v", missingSecrets)
	}
}

func TestService_CreateArtifactFromPath_Branches(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wfartpath-user", "wfartpath-repo")
	repoFullName := "wfartpath-user/wfartpath-repo"
	repo, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	wf := createWorkflow(t, svc, repo.ID, "CI", ".github/workflows/ci.yml", db.WorkflowActive)
	run1 := createWorkflowRun(t, svc, repo.ID, wf.ID, 1)

	t.Run("success", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, "hello.txt"), []byte("hello"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		if err := svc.CreateArtifactFromPathForTest(ctx, run1.ID, "good-artifact", tmpDir); err != nil {
			t.Fatalf("CreateArtifactFromPathForTest success: %v", err)
		}

		arts, err := svc.ListWorkflowRunArtifacts(ctx, repoFullName, int(run1.ID))
		if err != nil {
			t.Fatalf("ListWorkflowRunArtifacts: %v", err)
		}
		if len(arts) != 1 {
			t.Fatalf("expected 1 artifact, got %d", len(arts))
		}

		art := arts[0]
		if art.Name != "good-artifact" {
			t.Errorf("expected artifact name %q, got %q", "good-artifact", art.Name)
		}
		if art.ContentType != "application/zip" {
			t.Errorf("expected content type application/zip, got %q", art.ContentType)
		}

		zr, err := zip.NewReader(bytes.NewReader(art.Content), int64(len(art.Content)))
		if err != nil {
			t.Fatalf("zip.NewReader: %v", err)
		}
		if len(zr.File) != 1 {
			t.Fatalf("expected 1 file in zip, got %d", len(zr.File))
		}
		if zr.File[0].Name != "hello.txt" {
			t.Fatalf("expected zip entry hello.txt, got %q", zr.File[0].Name)
		}
		rc, err := zr.File[0].Open()
		if err != nil {
			t.Fatalf("zip entry open: %v", err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("zip entry read: %v", err)
		}
		if string(data) != "hello" {
			t.Errorf("expected zip content %q, got %q", "hello", string(data))
		}
	})

	t.Run("missing-path", func(t *testing.T) {
		run2 := createWorkflowRun(t, svc, repo.ID, wf.ID, 2)
		missingPath := filepath.Join(t.TempDir(), "nope")
		if err := svc.CreateArtifactFromPathForTest(ctx, run2.ID, "missing-artifact", missingPath); err != nil {
			t.Fatalf("CreateArtifactFromPathForTest missing-path: %v", err)
		}

		arts, err := svc.ListWorkflowRunArtifacts(ctx, repoFullName, int(run2.ID))
		if err != nil {
			t.Fatalf("ListWorkflowRunArtifacts missing-path: %v", err)
		}
		if len(arts) != 1 {
			t.Fatalf("expected 1 artifact for missing path, got %d", len(arts))
		}
		art := arts[0]
		zr, err := zip.NewReader(bytes.NewReader(art.Content), int64(len(art.Content)))
		if err != nil {
			t.Fatalf("zip.NewReader missing-path: %v", err)
		}
		if len(zr.File) != 0 {
			t.Fatalf("expected 0 files in zip for missing path, got %d", len(zr.File))
		}
	})

	t.Run("symlink-escape-skipped", func(t *testing.T) {
		run3 := createWorkflowRun(t, svc, repo.ID, wf.ID, 3)
		tmpDir := t.TempDir()
		outsideDir := t.TempDir()
		outsidePath := filepath.Join(outsideDir, "outside.txt")
		if err := os.WriteFile(outsidePath, []byte("outside"), 0644); err != nil {
			t.Fatalf("WriteFile outside: %v", err)
		}
		if err := os.Symlink(outsidePath, filepath.Join(tmpDir, "outside-link.txt")); err != nil {
			t.Fatalf("Symlink: %v", err)
		}

		if err := svc.CreateArtifactFromPathForTest(ctx, run3.ID, "symlink-artifact", tmpDir); err != nil {
			t.Fatalf("CreateArtifactFromPathForTest symlink: %v", err)
		}

		arts, err := svc.ListWorkflowRunArtifacts(ctx, repoFullName, int(run3.ID))
		if err != nil {
			t.Fatalf("ListWorkflowRunArtifacts symlink: %v", err)
		}
		if len(arts) != 1 {
			t.Fatalf("expected 1 artifact for symlink path, got %d", len(arts))
		}

		zr, err := zip.NewReader(bytes.NewReader(arts[0].Content), int64(len(arts[0].Content)))
		if err != nil {
			t.Fatalf("zip.NewReader symlink: %v", err)
		}
		if len(zr.File) != 0 {
			t.Fatalf("expected symlink escape to be skipped, got %d zip entries", len(zr.File))
		}
	})

	t.Run("deadline-exceeded-no-persist", func(t *testing.T) {
		run4 := createWorkflowRun(t, svc, repo.ID, wf.ID, 4)
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, "hello.txt"), []byte("hello"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		deadlineCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()

		err := svc.CreateArtifactFromPathForTest(deadlineCtx, run4.ID, "timed-out-artifact", tmpDir)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline exceeded, got %v", err)
		}

		arts, err := svc.ListWorkflowRunArtifacts(ctx, repoFullName, int(run4.ID))
		if err != nil {
			t.Fatalf("ListWorkflowRunArtifacts deadline-exceeded: %v", err)
		}
		if len(arts) != 0 {
			t.Fatalf("expected 0 artifacts after deadline exceeded, got %d", len(arts))
		}
	})
}

func createWorkflow(t *testing.T, svc *service.Service, repoID uint, name, path, state string) db.Workflow {
	t.Helper()
	wf := db.Workflow{
		RepositoryID: repoID,
		Name:         name,
		Path:         path,
		State:        state,
	}
	if err := svc.DB.Create(&wf).Error; err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	return wf
}

func createWorkflowRun(t *testing.T, svc *service.Service, repoID, workflowID uint, runNumber int) db.WorkflowRun {
	t.Helper()
	run := db.WorkflowRun{
		RepositoryID: repoID,
		WorkflowID:   workflowID,
		Name:         fmt.Sprintf("Run %d", runNumber),
		HeadBranch:   "main",
		HeadSHA:      fmt.Sprintf("sha-%d", runNumber),
		RunNumber:    runNumber,
		RunAttempt:   1,
		Event:        "workflow_dispatch",
		Status:       db.RunCompleted,
		Conclusion:   db.ConclusionSuccess,
	}
	if err := svc.DB.Create(&run).Error; err != nil {
		t.Fatalf("create workflow run: %v", err)
	}
	return run
}

func createWorkflowJob(t *testing.T, svc *service.Service, runID uint, name string) db.WorkflowRunJob {
	t.Helper()
	job := testWorkflowRunJob(runID, name)
	if err := svc.DB.Create(&job).Error; err != nil {
		t.Fatalf("create workflow job: %v", err)
	}
	return job
}

func createArtifact(t *testing.T, svc *service.Service, runID uint, name string) db.Artifact {
	t.Helper()
	art := db.Artifact{
		RunID:       runID,
		Name:        name,
		SizeInBytes: int64(len(name)),
		Content:     []byte(name),
		ContentType: "application/octet-stream",
	}
	if err := svc.DB.Create(&art).Error; err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	return art
}

func createActionCache(t *testing.T, svc *service.Service, repoID uint, key, ref, version string) db.ActionCache {
	t.Helper()
	cache := testActionCache(repoID, key, ref, version)
	if err := svc.DB.Create(&cache).Error; err != nil {
		t.Fatalf("create action cache: %v", err)
	}
	return cache
}
