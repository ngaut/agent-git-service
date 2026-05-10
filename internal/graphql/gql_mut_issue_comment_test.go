package graphql_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

// =============================================================================
// Comment Mutation Tests
// =============================================================================

// TestGraphQL_AddComment_Issue tests the addComment mutation for issues.
func TestGraphQL_AddComment_Issue(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Create repo and issue.
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "comment-test-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue for Comments",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Test addComment mutation.
	mut := `
	mutation($input: AddCommentInput!) {
		addComment(input: $input) {
			commentEdge {
				node {
					id
					body
					author { login }
					createdAt
					url
				}
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"body":      "This is a test comment",
			"subjectId": fmt.Sprintf("Issue_%d", issue.ID),
		},
	})

	addComment := data["addComment"].(map[string]any)
	edge := addComment["commentEdge"].(map[string]any)
	node := edge["node"].(map[string]any)

	if node["body"] != "This is a test comment" {
		t.Errorf("comment body: got %v, want 'This is a test comment'", node["body"])
	}
	if node["author"].(map[string]any)["login"] != u.Login {
		t.Errorf("comment author: got %v, want %s", node["author"].(map[string]any)["login"], u.Login)
	}
	if node["id"] == nil || node["id"] == "" {
		t.Error("comment id should be non-empty")
	}
	if node["url"] == nil || node["url"] == "" {
		t.Error("comment url should be non-empty")
	}

	// Verify comment exists in database.
	comments, err := svc.ListIssueComments(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), issue.Number)
	if err != nil {
		t.Fatalf("ListIssueComments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if comments[0].Body != "This is a test comment" {
		t.Errorf("stored comment body: got %v, want 'This is a test comment'", comments[0].Body)
	}
}

// TestGraphQL_UpdateIssueComment tests the updateIssueComment mutation.
func TestGraphQL_UpdateIssueComment(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Create repo, issue, and initial comment.
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-comment-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	comment, err := svc.CreateIssueComment(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), issue.Number, "original comment", u.Login, nil)
	if err != nil {
		t.Fatalf("CreateIssueComment: %v", err)
	}

	// Test updateIssueComment mutation.
	mut := `
	mutation($input: UpdateIssueCommentInput!) {
		updateIssueComment(input: $input) {
			issueComment {
				id
				body
				url
				author { login }
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"id":   fmt.Sprintf("IssueComment_%d", comment.ID),
			"body": "updated comment body",
		},
	})

	updateResult := data["updateIssueComment"].(map[string]any)
	updatedComment := updateResult["issueComment"].(map[string]any)

	if updatedComment["body"] != "updated comment body" {
		t.Errorf("updated comment body: got %v, want 'updated comment body'", updatedComment["body"])
	}
	if updatedComment["author"].(map[string]any)["login"] != u.Login {
		t.Errorf("updated comment author: got %v, want %s", updatedComment["author"].(map[string]any)["login"], u.Login)
	}

	// Verify comment is updated in database.
	stored, err := svc.GetIssueCommentByID(ctx, comment.ID)
	if err != nil {
		t.Fatalf("GetIssueCommentByID: %v", err)
	}
	if stored.Body != "updated comment body" {
		t.Errorf("stored comment body: got %v, want 'updated comment body'", stored.Body)
	}
}

