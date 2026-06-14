package service_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

// setupRepoWithGit creates a user and repo with an initial commit so that
// gitstore ReadFile/ListTreeFiles work correctly.
func setupRepoWithGit(t *testing.T, svc *service.Service, login, repoName string) string {
	t.Helper()
	ctx := context.Background()
	svc.DB.Create(&db.User{Login: login, Name: login, Type: db.TypeUser})
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: login, Name: repoName, AutoInit: true,
	}); err != nil {
		t.Fatalf("setupRepoWithGit: %v", err)
	}
	return login + "/" + repoName
}

// writeNestedFiles commits files with nested paths to a bare git repo.
// A nil content value removes the path from the tree.
// gitstore.WriteFile uses mktree which rejects paths containing slashes,
// so we use update-index + write-tree + commit-tree + update-ref instead.
func writeNestedFiles(t *testing.T, svc *service.Service, repoFullName, branch string, files map[string][]byte) {
	t.Helper()
	dir, err := svc.Git.GetRepoPath(context.Background(), repoFullName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	ref := "refs/heads/" + branch

	// Read existing index from parent tree (if branch exists).
	if out, err := exec.Command("git", "-C", dir, "rev-parse", ref).Output(); err == nil {
		parentTree := strings.TrimSpace(string(out)) + "^{tree}"
		cmd := exec.Command("git", "-C", dir, "read-tree", parentTree)
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+dir+"/test-index")
		if err := cmd.Run(); err != nil {
			t.Fatalf("read-tree: %v", err)
		}
	}

	indexFile := dir + "/test-index"
	envWithIndex := append(os.Environ(), "GIT_INDEX_FILE="+indexFile)

	for path, content := range files {
		if content == nil {
			removeCmd := exec.Command("git", "-C", dir, "update-index", "--index-info")
			removeCmd.Env = envWithIndex
			removeCmd.Stdin = strings.NewReader("0 " + strings.Repeat("0", 40) + "\t" + path + "\n")
			if out, err := removeCmd.CombinedOutput(); err != nil {
				t.Fatalf("update-index --remove %s: %v: %s", path, err, out)
			}
			continue
		}

		// Hash the blob.
		hashCmd := exec.Command("git", "-C", dir, "hash-object", "-w", "--stdin")
		hashCmd.Stdin = strings.NewReader(string(content))
		blobOut, err := hashCmd.Output()
		if err != nil {
			t.Fatalf("hash-object %s: %v", path, err)
		}
		blobSHA := strings.TrimSpace(string(blobOut))

		// Add to index.
		addCmd := exec.Command("git", "-C", dir, "update-index", "--add", "--cacheinfo", fmt.Sprintf("100644,%s,%s", blobSHA, path))
		addCmd.Env = envWithIndex
		if out, err := addCmd.CombinedOutput(); err != nil {
			t.Fatalf("update-index %s: %v: %s", path, err, out)
		}
	}

	// Write tree from index.
	writeTreeCmd := exec.Command("git", "-C", dir, "write-tree")
	writeTreeCmd.Env = envWithIndex
	treeOut, err := writeTreeCmd.Output()
	if err != nil {
		t.Fatalf("write-tree: %v", err)
	}
	treeSHA := strings.TrimSpace(string(treeOut))

	// Create commit.
	commitArgs := []string{"-C", dir, "commit-tree", treeSHA, "-m", "seed files"}
	if out, err := exec.Command("git", "-C", dir, "rev-parse", ref).Output(); err == nil {
		commitArgs = append(commitArgs, "-p", strings.TrimSpace(string(out)))
	}
	commitCmd := exec.Command("git", commitArgs...)
	commitCmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	commitOut, err := commitCmd.Output()
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}
	commitSHA := strings.TrimSpace(string(commitOut))

	// Update ref.
	if err := exec.Command("git", "-C", dir, "update-ref", ref, commitSHA).Run(); err != nil {
		t.Fatalf("update-ref: %v", err)
	}

	// Clean up temp index.
	os.Remove(indexFile)
}

func TestWorkflowService_SyncWorkflowsFromRepo_CreatesAndUpdates(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "wfsync-user", "wfsync-repo")

	// Seed three files: .yml, .yaml, and a non-workflow file.
	writeNestedFiles(t, svc, repoFullName, "main", map[string][]byte{
		".github/workflows/ci.yml":      []byte("name: CI\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - name: echo\n        run: echo ci\n"),
		".github/workflows/deploy.yaml": []byte("name: Deploy\njobs:\n  deploy:\n    runs-on: ubuntu-latest\n    steps:\n      - name: echo\n        run: echo deploy\n"),
		".github/workflows/README.md":   []byte("# Not a workflow\n"),
	})

	// --- Create path ---
	if err := svc.SyncWorkflowsFromRepo(ctx, repoFullName); err != nil {
		t.Fatalf("SyncWorkflowsFromRepo (create): %v", err)
	}

	workflows, err := svc.ListWorkflows(ctx, repoFullName)
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	if len(workflows) != 2 {
		t.Fatalf("expected 2 workflows, got %d", len(workflows))
	}

	wfByPath := map[string]db.Workflow{}
	for _, wf := range workflows {
		wfByPath[wf.Path] = wf
	}

	ciWF, ok := wfByPath[".github/workflows/ci.yml"]
	if !ok {
		t.Fatal("missing workflow for ci.yml")
	}
	if ciWF.Name != "CI" {
		t.Errorf("expected ci.yml name 'CI', got %q", ciWF.Name)
	}
	if ciWF.State != db.WorkflowActive {
		t.Errorf("expected ci.yml state %q, got %q", db.WorkflowActive, ciWF.State)
	}

	deployWF, ok := wfByPath[".github/workflows/deploy.yaml"]
	if !ok {
		t.Fatal("missing workflow for deploy.yaml")
	}
	if deployWF.Name != "Deploy" {
		t.Errorf("expected deploy.yaml name 'Deploy', got %q", deployWF.Name)
	}

	// README.md must not appear.
	if _, exists := wfByPath[".github/workflows/README.md"]; exists {
		t.Error("non-workflow file README.md should not create a workflow row")
	}

	// --- Update path: change ci.yml name ---
	writeNestedFiles(t, svc, repoFullName, "main", map[string][]byte{
		".github/workflows/ci.yml": []byte("name: CI-v2\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - name: echo\n        run: echo ci-v2\n"),
	})

	if err := svc.SyncWorkflowsFromRepo(ctx, repoFullName); err != nil {
		t.Fatalf("SyncWorkflowsFromRepo (update): %v", err)
	}

	workflows2, err := svc.ListWorkflows(ctx, repoFullName)
	if err != nil {
		t.Fatalf("ListWorkflows (update): %v", err)
	}
	if len(workflows2) != 2 {
		t.Fatalf("expected 2 workflows after update, got %d", len(workflows2))
	}

	for _, wf := range workflows2 {
		if wf.Path == ".github/workflows/ci.yml" && wf.Name != "CI-v2" {
			t.Errorf("expected updated name 'CI-v2', got %q", wf.Name)
		}
	}
}

