package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

// setupPRWithRealBranches creates a user, repo with a README, a feature branch
// with one file commit, and a PR. Returns the service, PR, authenticated context, and user.
// The caller's setupTestService cleanup handles teardown.
func setupPRWithRealBranches(t testing.TB, svc *service.Service, login, repoName string) (db.PullRequest, context.Context, db.User) {
	t.Helper()
	ctx := context.Background()

	user := db.User{Login: login, Name: login, Type: db.TypeUser, SiteAdmin: true}
	if err := svc.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    login,
		Name:          repoName,
		DefaultBranch: "main",
		AutoInit:      true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	fullName := login + "/" + repoName
	if err := svc.Git.CreateBranch(ctx, fullName, "feature", "main"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	if _, err := svc.Git.WriteFile(ctx, fullName, "feature", "hello.txt", "add test file", []byte("hello world\n")); err != nil {
		t.Fatalf("write file: %v", err)
	}

	authCtx := service.ContextWithUser(ctx, user)
	pr, err := svc.CreatePR(authCtx, service.CreatePRInput{
		RepoFullName: fullName,
		Title:        "Test PR",
		Body:         "test body",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  login,
	})
	if err != nil {
		t.Fatalf("create PR: %v", err)
	}
	return pr, authCtx, user
}

// ─── CreatePR negative paths ─────────────────────────────────────────────────────

func TestCreatePRNegativePaths(t *testing.T) {
	t.Run("same_head_and_base_branch", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()
		ctx := context.Background()

		setupRepoForTest(t, svc, "samebr-user", "samebr-repo")

		_, err := svc.CreatePR(ctx, service.CreatePRInput{
			RepoFullName: "samebr-user/samebr-repo",
			Title:        "Same branch PR",
			HeadRef:      "main",
			BaseRef:      "main",
			AuthorLogin:  "samebr-user",
		})
		if err == nil {
			t.Fatal("expected error when head and base branches are the same")
		}
		if !strings.Contains(err.Error(), "head and base must be different") {
			t.Errorf("expected 'head and base must be different' error, got: %v", err)
		}
	})

	t.Run("same_branch_implicit_default", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()
		ctx := context.Background()

		// Create repo with "main" as default branch.
		setupRepoForTest(t, svc, "impl-user", "impl-repo")

		// Omit BaseRef so it defaults to the repo's default branch ("main"),
		// then set HeadRef to "main" as well.
		_, err := svc.CreatePR(ctx, service.CreatePRInput{
			RepoFullName: "impl-user/impl-repo",
			Title:        "Implicit same branch",
			HeadRef:      "main",
			BaseRef:      "", // defaults to "main"
			AuthorLogin:  "impl-user",
		})
		if err == nil {
			t.Fatal("expected error when head matches the implicit default base branch")
		}
		if !strings.Contains(err.Error(), "head and base must be different") {
			t.Errorf("expected 'head and base must be different' error, got: %v", err)
		}
	})

	t.Run("same_branch_bogus_head_repo_rejected", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()
		ctx := context.Background()

		setupRepoForTest(t, svc, "bogus-user", "bogus-repo")

		// Supply a HeadRepoFullName that does not resolve to any repo.
		// The guard must still reject same-branch because the fallback
		// headRepID equals the base repo ID.
		_, err := svc.CreatePR(ctx, service.CreatePRInput{
			RepoFullName:     "bogus-user/bogus-repo",
			HeadRepoFullName: "nonexistent-org/phantom-repo",
			Title:            "Bypass attempt via bogus head repo",
			HeadRef:          "main",
			BaseRef:          "main",
			AuthorLogin:      "bogus-user",
		})
		if err == nil {
			t.Fatal("expected error when head repo falls back to base repo with same branch")
		}
		if !strings.Contains(err.Error(), "head and base must be different") {
			t.Errorf("expected 'head and base must be different' error, got: %v", err)
		}
	})

	t.Run("cross_repo_same_ref_name_allowed", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()
		ctx := context.Background()

		// Create two separate repos (simulating fork and upstream).
		setupRepoForTest(t, svc, "upstream-user", "cross-repo")
		setupRepoForTest(t, svc, "fork-user", "cross-repo")
		upstreamRepo, err := svc.GetRepo(ctx, "upstream-user/cross-repo")
		if err != nil {
			t.Fatalf("get upstream repo: %v", err)
		}
		forkUser, err := svc.GetUser(ctx, "fork-user")
		if err != nil {
			t.Fatalf("get fork user: %v", err)
		}
		if err := svc.AddCollaborator(ctx, upstreamRepo.ID, forkUser.ID, "write"); err != nil {
			t.Fatalf("add fork user as write collaborator: %v", err)
		}

		// Cross-repo PR: fork/main -> upstream/main (same branch name, different repos).
		pr, err := svc.CreatePR(ctx, service.CreatePRInput{
			RepoFullName:     "upstream-user/cross-repo",
			HeadRepoFullName: "fork-user/cross-repo",
			Title:            "Cross-repo PR with same branch name",
			HeadRef:          "main",
			BaseRef:          "main",
			AuthorLogin:      "fork-user",
		})
		if err != nil {
			t.Fatalf("expected cross-repo same-ref PR to succeed, got: %v", err)
		}
		if pr.Number < 1 {
			t.Errorf("expected valid PR number, got %d", pr.Number)
		}
	})

	t.Run("nonexistent_repo", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()
		ctx := context.Background()

		_, err := svc.CreatePR(ctx, service.CreatePRInput{
			RepoFullName: "ghost/no-such-repo",
			Title:        "PR on missing repo",
			HeadRef:      "feature",
			BaseRef:      "main",
			AuthorLogin:  "ghost",
		})
		if err == nil {
			t.Fatal("expected error when repo does not exist")
		}
	})

	t.Run("merge_closed_pr_rejected", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()

		pr, ctx, _ := setupPRWithRealBranches(t, svc, "clmerge-user", "clmerge-repo")
		fullName := "clmerge-user/clmerge-repo"

		// Close the PR without merging.
		closed := "closed"
		if _, err := svc.UpdatePR(ctx, fullName, pr.Number, service.UpdatePRInput{State: &closed}); err != nil {
			t.Fatalf("UpdatePR(close): %v", err)
		}

		// Attempt to merge the closed PR — should be rejected.
		_, err := svc.MergePR(ctx, fullName, pr.Number, "merge", "")
		if err == nil {
			t.Fatal("expected error when merging a closed (non-merged) PR")
		}
		if !strings.Contains(err.Error(), "not open") {
			t.Errorf("expected 'not open' error, got: %v", err)
		}

		// Verify PR remains closed and not merged.
		got, err := svc.GetPR(ctx, fullName, pr.Number)
		if err != nil {
			t.Fatalf("GetPR: %v", err)
		}
		if got.Merged {
			t.Error("expected Merged == false after rejected merge")
		}
		if got.State != "closed" {
			t.Errorf("expected state closed, got %s", got.State)
		}
	})

	t.Run("merge_closed_pr_rejected_by_id", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()

		pr, ctx, _ := setupPRWithRealBranches(t, svc, "clid-user", "clid-repo")
		fullName := "clid-user/clid-repo"

		// Close the PR without merging.
		closed := "closed"
		if _, err := svc.UpdatePR(ctx, fullName, pr.Number, service.UpdatePRInput{State: &closed}); err != nil {
			t.Fatalf("UpdatePR(close): %v", err)
		}

		// Attempt to merge via MergePRByID (GraphQL path) — should be rejected.
		err := svc.MergePRByID(ctx, pr.ID, "merge", "")
		if err == nil {
			t.Fatal("expected error when merging a closed PR via MergePRByID")
		}
		if !strings.Contains(err.Error(), "not open") {
			t.Errorf("expected 'not open' error, got: %v", err)
		}

		// Verify PR remains closed and not merged.
		got, err := svc.GetPR(ctx, fullName, pr.Number)
		if err != nil {
			t.Fatalf("GetPR: %v", err)
		}
		if got.Merged {
			t.Error("expected Merged == false after rejected MergePRByID")
		}
		if got.State != "closed" {
			t.Errorf("expected state closed, got %s", got.State)
		}
	})
}

