package graphql_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

// TestGraphQL_ProjectV2_ItemAddRemove tests addProjectV2ItemById and deleteProjectV2Item mutations.
func TestGraphQL_ProjectV2_ItemAddRemove(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Create a project.
	proj, err := svc.CreateProject(ctx, u.Login, "Test Board")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Create a repo and issue to add to the project.
	repo, _ := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "item-test-repo",
		AutoInit:   true,
	})
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Test Issue for Project",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	issueNodeID := fmt.Sprintf("Issue_%d", issue.ID)
	projNodeID := fmt.Sprintf("Project_%d", proj.ID)

	// Test addProjectV2ItemById mutation.
	addMut := `
	mutation($input: AddProjectV2ItemByIdInput!) {
		addProjectV2ItemById(input: $input) {
			item {
				id
				type
				content {
					... on Issue {
						id
						number
						title
					}
				}
			}
		}
	}`
	addData := doGql(t, mux, addMut, map[string]any{
		"input": map[string]any{
			"projectId": projNodeID,
			"contentId": issueNodeID,
		},
	})

	addResult := addData["addProjectV2ItemById"].(map[string]any)
	item := addResult["item"].(map[string]any)

	if item["type"] != "ISSUE" {
		t.Errorf("item type: got %v, want ISSUE", item["type"])
	}
	content := item["content"].(map[string]any)
	if content["number"] != float64(issue.Number) {
		t.Errorf("content number: got %v, want %d", content["number"], issue.Number)
	}
	if content["title"] != "Test Issue for Project" {
		t.Errorf("content title: got %v, want 'Test Issue for Project'", content["title"])
	}
	itemID := item["id"].(string)
	if itemID == "" {
		t.Error("item id should be non-empty")
	}

	// Verify the item appears in the project query.
	q := `
	query($login: String!, $number: Int!) {
		organization(login: $login) {
			projectV2(number: $number) {
				items(first: 10) {
					totalCount
					nodes {
						id
						type
					}
				}
			}
		}
	}`
	qData := doGql(t, mux, q, map[string]any{
		"login":  u.Login,
		"number": float64(proj.Number),
	})
	org := qData["organization"].(map[string]any)
	projData := org["projectV2"].(map[string]any)
	items := projData["items"].(map[string]any)
	totalCount := items["totalCount"].(float64)
	if totalCount != 1 {
		t.Errorf("items totalCount after add: got %v, want 1", totalCount)
	}

	// Test deleteProjectV2Item mutation.
	delMut := `
	mutation($input: DeleteProjectV2ItemInput!) {
		deleteProjectV2Item(input: $input) {
			deletedItemId
		}
	}`
	delData := doGql(t, mux, delMut, map[string]any{
		"input": map[string]any{
			"itemId": itemID,
		},
	})

	delResult := delData["deleteProjectV2Item"].(map[string]any)
	deletedItemID := delResult["deletedItemId"].(string)
	if deletedItemID != itemID {
		t.Errorf("deletedItemId: got %v, want %v", deletedItemID, itemID)
	}

	// Verify the item is removed from the project query.
	qData2 := doGql(t, mux, q, map[string]any{
		"login":  u.Login,
		"number": float64(proj.Number),
	})
	org2 := qData2["organization"].(map[string]any)
	projData2 := org2["projectV2"].(map[string]any)
	items2 := projData2["items"].(map[string]any)
	totalCount2 := items2["totalCount"].(float64)
	if totalCount2 != 0 {
		t.Errorf("items totalCount after delete: got %v, want 0", totalCount2)
	}
}

