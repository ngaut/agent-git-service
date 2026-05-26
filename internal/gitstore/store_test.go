package gitstore_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/tenant"
)

func TestStore_InitForkDelete(t *testing.T) {
	store, cleanup := gitstore.NewTestStore(t)
	defer cleanup()

	ctx := context.Background()
	repoName := "user/repo1"
	forkName := "user/repo2"

	// Test Exists (should be false)
	if store.Exists(ctx, repoName) {
		t.Error("expected repo not to exist")
	}

	// Test Init
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Test Exists (should be true)
	if !store.Exists(ctx, repoName) {
		t.Error("expected repo to exist after Init")
	}

	// Test SetupConfig
	if err := store.SetupConfig(ctx, repoName, "http://localhost"); err != nil {
		t.Errorf("SetupConfig failed: %v", err)
	}

	// Test Fork
	if err := store.Fork(ctx, repoName, forkName); err != nil {
		t.Fatalf("Fork failed: %v", err)
	}

	// Test Exists on Fork
	if !store.Exists(ctx, forkName) {
		t.Error("expected forked repo to exist")
	}

	// Test Delete
	if err := store.Delete(ctx, repoName); err != nil {
		t.Errorf("Delete failed: %v", err)
	}
	if store.Exists(ctx, repoName) {
		t.Error("expected repo to be deleted")
	}
}

func TestStore_MergeAndCompare(t *testing.T) {
	store, cleanup := gitstore.NewTestStore(t)
	defer cleanup()

	ctx := context.Background()
	repoName := "user/repo-merge"
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	if _, err := store.WriteFile(ctx, repoName, "main", "main.txt", "main change", []byte("main\n")); err != nil {
		t.Fatalf("WriteFile main failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "feature", "feature.txt", "feature change", []byte("feature\n")); err != nil {
		t.Fatalf("WriteFile feature failed: %v", err)
	}

	mainBeforeMerge, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA main before merge failed: %v", err)
	}

	mergeSHA, err := store.Merge(ctx, gitstore.MergeOptions{
		FullName:     repoName,
		BaseBranch:   "main",
		HeadBranch:   "feature",
		Committer:    "Test Bot",
		Email:        "bot@example.com",
		MergeMessage: "Merge feature branch",
	})
	if err != nil {
		t.Fatalf("Merge failed: %v", err)
	}

	mainAfterMerge, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA main after merge failed: %v", err)
	}
	if mergeSHA != mainAfterMerge {
		t.Fatalf("Merge SHA mismatch: got %s, main HEAD %s", mergeSHA, mainAfterMerge)
	}

	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	parentCount := gitParentCount(t, repoDir, mergeSHA)
	if parentCount != 2 {
		t.Fatalf("expected merge commit to have 2 parents, got %d", parentCount)
	}

	subject := strings.TrimSpace(runGit(t, repoDir, "show", "-s", "--format=%s", mergeSHA))
	if subject != "Merge feature branch" {
		t.Fatalf("unexpected merge subject: %q", subject)
	}

	diff, err := store.Compare(ctx, repoName, mainBeforeMerge, mergeSHA)
	if err != nil {
		t.Fatalf("Compare failed: %v", err)
	}
	if diff.AheadBy != 2 || diff.BehindBy != 0 {
		t.Fatalf("unexpected compare counts ahead=%d behind=%d", diff.AheadBy, diff.BehindBy)
	}
	if len(diff.Commits) != 2 {
		t.Fatalf("expected 2 commits in compare, got %d", len(diff.Commits))
	}
	if !hasCommitMessage(diff.Commits, "feature change") || !hasCommitMessage(diff.Commits, "Merge feature branch") {
		t.Fatalf("compare commits missing expected messages: %#v", diff.Commits)
	}
	if len(diff.Files) != 1 {
		t.Fatalf("expected 1 changed file in compare, got %d", len(diff.Files))
	}
	if diff.Files[0].Filename != "feature.txt" || diff.Files[0].Additions != 1 || diff.Files[0].Deletions != 0 {
		t.Fatalf("unexpected file diff info: %#v", diff.Files[0])
	}
}

