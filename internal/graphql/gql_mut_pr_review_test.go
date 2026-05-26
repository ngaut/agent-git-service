package graphql_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

// =============================================================================
// doRequestReviews Tests
// =============================================================================

// TestGraphQL_RequestReviews_Success tests successful reviewer assignment via requestReviews mutation.
func TestGraphQL_RequestReviews_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Migrate ReviewRequest table
	svc.DB.AutoMigrate(&db.ReviewRequest{})

	// Create a reviewer user
	reviewer := db.User{Login: "reviewer1", Name: "Reviewer One", Type: db.TypeUser}
	svc.DB.Create(&reviewer)

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "request-reviews-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "PR for review requests",
		Body:         "testing review requests",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)
	userNodeID := fmt.Sprintf("User_%d", reviewer.ID)

	q := `
	mutation($input: RequestReviewsInput!) {
		requestReviews(input: $input) {
			clientMutationId
			pullRequest {
				id
				number
				reviewRequests(first: 10) {
					nodes {
						requestedReviewer {
							... on User {
								login
							}
						}
					}
				}
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
			"userIds":       []string{userNodeID},
		},
	})

	rr := data["requestReviews"].(map[string]any)
	if rr["clientMutationId"] != "" {
		t.Errorf("clientMutationId: got %v, want empty string", rr["clientMutationId"])
	}

	prResult := rr["pullRequest"].(map[string]any)
	if prResult["number"] != float64(pr.Number) {
		t.Errorf("pullRequest.number: got %v, want %d", prResult["number"], pr.Number)
	}

	reviewReqs := prResult["reviewRequests"].(map[string]any)
	nodes := reviewReqs["nodes"].([]any)
	if len(nodes) < 1 {
		t.Fatal("expected at least 1 review request")
	}

	firstReq := nodes[0].(map[string]any)
	reviewerData := firstReq["requestedReviewer"].(map[string]any)
	if reviewerData["login"] != "reviewer1" {
		t.Errorf("requestedReviewer.login: got %v, want reviewer1", reviewerData["login"])
	}
}

// TestGraphQL_RequestReviews_ByLogin tests requestReviewsByLogin mutation with user logins.
func TestGraphQL_RequestReviews_ByLogin(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Migrate ReviewRequest table
	svc.DB.AutoMigrate(&db.ReviewRequest{})

	// Create reviewer users
	reviewer1 := db.User{Login: "reviewer-login1", Name: "Reviewer Login 1", Type: db.TypeUser}
	reviewer2 := db.User{Login: "reviewer-login2", Name: "Reviewer Login 2", Type: db.TypeUser}
	svc.DB.Create(&reviewer1)
	svc.DB.Create(&reviewer2)

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "request-reviews-login-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "PR for review by login",
		Body:         "testing review by login",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: RequestReviewsInput!) {
		requestReviewsByLogin(input: $input) {
			clientMutationId
			pullRequest {
				id
				reviewRequests(first: 10) {
					nodes {
						requestedReviewer {
							... on User {
								login
							}
						}
					}
				}
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
			"userLogins":    []string{"reviewer-login1", "reviewer-login2"},
		},
	})

	rr := data["requestReviewsByLogin"].(map[string]any)
	prResult := rr["pullRequest"].(map[string]any)
	reviewReqs := prResult["reviewRequests"].(map[string]any)
	nodes := reviewReqs["nodes"].([]any)

	if len(nodes) < 2 {
		t.Fatalf("expected 2 review requests, got %d", len(nodes))
	}

	// Collect logins to verify both reviewers are present
	logins := make(map[string]bool)
	for _, n := range nodes {
		req := n.(map[string]any)
		reviewerData := req["requestedReviewer"].(map[string]any)
		logins[reviewerData["login"].(string)] = true
	}

	if !logins["reviewer-login1"] {
		t.Error("expected reviewer-login1 in review requests")
	}
	if !logins["reviewer-login2"] {
		t.Error("expected reviewer-login2 in review requests")
	}
}

// TestGraphQL_RequestReviews_TeamSlug tests requesting team reviews via teamSlugs.
func TestGraphQL_RequestReviews_TeamSlug(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Migrate ReviewRequest table
	svc.DB.AutoMigrate(&db.ReviewRequest{})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "request-reviews-team-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "PR for team review",
		Body:         "testing team review requests",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: RequestReviewsInput!) {
		requestReviews(input: $input) {
			pullRequest {
				id
				reviewRequests(first: 10) {
					nodes {
						requestedReviewer {
							... on Team {
								name
							}
						}
					}
				}
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
			"teamSlugs":     []string{"core-team"},
		},
	})

	rr := data["requestReviews"].(map[string]any)
	prResult := rr["pullRequest"].(map[string]any)
	reviewReqs := prResult["reviewRequests"].(map[string]any)
	nodes := reviewReqs["nodes"].([]any)

	if len(nodes) < 1 {
		t.Fatal("expected at least 1 team review request")
	}

	firstReq := nodes[0].(map[string]any)
	teamData := firstReq["requestedReviewer"].(map[string]any)
	if teamData["name"] != "core-team" {
		t.Errorf("requestedReviewer.name: got %v, want core-team", teamData["name"])
	}
}

// TestGraphQL_RequestReviews_InvalidPRID tests request reviews fails with invalid PR ID.
func TestGraphQL_RequestReviews_InvalidPRID(t *testing.T) {
	_, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	q := `
	mutation($input: RequestReviewsInput!) {
		requestReviews(input: $input) {
			pullRequest {
				id
			}
		}
	}`

	// Use doRawGql since we expect the response may not have data
	res := doRawGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": "PullRequest_99999", // Non-existent PR
			"userLogins":    []string{"reviewer1"},
		},
	})

	// When PR doesn't exist, the mutation returns gracefully with empty pullRequest
	// Check if we have data (graceful handling) or errors
	if res["errors"] != nil {
		// Has errors - that's acceptable for invalid PR
		return
	}

	// Should return empty pullRequest with no errors (graceful handling)
	data, ok := res["data"].(map[string]any)
	if !ok {
		t.Fatal("expected data in response")
	}
	rr := data["requestReviews"].(map[string]any)
	prResult := rr["pullRequest"].(map[string]any)

	// PR should have the ID we provided but no review requests
	if prResult["id"] != "PullRequest_99999" {
		t.Errorf("pullRequest.id: got %v, want PullRequest_99999", prResult["id"])
	}

	reviewReqs := prResult["reviewRequests"].(map[string]any)
	nodes := reviewReqs["nodes"].([]any)
	if len(nodes) != 0 {
		t.Errorf("expected 0 review requests for invalid PR, got %d", len(nodes))
	}
}

// TestGraphQL_RequestReviews_MixedUserAndTeam tests requesting both user and team reviews.
func TestGraphQL_RequestReviews_MixedUserAndTeam(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Migrate ReviewRequest table
	svc.DB.AutoMigrate(&db.ReviewRequest{})

	// Create a reviewer user
	reviewer := db.User{Login: "mixed-reviewer", Name: "Mixed Reviewer", Type: db.TypeUser}
	svc.DB.Create(&reviewer)

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "request-reviews-mixed-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "PR for mixed review",
		Body:         "testing mixed review requests",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)
	userNodeID := fmt.Sprintf("User_%d", reviewer.ID)

	q := `
	mutation($input: RequestReviewsInput!) {
		requestReviews(input: $input) {
			pullRequest {
				id
				reviewRequests(first: 10) {
					totalCount
					nodes {
						requestedReviewer {
							... on User {
								login
							}
							... on Team {
								name
							}
						}
					}
				}
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
			"userIds":       []string{userNodeID},
			"teamSlugs":     []string{"qa-team"},
		},
	})

	rr := data["requestReviews"].(map[string]any)
	prResult := rr["pullRequest"].(map[string]any)
	reviewReqs := prResult["reviewRequests"].(map[string]any)
	nodes := reviewReqs["nodes"].([]any)

	if len(nodes) < 2 {
		t.Fatalf("expected 2 review requests (1 user + 1 team), got %d", len(nodes))
	}

	// Verify we have both user and team
	hasUser := false
	hasTeam := false
	for _, n := range nodes {
		req := n.(map[string]any)
		reviewerData := req["requestedReviewer"].(map[string]any)
		if login, ok := reviewerData["login"]; ok && login == "mixed-reviewer" {
			hasUser = true
		}
		if name, ok := reviewerData["name"]; ok && name == "qa-team" {
			hasTeam = true
		}
	}

	if !hasUser {
		t.Error("expected user reviewer in mixed request")
	}
	if !hasTeam {
		t.Error("expected team reviewer in mixed request")
	}
}

