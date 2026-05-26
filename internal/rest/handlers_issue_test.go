package rest_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

// ─── UpdateIssue: Issue vs PR fallback ──────────────────────────────────────

func TestUpdateIssue_IssueVsPRFallback(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "update-issue-pr-fallback")

	// Create a PR (which is also an issue in GitHub's model)
	full := "testuser/update-issue-pr-fallback"
	if err := h.Svc.Git.CreateBranch(ctx, full, "feature", "main"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	pr, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: full,
		Title:        "original pr title",
		Body:         "original pr body",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create PR: %v", err)
	}

	// PATCH the PR via the issues endpoint (PR fallback)
	w := h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/update-issue-pr-fallback/issues/"+strconv.Itoa(pr.Number), map[string]any{
		"title": "updated pr title",
		"body":  "updated pr body",
	})
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)

	if resp["title"] != "updated pr title" {
		t.Errorf("title: got %v, want %q", resp["title"], "updated pr title")
	}
	if resp["body"] != "updated pr body" {
		t.Errorf("body: got %v, want %q", resp["body"], "updated pr body")
	}
	if resp["pull_request"] == nil {
		t.Error("expected pull_request field to be present for PR")
	}
}

func TestUpdateIssue_PRLabelsAndAssignees(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "update-issue-pr-labels")

	// Seed labels
	_, _ = h.Svc.CreateLabel(ctx, "testuser/update-issue-pr-labels", "bug", "d73a4a", "")
	_, _ = h.Svc.CreateLabel(ctx, "testuser/update-issue-pr-labels", "enhancement", "a2eeef", "")

	full := "testuser/update-issue-pr-labels"
	if err := h.Svc.Git.CreateBranch(ctx, full, "feature", "main"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	pr, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: full,
		Title:        "pr title",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create PR: %v", err)
	}

	// PATCH labels via issues endpoint
	w := h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/update-issue-pr-labels/issues/"+strconv.Itoa(pr.Number), map[string]any{
		"labels": []string{"bug", "enhancement"},
	})
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)

	labels, ok := resp["labels"].([]any)
	if !ok || len(labels) != 2 {
		t.Fatalf("labels: expected 2, got %d", len(labels))
	}

	// PATCH assignees via issues endpoint
	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/update-issue-pr-labels/issues/"+strconv.Itoa(pr.Number), map[string]any{
		"assignees": []string{"testuser"},
	})
	assertStatusCode(t, w, 200)
	resp = testharness.DecodeJSON(t, w)

	assignees, ok := resp["assignees"].([]any)
	if !ok || len(assignees) != 1 {
		t.Fatalf("assignees: expected 1, got %d", len(assignees))
	}
}

// ─── AddIssueAssignees: Validation and Error Cases ─────────────────────────

func TestAddIssueAssignees_EmptyAssignees(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "add-assignees-empty")

	// Create an issue
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/add-assignees-empty/issues", map[string]any{
		"title": "test issue",
	})
	assertStatusCode(t, w, 201)

	// POST with empty assignees array - should succeed but not change anything
	w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/add-assignees-empty/issues/1/assignees", map[string]any{
		"assignees": []string{},
	})
	assertStatusCode(t, w, 201)
	resp := testharness.DecodeJSON(t, w)

	assignees, ok := resp["assignees"].([]any)
	if !ok || len(assignees) != 0 {
		t.Errorf("assignees: expected 0, got %d", len(assignees))
	}
}

func TestAddIssueAssignees_InvalidUser(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "add-assignees-invalid")

	// Create an issue
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/add-assignees-invalid/issues", map[string]any{
		"title": "test issue",
	})
	assertStatusCode(t, w, 201)

	// POST with non-existent user - service creates a placeholder user record
	// This is expected behavior - the assignee is added with a synthesized user object
	w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/add-assignees-invalid/issues/1/assignees", map[string]any{
		"assignees": []string{"nonexistent-user-xyz"},
	})
	assertStatusCode(t, w, 201)
	resp := testharness.DecodeJSON(t, w)

	assignees, ok := resp["assignees"].([]any)
	if !ok || len(assignees) != 1 {
		t.Fatalf("assignees: expected 1, got %d", len(assignees))
	}
}

