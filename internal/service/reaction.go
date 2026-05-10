package service

import (
	"context"

	"gh-server/internal/db"
)

// CreateReaction creates or returns an existing reaction.
// The unique index on (issue_id, comment_id, user_id, content) ensures idempotency.
func (s *Service) CreateReaction(ctx context.Context, issueID *uint, commentID *uint, userID uint, content string) (db.Reaction, error) {
	r := db.Reaction{
		IssueID:   issueID,
		CommentID: commentID,
		UserID:    userID,
		Content:   content,
	}

	// Check for existing reaction (upsert semantics)
	q := s.DBForCtx(ctx).Where("user_id = ? AND content = ?", userID, content)
	if issueID != nil {
		q = q.Where("issue_id = ?", *issueID)
	} else {
		q = q.Where("issue_id IS NULL")
	}
	if commentID != nil {
		q = q.Where("comment_id = ?", *commentID)
	} else {
		q = q.Where("comment_id IS NULL")
	}

	var existing db.Reaction
	if err := q.First(&existing).Error; err == nil {
		// Already exists — return the existing one (idempotent)
		if err := s.DBForCtx(ctx).Preload("User").First(&existing, existing.ID).Error; err != nil {
			return existing, wrapErr(err)
		}
		return existing, nil
	}

	if err := s.DBForCtx(ctx).Create(&r).Error; err != nil {
		return db.Reaction{}, wrapErr(err)
	}
	if err := s.DBForCtx(ctx).Preload("User").First(&r, r.ID).Error; err != nil {
		return r, wrapErr(err)
	}
	return r, nil
}

// ListIssueReactions returns all reactions on an issue ordered by creation time.
func (s *Service) ListIssueReactions(ctx context.Context, issueID int64) ([]db.Reaction, error) {
	if issueID <= 0 {
		return nil, ErrNotFound
	}
	if _, err := s.GetIssueByID(ctx, uint(issueID)); err != nil {
		return nil, err
	}
	var reactions []db.Reaction
	if err := s.DBForCtx(ctx).
		Preload("User").
		Where("issue_id = ?", uint(issueID)).
		Order("created_at ASC").
		Limit(defaultListLimit).
		Find(&reactions).Error; err != nil {
		return nil, err
	}
	return reactions, nil
}

// ListCommentReactions returns all reactions on a comment ordered by creation time.
func (s *Service) ListCommentReactions(ctx context.Context, commentID int64) ([]db.Reaction, error) {
	if commentID <= 0 {
		return nil, ErrNotFound
	}
	if _, err := s.GetIssueCommentByID(ctx, uint(commentID)); err != nil {
		return nil, err
	}
	var reactions []db.Reaction
	if err := s.DBForCtx(ctx).
		Preload("User").
		Where("comment_id = ?", uint(commentID)).
		Order("created_at ASC").
		Limit(defaultListLimit).
		Find(&reactions).Error; err != nil {
		return nil, err
	}
	return reactions, nil
}

// CountReactions returns a map of content -> count for an issue or comment.
// Exactly one of issueID or commentID must be non-zero.
func (s *Service) CountReactions(ctx context.Context, issueID uint, commentID uint) (map[string]int64, error) {
	if (issueID == 0 && commentID == 0) || (issueID != 0 && commentID != 0) {
		return nil, ErrNotFound
	}
	q := s.DBForCtx(ctx).Model(&db.Reaction{})
	if issueID != 0 {
		q = q.Where("issue_id = ?", issueID).Where("comment_id IS NULL")
	} else {
		q = q.Where("comment_id = ?", commentID).Where("issue_id IS NULL")
	}
	type reactionCount struct {
		Content string
		Count   int64
	}
	var rows []reactionCount
	if err := q.Select("content, COUNT(*) as count").Group("content").Find(&rows).Error; err != nil {
		return nil, wrapErr(err)
	}
	counts := make(map[string]int64, len(rows))
	for _, row := range rows {
		counts[row.Content] = row.Count
	}
	return counts, nil
}

// CountReactionsBatch returns a map of issueID -> (content -> count) for multiple issues in a single query.
func (s *Service) CountReactionsBatch(ctx context.Context, issueIDs []uint) (map[uint]map[string]int64, error) {
	result := make(map[uint]map[string]int64, len(issueIDs))
	if len(issueIDs) == 0 {
		return result, nil
	}
	type batchRow struct {
		IssueID uint
		Content string
		Count   int64
	}
	var rows []batchRow
	if err := s.DBForCtx(ctx).Model(&db.Reaction{}).
		Select("issue_id, content, COUNT(*) as count").
		Where("issue_id IN ? AND comment_id IS NULL", issueIDs).
		Group("issue_id, content").
		Find(&rows).Error; err != nil {
		return nil, wrapErr(err)
	}
	for _, row := range rows {
		if result[row.IssueID] == nil {
			result[row.IssueID] = make(map[string]int64)
		}
		result[row.IssueID][row.Content] = row.Count
	}
	return result, nil
}

// CountReactionsBatchForComments returns reaction counts for multiple comment IDs in one query.
func (s *Service) CountReactionsBatchForComments(ctx context.Context, commentIDs []uint) (map[uint]map[string]int64, error) {
	result := make(map[uint]map[string]int64, len(commentIDs))
	if len(commentIDs) == 0 {
		return result, nil
	}
	type batchRow struct {
		CommentID uint
		Content   string
		Count     int64
	}
	var rows []batchRow
	if err := s.DBForCtx(ctx).Model(&db.Reaction{}).
		Select("comment_id, content, COUNT(*) as count").
		Where("comment_id IN ? AND issue_id IS NULL", commentIDs).
		Group("comment_id, content").
		Find(&rows).Error; err != nil {
		return nil, wrapErr(err)
	}
	for _, row := range rows {
		if result[row.CommentID] == nil {
			result[row.CommentID] = make(map[string]int64)
		}
		result[row.CommentID][row.Content] = row.Count
	}
	return result, nil
}

// GetReaction fetches a single reaction by ID.
func (s *Service) GetReaction(ctx context.Context, reactionID int64) (db.Reaction, error) {
	if reactionID <= 0 {
		return db.Reaction{}, ErrNotFound
	}
	var reaction db.Reaction
	if err := s.DBForCtx(ctx).Preload("User").First(&reaction, uint(reactionID)).Error; err != nil {
		return reaction, wrapErr(err)
	}
	if reaction.IssueID != nil {
		if _, err := s.GetIssueByID(ctx, *reaction.IssueID); err != nil {
			return db.Reaction{}, err
		}
		return reaction, nil
	}
	if reaction.CommentID != nil {
		if _, err := s.GetIssueCommentByID(ctx, *reaction.CommentID); err != nil {
			return db.Reaction{}, err
		}
		return reaction, nil
	}
	return db.Reaction{}, ErrNotFound
}

// DeleteReaction deletes a reaction by ID if it belongs to the given user.
func (s *Service) DeleteReaction(ctx context.Context, reactionID int64, userID uint) error {
	if reactionID <= 0 {
		return ErrNotFound
	}
	reaction, err := s.GetReaction(ctx, reactionID)
	if err != nil {
		return err
	}
	if reaction.UserID != userID {
		return ErrForbidden
	}
	return deleteByID[db.Reaction](s, ctx, reaction.ID)
}