func TestWorkflowService_SyncWorkflowsFromRepo_RemovesStaleRowsAfterRenameAndDelete(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "wfsync-cleanup-user", "wfsync-cleanup-repo")

	writeNestedFiles(t, svc, repoFullName, "main", map[string][]byte{
		".github/workflows/ci.yml": []byte("name: CI\non:\n  workflow_dispatch:\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - name: echo\n        run: echo ci\n"),
	})

	if err := svc.SyncWorkflowsFromRepo(ctx, repoFullName); err != nil {
		t.Fatalf("SyncWorkflowsFromRepo (initial): %v", err)
	}

	workflows, err := svc.ListWorkflows(ctx, repoFullName)
	if err != nil {
		t.Fatalf("ListWorkflows (initial): %v", err)
	}
	if len(workflows) != 1 {
		t.Fatalf("expected 1 workflow initially, got %d", len(workflows))
	}
	initialWF := workflows[0]

	writeNestedFiles(t, svc, repoFullName, "main", map[string][]byte{
		".github/workflows/ci.yml":         nil,
		".github/workflows/ci-renamed.yml": []byte("name: CI Renamed\non:\n  workflow_dispatch:\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - name: echo\n        run: echo renamed\n"),
	})

	if err := svc.SyncWorkflowsFromRepo(ctx, repoFullName); err != nil {
		t.Fatalf("SyncWorkflowsFromRepo (rename): %v", err)
	}

	workflows, err = svc.ListWorkflows(ctx, repoFullName)
	if err != nil {
		t.Fatalf("ListWorkflows (rename): %v", err)
	}
	if len(workflows) != 1 {
		t.Fatalf("expected 1 workflow after rename, got %d", len(workflows))
	}

	renamedWF := workflows[0]
	if renamedWF.Path != ".github/workflows/ci-renamed.yml" {
		t.Fatalf("expected renamed workflow path, got %q", renamedWF.Path)
	}
	if renamedWF.Name != "CI Renamed" {
		t.Fatalf("expected renamed workflow name, got %q", renamedWF.Name)
	}
	if renamedWF.ID == initialWF.ID {
		t.Fatalf("expected renamed workflow to get a new id, still have %d", renamedWF.ID)
	}

	if _, err := svc.GetWorkflow(ctx, repoFullName, int(initialWF.ID)); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected stale workflow row to be removed after rename, got %v", err)
	}
	if _, err := svc.DispatchWorkflow(ctx, repoFullName, int(initialWF.ID), "main", nil); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected dispatch of renamed-away workflow to fail with ErrNotFound, got %v", err)
	}

	writeNestedFiles(t, svc, repoFullName, "main", map[string][]byte{
		".github/workflows/ci-renamed.yml": nil,
	})

	if err := svc.SyncWorkflowsFromRepo(ctx, repoFullName); err != nil {
		t.Fatalf("SyncWorkflowsFromRepo (delete): %v", err)
	}

	workflows, err = svc.ListWorkflows(ctx, repoFullName)
	if err != nil {
		t.Fatalf("ListWorkflows (delete): %v", err)
	}
	if len(workflows) != 0 {
		t.Fatalf("expected 0 workflows after delete, got %d", len(workflows))
	}

	if _, err := svc.GetWorkflow(ctx, repoFullName, int(renamedWF.ID)); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected deleted workflow row to be removed, got %v", err)
	}
	if _, err := svc.DispatchWorkflow(ctx, repoFullName, int(renamedWF.ID), "main", nil); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected dispatch of deleted workflow to fail with ErrNotFound, got %v", err)
	}
}

func TestWorkflowService_DispatchWorkflow_CreatesRunAndRelatedRecords(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	svc.EnableWorkflowExecForTest(0)

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "wfdisp-user", "wfdisp-repo")

	// Seed a valid workflow YAML at the path that will be stored in wf.Path.
	writeNestedFiles(t, svc, repoFullName, "main", map[string][]byte{
		".github/workflows/ci.yml": []byte("name: CI\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - name: echo\n        run: echo hello\n"),
	})

	// Sync to create the Workflow DB row.
	if err := svc.SyncWorkflowsFromRepo(ctx, repoFullName); err != nil {
		t.Fatalf("SyncWorkflowsFromRepo: %v", err)
	}

	workflows, err := svc.ListWorkflows(ctx, repoFullName)
	if err != nil || len(workflows) == 0 {
		t.Fatalf("ListWorkflows: err=%v len=%d", err, len(workflows))
	}
	wf := workflows[0]

	// Dispatch the workflow.
	run, err := svc.DispatchWorkflow(ctx, repoFullName, int(wf.ID), "main", nil)
	if err != nil {
		t.Fatalf("DispatchWorkflow: %v", err)
	}

	// --- Phase 1: immediate return-value assertions (no race) ---
	if run.Event != "workflow_dispatch" {
		t.Errorf("expected event 'workflow_dispatch', got %q", run.Event)
	}
	if run.Status != db.RunQueued {
		t.Errorf("expected status %q, got %q", db.RunQueued, run.Status)
	}
	if run.RunNumber != 1 {
		t.Errorf("expected run_number 1, got %d", run.RunNumber)
	}
	if run.RunAttempt != 1 {
		t.Errorf("expected run_attempt 1, got %d", run.RunAttempt)
	}
	if run.HeadBranch != "main" {
		t.Errorf("expected head_branch 'main', got %q", run.HeadBranch)
	}
	if run.HeadSHA == "" {
		t.Error("expected non-empty head_sha")
	}

	// ActionCache should exist for the repo.
	caches, err := svc.ListActionCaches(ctx, repoFullName)
	if err != nil {
		t.Fatalf("ListActionCaches: %v", err)
	}
	if len(caches) == 0 {
		t.Error("expected at least one ActionCache row after dispatch")
	}

	// --- Phase 2: bounded poll for async completion (max 5s, 50ms tick) ---
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	var completedRun db.WorkflowRun
	for {
		select {
		case <-deadline:
			t.Fatal("async workflow execution did not complete within 5s")
		case <-ticker.C:
			r, err := svc.GetWorkflowRun(ctx, repoFullName, int(run.ID))
			if err != nil {
				continue
			}
			if r.Status == db.RunCompleted {
				completedRun = r
				goto done
			}
		}
	}
