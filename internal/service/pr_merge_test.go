package service_test

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

// TestMergePR_UnauthBeforeDBLookup verifies that MergePR checks auth
// before any DB lookup. An unauthenticated caller must get an auth error
// without learning whether the PR exists or is already merged.
func TestMergePR_UnauthBeforeDBLookup(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background() // no user in context

	// Create a non-admin user, repo, and PR directly in DB.
	user := db.User{Login: "owner", Type: db.TypeUser, SiteAdmin: false}
	svc.DB.Create(&user)
	repo := db.Repository{Name: "repo", FullName: "owner/repo", OwnerID: user.ID}
	svc.DB.Create(&repo)
	pr := db.PullRequest{
		Number:       1,
		RepositoryID: repo.ID,
		Title:        "test pr",
		State:        "open",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorID:     user.ID,
	}
	svc.DB.Create(&pr)

	// No admin user in DB + no user in context → GetCurrentUser must fail.
	_, err := svc.MergePR(ctx, "owner/repo", 1, "merge", "")
	if err == nil {
		t.Fatal("expected auth error, got nil")
	}
	if !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("expected 'authentication required' error, got: %v", err)
	}
}

// TestMergePR_UnauthDoesNotLeakMergedState verifies that an unauthenticated
// caller cannot distinguish a merged PR from a non-existent one.
func TestMergePR_UnauthDoesNotLeakMergedState(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	user := db.User{Login: "owner", Type: db.TypeUser, SiteAdmin: false}
	svc.DB.Create(&user)
	repo := db.Repository{Name: "repo", FullName: "owner/repo", OwnerID: user.ID}
	svc.DB.Create(&repo)
	merged := true
	pr := db.PullRequest{
		Number:       1,
		RepositoryID: repo.ID,
		Title:        "merged pr",
		State:        "closed",
		Merged:       merged,
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorID:     user.ID,
	}
	svc.DB.Create(&pr)

	_, err := svc.MergePR(ctx, "owner/repo", 1, "merge", "")
	if err == nil {
		t.Fatal("expected auth error, got nil")
	}
	if strings.Contains(err.Error(), "already merged") {
		t.Fatal("auth error must not leak merge state to unauthenticated callers")
	}
	if !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("expected 'authentication required' error, got: %v", err)
	}
}

// TestMergePRByID_UnauthBeforeDBLookup verifies that MergePRByID checks auth
// before any DB lookup, so unauthenticated callers cannot learn whether a PR ID
// exists.
func TestMergePRByID_UnauthBeforeDBLookup(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	user := db.User{Login: "owner", Type: db.TypeUser, SiteAdmin: false}
	svc.DB.Create(&user)
	repo := db.Repository{Name: "repo", FullName: "owner/repo", OwnerID: user.ID}
	svc.DB.Create(&repo)
	pr := db.PullRequest{
		Number:       1,
		RepositoryID: repo.ID,
		Title:        "test pr",
		State:        "open",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorID:     user.ID,
	}
	svc.DB.Create(&pr)

	err := svc.MergePRByID(ctx, pr.ID, "merge", "")
	if err == nil {
		t.Fatal("expected auth error, got nil")
	}
	if strings.Contains(err.Error(), "pull request not found") {
		t.Fatal("auth error must not leak PR existence to unauthenticated callers")
	}
	if !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("expected 'authentication required' error, got: %v", err)
	}
}

// TestMergePRByID_NonexistentID_UnauthGetsAuthError verifies that calling
// MergePRByID with a non-existent ID still returns auth error first.
func TestMergePRByID_NonexistentID_UnauthGetsAuthError(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	err := svc.MergePRByID(ctx, 99999, "merge", "")
	if err == nil {
		t.Fatal("expected auth error, got nil")
	}
	if strings.Contains(err.Error(), "pull request not found") {
		t.Fatal("auth error must not leak PR existence to unauthenticated callers")
	}
	if !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("expected 'authentication required' error, got: %v", err)
	}
}

// commitParentCount returns the number of parents of a git commit.
func commitParentCount(t *testing.T, repoDir, sha string) int {
	t.Helper()
	out, err := exec.Command("git", "-C", repoDir, "cat-file", "-p", sha).Output()
	if err != nil {
		t.Fatalf("cat-file %s: %v", sha, err)
	}
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "parent ") {
			count++
		}
	}
	return count
}

