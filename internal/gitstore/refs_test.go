package gitstore_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gh-server/internal/gitstore"
)

func TestListBranches_HappyPath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-listbranch-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/list-branches"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "init", []byte("hello\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	branches, err := store.ListBranches(ctx, repoName)
	if err != nil {
		t.Fatalf("ListBranches failed: %v", err)
	}
	if len(branches) != 2 {
		t.Fatalf("expected 2 branches, got %d", len(branches))
	}

	names := map[string]bool{}
	for _, b := range branches {
		names[b.Name] = true
		if b.SHA == "" {
			t.Errorf("branch %q has empty SHA", b.Name)
		}
	}
	if !names["main"] || !names["feature"] {
		t.Errorf("expected branches main and feature, got %v", names)
	}
}

func TestListBranches_IteratorError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based test not reliable on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("cannot restrict file permissions as root")
	}

	tmpDir, err := os.MkdirTemp("", "gitstore-listbranch-iterr-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/iter-error"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "init", []byte("hi\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Corrupt a loose ref so the branch iterator fails during iteration.
	// Place an unreadable file in refs/heads/ — go-git tries to read it and
	// returns a permission-denied error from ForEach/Next.
	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	badRef := filepath.Join(repoDir, "refs", "heads", "corrupt")
	if err := os.WriteFile(badRef, []byte("dummy\n"), 0o644); err != nil {
		t.Fatalf("failed to create bad ref: %v", err)
	}
	if err := os.Chmod(badRef, 0o000); err != nil {
		t.Fatalf("failed to chmod bad ref: %v", err)
	}
	// Ensure cleanup can remove the file.
	t.Cleanup(func() { os.Chmod(badRef, 0o644) })

	_, err = store.ListBranches(ctx, repoName)
	if err == nil {
		t.Fatal("expected error from ListBranches when iterator encounters unreadable ref, got nil")
	}
	// The error surfaces during reference enumeration. The key assertion is
	// that ListBranches returns an error rather than silently yielding a
	// partial branch list (which was the pre-fix behavior for ForEach errors).
	if got := err.Error(); !contains(got, "permission denied") {
		t.Fatalf("expected permission-denied error, got: %v", err)
	}
}

func TestListBranches_NonexistentRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-listbranch-noexist-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.ListBranches(context.Background(), "no/such-repo")
	if err == nil {
		t.Fatal("expected error for nonexistent repo, got nil")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestCreatePRRef_NewRef tests creating a new refs/pull/ID/head reference
func TestCreatePRRef_NewRef(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-prref-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	baseRepo := "user/base-repo"
	headRepo := "user/head-repo"

	// Initialize base repo
	if err := store.Init(ctx, baseRepo, "main", true); err != nil {
		t.Fatalf("Init base failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, baseRepo, "main", "file.txt", "init base", []byte("base\n")); err != nil {
		t.Fatalf("WriteFile base failed: %v", err)
	}

	// Initialize head repo
	if err := store.Init(ctx, headRepo, "main", true); err != nil {
		t.Fatalf("Init head failed: %v", err)
	}
	headSHA, err := store.WriteFile(ctx, headRepo, "main", "pr-file.txt", "add pr file", []byte("pr content\n"))
	if err != nil {
		t.Fatalf("WriteFile head failed: %v", err)
	}

	// Create PR ref - this fetches from headRepo into baseRepo
	err = store.CreatePRRef(ctx, baseRepo, headRepo, headSHA, 42)
	if err != nil {
		t.Fatalf("CreatePRRef failed: %v", err)
	}

	// Verify the ref exists by checking it can be resolved
	baseDir, err := store.GetRepoPath(ctx, baseRepo)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	cmd := exec.Command("git", "-C", baseDir, "rev-parse", "refs/pull/42/head")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("refs/pull/42/head does not exist: %v, output: %s", err, out)
	}
}

// TestCreatePRRef_SameRepo tests CreatePRRef when baseRepo == headRepo (no fetch needed)
func TestCreatePRRef_SameRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-prref-same-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo := "user/same-repo"

	// Initialize repo
	if err := store.Init(ctx, repo, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	headSHA, err := store.WriteFile(ctx, repo, "main", "file.txt", "init", []byte("content\n"))
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Create PR ref with same repo for base and head
	err = store.CreatePRRef(ctx, repo, repo, headSHA, 123)
	if err != nil {
		t.Fatalf("CreatePRRef failed: %v", err)
	}

	// Verify the ref exists
	repoDir, err := store.GetRepoPath(ctx, repo)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "refs/pull/123/head")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("refs/pull/123/head does not exist: %v, output: %s", err, out)
	}
}