done:

	if completedRun.Conclusion != db.ConclusionSuccess {
		t.Errorf("expected conclusion %q, got %q", db.ConclusionSuccess, completedRun.Conclusion)
	}

	jobs, err := svc.ListWorkflowRunJobsByRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListWorkflowRunJobsByRun: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected exactly 1 job, got %d", len(jobs))
	}
	if jobs[0].Name != "build" {
		t.Errorf("expected job name %q, got %q", "build", jobs[0].Name)
	}
	if jobs[0].Conclusion != db.ConclusionSuccess {
		t.Errorf("expected job conclusion %q, got %q", db.ConclusionSuccess, jobs[0].Conclusion)
	}
}

func TestWorkflowService_DispatchWorkflow_DefaultExecDisabledFailsClosed(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "wfdisabled-user", "wfdisabled-repo")

	writeNestedFiles(t, svc, repoFullName, "main", map[string][]byte{
		".github/workflows/ci.yml": []byte("name: CI\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo blocked\n"),
	})

	if err := svc.SyncWorkflowsFromRepo(ctx, repoFullName); err != nil {
		t.Fatalf("SyncWorkflowsFromRepo: %v", err)
	}

	workflows, err := svc.ListWorkflows(ctx, repoFullName)
	if err != nil || len(workflows) == 0 {
		t.Fatalf("ListWorkflows: err=%v len=%d", err, len(workflows))
	}

	run, err := svc.DispatchWorkflow(ctx, repoFullName, int(workflows[0].ID), "main", nil)
	if err != nil {
		t.Fatalf("DispatchWorkflow: %v", err)
	}

	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			t.Fatal("workflow execution did not complete within 5s")
		case <-ticker.C:
			got, err := svc.GetWorkflowRun(ctx, repoFullName, int(run.ID))
			if err != nil {
				continue
			}
			if got.Status != db.RunCompleted {
				continue
			}
			if got.Conclusion != db.ConclusionFailure {
				t.Fatalf("expected conclusion %q, got %q", db.ConclusionFailure, got.Conclusion)
			}

			jobs, err := svc.ListWorkflowRunJobsByRun(ctx, run.ID)
			if err != nil {
				t.Fatalf("ListWorkflowRunJobsByRun: %v", err)
			}
			if len(jobs) != 0 {
				t.Fatalf("expected no jobs when execution is disabled, got %d", len(jobs))
			}
			return
		}
	}
}

func TestWorkflowService_DispatchWorkflow_TimeoutFailsRun(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	svc.SetWorkflowStepRunnerForTest(100*time.Millisecond, func(ctx context.Context, dir, script string, env []string) ([]byte, int, error) {
		<-ctx.Done()
		return []byte("runner blocked"), 1, ctx.Err()
	})

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "wftimeout-user", "wftimeout-repo")

	writeNestedFiles(t, svc, repoFullName, "main", map[string][]byte{
		".github/workflows/ci.yml": []byte("name: CI\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: while true; do :; done\n"),
	})

	if err := svc.SyncWorkflowsFromRepo(ctx, repoFullName); err != nil {
		t.Fatalf("SyncWorkflowsFromRepo: %v", err)
	}

	workflows, err := svc.ListWorkflows(ctx, repoFullName)
	if err != nil || len(workflows) == 0 {
		t.Fatalf("ListWorkflows: err=%v len=%d", err, len(workflows))
	}

	run, err := svc.DispatchWorkflow(ctx, repoFullName, int(workflows[0].ID), "main", nil)
	if err != nil {
		t.Fatalf("DispatchWorkflow: %v", err)
	}

	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			t.Fatal("workflow execution did not complete within 5s")
		case <-ticker.C:
			got, err := svc.GetWorkflowRun(ctx, repoFullName, int(run.ID))
			if err != nil {
				continue
			}
			if got.Status != db.RunCompleted {
				continue
			}
			if got.Conclusion != db.ConclusionFailure {
				t.Fatalf("expected conclusion %q, got %q", db.ConclusionFailure, got.Conclusion)
			}

			jobs, err := svc.ListWorkflowRunJobsByRun(ctx, run.ID)
			if err != nil {
				t.Fatalf("ListWorkflowRunJobsByRun: %v", err)
			}
			if len(jobs) != 1 {
				t.Fatalf("expected 1 job, got %d", len(jobs))
			}
			if jobs[0].Conclusion != db.ConclusionFailure {
				t.Fatalf("expected job conclusion %q, got %q", db.ConclusionFailure, jobs[0].Conclusion)
			}
			if !strings.Contains(string(jobs[0].Logs), "timed out") {
				t.Fatalf("expected timeout log entry, got %q", string(jobs[0].Logs))
			}
			return
		}
	}
}