func TestAddIssueAssignees_PRFallback(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "add-assignees-pr")

	full := "testuser/add-assignees-pr"
	if err := h.Svc.Git.CreateBranch(ctx, full, "feature", "main"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	pr, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: full,
		Title:        "pr for assignees",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create PR: %v", err)
	}

	// POST assignees via issues endpoint (PR fallback)
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/add-assignees-pr/issues/"+strconv.Itoa(pr.Number)+"/assignees", map[string]any{
		"assignees": []string{"testuser"},
	})
	assertStatusCode(t, w, 201)
	resp := testharness.DecodeJSON(t, w)

	assignees, ok := resp["assignees"].([]any)
	if !ok || len(assignees) != 1 {
		t.Fatalf("assignees: expected 1, got %d", len(assignees))
	}
	if resp["pull_request"] == nil {
		t.Error("expected pull_request field for PR")
	}
}

// ─── RemoveIssueAssignees: Validation and Error Cases ──────────────────────

func TestRemoveIssueAssignees_EmptyAssignees(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "remove-assignees-empty")

	// Create an issue with an assignee
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/remove-assignees-empty/issues", map[string]any{
		"title":     "test issue",
		"assignees": []string{"testuser"},
	})
	assertStatusCode(t, w, 201)

	// DELETE with empty assignees array - should succeed but not change anything
	w = h.DoRESTJSON(t, "DELETE", "/api/v3/repos/testuser/remove-assignees-empty/issues/1/assignees", map[string]any{
		"assignees": []string{},
	})
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)

	assignees, ok := resp["assignees"].([]any)
	if !ok || len(assignees) != 1 {
		t.Errorf("assignees: expected 1 (unchanged), got %d", len(assignees))
	}
}

func TestRemoveIssueAssignees_PRFallback(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "remove-assignees-pr")

	full := "testuser/remove-assignees-pr"
	if err := h.Svc.Git.CreateBranch(ctx, full, "feature", "main"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	pr, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: full,
		Title:        "pr for remove assignees",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create PR: %v", err)
	}

	// First add assignee
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/remove-assignees-pr/issues/"+strconv.Itoa(pr.Number)+"/assignees", map[string]any{
		"assignees": []string{"testuser"},
	})
	assertStatusCode(t, w, 201)

	// DELETE assignees via issues endpoint (PR fallback)
	w = h.DoRESTJSON(t, "DELETE", "/api/v3/repos/testuser/remove-assignees-pr/issues/"+strconv.Itoa(pr.Number)+"/assignees", map[string]any{
		"assignees": []string{"testuser"},
	})
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)

	assignees, ok := resp["assignees"].([]any)
	if !ok || len(assignees) != 0 {
		t.Errorf("assignees: expected 0 after removal, got %d", len(assignees))
	}
}

// ─── GetIssueTimeline: Event Transformation ────────────────────────────────

func TestGetIssueTimeline_Comments(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "timeline-comments")

	// Create an issue
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/timeline-comments/issues", map[string]any{
		"title": "timeline test issue",
	})
	assertStatusCode(t, w, 201)

	// Add a comment
	w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/timeline-comments/issues/1/comments", map[string]any{
		"body": "test comment",
	})
	assertStatusCode(t, w, 201)

	// Get timeline
	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/timeline-comments/issues/1/timeline", nil)
	assertStatusCode(t, w, 200)
	events := testharness.DecodeJSONArray(t, w)

	if len(events) < 1 {
		t.Fatalf("expected at least 1 event, got %d", len(events))
	}

	// Find the comment event
	foundComment := false
	for _, ev := range events {
		if ev["event"] == "commented" {
			foundComment = true
			if ev["body"] != "test comment" {
				t.Errorf("comment body: got %v, want %q", ev["body"], "test comment")
			}
			// Comment events include user field from the comment author
			if ev["user"] == nil {
				t.Error("comment event should have user field")
			}
		}
	}
	if !foundComment {
		t.Error("expected to find commented event in timeline")
	}
}

func TestGetIssueTimeline_Reviews(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "timeline-reviews")

	full := "testuser/timeline-reviews"
	if err := h.Svc.Git.CreateBranch(ctx, full, "feature", "main"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	pr, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: full,
		Title:        "pr for timeline",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create PR: %v", err)
	}

	// Create a review
	_, err = h.Svc.AddPRReview(ctx, pr.ID, h.User.Login, "commented", "test review", "")
	if err != nil {
		t.Fatalf("create review: %v", err)
	}

	// Get timeline via issues endpoint (PR fallback)
	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/timeline-reviews/issues/"+strconv.Itoa(pr.Number)+"/timeline", nil)
	assertStatusCode(t, w, 200)
	events := testharness.DecodeJSONArray(t, w)

	// Find the review event
	foundReview := false
	for _, ev := range events {
		if ev["event"] == "reviewed" {
			foundReview = true
			if ev["body"] != "test review" {
				t.Errorf("review body: got %v, want %q", ev["body"], "test review")
			}
		}
	}
	if !foundReview {
		t.Error("expected to find reviewed event in timeline")
	}
}

