package gitstore_test

import (
	"context"
	"math/rand"
	"os"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/gitstore"
)

// ============================================================================
// RevertMerge Tests
// ============================================================================

// TestStore_RevertMerge_SuccessfulRevert tests that RevertMerge successfully
// creates a revert commit for a merge commit and pushes it to a new branch.
func TestStore_RevertMerge_SuccessfulRevert(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-revert-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-revert"

	// Initialize repo with main branch
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create initial commit on main
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "initial commit", []byte("initial\n")); err != nil {
		t.Fatalf("WriteFile initial failed: %v", err)
	}

	// Create feature branch
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	// Add commit to feature branch
	if _, err := store.WriteFile(ctx, repoName, "feature", "feature.txt", "add feature", []byte("feature\n")); err != nil {
		t.Fatalf("WriteFile feature failed: %v", err)
	}

	// Merge feature into main (creates merge commit)
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

	// Verify merge commit has 2 parents
	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	parentCount := gitParentCount(t, repoDir, mergeSHA)
	if parentCount != 2 {
		t.Fatalf("expected merge commit to have 2 parents, got %d", parentCount)
	}

	// Get main SHA after merge
	mainAfterMerge, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA after merge failed: %v", err)
	}

	// Revert the merge commit
	revertBranchName := "revert-merge"
	revertSHA, err := store.RevertMerge(ctx, repoName, "main", mergeSHA, revertBranchName)
	if err != nil {
		t.Fatalf("RevertMerge failed: %v", err)
	}

	// Verify revert branch was created
	revertBranchSHA, err := store.HeadSHA(ctx, repoName, revertBranchName)
	if err != nil {
		t.Fatalf("HeadSHA revert branch failed: %v", err)
	}
	if revertSHA != revertBranchSHA {
		t.Fatalf("Revert SHA mismatch: got %s, revert branch HEAD %s", revertSHA, revertBranchSHA)
	}

	// Verify main branch was not modified
	mainAfterRevert, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA main after revert failed: %v", err)
	}
	if mainAfterRevert != mainAfterMerge {
		t.Fatalf("expected main HEAD to remain %s after revert, got %s", mainAfterMerge, mainAfterRevert)
	}

	// Verify revert commit has 1 parent (the main branch)
	revertParentCount := gitParentCount(t, repoDir, revertSHA)
	if revertParentCount != 1 {
		t.Fatalf("expected revert commit to have 1 parent, got %d", revertParentCount)
	}

	// Verify revert commit's parent is the merge commit
	parentSHA := strings.TrimSpace(runGit(t, repoDir, "rev-parse", revertSHA+"^"))
	if parentSHA != mainAfterMerge {
		t.Fatalf("expected revert commit parent %s, got %s", mainAfterMerge, parentSHA)
	}

	// Verify the revert commit message contains "Revert"
	subject := strings.TrimSpace(runGit(t, repoDir, "show", "-s", "--format=%s", revertSHA))
	if !strings.HasPrefix(subject, "Revert") {
		t.Fatalf("expected revert commit message to start with 'Revert', got: %q", subject)
	}
}

// TestStore_RevertMerge_InvalidTarget tests that RevertMerge returns an error
// when the target commit SHA is invalid or doesn't exist.
func TestStore_RevertMerge_InvalidTarget(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-revert-invalid-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-revert-invalid"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create initial commit
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "initial commit", []byte("initial\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Try to revert with invalid SHA
	invalidSHA := "0000000000000000000000000000000000000000"
	_, err = store.RevertMerge(ctx, repoName, "main", invalidSHA, "revert-invalid")
	if err == nil {
		t.Fatal("expected RevertMerge to fail with invalid SHA, got nil")
	}
	if !strings.Contains(err.Error(), "revert") && !strings.Contains(err.Error(), "fatal") {
		t.Fatalf("expected revert-related error, got: %v", err)
	}
}

// TestStore_RevertMerge_NonExistentRepo tests that RevertMerge returns an error
// when the repository doesn't exist.
func TestStore_RevertMerge_NonExistentRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-revert-norepo-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Try to revert on non-existent repo
	_, err = store.RevertMerge(ctx, "nonexistent/repo", "main", "abc123", "revert-branch")
	if err == nil {
		t.Fatal("expected RevertMerge to fail with non-existent repo, got nil")
	}
	if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "repo") {
		t.Fatalf("expected 'not found' error, got: %v", err)
	}
}

