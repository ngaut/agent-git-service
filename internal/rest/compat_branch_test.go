package rest_test

import (
	"context"
	"testing"

	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

// ─── Commit GET Response Fields ─────────────────────────────────────────────

func TestCompat_CommitGet_ResponseFields(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "compat-commit",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	// Get the default branch to find a commit SHA.
	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-commit/branches/main", nil)
	assertStatusCode(t, w, 200)
	branch := testharness.DecodeJSON(t, w)
	commit, _ := branch["commit"].(map[string]any)
	sha, _ := commit["sha"].(string)
	if sha == "" {
		t.Fatal("no commit SHA found on main branch")
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-commit/commits/"+sha, nil)
	assertStatusCode(t, w, 200)
	body := testharness.DecodeJSON(t, w)

	assertFieldsPresent(t, body, map[string]string{
		"sha":       "string",
		"commit":    "object",
		"author":    "",
		"committer": "",
		"parents":   "array",
		"files":     "array",
		"url":       "string",
		"html_url":  "string",
	})

	// Verify commit sub-object.
	commitObj, _ := body["commit"].(map[string]any)
	if commitObj != nil {
		assertFieldPresent(t, commitObj, "message", "string")
		assertFieldPresent(t, commitObj, "author", "object")
		assertFieldPresent(t, commitObj, "committer", "object")
	}

	files, _ := body["files"].([]any)
	if len(files) > 0 {
		fileObj, _ := files[0].(map[string]any)
		if fileObj == nil {
			t.Fatal("expected file entry to be an object")
		}
		assertFieldPresent(t, fileObj, "filename", "string")
		assertFieldPresent(t, fileObj, "status", "string")
		assertFieldPresent(t, fileObj, "additions", "number")
		assertFieldPresent(t, fileObj, "deletions", "number")
		assertFieldPresent(t, fileObj, "changes", "number")
	}
}

// ─── Branch List Response ───────────────────────────────────────────────────

func TestCompat_BranchList_ResponseShape(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "compat-branch-list")

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-branch-list/branches", nil)
	assertStatusCode(t, w, 200)
	branches := testharness.DecodeJSONArray(t, w)

	if len(branches) == 0 {
		t.Fatal("expected at least 1 branch (main)")
	}
	for _, b := range branches {
		assertFieldPresent(t, b, "name", "string")
		assertFieldPresent(t, b, "commit", "object")
		assertFieldPresent(t, b, "protected", "bool")
	}
}