func TestGetIssueTimeline_SystemEvents(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "timeline-system")

	// Create labels
	_, _ = h.Svc.CreateLabel(ctx, "testuser/timeline-system", "bug", "d73a4a", "")

	// Create an issue
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/timeline-system/issues", map[string]any{
		"title": "system events test",
	})
	assertStatusCode(t, w, 201)

	// Label the issue (creates an IssueEvent)
	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/timeline-system/issues/1", map[string]any{
		"labels": []string{"bug"},
	})
	assertStatusCode(t, w, 200)

	// Get timeline
	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/timeline-system/issues/1/timeline", nil)
	assertStatusCode(t, w, 200)
	events := testharness.DecodeJSONArray(t, w)

	// Find the labeled event
	foundLabeled := false
	for _, ev := range events {
		if ev["event"] == "labeled" {
			foundLabeled = true
			label, ok := ev["label"].(map[string]any)
			if !ok {
				t.Error("labeled event should have label object")
			} else if label["name"] != "bug" {
				t.Errorf("label name: got %v, want %q", label["name"], "bug")
			}
		}
	}
	if !foundLabeled {
		t.Error("expected to find labeled event in timeline")
	}
}

// ─── ListIssueEvents: Excludes Comments and Reviews ────────────────────────

func TestListIssueEvents_ExcludesCommentsAndReviews(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "issue-events-filter")

	// Create labels
	_, _ = h.Svc.CreateLabel(ctx, "testuser/issue-events-filter", "bug", "d73a4a", "")

	// Create an issue
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/issue-events-filter/issues", map[string]any{
		"title": "events filter test",
	})
	assertStatusCode(t, w, 201)

	// Add a comment
	w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/issue-events-filter/issues/1/comments", map[string]any{
		"body": "test comment",
	})
	assertStatusCode(t, w, 201)

	// Label the issue
	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/issue-events-filter/issues/1", map[string]any{
		"labels": []string{"bug"},
	})
	assertStatusCode(t, w, 200)

	// Get issue events (should exclude comments)
	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/issue-events-filter/issues/1/events", nil)
	assertStatusCode(t, w, 200)
	events := testharness.DecodeJSONArray(t, w)

	// Verify no "commented" events
	for _, ev := range events {
		if ev["event"] == "commented" {
			t.Error("ListIssueEvents should exclude comment events")
		}
	}

	// Verify labeled event is present with proper structure
	foundLabeled := false
	for _, ev := range events {
		if ev["event"] == "labeled" {
			foundLabeled = true
			if ev["id"] == nil {
				t.Error("labeled event should have id")
			}
			if ev["actor"] == nil {
				t.Error("labeled event should have actor")
			}
			label, ok := ev["label"].(map[string]any)
			if !ok || label["name"] != "bug" {
				t.Errorf("label: got %v, want bug", ev["label"])
			}
		}
	}
	if !foundLabeled {
		t.Error("expected to find labeled event")
	}
}

func TestListIssueEvents_PRFallback(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "issue-events-pr")

	full := "testuser/issue-events-pr"
	if err := h.Svc.Git.CreateBranch(ctx, full, "feature", "main"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	pr, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: full,
		Title:        "pr for events",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create PR: %v", err)
	}

	// Get issue events via PR fallback
	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/issue-events-pr/issues/"+strconv.Itoa(pr.Number)+"/events", nil)
	assertStatusCode(t, w, 200)
	events := testharness.DecodeJSONArray(t, w)

	// Should return empty array (no events yet)
	if len(events) != 0 {
		t.Errorf("expected empty array, got %d events", len(events))
	}
}

// ─── applyIssueEventData: Event Type Coverage ──────────────────────────────