func TestStore_RebaseAndCompare(t *testing.T) {
	store, cleanup := gitstore.NewTestStore(t)
	defer cleanup()

	ctx := context.Background()
	repoName := "user/repo-rebase"
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	if _, err := store.WriteFile(ctx, repoName, "main", "main.txt", "main change", []byte("main\n")); err != nil {
		t.Fatalf("WriteFile main failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "feature", "feature.txt", "feature change", []byte("feature\n")); err != nil {
		t.Fatalf("WriteFile feature failed: %v", err)
	}

	mainBeforeRebase, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA main before rebase failed: %v", err)
	}

	rebasedSHA, err := store.Rebase(ctx, gitstore.RebaseOptions{
		FullName:   repoName,
		BaseBranch: "main",
		HeadBranch: "feature",
		Committer:  "Test Bot",
		Email:      "bot@example.com",
	})
	if err != nil {
		t.Fatalf("Rebase failed: %v", err)
	}

	mainAfterRebase, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA main after rebase failed: %v", err)
	}
	if rebasedSHA != mainAfterRebase {
		t.Fatalf("Rebase SHA mismatch: got %s, main HEAD %s", rebasedSHA, mainAfterRebase)
	}

	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	parentCount := gitParentCount(t, repoDir, rebasedSHA)
	if parentCount != 1 {
		t.Fatalf("expected rebased HEAD to have 1 parent, got %d", parentCount)
	}

	parentSHA := strings.TrimSpace(runGit(t, repoDir, "rev-parse", rebasedSHA+"^"))
	if parentSHA != mainBeforeRebase {
		t.Fatalf("expected rebased HEAD parent %s, got %s", mainBeforeRebase, parentSHA)
	}

	diff, err := store.Compare(ctx, repoName, mainBeforeRebase, rebasedSHA)
	if err != nil {
		t.Fatalf("Compare failed: %v", err)
	}
	if diff.AheadBy != 1 || diff.BehindBy != 0 {
		t.Fatalf("unexpected compare counts ahead=%d behind=%d", diff.AheadBy, diff.BehindBy)
	}
	if len(diff.Commits) != 1 || diff.Commits[0].Message != "feature change" {
		t.Fatalf("unexpected compare commits: %#v", diff.Commits)
	}
	if len(diff.Files) != 1 {
		t.Fatalf("expected 1 changed file in compare, got %d", len(diff.Files))
	}
	if diff.Files[0].Filename != "feature.txt" || diff.Files[0].Additions != 1 || diff.Files[0].Deletions != 0 {
		t.Fatalf("unexpected file diff info: %#v", diff.Files[0])
	}
}

func TestStore_MergeConflictReturnsErrorAndDoesNotAdvanceBase(t *testing.T) {
	store, cleanup := gitstore.NewTestStore(t)
	defer cleanup()

	ctx := context.Background()
	repoName := "user/repo-merge-conflict"
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "conflict.txt", "add base file", []byte("line1\nshared\nline3\n")); err != nil {
		t.Fatalf("WriteFile base failed: %v", err)
	}
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	if _, err := store.WriteFile(ctx, repoName, "main", "conflict.txt", "main side change", []byte("line1\nmain-change\nline3\n")); err != nil {
		t.Fatalf("WriteFile main failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "feature", "conflict.txt", "feature side change", []byte("line1\nfeature-change\nline3\n")); err != nil {
		t.Fatalf("WriteFile feature failed: %v", err)
	}

	mainBeforeMerge, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA main before merge failed: %v", err)
	}

	_, mergeErr := store.Merge(ctx, gitstore.MergeOptions{
		FullName:     repoName,
		BaseBranch:   "main",
		HeadBranch:   "feature",
		Committer:    "Test Bot",
		Email:        "bot@example.com",
		MergeMessage: "merge with conflict",
	})
	if mergeErr == nil {
		t.Fatal("expected merge conflict error, got nil")
	}
	errMsg := strings.ToLower(mergeErr.Error())
	if !strings.Contains(errMsg, "conflict") {
		t.Fatalf("expected conflict-related error, got: %v", mergeErr)
	}

	mainAfterMerge, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA main after merge failed: %v", err)
	}
	if mainAfterMerge != mainBeforeMerge {
		t.Fatalf("expected main HEAD to remain %s after failed merge, got %s", mainBeforeMerge, mainAfterMerge)
	}
}

