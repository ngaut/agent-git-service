package rest_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

// compatSeedPR creates a repo with a feature branch and a PR for testing.
func compatSeedPR(t *testing.T, h *testharness.Harness, repoName, branchName string, opts ...func(*service.CreatePRInput)) {
	t.Helper()
	ctx := context.Background()
	full := "testuser/" + repoName
	compatSeedRepo(t, h, repoName)

	if err := h.Svc.Git.CreateBranch(ctx, full, branchName, "main"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	in := service.CreatePRInput{
		RepoFullName: full,
		Title:        "test PR",
		HeadRef:      branchName,
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	}
	for _, opt := range opts {
		opt(&in)
	}
	if _, err := h.Svc.CreatePR(ctx, in); err != nil {
		t.Fatalf("create PR: %v", err)
	}
}

// ─── PR GET Response Fields ─────────────────────────────────────────────────

func TestCompat_PRGet_ResponseFields(t *testing.T) {
	h := testharness.New(t)
	compatSeedPR(t, h, "compat-pr", "feature", func(in *service.CreatePRInput) {
		in.Title = "compat test PR"
		in.Body = "PR body"
	})

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-pr/pulls/1", nil)
	assertStatusCode(t, w, 200)
	body := testharness.DecodeJSON(t, w)

	// GitHub REST API PR response fields.
	assertFieldsPresent(t, body, map[string]string{
		"id":                    "number",
		"node_id":               "string",
		"number":                "number",
		"title":                 "string",
		"body":                  "string",
		"state":                 "string",
		"draft":                 "bool",
		"merged":                "bool",
		"mergeable":             "bool",
		"rebaseable":            "bool",
		"mergeable_state":       "string",
		"merge_commit_sha":      "",
		"user":                  "object",
		"labels":                "array",
		"assignee":              "",
		"assignees":             "array",
		"milestone":             "",
		"head":                  "object",
		"base":                  "object",
		"url":                   "string",
		"html_url":              "string",
		"diff_url":              "string",
		"patch_url":             "string",
		"issue_url":             "string",
		"comments_url":          "string",
		"commits_url":           "string",
		"review_comments_url":   "string",
		"statuses_url":          "string",
		"comments":              "number",
		"review_comments":       "number",
		"commits":               "number",
		"additions":             "number",
		"deletions":             "number",
		"changed_files":         "number",
		"created_at":            "string",
		"updated_at":            "string",
		"author_association":    "string",
		"reactions":             "object",
		"requested_reviewers":   "array",
		"requested_teams":       "array",
		"maintainer_can_modify": "bool",
	})
}

// ─── PR GET Head/Base Shape ─────────────────────────────────────────────────

func TestCompat_PRGet_HeadBaseShape(t *testing.T) {
	h := testharness.New(t)
	compatSeedPR(t, h, "compat-pr-hb", "feat")

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-pr-hb/pulls/1", nil)
	assertStatusCode(t, w, 200)
	body := testharness.DecodeJSON(t, w)

	for _, key := range []string{"head", "base"} {
		obj, ok := body[key].(map[string]any)
		if !ok {
			t.Errorf("%s: expected object, got %T", key, body[key])
			continue
		}
		assertFieldsPresent(t, obj, map[string]string{
			"label": "string",
			"ref":   "string",
			"sha":   "string",
			"user":  "object",
			"repo":  "object",
		})
	}
}

func TestCompat_PRCreate_ResponseFields(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	compatSeedRepo(t, h, "compat-pr-create")
	full := "testuser/compat-pr-create"
	if err := h.Svc.Git.CreateBranch(ctx, full, "feature", "main"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	if _, err := h.Svc.Git.WriteFile(ctx, full, "feature", "feature.txt", "add feature", []byte("hello feature")); err != nil {
		t.Fatalf("write file: %v", err)
	}

	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/compat-pr-create/pulls", map[string]any{
		"title": "compat create PR",
		"body":  "PR body",
		"head":  "feature",
		"base":  "main",
	})
	assertStatusCode(t, w, 201)
	body := testharness.DecodeJSON(t, w)

	assertFieldsPresent(t, body, map[string]string{
		"id":                    "number",
		"node_id":               "string",
		"number":                "number",
		"title":                 "string",
		"body":                  "string",
		"state":                 "string",
		"draft":                 "bool",
		"merged":                "bool",
		"mergeable":             "bool",
		"rebaseable":            "bool",
		"mergeable_state":       "string",
		"merge_commit_sha":      "",
		"user":                  "object",
		"labels":                "array",
		"assignee":              "",
		"assignees":             "array",
		"milestone":             "",
		"head":                  "object",
		"base":                  "object",
		"url":                   "string",
		"html_url":              "string",
		"diff_url":              "string",
		"patch_url":             "string",
		"issue_url":             "string",
		"comments_url":          "string",
		"commits_url":           "string",
		"review_comments_url":   "string",
		"statuses_url":          "string",
		"comments":              "number",
		"review_comments":       "number",
		"commits":               "number",
		"additions":             "number",
		"deletions":             "number",
		"changed_files":         "number",
		"created_at":            "string",
		"updated_at":            "string",
		"author_association":    "string",
		"reactions":             "object",
		"requested_reviewers":   "array",
		"requested_teams":       "array",
		"maintainer_can_modify": "bool",
	})

	for _, key := range []string{"comments", "review_comments", "commits", "additions", "deletions", "changed_files"} {
		got, ok := body[key].(float64)
		if !ok || got != 0 {
			t.Fatalf("%s: expected lightweight create response to return 0, got %v", key, body[key])
		}
	}

	reactions, ok := body["reactions"].(map[string]any)
	if !ok {
		t.Fatalf("expected reactions object, got %T", body["reactions"])
	}
	if total, ok := reactions["total_count"].(float64); !ok || total != 0 {
		t.Fatalf("expected reactions.total_count 0, got %v", reactions["total_count"])
	}
	if reviewers, ok := body["requested_reviewers"].([]any); !ok || len(reviewers) != 0 {
		t.Fatalf("expected requested_reviewers to be empty, got %v", body["requested_reviewers"])
	}
	if teams, ok := body["requested_teams"].([]any); !ok || len(teams) != 0 {
		t.Fatalf("expected requested_teams to be empty, got %v", body["requested_teams"])
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-pr-create/pulls/1", nil)
	assertStatusCode(t, w, 200)
	enriched := testharness.DecodeJSON(t, w)
	for _, key := range []string{"commits", "additions", "changed_files"} {
		got, ok := enriched[key].(float64)
		if !ok || got <= 0 {
			t.Fatalf("%s: expected enriched GET response to be > 0, got %v", key, enriched[key])
		}
	}
}

// ─── PR PATCH Partial Update ────────────────────────────────────────────────

func TestCompat_PRPATCH_PartialUpdate(t *testing.T) {
	h := testharness.New(t)
	compatSeedPR(t, h, "compat-pr-patch", "fix", func(in *service.CreatePRInput) {
		in.Title = "original pr title"
		in.Body = "original pr body"
	})

	// PATCH only title — body must remain.
	w := h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/compat-pr-patch/pulls/1", map[string]any{
		"title": "updated pr title",
	})
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)

	if resp["title"] != "updated pr title" {
		t.Errorf("title: got %v, want %q", resp["title"], "updated pr title")
	}
	if resp["body"] != "original pr body" {
		t.Errorf("body should be unchanged: got %v, want %q", resp["body"], "original pr body")
	}
}

