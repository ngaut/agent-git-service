package graphql_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

// =============================================================================
// doCreatePR Tests
// =============================================================================

// TestGraphQL_CreatePRMutation_Success tests successful PR creation via GraphQL mutation.
func TestGraphQL_CreatePRMutation_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Create source and target repos (same repo for simple case)
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "pr-create-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	// Create a feature branch
	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature-branch", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	repoNodeID := fmt.Sprintf("Repository_%d", repo.ID)

	q := `
	mutation($input: CreatePullRequestInput!) {
		createPullRequest(input: $input) {
			pullRequest {
				id
				number
				title
				body
				state
				headRefName
				baseRefName
				author { login }
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"repositoryId":        repoNodeID,
			"title":               "Add new feature",
			"body":                "This PR adds a new feature",
			"headRefName":         "feature-branch",
			"baseRefName":         "main",
			"maintainerCanModify": true,
		},
	})

	cp := data["createPullRequest"].(map[string]any)
	pr := cp["pullRequest"].(map[string]any)

	if pr["title"] != "Add new feature" {
		t.Errorf("title: got %v, want 'Add new feature'", pr["title"])
	}
	if pr["body"] != "This PR adds a new feature" {
		t.Errorf("body: got %v, want 'This PR adds a new feature'", pr["body"])
	}
	if pr["state"] != "OPEN" {
		t.Errorf("state: got %v, want OPEN", pr["state"])
	}
	if pr["headRefName"] != "feature-branch" {
		t.Errorf("headRefName: got %v, want feature-branch", pr["headRefName"])
	}
	if pr["baseRefName"] != "main" {
		t.Errorf("baseRefName: got %v, want main", pr["baseRefName"])
	}
	author := pr["author"].(map[string]any)
	if author["login"] != "tester" {
		t.Errorf("author.login: got %v, want tester", author["login"])
	}
	if pr["number"] == nil || pr["number"].(float64) < 1 {
		t.Errorf("number should be >= 1, got %v", pr["number"])
	}
}

// TestGraphQL_CreatePRMutation_ValidationFailure_EmptyTitle tests PR creation with empty title.
// Note: The service allows empty titles but logs a warning; the PR is still created.
func TestGraphQL_CreatePRMutation_ValidationFailure_EmptyTitle(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "pr-validation-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main")

	repoNodeID := fmt.Sprintf("Repository_%d", repo.ID)

	q := `
	mutation($input: CreatePullRequestInput!) {
		createPullRequest(input: $input) {
			pullRequest {
				id
				title
				number
			}
		}
	}`

	// Empty title - service creates PR but logs warning
	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"repositoryId": repoNodeID,
			"title":        "",
			"body":         "body without title",
			"headRefName":  "feature",
			"baseRefName":  "main",
		},
	})

	cp := data["createPullRequest"].(map[string]any)
	pr := cp["pullRequest"].(map[string]any)

	// PR is created even with empty title
	if pr["number"] == nil || pr["number"].(float64) < 1 {
		t.Errorf("PR should be created with number >= 1, got %v", pr["number"])
	}
}

// TestGraphQL_CreatePRMutation_ValidationFailure_InvalidRepo tests PR creation fails with invalid repo ID.
func TestGraphQL_CreatePRMutation_ValidationFailure_InvalidRepo(t *testing.T) {
	_, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	q := `
	mutation($input: CreatePullRequestInput!) {
		createPullRequest(input: $input) {
			pullRequest {
				id
				title
			}
		}
	}`

	res := doRawGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"repositoryId": "Repository_99999", // Non-existent repo
			"title":        "Test PR",
			"body":         "test body",
			"headRefName":  "feature",
			"baseRefName":  "main",
		},
	})

	// Should have errors for invalid repo
	if res["errors"] == nil {
		t.Error("expected errors for invalid repository ID, got none")
	}
}

// TestGraphQL_CreatePRMutation_ValidationFailure_SameRef tests PR creation fails when head == base.
func TestGraphQL_CreatePRMutation_ValidationFailure_SameRef(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "pr-sameref-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	repoNodeID := fmt.Sprintf("Repository_%d", repo.ID)

	q := `
	mutation($input: CreatePullRequestInput!) {
		createPullRequest(input: $input) {
			pullRequest {
				id
				title
			}
		}
	}`

	res := doRawGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"repositoryId": repoNodeID,
			"title":        "Same ref PR",
			"body":         "head and base are the same",
			"headRefName":  "main",
			"baseRefName":  "main",
		},
	})

	// Should have errors for same head/base
	if res["errors"] == nil {
		t.Error("expected errors for same head and base ref, got none")
	}
}

// TestGraphQL_CreatePRMutation_PermissionFailure_Unauthorized verifies that
// PR creation does not reveal repositories the viewer cannot access.
func TestGraphQL_CreatePRMutation_PermissionFailure_Unauthorized(t *testing.T) {
	svc, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	other := db.User{Login: "otheruser", Name: "Other User", Type: db.TypeUser}
	svc.DB.Create(&other)
	svc.DB.Create(&db.Token{UserID: other.ID, Value: "test-token-other"})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: other.Login,
		Name:       "other-pr-repo",
		Private:    true,
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	var before int64
	if err := svc.DB.Model(&db.PullRequest{}).Where("repository_id = ?", repo.ID).Count(&before).Error; err != nil {
		t.Fatalf("Count PRs: %v", err)
	}

	repoNodeID := fmt.Sprintf("Repository_%d", repo.ID)
	q := `
	mutation($input: CreatePullRequestInput!) {
		createPullRequest(input: $input) {
			pullRequest {
				id
				title
			}
		}
	}`

	res := doRawGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"repositoryId": repoNodeID,
			"title":        "Unauthorized PR",
			"body":         "should not be created",
			"headRefName":  "feature",
			"baseRefName":  "main",
		},
	})

	msg := firstGQLErrorMessage(t, res)
	if !strings.Contains(strings.ToLower(msg), "not found") {
		t.Fatalf("expected not-found error, got %q", msg)
	}

	var after int64
	if err := svc.DB.Model(&db.PullRequest{}).Where("repository_id = ?", repo.ID).Count(&after).Error; err != nil {
		t.Fatalf("Count PRs: %v", err)
	}
	if after != before {
		t.Fatalf("expected no PR created, count before %d after %d", before, after)
	}
}

// doRawGqlWithToken is a helper to execute GraphQL with a custom token.
func doRawGqlWithToken(t *testing.T, mux http.Handler, query string, args map[string]any) map[string]any {
	t.Helper()

	token := "token test-token"
	if args["token"] != nil {
		token = "token " + args["token"].(string)
		delete(args, "token")
	}

	reqBody := map[string]any{"query": query}
	if args["input"] != nil {
		reqBody["variables"] = map[string]any{"input": args["input"]}
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("gql marshal: %v", err)
	}

	req := httptest.NewRequest("POST", "/graphql", bytes.NewReader(b))
	req.Header.Set("Authorization", token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("gql json decode: %v", err)
	}
	return res
}

// =============================================================================
// Review Mutation Tests (doAddPRReview)
// =============================================================================

// TestGraphQL_AddPRReview_Approve tests submitting an APPROVE review.
func TestGraphQL_AddPRReview_Approve(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.AutoMigrate(&db.PullRequestReview{})

	repo, _ := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "review-approve-repo",
		AutoInit:   true,
	})
	svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Review Test PR",
		Body:         "testing reviews",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: AddPullRequestReviewInput!) {
		addPullRequestReview(input: $input) {
			pullRequestReview {
				id
				body
				state
				author { login }
				authorAssociation
				commit { oid }
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
			"event":         "APPROVE",
			"body":          "LGTM! Ready to merge.",
		},
	})

	review := data["addPullRequestReview"].(map[string]any)["pullRequestReview"].(map[string]any)

	// State is returned as the input event value (APPROVE), not the DB constant
	if review["state"] != "APPROVE" {
		t.Errorf("state: got %v, want APPROVE", review["state"])
	}
	if review["body"] != "LGTM! Ready to merge." {
		t.Errorf("body: got %v, want 'LGTM! Ready to merge.'", review["body"])
	}
	author := review["author"].(map[string]any)
	if author["login"] != "tester" {
		t.Errorf("author.login: got %v, want tester", author["login"])
	}
	if review["authorAssociation"] != "OWNER" {
		t.Errorf("authorAssociation: got %v, want OWNER", review["authorAssociation"])
	}
}

// TestGraphQL_AddPRReview_RequestChanges tests submitting a CHANGES_REQUESTED review.
func TestGraphQL_AddPRReview_RequestChanges(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.AutoMigrate(&db.PullRequestReview{})

	repo, _ := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "review-changes-repo",
		AutoInit:   true,
	})
	svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Needs Changes PR",
		Body:         "needs work",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: AddPullRequestReviewInput!) {
		addPullRequestReview(input: $input) {
			pullRequestReview {
				id
				body
				state
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
			"event":         "REQUEST_CHANGES",
			"body":          "Please fix the following issues...",
		},
	})

	review := data["addPullRequestReview"].(map[string]any)["pullRequestReview"].(map[string]any)

	// State is returned as the input event value (REQUEST_CHANGES)
	if review["state"] != "REQUEST_CHANGES" {
		t.Errorf("state: got %v, want REQUEST_CHANGES", review["state"])
	}
	if review["body"] != "Please fix the following issues..." {
		t.Errorf("body: got %v, want 'Please fix the following issues...'", review["body"])
	}
}

// TestGraphQL_AddPRReview_Comment tests submitting a COMMENT review.
func TestGraphQL_AddPRReview_Comment(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.AutoMigrate(&db.PullRequestReview{})

	repo, _ := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "review-comment-repo",
		AutoInit:   true,
	})
	svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Comment Review PR",
		Body:         "just commenting",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: AddPullRequestReviewInput!) {
		addPullRequestReview(input: $input) {
			pullRequestReview {
				id
				body
				state
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
			"event":         "COMMENT",
			"body":          "Just a comment, not approving or requesting changes.",
		},
	})

	review := data["addPullRequestReview"].(map[string]any)["pullRequestReview"].(map[string]any)

	// State is returned as the input event value (COMMENT)
	if review["state"] != "COMMENT" {
		t.Errorf("state: got %v, want COMMENT", review["state"])
	}
	if review["body"] != "Just a comment, not approving or requesting changes." {
		t.Errorf("body: got %v, want 'Just a comment, not approving or requesting changes.'", review["body"])
	}
}

// TestGraphQL_AddPRReview_InvalidPRID tests review submission fails with invalid PR ID.
func TestGraphQL_AddPRReview_InvalidPRID(t *testing.T) {
	_, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	q := `
	mutation($input: AddPullRequestReviewInput!) {
		addPullRequestReview(input: $input) {
			pullRequestReview {
				id
			}
		}
	}`

	res := doRawGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": "PullRequest_99999", // Non-existent PR
			"event":         "APPROVE",
			"body":          "test",
		},
	})

	if res["errors"] == nil {
		t.Error("expected errors for invalid PR ID, got none")
	}
}

// =============================================================================
// Update Pull Request Tests (doUpdatePR)
// =============================================================================

// TestGraphQL_UpdatePullRequest_Milestone_Set tests setting a milestone on a PR.
func TestGraphQL_UpdatePullRequest_Milestone_Set(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-pr-milestone-set",
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
		Title:        "PR with milestone",
		Body:         "set milestone",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	milestone, err := svc.CreateMilestone(ctx, repo.FullName, "Milestone 1", "desc", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}

	mut := `
	mutation($input: UpdatePullRequestInput!) {
		updatePullRequest(input: $input) {
			pullRequest {
				id
				milestone {
					id
					title
					number
				}
			}
		}
	}`

	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"pullRequestId": fmt.Sprintf("PullRequest_%d", pr.ID),
			"milestoneId":   fmt.Sprintf("Milestone_%d", milestone.ID),
		},
	})

	upd := data["updatePullRequest"].(map[string]any)
	prData := upd["pullRequest"].(map[string]any)

	if prData["milestone"] == nil {
		t.Fatal("milestone should be set")
	}
	ms := prData["milestone"].(map[string]any)
	if ms["id"] != fmt.Sprintf("Milestone_%d", milestone.ID) {
		t.Errorf("milestone.id: got %v, want Milestone_%d", ms["id"], milestone.ID)
	}
	if ms["title"] != "Milestone 1" {
		t.Errorf("milestone.title: got %v, want Milestone 1", ms["title"])
	}
	if ms["number"] != float64(milestone.Number) {
		t.Errorf("milestone.number: got %v, want %d", ms["number"], milestone.Number)
	}

	stored, err := svc.GetPRByID(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if stored.MilestoneID == nil || *stored.MilestoneID != milestone.ID {
		t.Fatalf("stored milestone ID: got %v, want %d", stored.MilestoneID, milestone.ID)
	}
}

// TestGraphQL_UpdatePullRequest_Milestone_Clear tests clearing a milestone via null milestoneId.
func TestGraphQL_UpdatePullRequest_Milestone_Clear(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-pr-milestone-clear",
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
		Title:        "PR milestone clear",
		Body:         "clear milestone",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	milestone, err := svc.CreateMilestone(ctx, repo.FullName, "Milestone Clear", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}
	if err := svc.SetPRMilestone(ctx, pr.ID, &milestone.ID); err != nil {
		t.Fatalf("SetPRMilestone: %v", err)
	}

	mut := `
	mutation($input: UpdatePullRequestInput!) {
		updatePullRequest(input: $input) {
			pullRequest {
				id
				milestone { id title }
			}
		}
	}`

	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"pullRequestId": fmt.Sprintf("PullRequest_%d", pr.ID),
			"milestoneId":   nil,
		},
	})

	upd := data["updatePullRequest"].(map[string]any)
	prData := upd["pullRequest"].(map[string]any)
	if prData["milestone"] != nil {
		t.Errorf("milestone should be nil after clear, got %v", prData["milestone"])
	}

	stored, err := svc.GetPRByID(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if stored.MilestoneID != nil {
		t.Fatalf("stored milestone ID: got %v, want nil", *stored.MilestoneID)
	}
}

// TestGraphQL_UpdatePullRequest_Milestone_Omitted tests milestone remains unchanged when omitted.
func TestGraphQL_UpdatePullRequest_Milestone_Omitted(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-pr-milestone-omit",
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
		Title:        "PR milestone omit",
		Body:         "omit milestone update",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	milestone, err := svc.CreateMilestone(ctx, repo.FullName, "Milestone Omit", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}
	if err := svc.SetPRMilestone(ctx, pr.ID, &milestone.ID); err != nil {
		t.Fatalf("SetPRMilestone: %v", err)
	}

	mut := `
	mutation($input: UpdatePullRequestInput!) {
		updatePullRequest(input: $input) {
			pullRequest {
				id
				title
				milestone { id title }
			}
		}
	}`

	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"pullRequestId": fmt.Sprintf("PullRequest_%d", pr.ID),
			"title":         "PR milestone omit updated",
		},
	})

	upd := data["updatePullRequest"].(map[string]any)
	prData := upd["pullRequest"].(map[string]any)
	msAny := prData["milestone"]
	if msAny == nil {
		t.Fatal("milestone should remain set when omitted")
	}
	ms := msAny.(map[string]any)
	if ms["id"] != fmt.Sprintf("Milestone_%d", milestone.ID) {
		t.Errorf("milestone.id: got %v, want Milestone_%d", ms["id"], milestone.ID)
	}
	if ms["title"] != "Milestone Omit" {
		t.Errorf("milestone.title: got %v, want Milestone Omit", ms["title"])
	}

	stored, err := svc.GetPRByID(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if stored.MilestoneID == nil || *stored.MilestoneID != milestone.ID {
		t.Fatalf("stored milestone ID: got %v, want %d", stored.MilestoneID, milestone.ID)
	}
}

// TestGraphQL_UpdatePullRequest_Milestone_InvalidID tests invalid milestone IDs error out.
func TestGraphQL_UpdatePullRequest_Milestone_InvalidID(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-pr-milestone-invalid",
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
		Title:        "PR milestone invalid",
		Body:         "invalid milestone",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	mut := `
	mutation($input: UpdatePullRequestInput!) {
		updatePullRequest(input: $input) {
			pullRequest { id }
		}
	}`

	res := doRawGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"pullRequestId": fmt.Sprintf("PullRequest_%d", pr.ID),
			"milestoneId":   "NotAMilestone",
		},
	})

	msg := firstGQLErrorMessage(t, res)
	if !strings.Contains(strings.ToLower(msg), "milestone") {
		t.Fatalf("expected milestone error, got %q", msg)
	}

	stored, err := svc.GetPRByID(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if stored.MilestoneID != nil {
		t.Fatalf("stored milestone ID: got %v, want nil", *stored.MilestoneID)
	}
}

// TestGraphQL_UpdatePullRequest_Milestone_CrossRepo tests cross-repo milestone assignment is rejected.
func TestGraphQL_UpdatePullRequest_Milestone_CrossRepo(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-pr-milestone-cross-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	otherRepo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-pr-milestone-other",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo(other): %v", err)
	}
	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "PR milestone cross repo",
		Body:         "cross repo milestone",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	otherMilestone, err := svc.CreateMilestone(ctx, otherRepo.FullName, "Other Repo MS", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone(other): %v", err)
	}

	mut := `
	mutation($input: UpdatePullRequestInput!) {
		updatePullRequest(input: $input) {
			pullRequest { id }
		}
	}`

	res := doRawGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"pullRequestId": fmt.Sprintf("PullRequest_%d", pr.ID),
			"milestoneId":   fmt.Sprintf("Milestone_%d", otherMilestone.ID),
		},
	})

	msg := firstGQLErrorMessage(t, res)
	if !strings.Contains(strings.ToLower(msg), "milestone does not belong") {
		t.Fatalf("expected cross-repo milestone error, got %q", msg)
	}

	stored, err := svc.GetPRByID(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if stored.MilestoneID != nil {
		t.Fatalf("stored milestone ID: got %v, want nil", *stored.MilestoneID)
	}
}

// =============================================================================
// Auto-Merge Toggle Tests (doSetAutoMerge)
// =============================================================================

// TestGraphQL_EnablePullRequestAutoMerge_Success tests enabling auto-merge on a PR.
func TestGraphQL_EnablePullRequestAutoMerge_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, _ := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "auto-merge-repo",
		AutoInit:   true,
	})
	// Enable auto-merge on repo
	svc.DB.Model(&db.Repository{}).Where("id = ?", repo.ID).Update("allow_auto_merge", true)

	svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Auto-merge PR",
		Body:         "will auto-merge",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: EnablePullRequestAutoMergeInput!) {
		enablePullRequestAutoMerge(input: $input) {
			pullRequest {
				id
				autoMergeRequest {
					enabledBy { login }
					authorEmail
					commitHeadline
					commitBody
					mergeMethod
				}
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId":   prNodeID,
			"mergeMethod":     "SQUASH",
			"authorEmail":     "queue@example.com",
			"commitHeadline":  "Queued squash",
			"commitBody":      "Merged after checks pass",
			"expectedHeadOid": pr.HeadSHA,
		},
	})

	emp := data["enablePullRequestAutoMerge"].(map[string]any)
	prResult := emp["pullRequest"].(map[string]any)

	// Verify auto-merge is enabled (autoMergeRequest should be present)
	if prResult["autoMergeRequest"] == nil {
		t.Error("autoMergeRequest should be present after enabling auto-merge")
	}
	autoMergeRequest := prResult["autoMergeRequest"].(map[string]any)
	enabledBy := autoMergeRequest["enabledBy"].(map[string]any)
	if enabledBy["login"] != "tester" {
		t.Errorf("enabledBy.login: got %v, want tester", enabledBy["login"])
	}
	if autoMergeRequest["authorEmail"] != "queue@example.com" {
		t.Errorf("authorEmail: got %v, want queue@example.com", autoMergeRequest["authorEmail"])
	}
	if autoMergeRequest["commitHeadline"] != "Queued squash" {
		t.Errorf("commitHeadline: got %v, want Queued squash", autoMergeRequest["commitHeadline"])
	}
	if autoMergeRequest["commitBody"] != "Merged after checks pass" {
		t.Errorf("commitBody: got %v, want Merged after checks pass", autoMergeRequest["commitBody"])
	}
}

// TestGraphQL_DisablePullRequestAutoMerge_Success tests disabling auto-merge on a PR.
func TestGraphQL_DisablePullRequestAutoMerge_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, _ := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "disable-auto-merge-repo",
		AutoInit:   true,
	})
	svc.DB.Model(&db.Repository{}).Where("id = ?", repo.ID).Update("allow_auto_merge", true)

	svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Disable Auto-merge PR",
		Body:         "disable auto-merge",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	// First enable auto-merge
	if _, err := svc.SetPRAutoMerge(service.ContextWithUser(ctx, u), pr.ID, service.SetPRAutoMergeInput{
		Enabled:         true,
		MergeMethod:     "MERGE",
		ExpectedHeadSHA: pr.HeadSHA,
	}); err != nil {
		t.Fatalf("SetPRAutoMerge: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: DisablePullRequestAutoMergeInput!) {
		disablePullRequestAutoMerge(input: $input) {
			pullRequest {
				id
				autoMergeRequest
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
		},
	})

	dmp := data["disablePullRequestAutoMerge"].(map[string]any)
	prResult := dmp["pullRequest"].(map[string]any)

	// Auto-merge should be disabled (autoMergeRequest should be null)
	if prResult["autoMergeRequest"] != nil {
		t.Error("autoMergeRequest should be nil after disabling auto-merge")
	}
}

// TestGraphQL_EnablePullRequestAutoMerge_RepoNotAllowed tests enabling auto-merge fails when repo doesn't allow it.
func TestGraphQL_EnablePullRequestAutoMerge_RepoNotAllowed(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, _ := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "no-auto-merge-repo",
		AutoInit:   true,
	})
	// Explicitly disable auto-merge on repo
	svc.DB.Model(&db.Repository{}).Where("id = ?", repo.ID).Update("allow_auto_merge", false)

	svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Not Allowed Auto-merge PR",
		Body:         "repo doesn't allow",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: EnablePullRequestAutoMergeInput!) {
		enablePullRequestAutoMerge(input: $input) {
			pullRequest {
				id
			}
		}
	}`

	res := doRawGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
			"mergeMethod":   "MERGE",
		},
	})

	// Should have errors for repo not allowing auto-merge
	if res["errors"] == nil {
		t.Error("expected errors for auto-merge not allowed, got none")
	}
}