func TestStore_RebaseConflictReturnsErrorAndDoesNotAdvanceBase(t *testing.T) {
	store, cleanup := gitstore.NewTestStore(t)
	defer cleanup()

	ctx := context.Background()
	repoName := "user/repo-rebase-conflict"
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "conflict.txt", "add base file", []byte("line1\nshared\nline3\n")); err != nil {
		t.Fatalf("WriteFile base failed: %v", err)
	}
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	if _, err := store.WriteFile(ctx, repoName, "main", "conflict.txt", "main side change", []byte("line1\nmain-change\nline3\n")); err != nil {
		t.Fatalf("WriteFile main failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "feature", "conflict.txt", "feature side change", []byte("line1\nfeature-change\nline3\n")); err != nil {
		t.Fatalf("WriteFile feature failed: %v", err)
	}

	mainBeforeRebase, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA main before rebase failed: %v", err)
	}

	_, rebaseErr := store.Rebase(ctx, gitstore.RebaseOptions{
		FullName:   repoName,
		BaseBranch: "main",
		HeadBranch: "feature",
		Committer:  "Test Bot",
		Email:      "bot@example.com",
	})
	if rebaseErr == nil {
		t.Fatal("expected rebase conflict error, got nil")
	}
	errMsg := strings.ToLower(rebaseErr.Error())
	if !strings.Contains(errMsg, "conflict") {
		t.Fatalf("expected conflict-related error, got: %v", rebaseErr)
	}

	mainAfterRebase, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA main after rebase failed: %v", err)
	}
	if mainAfterRebase != mainBeforeRebase {
		t.Fatalf("expected main HEAD to remain %s after failed rebase, got %s", mainBeforeRebase, mainAfterRebase)
	}
}

func TestStore_SquashMergeAndCompare(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-squash-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-squash"
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	if _, err := store.WriteFile(ctx, repoName, "main", "main.txt", "main change", []byte("main\n")); err != nil {
		t.Fatalf("WriteFile main failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "feature", "feature.txt", "feature change", []byte("feature\n")); err != nil {
		t.Fatalf("WriteFile feature failed: %v", err)
	}

	mainBeforeMerge, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA main before squash merge failed: %v", err)
	}

	squashSHA, err := store.SquashMerge(ctx, gitstore.SquashMergeOptions{
		FullName:      repoName,
		BaseBranch:    "main",
		HeadBranch:    "feature",
		Committer:     "Test Bot",
		Email:         "bot@example.com",
		SquashMessage: "Squash merge feature branch",
	})
	if err != nil {
		t.Fatalf("SquashMerge failed: %v", err)
	}

	mainAfterMerge, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA main after squash merge failed: %v", err)
	}
	if squashSHA != mainAfterMerge {
		t.Fatalf("SquashMerge SHA mismatch: got %s, main HEAD %s", squashSHA, mainAfterMerge)
	}

	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	parentCount := gitParentCount(t, repoDir, squashSHA)
	if parentCount != 1 {
		t.Fatalf("expected squash commit to have 1 parent, got %d", parentCount)
	}

	// Assert squash commit's parent equals pre-op HeadSHA(main)
	parentSHA := strings.TrimSpace(runGit(t, repoDir, "rev-parse", squashSHA+"^"))
	if parentSHA != mainBeforeMerge {
		t.Fatalf("expected squash commit parent %s, got %s", mainBeforeMerge, parentSHA)
	}

	// Assert both main.txt and feature.txt exist in the squash commit tree
	treeOutput := runGit(t, repoDir, "ls-tree", "--name-only", squashSHA)
	for _, want := range []string{"main.txt", "feature.txt"} {
		if !strings.Contains(treeOutput, want) {
			t.Fatalf("expected %s in squash commit tree, got:\n%s", want, treeOutput)
		}
	}

	subject := strings.TrimSpace(runGit(t, repoDir, "show", "-s", "--format=%s", squashSHA))
	if subject != "Squash merge feature branch" {
		t.Fatalf("unexpected squash commit subject: %q", subject)
	}

	diff, err := store.Compare(ctx, repoName, mainBeforeMerge, squashSHA)
	if err != nil {
		t.Fatalf("Compare failed: %v", err)
	}
	if diff.AheadBy != 1 || diff.BehindBy != 0 {
		t.Fatalf("unexpected compare counts ahead=%d behind=%d", diff.AheadBy, diff.BehindBy)
	}
	if len(diff.Commits) != 1 {
		t.Fatalf("expected 1 commit in compare, got %d", len(diff.Commits))
	}
	if diff.Commits[0].Message != "Squash merge feature branch" {
		t.Fatalf("unexpected commit message: %q", diff.Commits[0].Message)
	}
	if len(diff.Files) != 1 {
		t.Fatalf("expected 1 changed file in compare, got %d", len(diff.Files))
	}
	if diff.Files[0].Filename != "feature.txt" || diff.Files[0].Additions != 1 || diff.Files[0].Deletions != 0 {
		t.Fatalf("unexpected file diff info: %#v", diff.Files[0])
	}
}