// TestStore_RevertMerge_NonMergeCommit tests that RevertMerge can handle
// reverting a non-merge commit (single parent).
func TestStore_RevertMerge_NonMergeCommit(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-revert-nonmerge-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-revert-nonmerge"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create initial commit
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "initial commit", []byte("initial\n")); err != nil {
		t.Fatalf("WriteFile initial failed: %v", err)
	}

	// Create second commit
	secondSHA, err := store.WriteFile(ctx, repoName, "main", "file2.txt", "second commit", []byte("second\n"))
	if err != nil {
		t.Fatalf("WriteFile second failed: %v", err)
	}

	// Verify second commit has 1 parent
	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	parentCount := gitParentCount(t, repoDir, secondSHA)
	if parentCount != 1 {
		t.Fatalf("expected second commit to have 1 parent, got %d", parentCount)
	}

	// Revert the second commit
	revertBranchName := "revert-nonmerge"
	revertSHA, err := store.RevertMerge(ctx, repoName, "main", secondSHA, revertBranchName)
	if err != nil {
		t.Fatalf("RevertMerge failed: %v", err)
	}

	// Verify revert branch was created
	revertBranchSHA, err := store.HeadSHA(ctx, repoName, revertBranchName)
	if err != nil {
		t.Fatalf("HeadSHA revert branch failed: %v", err)
	}
	if revertSHA != revertBranchSHA {
		t.Fatalf("Revert SHA mismatch: got %s, revert branch HEAD %s", revertSHA, revertBranchSHA)
	}

	// Verify the revert undoes the changes (file2.txt should not exist in revert commit tree)
	treeOutput := runGit(t, repoDir, "ls-tree", "--name-only", revertSHA)
	if strings.Contains(treeOutput, "file2.txt") {
		t.Fatalf("expected file2.txt to be absent from revert commit tree, got:\n%s", treeOutput)
	}
}

// ============================================================================
// UpdatePRBranch Tests
// ============================================================================

// TestStore_UpdatePRBranch_FastForwardMerge tests that UpdatePRBranch with
// merge method successfully merges base into head when there are no conflicts.
func TestStore_UpdatePRBranch_FastForwardMerge(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-update-ff-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-update-ff"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create initial commit on main
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "initial commit", []byte("initial\n")); err != nil {
		t.Fatalf("WriteFile initial failed: %v", err)
	}

	// Create feature branch
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	// Get feature SHA before update
	featureBeforeUpdate, err := store.HeadSHA(ctx, repoName, "feature")
	if err != nil {
		t.Fatalf("HeadSHA feature before update failed: %v", err)
	}

	// Add commit to main (not feature)
	if _, err := store.WriteFile(ctx, repoName, "main", "main.txt", "add main file", []byte("main\n")); err != nil {
		t.Fatalf("WriteFile main failed: %v", err)
	}

	// Update feature branch with merge (default method)
	updatedSHA, err := store.UpdatePRBranch(ctx, gitstore.UpdatePRBranchOptions{
		FullName:     repoName,
		BaseBranch:   "main",
		HeadBranch:   "feature",
		Committer:    "Test Bot",
		Email:        "bot@example.com",
		UpdateMethod: "merge",
	})
	if err != nil {
		t.Fatalf("UpdatePRBranch failed: %v", err)
	}

	// Verify feature branch was updated
	featureAfterUpdate, err := store.HeadSHA(ctx, repoName, "feature")
	if err != nil {
		t.Fatalf("HeadSHA feature after update failed: %v", err)
	}
	if updatedSHA != featureAfterUpdate {
		t.Fatalf("Update SHA mismatch: got %s, feature HEAD %s", updatedSHA, featureAfterUpdate)
	}

	// Verify feature branch advanced (different from before)
	if featureAfterUpdate == featureBeforeUpdate {
		t.Fatal("expected feature branch to advance after update, but it didn't")
	}

	// Verify the merge commit has 2 parents (merge commit)
	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	parentCount := gitParentCount(t, repoDir, updatedSHA)
	if parentCount != 2 {
		t.Fatalf("expected merge commit to have 2 parents, got %d", parentCount)
	}

	// Verify main.txt is now in feature branch tree
	treeOutput := runGit(t, repoDir, "ls-tree", "--name-only", updatedSHA)
	if !strings.Contains(treeOutput, "main.txt") {
		t.Fatalf("expected main.txt in feature branch tree after update, got:\n%s", treeOutput)
	}
}