func TestWorkflowService_DispatchWorkflow_ArtifactPathTraversalFailsRun(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	artifactDirName := fmt.Sprintf("escaped-artifact-%d", time.Now().UnixNano())
	outsideDirCh := make(chan string, 1)
	svc.SetWorkflowStepRunnerForTest(2*time.Second, func(ctx context.Context, dir, script string, env []string) ([]byte, int, error) {
		outsideDir := filepath.Join(filepath.Dir(dir), artifactDirName)
		if err := os.MkdirAll(outsideDir, 0755); err != nil {
			return nil, 1, err
		}
		if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("secret"), 0644); err != nil {
			return nil, 1, err
		}
		select {
		case outsideDirCh <- outsideDir:
		default:
		}
		return []byte("created outside artifact candidate"), 0, nil
	})

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "wfartifact-user", "wfartifact-repo")

	writeNestedFiles(t, svc, repoFullName, "main", map[string][]byte{
		".github/workflows/ci.yml": []byte(fmt.Sprintf(`name: CI
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo hello
      - uses: actions/upload-artifact@v4
        with:
          name: outside
          path: ../%s
`, artifactDirName)),
	})

	if err := svc.SyncWorkflowsFromRepo(ctx, repoFullName); err != nil {
		t.Fatalf("SyncWorkflowsFromRepo: %v", err)
	}

	workflows, err := svc.ListWorkflows(ctx, repoFullName)
	if err != nil || len(workflows) == 0 {
		t.Fatalf("ListWorkflows: err=%v len=%d", err, len(workflows))
	}

	run, err := svc.DispatchWorkflow(ctx, repoFullName, int(workflows[0].ID), "main", nil)
	if err != nil {
		t.Fatalf("DispatchWorkflow: %v", err)
	}

	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			t.Fatal("workflow execution did not complete within 5s")
		case <-ticker.C:
			got, err := svc.GetWorkflowRun(ctx, repoFullName, int(run.ID))
			if err != nil {
				continue
			}
			if got.Status != db.RunCompleted {
				continue
			}
			if got.Conclusion != db.ConclusionFailure {
				t.Fatalf("expected conclusion %q, got %q", db.ConclusionFailure, got.Conclusion)
			}

			jobs, err := svc.ListWorkflowRunJobsByRun(ctx, run.ID)
			if err != nil {
				t.Fatalf("ListWorkflowRunJobsByRun: %v", err)
			}
			if len(jobs) != 1 {
				t.Fatalf("expected 1 job, got %d", len(jobs))
			}
			if jobs[0].Conclusion != db.ConclusionFailure {
				t.Fatalf("expected job conclusion %q, got %q", db.ConclusionFailure, jobs[0].Conclusion)
			}
			if !strings.Contains(string(jobs[0].Logs), "escapes job workspace") {
				t.Fatalf("expected artifact path escape log entry, got %q", string(jobs[0].Logs))
			}

			artifacts, err := svc.ListWorkflowRunArtifacts(ctx, repoFullName, int(run.ID))
			if err != nil {
				t.Fatalf("ListWorkflowRunArtifacts: %v", err)
			}
			if len(artifacts) != 0 {
				t.Fatalf("expected 0 artifacts, got %d", len(artifacts))
			}

			select {
			case outsideDir := <-outsideDirCh:
				_ = os.RemoveAll(outsideDir)
			default:
			}
			return
		}
	}
}

func TestWorkflowService_DispatchWorkflow_InvalidRef(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "wfinv-user", "wfinv-repo")

	// Seed a valid workflow YAML.
	writeNestedFiles(t, svc, repoFullName, "main", map[string][]byte{
		".github/workflows/ci.yml": []byte("name: CI\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - name: echo\n        run: echo hello\n"),
	})

	// Sync to create the Workflow DB row.
	if err := svc.SyncWorkflowsFromRepo(ctx, repoFullName); err != nil {
		t.Fatalf("SyncWorkflowsFromRepo: %v", err)
	}

	workflows, err := svc.ListWorkflows(ctx, repoFullName)
	if err != nil || len(workflows) == 0 {
		t.Fatalf("ListWorkflows: err=%v len=%d", err, len(workflows))
	}
	wf := workflows[0]

	// Dispatch with a non-existent ref should fail with ErrValidation.
	_, err = svc.DispatchWorkflow(ctx, repoFullName, int(wf.ID), "non-existent-branch", nil)
	if !errors.Is(err, service.ErrValidation) {
		t.Errorf("expected ErrValidation for non-existent ref, got %v", err)
	}

	// Verify no run was created.
	var count int64
	svc.DB.Model(&db.WorkflowRun{}).Where("workflow_id = ?", wf.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 runs for invalid ref, got %d", count)
	}
}

func TestWorkflowService_DispatchWorkflow_DisabledReturns422(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wfdis-user", "wfdis-repo")
	repoFullName := "wfdis-user/wfdis-repo"

	repo, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	// Create a disabled workflow directly in the DB.
	wf := db.Workflow{
		RepositoryID: repo.ID,
		Name:         "CI",
		Path:         ".github/workflows/ci.yml",
		State:        db.WorkflowDisabled,
	}
	if err := svc.DB.Create(&wf).Error; err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	// Dispatch should fail with ErrInvalidState.
	_, err = svc.DispatchWorkflow(ctx, repoFullName, int(wf.ID), "main", nil)
	if !errors.Is(err, service.ErrInvalidState) {
		t.Errorf("expected ErrInvalidState for disabled workflow, got %v", err)
	}

	// Verify no run was created.
	var count int64
	svc.DB.Model(&db.WorkflowRun{}).Where("workflow_id = ?", wf.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 runs for disabled workflow, got %d", count)
	}
}

func TestWorkflowService_RerunWorkflowRun_IncrementsAttempt(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wfrerun-user", "wfrerun-repo")
	repoFullName := "wfrerun-user/wfrerun-repo"

	repo, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	// Insert a workflow and a completed run directly via DB.
	wf := db.Workflow{RepositoryID: repo.ID, Name: "CI", Path: ".github/workflows/ci.yml", State: db.WorkflowActive}
	if err := svc.DB.Create(&wf).Error; err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	run := db.WorkflowRun{
		RepositoryID: repo.ID,
		WorkflowID:   wf.ID,
		Name:         "Run 1",
		HeadBranch:   "main",
		HeadSHA:      "abc123",
		RunNumber:    1,
		RunAttempt:   1,
		Event:        "workflow_dispatch",
		Status:       db.RunCompleted,
		Conclusion:   db.ConclusionFailure,
	}
	if err := svc.DB.Create(&run).Error; err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Rerun should succeed and increment attempt.
	if err := svc.RerunWorkflowRun(ctx, repoFullName, int(run.ID)); err != nil {
		t.Fatalf("RerunWorkflowRun: %v", err)
	}

	rerun, err := svc.GetWorkflowRun(ctx, repoFullName, int(run.ID))
	if err != nil {
		t.Fatalf("GetWorkflowRun after rerun: %v", err)
	}
	if rerun.RunAttempt != 2 {
		t.Errorf("expected run_attempt 2, got %d", rerun.RunAttempt)
	}
	if rerun.Status != db.RunCompleted {
		t.Errorf("expected status %q, got %q", db.RunCompleted, rerun.Status)
	}
	if rerun.Conclusion != db.ConclusionSuccess {
		t.Errorf("expected conclusion %q, got %q", db.ConclusionSuccess, rerun.Conclusion)
	}

	// Not-found case.
	err = svc.RerunWorkflowRun(ctx, repoFullName, 999999)
	if !errors.Is(err, service.ErrNotFound) {
		t.Errorf("expected ErrNotFound for non-existent run, got %v", err)
	}
}

