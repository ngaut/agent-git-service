package rest_test

import (
	"context"
	"fmt"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
	"gh-server/internal/testharness"
)

// ─── Issue GET Response Fields ──────────────────────────────────────────────

func TestCompat_IssueGet_ResponseFields(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "compat-issue")

	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/compat-issue/issues", map[string]any{
		"title": "compat test issue",
		"body":  "body content",
	})
	assertStatusCode(t, w, 201)

	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-issue/issues/1", nil)
	assertStatusCode(t, w, 200)
	body := testharness.DecodeJSON(t, w)

	// GitHub REST API Issue response fields (https://docs.github.com/en/rest/issues/issues#get-an-issue)
	assertFieldsPresent(t, body, map[string]string{
		"id":                 "number",
		"node_id":            "string",
		"number":             "number",
		"title":              "string",
		"body":               "string",
		"state":              "string",
		"locked":             "bool",
		"user":               "object",
		"labels":             "array",
		"assignee":           "",
		"assignees":          "array",
		"milestone":          "",
		"url":                "string",
		"html_url":           "string",
		"repository_url":     "string",
		"comments_url":       "string",
		"events_url":         "string",
		"labels_url":         "string",
		"comments":           "number",
		"created_at":         "string",
		"updated_at":         "string",
		"author_association": "string",
		"reactions":          "object",
		"state_reason":       "",
		"active_lock_reason": "",
	})

	// Verify user sub-object has required fields.
	user, _ := body["user"].(map[string]any)
	if user != nil {
		assertFieldsPresent(t, user, map[string]string{
			"login":      "string",
			"id":         "number",
			"avatar_url": "string",
			"url":        "string",
			"html_url":   "string",
			"type":       "string",
		})
	}
}

// ─── Issue CREATE Singular Assignee ─────────────────────────────────────────

func TestCompat_IssueCREATE_SingularAssignee(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "compat-issue-create-assignee")

	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/compat-issue-create-assignee/issues", map[string]any{
		"title":    "assignee singular test",
		"assignee": "testuser",
	})
	assertStatusCode(t, w, 201)
	resp := testharness.DecodeJSON(t, w)

	assignees, ok := resp["assignees"].([]any)
	if !ok || len(assignees) != 1 {
		t.Fatalf("assignees: expected 1, got %d", len(assignees))
	}
	assignee := assignees[0].(map[string]any)
	if assignee["login"] != "testuser" {
		t.Errorf("assignee login: got %v, want %q", assignee["login"], "testuser")
	}
}

// ─── Issue PATCH Partial Update ─────────────────────────────────────────────

func TestCompat_IssuePATCH_PartialUpdate(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "compat-issue-patch")

	// Create issue with specific body.
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/compat-issue-patch/issues", map[string]any{
		"title": "original title",
		"body":  "original body",
	})
	assertStatusCode(t, w, 201)

	// PATCH only title — body must remain unchanged.
	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/compat-issue-patch/issues/1", map[string]any{
		"title": "updated title",
	})
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)

	if resp["title"] != "updated title" {
		t.Errorf("title: got %v, want %q", resp["title"], "updated title")
	}
	if resp["body"] != "original body" {
		t.Errorf("body should be unchanged: got %v, want %q", resp["body"], "original body")
	}
}

// ─── Issue PATCH Label Replacement ──────────────────────────────────────────

func TestCompat_IssuePATCH_LabelReplacement(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "compat-issue-label")

	// Seed labels.
	_, _ = h.Svc.CreateLabel(ctx, "testuser/compat-issue-label", "bug", "d73a4a", "")
	_, _ = h.Svc.CreateLabel(ctx, "testuser/compat-issue-label", "enhancement", "a2eeef", "")
	_, _ = h.Svc.CreateLabel(ctx, "testuser/compat-issue-label", "docs", "0075ca", "")

	// Create issue with initial labels.
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/compat-issue-label/issues", map[string]any{
		"title":  "label test",
		"labels": []string{"bug", "enhancement"},
	})
	assertStatusCode(t, w, 201)

	// PATCH with labels — should fully replace, not merge.
	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/compat-issue-label/issues/1", map[string]any{
		"labels": []string{"docs"},
	})
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)

	labels, ok := resp["labels"].([]any)
	if !ok {
		t.Fatalf("labels: expected array, got %T", resp["labels"])
	}
	if len(labels) != 1 {
		t.Fatalf("labels: expected 1 label after replacement, got %d", len(labels))
	}
	lbl, _ := labels[0].(map[string]any)
	if lbl["name"] != "docs" {
		t.Errorf("label name: got %v, want %q", lbl["name"], "docs")
	}
}

// ─── Issue PATCH Assignees ──────────────────────────────────────────────────

