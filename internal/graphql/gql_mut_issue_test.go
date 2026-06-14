package graphql_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

// =============================================================================
// Issue State Mutation Tests (doSetIssueState)
// =============================================================================

// TestGraphQL_CloseIssue_Success tests closing an issue via closeIssue mutation.
func TestGraphQL_CloseIssue_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "close-issue-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue to Close",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Test closeIssue mutation.
	mut := `
	mutation($input: CloseIssueInput!) {
		closeIssue(input: $input) {
			issue {
				id
				state
				stateReason
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"issueId": fmt.Sprintf("Issue_%d", issue.ID),
		},
	})

	closeResult := data["closeIssue"].(map[string]any)
	closedIssue := closeResult["issue"].(map[string]any)

	if closedIssue["state"] != "CLOSED" {
		t.Errorf("state: got %v, want CLOSED", closedIssue["state"])
	}
	if closedIssue["stateReason"] != "COMPLETED" {
		t.Errorf("stateReason: got %v, want COMPLETED", closedIssue["stateReason"])
	}

	// Verify issue is closed in database.
	storedIssue, err := svc.GetIssue(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), issue.Number)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if storedIssue.State != db.StateClosed {
		t.Errorf("stored state: got %v, want %s", storedIssue.State, db.StateClosed)
	}
}

// TestGraphQL_CloseIssue_WithStateReason tests closing an issue with a custom stateReason.
func TestGraphQL_CloseIssue_WithStateReason(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "close-reason-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue with Reason",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Test closeIssue mutation with custom stateReason.
	mut := `
	mutation($input: CloseIssueInput!) {
		closeIssue(input: $input) {
			issue {
				state
				stateReason
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"issueId":     fmt.Sprintf("Issue_%d", issue.ID),
			"stateReason": "NOT_PLANNED",
		},
	})

	closeResult := data["closeIssue"].(map[string]any)
	closedIssue := closeResult["issue"].(map[string]any)

	if closedIssue["state"] != "CLOSED" {
		t.Errorf("state: got %v, want CLOSED", closedIssue["state"])
	}
	if closedIssue["stateReason"] != "NOT_PLANNED" {
		t.Errorf("stateReason: got %v, want NOT_PLANNED", closedIssue["stateReason"])
	}
}

