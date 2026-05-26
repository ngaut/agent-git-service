package service

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ngaut/agent-git-service/internal/db"
)

// IssueReadStateInput holds parameters for updating issue read state.
type IssueReadStateInput struct {
	IssueID           uint
	UserID            uint
	LastReadCommentID uint
}

// GetIssueReadState retrieves the read state for a specific issue and user.
// Returns db.IssueReadState and nil error if found, or empty struct and ErrNotFound if not found.
func (s *Service) GetIssueReadState(ctx context.Context, issueID, userID uint) (db.IssueReadState, error) {
	var state db.IssueReadState
	err := s.DBForCtx(ctx).
		Where("issue_id = ? AND user_id = ?", issueID, userID).
		First(&state).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return db.IssueReadState{}, ErrNotFound
		}
		return db.IssueReadState{}, err
	}
	return state, nil
}

// GetOrCreateIssueReadState retrieves the read state for an issue and user,
// creating a new record if it doesn't exist.
func (s *Service) GetOrCreateIssueReadState(ctx context.Context, issueID, userID uint) (db.IssueReadState, error) {
	state, err := s.GetIssueReadState(ctx, issueID, userID)
	if err == nil {
		return state, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return db.IssueReadState{}, err
	}

	// Create new read state
	state = db.IssueReadState{
		IssueID:           issueID,
		UserID:            userID,
		LastReadCommentID: 0,
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}
	if err := s.DBForCtx(ctx).Create(&state).Error; err != nil {
		return db.IssueReadState{}, err
	}
	return state, nil
}

// UpdateIssueReadState updates or creates the read state for an issue and user.
// Returns the updated read state.
func (s *Service) UpdateIssueReadState(ctx context.Context, in IssueReadStateInput) (db.IssueReadState, error) {
	now := time.Now()
	state := db.IssueReadState{
		IssueID:           in.IssueID,
		UserID:            in.UserID,
		LastReadCommentID: in.LastReadCommentID,
		UpdatedAt:         now,
		CreatedAt:         now,
	}

	// Use Clauses with OnConflict for proper upsert
	err := s.DBForCtx(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "issue_id"}, {Name: "user_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"last_read_comment_id", "updated_at"}),
		}).
		Create(&state).Error

	if err != nil {
		return db.IssueReadState{}, err
	}

	// Reload to get the actual values after upsert (in case it was an update)
	if err := s.DBForCtx(ctx).
		Where("issue_id = ? AND user_id = ?", in.IssueID, in.UserID).
		First(&state).Error; err != nil {
		return db.IssueReadState{}, err
	}

	return state, nil
}

// GetIssueParticipantsReadState retrieves read state for all participants of an issue.
// Participants include the issue author and all comment authors.
func (s *Service) GetIssueParticipantsReadState(ctx context.Context, issueID uint) ([]db.IssueReadState, error) {
	var states []db.IssueReadState
	err := s.DBForCtx(ctx).
		Where("issue_id = ?", issueID).
		Preload("User").
		Find(&states).Error
	return states, err
}

// MarkIssueAsRead marks an issue as read up to a specific comment for the current user.
// This is the main entry point for the POST /api/v3/issues/{id}/read endpoint.
func (s *Service) MarkIssueAsRead(ctx context.Context, issueID uint, lastReadCommentID uint) (db.IssueReadState, error) {
	viewer, ok := UserFromContext(ctx)
	if !ok {
		return db.IssueReadState{}, ErrUnauthorized
	}

	return s.UpdateIssueReadState(ctx, IssueReadStateInput{
		IssueID:           issueID,
		UserID:            viewer.ID,
		LastReadCommentID: lastReadCommentID,
	})
}

// GetCurrentUserIssueReadState gets the read state for the current user on an issue.
// This is the main entry point for the GET /api/v3/issues/{id}/read-state endpoint.
func (s *Service) GetCurrentUserIssueReadState(ctx context.Context, issueID uint) (db.IssueReadState, error) {
	viewer, ok := UserFromContext(ctx)
	if !ok {
		return db.IssueReadState{}, ErrUnauthorized
	}

	return s.GetOrCreateIssueReadState(ctx, issueID, viewer.ID)
}

// GetIssueUnreadCount returns the number of comments added after the user's last read position.
func (s *Service) GetIssueUnreadCount(ctx context.Context, issueID uint, userID uint) (int64, error) {
	// First get the issue to get its number
	var issue db.Issue
	if err := s.DBForCtx(ctx).First(&issue, issueID).Error; err != nil {
		return 0, err
	}

	state, err := s.GetIssueReadState(ctx, issueID, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// No read state means all comments are unread
			// Count all comments on the issue
			var count int64
			err := s.DBForCtx(ctx).
				Model(&db.IssueComment{}).
				Where("repository_id = ? AND issue_number = ?", issue.RepositoryID, issue.Number).
				Count(&count).Error
			return count, err
		}
		return 0, err
	}

	// Count comments with ID > last_read_comment_id on the same issue
	var count int64
	err = s.DBForCtx(ctx).
		Model(&db.IssueComment{}).
		Where("repository_id = ? AND issue_number = ? AND id > ?", issue.RepositoryID, issue.Number, state.LastReadCommentID).
		Count(&count).Error
	return count, err
}
