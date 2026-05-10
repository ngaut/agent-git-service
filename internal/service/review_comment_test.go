package service_test

import (
	"context"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

func TestCreatePRReviewComment(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "rccuser", "rccrepo")

	// Create PR
	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "rccuser/rccrepo",
		Title:        "PR for review comments",
		AuthorLogin:  "rccuser",
		HeadRef:      "feat",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Create a review comment
	comment := &db.PRReviewComment{
		AuthorLogin: "rccuser",
		Body:        "This is a test comment",
		CommitID:    "abc123",
		Path:        "file.go",
		Line:        10,
	}
	err = svc.CreatePRReviewComment(ctx, pr.ID, comment)
	if err != nil {
		t.Fatalf("CreatePRReviewComment failed: %v", err)
	}

	// Verify comment was created
	if comment.ID == 0 {
		t.Error("expected comment ID to be set")
	}
	if comment.PullRequestID != pr.ID {
		t.Errorf("expected PullRequestID %d, got %d", pr.ID, comment.PullRequestID)
	}
	if comment.SubjectType != "line" {
		t.Errorf("expected SubjectType 'line', got %s", comment.SubjectType)
	}
}

func TestListPRReviewComments(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "lrcuser", "lrcrepo")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "lrcuser/lrcrepo",
		Title:        "PR for listing comments",
		AuthorLogin:  "lrcuser",
		HeadRef:      "feat",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Create multiple comments
	for i := 1; i <= 3; i++ {
		comment := &db.PRReviewComment{
			AuthorLogin: "lrcuser",
			Body:        db.LargeText("Comment " + string(rune('0'+i))),
			CommitID:    "abc123",
			Path:        "file.go",
			Line:        i * 10,
		}
		if err := svc.CreatePRReviewComment(ctx, pr.ID, comment); err != nil {
			t.Fatalf("CreatePRReviewComment failed: %v", err)
		}
	}

	// List comments
	comments, err := svc.ListPRReviewComments(ctx, pr.ID)
	if err != nil {
		t.Fatalf("ListPRReviewComments failed: %v", err)
	}
	if len(comments) != 3 {
		t.Errorf("expected 3 comments, got %d", len(comments))
	}
}

func TestGetPRReviewComment(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "grcuser", "grcrepo")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "grcuser/grcrepo",
		Title:        "PR for getting comment",
		AuthorLogin:  "grcuser",
		HeadRef:      "feat",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Create a comment
	comment := &db.PRReviewComment{
		AuthorLogin: "grcuser",
		Body:        "Test comment",
		CommitID:    "abc123",
		Path:        "file.go",
		Line:        10,
	}
	if err := svc.CreatePRReviewComment(ctx, pr.ID, comment); err != nil {
		t.Fatalf("CreatePRReviewComment failed: %v", err)
	}

	// Get the comment
	got, err := svc.GetPRReviewComment(ctx, comment.ID)
	if err != nil {
		t.Fatalf("GetPRReviewComment failed: %v", err)
	}
	if got.Body != "Test comment" {
		t.Errorf("expected body 'Test comment', got %s", got.Body)
	}

	// Get non-existent comment
	_, err = svc.GetPRReviewComment(ctx, 99999)
	if err == nil {
		t.Error("expected error for non-existent comment")
	}
}

func TestUpdatePRReviewComment(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "urcuser", "urcrepo")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "urcuser/urcrepo",
		Title:        "PR for updating comment",
		AuthorLogin:  "urcuser",
		HeadRef:      "feat",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Create a comment
	comment := &db.PRReviewComment{
		AuthorLogin: "urcuser",
		Body:        "Original body",
		CommitID:    "abc123",
		Path:        "file.go",
		Line:        10,
	}
	if err := svc.CreatePRReviewComment(ctx, pr.ID, comment); err != nil {
		t.Fatalf("CreatePRReviewComment failed: %v", err)
	}

	// Update the comment
	updated, err := svc.UpdatePRReviewComment(ctx, comment.ID, "Updated body")
	if err != nil {
		t.Fatalf("UpdatePRReviewComment failed: %v", err)
	}
	if updated.Body != "Updated body" {
		t.Errorf("expected body 'Updated body', got %s", updated.Body)
	}

	// Update non-existent comment
	_, err = svc.UpdatePRReviewComment(ctx, 99999, "test")
	if err == nil {
		t.Error("expected error for non-existent comment")
	}
}

func TestDeletePRReviewComment(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "drcuser", "drcrepo")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "drcuser/drcrepo",
		Title:        "PR for deleting comment",
		AuthorLogin:  "drcuser",
		HeadRef:      "feat",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Create a comment
	comment := &db.PRReviewComment{
		AuthorLogin: "drcuser",
		Body:        "To be deleted",
		CommitID:    "abc123",
		Path:        "file.go",
		Line:        10,
	}
	if err := svc.CreatePRReviewComment(ctx, pr.ID, comment); err != nil {
		t.Fatalf("CreatePRReviewComment failed: %v", err)
	}

	// Delete the comment
	err = svc.DeletePRReviewComment(ctx, comment.ID)
	if err != nil {
		t.Fatalf("DeletePRReviewComment failed: %v", err)
	}

	// Verify deletion
	_, err = svc.GetPRReviewComment(ctx, comment.ID)
	if err == nil {
		t.Error("expected error after deletion")
	}
}