// TestCreatePRRef_ExistingRef tests creating a PR ref that already exists (should update)
func TestCreatePRRef_ExistingRef(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-prref-existing-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	baseRepo := "user/base-existing"
	headRepo := "user/head-existing"

	// Initialize base repo
	if err := store.Init(ctx, baseRepo, "main", true); err != nil {
		t.Fatalf("Init base failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, baseRepo, "main", "file.txt", "init base", []byte("base\n")); err != nil {
		t.Fatalf("WriteFile base failed: %v", err)
	}

	// Initialize head repo with first commit
	if err := store.Init(ctx, headRepo, "main", true); err != nil {
		t.Fatalf("Init head failed: %v", err)
	}
	firstSHA, err := store.WriteFile(ctx, headRepo, "main", "file.txt", "first commit", []byte("first\n"))
	if err != nil {
		t.Fatalf("WriteFile head first failed: %v", err)
	}

	// Create initial PR ref
	err = store.CreatePRRef(ctx, baseRepo, headRepo, firstSHA, 99)
	if err != nil {
		t.Fatalf("CreatePRRef first failed: %v", err)
	}

	// Add another commit to head repo
	secondSHA, err := store.WriteFile(ctx, headRepo, "main", "file.txt", "second commit", []byte("second\n"))
	if err != nil {
		t.Fatalf("WriteFile head second failed: %v", err)
	}

	// Update the PR ref with new SHA
	err = store.CreatePRRef(ctx, baseRepo, headRepo, secondSHA, 99)
	if err != nil {
		t.Fatalf("CreatePRRef update failed: %v", err)
	}

	// Verify the ref now points to the new SHA
	baseDir, err := store.GetRepoPath(ctx, baseRepo)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	cmd := exec.Command("git", "-C", baseDir, "rev-parse", "refs/pull/99/head")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("refs/pull/99/head does not exist: %v, output: %s", err, out)
	}
	resolvedSHA := strings.TrimSpace(string(out))
	if resolvedSHA != secondSHA {
		t.Errorf("expected refs/pull/99/head to point to %s, got %s", secondSHA, resolvedSHA)
	}
}

// TestCreatePRRef_InvalidSHA tests CreatePRRef with an invalid SHA
func TestCreatePRRef_InvalidSHA(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-prref-invalid-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	baseRepo := "user/base-invalid"
	headRepo := "user/head-invalid"

	// Initialize base repo
	if err := store.Init(ctx, baseRepo, "main", true); err != nil {
		t.Fatalf("Init base failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, baseRepo, "main", "file.txt", "init", []byte("base\n")); err != nil {
		t.Fatalf("WriteFile base failed: %v", err)
	}

	// Initialize head repo
	if err := store.Init(ctx, headRepo, "main", true); err != nil {
		t.Fatalf("Init head failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, headRepo, "main", "file.txt", "init", []byte("head\n")); err != nil {
		t.Fatalf("WriteFile head failed: %v", err)
	}

	// Try to create PR ref with invalid SHA
	invalidSHA := "invalid_sha_that_does_not_exist_12345678"
	err = store.CreatePRRef(ctx, baseRepo, headRepo, invalidSHA, 1)
	if err == nil {
		t.Fatal("expected error for invalid SHA, got nil")
	}

	// Verify error message is informative
	if !strings.Contains(err.Error(), "git fetch") && !strings.Contains(err.Error(), "update-ref") {
		t.Errorf("expected git-related error, got: %v", err)
	}
}

// TestCreatePRRef_NonExistentHeadRepo tests CreatePRRef with non-existent head repo
func TestCreatePRRef_NonExistentHeadRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-prref-nohead-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	baseRepo := "user/base-nohead"

	// Initialize base repo only
	if err := store.Init(ctx, baseRepo, "main", true); err != nil {
		t.Fatalf("Init base failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, baseRepo, "main", "file.txt", "init", []byte("base\n")); err != nil {
		t.Fatalf("WriteFile base failed: %v", err)
	}

	// Try to create PR ref with non-existent head repo
	err = store.CreatePRRef(ctx, baseRepo, "nonexistent/head-repo", "abc123abc123abc123abc123abc123abc123", 1)
	if err == nil {
		t.Fatal("expected error for non-existent head repo, got nil")
	}
}

// TestCreatePRRef_NonExistentBaseRepo tests CreatePRRef with non-existent base repo
func TestCreatePRRef_NonExistentBaseRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-prref-nobase-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	headRepo := "user/head-nobase"

	// Initialize head repo only
	if err := store.Init(ctx, headRepo, "main", true); err != nil {
		t.Fatalf("Init head failed: %v", err)
	}
	headSHA, err := store.WriteFile(ctx, headRepo, "main", "file.txt", "init", []byte("head\n"))
	if err != nil {
		t.Fatalf("WriteFile head failed: %v", err)
	}

	// Try to create PR ref with non-existent base repo
	err = store.CreatePRRef(ctx, "nonexistent/base-repo", headRepo, headSHA, 1)
	if err == nil {
		t.Fatal("expected error for non-existent base repo, got nil")
	}
}

func TestCreateBranchFromOid_Success(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-branch-oid-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/branch-oid"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	commitSHA, err := store.WriteFile(ctx, repoName, "main", "file.txt", "add file", []byte("hello\n"))
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	if err := store.CreateBranchFromOid(ctx, repoName, "feature", commitSHA); err != nil {
		t.Fatalf("CreateBranchFromOid failed: %v", err)
	}

	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	resolved := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "refs/heads/feature"))
	if resolved != commitSHA {
		t.Fatalf("expected refs/heads/feature to point to %s, got %s", commitSHA, resolved)
	}
}