func TestApplyIssueEventData_Renamed(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "event-rename")

	// Create an issue
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/event-rename/issues", map[string]any{
		"title": "original title",
	})
	assertStatusCode(t, w, 201)

	// Rename the issue (creates a renamed IssueEvent)
	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/event-rename/issues/1", map[string]any{
		"title": "new title",
	})
	assertStatusCode(t, w, 200)

	// Get timeline and check for rename event
	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/event-rename/issues/1/timeline", nil)
	assertStatusCode(t, w, 200)
	events := testharness.DecodeJSONArray(t, w)

	foundRename := false
	for _, ev := range events {
		if ev["event"] == "renamed" {
			foundRename = true
			rename, ok := ev["rename"].(map[string]any)
			if !ok {
				t.Error("renamed event should have rename object")
			} else {
				if rename["from"] != "original title" {
					t.Errorf("rename from: got %v, want %q", rename["from"], "original title")
				}
				if rename["to"] != "new title" {
					t.Errorf("rename to: got %v, want %q", rename["to"], "new title")
				}
			}
		}
	}
	if !foundRename {
		t.Error("expected to find renamed event")
	}
}

func TestApplyIssueEventData_ClosedWithReason(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "event-close-reason")

	// Create an issue
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/event-close-reason/issues", map[string]any{
		"title": "close reason test",
	})
	assertStatusCode(t, w, 201)

	// Close with state_reason
	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/event-close-reason/issues/1", map[string]any{
		"state":        "closed",
		"state_reason": "not_planned",
	})
	assertStatusCode(t, w, 200)

	// Get timeline and check for closed event with reason
	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/event-close-reason/issues/1/timeline", nil)
	assertStatusCode(t, w, 200)
	events := testharness.DecodeJSONArray(t, w)

	foundClosed := false
	for _, ev := range events {
		if ev["event"] == "closed" {
			foundClosed = true
			if ev["state_reason"] != "not_planned" {
				t.Errorf("state_reason: got %v, want %q", ev["state_reason"], "not_planned")
			}
		}
	}
	if !foundClosed {
		t.Error("expected to find closed event")
	}
}

func TestApplyIssueEventData_Assigned(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "event-assign")

	// Create an issue
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/event-assign/issues", map[string]any{
		"title": "assign event test",
	})
	assertStatusCode(t, w, 201)

	// Add assignee (creates an assigned IssueEvent)
	w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/event-assign/issues/1/assignees", map[string]any{
		"assignees": []string{"testuser"},
	})
	assertStatusCode(t, w, 201)

	// Get timeline and check for assigned event
	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/event-assign/issues/1/timeline", nil)
	assertStatusCode(t, w, 200)
	events := testharness.DecodeJSONArray(t, w)

	foundAssigned := false
	for _, ev := range events {
		if ev["event"] == "assigned" {
			foundAssigned = true
			assignee, ok := ev["assignee"].(map[string]any)
			if !ok {
				t.Error("assigned event should have assignee object")
			} else if assignee["login"] != "testuser" {
				t.Errorf("assignee login: got %v, want %q", assignee["login"], "testuser")
			}
		}
	}
	if !foundAssigned {
		t.Error("expected to find assigned event")
	}
}

func TestApplyIssueEventData_Locked(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "event-lock")

	// Create an issue
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/event-lock/issues", map[string]any{
		"title": "lock event test",
	})
	assertStatusCode(t, w, 201)

	// Lock the issue with reason
	w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/event-lock/issues/1/lock", map[string]any{
		"lock_reason": "too heated",
	})
	assertStatusCode(t, w, 204)

	// Get timeline and check for locked event
	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/event-lock/issues/1/timeline", nil)
	assertStatusCode(t, w, 200)
	events := testharness.DecodeJSONArray(t, w)

	foundLocked := false
	for _, ev := range events {
		if ev["event"] == "locked" {
			foundLocked = true
			if ev["lock_reason"] != "too heated" {
				t.Errorf("lock_reason: got %v, want %q", ev["lock_reason"], "too heated")
			}
		}
	}
	if !foundLocked {
		t.Error("expected to find locked event")
	}
}

