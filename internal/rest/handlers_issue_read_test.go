package rest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

func TestIssueReadHandlers(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	// Create test repo
	repo := db.Repository{
		FullName: "testowner/testrepo",
		Name:     "testrepo",
		OwnerID:  h.User.ID,
		Owner:    h.User,
	}
	require.NoError(t, h.DB.Create(&repo).Error)

	// Create test issue
	issue := db.Issue{
		Number:       1,
		RepositoryID: repo.ID,
		Title:        "Test Issue",
		AuthorID:     h.User.ID,
	}
	require.NoError(t, h.DB.Create(&issue).Error)

	// Create test comments
	comment1 := db.IssueComment{
		RepositoryID: repo.ID,
		IssueNumber:  issue.Number,
		Body:         "Comment 1",
		AuthorID:     h.User.ID,
	}
	comment2 := db.IssueComment{
		RepositoryID: repo.ID,
		IssueNumber:  issue.Number,
		Body:         "Comment 2",
		AuthorID:     h.User.ID,
	}
	require.NoError(t, h.DB.Create(&comment1).Error)
	require.NoError(t, h.DB.Create(&comment2).Error)

	t.Run("MarkIssueRead_Unauthenticated", func(t *testing.T) {
		// Note: testharness automatically adds auth, so we test via service layer instead
		// Service layer test verifies ErrUnauthorized is returned when no user in context
		t.Skip("testharness adds auth automatically; tested in service layer")
	})

	t.Run("MarkIssueRead_Authenticated", func(t *testing.T) {
		body := map[string]any{
			"last_read_comment_id": comment1.ID,
		}
		w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testowner/testrepo/issues/1/read", body)
		assert.Equal(t, http.StatusOK, w.Code)

		resp := decodeIssueReadStateResponseFromRecorder(t, w)
		assert.Equal(t, issue.ID, resp.IssueID)
		assert.Equal(t, h.User.ID, resp.UserID)
		assert.Equal(t, comment1.ID, resp.LastReadCommentID)
	})

	t.Run("MarkIssueRead_NotFound", func(t *testing.T) {
		w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testowner/testrepo/issues/999/read", map[string]any{})
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("GetIssueReadState_Unauthenticated", func(t *testing.T) {
		// Note: testharness automatically adds auth, so we test via service layer instead
		t.Skip("testharness adds auth automatically; tested in service layer")
	})

	t.Run("GetIssueReadState_Authenticated", func(t *testing.T) {
		w := h.DoRESTJSON(t, "GET", "/api/v3/repos/testowner/testrepo/issues/1/read-state", nil)
		assert.Equal(t, http.StatusOK, w.Code)

		resp := decodeIssueReadStateResponseFromRecorder(t, w)
		assert.Equal(t, issue.ID, resp.IssueID)
		assert.Equal(t, h.User.ID, resp.UserID)
	})

	t.Run("GetIssueReadState_NotFound", func(t *testing.T) {
		w := h.DoRESTJSON(t, "GET", "/api/v3/repos/testowner/testrepo/issues/999/read-state", nil)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("GetIssueParticipantsReadState", func(t *testing.T) {
		// Create read state for the user
		_, err := h.Svc.UpdateIssueReadState(ctx, service.IssueReadStateInput{
			IssueID:           issue.ID,
			UserID:            h.User.ID,
			LastReadCommentID: comment1.ID,
		})
		require.NoError(t, err)

		w := h.DoRESTJSON(t, "GET", "/api/v3/repos/testowner/testrepo/issues/1/participants/read-state", nil)
		assert.Equal(t, http.StatusOK, w.Code)

		var resp []participantReadStateResponse
		json.NewDecoder(w.Body).Decode(&resp)
		require.Len(t, resp, 1)
		assert.Equal(t, h.User.ID, resp[0].UserID)
		assert.Equal(t, h.User.Login, resp[0].UserLogin)
		assert.Equal(t, comment1.ID, resp[0].LastReadCommentID)
	})

	t.Run("GetIssueUnreadCount_Unauthenticated", func(t *testing.T) {
		// Note: testharness automatically adds auth, so we test via service layer instead
		t.Skip("testharness adds auth automatically; tested in service layer")
	})

	t.Run("GetIssueUnreadCount_Authenticated", func(t *testing.T) {
		// Set read state to comment1
		_, err := h.Svc.UpdateIssueReadState(ctx, service.IssueReadStateInput{
			IssueID:           issue.ID,
			UserID:            h.User.ID,
			LastReadCommentID: comment1.ID,
		})
		require.NoError(t, err)

		w := h.DoRESTJSON(t, "GET", "/api/v3/repos/testowner/testrepo/issues/1/unread-count", nil)
		assert.Equal(t, http.StatusOK, w.Code)

		var resp map[string]any
		json.NewDecoder(w.Body).Decode(&resp)
		assert.Equal(t, float64(1), resp["unread_count"]) // comment2 is unread
		assert.Equal(t, float64(issue.ID), resp["issue_id"])
	})
}

// Helper types for decoding responses
type issueReadStateResponse struct {
	IssueID           uint   `json:"issue_id"`
	UserID            uint   `json:"user_id"`
	LastReadCommentID uint   `json:"last_read_comment_id"`
	UpdatedAt         string `json:"updated_at"`
}

type participantReadStateResponse struct {
	UserID            uint   `json:"user_id"`
	UserLogin         string `json:"user_login"`
	LastReadCommentID uint   `json:"last_read_comment_id"`
	UpdatedAt         string `json:"updated_at"`
}

func decodeIssueReadStateResponse(t *testing.T, w *http.Response) issueReadStateResponse {
	t.Helper()
	var resp issueReadStateResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	return resp
}

func decodeIssueReadStateResponseFromRecorder(t *testing.T, w *httptest.ResponseRecorder) issueReadStateResponse {
	t.Helper()
	var resp issueReadStateResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	return resp
}