// =============================================================================
// doResolveReviewThread Tests
// =============================================================================

// TestGraphQL_ResolveReviewThread_Success tests resolving a review thread.
func TestGraphQL_ResolveReviewThread_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.AutoMigrate(&db.PullRequestReview{}, &db.PRReviewComment{})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "resolve-thread-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "PR for thread resolve",
		Body:         "testing thread resolution",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	// Create a PR review comment (thread root)
	comment := db.PRReviewComment{
		PullRequestID: pr.ID,
		AuthorLogin:   u.Login,
		Body:          "Review comment",
		Path:          "file.go",
		Line:          10,
		IsResolved:    false,
	}
	svc.DB.Create(&comment)

	threadNodeID := fmt.Sprintf("PRReviewComment_%d", comment.ID)

	q := `
	mutation($input: ResolveReviewThreadInput!) {
		resolveReviewThread(input: $input) {
			thread {
				id
				isResolved
				viewerCanResolve
				viewerCanUnresolve
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"threadId": threadNodeID,
		},
	})

	result := data["resolveReviewThread"].(map[string]any)
	threadResult := result["thread"].(map[string]any)

	if threadResult["id"] != threadNodeID {
		t.Errorf("thread.id: got %v, want %s", threadResult["id"], threadNodeID)
	}
	if threadResult["isResolved"] != true {
		t.Errorf("thread.isResolved: got %v, want true", threadResult["isResolved"])
	}
	if threadResult["viewerCanResolve"] != false {
		t.Errorf("thread.viewerCanResolve: got %v, want false", threadResult["viewerCanResolve"])
	}
	if threadResult["viewerCanUnresolve"] != true {
		t.Errorf("thread.viewerCanUnresolve: got %v, want true", threadResult["viewerCanUnresolve"])
	}

	// Verify comment was actually resolved in DB
	var updatedComment db.PRReviewComment
	if err := svc.DB.First(&updatedComment, comment.ID).Error; err != nil {
		t.Fatalf("failed to fetch updated comment: %v", err)
	}
	if !updatedComment.IsResolved {
		t.Error("comment should be resolved in database")
	}
}

// TestGraphQL_UnresolveReviewThread_Success tests unresolving a review thread.
func TestGraphQL_UnresolveReviewThread_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.AutoMigrate(&db.PullRequestReview{}, &db.PRReviewComment{})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "unresolve-thread-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "PR for thread unresolve",
		Body:         "testing thread unresolution",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	// Create a resolved PR review comment (thread root)
	comment := db.PRReviewComment{
		PullRequestID: pr.ID,
		AuthorLogin:   u.Login,
		Body:          "Review comment",
		Path:          "file.go",
		Line:          10,
		IsResolved:    true,
	}
	svc.DB.Create(&comment)

	threadNodeID := fmt.Sprintf("PRReviewComment_%d", comment.ID)

	q := `
	mutation($input: UnresolveReviewThreadInput!) {
		unresolveReviewThread(input: $input) {
			thread {
				id
				isResolved
				viewerCanResolve
				viewerCanUnresolve
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"threadId": threadNodeID,
		},
	})

	result := data["unresolveReviewThread"].(map[string]any)
	threadResult := result["thread"].(map[string]any)

	if threadResult["id"] != threadNodeID {
		t.Errorf("thread.id: got %v, want %s", threadResult["id"], threadNodeID)
	}
	if threadResult["isResolved"] != false {
		t.Errorf("thread.isResolved: got %v, want false", threadResult["isResolved"])
	}
	if threadResult["viewerCanResolve"] != true {
		t.Errorf("thread.viewerCanResolve: got %v, want true", threadResult["viewerCanResolve"])
	}
	if threadResult["viewerCanUnresolve"] != false {
		t.Errorf("thread.viewerCanUnresolve: got %v, want false", threadResult["viewerCanUnresolve"])
	}

	// Verify comment was actually unresolved in DB
	var updatedComment db.PRReviewComment
	if err := svc.DB.First(&updatedComment, comment.ID).Error; err != nil {
		t.Fatalf("failed to fetch updated comment: %v", err)
	}
	if updatedComment.IsResolved {
		t.Error("comment should be unresolved in database")
	}
}