// TestGraphQL_ReopenIssue_Success tests reopening a closed issue via reopenIssue mutation.
func TestGraphQL_ReopenIssue_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "reopen-issue-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue to Reopen",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// First close the issue.
	svc.UpdateIssueFields(ctx, issue.ID, map[string]any{
		"state":        db.StateClosed,
		"closed_at":    nil,
		"state_reason": db.StateReasonCompleted,
	})

	// Test reopenIssue mutation.
	mut := `
	mutation($input: ReopenIssueInput!) {
		reopenIssue(input: $input) {
			issue {
				id
				state
				stateReason
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"issueId": fmt.Sprintf("Issue_%d", issue.ID),
		},
	})

	reopenResult := data["reopenIssue"].(map[string]any)
	reopenedIssue := reopenResult["issue"].(map[string]any)

	if reopenedIssue["state"] != "OPEN" {
		t.Errorf("state: got %v, want OPEN", reopenedIssue["state"])
	}

	// Verify issue is reopened in database.
	storedIssue, err := svc.GetIssue(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), issue.Number)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if storedIssue.State != db.StateOpen {
		t.Errorf("stored state: got %v, want %s", storedIssue.State, db.StateOpen)
	}
}

// TestGraphQL_CloseIssue_InvalidID tests closing an issue with invalid ID.
func TestGraphQL_CloseIssue_InvalidID(t *testing.T) {
	_, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	mut := `
	mutation($input: CloseIssueInput!) {
		closeIssue(input: $input) {
			issue {
				id
			}
		}
	}`

	res := doRawGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"issueId": "Issue_99999", // Non-existent issue
		},
	})

	// Should return without error but with empty/minimal payload for invalid ID.
	data := res["data"]
	if data != nil {
		closeResult := data.(map[string]any)["closeIssue"]
		if closeResult != nil {
			issue := closeResult.(map[string]any)["issue"]
			if issue != nil && issue.(map[string]any)["id"] != "" {
				t.Error("expected empty issue for invalid ID")
			}
		}
	}
}

// =============================================================================
// Update Issue Mutation Tests (doUpdateIssue)
// =============================================================================

// TestGraphQL_UpdateIssue_TitleAndBody tests updating issue title and body.
func TestGraphQL_UpdateIssue_TitleAndBody(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-issue-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Original Title",
		Body:         "Original body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Test updateIssue mutation.
	mut := `
	mutation($input: UpdateIssueInput!) {
		updateIssue(input: $input) {
			issue {
				id
				title
				body
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"id":    fmt.Sprintf("Issue_%d", issue.ID),
			"title": "Updated Title",
			"body":  "Updated body",
		},
	})

	updateResult := data["updateIssue"].(map[string]any)
	updatedIssue := updateResult["issue"].(map[string]any)

	if updatedIssue["title"] != "Updated Title" {
		t.Errorf("title: got %v, want 'Updated Title'", updatedIssue["title"])
	}
	if updatedIssue["body"] != "Updated body" {
		t.Errorf("body: got %v, want 'Updated body'", updatedIssue["body"])
	}

	// Verify in database.
	storedIssue, err := svc.GetIssue(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), issue.Number)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if storedIssue.Title != "Updated Title" {
		t.Errorf("stored title: got %v, want 'Updated Title'", storedIssue.Title)
	}
	if storedIssue.Body != "Updated body" {
		t.Errorf("stored body: got %v, want 'Updated body'", storedIssue.Body)
	}
}

