package rest_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/rest/transform"
	"gh-server/internal/service"
	"gh-server/internal/testharness"

	"github.com/stretchr/testify/require"
)

func TestListIssueReactions(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "issue-reactions",
		AutoInit:   true,
	})
	require.NoError(t, err)

	issue, err := h.Svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: h.User.Login + "/issue-reactions",
		Title:        "Reaction target",
		Body:         "body",
		AuthorLogin:  h.User.Login,
	})
	require.NoError(t, err)

	reactor1 := db.User{Login: "issue-reactor-1", Name: "issue-reactor-1", Type: db.TypeUser}
	reactor2 := db.User{Login: "issue-reactor-2", Name: "issue-reactor-2", Type: db.TypeUser}
	require.NoError(t, h.DB.Create(&reactor1).Error)
	require.NoError(t, h.DB.Create(&reactor2).Error)

	reaction1, err := h.Svc.CreateReaction(ctx, &issue.ID, nil, reactor1.ID, "+1")
	require.NoError(t, err)
	reaction2, err := h.Svc.CreateReaction(ctx, &issue.ID, nil, reactor2.ID, "heart")
	require.NoError(t, err)

	w := h.DoREST(t, http.MethodGet, fmt.Sprintf("/api/v3/repos/%s/issues/%d/reactions", issue.Repository.FullName, issue.Number), nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	items := testharness.DecodeJSONArray(t, w)
	require.Len(t, items, 2)
	assertReactionItem(t, items[0], reaction1.ID, "+1", reactor1.Login)
	assertReactionItem(t, items[1], reaction2.ID, "heart", reactor2.Login)

	w = h.DoREST(t, http.MethodGet, fmt.Sprintf("/api/v3/repos/%s/issues/%d/reactions?content=heart", issue.Repository.FullName, issue.Number), nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	items = testharness.DecodeJSONArray(t, w)
	require.Len(t, items, 1)
	assertReactionItem(t, items[0], reaction2.ID, "heart", reactor2.Login)

	emptyIssue, err := h.Svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: issue.Repository.FullName,
		Title:        "No reactions",
		Body:         "body",
		AuthorLogin:  h.User.Login,
	})
	require.NoError(t, err)

	w = h.DoREST(t, http.MethodGet, fmt.Sprintf("/api/v3/repos/%s/issues/%d/reactions", emptyIssue.Repository.FullName, emptyIssue.Number), nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Empty(t, testharness.DecodeJSONArray(t, w))
}

func TestListIssueCommentReactions(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "comment-reactions",
		AutoInit:   true,
	})
	require.NoError(t, err)

	issue, err := h.Svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: h.User.Login + "/comment-reactions",
		Title:        "Comment reaction target",
		Body:         "body",
		AuthorLogin:  h.User.Login,
	})
	require.NoError(t, err)

	comment, err := h.Svc.CreateIssueComment(ctx, issue.Repository.FullName, issue.Number, "hello", h.User.Login, nil)
	require.NoError(t, err)
	emptyComment, err := h.Svc.CreateIssueComment(ctx, issue.Repository.FullName, issue.Number, "no reactions yet", h.User.Login, nil)
	require.NoError(t, err)

	reactor1 := db.User{Login: "comment-reactor-1", Name: "comment-reactor-1", Type: db.TypeUser}
	reactor2 := db.User{Login: "comment-reactor-2", Name: "comment-reactor-2", Type: db.TypeUser}
	require.NoError(t, h.DB.Create(&reactor1).Error)
	require.NoError(t, h.DB.Create(&reactor2).Error)

	reaction1, err := h.Svc.CreateReaction(ctx, nil, &comment.ID, reactor1.ID, "laugh")
	require.NoError(t, err)
	reaction2, err := h.Svc.CreateReaction(ctx, nil, &comment.ID, reactor2.ID, "rocket")
	require.NoError(t, err)

	w := h.DoREST(t, http.MethodGet, fmt.Sprintf("/api/v3/repos/%s/issues/comments/%d/reactions", issue.Repository.FullName, comment.ID), nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	items := testharness.DecodeJSONArray(t, w)
	require.Len(t, items, 2)
	assertReactionItem(t, items[0], reaction1.ID, "laugh", reactor1.Login)
	assertReactionItem(t, items[1], reaction2.ID, "rocket", reactor2.Login)

	w = h.DoREST(t, http.MethodGet, fmt.Sprintf("/api/v3/repos/%s/issues/comments/%d/reactions?content=rocket", issue.Repository.FullName, comment.ID), nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	items = testharness.DecodeJSONArray(t, w)
	require.Len(t, items, 1)
	assertReactionItem(t, items[0], reaction2.ID, "rocket", reactor2.Login)

	w = h.DoREST(t, http.MethodGet, fmt.Sprintf("/api/v3/repos/%s/issues/comments/%d/reactions", issue.Repository.FullName, emptyComment.ID), nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Empty(t, testharness.DecodeJSONArray(t, w))
}

func assertReactionItem(t *testing.T, item map[string]any, reactionID uint, content, login string) {
	t.Helper()

	require.Equal(t, float64(reactionID), item["id"])
	require.Equal(t, transform.NodeID("Reaction", reactionID), item["node_id"])
	require.Equal(t, content, item["content"])
	require.NotEmpty(t, item["created_at"])

	user, ok := item["user"].(map[string]any)
	require.True(t, ok, "user should be an object")
	require.Equal(t, login, user["login"])
}