// TestGraphQL_EnablePullRequestAutoMerge_ExpectedHeadMismatch tests the CAS guard.
func TestGraphQL_EnablePullRequestAutoMerge_ExpectedHeadMismatch(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, _ := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "auto-merge-head-mismatch-repo",
		AutoInit:   true,
	})
	svc.DB.Model(&db.Repository{}).Where("id = ?", repo.ID).Update("allow_auto_merge", true)

	svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Head mismatch PR",
		Body:         "expected head mismatch",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)
	q := `
	mutation($input: EnablePullRequestAutoMergeInput!) {
		enablePullRequestAutoMerge(input: $input) {
			pullRequest { id }
		}
	}`

	res := doRawGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId":   prNodeID,
			"mergeMethod":     "MERGE",
			"expectedHeadOid": "deadbeef",
		},
	})

	if res["errors"] == nil {
		t.Fatal("expected expectedHeadOid mismatch error")
	}
}

// =============================================================================
// Revert Behavior Tests (doRevertPR)
// =============================================================================

// TestGraphQL_RevertPR_Success tests reverting a merged PR.
// This test verifies the revert mutation flow - it creates a PR, merges it,
// then attempts to revert it. The revert may fail due to git state (no changes to revert)
// but the mutation handler flow is tested.
func TestGraphQL_RevertPR_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, _ := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "revert-success-repo",
		AutoInit:   true,
	})

	svc.Git.CreateBranch(ctx, repo.FullName, "feature-revert", "main")

	// Write a file on the feature branch
	if _, err := svc.Git.WriteFile(ctx, repo.FullName, "feature-revert", "test.txt", "initial commit", []byte("content\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Feature to Revert",
		Body:         "will be reverted",
		HeadRef:      "feature-revert",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	// Merge the PR using git to create proper state
	repoPath, _ := svc.Git.GetRepoPath(ctx, repo.FullName)
	// Checkout main and merge the feature branch
	execCmd(t, repoPath, "git", "checkout", "main")
	execCmd(t, repoPath, "git", "merge", "--no-ff", "-m", "merge commit", "feature-revert")

	// Get the merge commit SHA
	mergeSHA := execCmdOut(t, repoPath, "git", "rev-parse", "HEAD")

	// Update PR with merge info
	svc.DB.Model(&db.PullRequest{}).Where("id = ?", pr.ID).Updates(map[string]any{
		"merged":           true,
		"merge_commit_sha": strings.TrimSpace(mergeSHA),
		"state":            db.StateMerged,
	})

	// Reload PR
	pr, _ = svc.GetPRByID(ctx, pr.ID)
	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: RevertPullRequestInput!) {
		revertPullRequest(input: $input) {
			pullRequest {
				id
				merged
			}
			revertPullRequest {
				id
				number
				title
				body
				headRefName
				baseRefName
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
			"title":         "Revert: Feature to Revert",
			"body":          "Reverting the feature",
			"draft":         false,
		},
	})

	rp := data["revertPullRequest"].(map[string]any)

	// Original PR should be merged
	origPR := rp["pullRequest"].(map[string]any)
	if origPR["merged"] != true {
		t.Errorf("original PR merged: got %v, want true", origPR["merged"])
	}

	// Revert PR should be created
	revertPR := rp["revertPullRequest"].(map[string]any)
	if revertPR["title"] != "Revert: Feature to Revert" {
		t.Errorf("revert PR title: got %v, want 'Revert: Feature to Revert'", revertPR["title"])
	}
	if revertPR["body"] != "Reverting the feature" {
		t.Errorf("revert PR body: got %v, want 'Reverting the feature'", revertPR["body"])
	}
	if revertPR["headRefName"] == nil || revertPR["headRefName"] == "" {
		t.Error("revert PR should have headRefName")
	}
	if revertPR["baseRefName"] != "main" {
		t.Errorf("revert PR baseRefName: got %v, want main", revertPR["baseRefName"])
	}
}

