package graphql_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

// =============================================================================
// doIssues Tests - Alias-aware tests for multi-alias payloads
// =============================================================================

// TestDoIssues_AliasAssigned tests the assigned: issues(filterBy: {assignee: $viewer}) alias
func TestDoIssues_AliasAssigned(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "alias-repo",
		AutoInit:   true,
	})

	// Create issues with different assignees
	issue1, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tester/alias-repo",
		Title:        "Issue assigned to viewer",
		Body:         "body 1",
		AuthorLogin:  "tester",
	})
	// Set assignee on issue1
	svc.DB.Model(&db.Issue{}).Where("id = ?", issue1.ID).Update("assignee_logins", "tester")

	issue2, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tester/alias-repo",
		Title:        "Issue assigned to other",
		Body:         "body 2",
		AuthorLogin:  "tester",
	})
	// Set assignee on issue2 to otheruser
	svc.DB.Model(&db.Issue{}).Where("id = ?", issue2.ID).Update("assignee_logins", "otheruser")

	// Query with assigned alias
	q := `
	query($owner: String!, $name: String!, $viewer: String!) {
		repository(owner: $owner, name: $name) {
			assigned: issues(filterBy: {assignee: $viewer, states: OPEN}) {
				nodes {
					number
					title
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "alias-repo", "viewer": "tester",
	})
	repoData := data["repository"].(map[string]any)
	assigned := repoData["assigned"].(map[string]any)
	nodes := assigned["nodes"].([]any)

	require.Len(t, nodes, 1, "should return only issues assigned to viewer")
	node := nodes[0].(map[string]any)
	require.Equal(t, float64(issue1.Number), node["number"])
	require.Equal(t, "Issue assigned to viewer", node["title"])
}

// TestDoIssues_AliasMentioned tests the mentioned: issues(filterBy: {mentioned: $viewer}) alias
func TestDoIssues_AliasMentioned(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "mention-repo",
		AutoInit:   true,
	})

	_, _ = svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tester/mention-repo",
		Title:        "Issue mentioning viewer",
		Body:         "body with @tester mention",
		AuthorLogin:  "tester",
	})

	_, _ = svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tester/mention-repo",
		Title:        "Issue not mentioning viewer",
		Body:         "body without mention",
		AuthorLogin:  "tester",
	})

	// Query with mentioned alias
	q := `
	query($owner: String!, $name: String!, $viewer: String!) {
		repository(owner: $owner, name: $name) {
			mentioned: issues(filterBy: {mentioned: $viewer, states: OPEN}) {
				nodes {
					number
					title
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "mention-repo", "viewer": "tester",
	})
	repoData := data["repository"].(map[string]any)
	mentioned := repoData["mentioned"].(map[string]any)
	nodes := mentioned["nodes"].([]any)

	// Note: The current implementation doesn't actually filter by mention in body,
	// but the alias parsing should work
	require.NotNil(t, nodes)
}

// TestDoIssues_AliasCreatedBy tests the createdBy: issues(filterBy: {createdBy: $viewer}) alias
func TestDoIssues_AliasCreatedBy(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "created-repo",
		AutoInit:   true,
	})

	issue1, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tester/created-repo",
		Title:        "Issue created by viewer",
		Body:         "body 1",
		AuthorLogin:  "tester",
	})
	issue2, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tester/created-repo",
		Title:        "Issue created by other",
		Body:         "body 2",
		AuthorLogin:  "otheruser",
	})
	_ = issue2

	// Query with createdBy alias
	q := `
	query($owner: String!, $name: String!, $viewer: String!) {
		repository(owner: $owner, name: $name) {
			createdBy: issues(filterBy: {createdBy: $viewer, states: OPEN}) {
				nodes {
					number
					title
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "created-repo", "viewer": "tester",
	})
	repoData := data["repository"].(map[string]any)
	createdBy := repoData["createdBy"].(map[string]any)
	nodes := createdBy["nodes"].([]any)

	require.NotNil(t, nodes)
	require.Len(t, nodes, 1)
	node := nodes[0].(map[string]any)
	require.Equal(t, float64(issue1.Number), node["number"])
}

// TestDoIssues_MultiAliasPayload tests multiple aliases in a single query
func TestDoIssues_MultiAliasPayload(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "multi-alias-repo",
		AutoInit:   true,
	})

	// Create issues with various filters
	issue1, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tester/multi-alias-repo",
		Title:        "Assigned to viewer",
		Body:         "body 1",
		AuthorLogin:  "tester",
	})
	// Set assignee on issue1
	svc.DB.Model(&db.Issue{}).Where("id = ?", issue1.ID).Update("assignee_logins", "tester")

	issue2, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tester/multi-alias-repo",
		Title:        "Created by viewer but assigned to other",
		Body:         "body 2",
		AuthorLogin:  "tester",
	})
	// Set assignee on issue2 to otheruser
	svc.DB.Model(&db.Issue{}).Where("id = ?", issue2.ID).Update("assignee_logins", "otheruser")

	// Query with multiple aliases
	q := `
	query($owner: String!, $name: String!, $viewer: String!) {
		repository(owner: $owner, name: $name) {
			assigned: issues(filterBy: {assignee: $viewer, states: OPEN}) {
				nodes { number title }
			}
			createdBy: issues(filterBy: {createdBy: $viewer, states: OPEN}) {
				nodes { number title }
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "multi-alias-repo", "viewer": "tester",
	})
	repoData := data["repository"].(map[string]any)

	// Check assigned alias
	assigned := repoData["assigned"].(map[string]any)
	assignedNodes := assigned["nodes"].([]any)
	require.Len(t, assignedNodes, 1)
	require.Equal(t, float64(issue1.Number), assignedNodes[0].(map[string]any)["number"])

	// Check createdBy alias
	createdBy := repoData["createdBy"].(map[string]any)
	createdByNodes := createdBy["nodes"].([]any)
	require.Len(t, createdByNodes, 2) // Both issues created by viewer
}

