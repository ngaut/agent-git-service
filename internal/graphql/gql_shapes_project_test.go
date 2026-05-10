package graphql_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

// TestProjectItems_NoCrossContamination verifies that issue and pull request
// projectItems do not cross-match when their database IDs overlap.
// This is a regression test for issue #184.
//
// The bug: projectItemsGQL was searching for BOTH Issue_N and PullRequest_N
// using the same numeric ID N, even though issues and PRs come from different
// tables and can have overlapping IDs.
//
// The fix: projectItemsGQL now accepts an explicit content node ID string
// (e.g., "Issue_42" or "PullRequest_42") and only queries for that specific ID.
func TestProjectItems_NoCrossContamination(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Create a repository.
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "project-items-repo",
		AutoInit:   true,
	})
	require.NoError(t, err)

	// Create a project board.
	proj, err := svc.CreateProject(ctx, u.Login, "Test Board")
	require.NoError(t, err)

	// Create an issue.
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: repo.FullName,
		Title:        "Issue #1",
		Body:         "issue body",
		AuthorLogin:  u.Login,
	})
	require.NoError(t, err)

	// Create a pull request.
	svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main")
	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "PR #1",
		Body:         "pr body",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  u.Login,
	})
	require.NoError(t, err)

	// Simulate overlapping IDs: set PR's database ID to match the issue's database ID.
	// This is the scenario that caused the cross-contamination bug.
	// We do this by deleting the PR and re-inserting it with the same ID as the issue.
	svc.DB.Delete(&pr)
	pr.ID = issue.ID // Same database ID as the issue!
	pr.Number = 2    // Different number to avoid unique constraint
	svc.DB.Create(&pr)

	// Create project items using the full GraphQL node ID format (Type_DBID).
	// After the fix, querying issue.projectItems should only find items with ContentID="Issue_X",
	// and querying pullRequest.projectItems should only find items with ContentID="PullRequest_X".
	// Use fmt.Sprintf to match the gqlID format: "Type_ID"
	issueNodeID := fmt.Sprintf("Issue_%d", issue.ID)
	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	// Insert project items directly into the DB with the correct content IDs.
	issueProjectItem := &db.ProjectItem{
		ProjectID:  proj.ID,
		Type:       "ISSUE",
		ContentID:  issueNodeID,
		DraftTitle: "",
		DraftBody:  "",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	require.NoError(t, svc.DB.Create(issueProjectItem).Error)

	prProjectItem := &db.ProjectItem{
		ProjectID:  proj.ID,
		Type:       "PULL_REQUEST",
		ContentID:  prNodeID,
		DraftTitle: "",
		DraftBody:  "",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	require.NoError(t, svc.DB.Create(prProjectItem).Error)

	// Query the issue's projectItems.
	issueQuery := `
	query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) {
			issue(number: $number) {
				id
				number
				projectItems(first: 10) {
					totalCount
					nodes {
						id
						project { id title }
					}
				}
			}
		}
	}`
	issueData := doGql(t, mux, issueQuery, map[string]any{
		"owner":  u.Login,
		"name":   repo.Name,
		"number": float64(issue.Number),
	})
	repoData := issueData["repository"].(map[string]any)
	issueResult := repoData["issue"].(map[string]any)
	issueProjectItems := issueResult["projectItems"].(map[string]any)
	issueNodes := issueProjectItems["nodes"].([]any)

	// Query the PR's projectItems.
	prQuery := `
	query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) {
			pullRequest(number: $number) {
				id
				number
				projectItems(first: 10) {
					totalCount
					nodes {
						id
						project { id title }
					}
				}
			}
		}
	}`
	prData := doGql(t, mux, prQuery, map[string]any{
		"owner":  u.Login,
		"name":   repo.Name,
		"number": float64(pr.Number),
	})
	repoDataPR := prData["repository"].(map[string]any)
	prResult := repoDataPR["pullRequest"].(map[string]any)
	prProjectItems := prResult["projectItems"].(map[string]any)
	prNodes := prProjectItems["nodes"].([]any)

	// Assert: Issue should have exactly 1 project item (the one linked to Issue_42).
	require.Len(t, issueNodes, 1, "issue.projectItems should return exactly 1 item (linked to Issue_%d)", issue.Number)

	// Assert: PR should have exactly 1 project item (the one linked to PullRequest_42).
	require.Len(t, prNodes, 1, "pullRequest.projectItems should return exactly 1 item (linked to PullRequest_%d)", pr.Number)

	// Assert: The issue's project item should NOT be the same as the PR's project item.
	issueItemID := issueNodes[0].(map[string]any)["id"].(string)
	prItemID := prNodes[0].(map[string]any)["id"].(string)
	require.NotEqual(t, issueItemID, prItemID, "issue and PR project items should be different")
}

// TestProjectItems_IssueOnly verifies that issue.projectItems only returns
// items linked to that specific issue, not other issues.
func TestProjectItems_IssueOnly(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "issue-only-repo",
		AutoInit:   true,
	})
	require.NoError(t, err)

	proj, err := svc.CreateProject(ctx, u.Login, "Test Board")
	require.NoError(t, err)

	// Create two issues.
	issue1, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: repo.FullName,
		Title:        "Issue 1",
		Body:         "body 1",
		AuthorLogin:  u.Login,
	})
	require.NoError(t, err)

	issue2, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: repo.FullName,
		Title:        "Issue 2",
		Body:         "body 2",
		AuthorLogin:  u.Login,
	})
	require.NoError(t, err)

	// Create project items for each issue.
	issue1NodeID := fmt.Sprintf("Issue_%d", issue1.ID)
	issue2NodeID := fmt.Sprintf("Issue_%d", issue2.ID)

	item1 := &db.ProjectItem{
		ProjectID: proj.ID,
		Type:      "ISSUE",
		ContentID: issue1NodeID,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, svc.DB.Create(item1).Error)

	item2 := &db.ProjectItem{
		ProjectID: proj.ID,
		Type:      "ISSUE",
		ContentID: issue2NodeID,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, svc.DB.Create(item2).Error)

	// Query issue1's projectItems.
	q := `
	query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) {
			issue(number: $number) {
				projectItems(first: 10) {
					totalCount
					nodes { id }
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner":  u.Login,
		"name":   repo.Name,
		"number": float64(issue1.Number),
	})
	repoData := data["repository"].(map[string]any)
	issueResult := repoData["issue"].(map[string]any)
	projectItems := issueResult["projectItems"].(map[string]any)
	nodes := projectItems["nodes"].([]any)

	// Assert: issue1 should only have its own project item, not issue2's.
	require.Len(t, nodes, 1, "issue1.projectItems should return exactly 1 item")
	// Extract the ID from the "ProjectItem_X" format
	itemIDStr := nodes[0].(map[string]any)["id"].(string)
	require.Contains(t, itemIDStr, "ProjectItem_", "project item ID should have ProjectItem_ prefix")
	// Just verify it's the correct item by checking it's not item2's ID
	item2IDStr := fmt.Sprintf("ProjectItem_%d", item2.ID)
	require.NotEqual(t, itemIDStr, item2IDStr, "should return item1, not item2")
}

// TestProjectItemGQL_FieldValues tests that projectV2 items correctly return
// field values from ProjectItem.FieldValues JSON via GraphQL query.
func TestProjectItemGQL_FieldValues(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Create a project with a status field
	proj, err := svc.CreateProject(ctx, u.Login, "Test Board")
	require.NoError(t, err)

	statusField := &db.ProjectField{
		ProjectID: proj.ID,
		Name:      "Status",
		DataType:  "SINGLE_SELECT",
		Options:   `["Todo", "In Progress", "Done"]`,
	}
	require.NoError(t, svc.DB.Create(statusField).Error)

	// Create an issue
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "fieldvalues-repo",
		AutoInit:   true,
	})
	require.NoError(t, err)

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: repo.FullName,
		Title:        "Test Issue",
		Body:         "test body",
		AuthorLogin:  u.Login,
	})
	require.NoError(t, err)

	// Create project item directly with field values
	issueNodeID := fmt.Sprintf("Issue_%d", issue.ID)
	item := &db.ProjectItem{
		ProjectID:   proj.ID,
		Type:        "ISSUE",
		ContentID:   issueNodeID,
		FieldValues: fmt.Sprintf(`{"%d": "option-in-progress"}`, statusField.ID),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	require.NoError(t, svc.DB.Create(item).Error)

	// Query projectV2 items and verify fieldValues.nodes contains the value
	query := `
	query($login: String!, $number: Int!) {
		user(login: $login) {
			projectV2(number: $number) {
				items(first: 10) {
					nodes {
						id
						fieldValues(first: 10) {
							nodes {
								field { id }
								value
							}
						}
					}
				}
			}
		}
	}`
	data := doGql(t, mux, query, map[string]any{
		"login":  u.Login,
		"number": float64(proj.Number),
	})

	userData := data["user"].(map[string]any)
	projData := userData["projectV2"].(map[string]any)
	itemsData := projData["items"].(map[string]any)
	itemNodes := itemsData["nodes"].([]any)

	require.NotEmpty(t, itemNodes, "should have at least one item")
	itemNode := itemNodes[0].(map[string]any)
	fieldValues := itemNode["fieldValues"].(map[string]any)
	fvNodes := fieldValues["nodes"].([]any)

	// Verify fieldValues.nodes is populated with the status value
	foundStatus := false
	for _, node := range fvNodes {
		n := node.(map[string]any)
		field := n["field"].(map[string]any)
		value := n["value"]
		expectedFieldID := fmt.Sprintf("ProjectField_%d", statusField.ID)
		if field["id"] == expectedFieldID && value == "option-in-progress" {
			foundStatus = true
			break
		}
	}
	require.True(t, foundStatus, "fieldValues.nodes should contain the status field value")
}

// TestProjectItemGQL_EmptyFieldValues tests that projectV2 items with empty
// FieldValues return empty fieldValues.nodes.
func TestProjectItemGQL_EmptyFieldValues(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	proj, err := svc.CreateProject(ctx, u.Login, "Test Board Empty")
	require.NoError(t, err)

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "empty-fv-repo",
		AutoInit:   true,
	})
	require.NoError(t, err)

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: repo.FullName,
		Title:        "Test Issue Empty",
		Body:         "test body",
		AuthorLogin:  u.Login,
	})
	require.NoError(t, err)

	// Create project item directly without field values
	issueNodeID := fmt.Sprintf("Issue_%d", issue.ID)
	item := &db.ProjectItem{
		ProjectID:   proj.ID,
		Type:        "ISSUE",
		ContentID:   issueNodeID,
		FieldValues: "", // Empty
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	require.NoError(t, svc.DB.Create(item).Error)

	// Query and verify empty fieldValues.nodes
	query := `
	query($login: String!, $number: Int!) {
		user(login: $login) {
			projectV2(number: $number) {
				items(first: 10) {
					nodes {
						id
						fieldValues(first: 10) {
							nodes {
								field { id }
								value
							}
						}
					}
				}
			}
		}
	}`
	data := doGql(t, mux, query, map[string]any{
		"login":  u.Login,
		"number": float64(proj.Number),
	})

	userData := data["user"].(map[string]any)
	projData := userData["projectV2"].(map[string]any)
	itemsData := projData["items"].(map[string]any)
	itemNodes := itemsData["nodes"].([]any)

	require.NotEmpty(t, itemNodes, "should have at least one item")
	itemNode := itemNodes[0].(map[string]any)
	fieldValues := itemNode["fieldValues"].(map[string]any)
	fvNodes := fieldValues["nodes"].([]any)

	require.Empty(t, fvNodes, "fieldValues.nodes should be empty when no field values set")
}

// TestProjectItemsGQL_StatusDerivation tests that projectItemsGQL correctly
// derives status from field values when a status field exists.
func TestProjectItemsGQL_StatusDerivation(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Create a repository and issue
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "status-derive-repo",
		AutoInit:   true,
	})
	require.NoError(t, err)

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: repo.FullName,
		Title:        "Test Issue Status",
		Body:         "test body",
		AuthorLogin:  u.Login,
	})
	require.NoError(t, err)

	// Create a project with a status field
	proj, err := svc.CreateProject(ctx, u.Login, "Test Board Status")
	require.NoError(t, err)

	statusField := &db.ProjectField{
		ProjectID: proj.ID,
		Name:      "Status",
		DataType:  "SINGLE_SELECT",
		Options:   `["Todo", "In Progress", "Done"]`,
	}
	require.NoError(t, svc.DB.Create(statusField).Error)

	// Create project item directly with status field value
	issueNodeID := fmt.Sprintf("Issue_%d", issue.ID)
	item := &db.ProjectItem{
		ProjectID:   proj.ID,
		Type:        "ISSUE",
		ContentID:   issueNodeID,
		FieldValues: fmt.Sprintf(`{"%d": "option-in-progress"}`, statusField.ID),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	require.NoError(t, svc.DB.Create(item).Error)

	// Query issue's projectItems and verify status is derived
	q := `
	query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) {
			issue(number: $number) {
				projectItems(first: 10) {
					totalCount
					nodes {
						id
						project { id title }
						status { optionId name }
					}
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner":  u.Login,
		"name":   repo.Name,
		"number": float64(issue.Number),
	})

	repoData := data["repository"].(map[string]any)
	issueResult := repoData["issue"].(map[string]any)
	projectItems := issueResult["projectItems"].(map[string]any)
	nodes := projectItems["nodes"].([]any)

	require.Len(t, nodes, 1, "should have exactly 1 project item")

	node := nodes[0].(map[string]any)
	status := node["status"]
	require.NotNil(t, status, "status should not be nil when status field exists")

	statusMap := status.(map[string]any)
	require.Equal(t, "option-in-progress", statusMap["optionId"], "status optionId should match")
}

