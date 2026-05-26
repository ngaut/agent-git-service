package service_test

import (
	"context"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

// ─── PR Diff/Files/Commits Tests ─────────────────────────────────────────────────────

func TestListPRCommits(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	pr, authCtx, _ := setupPRWithRealBranches(t, svc, "commits-user", "commits-repo")
	fullName := "commits-user/commits-repo"

	// Add another commit to the feature branch
	if _, err := svc.Git.WriteFile(authCtx, fullName, "feature", "another.txt", "add another file", []byte("another commit\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Update PR head SHA
	if err := svc.PushHeadSHA(authCtx, fullName, pr.Number); err != nil {
		t.Fatalf("PushHeadSHA failed: %v", err)
	}

	// List commits
	commits, err := svc.ListPRCommits(authCtx, fullName, pr.Number)
	if err != nil {
		t.Fatalf("ListPRCommits failed: %v", err)
	}

	if len(commits) == 0 {
		t.Fatal("expected at least one commit")
	}

	// Verify commit structure
	for _, c := range commits {
		if c["sha"] == "" {
			t.Error("expected commit sha to be set")
		}
		commit, ok := c["commit"].(map[string]any)
		if !ok {
			t.Fatal("expected commit map")
		}
		if commit["message"] == "" {
			t.Error("expected commit message to be set")
		}
		author, ok := commit["author"].(map[string]any)
		if !ok {
			t.Fatal("expected author map")
		}
		if author["name"] == "" {
			t.Error("expected author name to be set")
		}
	}
}

func TestListPRFiles(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	pr, authCtx, _ := setupPRWithRealBranches(t, svc, "files-user", "files-repo")
	fullName := "files-user/files-repo"

	// Add another file to create more diff
	if _, err := svc.Git.WriteFile(authCtx, fullName, "feature", "test.txt", "add test file", []byte("test content\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Update PR head SHA
	if err := svc.PushHeadSHA(authCtx, fullName, pr.Number); err != nil {
		t.Fatalf("PushHeadSHA failed: %v", err)
	}

	// List files
	files, err := svc.ListPRFiles(authCtx, fullName, pr.Number)
	if err != nil {
		t.Fatalf("ListPRFiles failed: %v", err)
	}

	if len(files) == 0 {
		t.Fatal("expected at least one changed file")
	}

	// Verify file structure
	for _, f := range files {
		if f["filename"] == "" {
			t.Error("expected filename to be set")
		}
		if f["status"] == "" {
			t.Error("expected status to be set")
		}
		additions, ok := f["additions"].(int)
		if !ok || additions < 0 {
			t.Error("expected additions to be a non-negative int")
		}
	}
}

func TestDiffFiles(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	pr, authCtx, _ := setupPRWithRealBranches(t, svc, "diff-user", "diff-repo")
	fullName := "diff-user/diff-repo"

	// Get base and head SHA
	baseSHA := pr.BaseSHA
	if err := svc.PushHeadSHA(authCtx, fullName, pr.Number); err != nil {
		t.Fatalf("PushHeadSHA failed: %v", err)
	}

	// Refresh PR to get updated head SHA
	var updatedPR db.PullRequest
	svc.DB.First(&updatedPR, pr.ID)
	headSHA := updatedPR.HeadSHA

	// Test DiffFiles
	files, err := svc.DiffFiles(authCtx, fullName, baseSHA, headSHA)
	if err != nil {
		t.Fatalf("DiffFiles failed: %v", err)
	}

	if len(files) == 0 {
		t.Fatal("expected at least one changed file")
	}

	// Verify each file has required fields
	for _, f := range files {
		if f["filename"] == "" {
			t.Error("expected filename to be set")
		}
		status := f["status"].(string)
		if status != "added" && status != "modified" && status != "removed" && status != "changed" {
			t.Errorf("unexpected status: %s", status)
		}
	}
}

func TestDiffFiles_EmptySHAs(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Test with empty SHAs
	files, err := svc.DiffFiles(ctx, "user/repo", "", "")
	if err != nil {
		t.Fatalf("DiffFiles with empty SHAs failed: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected empty result for empty SHAs, got %d files", len(files))
	}
}

func TestPRDiffStats(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	pr, authCtx, _ := setupPRWithRealBranches(t, svc, "stats-user", "stats-repo")
	fullName := "stats-user/stats-repo"

	// Add more content to create stats
	if _, err := svc.Git.WriteFile(authCtx, fullName, "feature", "stats.txt", "add stats file", []byte("line1\nline2\nline3\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Update PR head SHA
	if err := svc.PushHeadSHA(authCtx, fullName, pr.Number); err != nil {
		t.Fatalf("PushHeadSHA failed: %v", err)
	}

	// Refresh PR to get updated head SHA
	var updatedPR db.PullRequest
	svc.DB.First(&updatedPR, pr.ID)

	// Test PRDiffStats
	additions, deletions, changedFiles := svc.PRDiffStats(authCtx, fullName, pr.BaseSHA, updatedPR.HeadSHA)

	if additions == 0 && deletions == 0 {
		t.Error("expected non-zero additions or deletions")
	}
	if changedFiles == 0 {
		t.Error("expected at least one changed file")
	}
}

func TestPRDiffStats_EmptySHAs(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	additions, deletions, changedFiles := svc.PRDiffStats(ctx, "user/repo", "", "")
	if additions != 0 || deletions != 0 || changedFiles != 0 {
		t.Errorf("expected all zeros for empty SHAs, got %d/%d/%d", additions, deletions, changedFiles)
	}
}

func TestPushHeadSHA(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	pr, authCtx, _ := setupPRWithRealBranches(t, svc, "pushsha-user", "pushsha-repo")
	fullName := "pushsha-user/pushsha-repo"

	// Make a commit on feature branch
	if _, err := svc.Git.WriteFile(authCtx, fullName, "feature", "new.txt", "new commit", []byte("new content\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Push head SHA
	err := svc.PushHeadSHA(authCtx, fullName, pr.Number)
	if err != nil {
		t.Fatalf("PushHeadSHA failed: %v", err)
	}

	// Verify head SHA was updated
	var updatedPR db.PullRequest
	if err := svc.DB.First(&updatedPR, pr.ID).Error; err != nil {
		t.Fatalf("failed to get updated PR: %v", err)
	}
	if updatedPR.HeadSHA == "" {
		t.Error("expected HeadSHA to be updated")
	}
	if updatedPR.HeadSHA == pr.HeadSHA {
		t.Error("expected HeadSHA to change after new commit")
	}
}

// ─── Review Lifecycle Tests ─────────────────────────────────────────────────────

func TestResolvePRReviewThread(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "resolve-user", "resolve-repo")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "resolve-user/resolve-repo",
		Title:        "PR for thread resolve",
		AuthorLogin:  "resolve-user",
		HeadRef:      "feature",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Create a review comment
	comment := db.PRReviewComment{
		PullRequestID: pr.ID,
		AuthorLogin:   "resolve-user",
		Body:          "Test comment",
		Path:          "README.md",
		Line:          1,
		IsResolved:    false,
	}
	if err := svc.DB.Create(&comment).Error; err != nil {
		t.Fatalf("Create comment failed: %v", err)
	}

	// Resolve the thread
	err = svc.ResolvePRReviewThread(ctx, comment.ID)
	if err != nil {
		t.Fatalf("ResolvePRReviewThread failed: %v", err)
	}

	// Verify it's resolved
	var updatedComment db.PRReviewComment
	if err := svc.DB.First(&updatedComment, comment.ID).Error; err != nil {
		t.Fatalf("failed to get updated comment: %v", err)
	}
	if !updatedComment.IsResolved {
		t.Error("expected comment to be resolved")
	}
}

func TestUnresolvePRReviewThread(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "unresolve-user", "unresolve-repo")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "unresolve-user/unresolve-repo",
		Title:        "PR for thread unresolve",
		AuthorLogin:  "unresolve-user",
		HeadRef:      "feature",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Create a resolved review comment
	comment := db.PRReviewComment{
		PullRequestID: pr.ID,
		AuthorLogin:   "unresolve-user",
		Body:          "Test comment",
		Path:          "README.md",
		Line:          1,
		IsResolved:    true,
	}
	if err := svc.DB.Create(&comment).Error; err != nil {
		t.Fatalf("Create comment failed: %v", err)
	}

	// Unresolve the thread
	err = svc.UnresolvePRReviewThread(ctx, comment.ID)
	if err != nil {
		t.Fatalf("UnresolvePRReviewThread failed: %v", err)
	}

	// Verify it's unresolved
	var updatedComment db.PRReviewComment
	if err := svc.DB.First(&updatedComment, comment.ID).Error; err != nil {
		t.Fatalf("failed to get updated comment: %v", err)
	}
	if updatedComment.IsResolved {
		t.Error("expected comment to be unresolved")
	}
}

func TestDismissPRReview(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "dismiss-user", "dismiss-repo")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "dismiss-user/dismiss-repo",
		Title:        "PR for review dismiss",
		AuthorLogin:  "dismiss-user",
		HeadRef:      "feature",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Create an approved review
	review := db.PullRequestReview{
		PullRequestID: pr.ID,
		AuthorLogin:   "dismiss-user",
		State:         "APPROVED",
		Body:          "LGTM",
	}
	if err := svc.DB.Create(&review).Error; err != nil {
		t.Fatalf("Create review failed: %v", err)
	}

	// Dismiss the review
	dismissedReview, err := svc.DismissPRReview(ctx, review.ID, "Needs more work")
	if err != nil {
		t.Fatalf("DismissPRReview failed: %v", err)
	}

	if dismissedReview.State != "DISMISSED" {
		t.Errorf("expected state DISMISSED, got %s", dismissedReview.State)
	}
	if dismissedReview.Body != "Needs more work" {
		t.Errorf("expected body 'Needs more work', got %s", dismissedReview.Body)
	}
}

func TestDismissPRReview_InvalidState(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "dismiss2-user", "dismiss2-repo")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "dismiss2-user/dismiss2-repo",
		Title:        "PR for review dismiss",
		AuthorLogin:  "dismiss2-user",
		HeadRef:      "feature",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Create a pending review (cannot be dismissed)
	review := db.PullRequestReview{
		PullRequestID: pr.ID,
		AuthorLogin:   "dismiss2-user",
		State:         "PENDING",
		Body:          "Draft",
	}
	if err := svc.DB.Create(&review).Error; err != nil {
		t.Fatalf("Create review failed: %v", err)
	}

	// Try to dismiss - should fail
	_, err = svc.DismissPRReview(ctx, review.ID, "Dismiss")
	if err == nil {
		t.Fatal("expected error when dismissing pending review")
	}
}

func TestSubmitPRReview(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "submit-user", "submit-repo")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "submit-user/submit-repo",
		Title:        "PR for review submit",
		AuthorLogin:  "submit-user",
		HeadRef:      "feature",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Create a pending review
	review := db.PullRequestReview{
		PullRequestID: pr.ID,
		AuthorLogin:   "submit-user",
		State:         "PENDING",
		Body:          "Draft review",
	}
	if err := svc.DB.Create(&review).Error; err != nil {
		t.Fatalf("Create review failed: %v", err)
	}

	// Submit the review as APPROVED
	submittedReview, err := svc.SubmitPRReview(ctx, review.ID, "APPROVED", "Looks good!")
	if err != nil {
		t.Fatalf("SubmitPRReview failed: %v", err)
	}

	if submittedReview.State != "APPROVED" {
		t.Errorf("expected state APPROVED, got %s", submittedReview.State)
	}
	if submittedReview.Body != "Looks good!" {
		t.Errorf("expected body 'Looks good!', got %s", submittedReview.Body)
	}
	if submittedReview.SubmittedAt.IsZero() {
		t.Error("expected SubmittedAt to be set")
	}
}

func TestSubmitPRReview_ChangesRequested(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "submit2-user", "submit2-repo")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "submit2-user/submit2-repo",
		Title:        "PR for review submit",
		AuthorLogin:  "submit2-user",
		HeadRef:      "feature",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Create a pending review
	review := db.PullRequestReview{
		PullRequestID: pr.ID,
		AuthorLogin:   "submit2-user",
		State:         "PENDING",
		Body:          "Draft",
	}
	if err := svc.DB.Create(&review).Error; err != nil {
		t.Fatalf("Create review failed: %v", err)
	}

	// Submit as CHANGES_REQUESTED
	submittedReview, err := svc.SubmitPRReview(ctx, review.ID, "CHANGES_REQUESTED", "Please fix this")
	if err != nil {
		t.Fatalf("SubmitPRReview failed: %v", err)
	}

	if submittedReview.State != "CHANGES_REQUESTED" {
		t.Errorf("expected state CHANGES_REQUESTED, got %s", submittedReview.State)
	}
}

func TestDeletePRReview(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "delete-user", "delete-repo")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "delete-user/delete-repo",
		Title:        "PR for review delete",
		AuthorLogin:  "delete-user",
		HeadRef:      "feature",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Create a pending review
	review := db.PullRequestReview{
		PullRequestID: pr.ID,
		AuthorLogin:   "delete-user",
		State:         "PENDING",
		Body:          "Draft",
	}
	if err := svc.DB.Create(&review).Error; err != nil {
		t.Fatalf("Create review failed: %v", err)
	}

	// Add a comment to the review
	reviewID := review.ID
	comment := db.PRReviewComment{
		PullRequestID:       pr.ID,
		PullRequestReviewID: &reviewID,
		AuthorLogin:         "delete-user",
		Body:                "Comment on review",
		Path:                "README.md",
		Line:                1,
	}
	if err := svc.DB.Create(&comment).Error; err != nil {
		t.Fatalf("Create comment failed: %v", err)
	}

	// Delete the review
	err = svc.DeletePRReview(ctx, review.ID)
	if err != nil {
		t.Fatalf("DeletePRReview failed: %v", err)
	}

	// Verify review is deleted
	var deletedReview db.PullRequestReview
	if err := svc.DB.First(&deletedReview, review.ID).Error; err == nil {
		t.Error("expected review to be deleted")
	}

	// Verify comment is also deleted
	var deletedComment db.PRReviewComment
	if err := svc.DB.First(&deletedComment, comment.ID).Error; err == nil {
		t.Error("expected comment to be deleted with review")
	}
}

func TestDeletePRReview_NonPending(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "delete2-user", "delete2-repo")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "delete2-user/delete2-repo",
		Title:        "PR for review delete",
		AuthorLogin:  "delete2-user",
		HeadRef:      "feature",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Create an approved review (cannot be deleted)
	review := db.PullRequestReview{
		PullRequestID: pr.ID,
		AuthorLogin:   "delete2-user",
		State:         "APPROVED",
		Body:          "LGTM",
	}
	if err := svc.DB.Create(&review).Error; err != nil {
		t.Fatalf("Create review failed: %v", err)
	}

	// Try to delete - should fail
	err = svc.DeletePRReview(ctx, review.ID)
	if err == nil {
		t.Fatal("expected error when deleting non-pending review")
	}
}

func TestMarkPRReadyForReview(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "ready-user", "ready-repo")

	// Create a draft PR
	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "ready-user/ready-repo",
		Title:        "Draft PR",
		AuthorLogin:  "ready-user",
		HeadRef:      "feature",
		BaseRef:      "main",
		Draft:        true,
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	if !pr.Draft {
		t.Fatal("expected PR to be draft")
	}

	// Mark as ready for review
	err = svc.MarkPRReadyForReview(ctx, pr.ID)
	if err != nil {
		t.Fatalf("MarkPRReadyForReview failed: %v", err)
	}

	// Verify draft is false
	var updatedPR db.PullRequest
	if err := svc.DB.First(&updatedPR, pr.ID).Error; err != nil {
		t.Fatalf("failed to get updated PR: %v", err)
	}
	if updatedPR.Draft {
		t.Error("expected PR to no longer be draft")
	}
}