func TestStore_SquashMergeConflictReturnsErrorAndDoesNotAdvanceBase(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-squash-conflict-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-squash-conflict"
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "conflict.txt", "add base file", []byte("line1\nshared\nline3\n")); err != nil {
		t.Fatalf("WriteFile base failed: %v", err)
	}
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	if _, err := store.WriteFile(ctx, repoName, "main", "conflict.txt", "main side change", []byte("line1\nmain-change\nline3\n")); err != nil {
		t.Fatalf("WriteFile main failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "feature", "conflict.txt", "feature side change", []byte("line1\nfeature-change\nline3\n")); err != nil {
		t.Fatalf("WriteFile feature failed: %v", err)
	}

	mainBeforeMerge, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA main before squash merge failed: %v", err)
	}

	_, mergeErr := store.SquashMerge(ctx, gitstore.SquashMergeOptions{
		FullName:      repoName,
		BaseBranch:    "main",
		HeadBranch:    "feature",
		Committer:     "Test Bot",
		Email:         "bot@example.com",
		SquashMessage: "squash with conflict",
	})
	if mergeErr == nil {
		t.Fatal("expected squash merge conflict error, got nil")
	}
	errMsg := strings.ToLower(mergeErr.Error())
	if !strings.Contains(errMsg, "conflict") {
		t.Fatalf("expected conflict-related error, got: %v", mergeErr)
	}

	mainAfterMerge, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA main after failed squash merge failed: %v", err)
	}
	if mainAfterMerge != mainBeforeMerge {
		t.Fatalf("expected main HEAD to remain %s after failed squash merge, got %s", mainBeforeMerge, mainAfterMerge)
	}
}

func TestStore_SquashMergeIdenticalBranches(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-squash-identical-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-squash-identical"
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "initial commit", []byte("content\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	mainBefore, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA main before squash merge failed: %v", err)
	}

	_, mergeErr := store.SquashMerge(ctx, gitstore.SquashMergeOptions{
		FullName:      repoName,
		BaseBranch:    "main",
		HeadBranch:    "feature",
		Committer:     "Test Bot",
		Email:         "bot@example.com",
		SquashMessage: "squash identical",
	})
	if mergeErr == nil {
		t.Fatal("expected error for squash merge with no changes, got nil")
	}
	if errMsg := mergeErr.Error(); !strings.Contains(errMsg, "commit (squash) failed") {
		t.Fatalf("expected error about failed squash commit (nothing to commit), got: %v", mergeErr)
	}

	mainAfter, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA main after failed squash merge failed: %v", err)
	}
	if mainAfter != mainBefore {
		t.Fatalf("expected main HEAD to remain %s after failed squash merge, got %s", mainBefore, mainAfter)
	}
}

func TestStore_SquashMergeNonexistentHead(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-squash-nohead-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-squash-nohead"
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "initial commit", []byte("content\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	mainBefore, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA main before squash merge failed: %v", err)
	}

	_, mergeErr := store.SquashMerge(ctx, gitstore.SquashMergeOptions{
		FullName:      repoName,
		BaseBranch:    "main",
		HeadBranch:    "no-such-branch",
		Committer:     "Test Bot",
		Email:         "bot@example.com",
		SquashMessage: "squash nonexistent",
	})
	if mergeErr == nil {
		t.Fatal("expected error for squash merge with nonexistent head branch, got nil")
	}
	if errMsg := mergeErr.Error(); !strings.Contains(errMsg, "merge --squash failed") {
		t.Fatalf("expected error about failed squash merge of missing branch, got: %v", mergeErr)
	}

	mainAfter, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA main after failed squash merge failed: %v", err)
	}
	if mainAfter != mainBefore {
		t.Fatalf("expected main HEAD to remain %s after failed squash merge, got %s", mainBefore, mainAfter)
	}
}