// ─── Merge lifecycle ────────────────────────────────────────────────────────────

func TestPRMergeLifecycle(t *testing.T) {
	t.Run("merge_strategy", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()

		pr, ctx, user := setupPRWithRealBranches(t, svc, "merge-user", "merge-repo")
		fullName := "merge-user/merge-repo"

		merged, err := svc.MergePR(ctx, fullName, pr.Number, "merge", "")
		if err != nil {
			t.Fatalf("MergePR: %v", err)
		}
		if !merged.Merged {
			t.Error("expected Merged == true")
		}
		if merged.State != "closed" {
			t.Errorf("expected state closed, got %s", merged.State)
		}
		if merged.ClosedAt == nil {
			t.Error("expected ClosedAt != nil")
		}
		if merged.MergedAt == nil {
			t.Error("expected MergedAt != nil")
		}
		if merged.MergeCommitSHA == "" {
			t.Error("expected MergeCommitSHA non-empty")
		}
		if merged.MergedByLogin != user.Login {
			t.Errorf("expected MergedByLogin %s, got %s", user.Login, merged.MergedByLogin)
		}

		// Verify git HEAD on main matches the merge SHA.
		gitSHA, err := svc.Git.HeadSHA(ctx, fullName, "main")
		if err != nil {
			t.Fatalf("HeadSHA: %v", err)
		}
		if gitSHA != merged.MergeCommitSHA {
			t.Errorf("git HEAD %s != MergeCommitSHA %s", gitSHA, merged.MergeCommitSHA)
		}
	})

	t.Run("squash_falls_through_to_merge", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()

		pr, ctx, _ := setupPRWithRealBranches(t, svc, "squash-user", "squash-repo")
		fullName := "squash-user/squash-repo"

		merged, err := svc.MergePR(ctx, fullName, pr.Number, "squash", "")
		if err != nil {
			t.Fatalf("MergePR(squash): %v", err)
		}
		if !merged.Merged {
			t.Error("expected Merged == true")
		}
		if merged.MergeCommitSHA == "" {
			t.Error("expected MergeCommitSHA non-empty")
		}

		gitSHA, err := svc.Git.HeadSHA(ctx, fullName, "main")
		if err != nil {
			t.Fatalf("HeadSHA: %v", err)
		}
		if gitSHA != merged.MergeCommitSHA {
			t.Errorf("git HEAD %s != MergeCommitSHA %s", gitSHA, merged.MergeCommitSHA)
		}
	})

	t.Run("rebase_strategy", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()

		pr, ctx, _ := setupPRWithRealBranches(t, svc, "rebase-user", "rebase-repo")
		fullName := "rebase-user/rebase-repo"

		merged, err := svc.MergePR(ctx, fullName, pr.Number, "rebase", "")
		if err != nil {
			t.Fatalf("MergePR(rebase): %v", err)
		}
		if !merged.Merged {
			t.Error("expected Merged == true")
		}
		if merged.MergeCommitSHA == "" {
			t.Error("expected MergeCommitSHA non-empty")
		}

		gitSHA, err := svc.Git.HeadSHA(ctx, fullName, "main")
		if err != nil {
			t.Fatalf("HeadSHA: %v", err)
		}
		if gitSHA != merged.MergeCommitSHA {
			t.Errorf("git HEAD %s != MergeCommitSHA %s", gitSHA, merged.MergeCommitSHA)
		}
	})

	t.Run("already_merged_error", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()

		pr, ctx, _ := setupPRWithRealBranches(t, svc, "almerge-user", "almerge-repo")
		fullName := "almerge-user/almerge-repo"

		if _, err := svc.MergePR(ctx, fullName, pr.Number, "merge", ""); err != nil {
			t.Fatalf("first MergePR: %v", err)
		}

		_, err := svc.MergePR(ctx, fullName, pr.Number, "merge", "")
		if err == nil {
			t.Fatal("expected error on already-merged PR")
		}
		if !strings.Contains(err.Error(), "already merged") {
			t.Errorf("expected 'already merged' error, got: %v", err)
		}
	})

	t.Run("merge_conflict_error", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()
		ctx := context.Background()

		user := db.User{Login: "conflict-user", Name: "conflict-user", Type: db.TypeUser, SiteAdmin: true}
		svc.DB.Create(&user)
		fullName := "conflict-user/conflict-repo"

		_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin:    "conflict-user",
			Name:          "conflict-repo",
			DefaultBranch: "main",
			AutoInit:      true,
		})
		if err != nil {
			t.Fatalf("create repo: %v", err)
		}

		// Create branch and write conflicting content on both branches.
		if err := svc.Git.CreateBranch(ctx, fullName, "conflict-branch", "main"); err != nil {
			t.Fatalf("create branch: %v", err)
		}
		// Write different content to the same file on both branches.
		if _, err := svc.Git.WriteFile(ctx, fullName, "conflict-branch", "conflict.txt", "head change", []byte("head content\n")); err != nil {
			t.Fatalf("write file on feature: %v", err)
		}
		if _, err := svc.Git.WriteFile(ctx, fullName, "main", "conflict.txt", "base change", []byte("base content\n")); err != nil {
			t.Fatalf("write file on main: %v", err)
		}

		authCtx := service.ContextWithUser(ctx, user)
		pr, err := svc.CreatePR(authCtx, service.CreatePRInput{
			RepoFullName: fullName,
			Title:        "Conflict PR",
			HeadRef:      "conflict-branch",
			BaseRef:      "main",
			AuthorLogin:  "conflict-user",
		})
		if err != nil {
			t.Fatalf("create PR: %v", err)
		}

		_, mergeErr := svc.MergePR(authCtx, fullName, pr.Number, "merge", "")
		if mergeErr == nil {
			t.Fatal("expected merge conflict error")
		}
		errMsg := strings.ToLower(mergeErr.Error())
		if !strings.Contains(errMsg, "conflict") && !strings.Contains(errMsg, "merge") {
			t.Errorf("expected conflict-related error, got: %v", mergeErr)
		}

		// Verify PR is still open after failed merge.
		got, err := svc.GetPR(ctx, fullName, pr.Number)
		if err != nil {
			t.Fatalf("GetPR: %v", err)
		}
		if got.Merged {
			t.Error("expected Merged == false after conflict")
		}
		if got.State != "open" {
			t.Errorf("expected state open, got %s", got.State)
		}
	})

	t.Run("explicit_commit_title", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()

		pr, ctx, _ := setupPRWithRealBranches(t, svc, "title-user", "title-repo")
		fullName := "title-user/title-repo"

		merged, err := svc.MergePR(ctx, fullName, pr.Number, "merge", "Custom merge title")
		if err != nil {
			t.Fatalf("MergePR with title: %v", err)
		}
		if !merged.Merged {
			t.Error("expected Merged == true")
		}
		if merged.MergeCommitSHA == "" {
			t.Error("expected MergeCommitSHA non-empty")
		}
	})

	t.Run("list_prs_after_merge", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()

		pr, ctx, _ := setupPRWithRealBranches(t, svc, "listm-user", "listm-repo")
		fullName := "listm-user/listm-repo"

		if _, err := svc.MergePR(ctx, fullName, pr.Number, "merge", ""); err != nil {
			t.Fatalf("MergePR: %v", err)
		}

		openPRs, err := svc.ListPRs(ctx, fullName, "open")
		if err != nil {
			t.Fatalf("ListPRs(open): %v", err)
		}
		if len(openPRs) != 0 {
			t.Errorf("expected 0 open PRs after merge, got %d", len(openPRs))
		}

		closedPRs, err := svc.ListPRs(ctx, fullName, "closed")
		if err != nil {
			t.Fatalf("ListPRs(closed): %v", err)
		}
		if len(closedPRs) != 1 {
			t.Errorf("expected 1 closed PR after merge, got %d", len(closedPRs))
		}
	})
}