func TestCompat_IssuePATCH_Assignees(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "compat-issue-assignees")

	// Create issue.
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/compat-issue-assignees/issues", map[string]any{
		"title": "assignee test",
	})
	assertStatusCode(t, w, 201)

	// PATCH with assignees — GitHub API supports this.
	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/compat-issue-assignees/issues/1", map[string]any{
		"assignees": []string{"testuser"},
	})
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)

	assignees, ok := resp["assignees"].([]any)
	if !ok {
		t.Fatalf("assignees: expected array, got %T", resp["assignees"])
	}
	if len(assignees) != 1 {
		t.Errorf("assignees: expected 1, got %d", len(assignees))
	}
}

// ─── Issue PATCH Milestone ──────────────────────────────────────────────────

func TestCompat_IssuePATCH_Milestone(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "compat-issue-milestone")

	// Create milestone.
	ms, err := h.Svc.CreateMilestone(ctx, "testuser/compat-issue-milestone", "v1.0", "", "open")
	if err != nil {
		t.Fatalf("seed milestone: %v", err)
	}

	// Create issue.
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/compat-issue-milestone/issues", map[string]any{
		"title": "milestone test",
	})
	assertStatusCode(t, w, 201)

	// PATCH with milestone — GitHub API accepts milestone number.
	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/compat-issue-milestone/issues/1", map[string]any{
		"milestone": ms.Number,
	})
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)

	milestone := resp["milestone"]
	if milestone == nil {
		t.Fatal("milestone should not be nil after PATCH")
	}
	msMap, ok := milestone.(map[string]any)
	if !ok {
		t.Fatalf("milestone: expected object, got %T", milestone)
	}
	if msMap["title"] != "v1.0" {
		t.Errorf("milestone title: got %v, want %q", msMap["title"], "v1.0")
	}
}

// ─── Issue PATCH StateReason ────────────────────────────────────────────────

func TestCompat_IssuePATCH_StateReason(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "compat-issue-sr")

	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/compat-issue-sr/issues", map[string]any{
		"title": "state reason test",
	})
	assertStatusCode(t, w, 201)

	// Close with state_reason = "not_planned".
	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/compat-issue-sr/issues/1", map[string]any{
		"state":        "closed",
		"state_reason": "not_planned",
	})
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)

	if resp["state"] != "closed" {
		t.Errorf("state: got %v, want %q", resp["state"], "closed")
	}
	if resp["state_reason"] != "not_planned" {
		t.Errorf("state_reason: got %v, want %q", resp["state_reason"], "not_planned")
	}
}

// ─── Issue List Pagination ──────────────────────────────────────────────────

func TestCompat_IssueList_Pagination(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "compat-issue-page")

	// Create 3 issues.
	for i := 1; i <= 3; i++ {
		w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/compat-issue-page/issues", map[string]any{
			"title": fmt.Sprintf("issue %d", i),
		})
		assertStatusCode(t, w, 201)
	}

	// Request page 1 with per_page=2.
	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-issue-page/issues?per_page=2", nil)
	assertStatusCode(t, w, 200)
	items := testharness.DecodeJSONArray(t, w)
	if len(items) != 2 {
		t.Errorf("page 1: expected 2 items, got %d", len(items))
	}
	assertPaginationHeaders(t, w, true)

	// Request page 2.
	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-issue-page/issues?per_page=2&page=2", nil)
	assertStatusCode(t, w, 200)
	items = testharness.DecodeJSONArray(t, w)
	if len(items) != 1 {
		t.Errorf("page 2: expected 1 item, got %d", len(items))
	}
}

// ─── Issue List Includes Pull Requests ─────────────────────────────────────