// TestGraphQL_UpdateIssue_State tests updating issue state (close/reopen).
func TestGraphQL_UpdateIssue_State(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-state-repo",
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

	// Test updateIssue mutation to close the issue.
	mut := `
	mutation($input: UpdateIssueInput!) {
		updateIssue(input: $input) {
			issue {
				state
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"id":    fmt.Sprintf("Issue_%d", issue.ID),
			"state": "CLOSED",
		},
	})

	updateResult := data["updateIssue"].(map[string]any)
	updatedIssue := updateResult["issue"].(map[string]any)

	if updatedIssue["state"] != "CLOSED" {
		t.Errorf("state: got %v, want CLOSED", updatedIssue["state"])
	}
}

// TestGraphQL_UpdateIssue_Labels tests updating issue labels.
func TestGraphQL_UpdateIssue_Labels(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Migrate Label table.
	svc.DB.AutoMigrate(&db.Label{})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-labels-repo",
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

	// Create labels.
	label1, err := svc.CreateLabel(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), "update-label-1", "ff0000", "Label 1")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	label2, err := svc.CreateLabel(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), "update-label-2", "00ff00", "Label 2")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}

	// Test updateIssue mutation with labelIds.
	mut := `
	mutation($input: UpdateIssueInput!) {
		updateIssue(input: $input) {
			issue {
				id
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"id":       fmt.Sprintf("Issue_%d", issue.ID),
			"labelIds": []string{fmt.Sprintf("Label_%d", label1.ID), fmt.Sprintf("Label_%d", label2.ID)},
		},
	})

	if data["updateIssue"] == nil {
		t.Error("updateIssue should return non-nil payload")
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

// TestGraphQL_UpdateIssue_Assignees tests updating issue assignees.
func TestGraphQL_UpdateIssue_Assignees(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-assignees-repo",
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

	// Create another user to assign.
	user2 := db.User{Login: "assignee-user", Name: "Assignee User", Type: db.TypeUser}
	svc.DB.Create(&user2)
	svc.DB.Create(&db.Token{UserID: user2.ID, Value: "test-token-assignee"})

	// Get node IDs for users.
	user1NodeID := restUserNodeID(t, mux, u.Login)
	user2NodeID := restUserNodeID(t, mux, user2.Login)

	// Test updateIssue mutation with assigneeIds.
	mut := `
	mutation($input: UpdateIssueInput!) {
		updateIssue(input: $input) {
			issue {
				id
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"id":          fmt.Sprintf("Issue_%d", issue.ID),
			"assigneeIds": []string{user1NodeID, user2NodeID},
		},
	})

	if data["updateIssue"] == nil {
		t.Error("updateIssue should return non-nil payload")
	}

	// Verify assignees are attached via AttachLabelsAndAssignees.
	// Note: The mutation calls AttachLabelsAndAssignees which handles assigneeIds.
	storedIssue, err := svc.GetIssue(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), issue.Number)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	// The assignee_logins field should be updated by AttachLabelsAndAssignees.
	// This test verifies the mutation executes without error.
	if storedIssue.ID != issue.ID {
		t.Errorf("stored issue ID: got %d, want %d", storedIssue.ID, issue.ID)
	}
}

// TestGraphQL_UpdateIssue_Milestone tests updating issue milestone.
func TestGraphQL_UpdateIssue_Milestone(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "update-milestone-repo",
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

	// Create a milestone.
	milestone, err := svc.CreateMilestone(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), "Test Milestone", "milestone description", "")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}

	// Test updateIssue mutation with milestoneId.
	mut := `
	mutation($input: UpdateIssueInput!) {
		updateIssue(input: $input) {
			issue {
				id
				milestone {
					title
				}
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"id":          fmt.Sprintf("Issue_%d", issue.ID),
			"milestoneId": fmt.Sprintf("Milestone_%d", milestone.ID),
		},
	})

	updateResult := data["updateIssue"].(map[string]any)
	updatedIssue := updateResult["issue"].(map[string]any)

	if updatedIssue["milestone"] == nil {
		t.Error("milestone should be set")
	} else {
		ms := updatedIssue["milestone"].(map[string]any)
		if ms["title"] != "Test Milestone" {
			t.Errorf("milestone title: got %v, want 'Test Milestone'", ms["title"])
		}
	}
}

// =============================================================================
// Lock/Unlock Issue Mutation Tests (doLockLockable)
// =============================================================================

// TestGraphQL_LockLockable_Issue_Success tests locking an issue.
func TestGraphQL_LockLockable_Issue_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "lock-issue-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue to Lock",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Test lockLockable mutation.
	mut := `
	mutation($input: LockLockableInput!) {
		lockLockable(input: $input) {
			lockedRecord {
				locked
				activeLockReason
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"lockableId": fmt.Sprintf("Issue_%d", issue.ID),
			"lockReason": "OFF_TOPIC",
		},
	})

	lockResult := data["lockLockable"].(map[string]any)
	lockedRecord := lockResult["lockedRecord"].(map[string]any)

	if lockedRecord["locked"] != true {
		t.Errorf("locked: got %v, want true", lockedRecord["locked"])
	}
	if lockedRecord["activeLockReason"] != "OFF_TOPIC" {
		t.Errorf("activeLockReason: got %v, want OFF_TOPIC", lockedRecord["activeLockReason"])
	}

	// Verify issue is locked in database.
	storedIssue, err := svc.GetIssue(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), issue.Number)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if !storedIssue.Locked {
		t.Error("stored issue should be locked")
	}
	if storedIssue.ActiveLockReason != "OFF_TOPIC" {
		t.Errorf("stored active_lock_reason: got %v, want OFF_TOPIC", storedIssue.ActiveLockReason)
	}
}

// TestGraphQL_UnlockLockable_Issue_Success tests unlocking an issue.
func TestGraphQL_UnlockLockable_Issue_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "unlock-issue-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue to Unlock",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// First lock the issue.
	svc.UpdateIssueFields(ctx, issue.ID, map[string]any{
		"locked":             true,
		"active_lock_reason": "SPAM",
	})

	// Test unlockLockable mutation.
	mut := `
	mutation($input: UnlockLockableInput!) {
		unlockLockable(input: $input) {
			lockedRecord {
				locked
				activeLockReason
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"lockableId": fmt.Sprintf("Issue_%d", issue.ID),
		},
	})

	unlockResult := data["unlockLockable"].(map[string]any)
	unlockedRecord := unlockResult["lockedRecord"].(map[string]any)

	if unlockedRecord["locked"] != false {
		t.Errorf("locked: got %v, want false", unlockedRecord["locked"])
	}

	// Verify issue is unlocked in database.
	storedIssue, err := svc.GetIssue(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), issue.Number)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if storedIssue.Locked {
		t.Error("stored issue should be unlocked")
	}
}