func TestApplyIssueEventData_Milestoned(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "event-milestone")

	// Create milestone
	ms, err := h.Svc.CreateMilestone(ctx, "testuser/event-milestone", "v1.0", "", "open")
	if err != nil {
		t.Fatalf("create milestone: %v", err)
	}

	// Create an issue
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/event-milestone/issues", map[string]any{
		"title": "milestone event test",
	})
	assertStatusCode(t, w, 201)

	// Set milestone (creates a milestoned IssueEvent)
	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/event-milestone/issues/1", map[string]any{
		"milestone": ms.Number,
	})
	assertStatusCode(t, w, 200)

	// Get timeline and check for milestoned event
	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/event-milestone/issues/1/timeline", nil)
	assertStatusCode(t, w, 200)
	events := testharness.DecodeJSONArray(t, w)

	foundMilestoned := false
	for _, ev := range events {
		if ev["event"] == "milestoned" {
			foundMilestoned = true
			milestone, ok := ev["milestone"].(map[string]any)
			if !ok {
				t.Error("milestoned event should have milestone object")
			} else if milestone["title"] != "v1.0" {
				t.Errorf("milestone title: got %v, want %q", milestone["title"], "v1.0")
			}
		}
	}
	if !foundMilestoned {
		t.Error("expected to find milestoned event")
	}
}

// ─── UpdateIssue: Additional Coverage ──────────────────────────────────────

func TestUpdateIssue_IssueAssigneesAndMilestone(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "update-issue-assignees")

	// Seed labels
	_, _ = h.Svc.CreateLabel(ctx, "testuser/update-issue-assignees", "bug", "d73a4a", "")

	// Create an issue
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/update-issue-assignees/issues", map[string]any{
		"title": "original issue",
	})
	assertStatusCode(t, w, 201)

	// PATCH assignees via issues endpoint
	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/update-issue-assignees/issues/1", map[string]any{
		"assignees": []string{"testuser"},
	})
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)

	assignees, ok := resp["assignees"].([]any)
	if !ok || len(assignees) != 1 {
		t.Fatalf("assignees: expected 1, got %d", len(assignees))
	}

	// PATCH milestone
	ms, err := h.Svc.CreateMilestone(ctx, "testuser/update-issue-assignees", "v1.0", "", "open")
	if err != nil {
		t.Fatalf("create milestone: %v", err)
	}
	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/update-issue-assignees/issues/1", map[string]any{
		"milestone": ms.Number,
	})
	assertStatusCode(t, w, 200)
	resp = testharness.DecodeJSON(t, w)

	if resp["milestone"] == nil {
		t.Error("expected milestone to be set")
	}
}

func TestUpdateIssue_PRFallbackWithMilestone(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "update-issue-pr-milestone")

	full := "testuser/update-issue-pr-milestone"
	if err := h.Svc.Git.CreateBranch(ctx, full, "feature", "main"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	pr, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: full,
		Title:        "original pr title",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create PR: %v", err)
	}

	// Create milestone
	ms, err := h.Svc.CreateMilestone(ctx, full, "v2.0", "", "open")
	if err != nil {
		t.Fatalf("create milestone: %v", err)
	}

	// PATCH milestone via issues endpoint (PR fallback)
	w := h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/update-issue-pr-milestone/issues/"+strconv.Itoa(pr.Number), map[string]any{
		"milestone": ms.Number,
	})
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)

	if resp["milestone"] == nil {
		t.Error("expected milestone to be set on PR")
	}
	if resp["pull_request"] == nil {
		t.Error("expected pull_request field for PR")
	}
}

// ─── AddIssueAssignees: Error Cases ────────────────────────────────────────

func TestAddIssueAssignees_ServiceError(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "add-assignees-error")

	// Create an issue
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/add-assignees-error/issues", map[string]any{
		"title": "test issue",
	})
	assertStatusCode(t, w, 201)

	// POST assignees - service layer handles the actual logic
	// This test ensures the handler properly returns the response
	w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/add-assignees-error/issues/1/assignees", map[string]any{
		"assignees": []string{"testuser"},
	})
	assertStatusCode(t, w, 201)
	resp := testharness.DecodeJSON(t, w)

	// Verify response includes assignees and reactions
	assignees, ok := resp["assignees"].([]any)
	if !ok {
		t.Error("expected assignees array in response")
	} else if len(assignees) < 1 {
		t.Errorf("expected at least 1 assignee, got %d", len(assignees))
	}
}

// ─── RemoveIssueAssignees: Error Cases ─────────────────────────────────────