// ─── File and commit retrieval ──────────────────────────────────────────────────

func TestPRFilesAndCommits(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	pr, ctx, _ := setupPRWithRealBranches(t, svc, "fc-user", "fc-repo")
	fullName := "fc-user/fc-repo"

	t.Run("list_files", func(t *testing.T) {
		files, err := svc.ListPRFiles(ctx, fullName, pr.Number)
		if err != nil {
			t.Fatalf("ListPRFiles: %v", err)
		}
		if len(files) == 0 {
			t.Fatal("expected at least 1 file")
		}

		found := false
		for _, f := range files {
			if f["filename"] == "hello.txt" {
				found = true
				if f["status"] != "added" {
					t.Errorf("expected status 'added', got %v", f["status"])
				}
				additions, _ := f["additions"].(int)
				if additions <= 0 {
					t.Errorf("expected additions > 0, got %d", additions)
				}
			}
		}
		if !found {
			t.Error("expected hello.txt in file list")
		}
	})

	t.Run("list_commits", func(t *testing.T) {
		commits, err := svc.ListPRCommits(ctx, fullName, pr.Number)
		if err != nil {
			t.Fatalf("ListPRCommits: %v", err)
		}
		if len(commits) == 0 {
			t.Fatal("expected at least 1 commit")
		}

		first := commits[0]
		sha, ok := first["sha"].(string)
		if !ok || sha == "" {
			t.Errorf("expected non-empty string sha, got %v (type %T)", first["sha"], first["sha"])
		}
		commitMap, ok := first["commit"].(map[string]any)
		if !ok {
			t.Fatal("expected commit field to be map")
		}
		msg, _ := commitMap["message"].(string)
		if msg == "" {
			t.Error("expected non-empty commit message")
		}
	})
}