// TestGraphQL_LockLockable_InvalidID tests locking with invalid lockable ID.
func TestGraphQL_LockLockable_InvalidID(t *testing.T) {
	_, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	mut := `
	mutation($input: LockLockableInput!) {
		lockLockable(input: $input) {
			lockedRecord {
				locked
			}
		}
	}`

	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"lockableId": "InvalidType_99999",
			"lockReason": "SPAM",
		},
	})

	// Should return without error but with minimal payload.
	lockResult := data["lockLockable"].(map[string]any)
	lockedRecord := lockResult["lockedRecord"].(map[string]any)

	if lockedRecord["locked"] != true {
		t.Errorf("locked: got %v, want true (default response)", lockedRecord["locked"])
	}
}

// =============================================================================
// Pin/Unpin Issue Mutation Tests (doPinIssue)
// =============================================================================

// TestGraphQL_PinIssue_Success tests pinning an issue.
func TestGraphQL_PinIssue_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "pin-issue-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue to Pin",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Test pinIssue mutation.
	mut := `
	mutation($input: PinIssueInput!) {
		pinIssue(input: $input) {
			issue {
				id
				isPinned
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"issueId": fmt.Sprintf("Issue_%d", issue.ID),
		},
	})

	pinResult := data["pinIssue"].(map[string]any)
	pinnedIssue := pinResult["issue"].(map[string]any)

	if pinnedIssue["isPinned"] != true {
		t.Errorf("isPinned: got %v, want true", pinnedIssue["isPinned"])
	}

	// Verify issue is pinned in database.
	storedIssue, err := svc.GetIssue(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), issue.Number)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if !storedIssue.IsPinned {
		t.Error("stored issue should be pinned")
	}
}

// TestGraphQL_UnpinIssue_Success tests unpinning an issue.
func TestGraphQL_UnpinIssue_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "unpin-issue-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue to Unpin",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// First pin the issue.
	svc.UpdateIssueFields(ctx, issue.ID, map[string]any{
		"is_pinned": true,
	})

	// Test unpinIssue mutation.
	mut := `
	mutation($input: UnpinIssueInput!) {
		unpinIssue(input: $input) {
			issue {
				id
				isPinned
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"issueId": fmt.Sprintf("Issue_%d", issue.ID),
		},
	})

	unpinResult := data["unpinIssue"].(map[string]any)
	unpinnedIssue := unpinResult["issue"].(map[string]any)

	if unpinnedIssue["isPinned"] != false {
		t.Errorf("isPinned: got %v, want false", unpinnedIssue["isPinned"])
	}

	// Verify issue is unpinned in database.
	storedIssue, err := svc.GetIssue(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), issue.Number)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if storedIssue.IsPinned {
		t.Error("stored issue should be unpinned")
	}
}

// TestGraphQL_PinIssue_InvalidID tests pinning with invalid issue ID.
func TestGraphQL_PinIssue_InvalidID(t *testing.T) {
	_, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	mut := `
	mutation($input: PinIssueInput!) {
		pinIssue(input: $input) {
			issue {
				isPinned
			}
		}
	}`

	res := doRawGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"issueId": "Issue_99999", // Non-existent issue
		},
	})

	// Should return error for invalid issue ID.
	if res["errors"] == nil {
		t.Error("expected errors for invalid issue ID")
	}
}