func TestCompat_IssueList_IncludesPullRequests(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	repoName := "compat-issue-list-pr"
	compatSeedRepo(t, h, repoName)

	full := "testuser/" + repoName
	if err := h.Svc.Git.CreateBranch(ctx, full, "feature", "main"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	pr, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: full,
		Title:        "compat PR",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create PR: %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/"+repoName+"/issues", nil)
	assertStatusCode(t, w, 200)
	items := testharness.DecodeJSONArray(t, w)

	found := false
	for _, item := range items {
		num, _ := item["number"].(float64)
		if int(num) == pr.Number {
			if item["pull_request"] == nil {
				t.Fatalf("expected PR item to include pull_request field")
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("expected PR #%d in issues list", pr.Number)
	}
}

func TestCompat_IssueList_MentionedIncludesPullRequests(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	repoName := "compat-issue-list-mentioned-pr"
	compatSeedRepo(t, h, repoName)

	full := "testuser/" + repoName
	if err := h.Svc.Git.CreateBranch(ctx, full, "mentioned-feature", "main"); err != nil {
		t.Fatalf("create mentioned branch: %v", err)
	}
	mentionedPR, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: full,
		Title:        "mentions user",
		Body:         "please review @testuser",
		HeadRef:      "mentioned-feature",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create mentioned PR: %v", err)
	}
	if err := h.Svc.Git.CreateBranch(ctx, full, "plain-feature", "main"); err != nil {
		t.Fatalf("create plain branch: %v", err)
	}
	plainPR, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: full,
		Title:        "plain PR",
		HeadRef:      "plain-feature",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create plain PR: %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/"+repoName+"/issues?state=all&mentioned=testuser", nil)
	assertStatusCode(t, w, 200)
	items := testharness.DecodeJSONArray(t, w)

	foundMentioned := false
	for _, item := range items {
		num, _ := item["number"].(float64)
		switch int(num) {
		case mentionedPR.Number:
			if item["pull_request"] == nil {
				t.Fatalf("expected mentioned PR item to include pull_request field")
			}
			foundMentioned = true
		case plainPR.Number:
			t.Fatalf("unexpected unmentioned PR #%d in mentioned issue list", plainPR.Number)
		}
	}
	if !foundMentioned {
		t.Fatalf("expected mentioned PR #%d in issues list", mentionedPR.Number)
	}
}

func TestCompat_IssueList_MentionedIncludesPRReviewSummaryBodies(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	repoName := "compat-issue-list-mentioned-pr-review"
	compatSeedRepo(t, h, repoName)

	full := "testuser/" + repoName
	if err := h.Svc.Git.CreateBranch(ctx, full, "mentioned-review-feature", "main"); err != nil {
		t.Fatalf("create mentioned branch: %v", err)
	}
	mentionedPR, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: full,
		Title:        "plain title",
		Body:         "plain body",
		HeadRef:      "mentioned-review-feature",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create mentioned PR: %v", err)
	}
	if err := h.Svc.DB.Create(&db.PullRequestReview{
		PullRequestID: mentionedPR.ID,
		AuthorLogin:   h.User.Login,
		State:         "COMMENTED",
		Body:          "review summary for @testuser",
	}).Error; err != nil {
		t.Fatalf("create review summary: %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/"+repoName+"/issues?state=all&mentioned=testuser", nil)
	assertStatusCode(t, w, 200)
	items := testharness.DecodeJSONArray(t, w)

	foundMentioned := false
	for _, item := range items {
		num, _ := item["number"].(float64)
		if int(num) != mentionedPR.Number {
			continue
		}
		if item["pull_request"] == nil {
			t.Fatalf("expected mentioned PR item to include pull_request field")
		}
		foundMentioned = true
	}
	if !foundMentioned {
		t.Fatalf("expected mentioned PR #%d in issues list", mentionedPR.Number)
	}
}

func TestCompat_IssueList_MentionedUsesMentionTokenBoundaries(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	repoName := "compat-issue-list-mentioned-boundaries"
	compatSeedRepo(t, h, repoName)

	full := "testuser/" + repoName
	issueMentioned, err := h.Svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: full,
		Title:        "exact mention",
		Body:         "hello @testuser",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create mentioned issue: %v", err)
	}
	issueSubstr, err := h.Svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: full,
		Title:        "substring only",
		Body:         "hello @testuser2",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create substring issue: %v", err)
	}
	issueEmail, err := h.Svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: full,
		Title:        "email only",
		Body:         "hello foo@testuser",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create email issue: %v", err)
	}

	if err := h.Svc.Git.CreateBranch(ctx, full, "mentioned-boundary", "main"); err != nil {
		t.Fatalf("create exact branch: %v", err)
	}
	prMentioned, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: full,
		Title:        "exact mention pr",
		Body:         "please review @testuser",
		HeadRef:      "mentioned-boundary",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create mentioned pr: %v", err)
	}
	if err := h.Svc.Git.CreateBranch(ctx, full, "substring-boundary", "main"); err != nil {
		t.Fatalf("create substring branch: %v", err)
	}
	prSubstr, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: full,
		Title:        "substring pr",
		Body:         "please review @testuser2",
		HeadRef:      "substring-boundary",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create substring pr: %v", err)
	}
	if err := h.Svc.Git.CreateBranch(ctx, full, "email-boundary", "main"); err != nil {
		t.Fatalf("create email branch: %v", err)
	}
	prEmail, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: full,
		Title:        "email pr",
		Body:         "please review foo@testuser",
		HeadRef:      "email-boundary",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create email pr: %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/"+repoName+"/issues?state=all&mentioned=testuser", nil)
	assertStatusCode(t, w, 200)
	items := testharness.DecodeJSONArray(t, w)

	got := map[int]bool{}
	for _, item := range items {
		num, _ := item["number"].(float64)
		got[int(num)] = true
	}
	if !got[issueMentioned.Number] || !got[prMentioned.Number] {
		t.Fatalf("expected exact-mention issue #%d and PR #%d in results, got %v", issueMentioned.Number, prMentioned.Number, got)
	}
	if got[issueSubstr.Number] || got[issueEmail.Number] || got[prSubstr.Number] || got[prEmail.Number] {
		t.Fatalf("unexpected substring/email match in mentioned filter results: %v", got)
	}
}