func TestWorkflowService_CancelWorkflowRun_StateTransitions(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wfcancel-user", "wfcancel-repo")
	repoFullName := "wfcancel-user/wfcancel-repo"

	repo, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	wf := db.Workflow{RepositoryID: repo.ID, Name: "CI", Path: ".github/workflows/ci.yml", State: db.WorkflowActive}
	if err := svc.DB.Create(&wf).Error; err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	// Cancel a queued run.
	t.Run("cancel_queued", func(t *testing.T) {
		run := db.WorkflowRun{
			RepositoryID: repo.ID, WorkflowID: wf.ID, Name: "Run Q",
			HeadBranch: "main", HeadSHA: "aaa", RunNumber: 1, RunAttempt: 1,
			Event: "workflow_dispatch", Status: db.RunQueued,
		}
		if err := svc.DB.Create(&run).Error; err != nil {
			t.Fatalf("create queued run: %v", err)
		}

		if err := svc.CancelWorkflowRun(ctx, repoFullName, int(run.ID)); err != nil {
			t.Fatalf("CancelWorkflowRun (queued): %v", err)
		}

		got, err := svc.GetWorkflowRun(ctx, repoFullName, int(run.ID))
		if err != nil {
			t.Fatalf("GetWorkflowRun: %v", err)
		}
		if got.Status != db.RunCompleted {
			t.Errorf("expected status %q, got %q", db.RunCompleted, got.Status)
		}
		if got.Conclusion != db.ConclusionCancelled {
			t.Errorf("expected conclusion %q, got %q", db.ConclusionCancelled, got.Conclusion)
		}
	})

	// Cancel an in_progress run.
	t.Run("cancel_in_progress", func(t *testing.T) {
		run := db.WorkflowRun{
			RepositoryID: repo.ID, WorkflowID: wf.ID, Name: "Run IP",
			HeadBranch: "main", HeadSHA: "bbb", RunNumber: 2, RunAttempt: 1,
			Event: "workflow_dispatch", Status: db.RunInProgress,
		}
		if err := svc.DB.Create(&run).Error; err != nil {
			t.Fatalf("create in_progress run: %v", err)
		}

		if err := svc.CancelWorkflowRun(ctx, repoFullName, int(run.ID)); err != nil {
			t.Fatalf("CancelWorkflowRun (in_progress): %v", err)
		}

		got, err := svc.GetWorkflowRun(ctx, repoFullName, int(run.ID))
		if err != nil {
			t.Fatalf("GetWorkflowRun: %v", err)
		}
		if got.Status != db.RunCompleted {
			t.Errorf("expected status %q, got %q", db.RunCompleted, got.Status)
		}
		if got.Conclusion != db.ConclusionCancelled {
			t.Errorf("expected conclusion %q, got %q", db.ConclusionCancelled, got.Conclusion)
		}
	})

	// Cancel a completed run should fail with ErrInvalidState.
	t.Run("cancel_completed_fails", func(t *testing.T) {
		run := db.WorkflowRun{
			RepositoryID: repo.ID, WorkflowID: wf.ID, Name: "Run C",
			HeadBranch: "main", HeadSHA: "ccc", RunNumber: 3, RunAttempt: 1,
			Event: "workflow_dispatch", Status: db.RunCompleted, Conclusion: db.ConclusionSuccess,
		}
		if err := svc.DB.Create(&run).Error; err != nil {
			t.Fatalf("create completed run: %v", err)
		}

		err := svc.CancelWorkflowRun(ctx, repoFullName, int(run.ID))
		if !errors.Is(err, service.ErrInvalidState) {
			t.Errorf("expected ErrInvalidState for completed run, got %v", err)
		}
	})
}