func TestListReviewCommentsForReview(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "listc-user", "listc-repo")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "listc-user/listc-repo",
		Title:        "PR for comments",
		AuthorLogin:  "listc-user",
		HeadRef:      "feature",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Create a review
	review := db.PullRequestReview{
		PullRequestID: pr.ID,
		AuthorLogin:   "listc-user",
		State:         "APPROVED",
		Body:          "LGTM",
	}
	if err := svc.DB.Create(&review).Error; err != nil {
		t.Fatalf("Create review failed: %v", err)
	}

	// Add comments to the review
	reviewID := review.ID
	comment1 := db.PRReviewComment{
		PullRequestID:       pr.ID,
		PullRequestReviewID: &reviewID,
		AuthorLogin:         "listc-user",
		Body:                "Comment 1",
		Path:                "README.md",
		Line:                1,
	}
	comment2 := db.PRReviewComment{
		PullRequestID:       pr.ID,
		PullRequestReviewID: &reviewID,
		AuthorLogin:         "listc-user",
		Body:                "Comment 2",
		Path:                "main.go",
		Line:                10,
	}
	if err := svc.DB.Create(&comment1).Error; err != nil {
		t.Fatalf("Create comment1 failed: %v", err)
	}
	if err := svc.DB.Create(&comment2).Error; err != nil {
		t.Fatalf("Create comment2 failed: %v", err)
	}

	// List comments for the review
	comments, err := svc.ListReviewCommentsForReview(ctx, review.ID)
	if err != nil {
		t.Fatalf("ListReviewCommentsForReview failed: %v", err)
	}

	if len(comments) != 2 {
		t.Errorf("expected 2 comments, got %d", len(comments))
	}
}