// ─── Review state transitions ───────────────────────────────────────────────────

func TestPRReviewLifecycle(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "rvl-user", "rvl-repo")
	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "rvl-user/rvl-repo",
		Title:        "Review lifecycle PR",
		AuthorLogin:  "rvl-user",
		HeadRef:      "feat",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	t.Run("submit_approve", func(t *testing.T) {
		r, err := svc.AddPRReview(ctx, pr.ID, "rvl-user", "PENDING", "", "abc")
		if err != nil {
			t.Fatalf("AddPRReview: %v", err)
		}
		submitted, err := svc.SubmitPRReview(ctx, r.ID, "APPROVED", "")
		if err != nil {
			t.Fatalf("SubmitPRReview: %v", err)
		}
		if submitted.State != "APPROVED" {
			t.Errorf("expected state APPROVED, got %s", submitted.State)
		}
		if submitted.SubmittedAt == nil {
			t.Error("expected SubmittedAt != nil")
		}
	})

	t.Run("submit_changes_requested", func(t *testing.T) {
		r, err := svc.AddPRReview(ctx, pr.ID, "rvl-user", "PENDING", "", "abc")
		if err != nil {
			t.Fatalf("AddPRReview: %v", err)
		}
		submitted, err := svc.SubmitPRReview(ctx, r.ID, "CHANGES_REQUESTED", "fix this")
		if err != nil {
			t.Fatalf("SubmitPRReview: %v", err)
		}
		if submitted.State != "CHANGES_REQUESTED" {
			t.Errorf("expected CHANGES_REQUESTED, got %s", submitted.State)
		}
		if submitted.Body != "fix this" {
			t.Errorf("expected body 'fix this', got %q", submitted.Body)
		}
	})

	t.Run("dismiss_approved", func(t *testing.T) {
		r, err := svc.AddPRReview(ctx, pr.ID, "rvl-user", "APPROVED", "lgtm", "abc")
		if err != nil {
			t.Fatalf("AddPRReview: %v", err)
		}
		dismissed, err := svc.DismissPRReview(ctx, r.ID, "stale")
		if err != nil {
			t.Fatalf("DismissPRReview: %v", err)
		}
		if dismissed.State != "DISMISSED" {
			t.Errorf("expected DISMISSED, got %s", dismissed.State)
		}
	})

	t.Run("dismiss_changes_requested", func(t *testing.T) {
		r, err := svc.AddPRReview(ctx, pr.ID, "rvl-user", "CHANGES_REQUESTED", "fix", "abc")
		if err != nil {
			t.Fatalf("AddPRReview: %v", err)
		}
		dismissed, err := svc.DismissPRReview(ctx, r.ID, "resolved")
		if err != nil {
			t.Fatalf("DismissPRReview: %v", err)
		}
		if dismissed.State != "DISMISSED" {
			t.Errorf("expected DISMISSED, got %s", dismissed.State)
		}
	})

	t.Run("dismiss_pending_rejected", func(t *testing.T) {
		r, err := svc.AddPRReview(ctx, pr.ID, "rvl-user", "PENDING", "", "abc")
		if err != nil {
			t.Fatalf("AddPRReview: %v", err)
		}
		_, err = svc.DismissPRReview(ctx, r.ID, "nope")
		if err == nil {
			t.Fatal("expected error dismissing PENDING review")
		}
	})

	t.Run("dismiss_commented_rejected", func(t *testing.T) {
		r, err := svc.AddPRReview(ctx, pr.ID, "rvl-user", "COMMENTED", "note", "abc")
		if err != nil {
			t.Fatalf("AddPRReview: %v", err)
		}
		_, err = svc.DismissPRReview(ctx, r.ID, "nope")
		if err == nil {
			t.Fatal("expected error dismissing COMMENTED review")
		}
	})

	t.Run("delete_pending", func(t *testing.T) {
		r, err := svc.AddPRReview(ctx, pr.ID, "rvl-user", "PENDING", "", "abc")
		if err != nil {
			t.Fatalf("AddPRReview: %v", err)
		}
		if err := svc.DeletePRReview(ctx, r.ID); err != nil {
			t.Fatalf("DeletePRReview: %v", err)
		}
		_, err = svc.GetPRReview(ctx, r.ID)
		if err == nil {
			t.Fatal("expected error fetching deleted review")
		}
	})

	t.Run("delete_non_pending_rejected", func(t *testing.T) {
		r, err := svc.AddPRReview(ctx, pr.ID, "rvl-user", "APPROVED", "lgtm", "abc")
		if err != nil {
			t.Fatalf("AddPRReview: %v", err)
		}
		err = svc.DeletePRReview(ctx, r.ID)
		if err == nil {
			t.Fatal("expected error deleting non-PENDING review")
		}
	})

	t.Run("update_body", func(t *testing.T) {
		r, err := svc.AddPRReview(ctx, pr.ID, "rvl-user", "APPROVED", "original", "abc")
		if err != nil {
			t.Fatalf("AddPRReview: %v", err)
		}
		updated, err := svc.UpdatePRReview(ctx, r.ID, "updated body")
		if err != nil {
			t.Fatalf("UpdatePRReview: %v", err)
		}
		if updated.Body != "updated body" {
			t.Errorf("expected 'updated body', got %q", updated.Body)
		}
	})
}