// TestStore_UpdatePRBranch_Rebase tests that UpdatePRBranch with rebase method
// successfully rebases head onto base when there are no conflicts.
func TestStore_UpdatePRBranch_Rebase(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-update-rebase-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-update-rebase"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create initial commit on main
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "initial commit", []byte("initial\n")); err != nil {
		t.Fatalf("WriteFile initial failed: %v", err)
	}

	// Create feature branch
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	// Add commit to feature
	if _, err := store.WriteFile(ctx, repoName, "feature", "feature.txt", "add feature", []byte("feature\n")); err != nil {
		t.Fatalf("WriteFile feature failed: %v", err)
	}

	// Add commit to main (not feature)
	if _, err := store.WriteFile(ctx, repoName, "main", "main.txt", "add main file", []byte("main\n")); err != nil {
		t.Fatalf("WriteFile main failed: %v", err)
	}

	// Update feature branch with rebase method
	updatedSHA, err := store.UpdatePRBranch(ctx, gitstore.UpdatePRBranchOptions{
		FullName:     repoName,
		BaseBranch:   "main",
		HeadBranch:   "feature",
		Committer:    "Test Bot",
		Email:        "bot@example.com",
		UpdateMethod: "rebase",
	})
	if err != nil {
		t.Fatalf("UpdatePRBranch rebase failed: %v", err)
	}

	// Verify feature branch was updated
	featureAfterUpdate, err := store.HeadSHA(ctx, repoName, "feature")
	if err != nil {
		t.Fatalf("HeadSHA feature after update failed: %v", err)
	}
	if updatedSHA != featureAfterUpdate {
		t.Fatalf("Update SHA mismatch: got %s, feature HEAD %s", updatedSHA, featureAfterUpdate)
	}

	// Verify the rebased commit has 1 parent (linear history)
	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	parentCount := gitParentCount(t, repoDir, updatedSHA)
	if parentCount != 1 {
		t.Fatalf("expected rebased commit to have 1 parent, got %d", parentCount)
	}

	// Verify main.txt is now in feature branch tree
	treeOutput := runGit(t, repoDir, "ls-tree", "--name-only", updatedSHA)
	if !strings.Contains(treeOutput, "main.txt") {
		t.Fatalf("expected main.txt in feature branch tree after rebase, got:\n%s", treeOutput)
	}

	// Verify feature.txt is still in feature branch tree
	if !strings.Contains(treeOutput, "feature.txt") {
		t.Fatalf("expected feature.txt in feature branch tree after rebase, got:\n%s", treeOutput)
	}
}