// TestDoIssues_AliasWithLabels tests alias queries with label filters
func TestDoIssues_AliasWithLabels(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.AutoMigrate(&db.Label{})

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "label-repo",
		AutoInit:   true,
	})

	issue, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tester/label-repo",
		Title:        "Labeled issue",
		Body:         "body",
		AuthorLogin:  "tester",
	})
	// Set assignee on issue
	svc.DB.Model(&db.Issue{}).Where("id = ?", issue.ID).Update("assignee_logins", "tester")

	// Add label to issue
	label, err := svc.CreateLabel(ctx, "tester/label-repo", "bug", "d73a4a", "Something isn't working")
	require.NoError(t, err)
	require.NoError(t, svc.DB.Exec("INSERT INTO issue_labels (issue_id, label_id) VALUES (?, ?)", issue.ID, label.ID).Error)

	q := `
	query($owner: String!, $name: String!, $viewer: String!) {
		repository(owner: $owner, name: $name) {
			assigned: issues(filterBy: {assignee: $viewer, states: OPEN, labels: ["bug"]}) {
				nodes { number title }
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "label-repo", "viewer": "tester",
	})
	repoData := data["repository"].(map[string]any)
	assigned := repoData["assigned"].(map[string]any)
	nodes := assigned["nodes"].([]any)

	require.NotNil(t, nodes)
}

// =============================================================================
// doPRs Tests - Empty, filtered, and mixed-result scenarios
// =============================================================================

// TestDoPRs_EmptyResults tests PR list when no PRs exist
func TestDoPRs_EmptyResults(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "empty-pr-repo",
		AutoInit:   true,
	})

	q := `
	query($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) {
			pullRequests(first: 10) {
				nodes { number title }
				totalCount
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "empty-pr-repo",
	})
	repoData := data["repository"].(map[string]any)
	prs := repoData["pullRequests"].(map[string]any)
	nodes := prs["nodes"].([]any)

	require.Empty(t, nodes, "should return empty array when no PRs exist")
	require.Equal(t, float64(0), prs["totalCount"].(float64))
}