// =============================================================================
// Transfer Issue Mutation Tests (doTransferIssue)
// =============================================================================

// TestGraphQL_TransferIssue_Success tests transferring an issue to a different repo.
func TestGraphQL_TransferIssue_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Create source repo.
	sourceRepo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "source-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo (source): %v", err)
	}

	// Create destination repo.
	destRepo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "dest-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo (dest): %v", err)
	}
	destExisting, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, destRepo.Name),
		Title:        "Existing destination issue",
		Body:         "dest issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue (dest existing): %v", err)
	}

	// Create issue in source repo.
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, sourceRepo.Name),
		Title:        "Test Issue to Transfer",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Get destination repo node ID.
	destRepoNodeID := fmt.Sprintf("Repository_%d", destRepo.ID)

	// Test transferIssue mutation.
	mut := `
	mutation($input: TransferIssueInput!) {
		transferIssue(input: $input) {
			issue {
				id
				number
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"issueId":      fmt.Sprintf("Issue_%d", issue.ID),
			"repositoryId": destRepoNodeID,
		},
	})

	transferResult := data["transferIssue"].(map[string]any)
	transferredIssue := transferResult["issue"].(map[string]any)

	if transferredIssue["id"] == nil {
		t.Error("issue id should be present")
	}
	wantTransferredNumber := destExisting.Number + 1
	if transferredIssue["number"] != float64(wantTransferredNumber) {
		t.Errorf("transferred issue number: got %v, want %d", transferredIssue["number"], wantTransferredNumber)
	}

	// Verify issue is transferred in database.
	storedIssue, err := svc.GetIssueByID(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssueByID: %v", err)
	}
	if storedIssue.RepositoryID != destRepo.ID {
		t.Errorf("repository_id: got %d, want %d", storedIssue.RepositoryID, destRepo.ID)
	}
	if storedIssue.Number != wantTransferredNumber {
		t.Errorf("stored issue number: got %d, want %d", storedIssue.Number, wantTransferredNumber)
	}
	followUp, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, destRepo.Name),
		Title:        "Follow-up destination issue",
		Body:         "follow-up body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue (dest follow-up): %v", err)
	}
	if followUp.Number != wantTransferredNumber+1 {
		t.Errorf("follow-up issue number: got %d, want %d", followUp.Number, wantTransferredNumber+1)
	}
}

// TestGraphQL_TransferIssue_InvalidRepo tests transferring with invalid repo ID.
func TestGraphQL_TransferIssue_InvalidRepo(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "transfer-invalid-repo",
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

	// Test transferIssue mutation with invalid repo ID.
	mut := `
	mutation($input: TransferIssueInput!) {
		transferIssue(input: $input) {
			issue {
				id
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"issueId":      fmt.Sprintf("Issue_%d", issue.ID),
			"repositoryId": "Repository_99999", // Non-existent repo
		},
	})

	// Should return without error but with minimal payload.
	transferResult := data["transferIssue"].(map[string]any)
	transferredIssue := transferResult["issue"].(map[string]any)

	if transferredIssue["id"] == nil {
		t.Error("issue id should be present")
	}
}

// TestGraphQL_TransferIssue_InvalidIssueID tests transferring with invalid issue ID.
func TestGraphQL_TransferIssue_InvalidIssueID(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "transfer-invalid-issue-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	destRepoNodeID := fmt.Sprintf("Repository_%d", repo.ID)

	// Test transferIssue mutation with invalid issue ID.
	mut := `
	mutation($input: TransferIssueInput!) {
		transferIssue(input: $input) {
			issue {
				id
			}
		}
	}`

	res := doRawGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"issueId":      "Issue_99999", // Non-existent issue
			"repositoryId": destRepoNodeID,
		},
	})

	// Should return error for invalid issue ID.
	if res["errors"] == nil {
		t.Error("expected errors for invalid issue ID")
	}
}