// TestGraphQL_ResolveReviewThread_InvalidThreadID tests resolve with non-existent thread ID.
// Note: The service layer uses GORM Update which doesn't error on non-existent records.
// This test verifies the mutation handles the case gracefully.
func TestGraphQL_ResolveReviewThread_InvalidThreadID(t *testing.T) {
	svc, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	// Migrate PRReviewComment table
	svc.DB.AutoMigrate(&db.PRReviewComment{})

	q := `
	mutation($input: ResolveReviewThreadInput!) {
		resolveReviewThread(input: $input) {
			thread {
				id
				isResolved
			}
		}
	}`

	// Use a prefix that won't parse (invalid format)
	res := doRawGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"threadId": "InvalidThread_99999", // Invalid prefix - won't parse
		},
	})

	// Should have errors for truly invalid thread ID format
	if res["errors"] == nil {
		t.Error("expected errors for invalid thread ID format, got none")
	}

	msg := firstGQLErrorMessage(t, res)
	if !strings.Contains(strings.ToLower(msg), "invalid thread") {
		t.Errorf("expected 'invalid thread' error, got %q", msg)
	}
}

// TestGraphQL_ResolveReviewThread_PRReviewCommentID tests resolve with PRReviewComment prefix.
func TestGraphQL_ResolveReviewThread_PRReviewCommentID(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.AutoMigrate(&db.PullRequestReview{}, &db.PRReviewComment{})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "resolve-comment-thread-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "PR for comment thread",
		Body:         "testing comment thread resolution",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	// Create a PR review comment (thread root)
	comment := db.PRReviewComment{
		PullRequestID: pr.ID,
		AuthorLogin:   u.Login,
		Body:          "Review comment",
		Path:          "file.go",
		Line:          10,
		IsResolved:    false,
	}
	svc.DB.Create(&comment)

	// Use PRReviewComment prefix (which is correct for this model)
	threadNodeID := fmt.Sprintf("PRReviewComment_%d", comment.ID)

	q := `
	mutation($input: ResolveReviewThreadInput!) {
		resolveReviewThread(input: $input) {
			thread {
				id
				isResolved
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"threadId": threadNodeID,
		},
	})

	result := data["resolveReviewThread"].(map[string]any)
	threadResult := result["thread"].(map[string]any)

	if threadResult["isResolved"] != true {
		t.Errorf("thread.isResolved: got %v, want true", threadResult["isResolved"])
	}
}

// TestGraphQL_ResolveReviewThread_ResponseFieldShape tests the complete response field shape.
func TestGraphQL_ResolveReviewThread_ResponseFieldShape(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.AutoMigrate(&db.PullRequestReview{}, &db.PRReviewComment{})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "resolve-thread-shape-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "PR for thread shape test",
		Body:         "testing response shape",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	comment := db.PRReviewComment{
		PullRequestID: pr.ID,
		AuthorLogin:   u.Login,
		Body:          "Review comment",
		Path:          "file.go",
		Line:          10,
		IsResolved:    false,
	}
	svc.DB.Create(&comment)

	threadNodeID := fmt.Sprintf("PRReviewComment_%d", comment.ID)

	q := `
	mutation($input: ResolveReviewThreadInput!) {
		resolveReviewThread(input: $input) {
			thread {
				id
				isResolved
				viewerCanResolve
				viewerCanUnresolve
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"threadId": threadNodeID,
		},
	})

	// Verify response structure
	result, ok := data["resolveReviewThread"].(map[string]any)
	if !ok {
		t.Fatal("resolveReviewThread should be a map")
	}

	threadResult, ok := result["thread"].(map[string]any)
	if !ok {
		t.Fatal("thread should be a map")
	}

	// Verify all expected fields exist with correct types
	expectedFields := map[string]string{
		"id":                 "string",
		"isResolved":         "bool",
		"viewerCanResolve":   "bool",
		"viewerCanUnresolve": "bool",
	}

	for field, expectedType := range expectedFields {
		if _, exists := threadResult[field]; !exists {
			t.Errorf("thread missing field: %s", field)
			continue
		}

		switch expectedType {
		case "string":
			if _, ok := threadResult[field].(string); !ok {
				t.Errorf("thread.%s should be string, got %T", field, threadResult[field])
			}
		case "bool":
			if _, ok := threadResult[field].(bool); !ok {
				t.Errorf("thread.%s should be bool, got %T", field, threadResult[field])
			}
		}
	}
}