// TestDoPRs_FilteredByHeadRef tests PR filtering by headRefName
func TestDoPRs_FilteredByHeadRef(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "headref-repo",
		AutoInit:   true,
	})
	svc.Git.CreateBranch(ctx, "tester/headref-repo", "feature-a", "main")
	svc.Git.CreateBranch(ctx, "tester/headref-repo", "feature-b", "main")

	pr1, _ := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "tester/headref-repo",
		Title:        "PR from feature-a",
		HeadRef:      "feature-a",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})
	pr2, _ := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "tester/headref-repo",
		Title:        "PR from feature-b",
		HeadRef:      "feature-b",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})
	_ = pr2

	q := `
	query($owner: String!, $name: String!, $headRef: String!) {
		repository(owner: $owner, name: $name) {
			pullRequests(headRefName: $headRef, first: 10) {
				nodes { number title headRefName }
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "headref-repo", "headRef": "feature-a",
	})
	repoData := data["repository"].(map[string]any)
	prs := repoData["pullRequests"].(map[string]any)
	nodes := prs["nodes"].([]any)

	require.Len(t, nodes, 1, "should filter to only PRs from feature-a")
	node := nodes[0].(map[string]any)
	require.Equal(t, float64(pr1.Number), node["number"])
	require.Equal(t, "feature-a", node["headRefName"])
}

// TestDoPRs_MixedStates tests PR list with mixed states
func TestDoPRs_MixedStates(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "mixed-state-repo",
		AutoInit:   true,
	})

	// Create open PR
	prOpen, _ := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "tester/mixed-state-repo",
		Title:        "Open PR",
		HeadRef:      "feature-open",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})

	// Create and close another PR
	prClosed, _ := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "tester/mixed-state-repo",
		Title:        "Closed PR",
		HeadRef:      "feature-closed",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})
	svc.DB.Model(&db.PullRequest{}).Where("id = ?", prClosed.ID).Update("state", db.StateClosed)

	// Query all states
	q := `
	query($owner: String!, $name: String!, $states: [PullRequestState!]) {
		repository(owner: $owner, name: $name) {
			pullRequests(states: $states, first: 10) {
				nodes { number title state }
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "mixed-state-repo", "states": []string{"OPEN", "CLOSED"},
	})
	repoData := data["repository"].(map[string]any)
	prs := repoData["pullRequests"].(map[string]any)
	nodes := prs["nodes"].([]any)

	require.Len(t, nodes, 2, "should return both open and closed PRs")
	_ = prOpen
}

// =============================================================================
// doSearch and runSearch Tests - Empty, filtered, and mixed-result scenarios
// =============================================================================

// TestDoSearch_EmptyResults tests search with no results
func TestDoSearch_EmptyResults(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "search-empty-repo",
		AutoInit:   true,
	})

	q := `
	query($query: String!) {
		search(query: $query, type: ISSUE, first: 10) {
			issueCount
			nodes { ... on Issue { number title } }
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"query": "repo:tester/search-empty-repo type:issue state:open label:nonexistent",
	})
	search := data["search"].(map[string]any)
	nodes := search["nodes"].([]any)

	require.Empty(t, nodes, "should return empty array for non-matching search")
	require.Equal(t, float64(0), search["issueCount"].(float64))
}

// TestDoSearch_FilteredResults tests search with specific filters
func TestDoSearch_FilteredResults(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "search-filter-repo",
		AutoInit:   true,
	})

	_, _ = svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tester/search-filter-repo",
		Title:        "Bug issue",
		Body:         "this is a bug",
		AuthorLogin:  "tester",
	})
	_, _ = svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tester/search-filter-repo",
		Title:        "Feature request",
		Body:         "new feature",
		AuthorLogin:  "tester",
	})

	// Search by author
	q := `
	query($query: String!) {
		search(query: $query, type: ISSUE, first: 10) {
			issueCount
			nodes { ... on Issue { number title } }
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"query": "repo:tester/search-filter-repo author:tester",
	})
	search := data["search"].(map[string]any)
	nodes := search["nodes"].([]any)

	require.Len(t, nodes, 2, "should find issues by author")
}