func TestStore_WriteFileCommitIdentity(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-writefile-identity-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-identity-write"
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	sha, err := store.WriteFile(ctx, repoName, "main", "file.txt", "test commit", []byte("content\n"))
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Assert branch tip advancement
	headSHA, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA failed: %v", err)
	}
	if sha != headSHA {
		t.Fatalf("expected WriteFile SHA %s to equal HeadSHA(main) %s", sha, headSHA)
	}

	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}

	// Assert file.txt exists in commit tree
	treeOutput := runGit(t, repoDir, "ls-tree", "--name-only", sha)
	if !strings.Contains(treeOutput, "file.txt") {
		t.Fatalf("expected file.txt in commit tree, got:\n%s", treeOutput)
	}

	identity := strings.TrimSpace(runGit(t, repoDir, "log", "-1", "--format=%an <%ae> %cn <%ce>", sha))
	expected := "gh-server <gh-server@localhost> gh-server <gh-server@localhost>"
	if identity != expected {
		t.Fatalf("unexpected commit identity:\n  got:  %s\n  want: %s", identity, expected)
	}
}

func TestStore_DeleteFileFromRepoCommitIdentity(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-deletefile-identity-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-identity-delete"
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "add file", []byte("content\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	sha, err := store.DeleteFileFromRepo(ctx, repoName, "main", "file.txt", "delete file")
	if err != nil {
		t.Fatalf("DeleteFileFromRepo failed: %v", err)
	}

	// Assert branch tip advancement
	headSHA, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA failed: %v", err)
	}
	if sha != headSHA {
		t.Fatalf("expected DeleteFileFromRepo SHA %s to equal HeadSHA(main) %s", sha, headSHA)
	}

	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}

	// Assert file.txt is absent from commit tree
	treeOutput := runGit(t, repoDir, "ls-tree", "-r", "--name-only", sha)
	if strings.Contains(treeOutput, "file.txt") {
		t.Fatalf("expected file.txt to be absent from commit tree after delete, got:\n%s", treeOutput)
	}

	identity := strings.TrimSpace(runGit(t, repoDir, "log", "-1", "--format=%an <%ae> %cn <%ce>", sha))
	expected := "gh-server <gh-server@localhost> gh-server <gh-server@localhost>"
	if identity != expected {
		t.Fatalf("unexpected commit identity:\n  got:  %s\n  want: %s", identity, expected)
	}
}

// TestStore_WriteFileNestedPath tests creating files in nested directories (issue #208)
func TestStore_WriteFileNestedPath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-nested-path-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-nested"
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Test creating a file in a nested directory (.github/workflows/ci.yml)
	nestedPath := ".github/workflows/ci.yml"
	content := []byte("name: ci\non:\n  workflow_dispatch:\n")
	sha, err := store.WriteFile(ctx, repoName, "main", nestedPath, "add workflow file", content)
	if err != nil {
		t.Fatalf("WriteFile nested path failed: %v", err)
	}

	// Assert branch tip advancement
	headSHA, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA failed: %v", err)
	}
	if sha != headSHA {
		t.Fatalf("expected WriteFile SHA %s to equal HeadSHA(main) %s", sha, headSHA)
	}

	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}

	// Assert the nested file exists in commit tree
	treeOutput := runGit(t, repoDir, "ls-tree", "-r", "--name-only", sha)
	if !strings.Contains(treeOutput, nestedPath) {
		t.Fatalf("expected %s in commit tree, got:\n%s", nestedPath, treeOutput)
	}

	// Assert the content is correct
	readContent, err := store.ReadFile(ctx, repoName, nestedPath)
	if err != nil {
		t.Fatalf("ReadFile nested path failed: %v", err)
	}
	if string(readContent) != string(content) {
		t.Fatalf("content mismatch:\n  got:  %s\n  want: %s", readContent, content)
	}

	// Test creating another file in the same nested directory
	secondPath := ".github/workflows/release.yml"
	secondContent := []byte("name: release\non:\n  push:\n    tags:\n      - 'v*'\n")
	sha2, err := store.WriteFile(ctx, repoName, "main", secondPath, "add release workflow", secondContent)
	if err != nil {
		t.Fatalf("WriteFile second nested path failed: %v", err)
	}

	// Verify both files exist
	treeOutput2 := runGit(t, repoDir, "ls-tree", "-r", "--name-only", sha2)
	if !strings.Contains(treeOutput2, nestedPath) {
		t.Fatalf("expected %s in commit tree after second write, got:\n%s", nestedPath, treeOutput2)
	}
	if !strings.Contains(treeOutput2, secondPath) {
		t.Fatalf("expected %s in commit tree after second write, got:\n%s", secondPath, treeOutput2)
	}

	// Test creating a file in a deeply nested directory
	deepPath := "a/b/c/d/deep.txt"
	deepContent := []byte("deep nested content")
	sha3, err := store.WriteFile(ctx, repoName, "main", deepPath, "add deeply nested file", deepContent)
	if err != nil {
		t.Fatalf("WriteFile deeply nested path failed: %v", err)
	}

	treeOutput3 := runGit(t, repoDir, "ls-tree", "-r", "--name-only", sha3)
	if !strings.Contains(treeOutput3, deepPath) {
		t.Fatalf("expected %s in commit tree, got:\n%s", deepPath, treeOutput3)
	}
}