// TestStore_UpdatePRBranch_Conflict tests that UpdatePRBranch returns an error
// when there are merge conflicts between base and head.
func TestStore_UpdatePRBranch_Conflict(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-update-conflict-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-update-conflict"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create initial commit with shared file
	if _, err := store.WriteFile(ctx, repoName, "main", "conflict.txt", "initial commit", []byte("line1\nshared\nline3\n")); err != nil {
		t.Fatalf("WriteFile initial failed: %v", err)
	}

	// Create feature branch
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	// Modify conflict.txt on main
	if _, err := store.WriteFile(ctx, repoName, "main", "conflict.txt", "main change", []byte("line1\nmain-change\nline3\n")); err != nil {
		t.Fatalf("WriteFile main failed: %v", err)
	}

	// Modify conflict.txt on feature (same file, different change)
	if _, err := store.WriteFile(ctx, repoName, "feature", "conflict.txt", "feature change", []byte("line1\nfeature-change\nline3\n")); err != nil {
		t.Fatalf("WriteFile feature failed: %v", err)
	}

	// Get feature SHA before update attempt
	featureBeforeUpdate, err := store.HeadSHA(ctx, repoName, "feature")
	if err != nil {
		t.Fatalf("HeadSHA feature before update failed: %v", err)
	}

	// Try to update feature branch with merge (should fail with conflict)
	_, err = store.UpdatePRBranch(ctx, gitstore.UpdatePRBranchOptions{
		FullName:     repoName,
		BaseBranch:   "main",
		HeadBranch:   "feature",
		Committer:    "Test Bot",
		Email:        "bot@example.com",
		UpdateMethod: "merge",
	})
	if err == nil {
		t.Fatal("expected UpdatePRBranch to fail with conflict, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "conflict") {
		t.Fatalf("expected conflict-related error, got: %v", err)
	}

	// Verify feature branch was NOT modified
	featureAfterUpdate, err := store.HeadSHA(ctx, repoName, "feature")
	if err != nil {
		t.Fatalf("HeadSHA feature after update failed: %v", err)
	}
	if featureAfterUpdate != featureBeforeUpdate {
		t.Fatalf("expected feature HEAD to remain %s after failed update, got %s", featureBeforeUpdate, featureAfterUpdate)
	}
}

// TestStore_UpdatePRBranch_ConflictRebase tests that UpdatePRBranch with rebase
// method returns an error when there are conflicts.
func TestStore_UpdatePRBranch_ConflictRebase(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-update-conflict-rebase-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-update-conflict-rebase"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create initial commit with shared file
	if _, err := store.WriteFile(ctx, repoName, "main", "conflict.txt", "initial commit", []byte("line1\nshared\nline3\n")); err != nil {
		t.Fatalf("WriteFile initial failed: %v", err)
	}

	// Create feature branch
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	// Modify conflict.txt on main
	if _, err := store.WriteFile(ctx, repoName, "main", "conflict.txt", "main change", []byte("line1\nmain-change\nline3\n")); err != nil {
		t.Fatalf("WriteFile main failed: %v", err)
	}

	// Modify conflict.txt on feature (same file, different change)
	if _, err := store.WriteFile(ctx, repoName, "feature", "conflict.txt", "feature change", []byte("line1\nfeature-change\nline3\n")); err != nil {
		t.Fatalf("WriteFile feature failed: %v", err)
	}

	// Get feature SHA before update attempt
	featureBeforeUpdate, err := store.HeadSHA(ctx, repoName, "feature")
	if err != nil {
		t.Fatalf("HeadSHA feature before update failed: %v", err)
	}

	// Try to update feature branch with rebase (should fail with conflict)
	_, err = store.UpdatePRBranch(ctx, gitstore.UpdatePRBranchOptions{
		FullName:     repoName,
		BaseBranch:   "main",
		HeadBranch:   "feature",
		Committer:    "Test Bot",
		Email:        "bot@example.com",
		UpdateMethod: "rebase",
	})
	if err == nil {
		t.Fatal("expected UpdatePRBranch rebase to fail with conflict, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "conflict") {
		t.Fatalf("expected conflict-related error, got: %v", err)
	}

	// Verify feature branch was NOT modified
	featureAfterUpdate, err := store.HeadSHA(ctx, repoName, "feature")
	if err != nil {
		t.Fatalf("HeadSHA feature after update failed: %v", err)
	}
	if featureAfterUpdate != featureBeforeUpdate {
		t.Fatalf("expected feature HEAD to remain %s after failed update, got %s", featureBeforeUpdate, featureAfterUpdate)
	}
}

// TestStore_UpdatePRBranch_MissingRef tests that UpdatePRBranch returns an error
// when the head branch doesn't exist.
func TestStore_UpdatePRBranch_MissingRef(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-update-missing-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-update-missing"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create initial commit on main
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "initial commit", []byte("initial\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Try to update non-existent feature branch
	_, err = store.UpdatePRBranch(ctx, gitstore.UpdatePRBranchOptions{
		FullName:     repoName,
		BaseBranch:   "main",
		HeadBranch:   "nonexistent-branch",
		Committer:    "Test Bot",
		Email:        "bot@example.com",
		UpdateMethod: "merge",
	})
	if err == nil {
		t.Fatal("expected UpdatePRBranch to fail with missing branch, got nil")
	}
	// Error should mention checkout failure or similar
	if !strings.Contains(err.Error(), "checkout") && !strings.Contains(err.Error(), "failed") {
		t.Fatalf("expected checkout/branch-related error, got: %v", err)
	}
}

// TestStore_UpdatePRBranch_MissingBaseRef tests that UpdatePRBranch returns an error
// when the base branch doesn't exist.
func TestStore_UpdatePRBranch_MissingBaseRef(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-update-missing-base-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-update-missing-base"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create initial commit on main
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "initial commit", []byte("initial\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Create feature branch
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	// Try to update feature branch from non-existent base
	_, err = store.UpdatePRBranch(ctx, gitstore.UpdatePRBranchOptions{
		FullName:     repoName,
		BaseBranch:   "nonexistent-base",
		HeadBranch:   "feature",
		Committer:    "Test Bot",
		Email:        "bot@example.com",
		UpdateMethod: "merge",
	})
	if err == nil {
		t.Fatal("expected UpdatePRBranch to fail with missing base branch, got nil")
	}
}

// TestStore_UpdatePRBranch_NonExistentRepo tests that UpdatePRBranch returns an error
// when the repository doesn't exist.
func TestStore_UpdatePRBranch_NonExistentRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-update-norepo-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Try to update on non-existent repo
	_, err = store.UpdatePRBranch(ctx, gitstore.UpdatePRBranchOptions{
		FullName:     "nonexistent/repo",
		BaseBranch:   "main",
		HeadBranch:   "feature",
		Committer:    "Test Bot",
		Email:        "bot@example.com",
		UpdateMethod: "merge",
	})
	if err == nil {
		t.Fatal("expected UpdatePRBranch to fail with non-existent repo, got nil")
	}
	if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "repo") {
		t.Fatalf("expected 'not found' error, got: %v", err)
	}
}

// ============================================================================
// Merge Utility Tests
// ============================================================================

// TestStore_CanMerge_Mergeable tests that CanMerge reports a clean merge.
func TestStore_CanMerge_Mergeable(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-canmerge-ok-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-canmerge-ok"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	if _, err := store.WriteFile(ctx, repoName, "feature", "feature.txt", "add feature", []byte("feature\n")); err != nil {
		t.Fatalf("WriteFile feature failed: %v", err)
	}

	status := store.CanMerge(ctx, repoName, "main", "feature")
	if status != "MERGEABLE" {
		t.Fatalf("expected MERGEABLE, got %s", status)
	}
}

// TestStore_CanMerge_Conflicting tests that CanMerge reports conflicts.
func TestStore_CanMerge_Conflicting(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-canmerge-conflict-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-canmerge-conflict"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if _, err := store.WriteFile(ctx, repoName, "main", "conflict.txt", "initial", []byte("line1\nshared\nline3\n")); err != nil {
		t.Fatalf("WriteFile initial failed: %v", err)
	}

	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	if _, err := store.WriteFile(ctx, repoName, "main", "conflict.txt", "main change", []byte("line1\nmain-change\nline3\n")); err != nil {
		t.Fatalf("WriteFile main failed: %v", err)
	}

	if _, err := store.WriteFile(ctx, repoName, "feature", "conflict.txt", "feature change", []byte("line1\nfeature-change\nline3\n")); err != nil {
		t.Fatalf("WriteFile feature failed: %v", err)
	}

	status := store.CanMerge(ctx, repoName, "main", "feature")
	if status != "CONFLICTING" {
		t.Fatalf("expected CONFLICTING, got %s", status)
	}
}