// execCmd executes a git command in the given directory.
func execCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		t.Logf("cmd %v failed: %v", args, err)
	}
}

// execCmdOut executes a git command and returns output.
func execCmdOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Logf("cmd %v failed: %v", args, err)
		return ""
	}
	return string(out)
}

// TestGraphQL_RevertPR_NotMerged tests revert fails on unmerged PR.
func TestGraphQL_RevertPR_NotMerged(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, _ := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "revert-notmerged-repo",
		AutoInit:   true,
	})

	svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Unmerged PR",
		Body:         "not merged yet",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: RevertPullRequestInput!) {
		revertPullRequest(input: $input) {
			pullRequest {
				id
			}
		}
	}`

	res := doRawGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
		},
	})

	// Should have errors for unmerged PR
	if res["errors"] == nil {
		t.Error("expected errors for reverting unmerged PR, got none")
	}
}

// TestGraphQL_RevertPR_NoMergeCommitSHA tests revert fails on merged PR without merge commit SHA.
func TestGraphQL_RevertPR_NoMergeCommitSHA(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, _ := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "revert-nosha-repo",
		AutoInit:   true,
	})

	svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "PR without merge SHA",
		Body:         "testing",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	// Mark as merged but without merge commit SHA
	svc.DB.Model(&db.PullRequest{}).Where("id = ?", pr.ID).Updates(map[string]any{
		"merged":           true,
		"merge_commit_sha": "",
		"state":            db.StateMerged,
	})

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: RevertPullRequestInput!) {
		revertPullRequest(input: $input) {
			pullRequest {
				id
			}
		}
	}`

	res := doRawGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
		},
	})

	// Should have errors for missing merge commit SHA
	if res["errors"] == nil {
		t.Error("expected errors for PR without merge commit SHA, got none")
	}
}