func TestReplyToPRReviewComment(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "rrcuser", "rrcrepo")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "rrcuser/rrcrepo",
		Title:        "PR for reply comment",
		AuthorLogin:  "rrcuser",
		HeadRef:      "feat",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Create parent comment
	parent := &db.PRReviewComment{
		AuthorLogin: "rrcuser",
		Body:        "Parent comment",
		CommitID:    "abc123",
		Path:        "file.go",
		Line:        10,
	}
	if err := svc.CreatePRReviewComment(ctx, pr.ID, parent); err != nil {
		t.Fatalf("CreatePRReviewComment failed: %v", err)
	}

	// Reply to the comment
	reply, err := svc.ReplyToPRReviewComment(ctx, pr.ID, parent.ID, "Reply body", "replier")
	if err != nil {
		t.Fatalf("ReplyToPRReviewComment failed: %v", err)
	}

	// Verify reply
	if reply.InReplyToID == nil || *reply.InReplyToID != parent.ID {
		t.Errorf("expected InReplyToID %d, got %v", parent.ID, reply.InReplyToID)
	}
	if reply.Path != parent.Path {
		t.Errorf("expected Path %s, got %s", parent.Path, reply.Path)
	}
	if reply.Line != parent.Line {
		t.Errorf("expected Line %d, got %d", parent.Line, reply.Line)
	}
	if reply.Body != "Reply body" {
		t.Errorf("expected body 'Reply body', got %s", reply.Body)
	}

	// Reply to non-existent parent
	_, err = svc.ReplyToPRReviewComment(ctx, pr.ID, 99999, "test", "user")
	if err == nil {
		t.Error("expected error for non-existent parent")
	}
}

func TestResolveUnresolvePRReviewThread(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "rurtuser", "rurtrepo")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "rurtuser/rurtrepo",
		Title:        "PR for resolve/unresolve",
		AuthorLogin:  "rurtuser",
		HeadRef:      "feat",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Create a comment
	comment := &db.PRReviewComment{
		AuthorLogin: "rurtuser",
		Body:        "Thread root",
		CommitID:    "abc123",
		Path:        "file.go",
		Line:        10,
	}
	if err := svc.CreatePRReviewComment(ctx, pr.ID, comment); err != nil {
		t.Fatalf("CreatePRReviewComment failed: %v", err)
	}

	// Resolve the thread
	err = svc.ResolvePRReviewThread(ctx, comment.ID)
	if err != nil {
		t.Fatalf("ResolvePRReviewThread failed: %v", err)
	}

	// Verify resolution
	got, err := svc.GetPRReviewComment(ctx, comment.ID)
	if err != nil {
		t.Fatalf("GetPRReviewComment failed: %v", err)
	}
	if !got.IsResolved {
		t.Error("expected IsResolved to be true")
	}

	// Unresolve the thread
	err = svc.UnresolvePRReviewThread(ctx, comment.ID)
	if err != nil {
		t.Fatalf("UnresolvePRReviewThread failed: %v", err)
	}

	// Verify unresolution
	got2, err := svc.GetPRReviewComment(ctx, comment.ID)
	if err != nil {
		t.Fatalf("GetPRReviewComment failed: %v", err)
	}
	if got2.IsResolved {
		t.Error("expected IsResolved to be false")
	}
}

func TestGetPRReview(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "gpruser", "gprrepo")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "gpruser/gprrepo",
		Title:        "PR for getting review",
		AuthorLogin:  "gpruser",
		HeadRef:      "feat",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Create a review
	review, err := svc.AddPRReview(ctx, pr.ID, "gpruser", "APPROVED", "LGTM", "abc123")
	if err != nil {
		t.Fatalf("AddPRReview failed: %v", err)
	}

	// Get the review
	got, err := svc.GetPRReview(ctx, review.ID)
	if err != nil {
		t.Fatalf("GetPRReview failed: %v", err)
	}
	if got.State != "APPROVED" {
		t.Errorf("expected state APPROVED, got %s", got.State)
	}

	// Get non-existent review
	_, err = svc.GetPRReview(ctx, 99999)
	if err == nil {
		t.Error("expected error for non-existent review")
	}
}

func TestCountFunctions(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "cntuser", "cntrepo")

	// Get repo for repoID
	repo, err := svc.GetRepo(ctx, "cntuser/cntrepo")
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}

	// Create an issue
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "cntuser/cntrepo",
		Title:        "Test issue",
		AuthorLogin:  "cntuser",
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Create a PR
	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "cntuser/cntrepo",
		Title:        "Test PR",
		AuthorLogin:  "cntuser",
		HeadRef:      "feat",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Test CountIssueComments (should be 0 initially)
	count := svc.CountIssueComments(ctx, repo.ID, issue.Number)
	if count != 0 {
		t.Errorf("expected 0 issue comments, got %d", count)
	}

	// Test CountPRComments (should be 0 initially)
	count = svc.CountPRComments(ctx, repo.ID, pr.Number)
	if count != 0 {
		t.Errorf("expected 0 PR comments, got %d", count)
	}

	// Test CountPRReviewComments (should be 0 initially)
	count = svc.CountPRReviewComments(ctx, pr.ID)
	if count != 0 {
		t.Errorf("expected 0 PR review comments, got %d", count)
	}

	// Create a review comment
	comment := &db.PRReviewComment{
		AuthorLogin: "cntuser",
		Body:        "Test comment",
		CommitID:    "abc123",
		Path:        "file.go",
		Line:        10,
	}
	if err := svc.CreatePRReviewComment(ctx, pr.ID, comment); err != nil {
		t.Fatalf("CreatePRReviewComment failed: %v", err)
	}

	// Verify count increased
	count = svc.CountPRReviewComments(ctx, pr.ID)
	if count != 1 {
		t.Errorf("expected 1 PR review comment, got %d", count)
	}
}