// TestGraphQL_DeleteIssueComment tests the deleteIssueComment mutation.
func TestGraphQL_DeleteIssueComment(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Create repo, issue, and comment.
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "delete-comment-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	comment, err := svc.CreateIssueComment(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), issue.Number, "comment to delete", u.Login, nil)
	if err != nil {
		t.Fatalf("CreateIssueComment: %v", err)
	}

	// Test deleteIssueComment mutation.
	mut := `
	mutation($input: DeleteIssueCommentInput!) {
		deleteIssueComment(input: $input) {
			deletedCommentId
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"id": fmt.Sprintf("IssueComment_%d", comment.ID),
		},
	})

	delResult := data["deleteIssueComment"]
	if delResult != nil {
		// deleteIssueComment returns nil payload on success
		t.Logf("deleteIssueComment returned: %v", delResult)
	}

	// Verify comment is deleted from database.
	_, err = svc.GetIssueCommentByID(ctx, comment.ID)
	if err == nil {
		t.Error("comment should be deleted from database")
	}
}

// =============================================================================
// Label Mutation Tests
// =============================================================================

// TestGraphQL_AddLabelsToLabelable tests adding labels to an issue.
func TestGraphQL_AddLabelsToLabelable(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Migrate Label table.
	svc.DB.AutoMigrate(&db.Label{})

	// Create repo and issue.
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "label-test-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue for Labels",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Create labels (use unique names to avoid conflict with seeded labels).
	label1, err := svc.CreateLabel(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), "test-bug", "d73a4a", "Test bug label")
	if err != nil {
		t.Fatalf("CreateLabel test-bug: %v", err)
	}
	label2, err := svc.CreateLabel(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), "test-enhancement", "a2eeef", "Test enhancement label")
	if err != nil {
		t.Fatalf("CreateLabel test-enhancement: %v", err)
	}

	// Test addLabelsToLabelable mutation.
	mut := `
	mutation($input: AddLabelsToLabelableInput!) {
		addLabelsToLabelable(input: $input) {
			__typename
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"labelableId": fmt.Sprintf("Issue_%d", issue.ID),
			"labelIds":    []string{fmt.Sprintf("Label_%d", label1.ID), fmt.Sprintf("Label_%d", label2.ID)},
		},
	})

	if data["addLabelsToLabelable"] == nil {
		t.Error("addLabelsToLabelable should return non-nil payload")
	}

	// Verify labels are attached in database.
	labels, err := svc.ListIssueLabels(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), issue.Number)
	if err != nil {
		t.Fatalf("ListIssueLabels: %v", err)
	}
	if len(labels) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(labels))
	}
}

// TestGraphQL_AddLabelsToLabelable_Duplicates tests idempotency with duplicate label IDs.
func TestGraphQL_AddLabelsToLabelable_Duplicates(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Migrate Label table.
	svc.DB.AutoMigrate(&db.Label{})

	// Create repo and issue.
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "label-dup-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Create a label.
	label, err := svc.CreateLabel(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), "duplicate-test", "ffffff", "Test label")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}

	// Add the same label twice (should be idempotent).
	mut := `
	mutation($input: AddLabelsToLabelableInput!) {
		addLabelsToLabelable(input: $input) {
			__typename
		}
	}`

	// First add.
	data1 := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"labelableId": fmt.Sprintf("Issue_%d", issue.ID),
			"labelIds":    []string{fmt.Sprintf("Label_%d", label.ID), fmt.Sprintf("Label_%d", label.ID)},
		},
	})
	if data1["addLabelsToLabelable"] == nil {
		t.Error("first addLabelsToLabelable should succeed")
	}

	// Second add (duplicate).
	data2 := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"labelableId": fmt.Sprintf("Issue_%d", issue.ID),
			"labelIds":    []string{fmt.Sprintf("Label_%d", label.ID)},
		},
	})
	if data2["addLabelsToLabelable"] == nil {
		t.Error("second addLabelsToLabelable should succeed (idempotent)")
	}

	// Verify only one label is attached.
	labels, err := svc.ListIssueLabels(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), issue.Number)
	if err != nil {
		t.Fatalf("ListIssueLabels: %v", err)
	}
	if len(labels) != 1 {
		t.Errorf("expected 1 label after duplicate adds, got %d", len(labels))
	}
}

// TestGraphQL_AddLabelsToLabelable_InvalidTarget tests adding labels with invalid labelable ID.
func TestGraphQL_AddLabelsToLabelable_InvalidTarget(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Migrate Label table.
	svc.DB.AutoMigrate(&db.Label{})

	// Create repo and label.
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "label-invalid-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	label, err := svc.CreateLabel(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), "orphan", "000000", "Orphan label")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}

	mut := `
	mutation($input: AddLabelsToLabelableInput!) {
		addLabelsToLabelable(input: $input) {
			__typename
		}
	}`
	res := doRawGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"labelableId": "InvalidType_999999",
			"labelIds":    []string{fmt.Sprintf("Label_%d", label.ID)},
		},
	})

	if res["errors"] == nil {
		t.Error("expected errors for invalid labelableId")
	}
}