// =============================================================================
// Update Branch Tests (doUpdatePRBranch)
// =============================================================================

// TestGraphQL_UpdatePRBranch_Merge_Success tests updating PR branch with MERGE method.
func TestGraphQL_UpdatePRBranch_Merge_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, _ := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-branch-merge-repo",
		AutoInit:   true,
	})

	svc.Git.CreateBranch(ctx, repo.FullName, "feature-update", "main")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Update Branch PR",
		Body:         "testing branch update",
		HeadRef:      "feature-update",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: UpdatePullRequestBranchInput!) {
		updatePullRequestBranch(input: $input) {
			pullRequest {
				id
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
			"updateMethod":  "MERGE",
		},
	})

	// Should succeed without errors
	upd := data["updatePullRequestBranch"].(map[string]any)
	if upd["pullRequest"] == nil {
		t.Error("updatePullRequestBranch should return pullRequest")
	}
}

// TestGraphQL_UpdatePRBranch_Rebase_Success tests updating PR branch with REBASE method.
func TestGraphQL_UpdatePRBranch_Rebase_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, _ := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-branch-rebase-repo",
		AutoInit:   true,
	})

	svc.Git.CreateBranch(ctx, repo.FullName, "feature-rebase", "main")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Rebase Update PR",
		Body:         "testing rebase update",
		HeadRef:      "feature-rebase",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: UpdatePullRequestBranchInput!) {
		updatePullRequestBranch(input: $input) {
			pullRequest {
				id
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
			"updateMethod":  "REBASE",
		},
	})

	// Should succeed without errors
	upd := data["updatePullRequestBranch"].(map[string]any)
	if upd["pullRequest"] == nil {
		t.Error("updatePullRequestBranch should return pullRequest")
	}
}