// ─── PR Review PUT Update ──────────────────────────────────────────────────

func TestCompat_PRReviewUpdate_UsesPut(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedPR(t, h, "compat-pr-review-put", "review-update")

	pr, err := h.Svc.GetPR(ctx, "testuser/compat-pr-review-put", 1)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	review, err := h.Svc.AddPRReview(ctx, pr.ID, h.User.Login, "COMMENT", "original body", "")
	if err != nil {
		t.Fatalf("AddPRReview: %v", err)
	}

	path := fmt.Sprintf("/api/v3/repos/testuser/compat-pr-review-put/pulls/1/reviews/%d", review.ID)
	w := h.DoRESTJSON(t, "PATCH", path, map[string]any{"body": "patched body"})
	assertStatusCode(t, w, 405)

	w = h.DoRESTJSON(t, "PUT", path, map[string]any{"body": "updated body"})
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)
	if resp["body"] != "updated body" {
		t.Fatalf("body: got %v, want %q", resp["body"], "updated body")
	}

	stored, err := h.Svc.GetPRReview(ctx, review.ID)
	if err != nil {
		t.Fatalf("GetPRReview: %v", err)
	}
	if string(stored.Body) != "updated body" {
		t.Fatalf("stored body: got %q, want %q", string(stored.Body), "updated body")
	}
}

// ─── PR Ready For Review Response Shape ─────────────────────────────────────

func TestCompat_PRReadyForReview_ResponseShape(t *testing.T) {
	h := testharness.New(t)
	compatSeedPR(t, h, "compat-pr-ready", "draft-br", func(in *service.CreatePRInput) {
		in.Title = "draft PR"
		in.Draft = true
	})

	w := h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/compat-pr-ready/pulls/1/ready_for_review", nil)
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)

	// MarkPRReadyForReview should return an enriched PR response, same as GET.
	assertFieldsPresent(t, resp, map[string]string{
		"requested_reviewers": "array",
		"requested_teams":     "array",
		"comments":            "number",
		"review_comments":     "number",
		"commits":             "number",
		"additions":           "number",
		"deletions":           "number",
		"changed_files":       "number",
	})

	if resp["draft"] != false {
		t.Errorf("draft: got %v, want false after marking ready", resp["draft"])
	}
}

// ─── PR Merge Response Shape ────────────────────────────────────────────────

func TestCompat_PRMerge_ResponseShape(t *testing.T) {
	h := testharness.New(t)
	compatSeedPR(t, h, "compat-pr-merge", "merge-br")

	w := h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/compat-pr-merge/pulls/1/merge", map[string]any{
		"merge_method": "merge",
	})
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)

	// GitHub merge response includes sha, merged, message.
	assertFieldsPresent(t, resp, map[string]string{
		"sha":     "string",
		"merged":  "bool",
		"message": "string",
	})
}