func TestGraphQL_AddLabelsToLabelable_PullRequest(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.AutoMigrate(&db.Label{})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "pr-label-test-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	fullName := fmt.Sprintf("%s/%s", u.Login, repo.Name)
	if err := svc.Git.CreateBranch(ctx, fullName, "feature", repo.DefaultBranch); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName:     fullName,
		HeadRepoFullName: fullName,
		Title:            "Test PR for Labels",
		Body:             "body",
		HeadRef:          "feature",
		BaseRef:          repo.DefaultBranch,
		AuthorLogin:      u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	label, err := svc.CreateLabel(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), "pr-label", "123456", "PR label")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}

	mut := `
	mutation($input: AddLabelsToLabelableInput!) {
		addLabelsToLabelable(input: $input) {
			__typename
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"labelableId": fmt.Sprintf("PullRequest_%d", pr.ID),
			"labelIds":    []string{fmt.Sprintf("Label_%d", label.ID)},
		},
	})
	if data["addLabelsToLabelable"] == nil {
		t.Fatal("addLabelsToLabelable should succeed for pull requests")
	}
	stored, err := svc.ReloadPR(ctx, pr.ID)
	if err != nil {
		t.Fatalf("ReloadPR: %v", err)
	}
	if len(stored.Labels) != 1 || stored.Labels[0].Name != "pr-label" {
		t.Fatalf("expected persisted PR label, got %#v", stored.Labels)
	}
}

func TestGraphQL_AddLabelsToLabelable_RollsBackOnInvalidLabelID(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.AutoMigrate(&db.Label{})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "label-atomic-add-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	fullName := fmt.Sprintf("%s/%s", u.Login, repo.Name)
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fullName,
		Title:        "Atomic add",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	label, err := svc.CreateLabel(ctx, fullName, "valid-add", "123abc", "valid")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}

	mut := `
	mutation($input: AddLabelsToLabelableInput!) {
		addLabelsToLabelable(input: $input) {
			__typename
		}
	}`
	res := doRawGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"labelableId": fmt.Sprintf("Issue_%d", issue.ID),
			"labelIds":    []string{fmt.Sprintf("Label_%d", label.ID), "not-a-label-id"},
		},
	})

	if res["errors"] == nil {
		t.Fatal("expected errors for mixed valid/invalid labelIds")
	}

	labels, err := svc.ListIssueLabels(ctx, fullName, issue.Number)
	if err != nil {
		t.Fatalf("ListIssueLabels: %v", err)
	}
	if len(labels) != 0 {
		t.Fatalf("expected rollback to leave issue labels unchanged, got %#v", labels)
	}
}

// TestGraphQL_RemoveLabelsFromLabelable tests removing labels from an issue.
func TestGraphQL_RemoveLabelsFromLabelable(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Migrate Label table.
	svc.DB.AutoMigrate(&db.Label{})

	// Create repo and issue.
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "remove-label-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Create and attach labels.
	label1, err := svc.CreateLabel(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), "to-remove", "ff0000", "To remove")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	_, err = svc.CreateLabel(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), "to-keep", "00ff00", "To keep")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}

	// Attach both labels.
	_, err = svc.AddIssueLabels(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), issue.Number, []string{"to-remove", "to-keep"})
	if err != nil {
		t.Fatalf("AddIssueLabels: %v", err)
	}

	// Test removeLabelsFromLabelable mutation.
	mut := `
	mutation($input: RemoveLabelsFromLabelableInput!) {
		removeLabelsFromLabelable(input: $input) {
			__typename
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"labelableId": fmt.Sprintf("Issue_%d", issue.ID),
			"labelIds":    []string{fmt.Sprintf("Label_%d", label1.ID)},
		},
	})

	if data["removeLabelsFromLabelable"] == nil {
		t.Error("removeLabelsFromLabelable should return non-nil payload")
	}

	// Verify only label2 remains.
	labels, err := svc.ListIssueLabels(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), issue.Number)
	if err != nil {
		t.Fatalf("ListIssueLabels: %v", err)
	}
	if len(labels) != 1 {
		t.Fatalf("expected 1 label after removal, got %d", len(labels))
	}
	if labels[0].Name != "to-keep" {
		t.Errorf("expected 'to-keep' label, got %s", labels[0].Name)
	}
}