func TestCreateBranchFromOid_InvalidAndMissingOID(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-branch-oid-invalid-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/branch-oid-invalid"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	commitSHA, err := store.WriteFile(ctx, repoName, "main", "file.txt", "add file", []byte("hello\n"))
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}

	t.Run("invalid-format", func(t *testing.T) {
		err := store.CreateBranchFromOid(ctx, repoName, "invalid-branch", "not-a-valid-oid")
		if err == nil {
			t.Fatal("expected error for invalid OID, got nil")
		}
		if !strings.Contains(err.Error(), "invalid oid") {
			t.Fatalf("expected invalid oid error, got: %v", err)
		}
		assertRefMissing(t, repoDir, "refs/heads/invalid-branch")
	})

	t.Run("missing-commit", func(t *testing.T) {
		missingOID := commitSHA
		last := missingOID[len(missingOID)-1]
		if last == '0' {
			missingOID = missingOID[:len(missingOID)-1] + "1"
		} else {
			missingOID = missingOID[:len(missingOID)-1] + "0"
		}
		err := store.CreateBranchFromOid(ctx, repoName, "missing-branch", missingOID)
		if err == nil {
			t.Fatal("expected error for missing commit OID, got nil")
		}
		if !strings.Contains(err.Error(), "resolve commit") {
			t.Fatalf("expected resolve commit error, got: %v", err)
		}
		assertRefMissing(t, repoDir, "refs/heads/missing-branch")
	})
}

func TestUpdateRef_Success(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-updateref-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/update-ref"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "first", []byte("first\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}
	updatedSHA, err := store.WriteFile(ctx, repoName, "main", "file.txt", "second", []byte("second\n"))
	if err != nil {
		t.Fatalf("WriteFile second failed: %v", err)
	}

	if err := store.UpdateRef(ctx, repoName, "refs/heads/feature", updatedSHA); err != nil {
		t.Fatalf("UpdateRef failed: %v", err)
	}

	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	resolved := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "refs/heads/feature"))
	if resolved != updatedSHA {
		t.Fatalf("expected refs/heads/feature to point to %s, got %s", updatedSHA, resolved)
	}
}

func TestUpdateRef_InvalidSHA(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-updateref-invalid-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/update-ref-invalid"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "init", []byte("init\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	err = store.UpdateRef(ctx, repoName, "refs/heads/main", "not-a-valid-sha")
	if err == nil {
		t.Fatal("expected error for invalid SHA, got nil")
	}
	if !errors.Is(err, gitstore.ErrInvalidSHA) {
		t.Fatalf("expected ErrInvalidSHA, got: %v", err)
	}
}

func TestDeleteRef_Success(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-deleteref-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/delete-ref"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "init", []byte("init\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	if err := store.DeleteRef(ctx, repoName, "refs/heads/feature"); err != nil {
		t.Fatalf("DeleteRef failed: %v", err)
	}

	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	assertRefMissing(t, repoDir, "refs/heads/feature")
}

func TestDeleteRef_MissingRef(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-deleteref-missing-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/delete-ref-missing"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "init", []byte("init\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	err = store.DeleteRef(ctx, repoName, "refs/heads/missing")
	if err == nil {
		t.Fatal("expected error for missing ref, got nil")
	}
	if !strings.Contains(err.Error(), "git show-ref --verify") {
		t.Fatalf("expected git show-ref error, got: %v", err)
	}
}

func assertRefMissing(t *testing.T, repoDir, ref string) {
	t.Helper()
	cmd := exec.Command("git", "-C", repoDir, "show-ref", "--verify", ref)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("expected %s to be missing, got output: %s", ref, out)
	}
}