// ─── Review comment lifecycle ───────────────────────────────────────────────────

func TestPRReviewCommentLifecycle(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "rcl-user", "rcl-repo")
	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "rcl-user/rcl-repo",
		Title:        "Comment lifecycle PR",
		AuthorLogin:  "rcl-user",
		HeadRef:      "feat",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	review, err := svc.AddPRReview(ctx, pr.ID, "rcl-user", "PENDING", "", "sha1")
	if err != nil {
		t.Fatalf("AddPRReview: %v", err)
	}

	t.Run("create_and_list", func(t *testing.T) {
		comment := &db.PRReviewComment{
			PullRequestReviewID: &review.ID,
			AuthorLogin:         "rcl-user",
			Body:                "line comment",
			CommitID:            "sha1",
			Path:                "file.go",
			Line:                10,
			Side:                "RIGHT",
		}
		if err := svc.CreatePRReviewComment(ctx, pr.ID, comment); err != nil {
			t.Fatalf("CreatePRReviewComment: %v", err)
		}

		comments, err := svc.ListPRReviewComments(ctx, pr.ID)
		if err != nil {
			t.Fatalf("ListPRReviewComments: %v", err)
		}
		if len(comments) < 1 {
			t.Fatal("expected at least 1 comment")
		}
		if comments[0].Body != "line comment" {
			t.Errorf("expected body 'line comment', got %q", comments[0].Body)
		}
		if comments[0].SubjectType != "line" {
			t.Errorf("expected SubjectType 'line', got %q", comments[0].SubjectType)
		}
	})

	t.Run("reply_inherits_parent", func(t *testing.T) {
		parent := &db.PRReviewComment{
			PullRequestReviewID: &review.ID,
			AuthorLogin:         "rcl-user",
			Body:                "parent comment",
			CommitID:            "sha1",
			Path:                "reply.go",
			Line:                5,
			Side:                "RIGHT",
			SubjectType:         "line",
			DiffHunk:            "@@ -1,3 +1,5 @@",
		}
		if err := svc.CreatePRReviewComment(ctx, pr.ID, parent); err != nil {
			t.Fatalf("CreatePRReviewComment(parent): %v", err)
		}

		reply, err := svc.ReplyToPRReviewComment(ctx, pr.ID, parent.ID, "reply body", "rcl-user")
		if err != nil {
			t.Fatalf("ReplyToPRReviewComment: %v", err)
		}
		if reply.Path != parent.Path {
			t.Errorf("expected reply Path %q, got %q", parent.Path, reply.Path)
		}
		if reply.CommitID != parent.CommitID {
			t.Errorf("expected reply CommitID %q, got %q", parent.CommitID, reply.CommitID)
		}
		if reply.Line != parent.Line {
			t.Errorf("expected reply Line %d, got %d", parent.Line, reply.Line)
		}
		if reply.Side != parent.Side {
			t.Errorf("expected reply Side %q, got %q", parent.Side, reply.Side)
		}
		if reply.DiffHunk != parent.DiffHunk {
			t.Errorf("expected reply DiffHunk %q, got %q", parent.DiffHunk, reply.DiffHunk)
		}
		if reply.InReplyToID == nil || *reply.InReplyToID != parent.ID {
			t.Errorf("expected InReplyToID %d, got %v", parent.ID, reply.InReplyToID)
		}
	})

	t.Run("update_body", func(t *testing.T) {
		c := &db.PRReviewComment{
			PullRequestReviewID: &review.ID,
			AuthorLogin:         "rcl-user",
			Body:                "old body",
			Path:                "upd.go",
			Line:                1,
		}
		if err := svc.CreatePRReviewComment(ctx, pr.ID, c); err != nil {
			t.Fatalf("CreatePRReviewComment: %v", err)
		}

		updated, err := svc.UpdatePRReviewComment(ctx, c.ID, "new body")
		if err != nil {
			t.Fatalf("UpdatePRReviewComment: %v", err)
		}
		if updated.Body != "new body" {
			t.Errorf("expected 'new body', got %q", updated.Body)
		}
	})

	t.Run("delete", func(t *testing.T) {
		c := &db.PRReviewComment{
			PullRequestReviewID: &review.ID,
			AuthorLogin:         "rcl-user",
			Body:                "delete me",
			Path:                "del.go",
			Line:                1,
		}
		if err := svc.CreatePRReviewComment(ctx, pr.ID, c); err != nil {
			t.Fatalf("CreatePRReviewComment: %v", err)
		}

		if err := svc.DeletePRReviewComment(ctx, c.ID); err != nil {
			t.Fatalf("DeletePRReviewComment: %v", err)
		}
		_, err := svc.GetPRReviewComment(ctx, c.ID)
		if err == nil {
			t.Fatal("expected error fetching deleted comment")
		}
	})

	t.Run("resolve_unresolve", func(t *testing.T) {
		c := &db.PRReviewComment{
			PullRequestReviewID: &review.ID,
			AuthorLogin:         "rcl-user",
			Body:                "resolve me",
			Path:                "res.go",
			Line:                1,
		}
		if err := svc.CreatePRReviewComment(ctx, pr.ID, c); err != nil {
			t.Fatalf("CreatePRReviewComment: %v", err)
		}

		if err := svc.ResolvePRReviewThread(ctx, c.ID); err != nil {
			t.Fatalf("ResolvePRReviewThread: %v", err)
		}
		resolved, err := svc.GetPRReviewComment(ctx, c.ID)
		if err != nil {
			t.Fatalf("GetPRReviewComment: %v", err)
		}
		if !resolved.IsResolved {
			t.Error("expected IsResolved == true")
		}

		if err := svc.UnresolvePRReviewThread(ctx, c.ID); err != nil {
			t.Fatalf("UnresolvePRReviewThread: %v", err)
		}
		unresolved, err := svc.GetPRReviewComment(ctx, c.ID)
		if err != nil {
			t.Fatalf("GetPRReviewComment: %v", err)
		}
		if unresolved.IsResolved {
			t.Error("expected IsResolved == false")
		}
	})
}