// TestGraphQL_ProjectV2_ItemFieldValueUpdate tests updateProjectV2ItemFieldValue and clearProjectV2ItemFieldValue mutations.
func TestGraphQL_ProjectV2_ItemFieldValueUpdate(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Create a project with a Status field (SINGLE_SELECT).
	proj, err := svc.CreateProject(ctx, u.Login, "Status Board")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	fieldStatus := &db.ProjectField{
		ProjectID: proj.ID,
		Name:      "Status",
		DataType:  "SINGLE_SELECT",
		Options:   `[{"id":"opt-todo","name":"Todo"},{"id":"opt-done","name":"Done"}]`,
	}
	if err := svc.CreateProjectField(ctx, fieldStatus); err != nil {
		t.Fatalf("CreateProjectField: %v", err)
	}

	// Create a draft issue item.
	item := &db.ProjectItem{
		ProjectID:  proj.ID,
		Type:       "DRAFT_ISSUE",
		DraftTitle: "Task to track",
		DraftBody:  "task body",
	}
	if err := svc.CreateProjectItem(ctx, item); err != nil {
		t.Fatalf("CreateProjectItem: %v", err)
	}

	itemNodeID := fmt.Sprintf("ProjectItem_%d", item.ID)
	fieldNodeID := fmt.Sprintf("ProjectField_%d", fieldStatus.ID)

	// Test updateProjectV2ItemFieldValue mutation - set status to "Todo".
	updateMut := `
	mutation($input: UpdateProjectV2ItemFieldValueInput!) {
		updateProjectV2ItemFieldValue(input: $input) {
			projectV2Item {
				id
				type
			}
		}
	}`
	updateData := doGql(t, mux, updateMut, map[string]any{
		"input": map[string]any{
			"itemId":  itemNodeID,
			"fieldId": fieldNodeID,
			"value": map[string]any{
				"singleSelectOptionId": "opt-todo",
			},
		},
	})

	updateResult := updateData["updateProjectV2ItemFieldValue"].(map[string]any)
	updatedItem := updateResult["projectV2Item"].(map[string]any)
	if updatedItem["id"] != itemNodeID {
		t.Errorf("updated item id: got %v, want %v", updatedItem["id"], itemNodeID)
	}

	// Verify the field value is persisted by re-fetching the item from DB.
	updatedItemDB, err := svc.GetProjectItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetProjectItem: %v", err)
	}
	if updatedItemDB.FieldValues == "" {
		t.Fatal("FieldValues should not be empty after update")
	}
	// Verify the actual stored value matches what was set.
	// Use quoted string to avoid partial matches (e.g., "opt-todo-item").
	if !strings.Contains(updatedItemDB.FieldValues, `"opt-todo"`) {
		t.Errorf("FieldValues should contain '\"opt-todo\"', got: %s", updatedItemDB.FieldValues)
	}

	// Test clearProjectV2ItemFieldValue mutation.
	clearMut := `
	mutation($input: ClearProjectV2ItemFieldValueInput!) {
		clearProjectV2ItemFieldValue(input: $input) {
			projectV2Item {
				id
				type
			}
		}
	}`
	clearData := doGql(t, mux, clearMut, map[string]any{
		"input": map[string]any{
			"itemId":  itemNodeID,
			"fieldId": fieldNodeID,
		},
	})

	clearResult := clearData["clearProjectV2ItemFieldValue"].(map[string]any)
	clearedItem := clearResult["projectV2Item"].(map[string]any)
	if clearedItem["id"] != itemNodeID {
		t.Errorf("cleared item id: got %v, want %v", clearedItem["id"], itemNodeID)
	}

	// Verify the field value is cleared.
	clearedItemDB, err := svc.GetProjectItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetProjectItem after clear: %v", err)
	}
	// After clearing the only field, FieldValues should be empty or "{}".
	if clearedItemDB.FieldValues != "" && clearedItemDB.FieldValues != "{}" {
		t.Errorf("FieldValues after clear: got %v, want empty or {}", clearedItemDB.FieldValues)
	}
}