// ============================================================================
// Issue #405: Gitstore tenant isolation coverage tests
// ============================================================================

// TestStore_RootForCtx_ExplicitTenant tests rootForCtx with an explicit tenant in context.
func TestStore_RootForCtx_ExplicitTenant(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-tenant-explicit-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir, gitstore.WithTenantIsolation())
	if err != nil {
		t.Fatal(err)
	}

	ctx := tenant.ContextWithTenant(context.Background(), "tenant-alpha")
	root, err := store.RepoRoot(ctx)
	if err != nil {
		t.Fatalf("RepoRoot failed: %v", err)
	}

	expected := filepath.Join(tmpDir, "tenant-alpha")
	if root != expected {
		t.Fatalf("RepoRoot mismatch: got %q, want %q", root, expected)
	}
}

// TestStore_RootForCtx_MissingTenantWithDefault tests rootForCtx when tenant is missing
// but a default tenant is configured.
func TestStore_RootForCtx_MissingTenantWithDefault(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-tenant-default-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir, gitstore.WithTenantIsolation(), gitstore.WithDefaultTenant("default"))
	if err != nil {
		t.Fatal(err)
	}

	// Context without tenant should fall back to default
	ctx := context.Background()
	root, err := store.RepoRoot(ctx)
	if err != nil {
		t.Fatalf("RepoRoot with default tenant failed: %v", err)
	}

	expected := filepath.Join(tmpDir, "default")
	if root != expected {
		t.Fatalf("RepoRoot mismatch: got %q, want %q", root, expected)
	}
}

// TestStore_RootForCtx_MissingTenantNoDefault tests rootForCtx when tenant is missing
// and no default is configured (should error).
func TestStore_RootForCtx_MissingTenantNoDefault(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-tenant-missing-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir, gitstore.WithTenantIsolation())
	if err != nil {
		t.Fatal(err)
	}

	// Context without tenant and no default should error
	ctx := context.Background()
	_, err = store.RepoRoot(ctx)
	if err == nil {
		t.Fatal("expected error for missing tenant, got nil")
	}
	if !strings.Contains(err.Error(), "missing tenant") {
		t.Fatalf("expected 'missing tenant' error, got: %v", err)
	}
}

// TestStore_RootForCtx_InvalidTenantValues tests rootForCtx with invalid tenant values.
func TestStore_RootForCtx_InvalidTenantValues(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-tenant-invalid-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir, gitstore.WithTenantIsolation())
	if err != nil {
		t.Fatal(err)
	}

	invalidTenants := []struct {
		name  string
		value string
	}{
		{"dot", "."},
		{"dotdot", ".."},
		{"slash", "tenant/alpha"},
		{"backslash", "tenant\\alpha"},
		{"null byte", "tenant\x00alpha"},
		{"empty string", ""},
	}

	for _, tc := range invalidTenants {
		t.Run(tc.name, func(t *testing.T) {
			ctx := tenant.ContextWithTenant(context.Background(), tc.value)
			_, err := store.RepoRoot(ctx)
			if err == nil {
				t.Fatalf("expected error for invalid tenant %q, got nil", tc.value)
			}
			// Empty string is treated as missing tenant (FromContext returns false for empty)
			// Other invalid values should produce "invalid tenant" errors
			if tc.value == "" {
				if !strings.Contains(err.Error(), "missing tenant") {
					t.Fatalf("expected 'missing tenant' error for empty string, got: %v", err)
				}
			} else if !strings.Contains(err.Error(), "invalid tenant") && !strings.Contains(err.Error(), "empty tenant") {
				t.Fatalf("expected 'invalid tenant' or 'empty tenant' error, got: %v", err)
			}
		})
	}
}

