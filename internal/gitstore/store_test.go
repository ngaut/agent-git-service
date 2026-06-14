package gitstore_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/gitstore"
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
	readme, err := store.ReadFile(ctx, repoName, "README.md")
	if err != nil {
		t.Fatalf("ReadFile README.md failed: %v", err)
	}
	if len(readme) != 0 {
		t.Fatalf("expected auto-init README.md to be empty, got %q", readme)
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