// TestProjectItemsGQL_NoStatusField tests that projectItemsGQL returns nil status
// when no status field exists in the project.
func TestProjectItemsGQL_NoStatusField(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "no-status-field-repo",
		AutoInit:   true,
	})
	require.NoError(t, err)

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: repo.FullName,
		Title:        "Test Issue No Status",
		Body:         "test body",
		AuthorLogin:  u.Login,
	})
	require.NoError(t, err)

	// Create a project WITHOUT a status field
	proj, err := svc.CreateProject(ctx, u.Login, "Test Board No Status Field")
	require.NoError(t, err)

	// Create a regular field (not status)
	priorityField := &db.ProjectField{
		ProjectID: proj.ID,
		Name:      "Priority",
		DataType:  "SINGLE_SELECT",
		Options:   `["Low", "Medium", "High"]`,
	}
	require.NoError(t, svc.DB.Create(priorityField).Error)

	// Create project item with priority field value (not status)
	issueNodeID := fmt.Sprintf("Issue_%d", issue.ID)
	item := &db.ProjectItem{
		ProjectID:   proj.ID,
		Type:        "ISSUE",
		ContentID:   issueNodeID,
		FieldValues: fmt.Sprintf(`{"%d": "high"}`, priorityField.ID),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	require.NoError(t, svc.DB.Create(item).Error)

	// Query issue's projectItems - status should be nil
	q := `
	query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) {
			issue(number: $number) {
				projectItems(first: 10) {
					totalCount
					nodes {
						id
						project { id title }
						status { optionId name }
					}
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner":  u.Login,
		"name":   repo.Name,
		"number": float64(issue.Number),
	})

	repoData := data["repository"].(map[string]any)
	issueResult := repoData["issue"].(map[string]any)
	projectItems := issueResult["projectItems"].(map[string]any)
	nodes := projectItems["nodes"].([]any)

	require.Len(t, nodes, 1, "should have exactly 1 project item")

	node := nodes[0].(map[string]any)
	status := node["status"]
	// When no status field exists, status should be nil or have nil optionId
	if statusMap, ok := status.(map[string]any); ok {
		require.Nil(t, statusMap["optionId"], "status.optionId should be nil when no status field exists")
	} else {
		require.Nil(t, status, "status should be nil when no status field exists")
	}
}