// TestStore_CanMerge_InvalidRepoName tests that CanMerge reports unknown on invalid input.
func TestStore_CanMerge_InvalidRepoName(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-canmerge-invalid-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	status := store.CanMerge(ctx, "invalid", "main", "feature")
	if status != "UNKNOWN" {
		t.Fatalf("expected UNKNOWN, got %s", status)
	}
}

// TestStore_IsEmpty_EmptyRepo tests IsEmpty on an empty repo.
func TestStore_IsEmpty_EmptyRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-isempty-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-empty"

	if err := store.Init(ctx, repoName, "main", false); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if !store.IsEmpty(ctx, repoName) {
		t.Fatal("expected repo to be empty")
	}
}

// TestStore_IsEmpty_NonEmptyRepo tests IsEmpty after a commit is created.
func TestStore_IsEmpty_NonEmptyRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-isempty-nonempty-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-nonempty"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "add file", []byte("content\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	if store.IsEmpty(ctx, repoName) {
		t.Fatal("expected repo to be non-empty")
	}
}

// TestStore_IsEmpty_NonExistentRepo tests IsEmpty on a missing repo.
func TestStore_IsEmpty_NonExistentRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-isempty-missing-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if !store.IsEmpty(ctx, "user/repo-missing") {
		t.Fatal("expected missing repo to be reported as empty")
	}
}