// TestStore_ValidateTenantSegment tests tenant validation through the public API.
// Since validateTenantSegment is internal, we test it via RepoRoot which uses it.
func TestStore_ValidateTenantSegment(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-tenant-validate-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir, gitstore.WithTenantIsolation())
	if err != nil {
		t.Fatal(err)
	}

	validTenants := []string{
		"default",
		"tenant-alpha",
		"tenant_123",
		"Tenant-Alpha-123",
		"a",
		"very-long-tenant-name-with-many-characters",
	}

	for _, tnt := range validTenants {
		t.Run("valid_"+tnt, func(t *testing.T) {
			ctx := tenant.ContextWithTenant(context.Background(), tnt)
			root, err := store.RepoRoot(ctx)
			if err != nil {
				t.Fatalf("expected valid tenant %q to work, got error: %v", tnt, err)
			}
			// Verify the root path contains the tenant name
			if !strings.HasSuffix(root, tnt) {
				t.Fatalf("expected root to end with tenant %q, got %q", tnt, root)
			}
		})
	}

	invalidTenants := []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"dot", "."},
		{"dotdot", ".."},
		{"slash", "a/b"},
		{"backslash", "a\\b"},
		{"null byte", "a\x00b"},
	}

	for _, tc := range invalidTenants {
		t.Run("invalid_"+tc.name, func(t *testing.T) {
			ctx := tenant.ContextWithTenant(context.Background(), tc.value)
			_, err := store.RepoRoot(ctx)
			if err == nil {
				t.Fatalf("expected error for invalid tenant %q, got nil", tc.value)
			}
		})
	}
}

// TestStore_TenantIsolation_PathBoundary tests that tenant-scoped roots never escape
// expected boundaries through path traversal attempts.
func TestStore_TenantIsolation_PathBoundary(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-tenant-boundary-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir, gitstore.WithTenantIsolation())
	if err != nil {
		t.Fatal(err)
	}

	// Test that path traversal attempts in repo names are rejected
	traversalAttempts := []string{
		"../escape/repo",
		"..\\escape\\repo",
		"owner/../../../escape",
		"owner/repo-with/..\\..\\escape",
		"../..",
		"owner/..",
	}

	ctx := tenant.ContextWithTenant(context.Background(), "tenant-alpha")
	for _, attempt := range traversalAttempts {
		t.Run("traversal_"+attempt, func(t *testing.T) {
			_, err := store.GetRepoPath(ctx, attempt)
			if err == nil {
				t.Fatalf("expected error for path traversal attempt %q, got nil", attempt)
			}
		})
	}

	// Verify that valid repo paths are correctly scoped to tenant directory
	validRepo := "owner/repo"
	repoPath, err := store.GetRepoPath(ctx, validRepo)
	if err != nil {
		t.Fatalf("GetRepoPath failed for valid repo: %v", err)
	}

	expectedPrefix := filepath.Join(tmpDir, "tenant-alpha")
	if !strings.HasPrefix(repoPath, expectedPrefix) {
		t.Fatalf("repo path %q is not scoped to tenant directory %q", repoPath, expectedPrefix)
	}
}

// TestStore_RepoPath_TenantIsolation tests repoPath with tenant isolation enabled,
// ensuring proper tenant scoping and validation.
func TestStore_RepoPath_TenantIsolation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-tenant-repopath-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir, gitstore.WithTenantIsolation(), gitstore.WithDefaultTenant("default"))
	if err != nil {
		t.Fatal(err)
	}

	// Test 1: Explicit tenant in context
	ctxWithTenant := tenant.ContextWithTenant(context.Background(), "tenant-beta")
	repoPath1, err := store.GetRepoPath(ctxWithTenant, "owner/repo1")
	if err != nil {
		t.Fatalf("GetRepoPath with explicit tenant failed: %v", err)
	}
	expectedPath1 := filepath.Join(tmpDir, "tenant-beta", "owner", "repo1.git")
	if repoPath1 != expectedPath1 {
		t.Fatalf("repo path mismatch: got %q, want %q", repoPath1, expectedPath1)
	}

	// Test 2: Missing tenant falls back to default
	ctxNoTenant := context.Background()
	repoPath2, err := store.GetRepoPath(ctxNoTenant, "owner/repo2")
	if err != nil {
		t.Fatalf("GetRepoPath with default tenant failed: %v", err)
	}
	expectedPath2 := filepath.Join(tmpDir, "default", "owner", "repo2.git")
	if repoPath2 != expectedPath2 {
		t.Fatalf("repo path mismatch: got %q, want %q", repoPath2, expectedPath2)
	}

	// Test 3: Different tenants get different paths
	repoPath3, err := store.GetRepoPath(ctxWithTenant, "owner/repo3")
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	if filepath.Dir(filepath.Dir(repoPath3)) != filepath.Dir(filepath.Dir(repoPath1)) {
		t.Fatalf("repos for same tenant should be in same tenant directory")
	}
	if filepath.Dir(filepath.Dir(repoPath2)) == filepath.Dir(filepath.Dir(repoPath1)) {
		t.Fatalf("repos for different tenants should be in different directories")
	}
}