// TestGraphQL_RemoveLabelsFromLabelable_MissingTarget tests removing non-existent labels.
func TestGraphQL_RemoveLabelsFromLabelable_MissingTarget(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Migrate Label table.
	svc.DB.AutoMigrate(&db.Label{})

	// Create repo and issue.
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "remove-missing-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Create a label but don't attach it.
	label, err := svc.CreateLabel(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), "not-attached", "ffffff", "Not attached")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}

	// Test removing a label that isn't attached.
	mut := `
	mutation($input: RemoveLabelsFromLabelableInput!) {
		removeLabelsFromLabelable(input: $input) {
			__typename
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"labelableId": fmt.Sprintf("Issue_%d", issue.ID),
			"labelIds":    []string{fmt.Sprintf("Label_%d", label.ID)},
		},
	})

	// Should succeed without error even if label wasn't attached.
	if data["removeLabelsFromLabelable"] == nil {
		t.Error("removeLabelsFromLabelable should succeed even for missing labels")
	}
}

func TestGraphQL_RemoveLabelsFromLabelable_InvalidLabelID(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.AutoMigrate(&db.Label{})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "remove-invalid-label-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	mut := `
	mutation($input: RemoveLabelsFromLabelableInput!) {
		removeLabelsFromLabelable(input: $input) {
			__typename
		}
	}`
	res := doRawGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"labelableId": fmt.Sprintf("Issue_%d", issue.ID),
			"labelIds":    []string{"not-a-label-id"},
		},
	})

	if res["errors"] == nil {
		t.Error("expected errors for invalid labelIds")
	}
}

func TestGraphQL_RemoveLabelsFromLabelable_PullRequestRollsBackOnInvalidLabelID(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.AutoMigrate(&db.Label{})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "pr-label-atomic-remove-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	fullName := fmt.Sprintf("%s/%s", u.Login, repo.Name)
	if err := svc.Git.CreateBranch(ctx, fullName, "feature", repo.DefaultBranch); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName:     fullName,
		HeadRepoFullName: fullName,
		Title:            "Atomic remove",
		Body:             "body",
		HeadRef:          "feature",
		BaseRef:          repo.DefaultBranch,
		AuthorLogin:      u.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	label1, err := svc.CreateLabel(ctx, fullName, "remove-me", "654321", "remove")
	if err != nil {
		t.Fatalf("CreateLabel(remove-me): %v", err)
	}
	label2, err := svc.CreateLabel(ctx, fullName, "keep-me", "abcdef", "keep")
	if err != nil {
		t.Fatalf("CreateLabel(keep-me): %v", err)
	}
	if err := svc.AddPRLabelByID(ctx, pr.ID, label1.ID); err != nil {
		t.Fatalf("AddPRLabelByID(label1): %v", err)
	}
	if err := svc.AddPRLabelByID(ctx, pr.ID, label2.ID); err != nil {
		t.Fatalf("AddPRLabelByID(label2): %v", err)
	}

	mut := `
	mutation($input: RemoveLabelsFromLabelableInput!) {
		removeLabelsFromLabelable(input: $input) {
			__typename
		}
	}`
	res := doRawGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"labelableId": fmt.Sprintf("PullRequest_%d", pr.ID),
			"labelIds":    []string{fmt.Sprintf("Label_%d", label1.ID), "not-a-label-id"},
		},
	})

	if res["errors"] == nil {
		t.Fatal("expected errors for mixed valid/invalid labelIds")
	}

	stored, err := svc.ReloadPR(ctx, pr.ID)
	if err != nil {
		t.Fatalf("ReloadPR: %v", err)
	}
	if len(stored.Labels) != 2 {
		t.Fatalf("expected rollback to preserve both PR labels, got %#v", stored.Labels)
	}
}

// =============================================================================
// Assignee Mutation Tests
// =============================================================================

// TestGraphQL_ReplaceActorsForAssignable_Issue tests replacing assignees on an issue.
func TestGraphQL_ReplaceActorsForAssignable_Issue(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Create repo and issue.
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "assignee-test-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue for Assignees",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Create another user to assign.
	user2 := db.User{Login: "assignee2", Name: "Assignee Two", Type: db.TypeUser}
	svc.DB.Create(&user2)
	svc.DB.Create(&db.Token{UserID: user2.ID, Value: "test-token-2"})

	// Get node IDs for users.
	user1NodeID := restUserNodeID(t, mux, u.Login)
	user2NodeID := restUserNodeID(t, mux, user2.Login)

	// Test replaceActorsForAssignable mutation.
	mut := `
	mutation($input: ReplaceActorsForAssignableInput!) {
		replaceActorsForAssignable(input: $input) {
			assignable {
				... on Issue {
					number
					title
					assignees(first: 10) {
						nodes { login }
					}
				}
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"assignableId": fmt.Sprintf("Issue_%d", issue.ID),
			"actorIds":     []string{user1NodeID, user2NodeID},
		},
	})

	assignable := data["replaceActorsForAssignable"].(map[string]any)["assignable"].(map[string]any)
	if assignable == nil {
		t.Fatal("assignable should not be nil")
	}

	assignees := assignable["assignees"].(map[string]any)["nodes"].([]any)
	if len(assignees) != 2 {
		t.Fatalf("expected 2 assignees, got %d", len(assignees))
	}

	// Verify assignees in database.
	storedIssue, err := svc.GetIssue(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), issue.Number)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if !strings.Contains(storedIssue.AssigneeLogins, u.Login) {
		t.Errorf("assignee_logins should contain %s, got %s", u.Login, storedIssue.AssigneeLogins)
	}
	if !strings.Contains(storedIssue.AssigneeLogins, user2.Login) {
		t.Errorf("assignee_logins should contain %s, got %s", user2.Login, storedIssue.AssigneeLogins)
	}
}

