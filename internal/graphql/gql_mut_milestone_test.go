package graphql_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

// =============================================================================
// Milestone Mutation Tests
// =============================================================================

// TestGraphQL_CreateMilestone_Success tests creating a milestone via createMilestone mutation.
func TestGraphQL_CreateMilestone_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "milestone-create-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	mut := `
	mutation($input: CreateMilestoneInput!) {
		createMilestone(input: $input) {
			milestone {
				id
				number
				title
				description
				state
				dueOn
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"repositoryId": fmt.Sprintf("Repository_%d", repo.ID),
			"title":        "Test Milestone",
			"description":  "Test milestone description",
			"state":        "open",
		},
	})

	createResult := data["createMilestone"].(map[string]any)
	milestone := createResult["milestone"].(map[string]any)

	if milestone["title"] != "Test Milestone" {
		t.Errorf("title: got %v, want Test Milestone", milestone["title"])
	}
	if milestone["description"] != "Test milestone description" {
		t.Errorf("description: got %v, want Test milestone description", milestone["description"])
	}
	if milestone["state"] != "OPEN" {
		t.Errorf("state: got %v, want OPEN", milestone["state"])
	}
	if milestone["number"] == nil {
		t.Errorf("number: got nil, want non-nil")
	}

	// Verify milestone exists in database.
	stored, err := svc.GetMilestoneByNumber(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), int(milestone["number"].(float64)))
	if err != nil {
		t.Fatalf("GetMilestoneByNumber: %v", err)
	}
	if stored.Title != "Test Milestone" {
		t.Errorf("stored title: got %v, want Test Milestone", stored.Title)
	}
}

// TestGraphQL_CreateMilestone_WithDueOn tests creating a milestone with a due date.
func TestGraphQL_CreateMilestone_WithDueOn(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "milestone-dueon-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	dueOn := time.Now().AddDate(0, 1, 0).Format(time.RFC3339)

	mut := `
	mutation($input: CreateMilestoneInput!) {
		createMilestone(input: $input) {
			milestone {
				title
				dueOn
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"repositoryId": fmt.Sprintf("Repository_%d", repo.ID),
			"title":        "Milestone with Due Date",
			"dueOn":        dueOn,
		},
	})

	createResult := data["createMilestone"].(map[string]any)
	milestone := createResult["milestone"].(map[string]any)

	if milestone["dueOn"] == nil {
		t.Errorf("dueOn: got nil, want non-nil")
	}
}

// TestGraphQL_CreateMilestone_RequiresTitle tests that title is required.
func TestGraphQL_CreateMilestone_RequiresTitle(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "milestone-title-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	mut := `
	mutation($input: CreateMilestoneInput!) {
		createMilestone(input: $input) {
			milestone {
				id
			}
		}
	}`
	res := doRawGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"repositoryId": fmt.Sprintf("Repository_%d", repo.ID),
			"title":        "",
		},
	})

	if res["errors"] == nil {
		t.Errorf("expected gql errors for missing title, got none")
	}
}

// TestGraphQL_UpdateMilestone_Success tests updating a milestone via updateMilestone mutation.
func TestGraphQL_UpdateMilestone_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "milestone-update-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	m, err := svc.CreateMilestone(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), "Original Title", "Original description", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}

	mut := `
	mutation($input: UpdateMilestoneInput!) {
		updateMilestone(input: $input) {
			milestone {
				id
				title
				description
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"milestoneId": fmt.Sprintf("Milestone_%d", m.ID),
			"title":       "Updated Title",
			"description": "Updated description",
		},
	})

	updateResult := data["updateMilestone"].(map[string]any)
	milestone := updateResult["milestone"].(map[string]any)

	if milestone["title"] != "Updated Title" {
		t.Errorf("title: got %v, want Updated Title", milestone["title"])
	}
	if milestone["description"] != "Updated description" {
		t.Errorf("description: got %v, want Updated description", milestone["description"])
	}

	// Verify in database.
	stored, err := svc.GetMilestoneByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMilestoneByID: %v", err)
	}
	if stored.Title != "Updated Title" {
		t.Errorf("stored title: got %v, want Updated Title", stored.Title)
	}
}

