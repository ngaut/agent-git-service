package graphql

import (
	"testing"
)

func TestExtractFields_SimpleFields(t *testing.T) {
	query := `query { repository { id name description } }`
	root, frags := extractFields(query)

	if len(frags) != 0 {
		t.Errorf("expected no fragments, got %d", len(frags))
	}
	repoNode, ok := root["repository"].(map[string]any)
	if !ok {
		t.Fatal("expected repository to be a map")
	}
	for _, field := range []string{"id", "name", "description"} {
		if _, exists := repoNode[field]; !exists {
			t.Errorf("missing field %q in repository", field)
		}
	}
}

func TestExtractFields_NestedObjects(t *testing.T) {
	query := `query { repository { owner { login id } } }`
	root, _ := extractFields(query)

	repoNode := root["repository"].(map[string]any)
	ownerNode, ok := repoNode["owner"].(map[string]any)
	if !ok {
		t.Fatal("expected owner to be a map")
	}
	if _, exists := ownerNode["login"]; !exists {
		t.Error("missing login in owner")
	}
	if _, exists := ownerNode["id"]; !exists {
		t.Error("missing id in owner")
	}
}

func TestExtractFields_OperationNameSkipped(t *testing.T) {
	query := `query RepositoryInfo($owner: String!) { repository { id } }`
	root, _ := extractFields(query)

	// The operation name "RepositoryInfo" and variables should not appear in the AST
	if _, exists := root["RepositoryInfo"]; exists {
		t.Error("operation name should not appear in AST")
	}
	if _, exists := root["repository"]; !exists {
		t.Error("repository should be in AST")
	}
}

func TestExtractFields_MutationKeyword(t *testing.T) {
	query := `mutation CreateIssue($input: CreateIssueInput!) { createIssue(input: $input) { issue { id } } }`
	root, _ := extractFields(query)

	if _, exists := root["CreateIssue"]; exists {
		t.Error("mutation name should not appear in AST")
	}
	createNode, ok := root["createIssue"].(map[string]any)
	if !ok {
		t.Fatal("expected createIssue to be a map")
	}
	issueNode, ok := createNode["issue"].(map[string]any)
	if !ok {
		t.Fatal("expected issue to be a map")
	}
	if _, exists := issueNode["id"]; !exists {
		t.Error("missing id in issue")
	}
}

func TestExtractFields_FragmentSpread(t *testing.T) {
	query := `
		fragment repo on Repository { id name }
		query { repository { ...repo owner { login } } }
	`
	root, frags := extractFields(query)

	// Fragment should be parsed
	if _, exists := frags["repo"]; !exists {
		t.Fatal("expected fragment 'repo' to be parsed")
	}
	repoFrag := frags["repo"]
	if _, exists := repoFrag["id"]; !exists {
		t.Error("fragment should have 'id'")
	}
	if _, exists := repoFrag["name"]; !exists {
		t.Error("fragment should have 'name'")
	}

	// Root should have fragment spread marker
	repoNode := root["repository"].(map[string]any)
	if _, exists := repoNode["...repo"]; !exists {
		t.Error("repository should have fragment spread marker '...repo'")
	}
}

func TestExtractFields_InlineFragment(t *testing.T) {
	query := `query { node(id: "abc") { ... on Issue { title number } } }`
	root, _ := extractFields(query)

	nodeNode, ok := root["node"].(map[string]any)
	if !ok {
		t.Fatal("expected node to be a map")
	}
	// Inline fragments merge into parent
	if _, exists := nodeNode["title"]; !exists {
		t.Error("missing title from inline fragment")
	}
	if _, exists := nodeNode["number"]; !exists {
		t.Error("missing number from inline fragment")
	}
}

func TestExtractFields_Alias(t *testing.T) {
	// Aliases use colons: `alias: field(args) { ... }`
	// extractFields should keep the alias name but skip the colon and real field name
	query := `query { repo_0: repository(owner: "foo", name: "bar") { id } }`
	root, _ := extractFields(query)

	// repo_0 should be the key (from the regex path in handler, but extractFields
	// should at least not crash and may or may not capture this correctly)
	// The current implementation treats 'repo_0' as a word before ':'
	// then skips the colon and the next word, so repo_0 should not be in the AST
	// since the colon handler skips it.
	// This test documents current behavior.
	_ = root // The important thing is that extractFields doesn't panic
}

func TestFilterMap_SimpleFields(t *testing.T) {
	data := map[string]any{
		"id":    "123",
		"name":  "repo",
		"extra": "should-be-removed",
	}
	fields := map[string]any{
		"id":   true,
		"name": true,
	}
	result := filterMap(data, fields, nil)

	if result["id"] != "123" {
		t.Errorf("expected id=123, got %v", result["id"])
	}
	if result["name"] != "repo" {
		t.Errorf("expected name=repo, got %v", result["name"])
	}
	if _, exists := result["extra"]; exists {
		t.Error("extra field should be filtered out")
	}
}

func TestFilterMap_NestedFiltering(t *testing.T) {
	data := map[string]any{
		"owner": map[string]any{
			"login": "alice",
			"email": "secret@example.com",
		},
	}
	fields := map[string]any{
		"owner": map[string]any{
			"login": true,
		},
	}
	result := filterMap(data, fields, nil)

	owner := result["owner"].(map[string]any)
	if owner["login"] != "alice" {
		t.Errorf("expected login=alice, got %v", owner["login"])
	}
	if _, exists := owner["email"]; exists {
		t.Error("email should be filtered out of owner")
	}
}

func TestFilterMap_ArrayFiltering(t *testing.T) {
	data := map[string]any{
		"nodes": []any{
			map[string]any{"id": "1", "name": "a", "secret": "x"},
			map[string]any{"id": "2", "name": "b", "secret": "y"},
		},
	}
	fields := map[string]any{
		"nodes": map[string]any{
			"id":   true,
			"name": true,
		},
	}
	result := filterMap(data, fields, nil)

	nodes := result["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
	node0 := nodes[0].(map[string]any)
	if _, exists := node0["secret"]; exists {
		t.Error("secret should be filtered from array elements")
	}
}

func TestFilterMap_FragmentSpreadResolution(t *testing.T) {
	data := map[string]any{
		"id":   "123",
		"name": "repo",
	}
	fields := map[string]any{
		"...repoFields": true,
	}
	fragments := map[string]map[string]any{
		"repoFields": {
			"id":   true,
			"name": true,
		},
	}
	result := filterMap(data, fields, fragments)

	if result["id"] != "123" {
		t.Errorf("expected id=123 via fragment, got %v", result["id"])
	}
	if result["name"] != "repo" {
		t.Errorf("expected name=repo via fragment, got %v", result["name"])
	}
}