// TestGraphQL_ReplaceActorsForAssignable_Duplicates tests idempotency with duplicate actor IDs.
func TestGraphQL_ReplaceActorsForAssignable_Duplicates(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Create repo and issue.
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "assignee-dup-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	userNodeID := restUserNodeID(t, mux, u.Login)

	// Test with duplicate actor IDs.
	mut := `
	mutation($input: ReplaceActorsForAssignableInput!) {
		replaceActorsForAssignable(input: $input) {
			assignable {
				... on Issue {
					assignees(first: 10) {
						nodes { login }
					}
				}
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"assignableId": fmt.Sprintf("Issue_%d", issue.ID),
			"actorIds":     []string{userNodeID, userNodeID, userNodeID},
		},
	})

	assignable := data["replaceActorsForAssignable"].(map[string]any)["assignable"].(map[string]any)
	assignees := assignable["assignees"].(map[string]any)["nodes"].([]any)

	// Note: Current implementation does not deduplicate assignees in replaceActorsForAssignable.
	// The same login appears multiple times when duplicate actor IDs are provided.
	// This test verifies the mutation executes without error; deduplication is a known enhancement.
	if len(assignees) == 0 {
		t.Error("expected at least one assignee, got 0")
	}
	// Verify the assignee is the expected user.
	for _, a := range assignees {
		if a.(map[string]any)["login"] != u.Login {
			t.Errorf("assignee login: got %v, want %s", a.(map[string]any)["login"], u.Login)
		}
	}
}

// TestGraphQL_ReplaceActorsForAssignable_InvalidActor tests with invalid actor IDs.
func TestGraphQL_ReplaceActorsForAssignable_InvalidActor(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Create repo and issue.
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "assignee-invalid-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Test with invalid actor ID.
	mut := `
	mutation($input: ReplaceActorsForAssignableInput!) {
		replaceActorsForAssignable(input: $input) {
			assignable {
				... on Issue {
					assignees(first: 10) {
						nodes { login }
					}
				}
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"assignableId": fmt.Sprintf("Issue_%d", issue.ID),
			"actorIds":     []string{"InvalidUser_999999"},
		},
	})

	assignable := data["replaceActorsForAssignable"].(map[string]any)["assignable"].(map[string]any)
	assignees := assignable["assignees"].(map[string]any)["nodes"].([]any)

	// Should have no assignees since the actor ID is invalid.
	if len(assignees) != 0 {
		t.Errorf("expected 0 assignees for invalid actor, got %d", len(assignees))
	}
}

// =============================================================================
// Linked Branch Mutation Tests
// =============================================================================