// TestGraphQL_UpdateMilestone_State tests updating milestone state.
func TestGraphQL_UpdateMilestone_State(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "milestone-state-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	m, err := svc.CreateMilestone(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), "State Test", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}

	mut := `
	mutation($input: UpdateMilestoneInput!) {
		updateMilestone(input: $input) {
			milestone {
				state
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"milestoneId": fmt.Sprintf("Milestone_%d", m.ID),
			"state":       "closed",
		},
	})

	updateResult := data["updateMilestone"].(map[string]any)
	milestone := updateResult["milestone"].(map[string]any)

	if milestone["state"] != "CLOSED" {
		t.Errorf("state: got %v, want CLOSED", milestone["state"])
	}
}

// TestGraphQL_DeleteMilestone_Success tests deleting a milestone via deleteMilestone mutation.
func TestGraphQL_DeleteMilestone_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "milestone-delete-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	m, err := svc.CreateMilestone(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), "To Delete", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}

	mut := `
	mutation($input: DeleteMilestoneInput!) {
		deleteMilestone(input: $input)
	}`
	res := doRawGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"milestoneId": fmt.Sprintf("Milestone_%d", m.ID),
		},
	})

	// deleteMilestone returns null on success (void mutation)
	if res["errors"] != nil {
		t.Errorf("unexpected gql errors: %v", res["errors"])
	}

	// Verify milestone is deleted.
	_, err = svc.GetMilestoneByID(ctx, m.ID)
	if err == nil {
		t.Errorf("milestone should be deleted but still exists")
	}
}

// TestGraphQL_CloseMilestone_Success tests closing a milestone via closeMilestone mutation.
func TestGraphQL_CloseMilestone_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "milestone-close-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	m, err := svc.CreateMilestone(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), "To Close", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}

	mut := `
	mutation($input: CloseMilestoneInput!) {
		closeMilestone(input: $input) {
			milestone {
				id
				state
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"milestoneId": fmt.Sprintf("Milestone_%d", m.ID),
		},
	})

	closeResult := data["closeMilestone"].(map[string]any)
	milestone := closeResult["milestone"].(map[string]any)

	if milestone["state"] != "CLOSED" {
		t.Errorf("state: got %v, want CLOSED", milestone["state"])
	}

	// Verify in database.
	stored, err := svc.GetMilestoneByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMilestoneByID: %v", err)
	}
	if stored.State != db.StateClosed {
		t.Errorf("stored state: got %v, want %s", stored.State, db.StateClosed)
	}
}

// TestGraphQL_ReopenMilestone_Success tests reopening a milestone via reopenMilestone mutation.
func TestGraphQL_ReopenMilestone_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "milestone-reopen-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	m, err := svc.CreateMilestone(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), "To Reopen", "", "closed")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}

	mut := `
	mutation($input: ReopenMilestoneInput!) {
		reopenMilestone(input: $input) {
			milestone {
				id
				state
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"milestoneId": fmt.Sprintf("Milestone_%d", m.ID),
		},
	})

	reopenResult := data["reopenMilestone"].(map[string]any)
	milestone := reopenResult["milestone"].(map[string]any)

	if milestone["state"] != "OPEN" {
		t.Errorf("state: got %v, want OPEN", milestone["state"])
	}

	// Verify in database.
	stored, err := svc.GetMilestoneByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMilestoneByID: %v", err)
	}
	if stored.State != db.StateOpen {
		t.Errorf("stored state: got %v, want %s", stored.State, db.StateOpen)
	}
}

// TestGraphQL_UpdateMilestone_InvalidState tests updating with invalid state.
func TestGraphQL_UpdateMilestone_InvalidState(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "milestone-invalid-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	m, err := svc.CreateMilestone(ctx, fmt.Sprintf("%s/%s", u.Login, repo.Name), "Invalid State Test", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}

	mut := `
	mutation($input: UpdateMilestoneInput!) {
		updateMilestone(input: $input) {
			milestone {
				state
			}
		}
	}`
	res := doRawGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"milestoneId": fmt.Sprintf("Milestone_%d", m.ID),
			"state":       "invalid_state",
		},
	})

	if res["errors"] == nil {
		t.Errorf("expected gql errors for invalid state, got none")
	}
}

// TestGraphQL_CreateMilestone_InvalidState tests creating with invalid state.
func TestGraphQL_CreateMilestone_InvalidState(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "milestone-invalid-state-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	mut := `
	mutation($input: CreateMilestoneInput!) {
		createMilestone(input: $input) {
			milestone {
				id
			}
		}
	}`
	res := doRawGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"repositoryId": fmt.Sprintf("Repository_%d", repo.ID),
			"title":        "Invalid State Milestone",
			"state":        "invalid_state",
		},
	})

	if res["errors"] == nil {
		t.Errorf("expected gql errors for invalid state, got none")
	}
}