func TestCountIssueComments(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	user := db.User{Login: "count-user", Type: db.TypeUser}
	svc.DB.Create(&user)

	repo := db.Repository{
		Name:     "count-repo",
		FullName: "count-user/count-repo",
		OwnerID:  user.ID,
	}
	svc.DB.Create(&repo)

	issue := db.Issue{
		RepositoryID: repo.ID,
		Number:       1,
		Title:        "Test issue",
		AuthorID:     user.ID,
	}
	svc.DB.Create(&issue)

	// Add comments
	comment1 := db.IssueComment{
		RepositoryID: repo.ID,
		IssueNumber:  1,
		AuthorID:     user.ID,
		Body:         "Comment 1",
	}
	comment2 := db.IssueComment{
		RepositoryID: repo.ID,
		IssueNumber:  1,
		AuthorID:     user.ID,
		Body:         "Comment 2",
	}
	svc.DB.Create(&comment1)
	svc.DB.Create(&comment2)

	// Count comments
	count := svc.CountIssueComments(ctx, repo.ID, 1)
	if count != 2 {
		t.Errorf("expected 2 comments, got %d", count)
	}

	// Count for non-existent issue
	count2 := svc.CountIssueComments(ctx, repo.ID, 999)
	if count2 != 0 {
		t.Errorf("expected 0 comments for non-existent issue, got %d", count2)
	}
}

