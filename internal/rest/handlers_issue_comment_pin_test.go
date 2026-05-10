package rest_test

import (
	"context"
	"fmt"
	"testing"

	"gh-server/internal/service"
	"gh-server/internal/testharness"
)

func TestIssueCommentPinEndpoints(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "issue-comment-pin")

	full := "testuser/issue-comment-pin"
	issue, err := h.Svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: full,
		Title:        "Pin comment test",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	comment, err := h.Svc.CreateIssueComment(ctx, full, issue.Number, "Pin this comment", h.User.Login, nil)
	if err != nil {
		t.Fatalf("CreateIssueComment: %v", err)
	}

	path := fmt.Sprintf("/api/v3/repos/testuser/issue-comment-pin/issues/comments/%d/pin", comment.ID)

	w := h.DoRESTJSON(t, "POST", path, nil)
	assertStatusCode(t, w, 405)

	w = h.DoRESTJSON(t, "PUT", path, nil)
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)

	if resp["isPinned"] != true {
		t.Fatalf("isPinned: got %v, want true", resp["isPinned"])
	}
	if resp["is_pinned"] != true {
		t.Fatalf("is_pinned: got %v, want true", resp["is_pinned"])
	}
	if resp["pinnedAt"] == nil {
		t.Fatal("expected pinnedAt to be set")
	}
	if resp["pinned_at"] == nil {
		t.Fatal("expected pinned_at to be set")
	}

	stored, err := h.Svc.GetIssueCommentByID(ctx, comment.ID)
	if err != nil {
		t.Fatalf("GetIssueCommentByID after pin: %v", err)
	}
	if !stored.IsPinned {
		t.Fatal("expected stored comment to be pinned")
	}
	if stored.PinnedAt == nil {
		t.Fatal("expected stored pinned_at to be set")
	}

	w = h.DoRESTJSON(t, "DELETE", path, nil)
	assertStatusCode(t, w, 200)
	resp = testharness.DecodeJSON(t, w)

	if resp["isPinned"] != false {
		t.Fatalf("isPinned: got %v, want false", resp["isPinned"])
	}
	if resp["is_pinned"] != false {
		t.Fatalf("is_pinned: got %v, want false", resp["is_pinned"])
	}
	if resp["pinnedAt"] != nil {
		t.Fatalf("expected pinnedAt to be cleared, got %v", resp["pinnedAt"])
	}
	if resp["pinned_at"] != nil {
		t.Fatalf("expected pinned_at to be cleared, got %v", resp["pinned_at"])
	}

	stored, err = h.Svc.GetIssueCommentByID(ctx, comment.ID)
	if err != nil {
		t.Fatalf("GetIssueCommentByID after unpin: %v", err)
	}
	if stored.IsPinned {
		t.Fatal("expected stored comment to be unpinned")
	}
	if stored.PinnedAt != nil {
		t.Fatalf("expected stored pinned_at to be cleared, got %v", stored.PinnedAt)
	}
}