// TestGraphQL_ProjectV2_ItemArchiveUnarchive tests archiveProjectV2Item and unarchiveProjectV2Item mutations.
func TestGraphQL_ProjectV2_ItemArchiveUnarchive(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Create a project.
	proj, err := svc.CreateProject(ctx, u.Login, "Archive Board")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Create a draft issue item.
	item := &db.ProjectItem{
		ProjectID:  proj.ID,
		Type:       "DRAFT_ISSUE",
		DraftTitle: "Task to archive",
		DraftBody:  "task body",
	}
	if err := svc.CreateProjectItem(ctx, item); err != nil {
		t.Fatalf("CreateProjectItem: %v", err)
	}

	itemNodeID := fmt.Sprintf("ProjectItem_%d", item.ID)

	// Verify initial state: not archived.
	if item.Archived {
		t.Fatal("item should not be archived initially")
	}

	// Test archiveProjectV2Item mutation.
	archiveMut := `
	mutation($input: ArchiveProjectV2ItemInput!) {
		archiveProjectV2Item(input: $input) {
			item {
				id
				isArchived
			}
		}
	}`
	archiveData := doGql(t, mux, archiveMut, map[string]any{
		"input": map[string]any{
			"itemId": itemNodeID,
		},
	})

	archiveResult := archiveData["archiveProjectV2Item"].(map[string]any)
	archivedItem := archiveResult["item"].(map[string]any)
	if archivedItem["id"] != itemNodeID {
		t.Errorf("archived item id: got %v, want %v", archivedItem["id"], itemNodeID)
	}
	if archivedItem["isArchived"] != true {
		t.Errorf("archived item isArchived: got %v, want true", archivedItem["isArchived"])
	}

	// Verify the item is archived in DB.
	archivedItemDB, err := svc.GetProjectItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetProjectItem after archive: %v", err)
	}
	if !archivedItemDB.Archived {
		t.Error("item should be archived in DB")
	}

	// Test unarchiveProjectV2Item mutation.
	unarchiveMut := `
	mutation($input: UnarchiveProjectV2ItemInput!) {
		unarchiveProjectV2Item(input: $input) {
			item {
				id
				isArchived
			}
		}
	}`
	unarchiveData := doGql(t, mux, unarchiveMut, map[string]any{
		"input": map[string]any{
			"itemId": itemNodeID,
		},
	})

	unarchiveResult := unarchiveData["unarchiveProjectV2Item"].(map[string]any)
	unarchivedItem := unarchiveResult["item"].(map[string]any)
	if unarchivedItem["id"] != itemNodeID {
		t.Errorf("unarchived item id: got %v, want %v", unarchivedItem["id"], itemNodeID)
	}
	if unarchivedItem["isArchived"] != false {
		t.Errorf("unarchived item isArchived: got %v, want false", unarchivedItem["isArchived"])
	}

	// Verify the item is unarchived in DB.
	unarchivedItemDB, err := svc.GetProjectItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetProjectItem after unarchive: %v", err)
	}
	if unarchivedItemDB.Archived {
		t.Error("item should not be archived in DB after unarchive")
	}
}