func TestCountPRComments(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	user := db.User{Login: "countpr-user", Type: db.TypeUser}
	svc.DB.Create(&user)

	repo := db.Repository{
		Name:     "countpr-repo",
		FullName: "countpr-user/countpr-repo",
		OwnerID:  user.ID,
	}
	svc.DB.Create(&repo)

	pr := db.PullRequest{
		RepositoryID: repo.ID,
		Number:       1,
		Title:        "Test PR",
		AuthorID:     user.ID,
	}
	svc.DB.Create(&pr)

	// Add issue-style comments to PR
	comment1 := db.IssueComment{
		RepositoryID: repo.ID,
		IssueNumber:  1,
		AuthorID:     user.ID,
		Body:         "Comment 1",
	}
	comment2 := db.IssueComment{
		RepositoryID: repo.ID,
		IssueNumber:  1,
		AuthorID:     user.ID,
		Body:         "Comment 2",
	}
	svc.DB.Create(&comment1)
	svc.DB.Create(&comment2)

	// Count PR comments
	count := svc.CountPRComments(ctx, repo.ID, 1)
	if count != 2 {
		t.Errorf("expected 2 comments, got %d", count)
	}
}

func TestCountPRReviewComments(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "countrev-user", "countrev-repo")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "countrev-user/countrev-repo",
		Title:        "PR for review comments",
		AuthorLogin:  "countrev-user",
		HeadRef:      "feature",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Add review comments
	comment1 := db.PRReviewComment{
		PullRequestID: pr.ID,
		AuthorLogin:   "countrev-user",
		Body:          "Comment 1",
		Path:          "README.md",
		Line:          1,
	}
	comment2 := db.PRReviewComment{
		PullRequestID: pr.ID,
		AuthorLogin:   "countrev-user",
		Body:          "Comment 2",
		Path:          "main.go",
		Line:          10,
	}
	comment3 := db.PRReviewComment{
		PullRequestID: pr.ID,
		AuthorLogin:   "countrev-user",
		Body:          "Comment 3",
		Path:          "util.go",
		Line:          5,
	}
	svc.DB.Create(&comment1)
	svc.DB.Create(&comment2)
	svc.DB.Create(&comment3)

	// Count review comments
	count := svc.CountPRReviewComments(ctx, pr.ID)
	if count != 3 {
		t.Errorf("expected 3 review comments, got %d", count)
	}
}