// =============================================================================
// doUpdatePR Tests - Title, Body, Base Updates
// =============================================================================

// TestGraphQL_UpdatePR_Title_Success tests updating PR title.
func TestGraphQL_UpdatePR_Title_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-pr-title-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Original Title",
		Body:         "original body",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: UpdatePullRequestInput!) {
		updatePullRequest(input: $input) {
			pullRequest {
				id
				title
				body
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
			"title":         "Updated Title",
		},
	})

	result := data["updatePullRequest"].(map[string]any)
	prResult := result["pullRequest"].(map[string]any)

	if prResult["title"] != "Updated Title" {
		t.Errorf("title: got %v, want 'Updated Title'", prResult["title"])
	}
	if prResult["body"] != "original body" {
		t.Errorf("body should remain unchanged: got %v", prResult["body"])
	}

	// Verify in DB
	updatedPR, err := svc.GetPRByID(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if updatedPR.Title != "Updated Title" {
		t.Errorf("DB title: got %v, want 'Updated Title'", updatedPR.Title)
	}
}

// TestGraphQL_UpdatePR_Body_Success tests updating PR body.
func TestGraphQL_UpdatePR_Body_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-pr-body-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "PR Body Update",
		Body:         "original body",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: UpdatePullRequestInput!) {
		updatePullRequest(input: $input) {
			pullRequest {
				id
				title
				body
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
			"body":          "updated body with new content",
		},
	})

	result := data["updatePullRequest"].(map[string]any)
	prResult := result["pullRequest"].(map[string]any)

	if prResult["body"] != "updated body with new content" {
		t.Errorf("body: got %v, want 'updated body with new content'", prResult["body"])
	}
	if prResult["title"] != "PR Body Update" {
		t.Errorf("title should remain unchanged: got %v", prResult["title"])
	}

	// Verify in DB
	updatedPR, err := svc.GetPRByID(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if updatedPR.Body != "updated body with new content" {
		t.Errorf("DB body: got %v, want 'updated body with new content'", updatedPR.Body)
	}
}