// TestWorkflowService_CrossRepo_AuthBoundary verifies that GetWorkflowRun and
// CancelWorkflowRun reject access to runs belonging to a different repository.
func TestWorkflowService_CrossRepo_AuthBoundary(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create two separate repos.
	setupRepoForTest(t, svc, "owner-a", "repo-a")
	setupRepoForTest(t, svc, "owner-b", "repo-b")

	repoA, err := svc.GetRepo(ctx, "owner-a/repo-a")
	if err != nil {
		t.Fatalf("GetRepo A: %v", err)
	}
	repoB, err := svc.GetRepo(ctx, "owner-b/repo-b")
	if err != nil {
		t.Fatalf("GetRepo B: %v", err)
	}

	// Create a workflow and run in repo A.
	wfA := db.Workflow{RepositoryID: repoA.ID, Name: "CI", Path: ".github/workflows/ci.yml", State: db.WorkflowActive}
	if err := svc.DB.Create(&wfA).Error; err != nil {
		t.Fatalf("create workflow A: %v", err)
	}
	runA := db.WorkflowRun{
		RepositoryID: repoA.ID, WorkflowID: wfA.ID, Name: "Run A",
		HeadBranch: "main", HeadSHA: "aaa111", RunNumber: 1, RunAttempt: 1,
		Event: "workflow_dispatch", Status: db.RunQueued,
	}
	if err := svc.DB.Create(&runA).Error; err != nil {
		t.Fatalf("create run A: %v", err)
	}

	// Create a workflow and run in repo B.
	wfB := db.Workflow{RepositoryID: repoB.ID, Name: "CI", Path: ".github/workflows/ci.yml", State: db.WorkflowActive}
	if err := svc.DB.Create(&wfB).Error; err != nil {
		t.Fatalf("create workflow B: %v", err)
	}
	runB := db.WorkflowRun{
		RepositoryID: repoB.ID, WorkflowID: wfB.ID, Name: "Run B",
		HeadBranch: "main", HeadSHA: "bbb222", RunNumber: 1, RunAttempt: 1,
		Event: "workflow_dispatch", Status: db.RunQueued,
	}
	if err := svc.DB.Create(&runB).Error; err != nil {
		t.Fatalf("create run B: %v", err)
	}

	t.Run("GetWorkflowRun_rejects_cross_repo_read", func(t *testing.T) {
		// Try to read repo A's run using repo B's name — must fail.
		_, err := svc.GetWorkflowRun(ctx, "owner-b/repo-b", int(runA.ID))
		if !errors.Is(err, service.ErrNotFound) {
			t.Errorf("expected ErrNotFound reading repo-A run via repo-B, got %v", err)
		}

		// Confirm the run is still accessible from its own repo.
		got, err := svc.GetWorkflowRun(ctx, "owner-a/repo-a", int(runA.ID))
		if err != nil {
			t.Fatalf("GetWorkflowRun same-repo should succeed: %v", err)
		}
		if got.ID != runA.ID {
			t.Errorf("expected run ID %d, got %d", runA.ID, got.ID)
		}
	})

	t.Run("CancelWorkflowRun_rejects_cross_repo_cancel", func(t *testing.T) {
		// Try to cancel repo B's run using repo A's name — must fail.
		err := svc.CancelWorkflowRun(ctx, "owner-a/repo-a", int(runB.ID))
		if !errors.Is(err, service.ErrNotFound) {
			t.Errorf("expected ErrNotFound cancelling repo-B run via repo-A, got %v", err)
		}

		// Confirm the run was NOT cancelled (still queued).
		var check db.WorkflowRun
		if err := svc.DB.First(&check, runB.ID).Error; err != nil {
			t.Fatalf("direct DB lookup: %v", err)
		}
		if check.Status != db.RunQueued {
			t.Errorf("run should still be queued, got status %q", check.Status)
		}

		// Confirm cancel works from the correct repo.
		if err := svc.CancelWorkflowRun(ctx, "owner-b/repo-b", int(runB.ID)); err != nil {
			t.Fatalf("CancelWorkflowRun same-repo should succeed: %v", err)
		}
	})

	t.Run("DeleteWorkflowRun_rejects_cross_repo_delete", func(t *testing.T) {
		// Create a fresh run in repo A for deletion testing.
		runDel := db.WorkflowRun{
			RepositoryID: repoA.ID, WorkflowID: wfA.ID, Name: "Run Del",
			HeadBranch: "main", HeadSHA: "del111", RunNumber: 2, RunAttempt: 1,
			Event: "workflow_dispatch", Status: db.RunCompleted, Conclusion: db.ConclusionSuccess,
		}
		if err := svc.DB.Create(&runDel).Error; err != nil {
			t.Fatalf("create run for delete: %v", err)
		}

		// Try to delete repo A's run via repo B — must fail.
		err := svc.DeleteWorkflowRun(ctx, "owner-b/repo-b", int(runDel.ID))
		if !errors.Is(err, service.ErrNotFound) {
			t.Errorf("expected ErrNotFound deleting repo-A run via repo-B, got %v", err)
		}

		// Confirm the run still exists.
		var check db.WorkflowRun
		if err := svc.DB.First(&check, runDel.ID).Error; err != nil {
			t.Fatalf("run should still exist after cross-repo delete attempt: %v", err)
		}

		// Confirm delete works from the correct repo.
		if err := svc.DeleteWorkflowRun(ctx, "owner-a/repo-a", int(runDel.ID)); err != nil {
			t.Fatalf("DeleteWorkflowRun same-repo should succeed: %v", err)
		}
	})

	t.Run("ListWorkflowRunJobs_rejects_cross_repo_read", func(t *testing.T) {
		// Create a run with a job in repo A.
		runJobs := db.WorkflowRun{
			RepositoryID: repoA.ID, WorkflowID: wfA.ID, Name: "Run Jobs",
			HeadBranch: "main", HeadSHA: "job111", RunNumber: 3, RunAttempt: 1,
			Event: "workflow_dispatch", Status: db.RunCompleted, Conclusion: db.ConclusionSuccess,
		}
		if err := svc.DB.Create(&runJobs).Error; err != nil {
			t.Fatalf("create run for jobs: %v", err)
		}
		now := time.Now()
		job := db.WorkflowRunJob{
			RunID: runJobs.ID, Name: "build", Status: db.RunCompleted, Conclusion: db.ConclusionSuccess,
			StartedAt: now, CompletedAt: now,
		}
		if err := svc.DB.Create(&job).Error; err != nil {
			t.Fatalf("create job: %v", err)
		}

		// Try to list jobs via repo B — must fail.
		_, err := svc.ListWorkflowRunJobs(ctx, "owner-b/repo-b", int(runJobs.ID))
		if !errors.Is(err, service.ErrNotFound) {
			t.Errorf("expected ErrNotFound listing jobs for repo-A run via repo-B, got %v", err)
		}

		// Confirm listing works from the correct repo.
		jobs, err := svc.ListWorkflowRunJobs(ctx, "owner-a/repo-a", int(runJobs.ID))
		if err != nil {
			t.Fatalf("ListWorkflowRunJobs same-repo should succeed: %v", err)
		}
		if len(jobs) != 1 {
			t.Errorf("expected 1 job, got %d", len(jobs))
		}
	})

	t.Run("ListWorkflowRuns_SameRepo_FilterByWorkflowID_OK", func(t *testing.T) {
		// Create a second workflow in repo A.
		wfA2 := db.Workflow{RepositoryID: repoA.ID, Name: "Deploy", Path: ".github/workflows/deploy.yml", State: db.WorkflowActive}
		if err := svc.DB.Create(&wfA2).Error; err != nil {
			t.Fatalf("create workflow A2: %v", err)
		}
		// Create runs for each workflow in repo A.
		runWfA1 := db.WorkflowRun{
			RepositoryID: repoA.ID, WorkflowID: wfA.ID, Name: "CI Run",
			HeadBranch: "main", HeadSHA: "f1a1a1", RunNumber: 10, RunAttempt: 1,
			Event: "push", Status: db.RunCompleted, Conclusion: db.ConclusionSuccess,
		}
		runWfA2 := db.WorkflowRun{
			RepositoryID: repoA.ID, WorkflowID: wfA2.ID, Name: "Deploy Run",
			HeadBranch: "main", HeadSHA: "f1a2a2", RunNumber: 11, RunAttempt: 1,
			Event: "push", Status: db.RunCompleted, Conclusion: db.ConclusionSuccess,
		}
		if err := svc.DB.Create(&runWfA1).Error; err != nil {
			t.Fatalf("create runWfA1: %v", err)
		}
		if err := svc.DB.Create(&runWfA2).Error; err != nil {
			t.Fatalf("create runWfA2: %v", err)
		}

		// Filter by wfA.ID — should only return runs for that workflow.
		runs, err := svc.ListWorkflowRuns(ctx, "owner-a/repo-a", int(wfA.ID))
		if err != nil {
			t.Fatalf("ListWorkflowRuns filtered: %v", err)
		}
		for _, r := range runs {
			if r.WorkflowID != wfA.ID {
				t.Errorf("expected all runs to have workflow_id %d, got %d (run %d)", wfA.ID, r.WorkflowID, r.ID)
			}
			if r.RepositoryID != repoA.ID {
				t.Errorf("expected all runs to have repository_id %d, got %d (run %d)", repoA.ID, r.RepositoryID, r.ID)
			}
		}
	})

	t.Run("ListWorkflowRuns_CrossRepo_WorkflowID_ReturnsEmpty", func(t *testing.T) {
		// Query repo B with wfA's ID — repo B has no workflow with that ID,
		// so results must be empty (the bug would leak repo A's runs).
		runs, err := svc.ListWorkflowRuns(ctx, "owner-b/repo-b", int(wfA.ID))
		if err != nil {
			t.Fatalf("ListWorkflowRuns cross-repo: %v", err)
		}
		if len(runs) != 0 {
			t.Errorf("expected 0 runs for cross-repo workflow ID, got %d", len(runs))
		}
	})

	t.Run("ListWorkflowRuns_NoWorkflowFilter_OnlyCurrentRepo", func(t *testing.T) {
		// List all runs for repo A (workflowID=0) — must not include repo B's runs.
		runs, err := svc.ListWorkflowRuns(ctx, "owner-a/repo-a", 0)
		if err != nil {
			t.Fatalf("ListWorkflowRuns no filter: %v", err)
		}
		for _, r := range runs {
			if r.RepositoryID != repoA.ID {
				t.Errorf("expected all runs to belong to repo A (id=%d), got repository_id=%d (run %d)", repoA.ID, r.RepositoryID, r.ID)
			}
		}
		if len(runs) == 0 {
			t.Error("expected at least one run for repo A")
		}
	})

	t.Run("GetArtifact_rejects_cross_repo_read", func(t *testing.T) {
		// Create a run with an artifact in repo A.
		runGA := db.WorkflowRun{
			RepositoryID: repoA.ID, WorkflowID: wfA.ID, Name: "Run GA",
			HeadBranch: "main", HeadSHA: "ga111", RunNumber: 5, RunAttempt: 1,
			Event: "workflow_dispatch", Status: db.RunCompleted, Conclusion: db.ConclusionSuccess,
		}
		if err := svc.DB.Create(&runGA).Error; err != nil {
			t.Fatalf("create run for GetArtifact: %v", err)
		}
		artGA := db.Artifact{
			RunID: runGA.ID, Name: "ga-artifact", SizeInBytes: 42,
		}
		if err := svc.DB.Create(&artGA).Error; err != nil {
			t.Fatalf("create artifact: %v", err)
		}

		// Try to get the artifact via repo B — must fail.
		_, err := svc.GetArtifact(ctx, "owner-b/repo-b", int(artGA.ID))
		if !errors.Is(err, service.ErrNotFound) {
			t.Errorf("expected ErrNotFound getting repo-A artifact via repo-B, got %v", err)
		}

		// Confirm the artifact is accessible from its own repo.
		got, err := svc.GetArtifact(ctx, "owner-a/repo-a", int(artGA.ID))
		if err != nil {
			t.Fatalf("GetArtifact same-repo should succeed: %v", err)
		}
		if got.ID != artGA.ID {
			t.Errorf("expected artifact ID %d, got %d", artGA.ID, got.ID)
		}
	})

	t.Run("ListWorkflowRunArtifacts_rejects_cross_repo_read", func(t *testing.T) {
		// Create a run with an artifact in repo A.
		runArt := db.WorkflowRun{
			RepositoryID: repoA.ID, WorkflowID: wfA.ID, Name: "Run Art",
			HeadBranch: "main", HeadSHA: "art111", RunNumber: 4, RunAttempt: 1,
			Event: "workflow_dispatch", Status: db.RunCompleted, Conclusion: db.ConclusionSuccess,
		}
		if err := svc.DB.Create(&runArt).Error; err != nil {
			t.Fatalf("create run for artifacts: %v", err)
		}
		art := db.Artifact{
			RunID: runArt.ID, Name: "test-artifact", SizeInBytes: 100,
		}
		if err := svc.DB.Create(&art).Error; err != nil {
			t.Fatalf("create artifact: %v", err)
		}

		// Try to list artifacts via repo B — must fail.
		_, err := svc.ListWorkflowRunArtifacts(ctx, "owner-b/repo-b", int(runArt.ID))
		if !errors.Is(err, service.ErrNotFound) {
			t.Errorf("expected ErrNotFound listing artifacts for repo-A run via repo-B, got %v", err)
		}

		// Confirm listing works from the correct repo.
		artifacts, err := svc.ListWorkflowRunArtifacts(ctx, "owner-a/repo-a", int(runArt.ID))
		if err != nil {
			t.Fatalf("ListWorkflowRunArtifacts same-repo should succeed: %v", err)
		}
		if len(artifacts) != 1 {
			t.Errorf("expected 1 artifact, got %d", len(artifacts))
		}
	})
}

