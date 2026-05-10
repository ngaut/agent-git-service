package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

func TestIssueReadState_Service(t *testing.T) {
	svc, teardown := setupTestService(t)
	defer teardown()

	ctx := context.Background()

	// Create test users
	user1 := db.User{Login: "reader1", Name: "Reader One", Type: db.TypeUser}
	user2 := db.User{Login: "reader2", Name: "Reader Two", Type: db.TypeUser}
	require.NoError(t, svc.DB.Create(&user1).Error)
	require.NoError(t, svc.DB.Create(&user2).Error)

	// Create test repo
	repo := db.Repository{
		FullName: "testowner/testrepo",
		Name:     "testrepo",
		OwnerID:  user1.ID,
		Owner:    user1,
	}
	require.NoError(t, svc.DB.Create(&repo).Error)

	// Create test issue
	issue := db.Issue{
		Number:       1,
		RepositoryID: repo.ID,
		Title:        "Test Issue",
		AuthorID:     user1.ID,
	}
	require.NoError(t, svc.DB.Create(&issue).Error)

	// Create test comments
	comment1 := db.IssueComment{
		RepositoryID: repo.ID,
		IssueNumber:  issue.Number,
		Body:         "Comment 1",
		AuthorID:     user1.ID,
	}
	comment2 := db.IssueComment{
		RepositoryID: repo.ID,
		IssueNumber:  issue.Number,
		Body:         "Comment 2",
		AuthorID:     user2.ID,
	}
	comment3 := db.IssueComment{
		RepositoryID: repo.ID,
		IssueNumber:  issue.Number,
		Body:         "Comment 3",
		AuthorID:     user1.ID,
	}
	require.NoError(t, svc.DB.Create(&comment1).Error)
	require.NoError(t, svc.DB.Create(&comment2).Error)
	require.NoError(t, svc.DB.Create(&comment3).Error)

	t.Run("GetIssueReadState_NotFound", func(t *testing.T) {
		_, err := svc.GetIssueReadState(ctx, issue.ID, user1.ID)
		assert.ErrorIs(t, err, service.ErrNotFound)
	})

	t.Run("GetOrCreateIssueReadState", func(t *testing.T) {
		state, err := svc.GetOrCreateIssueReadState(ctx, issue.ID, user1.ID)
		require.NoError(t, err)
		assert.Equal(t, issue.ID, state.IssueID)
		assert.Equal(t, user1.ID, state.UserID)
		assert.Equal(t, uint(0), state.LastReadCommentID)

		// Second call should return existing record
		state2, err := svc.GetOrCreateIssueReadState(ctx, issue.ID, user1.ID)
		require.NoError(t, err)
		assert.Equal(t, state.ID, state2.ID)
	})

	t.Run("UpdateIssueReadState", func(t *testing.T) {
		state, err := svc.UpdateIssueReadState(ctx, service.IssueReadStateInput{
			IssueID:           issue.ID,
			UserID:            user1.ID,
			LastReadCommentID: comment2.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, comment2.ID, state.LastReadCommentID)

		// Update again
		state, err = svc.UpdateIssueReadState(ctx, service.IssueReadStateInput{
			IssueID:           issue.ID,
			UserID:            user1.ID,
			LastReadCommentID: comment3.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, comment3.ID, state.LastReadCommentID)
	})

	t.Run("GetIssueUnreadCount", func(t *testing.T) {
		// Set read state to comment1
		_, err := svc.UpdateIssueReadState(ctx, service.IssueReadStateInput{
			IssueID:           issue.ID,
			UserID:            user1.ID,
			LastReadCommentID: comment1.ID,
		})
		require.NoError(t, err)

		// Should have 2 unread comments (comment2 and comment3)
		count, err := svc.GetIssueUnreadCount(ctx, issue.ID, user1.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(2), count)

		// Mark all as read
		_, err = svc.UpdateIssueReadState(ctx, service.IssueReadStateInput{
			IssueID:           issue.ID,
			UserID:            user1.ID,
			LastReadCommentID: comment3.ID,
		})
		require.NoError(t, err)

		// Should have 0 unread
		count, err = svc.GetIssueUnreadCount(ctx, issue.ID, user1.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(0), count)
	})

	t.Run("GetIssueParticipantsReadState", func(t *testing.T) {
		// Create read states for both users
		_, err := svc.UpdateIssueReadState(ctx, service.IssueReadStateInput{
			IssueID:           issue.ID,
			UserID:            user1.ID,
			LastReadCommentID: comment1.ID,
		})
		require.NoError(t, err)

		_, err = svc.UpdateIssueReadState(ctx, service.IssueReadStateInput{
			IssueID:           issue.ID,
			UserID:            user2.ID,
			LastReadCommentID: comment2.ID,
		})
		require.NoError(t, err)

		states, err := svc.GetIssueParticipantsReadState(ctx, issue.ID)
		require.NoError(t, err)
		assert.Len(t, states, 2)
	})

	t.Run("MarkIssueAsRead_Unauthorized", func(t *testing.T) {
		// No user in context
		_, err := svc.MarkIssueAsRead(ctx, issue.ID, comment1.ID)
		assert.ErrorIs(t, err, service.ErrUnauthorized)
	})

	t.Run("MarkIssueAsRead_Authorized", func(t *testing.T) {
		// Add user to context
		ctxWithUser := service.ContextWithUser(ctx, user1)
		state, err := svc.MarkIssueAsRead(ctxWithUser, issue.ID, comment3.ID)
		require.NoError(t, err)
		assert.Equal(t, comment3.ID, state.LastReadCommentID)
		assert.Equal(t, user1.ID, state.UserID)
	})

	t.Run("GetCurrentUserIssueReadState", func(t *testing.T) {
		// No user in context
		_, err := svc.GetCurrentUserIssueReadState(ctx, issue.ID)
		assert.ErrorIs(t, err, service.ErrUnauthorized)

		// Add user to context
		ctxWithUser := service.ContextWithUser(ctx, user2)
		state, err := svc.GetCurrentUserIssueReadState(ctxWithUser, issue.ID)
		require.NoError(t, err)
		assert.Equal(t, user2.ID, state.UserID)
		assert.Equal(t, issue.ID, state.IssueID)
	})
}