// TestMergePR_RebaseWithCommitTitle verifies that rebase strategy is honoured
// even when commit_title is provided. The result must have 1 parent (no merge commit).
func TestMergePR_RebaseWithCommitTitle(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	pr, authCtx, _ := setupPRWithRealBranches(t, svc, "rebase-user", "rebase-repo")
	fullName := "rebase-user/rebase-repo"

	merged, err := svc.MergePR(authCtx, fullName, pr.Number, "rebase", "Custom rebase title")
	if err != nil {
		t.Fatalf("MergePR rebase: %v", err)
	}
	if !merged.Merged {
		t.Fatal("expected PR to be merged")
	}

	repoDir, err := svc.Git.GetRepoPath(authCtx, fullName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	parents := commitParentCount(t, repoDir, merged.MergeCommitSHA)
	if parents != 1 {
		t.Fatalf("rebase commit should have 1 parent, got %d", parents)
	}
}

// TestMergePR_SquashWithCommitTitle verifies that squash strategy is honoured
// even when commit_title is provided. The result must have 1 parent and the
// custom commit message.
func TestMergePR_SquashWithCommitTitle(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	pr, authCtx, _ := setupPRWithRealBranches(t, svc, "squash-user", "squash-repo")
	fullName := "squash-user/squash-repo"
	customMsg := "feat: squashed changes"

	merged, err := svc.MergePR(authCtx, fullName, pr.Number, "squash", customMsg)
	if err != nil {
		t.Fatalf("MergePR squash: %v", err)
	}
	if !merged.Merged {
		t.Fatal("expected PR to be merged")
	}

	repoDir, err := svc.Git.GetRepoPath(authCtx, fullName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	parents := commitParentCount(t, repoDir, merged.MergeCommitSHA)
	if parents != 1 {
		t.Fatalf("squash commit should have 1 parent, got %d", parents)
	}

	// Verify the custom commit message was used.
	out, err := exec.Command("git", "-C", repoDir, "log", "-1", "--format=%s", merged.MergeCommitSHA).Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	subject := strings.TrimSpace(string(out))
	if subject != customMsg {
		t.Fatalf("expected commit message %q, got %q", customMsg, subject)
	}
}

// TestMergePR_MergeWithCommitTitle verifies that merge strategy with a custom
// title produces a proper 2-parent merge commit (regression test for the
// removed fast path).
func TestMergePR_MergeWithCommitTitle(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	pr, authCtx, _ := setupPRWithRealBranches(t, svc, "merge-user", "merge-repo")
	fullName := "merge-user/merge-repo"
	customMsg := "Merge PR with custom title"

	merged, err := svc.MergePR(authCtx, fullName, pr.Number, "merge", customMsg)
	if err != nil {
		t.Fatalf("MergePR merge: %v", err)
	}
	if !merged.Merged {
		t.Fatal("expected PR to be merged")
	}

	repoDir, err := svc.Git.GetRepoPath(authCtx, fullName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	parents := commitParentCount(t, repoDir, merged.MergeCommitSHA)
	if parents != 2 {
		t.Fatalf("merge commit should have 2 parents, got %d", parents)
	}

	// Verify the custom commit message was used.
	out, err := exec.Command("git", "-C", repoDir, "log", "-1", "--format=%s", merged.MergeCommitSHA).Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	subject := strings.TrimSpace(string(out))
	if subject != customMsg {
		t.Fatalf("expected commit message %q, got %q", customMsg, subject)
	}
}

// TestMergePR_SquashDefaultMethod verifies that passing "squash" as strategy
// without commit title still uses the squash merge path (1 parent), not the
// old behavior that mapped squash → merge.
func TestMergePR_SquashDefaultMethod(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	pr, authCtx, _ := setupPRWithRealBranches(t, svc, "sq-default-user", "sq-default-repo")
	fullName := "sq-default-user/sq-default-repo"

	merged, err := svc.MergePR(authCtx, fullName, pr.Number, "squash", "")
	if err != nil {
		t.Fatalf("MergePR squash: %v", err)
	}

	repoDir, err := svc.Git.GetRepoPath(authCtx, fullName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	parents := commitParentCount(t, repoDir, merged.MergeCommitSHA)
	if parents != 1 {
		t.Fatalf("squash commit should have 1 parent, got %d", parents)
	}

	// Default message should be the standard merge PR message.
	out, err := exec.Command("git", "-C", repoDir, "log", "-1", "--format=%s", merged.MergeCommitSHA).Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	subject := strings.TrimSpace(string(out))
	expected := fmt.Sprintf("Merge pull request #%d", pr.Number)
	if subject != expected {
		t.Fatalf("expected commit message %q, got %q", expected, subject)
	}
}

// ─── MergePRRecord tests ─────────────────────────────────────────────────────

// TestMergePRRecord_NoGitStore marks PR as merged without git operations.
func TestMergePRRecord_NoGitStore(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	user := db.User{Login: "nogs-user", Type: db.TypeUser, SiteAdmin: true}
	svc.DB.Create(&user)
	repo := db.Repository{Name: "nogs-repo", FullName: "nogs-user/nogs-repo", OwnerID: user.ID}
	svc.DB.Create(&repo)
	pr := db.PullRequest{
		Number:           1,
		RepositoryID:     repo.ID,
		HeadRepositoryID: repo.ID,
		Title:            "no git pr",
		State:            db.StateOpen,
		HeadRef:          "feature",
		BaseRef:          "main",
		AuthorID:         user.ID,
	}
	svc.DB.Create(&pr)

	authCtx := service.ContextWithUser(ctx, user)
	svc.Git = nil // no git store

	err := svc.MergePRRecord(authCtx, &pr, "merge", "")
	if err != nil {
		t.Fatalf("MergePRRecord with no git store: %v", err)
	}
	if !pr.Merged {
		t.Fatal("expected PR to be marked merged")
	}
	if pr.State != db.StateClosed {
		t.Fatalf("expected state closed, got %s", pr.State)
	}
	if pr.MergedByLogin != user.Login {
		t.Fatalf("expected merged_by %s, got %s", user.Login, pr.MergedByLogin)
	}
}

// TestMergePRRecord_RebaseStrategy verifies rebase merge method.
func TestMergePRRecord_RebaseStrategy(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	pr, authCtx, _ := setupPRWithRealBranches(t, svc, "rebase-rec-user", "rebase-rec-repo")

	err := svc.MergePRRecord(authCtx, &pr, "rebase", "Rebase commit")
	if err != nil {
		t.Fatalf("MergePRRecord rebase: %v", err)
	}
	if !pr.Merged {
		t.Fatal("expected PR to be marked merged")
	}

	repoDir, err := svc.Git.GetRepoPath(authCtx, pr.Repository.FullName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	parents := commitParentCount(t, repoDir, pr.MergeCommitSHA)
	if parents != 1 {
		t.Fatalf("rebase commit should have 1 parent, got %d", parents)
	}
}

// TestMergePRRecord_SquashStrategy verifies squash merge method.
func TestMergePRRecord_SquashStrategy(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	pr, authCtx, _ := setupPRWithRealBranches(t, svc, "squash-rec-user", "squash-rec-repo")
	customMsg := "Squashed: all changes"

	err := svc.MergePRRecord(authCtx, &pr, "squash", customMsg)
	if err != nil {
		t.Fatalf("MergePRRecord squash: %v", err)
	}
	if !pr.Merged {
		t.Fatal("expected PR to be marked merged")
	}

	repoDir, err := svc.Git.GetRepoPath(authCtx, pr.Repository.FullName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	parents := commitParentCount(t, repoDir, pr.MergeCommitSHA)
	if parents != 1 {
		t.Fatalf("squash commit should have 1 parent, got %d", parents)
	}
}

// TestMergePRRecord_MergeStrategy verifies merge (default) method.
func TestMergePRRecord_MergeStrategy(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	pr, authCtx, _ := setupPRWithRealBranches(t, svc, "merge-rec-user", "merge-rec-repo")

	err := svc.MergePRRecord(authCtx, &pr, "merge", "Merge commit")
	if err != nil {
		t.Fatalf("MergePRRecord merge: %v", err)
	}
	if !pr.Merged {
		t.Fatal("expected PR to be marked merged")
	}

	repoDir, err := svc.Git.GetRepoPath(authCtx, pr.Repository.FullName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	parents := commitParentCount(t, repoDir, pr.MergeCommitSHA)
	if parents != 2 {
		t.Fatalf("merge commit should have 2 parents, got %d", parents)
	}
}