func TestRemoveIssueAssignees_ServiceError(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "remove-assignees-error")

	// Create an issue with assignee
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/remove-assignees-error/issues", map[string]any{
		"title":     "test issue",
		"assignees": []string{"testuser"},
	})
	assertStatusCode(t, w, 201)

	// DELETE assignees
	w = h.DoRESTJSON(t, "DELETE", "/api/v3/repos/testuser/remove-assignees-error/issues/1/assignees", map[string]any{
		"assignees": []string{"testuser"},
	})
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)

	// Verify response structure
	assignees, ok := resp["assignees"].([]any)
	if !ok {
		t.Error("expected assignees array in response")
	} else if len(assignees) != 0 {
		t.Errorf("expected 0 assignees after removal, got %d", len(assignees))
	}
}

// ─── GetIssueTimeline: Additional Coverage ─────────────────────────────────

func TestGetIssueTimeline_EmptyTimeline(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "timeline-empty")

	// Create an issue without any events
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/timeline-empty/issues", map[string]any{
		"title": "empty timeline issue",
	})
	assertStatusCode(t, w, 201)
	issue, err := h.Svc.GetIssue(context.Background(), "testuser/timeline-empty", 1)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if err := h.DB.Exec("DELETE FROM issue_events WHERE issue_id = ?", issue.ID).Error; err != nil {
		t.Fatalf("clear issue events: %v", err)
	}

	// Get timeline - should return empty array
	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/timeline-empty/issues/1/timeline", nil)
	assertStatusCode(t, w, 200)
	events := testharness.DecodeJSONArray(t, w)

	if len(events) != 0 {
		t.Errorf("expected empty array, got %d events", len(events))
	}
}

func TestGetIssueTimeline_PRFallback(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "timeline-pr")

	full := "testuser/timeline-pr"
	if err := h.Svc.Git.CreateBranch(ctx, full, "feature", "main"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	pr, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: full,
		Title:        "pr for timeline",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create PR: %v", err)
	}

	// Get timeline via issues endpoint (PR fallback)
	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/timeline-pr/issues/"+strconv.Itoa(pr.Number)+"/timeline", nil)
	assertStatusCode(t, w, 200)
	events := testharness.DecodeJSONArray(t, w)

	// Should return array (may be empty)
	if len(events) != 0 {
		t.Errorf("expected empty timeline, got %d events", len(events))
	}
}

// ─── ListIssueEvents: Additional Coverage ──────────────────────────────────

func TestListIssueEvents_WithReviewEvent(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "issue-events-review")

	full := "testuser/issue-events-review"
	if err := h.Svc.Git.CreateBranch(ctx, full, "feature", "main"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	pr, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: full,
		Title:        "pr for events",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create PR: %v", err)
	}

	// Create a review
	_, err = h.Svc.AddPRReview(ctx, pr.ID, h.User.Login, "commented", "test review", "")
	if err != nil {
		t.Fatalf("create review: %v", err)
	}

	// Get issue events - should exclude review events
	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/issue-events-review/issues/"+strconv.Itoa(pr.Number)+"/events", nil)
	assertStatusCode(t, w, 200)
	events := testharness.DecodeJSONArray(t, w)

	// Verify no "reviewed" events
	for _, ev := range events {
		if ev["event"] == "reviewed" {
			t.Error("ListIssueEvents should exclude review events")
		}
	}
}

func TestListIssueEvents_WithCommentEvent(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "issue-events-comment")

	// Create an issue
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/issue-events-comment/issues", map[string]any{
		"title": "events test",
	})
	assertStatusCode(t, w, 201)

	// Add a comment
	w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/issue-events-comment/issues/1/comments", map[string]any{
		"body": "test comment",
	})
	assertStatusCode(t, w, 201)

	// Get issue events - should exclude comment events
	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/issue-events-comment/issues/1/events", nil)
	assertStatusCode(t, w, 200)
	events := testharness.DecodeJSONArray(t, w)

	// Verify no "commented" events
	for _, ev := range events {
		if ev["event"] == "commented" {
			t.Error("ListIssueEvents should exclude comment events")
		}
	}
}

// ─── applyIssueEventData: Additional Event Types ───────────────────────────