// TestStore_Exists_TenantIsolation tests that Exists properly respects tenant isolation.
func TestStore_Exists_TenantIsolation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-tenant-exists-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir, gitstore.WithTenantIsolation(), gitstore.WithDefaultTenant("default"))
	if err != nil {
		t.Fatal(err)
	}

	ctx1 := tenant.ContextWithTenant(context.Background(), "tenant-a")
	ctx2 := tenant.ContextWithTenant(context.Background(), "tenant-b")

	repoName := "owner/repo"

	// Init repo in tenant-a
	if err := store.Init(ctx1, repoName, "main", true); err != nil {
		t.Fatalf("Init in tenant-a failed: %v", err)
	}

	// Repo should exist in tenant-a
	if !store.Exists(ctx1, repoName) {
		t.Error("expected repo to exist in tenant-a")
	}

	// Repo should NOT exist in tenant-b (different tenant)
	if store.Exists(ctx2, repoName) {
		t.Error("expected repo to not exist in tenant-b")
	}

	// Repo should NOT exist in default tenant
	if store.Exists(context.Background(), repoName) {
		t.Error("expected repo to not exist in default tenant")
	}
}

// TestStore_MalformedContextValues tests handling of malformed context values.
func TestStore_MalformedContextValues(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-tenant-malformed-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir, gitstore.WithTenantIsolation())
	if err != nil {
		t.Fatal(err)
	}

	// Test with context that has non-string tenant value (should be treated as missing)
	type badKey struct{}
	badCtx := context.WithValue(context.Background(), badKey{}, 12345)

	// Should error because tenant is not a string (FromContext returns false)
	_, err = store.RepoRoot(badCtx)
	if err == nil {
		t.Fatal("expected error for malformed context (non-string tenant), got nil")
	}
}

// TestStore_UnauthorizedTenantAccess tests that unauthorized tenant access is prevented.
func TestStore_UnauthorizedTenantAccess(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-tenant-unauth-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir, gitstore.WithTenantIsolation())
	if err != nil {
		t.Fatal(err)
	}

	// Create a repo in tenant-alpha's directory
	ctxAlpha := tenant.ContextWithTenant(context.Background(), "tenant-alpha")
	repoName := "owner/repo"
	if err := store.Init(ctxAlpha, repoName, "main", true); err != nil {
		t.Fatalf("Init in tenant-alpha failed: %v", err)
	}

	// tenant-beta should not be able to access tenant-alpha's repo
	ctxBeta := tenant.ContextWithTenant(context.Background(), "tenant-beta")
	if store.Exists(ctxBeta, repoName) {
		t.Error("tenant-beta should not be able to see tenant-alpha's repo")
	}

	// Verify the paths are different
	pathAlpha, _ := store.GetRepoPath(ctxAlpha, repoName)
	pathBeta, _ := store.GetRepoPath(ctxBeta, repoName)
	if pathAlpha == pathBeta {
		t.Fatalf("paths for different tenants should be different: both got %q", pathAlpha)
	}
}

func runGit(t *testing.T, repoDir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
	return string(out)
}

func gitParentCount(t *testing.T, repoDir, sha string) int {
	t.Helper()
	line := strings.TrimSpace(runGit(t, repoDir, "rev-list", "--parents", "-n", "1", sha))
	fields := strings.Fields(line)
	if len(fields) == 0 {
		t.Fatalf("unexpected empty rev-list output for %s", sha)
	}
	return len(fields) - 1
}

func hasCommitMessage(commits []gitstore.CommitInfo, msg string) bool {
	for _, commit := range commits {
		if commit.Message == msg {
			return true
		}
	}
	return false
}