func TestGetArtifact_SameRepo_OK(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "art-user", "art-repo")
	repo, err := svc.GetRepo(ctx, "art-user/art-repo")
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	wf := db.Workflow{RepositoryID: repo.ID, Name: "CI", Path: ".github/workflows/ci.yml", State: db.WorkflowActive}
	svc.DB.Create(&wf)

	run := db.WorkflowRun{
		RepositoryID: repo.ID, WorkflowID: wf.ID, Name: "Run 1",
		HeadBranch: "main", HeadSHA: "aaa", RunNumber: 1, RunAttempt: 1,
		Event: "workflow_dispatch", Status: db.RunCompleted, Conclusion: db.ConclusionSuccess,
	}
	svc.DB.Create(&run)

	art := db.Artifact{RunID: run.ID, Name: "build-output", SizeInBytes: 256}
	svc.DB.Create(&art)

	got, err := svc.GetArtifact(ctx, "art-user/art-repo", int(art.ID))
	if err != nil {
		t.Fatalf("GetArtifact same-repo: %v", err)
	}
	if got.ID != art.ID {
		t.Errorf("expected artifact ID %d, got %d", art.ID, got.ID)
	}
	if got.Name != "build-output" {
		t.Errorf("expected artifact name %q, got %q", "build-output", got.Name)
	}
}