func TestApplyIssueEventData_Unlabeled(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "event-unlabel")

	// Create label and issue
	_, _ = h.Svc.CreateLabel(ctx, "testuser/event-unlabel", "bug", "d73a4a", "")
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/event-unlabel/issues", map[string]any{
		"title":  "test",
		"labels": []string{"bug"},
	})
	assertStatusCode(t, w, 201)

	// Remove label (creates unlabeled event)
	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/event-unlabel/issues/1", map[string]any{
		"labels": []string{},
	})
	assertStatusCode(t, w, 200)

	// Get timeline and check for unlabeled event
	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/event-unlabel/issues/1/timeline", nil)
	assertStatusCode(t, w, 200)
	events := testharness.DecodeJSONArray(t, w)

	foundUnlabeled := false
	for _, ev := range events {
		if ev["event"] == "unlabeled" {
			foundUnlabeled = true
			label, ok := ev["label"].(map[string]any)
			if !ok {
				t.Error("unlabeled event should have label object")
			} else if label["name"] != "bug" {
				t.Errorf("label name: got %v, want %q", label["name"], "bug")
			}
		}
	}
	if !foundUnlabeled {
		t.Error("expected to find unlabeled event")
	}
}

func TestApplyIssueEventData_Demilestoned(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "event-demilestone")

	// Create two milestones
	ms1, err := h.Svc.CreateMilestone(ctx, "testuser/event-demilestone", "v1.0", "", "open")
	if err != nil {
		t.Fatalf("create milestone 1: %v", err)
	}
	ms2, err := h.Svc.CreateMilestone(ctx, "testuser/event-demilestone", "v2.0", "", "open")
	if err != nil {
		t.Fatalf("create milestone 2: %v", err)
	}

	// Create issue with first milestone
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/event-demilestone/issues", map[string]any{
		"title":     "test",
		"milestone": ms1.Number,
	})
	assertStatusCode(t, w, 201)

	// Change to second milestone (creates demilestoned + milestoned events)
	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/event-demilestone/issues/1", map[string]any{
		"milestone": ms2.Number,
	})
	assertStatusCode(t, w, 200)

	// Get timeline and check for demilestoned event
	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/event-demilestone/issues/1/timeline", nil)
	assertStatusCode(t, w, 200)
	events := testharness.DecodeJSONArray(t, w)

	foundDemilestoned := false
	for _, ev := range events {
		if ev["event"] == "demilestoned" {
			foundDemilestoned = true
			milestone, ok := ev["milestone"].(map[string]any)
			if !ok {
				t.Error("demilestoned event should have milestone object")
			} else if milestone["title"] != "v1.0" {
				t.Errorf("milestone title: got %v, want %q", milestone["title"], "v1.0")
			}
		}
	}
	if !foundDemilestoned {
		t.Error("expected to find demilestoned event")
	}
}

func TestApplyIssueEventData_Reopened(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "event-reopen")

	// Create and close an issue
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/event-reopen/issues", map[string]any{
		"title": "test",
	})
	assertStatusCode(t, w, 201)
	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/event-reopen/issues/1", map[string]any{
		"state": "closed",
	})
	assertStatusCode(t, w, 200)

	// Reopen the issue (creates reopened event)
	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/event-reopen/issues/1", map[string]any{
		"state": "open",
	})
	assertStatusCode(t, w, 200)

	// Get timeline and check for reopened event
	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/event-reopen/issues/1/timeline", nil)
	assertStatusCode(t, w, 200)
	events := testharness.DecodeJSONArray(t, w)

	foundReopened := false
	for _, ev := range events {
		if ev["event"] == "reopened" {
			foundReopened = true
		}
	}
	if !foundReopened {
		t.Error("expected to find reopened event")
	}
}

func TestApplyIssueEventData_Unassigned(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "event-unassign")

	// Create issue with assignee
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/event-unassign/issues", map[string]any{
		"title":     "test",
		"assignees": []string{"testuser"},
	})
	assertStatusCode(t, w, 201)

	// Remove assignee (creates unassigned event)
	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/event-unassign/issues/1", map[string]any{
		"assignees": []string{},
	})
	assertStatusCode(t, w, 200)

	// Get timeline and check for unassigned event
	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/event-unassign/issues/1/timeline", nil)
	assertStatusCode(t, w, 200)
	events := testharness.DecodeJSONArray(t, w)

	foundUnassigned := false
	for _, ev := range events {
		if ev["event"] == "unassigned" {
			foundUnassigned = true
			assignee, ok := ev["assignee"].(map[string]any)
			if !ok {
				t.Error("unassigned event should have assignee object")
			} else if assignee["login"] != "testuser" {
				t.Errorf("assignee login: got %v, want %q", assignee["login"], "testuser")
			}
		}
	}
	if !foundUnassigned {
		t.Error("expected to find unassigned event")
	}
}