// TestStore_DiskUsageKB_MissingRepo tests DiskUsageKB on a missing repo.
func TestStore_DiskUsageKB_MissingRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-disk-missing-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if got := store.DiskUsageKB(ctx, "user/repo-missing"); got != 0 {
		t.Fatalf("expected 0 KB for missing repo, got %d", got)
	}
}

// TestStore_DiskUsageKB_IncreasesAfterWrite tests DiskUsageKB growth after adding data.
func TestStore_DiskUsageKB_IncreasesAfterWrite(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-disk-usage-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-disk-usage"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	beforeKB := store.DiskUsageKB(ctx, repoName)
	if beforeKB <= 0 {
		t.Fatalf("expected positive disk usage, got %d", beforeKB)
	}

	data := make([]byte, 1<<20)
	rng := rand.New(rand.NewSource(1))
	if _, err := rng.Read(data); err != nil {
		t.Fatalf("rand.Read failed: %v", err)
	}

	if _, err := store.WriteFile(ctx, repoName, "main", "big.bin", "add big file", data); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	afterKB := store.DiskUsageKB(ctx, repoName)
	if afterKB <= beforeKB {
		t.Fatalf("expected disk usage to increase, before %d KB after %d KB", beforeKB, afterKB)
	}
}

// TestStore_IsEmpty_CorruptedHEAD tests IsEmpty when HEAD file exists but is corrupted.
// This tests the error path where git cat-file fails even though repo directory exists.
func TestStore_IsEmpty_CorruptedHEAD(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-isempty-corrupt-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-corrupt-head"

	// Initialize repo with a commit
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Get the repo directory path
	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}

	// Corrupt the HEAD file by writing invalid content
	headPath := repoDir + "/HEAD"
	if err := os.WriteFile(headPath, []byte("invalid-head-content\n"), 0644); err != nil {
		t.Fatalf("Failed to corrupt HEAD file: %v", err)
	}

	// IsEmpty should return true when git cat-file fails due to corrupted HEAD
	if !store.IsEmpty(ctx, repoName) {
		t.Fatal("expected IsEmpty to return true for repo with corrupted HEAD")
	}
}

// TestStore_IsEmpty_InvalidRepoName tests IsEmpty when repoPath returns an error
// due to invalid full name format (triggers the err != nil branch).
func TestStore_IsEmpty_InvalidRepoName(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-isempty-invalid-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	// Use an invalid repo name that will fail validateFullName (e.g., contains path traversal)
	invalidRepoName := "../escape/repo"

	// IsEmpty should return true when repoPath returns an error
	if !store.IsEmpty(ctx, invalidRepoName) {
		t.Fatal("expected IsEmpty to return true for invalid repo name")
	}
}

// TestStore_DiskUsageKB_PermissionDenied tests DiskUsageKB when du command fails
// due to permission denied on the repository directory.
func TestStore_DiskUsageKB_PermissionDenied(t *testing.T) {
	// Skip if running as root (root can access anything)
	if os.Geteuid() == 0 {
		t.Skip("skipping test when running as root")
	}

	tmpDir, err := os.MkdirTemp("", "gitstore-disk-perm-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-disk-perm"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Get the repo directory path
	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}

	// Remove read permissions from the repo directory to make du fail
	if err := os.Chmod(repoDir, 0000); err != nil {
		t.Fatalf("Failed to change permissions: %v", err)
	}
	// Restore permissions for cleanup
	defer os.Chmod(repoDir, 0755)

	// DiskUsageKB should return 0 when du command fails
	if got := store.DiskUsageKB(ctx, repoName); got != 0 {
		t.Fatalf("expected 0 KB when du fails due to permissions, got %d", got)
	}
}

// TestStore_DiskUsageKB_InvalidRepoName tests DiskUsageKB when repoPath returns an error
// due to invalid full name format (triggers the err != nil branch).
func TestStore_DiskUsageKB_InvalidRepoName(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-disk-invalid-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	// Use an invalid repo name that will fail validateFullName
	invalidRepoName := "../escape/repo"

	// DiskUsageKB should return 0 when repoPath returns an error
	if got := store.DiskUsageKB(ctx, invalidRepoName); got != 0 {
		t.Fatalf("expected 0 KB for invalid repo name, got %d", got)
	}
}