// TestGraphQL_ProjectV2_BoardLifecycle tests a client-realistic board flow:
// create project -> add issue -> move between statuses -> verify query result.
func TestGraphQL_ProjectV2_BoardLifecycle(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Step 1: Create a project under the test user.
	createMut := `
	mutation($input: CreateProjectV2Input!) {
		createProjectV2(input: $input) {
			projectV2 {
				id
				title
				number
			}
		}
	}`
	createData := doGql(t, mux, createMut, map[string]any{
		"input": map[string]any{
			"title":   "Sprint Board",
			"ownerId": fmt.Sprintf("User_%d", u.ID),
		},
	})
	createResult := createData["createProjectV2"].(map[string]any)
	projGQL := createResult["projectV2"].(map[string]any)
	projNodeID := projGQL["id"].(string)
	projNumber := projGQL["number"].(float64)

	if projGQL["title"] != "Sprint Board" {
		t.Errorf("project title: got %v, want 'Sprint Board'", projGQL["title"])
	}

	// Step 2: Add a Status field to the project.
	fieldStatus := &db.ProjectField{
		ProjectID: uint(projNumber),
		Name:      "Status",
		DataType:  "SINGLE_SELECT",
		Options:   `[{"id":"opt-todo","name":"Todo"},{"id":"opt-inprogress","name":"In Progress"},{"id":"opt-done","name":"Done"}]`,
	}
	if err := svc.CreateProjectField(ctx, fieldStatus); err != nil {
		t.Fatalf("CreateProjectField: %v", err)
	}
	fieldNodeID := fmt.Sprintf("ProjectField_%d", fieldStatus.ID)

	// Step 3: Create a repo and issue.
	repo, _ := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "lifecycle-repo",
		AutoInit:   true,
	})
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", u.Login, repo.Name),
		Title:        "Sprint Task",
		Body:         "task to move through statuses",
		AuthorLogin:  u.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	issueNodeID := fmt.Sprintf("Issue_%d", issue.ID)

	// Step 4: Add the issue to the project.
	addMut := `
	mutation($input: AddProjectV2ItemByIdInput!) {
		addProjectV2ItemById(input: $input) {
			item {
				id
				type
				content {
					... on Issue {
						number
						title
					}
				}
			}
		}
	}`
	addData := doGql(t, mux, addMut, map[string]any{
		"input": map[string]any{
			"projectId": projNodeID,
			"contentId": issueNodeID,
		},
	})
	addResult := addData["addProjectV2ItemById"].(map[string]any)
	item := addResult["item"].(map[string]any)
	itemID := item["id"].(string)
	if item["type"] != "ISSUE" {
		t.Errorf("item type: got %v, want ISSUE", item["type"])
	}

	// Step 5: Move the item to "Todo" status.
	updateToTodo := `
	mutation($input: UpdateProjectV2ItemFieldValueInput!) {
		updateProjectV2ItemFieldValue(input: $input) {
			projectV2Item {
				id
			}
		}
	}`
	todoData := doGql(t, mux, updateToTodo, map[string]any{
		"input": map[string]any{
			"itemId":  itemID,
			"fieldId": fieldNodeID,
			"value": map[string]any{
				"singleSelectOptionId": "opt-todo",
			},
		},
	})
	todoResult := todoData["updateProjectV2ItemFieldValue"].(map[string]any)
	todoItem := todoResult["projectV2Item"].(map[string]any)
	if todoItem["id"] != itemID {
		t.Errorf("todo status update: item id got %v, want %v", todoItem["id"], itemID)
	}

	// Verify DB state after setting status to "opt-todo".
	todoItemDB, err := svc.GetProjectItem(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetProjectItem after todo status: %v", err)
	}
	if !strings.Contains(todoItemDB.FieldValues, `"opt-todo"`) {
		t.Errorf("FieldValues after todo status should contain '\"opt-todo\"', got: %s", todoItemDB.FieldValues)
	}

	// Step 6: Move the item to "In Progress" status.
	inProgressData := doGql(t, mux, updateToTodo, map[string]any{
		"input": map[string]any{
			"itemId":  itemID,
			"fieldId": fieldNodeID,
			"value": map[string]any{
				"singleSelectOptionId": "opt-inprogress",
			},
		},
	})
	inProgressResult := inProgressData["updateProjectV2ItemFieldValue"].(map[string]any)
	inProgressItem := inProgressResult["projectV2Item"].(map[string]any)
	if inProgressItem["id"] != itemID {
		t.Errorf("in-progress status update: item id got %v, want %v", inProgressItem["id"], itemID)
	}

	// Verify DB state after setting status to "opt-inprogress".
	inProgressItemDB, err := svc.GetProjectItem(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetProjectItem after in-progress status: %v", err)
	}
	if !strings.Contains(inProgressItemDB.FieldValues, `"opt-inprogress"`) {
		t.Errorf("FieldValues after in-progress status should contain '\"opt-inprogress\"', got: %s", inProgressItemDB.FieldValues)
	}

	// Step 7: Move the item to "Done" status.
	doneData := doGql(t, mux, updateToTodo, map[string]any{
		"input": map[string]any{
			"itemId":  itemID,
			"fieldId": fieldNodeID,
			"value": map[string]any{
				"singleSelectOptionId": "opt-done",
			},
		},
	})
	doneResult := doneData["updateProjectV2ItemFieldValue"].(map[string]any)
	doneItem := doneResult["projectV2Item"].(map[string]any)
	if doneItem["id"] != itemID {
		t.Errorf("done status update: item id got %v, want %v", doneItem["id"], itemID)
	}

	// Verify DB state after setting status to "opt-done".
	doneItemDB, err := svc.GetProjectItem(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetProjectItem after done status: %v", err)
	}
	if !strings.Contains(doneItemDB.FieldValues, `"opt-done"`) {
		t.Errorf("FieldValues after done status should contain '\"opt-done\"', got: %s", doneItemDB.FieldValues)
	}

	// Step 8: Verify the final state via query.
	q := `
	query($login: String!, $number: Int!) {
		organization(login: $login) {
			projectV2(number: $number) {
				id
				title
				items(first: 10) {
					totalCount
					nodes {
						id
						type
						content {
							... on Issue {
								number
								title
							}
						}
					}
				}
			}
		}
	}`
	qData := doGql(t, mux, q, map[string]any{
		"login":  u.Login,
		"number": projNumber,
	})
	org := qData["organization"].(map[string]any)
	projData := org["projectV2"].(map[string]any)

	if projData["title"] != "Sprint Board" {
		t.Errorf("queried project title: got %v, want 'Sprint Board'", projData["title"])
	}

	items := projData["items"].(map[string]any)
	totalCount := items["totalCount"].(float64)
	if totalCount != 1 {
		t.Errorf("items totalCount: got %v, want 1", totalCount)
	}

	itemNodes := items["nodes"].([]any)
	if len(itemNodes) != 1 {
		t.Fatalf("expected 1 item node, got %d", len(itemNodes))
	}

	itemNode := itemNodes[0].(map[string]any)
	content := itemNode["content"].(map[string]any)
	if content["number"] != float64(issue.Number) {
		t.Errorf("content number: got %v, want %d", content["number"], issue.Number)
	}
	if content["title"] != "Sprint Task" {
		t.Errorf("content title: got %v, want 'Sprint Task'", content["title"])
	}

	// Step 9: Archive the item.
	archiveMut := `
	mutation($input: ArchiveProjectV2ItemInput!) {
		archiveProjectV2Item(input: $input) {
			item {
				id
				isArchived
			}
		}
	}`
	archiveData := doGql(t, mux, archiveMut, map[string]any{
		"input": map[string]any{
			"itemId": itemID,
		},
	})
	archiveResult := archiveData["archiveProjectV2Item"].(map[string]any)
	archivedItem := archiveResult["item"].(map[string]any)
	if archivedItem["isArchived"] != true {
		t.Errorf("archived item isArchived: got %v, want true", archivedItem["isArchived"])
	}

	// Step 10: Delete the item.
	delMut := `
	mutation($input: DeleteProjectV2ItemInput!) {
		deleteProjectV2Item(input: $input) {
			deletedItemId
		}
	}`
	delData := doGql(t, mux, delMut, map[string]any{
		"input": map[string]any{
			"itemId": itemID,
		},
	})
	delResult := delData["deleteProjectV2Item"].(map[string]any)
	if delResult["deletedItemId"] != itemID {
		t.Errorf("deletedItemId: got %v, want %v", delResult["deletedItemId"], itemID)
	}

	// Final verification: project should have 0 items.
	qData2 := doGql(t, mux, q, map[string]any{
		"login":  u.Login,
		"number": projNumber,
	})
	org2 := qData2["organization"].(map[string]any)
	projData2 := org2["projectV2"].(map[string]any)
	items2 := projData2["items"].(map[string]any)
	totalCount2 := items2["totalCount"].(float64)
	if totalCount2 != 0 {
		t.Errorf("final items totalCount: got %v, want 0", totalCount2)
	}
}