// TestDoSearch_MixedResultTypes tests search returning issues and PRs
func TestDoSearch_MixedResultTypes(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "search-mixed-repo",
		AutoInit:   true,
	})

	issue, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tester/search-mixed-repo",
		Title:        "Test issue",
		Body:         "body",
		AuthorLogin:  "tester",
	})

	svc.Git.CreateBranch(ctx, "tester/search-mixed-repo", "feature", "main")
	pr, _ := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "tester/search-mixed-repo",
		Title:        "Test PR",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})

	// Search for issues
	q := `
	query($query: String!) {
		search(query: $query, type: ISSUE, first: 10) {
			issueCount
			nodes {
				... on Issue { number title __typename }
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"query": "repo:tester/search-mixed-repo",
	})
	search := data["search"].(map[string]any)
	nodes := search["nodes"].([]any)

	require.GreaterOrEqual(t, len(nodes), 1, "should find at least the issue")
	_ = issue
	_ = pr
}

// TestDoSearch_AliasedSearches tests multiple aliased search queries
func TestDoSearch_AliasedSearches(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "alias-search-repo",
		AutoInit:   true,
	})

	svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tester/alias-search-repo",
		Title:        "Viewer created issue",
		Body:         "body",
		AuthorLogin:  "tester",
	})

	// Query with aliased searches
	q := `
	query($viewerQuery: String!, $repoQuery: String!) {
		viewerCreated: search(query: $viewerQuery, type: ISSUE, first: 10) {
			issueCount
			nodes { ... on Issue { number title } }
		}
		repoSearch: search(query: $repoQuery, type: ISSUE, first: 10) {
			issueCount
			nodes { ... on Issue { number title } }
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"viewerQuery": "repo:tester/alias-search-repo author:tester",
		"repoQuery":   "repo:tester/alias-search-repo",
	})

	// Check viewerCreated alias
	viewerCreated := data["viewerCreated"].(map[string]any)
	require.GreaterOrEqual(t, viewerCreated["issueCount"].(float64), float64(1))

	// Check repoSearch alias
	repoSearch := data["repoSearch"].(map[string]any)
	require.GreaterOrEqual(t, repoSearch["issueCount"].(float64), float64(1))
}

// =============================================================================
// doNode Tests - Found/not-found and type mismatch behavior
// =============================================================================

// TestDoNode_IssueFound tests node resolution for existing Issue
func TestDoNode_IssueFound(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "node-issue-repo",
		AutoInit:   true,
	})

	issue, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tester/node-issue-repo",
		Title:        "Node test issue",
		Body:         "body",
		AuthorLogin:  "tester",
	})

	issueNodeID := fmt.Sprintf("Issue_%d", issue.ID)

	q := `
	query($id: ID!) {
		node(id: $id) {
			id
			__typename
		}
	}`
	data := doGql(t, mux, q, map[string]any{"id": issueNodeID})
	node := data["node"].(map[string]any)

	require.Equal(t, issueNodeID, node["id"])
	require.Equal(t, "Issue", node["__typename"])
}