// TestGraphQL_UpdatePR_BaseRef_Success tests updating PR base branch.
func TestGraphQL_UpdatePR_BaseRef_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-pr-base-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	// Create develop branch
	if err := svc.Git.CreateBranch(ctx, repo.FullName, "develop", "main"); err != nil {
		t.Fatalf("CreateBranch(develop): %v", err)
	}
	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch(feature): %v", err)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "PR Base Update",
		Body:         "changing base branch",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: UpdatePullRequestInput!) {
		updatePullRequest(input: $input) {
			pullRequest {
				id
				baseRefName
				headRefName
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
			"baseRefName":   "develop",
		},
	})

	result := data["updatePullRequest"].(map[string]any)
	prResult := result["pullRequest"].(map[string]any)

	if prResult["baseRefName"] != "develop" {
		t.Errorf("baseRefName: got %v, want 'develop'", prResult["baseRefName"])
	}
	if prResult["headRefName"] != "feature" {
		t.Errorf("headRefName should remain unchanged: got %v", prResult["headRefName"])
	}

	// Verify in DB
	updatedPR, err := svc.GetPRByID(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if updatedPR.BaseRef != "develop" {
		t.Errorf("DB base_ref: got %v, want 'develop'", updatedPR.BaseRef)
	}
}

// TestGraphQL_UpdatePR_InvalidPRID tests update fails gracefully with invalid PR ID.
func TestGraphQL_UpdatePR_InvalidPRID(t *testing.T) {
	_, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	q := `
	mutation($input: UpdatePullRequestInput!) {
		updatePullRequest(input: $input) {
			pullRequest {
				id
			}
		}
	}`

	// Use doRawGql since we expect errors for invalid PR
	res := doRawGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": "PullRequest_99999",
			"title":         "New Title",
		},
	})

	// Should have errors for invalid PR ID
	if res["errors"] == nil {
		t.Error("expected errors for invalid PR ID, got none")
	}
}