// TestGraphQL_UpdatePRBranch_InvalidPR tests update branch fails with invalid PR ID.
func TestGraphQL_UpdatePRBranch_InvalidPR(t *testing.T) {
	_, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	q := `
	mutation($input: UpdatePullRequestBranchInput!) {
		updatePullRequestBranch(input: $input) {
			pullRequest {
				id
			}
		}
	}`

	res := doRawGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": "PullRequest_99999",
			"updateMethod":  "MERGE",
		},
	})

	// Should have errors for invalid PR
	if res["errors"] == nil {
		t.Error("expected errors for invalid PR ID, got none")
	}
}

// TestGraphQL_UpdatePRBranch_CrossRepo tests update branch for cross-repo (fork) PR.
func TestGraphQL_UpdatePRBranch_CrossRepo(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Create upstream repo
	upstream, _ := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "upstream-repo",
		AutoInit:   true,
	})

	// Create fork repo (same owner for test simplicity)
	fork, _ := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "fork-repo",
		AutoInit:   true,
	})

	svc.Git.CreateBranch(ctx, fork.FullName, "feature-fork", "main")

	// Create cross-repo PR
	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName:     upstream.FullName,
		HeadRepoFullName: fork.FullName,
		Title:            "Cross-repo PR",
		Body:             "from fork",
		HeadRef:          "feature-fork",
		BaseRef:          "main",
		AuthorLogin:      "tester",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: UpdatePullRequestBranchInput!) {
		updatePullRequestBranch(input: $input) {
			pullRequest {
				id
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
			"updateMethod":  "MERGE",
		},
	})

	// Should succeed for cross-repo PR
	upd := data["updatePullRequestBranch"].(map[string]any)
	if upd["pullRequest"] == nil {
		t.Error("updatePullRequestBranch should return pullRequest for cross-repo PR")
	}
}

