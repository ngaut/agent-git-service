package graphql_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

func seedIssueCommentForPinMutation(t *testing.T, svc *service.Service, userLogin, repoName string) db.IssueComment {
	t.Helper()

	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: userLogin,
		Name:       repoName,
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fmt.Sprintf("%s/%s", userLogin, repo.Name),
		Title:        "Comment pin mutation test issue",
		Body:         "issue body",
		AuthorLogin:  userLogin,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	comment, err := svc.CreateIssueComment(ctx, fmt.Sprintf("%s/%s", userLogin, repo.Name), issue.Number, "comment to pin", userLogin, nil)
	if err != nil {
		t.Fatalf("CreateIssueComment: %v", err)
	}

	return comment
}

func TestGraphQL_PinIssueComment_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()

	comment := seedIssueCommentForPinMutation(t, svc, u.Login, "pin-issue-comment-repo")

	mut := `
	mutation($input: PinIssueCommentInput!) {
		pinIssueComment(input: $input) {
			issueComment {
				id
				isPinned
				pinnedAt
				viewerDidAuthor
				author { login }
				url
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"commentId": fmt.Sprintf("IssueComment_%d", comment.ID),
		},
	})

	pinResult := data["pinIssueComment"].(map[string]any)
	pinnedComment := pinResult["issueComment"].(map[string]any)

	if pinnedComment["id"] != fmt.Sprintf("IssueComment_%d", comment.ID) {
		t.Errorf("id: got %v, want %s", pinnedComment["id"], fmt.Sprintf("IssueComment_%d", comment.ID))
	}
	if pinnedComment["isPinned"] != true {
		t.Errorf("isPinned: got %v, want true", pinnedComment["isPinned"])
	}
	if pinnedComment["pinnedAt"] == nil || pinnedComment["pinnedAt"] == "" {
		t.Fatalf("expected pinnedAt to be populated, got %v", pinnedComment["pinnedAt"])
	}
	if pinnedComment["viewerDidAuthor"] != true {
		t.Errorf("viewerDidAuthor: got %v, want true", pinnedComment["viewerDidAuthor"])
	}
	if pinnedComment["author"].(map[string]any)["login"] != u.Login {
		t.Errorf("author.login: got %v, want %s", pinnedComment["author"].(map[string]any)["login"], u.Login)
	}
	if !strings.HasSuffix(fmt.Sprint(pinnedComment["url"]), fmt.Sprintf("/comment/%d", comment.ID)) {
		t.Errorf("url: got %v, want suffix %s", pinnedComment["url"], fmt.Sprintf("/comment/%d", comment.ID))
	}

	stored, err := svc.GetIssueCommentByID(context.Background(), comment.ID)
	if err != nil {
		t.Fatalf("GetIssueCommentByID after pin: %v", err)
	}
	if !stored.IsPinned {
		t.Fatal("expected stored comment to be pinned")
	}
	if stored.PinnedAt == nil {
		t.Fatal("expected stored pinnedAt to be set")
	}
}

func TestGraphQL_UnpinIssueComment_Success(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()

	ctx := context.Background()
	comment := seedIssueCommentForPinMutation(t, svc, u.Login, "unpin-issue-comment-repo")

	if err := svc.PinIssueComment(ctx, comment.ID, true); err != nil {
		t.Fatalf("PinIssueComment: %v", err)
	}

	mut := `
	mutation($input: UnpinIssueCommentInput!) {
		unpinIssueComment(input: $input) {
			issueComment {
				id
				isPinned
				pinnedAt
				viewerDidAuthor
				author { login }
				url
			}
		}
	}`
	data := doGql(t, mux, mut, map[string]any{
		"input": map[string]any{
			"issueCommentId": fmt.Sprintf("IssueComment_%d", comment.ID),
		},
	})

	unpinResult := data["unpinIssueComment"].(map[string]any)
	unpinnedComment := unpinResult["issueComment"].(map[string]any)

	if unpinnedComment["id"] != fmt.Sprintf("IssueComment_%d", comment.ID) {
		t.Errorf("id: got %v, want %s", unpinnedComment["id"], fmt.Sprintf("IssueComment_%d", comment.ID))
	}
	if unpinnedComment["isPinned"] != false {
		t.Errorf("isPinned: got %v, want false", unpinnedComment["isPinned"])
	}
	if unpinnedComment["pinnedAt"] != nil {
		t.Fatalf("expected pinnedAt to be cleared, got %v", unpinnedComment["pinnedAt"])
	}
	if unpinnedComment["viewerDidAuthor"] != true {
		t.Errorf("viewerDidAuthor: got %v, want true", unpinnedComment["viewerDidAuthor"])
	}
	if unpinnedComment["author"].(map[string]any)["login"] != u.Login {
		t.Errorf("author.login: got %v, want %s", unpinnedComment["author"].(map[string]any)["login"], u.Login)
	}
	if !strings.HasSuffix(fmt.Sprint(unpinnedComment["url"]), fmt.Sprintf("/comment/%d", comment.ID)) {
		t.Errorf("url: got %v, want suffix %s", unpinnedComment["url"], fmt.Sprintf("/comment/%d", comment.ID))
	}

	stored, err := svc.GetIssueCommentByID(ctx, comment.ID)
	if err != nil {
		t.Fatalf("GetIssueCommentByID after unpin: %v", err)
	}
	if stored.IsPinned {
		t.Fatal("expected stored comment to be unpinned")
	}
	if stored.PinnedAt != nil {
		t.Fatalf("expected stored pinnedAt to be cleared, got %v", stored.PinnedAt)
	}
}