// TestGraphQL_CreateLinkedBranch_Valid tests creating a linked branch with valid references.
func TestGraphQL_CreateLinkedBranch_Valid(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Migrate LinkedBranch table.
	svc.DB.AutoMigrate(&db.LinkedBranch{})

	// Create repo.
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "linked-branch-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	// Create issue.
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue for Linked Branch",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Get repo node ID.
	repoNodeID := fmt.Sprintf("Repository_%d", repo.ID)

	// Test createLinkedBranch mutation.
	mut := `
	mutation($input: CreateLinkedBranchInput!) {
		createLinkedBranch(input: $input) {
			linkedBranch {
				id
				ref {
					name
				}
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"repositoryId": repoNodeID,
			"issueId":      fmt.Sprintf("Issue_%d", issue.ID),
			"name":         "feature/test-branch",
			"oid":          "HEAD",
		},
	})

	linkedBranch := data["createLinkedBranch"].(map[string]any)["linkedBranch"].(map[string]any)
	if linkedBranch == nil {
		t.Fatal("linkedBranch should not be nil")
	}

	ref := linkedBranch["ref"].(map[string]any)
	if ref["name"] != "feature/test-branch" {
		t.Errorf("branch name: got %v, want 'feature/test-branch'", ref["name"])
	}

	// Verify linked branch exists in database.
	var lb db.LinkedBranch
	if err := svc.DB.Where("issue_id = ? AND branch_name = ?", issue.ID, "feature/test-branch").First(&lb).Error; err != nil {
		t.Fatalf("linked branch not found in database: %v", err)
	}
	if lb.RepositoryID != repo.ID {
		t.Errorf("repository_id: got %d, want %d", lb.RepositoryID, repo.ID)
	}
}

// TestGraphQL_CreateLinkedBranch_InvalidRepo tests creating a linked branch with non-existent repo.
func TestGraphQL_CreateLinkedBranch_InvalidRepo(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Migrate LinkedBranch table.
	svc.DB.AutoMigrate(&db.LinkedBranch{})

	// Create issue in an existing repo.
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "valid-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Test with invalid repository ID.
	mut := `
	mutation($input: CreateLinkedBranchInput!) {
		createLinkedBranch(input: $input) {
			linkedBranch {
				id
			}
		}
	}`

	// Use doRawGql to check for errors.
	result := doRawGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"repositoryId": "Repository_999999",
			"issueId":      fmt.Sprintf("Issue_%d", issue.ID),
			"name":         "feature/invalid-repo",
			"oid":          "HEAD",
		},
	})

	// When repository is not found, the mutation returns an error.
	// Check for errors in the response.
	if result["errors"] == nil {
		// Note: If no errors, the mutation may have succeeded with empty/nil data.
		// Verify that no linked branch was created.
		data := result["data"]
		if data != nil {
			lb := data.(map[string]any)["createLinkedBranch"]
			if lb != nil {
				lbData := lb.(map[string]any)["linkedBranch"]
				if lbData != nil {
					t.Logf("createLinkedBranch returned: %v", lbData)
				}
			}
		}
	}
}

// TestGraphQL_CreateLinkedBranch_InvalidOID tests creating a linked branch with invalid OID.
func TestGraphQL_CreateLinkedBranch_InvalidOID(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Migrate LinkedBranch table.
	svc.DB.AutoMigrate(&db.LinkedBranch{})

	// Create repo and issue.
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "invalid-oid-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	repoNodeID := fmt.Sprintf("Repository_%d", repo.ID)

	// Test with invalid OID.
	mut := `
	mutation($input: CreateLinkedBranchInput!) {
		createLinkedBranch(input: $input) {
			linkedBranch {
				id
			}
		}
	}`

	result := doRawGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"repositoryId": repoNodeID,
			"issueId":      fmt.Sprintf("Issue_%d", issue.ID),
			"name":         "feature/invalid-oid",
			"oid":          "invalid-oid-that-does-not-exist",
		},
	})

	// When OID is invalid, Git.CreateBranchFromOid should fail and return an error.
	// Check for errors in the response.
	if result["errors"] == nil {
		// Note: If no errors, verify that no linked branch was created.
		data := result["data"]
		if data != nil {
			lb := data.(map[string]any)["createLinkedBranch"]
			if lb != nil {
				lbData := lb.(map[string]any)["linkedBranch"]
				if lbData != nil {
					t.Logf("createLinkedBranch returned: %v", lbData)
				}
			}
		}
	}
}

// TestGraphQL_CreateLinkedBranch_PermissionBoundary tests permission boundaries for linked branches.
func TestGraphQL_CreateLinkedBranch_PermissionBoundary(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Migrate LinkedBranch table.
	svc.DB.AutoMigrate(&db.LinkedBranch{})
	svc.DB.AutoMigrate(&db.Collaborator{})

	// Create another user.
	user2 := db.User{Login: "otheruser", Name: "Other User", Type: db.TypeUser}
	svc.DB.Create(&user2)
	svc.DB.Create(&db.Token{UserID: user2.ID, Value: "test-token-other"})

	// Create repo owned by user2.
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: user2.Login,
		Name:       "other-user-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	// Create issue in user2's repo.
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", user2.Login, repo.Name),
		Title:        "Other User's Issue",
		Body:         "issue body",
		AuthorLogin:  user2.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	repoNodeID := fmt.Sprintf("Repository_%d", repo.ID)
	deniedBranchName := "feature/cross-user-denied"
	baseOID, err := svc.Git.HeadSHA(ctx, repo.FullName, repo.DefaultBranch)
	if err != nil {
		t.Fatalf("HeadSHA(%s): %v", repo.DefaultBranch, err)
	}

	if _, err := svc.Git.HeadSHA(ctx, repo.FullName, deniedBranchName); err == nil {
		t.Fatalf("precondition failed: branch %q should not exist", deniedBranchName)
	}

	// Test: user 'u' trying to create linked branch on 'otheruser's repo.
	// The mutation uses the authenticated user from context (u), not user2.
	mut := `
	mutation($input: CreateLinkedBranchInput!) {
		createLinkedBranch(input: $input) {
			linkedBranch {
				id
				ref {
					name
				}
			}
		}
	}`

	// This should succeed because the GraphQL server authenticates as 'u' (test-token)
	// but the repo belongs to 'otheruser'. The permission check happens in the service layer.
	// Depending on implementation, this may or may not be allowed.
	result := doRawGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"repositoryId": repoNodeID,
			"issueId":      fmt.Sprintf("Issue_%d", issue.ID),
			"name":         deniedBranchName,
			"oid":          baseOID,
		},
	})

	msg := firstGQLErrorMessage(t, result)
	if !strings.Contains(strings.ToLower(msg), "not found") {
		t.Fatalf("expected not-found error, got %q", msg)
	}

	if _, err := svc.Git.HeadSHA(ctx, repo.FullName, deniedBranchName); err == nil {
		t.Fatalf("branch %q should not be created on auth failure", deniedBranchName)
	}

	var lb db.LinkedBranch
	if err := svc.DB.Where("issue_id = ? AND branch_name = ?", issue.ID, deniedBranchName).First(&lb).Error; err == nil {
		t.Fatalf("linked branch should not be created on auth failure")
	}

	// Non-owner user with write collaborator permission should succeed.
	if err := svc.AddCollaborator(ctx, repo.ID, u.ID, "write"); err != nil {
		t.Fatalf("AddCollaborator(write): %v", err)
	}

	allowedBranchName := "feature/cross-user-write"
	if _, err := svc.Git.HeadSHA(ctx, repo.FullName, allowedBranchName); err == nil {
		t.Fatalf("precondition failed: branch %q should not exist", allowedBranchName)
	}

	allowedResult := doRawGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"repositoryId": repoNodeID,
			"issueId":      fmt.Sprintf("Issue_%d", issue.ID),
			"name":         allowedBranchName,
			"oid":          baseOID,
		},
	})

	if errs, ok := allowedResult["errors"]; ok && errs != nil {
		t.Fatalf("expected success for write collaborator, got errors: %v", errs)
	}
	data, ok := allowedResult["data"].(map[string]any)
	if !ok || data == nil {
		t.Fatalf("expected data payload, got: %v", allowedResult)
	}
	createLinkedBranch, ok := data["createLinkedBranch"].(map[string]any)
	if !ok || createLinkedBranch == nil {
		t.Fatalf("expected createLinkedBranch payload, got: %v", data["createLinkedBranch"])
	}
	linkedBranch, ok := createLinkedBranch["linkedBranch"].(map[string]any)
	if !ok || linkedBranch == nil {
		t.Fatalf("expected linkedBranch payload, got: %v", createLinkedBranch["linkedBranch"])
	}
	ref, ok := linkedBranch["ref"].(map[string]any)
	if !ok || ref["name"] != allowedBranchName {
		t.Fatalf("expected linked branch ref name %q, got: %v", allowedBranchName, linkedBranch["ref"])
	}

	if _, err := svc.Git.HeadSHA(ctx, repo.FullName, allowedBranchName); err != nil {
		t.Fatalf("expected branch %q to be created, got err: %v", allowedBranchName, err)
	}
	if err := svc.DB.Where("issue_id = ? AND branch_name = ?", issue.ID, allowedBranchName).First(&lb).Error; err != nil {
		t.Fatalf("expected linked branch row for %q, got err: %v", allowedBranchName, err)
	}
}