// TestGraphQL_UpdatePRBranch_Conflict tests update branch fails with merge conflict.
func TestGraphQL_UpdatePRBranch_Conflict(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, _ := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "conflict-repo",
		AutoInit:   true,
	})

	// Create feature branch
	svc.Git.CreateBranch(ctx, repo.FullName, "feature-conflict", "main")

	// Write conflicting file on feature branch
	if _, err := svc.Git.WriteFile(ctx, repo.FullName, "feature-conflict", "conflict.txt", "feature commit", []byte("feature content\n")); err != nil {
		t.Fatalf("WriteFile(feature): %v", err)
	}

	// Write same file on main branch
	if _, err := svc.Git.WriteFile(ctx, repo.FullName, "main", "conflict.txt", "main commit", []byte("main content\n")); err != nil {
		t.Fatalf("WriteFile(main): %v", err)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Conflict PR",
		Body:         "has conflicts",
		HeadRef:      "feature-conflict",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	mutation($input: UpdatePullRequestBranchInput!) {
		updatePullRequestBranch(input: $input) {
			pullRequest {
				id
			}
		}
	}`

	res := doRawGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": prNodeID,
			"updateMethod":  "MERGE",
		},
	})

	// Should have errors for merge conflict
	if res["errors"] == nil {
		t.Error("expected errors for merge conflict, got none")
	}
}

// TestGraphQL_UpdatePRBranch_Unauthorized is skipped - auth behavior
// is tested at the service layer; GraphQL layer passes through service errors.
func TestGraphQL_UpdatePRBranch_Unauthorized(t *testing.T) {
	svc, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	other := db.User{Login: "otherpruser", Name: "Other User", Type: db.TypeUser}
	svc.DB.Create(&other)
	svc.DB.Create(&db.Token{UserID: other.ID, Value: "test-token-other-pr"})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: other.Login,
		Name:       "unauth-pr-branch-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if _, err := svc.Git.WriteFile(ctx, repo.FullName, "feature", "feature.txt", "feature commit", []byte("feature\n")); err != nil {
		t.Fatalf("WriteFile(feature): %v", err)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Other user's PR",
		Body:         "should not be updated",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  other.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	if _, err := svc.Git.WriteFile(ctx, repo.FullName, "main", "base.txt", "base commit", []byte("base\n")); err != nil {
		t.Fatalf("WriteFile(main): %v", err)
	}

	headBefore, err := svc.Git.HeadSHA(ctx, repo.FullName, "feature")
	if err != nil {
		t.Fatalf("HeadSHA(feature): %v", err)
	}
	prBefore, err := svc.GetPRByID(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}

	q := `
	mutation($input: UpdatePullRequestBranchInput!) {
		updatePullRequestBranch(input: $input) {
			pullRequest {
				id
			}
		}
	}`

	res := doRawGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": fmt.Sprintf("PullRequest_%d", pr.ID),
			"updateMethod":  "MERGE",
		},
	})

	msg := firstGQLErrorMessage(t, res)
	if !strings.Contains(strings.ToLower(msg), "not found") {
		t.Fatalf("expected not-found error, got %q", msg)
	}

	headAfter, err := svc.Git.HeadSHA(ctx, repo.FullName, "feature")
	if err != nil {
		t.Fatalf("HeadSHA(feature after): %v", err)
	}
	if headAfter != headBefore {
		t.Fatalf("expected feature head SHA unchanged, got %s want %s", headAfter, headBefore)
	}

	prAfter, err := svc.GetPRByID(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID(after): %v", err)
	}
	if prAfter.HeadSHA != prBefore.HeadSHA {
		t.Fatalf("expected PR head SHA unchanged, got %s want %s", prAfter.HeadSHA, prBefore.HeadSHA)
	}
}

// =============================================================================
// Update Pull Request Milestone Tests (updatePullRequest)
// =============================================================================

// TestGraphQL_UpdatePRMilestone_Set tests setting a milestone on a PR via updatePullRequest.
func TestGraphQL_UpdatePRMilestone_Set(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-pr-milestone-set-2",
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
		Title:        "PR milestone set",
		Body:         "setting milestone",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	milestone, err := svc.CreateMilestone(ctx, repo.FullName, "MS Set", "desc", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}

	q := `
	mutation($input: UpdatePullRequestInput!) {
		updatePullRequest(input: $input) {
			pullRequest {
				id
				milestone {
					id
					title
					number
				}
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": fmt.Sprintf("PullRequest_%d", pr.ID),
			"milestoneId":   fmt.Sprintf("Milestone_%d", milestone.ID),
		},
	})

	prData := data["updatePullRequest"].(map[string]any)["pullRequest"].(map[string]any)
	msAny := prData["milestone"]
	if msAny == nil {
		t.Fatal("milestone should be set")
	}
	ms := msAny.(map[string]any)
	if ms["id"] != fmt.Sprintf("Milestone_%d", milestone.ID) {
		t.Errorf("milestone.id: got %v, want Milestone_%d", ms["id"], milestone.ID)
	}
	if ms["title"] != "MS Set" {
		t.Errorf("milestone.title: got %v, want MS Set", ms["title"])
	}
	if ms["number"] != float64(milestone.Number) {
		t.Errorf("milestone.number: got %v, want %d", ms["number"], milestone.Number)
	}

	stored, err := svc.GetPRByID(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if stored.MilestoneID == nil || *stored.MilestoneID != milestone.ID {
		t.Fatalf("stored milestone ID: got %v, want %d", stored.MilestoneID, milestone.ID)
	}
}

// TestGraphQL_UpdatePRMilestone_Clear tests clearing a milestone via updatePullRequest.
func TestGraphQL_UpdatePRMilestone_Clear(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-pr-milestone-clear-2",
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
		Title:        "PR milestone clear",
		Body:         "clearing milestone",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	milestone, err := svc.CreateMilestone(ctx, repo.FullName, "MS Clear", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}
	if err := svc.SetPRMilestone(ctx, pr.ID, &milestone.ID); err != nil {
		t.Fatalf("SetPRMilestone: %v", err)
	}

	q := `
	mutation($input: UpdatePullRequestInput!) {
		updatePullRequest(input: $input) {
			pullRequest {
				id
				milestone { id title }
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": fmt.Sprintf("PullRequest_%d", pr.ID),
			"milestoneId":   nil,
		},
	})

	prData := data["updatePullRequest"].(map[string]any)["pullRequest"].(map[string]any)
	if prData["milestone"] != nil {
		t.Errorf("milestone should be nil after clear, got %v", prData["milestone"])
	}

	stored, err := svc.GetPRByID(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if stored.MilestoneID != nil {
		t.Fatalf("stored milestone ID: got %v, want nil", *stored.MilestoneID)
	}
}

// TestGraphQL_UpdatePRMilestone_InvalidMilestoneID tests invalid milestone IDs are rejected.
func TestGraphQL_UpdatePRMilestone_InvalidMilestoneID(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-pr-milestone-invalid-2",
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
		Title:        "PR milestone invalid",
		Body:         "invalid milestone",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	q := `
	mutation($input: UpdatePullRequestInput!) {
		updatePullRequest(input: $input) {
			pullRequest { id }
		}
	}`

	res := doRawGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": fmt.Sprintf("PullRequest_%d", pr.ID),
			"milestoneId":   "NotAMilestone",
		},
	})

	if res["errors"] == nil {
		t.Fatal("expected errors for invalid milestone ID, got none")
	}
	msg := firstGQLErrorMessage(t, res)
	if !strings.Contains(strings.ToLower(msg), "milestone") {
		t.Fatalf("expected milestone error, got %q", msg)
	}

	stored, err := svc.GetPRByID(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if stored.MilestoneID != nil {
		t.Fatalf("stored milestone ID: got %v, want nil", *stored.MilestoneID)
	}
}

// TestGraphQL_UpdatePRMilestone_CrossRepo tests cross-repo milestone assignment errors.
func TestGraphQL_UpdatePRMilestone_CrossRepo(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-pr-milestone-cross-repo-2",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	otherRepo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-pr-milestone-other-2",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo(other): %v", err)
	}
	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "PR milestone cross repo",
		Body:         "cross repo milestone",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	otherMilestone, err := svc.CreateMilestone(ctx, otherRepo.FullName, "Other Repo MS", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone(other): %v", err)
	}

	q := `
	mutation($input: UpdatePullRequestInput!) {
		updatePullRequest(input: $input) {
			pullRequest { id }
		}
	}`

	res := doRawGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": fmt.Sprintf("PullRequest_%d", pr.ID),
			"milestoneId":   fmt.Sprintf("Milestone_%d", otherMilestone.ID),
		},
	})

	if res["errors"] == nil {
		t.Fatal("expected errors for cross-repo milestone, got none")
	}
	msg := firstGQLErrorMessage(t, res)
	if !strings.Contains(strings.ToLower(msg), "milestone does not belong") {
		t.Fatalf("expected cross-repo milestone error, got %q", msg)
	}

	stored, err := svc.GetPRByID(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if stored.MilestoneID != nil {
		t.Fatalf("stored milestone ID: got %v, want nil", *stored.MilestoneID)
	}
}

// TestGraphQL_UpdatePRMilestone_ChangeMilestone tests changing from one milestone to another.
func TestGraphQL_UpdatePRMilestone_ChangeMilestone(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-pr-milestone-change",
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
		Title:        "PR milestone change",
		Body:         "change milestone",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	firstMS, err := svc.CreateMilestone(ctx, repo.FullName, "MS One", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone(first): %v", err)
	}
	secondMS, err := svc.CreateMilestone(ctx, repo.FullName, "MS Two", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone(second): %v", err)
	}
	if err := svc.SetPRMilestone(ctx, pr.ID, &firstMS.ID); err != nil {
		t.Fatalf("SetPRMilestone: %v", err)
	}

	q := `
	mutation($input: UpdatePullRequestInput!) {
		updatePullRequest(input: $input) {
			pullRequest {
				id
				milestone {
					id
					title
					number
				}
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"pullRequestId": fmt.Sprintf("PullRequest_%d", pr.ID),
			"milestoneId":   fmt.Sprintf("Milestone_%d", secondMS.ID),
		},
	})

	prData := data["updatePullRequest"].(map[string]any)["pullRequest"].(map[string]any)
	msAny := prData["milestone"]
	if msAny == nil {
		t.Fatal("milestone should be set")
	}
	ms := msAny.(map[string]any)
	if ms["id"] != fmt.Sprintf("Milestone_%d", secondMS.ID) {
		t.Errorf("milestone.id: got %v, want Milestone_%d", ms["id"], secondMS.ID)
	}
	if ms["title"] != "MS Two" {
		t.Errorf("milestone.title: got %v, want MS Two", ms["title"])
	}
	if ms["number"] != float64(secondMS.Number) {
		t.Errorf("milestone.number: got %v, want %d", ms["number"], secondMS.Number)
	}

	stored, err := svc.GetPRByID(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if stored.MilestoneID == nil || *stored.MilestoneID != secondMS.ID {
		t.Fatalf("stored milestone ID: got %v, want %d", stored.MilestoneID, secondMS.ID)
	}
}