func TestGetArtifact_CrossRepo_NotFound(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "cross-a", "repo-a")
	setupRepoForTest(t, svc, "cross-b", "repo-b")

	repoA, _ := svc.GetRepo(ctx, "cross-a/repo-a")

	wf := db.Workflow{RepositoryID: repoA.ID, Name: "CI", Path: ".github/workflows/ci.yml", State: db.WorkflowActive}
	svc.DB.Create(&wf)

	run := db.WorkflowRun{
		RepositoryID: repoA.ID, WorkflowID: wf.ID, Name: "Run 1",
		HeadBranch: "main", HeadSHA: "aaa", RunNumber: 1, RunAttempt: 1,
		Event: "workflow_dispatch", Status: db.RunCompleted, Conclusion: db.ConclusionSuccess,
	}
	svc.DB.Create(&run)

	art := db.Artifact{RunID: run.ID, Name: "secret-artifact", SizeInBytes: 100}
	svc.DB.Create(&art)

	// Accessing repo-A's artifact via repo-B must fail.
	_, err := svc.GetArtifact(ctx, "cross-b/repo-b", int(art.ID))
	if !errors.Is(err, service.ErrNotFound) {
		t.Errorf("expected ErrNotFound for cross-repo artifact access, got %v", err)
	}
}

func TestGetArtifact_NotExist_NotFound(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "noart-user", "noart-repo")

	_, err := svc.GetArtifact(ctx, "noart-user/noart-repo", 999999)
	if !errors.Is(err, service.ErrNotFound) {
		t.Errorf("expected ErrNotFound for non-existent artifact, got %v", err)
	}
}

func TestFindWorkflow_ByID(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "findwf-user", "findwf-repo")
	repo, _ := svc.GetRepo(ctx, repoFullName)

	wf := db.Workflow{RepositoryID: repo.ID, Name: "Test Workflow", Path: ".github/workflows/test.yml", State: db.WorkflowActive}
	svc.DB.Create(&wf)

	found, err := svc.FindWorkflow(ctx, repoFullName, fmt.Sprintf("%d", wf.ID))
	if err != nil {
		t.Fatalf("FindWorkflow by ID failed: %v", err)
	}
	if found.ID != wf.ID {
		t.Errorf("expected workflow ID %d, got %d", wf.ID, found.ID)
	}
	if found.Name != "Test Workflow" {
		t.Errorf("expected workflow name %q, got %q", "Test Workflow", found.Name)
	}
}

func TestFindWorkflow_ByName(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "findwf2-user", "findwf2-repo")
	repo, _ := svc.GetRepo(ctx, repoFullName)

	wf := db.Workflow{RepositoryID: repo.ID, Name: "UniqueWorkflowName", Path: ".github/workflows/unique.yml", State: db.WorkflowActive}
	svc.DB.Create(&wf)

	found, err := svc.FindWorkflow(ctx, repoFullName, "UniqueWorkflowName")
	if err != nil {
		t.Fatalf("FindWorkflow by name failed: %v", err)
	}
	if found.ID != wf.ID {
		t.Errorf("expected workflow ID %d, got %d", wf.ID, found.ID)
	}
}

func TestFindWorkflow_ByFilename(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "findwf3-user", "findwf3-repo")
	repo, _ := svc.GetRepo(ctx, repoFullName)

	wf := db.Workflow{RepositoryID: repo.ID, Name: "CI Pipeline", Path: ".github/workflows/ci.yml", State: db.WorkflowActive}
	svc.DB.Create(&wf)

	found, err := svc.FindWorkflow(ctx, repoFullName, "ci.yml")
	if err != nil {
		t.Fatalf("FindWorkflow by filename failed: %v", err)
	}
	if found.ID != wf.ID {
		t.Errorf("expected workflow ID %d, got %d", wf.ID, found.ID)
	}
	if found.Path != ".github/workflows/ci.yml" {
		t.Errorf("expected workflow path %q, got %q", ".github/workflows/ci.yml", found.Path)
	}
}

func TestFindWorkflow_NotFound(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoFullName := setupRepoWithGit(t, svc, "findwf4-user", "findwf4-repo")

	_, err := svc.FindWorkflow(ctx, repoFullName, "nonexistent")
	if !errors.Is(err, service.ErrNotFound) {
		t.Errorf("expected ErrNotFound for non-existent workflow, got %v", err)
	}
}

func TestFindWorkflow_CrossRepo_NotFound(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	repoA := setupRepoWithGit(t, svc, "findwf-a", "repo-a")
	setupRepoWithGit(t, svc, "findwf-b", "repo-b")

	repoAObj, _ := svc.GetRepo(ctx, repoA)
	wf := db.Workflow{RepositoryID: repoAObj.ID, Name: "RepoA Workflow", Path: ".github/workflows/a.yml", State: db.WorkflowActive}
	svc.DB.Create(&wf)

	// Trying to find repo-A's workflow via repo-B should fail
	_, err := svc.FindWorkflow(ctx, "findwf-b/repo-b", "RepoA Workflow")
	if !errors.Is(err, service.ErrNotFound) {
		t.Errorf("expected ErrNotFound for cross-repo workflow lookup, got %v", err)
	}
}