// ─── Draft → ready transition ───────────────────────────────────────────────────

func TestPRDraftReadyTransition(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "draft-user", "draft-repo")
	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "draft-user/draft-repo",
		Title:        "Draft PR",
		AuthorLogin:  "draft-user",
		HeadRef:      "feat",
		BaseRef:      "main",
		Draft:        true,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if !pr.Draft {
		t.Fatal("expected Draft == true after creation")
	}

	t.Run("mark_ready", func(t *testing.T) {
		if err := svc.MarkPRReadyForReview(ctx, pr.ID); err != nil {
			t.Fatalf("MarkPRReadyForReview: %v", err)
		}

		got, err := svc.GetPRByID(ctx, pr.ID)
		if err != nil {
			t.Fatalf("GetPRByID: %v", err)
		}
		if got.Draft {
			t.Error("expected Draft == false after MarkPRReadyForReview")
		}
	})
}

// ─── Team review request lifecycle ──────────────────────────────────────────────

func TestPRReviewRequestExpanded(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "trr-user", "trr-repo")
	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "trr-user/trr-repo",
		Title:        "Team review PR",
		AuthorLogin:  "trr-user",
		HeadRef:      "feat",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	t.Run("team_review_request", func(t *testing.T) {
		if err := svc.RequestTeamReview(ctx, pr.ID, "core-team"); err != nil {
			t.Fatalf("RequestTeamReview: %v", err)
		}

		reqs, err := svc.ListReviewRequests(ctx, pr.ID)
		if err != nil {
			t.Fatalf("ListReviewRequests: %v", err)
		}
		found := false
		for _, r := range reqs {
			if r.TeamSlug == "core-team" {
				found = true
			}
		}
		if !found {
			t.Error("expected team review request for core-team")
		}
	})

	t.Run("team_duplicate_idempotent", func(t *testing.T) {
		// Request again — should not error or duplicate.
		if err := svc.RequestTeamReview(ctx, pr.ID, "core-team"); err != nil {
			t.Fatalf("RequestTeamReview(duplicate): %v", err)
		}

		reqs, err := svc.ListReviewRequests(ctx, pr.ID)
		if err != nil {
			t.Fatalf("ListReviewRequests: %v", err)
		}
		count := 0
		for _, r := range reqs {
			if r.TeamSlug == "core-team" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("expected exactly 1 team request, got %d", count)
		}
	})

	t.Run("remove_team_review_request", func(t *testing.T) {
		if err := svc.RemoveTeamReviewRequest(ctx, pr.ID, "core-team"); err != nil {
			t.Fatalf("RemoveTeamReviewRequest: %v", err)
		}

		reqs, err := svc.ListReviewRequests(ctx, pr.ID)
		if err != nil {
			t.Fatalf("ListReviewRequests: %v", err)
		}
		for _, r := range reqs {
			if r.TeamSlug == "core-team" {
				t.Error("expected team request to be removed")
			}
		}
	})
}