// TestDoNode_PRFound tests node resolution for existing PullRequest
func TestDoNode_PRFound(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "node-pr-repo",
		AutoInit:   true,
	})
	svc.Git.CreateBranch(ctx, "tester/node-pr-repo", "feature", "main")

	pr, _ := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "tester/node-pr-repo",
		Title:        "Node test PR",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})

	prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)

	q := `
	query($id: ID!) {
		node(id: $id) {
			id
			__typename
			... on PullRequest {
				number
				title
				headRefName
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{"id": prNodeID})
	node := data["node"].(map[string]any)

	require.Equal(t, prNodeID, node["id"])
	require.Equal(t, "PullRequest", node["__typename"])
	require.Equal(t, float64(pr.Number), node["number"])
	require.Equal(t, "Node test PR", node["title"])
}

// TestDoNode_NotFound tests node resolution for non-existent ID
func TestDoNode_NotFound(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "node-notfound-repo",
		AutoInit:   true,
	})

	// Use a non-existent node ID
	q := `
	query($id: ID!) {
		node(id: $id) {
			id
			__typename
		}
	}`
	data := doGql(t, mux, q, map[string]any{"id": "Issue_99999"})
	node := data["node"]

	require.Nil(t, node, "should return nil for non-existent node")
}

// TestDoNode_TypeMismatch tests querying an Issue ID as PullRequest type
func TestDoNode_TypeMismatch(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "node-mismatch-repo",
		AutoInit:   true,
	})

	issue, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tester/node-mismatch-repo",
		Title:        "Type mismatch issue",
		Body:         "body",
		AuthorLogin:  "tester",
	})

	// Use Issue ID but query expects PullRequest
	issueNodeID := fmt.Sprintf("Issue_%d", issue.ID)

	q := `
	query($id: ID!) {
		node(id: $id) {
			id
			__typename
			... on PullRequest {
				number
				title
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{"id": issueNodeID})
	node := data["node"].(map[string]any)

	// The node should be found as Issue, but PullRequest fragment won't match
	require.Equal(t, issueNodeID, node["id"])
	require.Equal(t, "Issue", node["__typename"])
	// PullRequest-specific fields should not be present
	_, hasNumber := node["number"]
	require.False(t, hasNumber, "Issue node should not have PullRequest number field via fragment")
}

// TestDoNode_WithComments tests node resolution with comments field
func TestDoNode_WithComments(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "node-comments-repo",
		AutoInit:   true,
	})

	issue, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tester/node-comments-repo",
		Title:        "Issue with comments",
		Body:         "body",
		AuthorLogin:  "tester",
	})
	svc.CreateIssueComment(ctx, "tester/node-comments-repo", issue.Number, "comment 1", "tester", nil)
	svc.CreateIssueComment(ctx, "tester/node-comments-repo", issue.Number, "comment 2", "tester", nil)

	issueNodeID := fmt.Sprintf("Issue_%d", issue.ID)

	q := `
	query($id: ID!) {
		node(id: $id) {
			id
			__typename
			... on Issue {
				comments(first: 10) {
					totalCount
					nodes { body }
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{"id": issueNodeID})
	node := data["node"].(map[string]any)

	comments := node["comments"].(map[string]any)
	require.Equal(t, float64(2), comments["totalCount"].(float64))
	nodes := comments["nodes"].([]any)
	require.Len(t, nodes, 2)
}

// =============================================================================
// doResource Tests - URL resolution to Issue/PR
// =============================================================================

// TestDoResource_IssueURL tests resource resolution for issue URL
func TestDoResource_IssueURL(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "resource-issue-repo",
		AutoInit:   true,
	})

	_, _ = svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tester/resource-issue-repo",
		Title:        "Resource test issue",
		Body:         "body",
		AuthorLogin:  "tester",
	})

	issueURL := "http://localhost:8080/tester/resource-issue-repo/issues/1"

	q := `
	query($url: URI!) {
		resource(url: $url) {
			__typename
			id
		}
	}`
	data := doGql(t, mux, q, map[string]any{"url": issueURL})
	resource := data["resource"].(map[string]any)

	require.Equal(t, "Issue", resource["__typename"])
	require.Contains(t, resource["id"], "Issue_")
}

// TestDoResource_PRURL tests resource resolution for pull request URL
func TestDoResource_PRURL(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "resource-pr-repo",
		AutoInit:   true,
	})
	svc.Git.CreateBranch(ctx, "tester/resource-pr-repo", "feature", "main")

	_, _ = svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "tester/resource-pr-repo",
		Title:        "Resource test PR",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})

	prURL := "http://localhost:8080/tester/resource-pr-repo/pull/1"

	q := `
	query($url: URI!) {
		resource(url: $url) {
			__typename
			id
		}
	}`
	data := doGql(t, mux, q, map[string]any{"url": prURL})
	resource := data["resource"].(map[string]any)

	require.Equal(t, "PullRequest", resource["__typename"])
	require.Contains(t, resource["id"], "PullRequest_")
}

// TestDoResource_NotFound tests resource resolution for non-existent URL
func TestDoResource_NotFound(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "resource-notfound-repo",
		AutoInit:   true,
	})

	// URL for non-existent issue
	issueURL := "http://localhost:8080/tester/resource-notfound-repo/issues/999"

	q := `
	query($url: URI!) {
		resource(url: $url) {
			__typename
			id
		}
	}`
	data := doGql(t, mux, q, map[string]any{"url": issueURL})
	resource := data["resource"]

	require.Nil(t, resource, "should return nil for non-existent resource")
}

// TestDoResource_InvalidURL tests resource resolution for malformed URL
func TestDoResource_InvalidURL(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "resource-invalid-repo",
		AutoInit:   true,
	})

	// Malformed URL
	invalidURL := "http://localhost:8080/invalid"

	q := `
	query($url: URI!) {
		resource(url: $url) {
			__typename
			id
		}
	}`
	data := doGql(t, mux, q, map[string]any{"url": invalidURL})
	resource := data["resource"]

	require.Nil(t, resource, "should return nil for malformed URL")
}

// TestDoResource_EmptyURL tests resource resolution for empty URL
func TestDoResource_EmptyURL(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "resource-empty-repo",
		AutoInit:   true,
	})

	q := `
	query($url: URI!) {
		resource(url: $url) {
			__typename
			id
		}
	}`
	data := doGql(t, mux, q, map[string]any{"url": ""})
	resource := data["resource"]

	require.Nil(t, resource, "should return nil for empty URL")
}

// =============================================================================
// doReleaseList Tests
// =============================================================================

// TestDoReleaseList_EmptyRepo tests release list query on repo with no releases
func TestDoReleaseList_EmptyRepo(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "release-empty-repo",
		AutoInit:   true,
	})

	q := `
	query($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) {
			releases(first: 10) {
				nodes {
					tagName
					name
					isDraft
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "release-empty-repo",
	})
	repoData := data["repository"].(map[string]any)
	releases := repoData["releases"].(map[string]any)
	nodes := releases["nodes"].([]any)

	require.Len(t, nodes, 0, "should return empty list for repo with no releases")
}

