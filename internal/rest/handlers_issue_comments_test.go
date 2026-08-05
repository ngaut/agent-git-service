package rest_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

func TestCreateIssueCommentRejectsRepliesBeyondMaxDepth(t *testing.T) {
	h := testharness.New(t)
	issue := seedCommentIssue(t, h, "comment-depth-limit")
	ctx := context.Background()

	root, err := h.Svc.CreateIssueComment(ctx, issue.Repository.FullName, issue.Number, "Root", h.User.Login, nil)
	if err != nil {
		t.Fatalf("CreateIssueComment (root) failed: %v", err)
	}
	reply1, err := h.Svc.CreateIssueComment(ctx, issue.Repository.FullName, issue.Number, "Reply 1", h.User.Login, &root.ID)
	if err != nil {
		t.Fatalf("CreateIssueComment (reply1) failed: %v", err)
	}
	reply2, err := h.Svc.CreateIssueComment(ctx, issue.Repository.FullName, issue.Number, "Reply 2", h.User.Login, &reply1.ID)
	if err != nil {
		t.Fatalf("CreateIssueComment (reply2) failed: %v", err)
	}
	reply3, err := h.Svc.CreateIssueComment(ctx, issue.Repository.FullName, issue.Number, "Reply 3", h.User.Login, &reply2.ID)
	if err != nil {
		t.Fatalf("CreateIssueComment (reply3) failed: %v", err)
	}

	allowedPath := fmt.Sprintf("/api/v3/repos/%s/issues/%d/comments", issue.Repository.FullName, issue.Number)
	w := h.DoRESTJSON(t, http.MethodPost, allowedPath, map[string]any{
		"body":        "Reply 4",
		"in_reply_to": reply3.ID,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST comment at depth 4: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	created := testharness.DecodeJSON(t, w)
	reply4IDValue, ok := created["id"].(float64)
	if !ok {
		t.Fatalf("created comment missing numeric id: %+v", created)
	}
	reply4ID := uint(reply4IDValue)

	w = h.DoRESTJSON(t, http.MethodPost, allowedPath, map[string]any{
		"body":        "Reply 5",
		"in_reply_to": reply4ID,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("POST comment beyond depth limit: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	resp := testharness.DecodeJSON(t, w)
	if got, want := resp["message"], "reply would exceed maximum issue comment thread depth of 5 levels"; got != want {
		t.Fatalf("message = %v, want %q", got, want)
	}
}

func TestListRepoIssueComments(t *testing.T) {
	h := testharness.New(t)
	issue := seedCommentIssue(t, h, "repo-comments")
	ctx := context.Background()

	first, err := h.Svc.CreateIssueComment(ctx, issue.Repository.FullName, issue.Number, "first", h.User.Login, nil)
	if err != nil {
		t.Fatalf("create first comment: %v", err)
	}
	second, err := h.Svc.CreateIssueComment(ctx, issue.Repository.FullName, issue.Number, "second", h.User.Login, nil)
	if err != nil {
		t.Fatalf("create second comment: %v", err)
	}

	w := h.DoREST(t, http.MethodGet, fmt.Sprintf("/api/v3/repos/%s/issues/comments?sort=created&direction=desc", issue.Repository.FullName), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET repo issue comments: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	items := testharness.DecodeJSONArray(t, w)
	if len(items) != 2 {
		t.Fatalf("expected two comments, got %d", len(items))
	}
	if got := uint(items[0]["id"].(float64)); got != second.ID {
		t.Fatalf("first listed id: got %d, want second comment %d", got, second.ID)
	}
	if got := uint(items[1]["id"].(float64)); got != first.ID {
		t.Fatalf("second listed id: got %d, want first comment %d", got, first.ID)
	}
}

func seedCommentIssue(t *testing.T, h *testharness.Harness, repoName string) db.Issue {
	t.Helper()

	ctx := context.Background()
	repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       repoName,
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	created, err := h.Svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: repo.FullName,
		Title:        "comment conversation",
		AuthorLogin:  h.User.Login,
		Labels:       []string{"type:conversation"},
	})
	if err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	issue, err := h.Svc.GetIssueByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("reload issue: %v", err)
	}
	return issue
}
