package service_test

import (
	"context"
	"testing"

	"github.com/ngaut/agent-git-service/internal/service"
)

func TestReviewRequestFlow(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "rvuser", "rvrepo")

	// Create PR to review
	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "rvuser/rvrepo",
		Title:        "PR for review",
		AuthorLogin:  "rvuser",
		HeadRef:      "feat",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Request review
	if err := svc.RequestReview(ctx, pr.ID, "reviewer1"); err != nil {
		t.Fatalf("RequestReview failed: %v", err)
	}
	if err := svc.RequestReview(ctx, pr.ID, "reviewer2"); err != nil {
		t.Fatalf("RequestReview(2) failed: %v", err)
	}

	// Duplicate request should be idempotent
	if err := svc.RequestReview(ctx, pr.ID, "reviewer1"); err != nil {
		t.Fatalf("RequestReview(duplicate) failed: %v", err)
	}

	// List
	reqs, err := svc.ListReviewRequests(ctx, pr.ID)
	if err != nil {
		t.Fatalf("ListReviewRequests failed: %v", err)
	}
	if len(reqs) != 2 {
		t.Errorf("expected 2 review requests, got %d", len(reqs))
	}

	// Remove
	if err := svc.RemoveReviewRequest(ctx, pr.ID, "reviewer1"); err != nil {
		t.Fatalf("RemoveReviewRequest failed: %v", err)
	}
	reqs2, _ := svc.ListReviewRequests(ctx, pr.ID)
	if len(reqs2) != 1 {
		t.Errorf("expected 1 request after remove, got %d", len(reqs2))
	}
}

func TestPRReviewFlow(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "rvuser2", "rvrepo2")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "rvuser2/rvrepo2",
		Title:        "PR for reviews",
		AuthorLogin:  "rvuser2",
		HeadRef:      "feat",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Add reviews
	r1, err := svc.AddPRReview(ctx, pr.ID, "rvuser2", "APPROVED", "LGTM", "abc123")
	if err != nil {
		t.Fatalf("AddPRReview failed: %v", err)
	}
	if r1.State != "APPROVED" {
		t.Errorf("expected state APPROVED, got %s", r1.State)
	}
	if r1.Body != "LGTM" {
		t.Errorf("expected body LGTM, got %s", r1.Body)
	}

	_, err = svc.AddPRReview(ctx, pr.ID, "rvuser2", "CHANGES_REQUESTED", "Please fix", "def456")
	if err != nil {
		t.Fatalf("AddPRReview(2) failed: %v", err)
	}

	// List reviews
	reviews, err := svc.ListPRReviews(ctx, pr.ID)
	if err != nil {
		t.Fatalf("ListPRReviews failed: %v", err)
	}
	if len(reviews) != 2 {
		t.Errorf("expected 2 reviews, got %d", len(reviews))
	}
	// Should be in order
	if reviews[0].State != "APPROVED" || reviews[1].State != "CHANGES_REQUESTED" {
		t.Errorf("reviews not in expected order: %s, %s", reviews[0].State, reviews[1].State)
	}
}

// TestAddPRReview_RESTEventNormalization tests that REST API event values
// (APPROVE, REQUEST_CHANGES, COMMENT) are normalized to database states
// (APPROVED, CHANGES_REQUESTED, COMMENTED) when creating reviews.
// Regression test for issue #542.
func TestAddPRReview_RESTEventNormalization(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "normuser", "normrepo")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "normuser/normrepo",
		Title:        "PR for event normalization test",
		AuthorLogin:  "normuser",
		HeadRef:      "feat",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	tests := []struct {
		name          string
		inputEvent    string
		expectedState string
	}{
		{"APPROVE normalizes to APPROVED", "APPROVE", "APPROVED"},
		{"approve (lowercase) normalizes to APPROVED", "approve", "APPROVED"},
		{"REQUEST_CHANGES normalizes to CHANGES_REQUESTED", "REQUEST_CHANGES", "CHANGES_REQUESTED"},
		{"request_changes (lowercase) normalizes to CHANGES_REQUESTED", "request_changes", "CHANGES_REQUESTED"},
		{"COMMENT normalizes to COMMENTED", "COMMENT", "COMMENTED"},
		{"Already normalized APPROVED stays APPROVED", "APPROVED", "APPROVED"},
		{"Already normalized CHANGES_REQUESTED stays CHANGES_REQUESTED", "CHANGES_REQUESTED", "CHANGES_REQUESTED"},
		{"Already normalized COMMENTED stays COMMENTED", "COMMENTED", "COMMENTED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			review, err := svc.AddPRReview(ctx, pr.ID, "reviewer", tt.inputEvent, "test body", "sha123")
			if err != nil {
				t.Fatalf("AddPRReview failed: %v", err)
			}
			if review.State != tt.expectedState {
				t.Errorf("AddPRReview(%q) = state %q, want %q", tt.inputEvent, review.State, tt.expectedState)
			}
		})
	}
}

// TestSubmitPRReview_RESTEventNormalization tests that REST API event values
// are normalized when submitting pending reviews.
// Regression test for issue #542.
func TestSubmitPRReview_RESTEventNormalization(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "subuser", "subrepo")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "subuser/subrepo",
		Title:        "PR for submit normalization test",
		AuthorLogin:  "subuser",
		HeadRef:      "feat",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Create a pending review first
	pending, err := svc.AddPRReview(ctx, pr.ID, "reviewer", "PENDING", "", "sha123")
	if err != nil {
		t.Fatalf("AddPRReview(pending) failed: %v", err)
	}

	tests := []struct {
		name          string
		inputEvent    string
		expectedState string
	}{
		{"APPROVE normalizes to APPROVED", "APPROVE", "APPROVED"},
		{"REQUEST_CHANGES normalizes to CHANGES_REQUESTED", "REQUEST_CHANGES", "CHANGES_REQUESTED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			submitted, err := svc.SubmitPRReview(ctx, pending.ID, tt.inputEvent, "submitted")
			if err != nil {
				t.Fatalf("SubmitPRReview failed: %v", err)
			}
			if submitted.State != tt.expectedState {
				t.Errorf("SubmitPRReview(%q) = state %q, want %q", tt.inputEvent, submitted.State, tt.expectedState)
			}
		})
	}
}