// TestGraphQL_UpdatePR_LabelsAndAssignees tests attaching labels and assignees.
func TestGraphQL_UpdatePR_LabelsAndAssignees(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.AutoMigrate(&db.Label{})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-pr-labels-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "PR with labels",
		Body:         "testing labels and assignees",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	// Create the "bug" label explicitly for the repo.
	label, err := svc.CreateLabel(ctx, repo.FullName, "bug", "d73a4a", "Something isn't working")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)
	labelNodeID := fmt.Sprintf("Label_%d", label.ID)

	q := `
	mutation($input: UpdatePullRequestInput!) {
		updatePullRequest(input: $input) {
			pullRequest {
				id
				labels(first: 10) {
					nodes {
						id
						name
					}
				}
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
			"labelIds":      []string{labelNodeID},
		},
	})

	result := data["updatePullRequest"].(map[string]any)
	prResult := result["pullRequest"].(map[string]any)

	labels := prResult["labels"].(map[string]any)
	nodes := labels["nodes"].([]any)

	if len(nodes) < 1 {
		t.Fatalf("expected at least 1 label, got %d", len(nodes))
	}

	firstLabel := nodes[0].(map[string]any)
	if firstLabel["name"] != "bug" {
		t.Errorf("label name: got %v, want 'bug'", firstLabel["name"])
	}
}

// =============================================================================
// doSetPRDraft Tests - Draft Toggles
// =============================================================================

// TestGraphQL_MarkPRReadyForReview_Success tests marking a draft PR as ready for review.
func TestGraphQL_MarkPRReadyForReview_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "mark-ready-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	// Create a draft PR
	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Draft PR",
		Body:         "will be marked ready",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
		Draft:        true,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	// Verify it's a draft
	if !pr.Draft {
		t.Fatal("PR should be created as draft")
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: MarkPullRequestReadyForReviewInput!) {
		markPullRequestReadyForReview(input: $input) {
			pullRequest {
				id
				isDraft
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
		},
	})

	result := data["markPullRequestReadyForReview"].(map[string]any)
	prResult := result["pullRequest"].(map[string]any)

	if prResult["isDraft"] != false {
		t.Errorf("isDraft: got %v, want false", prResult["isDraft"])
	}

	// Verify in DB
	updatedPR, err := svc.GetPRByID(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if updatedPR.Draft {
		t.Error("DB should show PR as not draft")
	}
}

// TestGraphQL_ConvertPRToDraft_Success tests converting a ready PR to draft.
func TestGraphQL_ConvertPRToDraft_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "convert-draft-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	// Create a non-draft PR
	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Ready PR",
		Body:         "will be converted to draft",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
		Draft:        false,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	// Verify it's not a draft
	if pr.Draft {
		t.Fatal("PR should be created as non-draft")
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: ConvertPullRequestToDraftInput!) {
		convertPullRequestToDraft(input: $input) {
			pullRequest {
				id
				isDraft
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
		},
	})

	result := data["convertPullRequestToDraft"].(map[string]any)
	prResult := result["pullRequest"].(map[string]any)

	if prResult["isDraft"] != true {
		t.Errorf("isDraft: got %v, want true", prResult["isDraft"])
	}

	// Verify in DB
	updatedPR, err := svc.GetPRByID(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if !updatedPR.Draft {
		t.Error("DB should show PR as draft")
	}
}

// TestGraphQL_MarkPRReadyForReview_InvalidPRID tests mark ready fails gracefully with invalid PR ID.
func TestGraphQL_MarkPRReadyForReview_InvalidPRID(t *testing.T) {
	_, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	q := `
	mutation($input: MarkPullRequestReadyForReviewInput!) {
		markPullRequestReadyForReview(input: $input) {
			pullRequest {
				id
				isDraft
			}
		}
	}`

	// Use doRawGql since we expect errors for invalid PR
	res := doRawGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": "PullRequest_99999",
		},
	})

	// Should have errors for invalid PR ID
	if res["errors"] == nil {
		t.Error("expected errors for invalid PR ID, got none")
	}
}

// TestGraphQL_ConvertPRToDraft_InvalidPRID tests convert to draft fails gracefully with invalid PR ID.
func TestGraphQL_ConvertPRToDraft_InvalidPRID(t *testing.T) {
	_, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	q := `
	mutation($input: ConvertPullRequestToDraftInput!) {
		convertPullRequestToDraft(input: $input) {
			pullRequest {
				id
				isDraft
			}
		}
	}`

	// Use doRawGql since we expect errors for invalid PR
	res := doRawGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": "PullRequest_99999",
		},
	})

	// Should have errors for invalid PR ID
	if res["errors"] == nil {
		t.Error("expected errors for invalid PR ID, got none")
	}
}

// TestGraphQL_DraftToggle_ResponseFieldShape tests the response field shape for draft toggles.
// Note: doSetPRDraft returns minimal response with just isDraft field.
func TestGraphQL_DraftToggle_ResponseFieldShape(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "draft-shape-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Shape Test PR",
		Body:         "testing response shape",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
		Draft:        true,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: MarkPullRequestReadyForReviewInput!) {
		markPullRequestReadyForReview(input: $input) {
			pullRequest {
				isDraft
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
		},
	})

	// Verify response structure
	result, ok := data["markPullRequestReadyForReview"].(map[string]any)
	if !ok {
		t.Fatal("markPullRequestReadyForReview should be a map")
	}

	prResult, ok := result["pullRequest"].(map[string]any)
	if !ok {
		t.Fatal("pullRequest should be a map")
	}

	// Verify isDraft field exists with correct type (main field returned by doSetPRDraft)
	if _, exists := prResult["isDraft"]; !exists {
		t.Error("pullRequest missing field: isDraft")
	} else if _, ok := prResult["isDraft"].(bool); !ok {
		t.Errorf("pullRequest.isDraft should be bool, got %T", prResult["isDraft"])
	}

	// Verify the draft status changed from true to false
	if prResult["isDraft"] != false {
		t.Errorf("isDraft: got %v, want false (marked as ready for review)", prResult["isDraft"])
	}
}