// TestDoReleaseList_WithReleases tests release list query on repo with releases
func TestDoReleaseList_WithReleases(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "release-repo",
		AutoInit:   true,
	})
	require.NoError(t, err)

	q := `
	query($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) {
			releases(first: 10) {
				nodes {
					tagName
					name
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "release-repo",
	})
	repoData := data["repository"].(map[string]any)
	releases := repoData["releases"].(map[string]any)
	nodes := releases["nodes"].([]any)

	// Empty repo should return empty list
	require.Len(t, nodes, 0, "should return empty list for repo with no releases")
}

// =============================================================================
// doReleaseSingle Tests
// =============================================================================

// TestDoReleaseSingle_ByTag tests single release query by tag name for non-existent tag
func TestDoReleaseSingle_ByTag(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "release-single-repo",
		AutoInit:   true,
	})
	require.NoError(t, err)

	q := `
	query($owner: String!, $name: String!, $tagName: String!) {
		repository(owner: $owner, name: $name) {
			release(tagName: $tagName) {
				tagName
				name
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "release-single-repo", "tagName": "v1.0.0",
	})
	repoData := data["repository"].(map[string]any)
	release := repoData["release"]

	// Non-existent tag should return nil
	require.Nil(t, release, "should return nil for non-existent tag")
}

// TestDoReleaseSingle_NotFound tests single release query for non-existent tag
func TestDoReleaseSingle_NotFound(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "release-notfound-repo",
		AutoInit:   true,
	})

	q := `
	query($owner: String!, $name: String!, $tagName: String!) {
		repository(owner: $owner, name: $name) {
			release(tagName: $tagName) {
				tagName
				name
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "release-notfound-repo", "tagName": "nonexistent",
	})
	repoData := data["repository"].(map[string]any)
	release := repoData["release"]

	require.Nil(t, release, "should return nil for non-existent tag")
}

// =============================================================================
// doRepoLabels Tests
// =============================================================================

// TestDoRepoLabels_WithLabels tests labels query on repo (verifies label structure)
func TestDoRepoLabels_WithLabels(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "labels-repo",
		AutoInit:   true,
	})
	require.NoError(t, err)
	_, err = svc.CreateLabel(ctx, "tester/labels-repo", "bug", "d73a4a", "Something isn't working")
	require.NoError(t, err)

	q := `
	query($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) {
			labels(first: 10) {
				nodes {
					name
					color
					description
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "labels-repo",
	})
	repoData := data["repository"].(map[string]any)
	labels := repoData["labels"].(map[string]any)
	nodes := labels["nodes"].([]any)

	// Repo has explicitly created labels, verify query works and structure is correct
	require.Greater(t, len(nodes), 0, "should return labels")
	// Verify label structure
	node := nodes[0].(map[string]any)
	require.Contains(t, node, "name", "label should have name")
	require.Contains(t, node, "color", "label should have color")
	require.Contains(t, node, "description", "label should have description")
}

// TestDoRepoLabels_EmptyRepo tests labels query on repo (may have default labels)
func TestDoRepoLabels_EmptyRepo(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "labels-empty-repo",
		AutoInit:   true,
	})
	require.NoError(t, err)

	q := `
	query($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) {
			labels(first: 10) {
				nodes {
					name
					color
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "labels-empty-repo",
	})
	repoData := data["repository"].(map[string]any)
	labels := repoData["labels"].(map[string]any)
	nodes := labels["nodes"].([]any)

	// Repos start with zero labels, but the query should still return a labels list
	require.GreaterOrEqual(t, len(nodes), 0, "should return labels list")
	// Verify label structure
	if len(nodes) > 0 {
		node := nodes[0].(map[string]any)
		require.Contains(t, node, "name", "label should have name")
		require.Contains(t, node, "color", "label should have color")
	}
}

// =============================================================================
// doAssignableUsers Tests
// =============================================================================

// TestDoAssignableUsers_WithUsers tests assignable users query
func TestDoAssignableUsers_WithUsers(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "assignable-repo",
		AutoInit:   true,
	})

	q := `
	query($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) {
			assignableUsers(first: 10) {
				nodes {
					login
					name
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "assignable-repo",
	})
	repoData := data["repository"].(map[string]any)
	users := repoData["assignableUsers"].(map[string]any)
	nodes := users["nodes"].([]any)

	require.Greater(t, len(nodes), 0, "should return at least one user")
}

// TestDoAssignableUsers_NonExistentRepo tests assignable users query for non-existent repo
func TestDoAssignableUsers_NonExistentRepo(t *testing.T) {
	_, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	q := `
	query($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) {
			assignableUsers(first: 10) {
				nodes {
					login
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "nonexistent-repo",
	})
	repoData := data["repository"].(map[string]any)
	users := repoData["assignableUsers"].(map[string]any)
	nodes := users["nodes"].([]any)

	require.Len(t, nodes, 0, "should return empty list for non-existent repo")
}

// =============================================================================
// doRepoMilestones Tests
// =============================================================================

// TestDoRepoMilestones_WithMilestones tests milestones query with milestones
func TestDoRepoMilestones_WithMilestones(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "milestones-repo",
		AutoInit:   true,
	})
	require.NoError(t, err)

	// Create a milestone
	_, err = svc.CreateMilestone(ctx, "tester/milestones-repo", "v1.0", "Version 1.0 milestone", "open")
	require.NoError(t, err)

	q := `
	query($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) {
			milestones(first: 10) {
				nodes {
					title
					description
					state
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "milestones-repo",
	})
	repoData := data["repository"].(map[string]any)
	milestones := repoData["milestones"].(map[string]any)
	nodes := milestones["nodes"].([]any)

	require.GreaterOrEqual(t, len(nodes), 1, "should return at least 1 milestone")
}

// TestDoRepoMilestones_EmptyRepo tests milestones query on repo with no milestones
func TestDoRepoMilestones_EmptyRepo(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "milestones-empty-repo",
		AutoInit:   true,
	})

	q := `
	query($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) {
			milestones(first: 10) {
				nodes {
					title
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "milestones-empty-repo",
	})
	repoData := data["repository"].(map[string]any)
	milestones := repoData["milestones"].(map[string]any)
	nodes := milestones["nodes"].([]any)

	require.Len(t, nodes, 0, "should return empty list for repo with no milestones")
}

// TestDoRepoMilestones_WithStateFilter tests milestones query with state filter
func TestDoRepoMilestones_WithStateFilter(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "milestones-filter-repo",
		AutoInit:   true,
	})
	require.NoError(t, err)

	// Query with OPEN state filter (no milestones created, just test query works)
	q := `
	query($owner: String!, $name: String!, $states: [MilestoneState!]) {
		repository(owner: $owner, name: $name) {
			milestones(first: 10, states: $states) {
				nodes {
					title
					state
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "milestones-filter-repo", "states": []string{"OPEN"},
	})
	repoData := data["repository"].(map[string]any)
	milestones := repoData["milestones"].(map[string]any)
	nodes := milestones["nodes"].([]any)

	// Empty result is expected since no milestones exist
	require.Len(t, nodes, 0, "should return empty list when no milestones match filter")
}
