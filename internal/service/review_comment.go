// Package service — pull request reviews and line comments
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
)

// ListPRReviewComments returns line-level comments for a pull request.
func (s *Service) ListPRReviewComments(ctx context.Context, prID uint) ([]db.PRReviewComment, error) {
	var comments []db.PRReviewComment
	if err := s.DBForCtx(ctx).Where("pull_request_id = ?", prID).Order("created_at ASC").Find(&comments).Error; err != nil {
		return nil, err
	}
	return comments, nil
}

// CreatePRReviewComment creates a single line-level comment on a pull request.
func (s *Service) CreatePRReviewComment(ctx context.Context, prID uint, c *db.PRReviewComment) error {
	c.PullRequestID = prID
	if c.SubjectType == "" {
		c.SubjectType = "line"
	}

	if c.DiffHunk == "" && c.CommitID != "" && c.Path != "" {
		var pr db.PullRequest
		if err := s.DBForCtx(ctx).Preload("Repository").First(&pr, prID).Error; err == nil {
			line := c.Line
			if line == 0 {
				line = c.OriginalLine
			}
			if line > 0 {
				hunk, _ := s.Git.GetDiffHunk(ctx, pr.Repository.FullName, pr.BaseSHA, c.CommitID, c.Path, line)
				c.DiffHunk = hunk
			}
		}
	}

	if err := s.DBForCtx(ctx).Create(c).Error; err != nil {
		return err
	}

	pr, err := s.GetPRByID(ctx, prID)
	if err != nil {
		return err
	}
	actor, actorErr := s.lookupUserByLoginCI(ctx, c.AuthorLogin)
	if actorErr != nil && !errors.Is(actorErr, ErrNotFound) {
		return actorErr
	}
	return s.createMentionNotificationsForBody(
		ctx,
		actor.ID,
		NotificationSubjectPullRequest,
		pr.ID,
		pr.RepositoryID,
		prReviewCommentURL(s.BaseURL, pr.Repository.FullName, c.ID),
		string(c.Body),
	)
}

// ReplyToPRReviewComment creates a reply to an existing review comment.
// It copies the path, commit_id, line, side and diff_hunk from the parent comment.
func (s *Service) ReplyToPRReviewComment(ctx context.Context, prID uint, parentID uint, body, authorLogin string) (db.PRReviewComment, error) {
	var parent db.PRReviewComment
	if err := s.DBForCtx(ctx).First(&parent, parentID).Error; err != nil {
		return db.PRReviewComment{}, wrapErrf(err, "parent comment #%d", parentID)
	}
	reply := db.PRReviewComment{
		PullRequestID:       prID,
		PullRequestReviewID: parent.PullRequestReviewID,
		InReplyToID:         &parentID,
		AuthorLogin:         authorLogin,
		Body:                db.LargeText(body),
		CommitID:            parent.CommitID,
		Path:                parent.Path,
		Line:                parent.Line,
		OriginalLine:        parent.OriginalLine,
		Side:                parent.Side,
		SubjectType:         parent.SubjectType,
		DiffHunk:            parent.DiffHunk,
	}
	if err := s.DBForCtx(ctx).Create(&reply).Error; err != nil {
		return db.PRReviewComment{}, err
	}
	pr, err := s.GetPRByID(ctx, prID)
	if err != nil {
		return db.PRReviewComment{}, err
	}
	actor, actorErr := s.lookupUserByLoginCI(ctx, authorLogin)
	if actorErr != nil && !errors.Is(actorErr, ErrNotFound) {
		return db.PRReviewComment{}, actorErr
	}
	parentAuthor, parentErr := s.lookupUserByLoginCI(ctx, parent.AuthorLogin)
	if parentErr == nil {
		if _, err := s.CreateReplyNotification(ctx, parentAuthor.ID, actor.ID, NotificationSubjectPullRequest, pr.ID, pr.RepositoryID, prReviewCommentURL(s.BaseURL, pr.Repository.FullName, reply.ID)); err != nil {
			return db.PRReviewComment{}, err
		}
	} else if !errors.Is(parentErr, ErrNotFound) {
		return db.PRReviewComment{}, parentErr
	}
	if err := s.createMentionNotificationsForBody(
		ctx,
		actor.ID,
		NotificationSubjectPullRequest,
		pr.ID,
		pr.RepositoryID,
		prReviewCommentURL(s.BaseURL, pr.Repository.FullName, reply.ID),
		string(reply.Body),
	); err != nil {
		return db.PRReviewComment{}, err
	}
	return reply, nil
}

// UpdatePRReviewComment updates the body of a review comment.
func (s *Service) UpdatePRReviewComment(ctx context.Context, commentID uint, body string) (db.PRReviewComment, error) {
	var c db.PRReviewComment
	if err := s.DBForCtx(ctx).First(&c, commentID).Error; err != nil {
		return c, wrapErrf(err, "review comment #%d", commentID)
	}
	c.Body = db.LargeText(body)
	if err := s.DBForCtx(ctx).Save(&c).Error; err != nil {
		return c, err
	}
	return c, nil
}

// ResolvePRReviewThread marks a PR review comment (thread root) as resolved.
func (s *Service) ResolvePRReviewThread(ctx context.Context, commentID uint) error {
	return s.DBForCtx(ctx).Model(&db.PRReviewComment{}).Where("id = ?", commentID).Update("is_resolved", true).Error
}

// UnresolvePRReviewThread marks a PR review comment (thread root) as unresolved.
func (s *Service) UnresolvePRReviewThread(ctx context.Context, commentID uint) error {
	return s.DBForCtx(ctx).Model(&db.PRReviewComment{}).Where("id = ?", commentID).Update("is_resolved", false).Error
}

// DeletePRReviewComment deletes a review comment by ID.
func (s *Service) DeletePRReviewComment(ctx context.Context, commentID uint) error {
	return s.DBForCtx(ctx).Delete(&db.PRReviewComment{}, commentID).Error
}

// GetPRReviewComment returns a single PR review comment by ID.
func (s *Service) GetPRReviewComment(ctx context.Context, commentID uint) (db.PRReviewComment, error) {
	var c db.PRReviewComment
	if err := s.DBForCtx(ctx).First(&c, commentID).Error; err != nil {
		return c, wrapErrf(err, "review comment #%d", commentID)
	}
	return c, nil
}

// GetPRReview returns a single PR review by ID.
func (s *Service) GetPRReview(ctx context.Context, reviewID uint) (db.PullRequestReview, error) {
	var r db.PullRequestReview
	if err := s.DBForCtx(ctx).First(&r, reviewID).Error; err != nil {
		return r, wrapErrf(err, "review #%d", reviewID)
	}
	return r, nil
}

// ListReviewCommentsForReview returns line-level comments for a specific review.
func (s *Service) ListReviewCommentsForReview(ctx context.Context, reviewID uint) ([]db.PRReviewComment, error) {
	var comments []db.PRReviewComment
	if err := s.DBForCtx(ctx).Where("pull_request_review_id = ?", reviewID).Order("created_at ASC").Find(&comments).Error; err != nil {
		return nil, err
	}
	return comments, nil
}

// DismissPRReview dismisses a submitted review.
func (s *Service) DismissPRReview(ctx context.Context, reviewID uint, message string) (db.PullRequestReview, error) {
	var r db.PullRequestReview
	if err := s.DBForCtx(ctx).First(&r, reviewID).Error; err != nil {
		return r, wrapErrf(err, "review #%d", reviewID)
	}
	if r.State != "APPROVED" && r.State != "CHANGES_REQUESTED" {
		return r, fmt.Errorf("review #%d is in state %q and cannot be dismissed", reviewID, r.State)
	}
	r.State = "DISMISSED"
	if message != "" {
		r.Body = db.LargeText(message)
	}
	if err := s.DBForCtx(ctx).Save(&r).Error; err != nil {
		return r, err
	}
	return r, nil
}

// DeletePRReview deletes a pending review.
func (s *Service) DeletePRReview(ctx context.Context, reviewID uint) error {
	var r db.PullRequestReview
	if err := s.DBForCtx(ctx).First(&r, reviewID).Error; err != nil {
		return wrapErrf(err, "review #%d", reviewID)
	}
	if r.State != "PENDING" {
		return fmt.Errorf("review #%d is in state %q; only PENDING reviews can be deleted", reviewID, r.State)
	}
	// Delete associated comments first
	if err := s.DBForCtx(ctx).Where("pull_request_review_id = ?", reviewID).Delete(&db.PRReviewComment{}).Error; err != nil {
		return err
	}
	return s.DBForCtx(ctx).Delete(&r).Error
}

// SubmitPRReview transitions a pending PR review to a submitted state (e.g. APPROVED, CHANGES_REQUESTED).
func (s *Service) SubmitPRReview(ctx context.Context, reviewID uint, event string, body string) (db.PullRequestReview, error) {
	var r db.PullRequestReview
	err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&r, reviewID).Error; err != nil {
			return err
		}
		updates := map[string]any{"state": normalizeReviewEvent(event), "submitted_at": time.Now()}
		if body != "" {
			updates["body"] = body
		}
		if err := tx.Model(&r).Updates(updates).Error; err != nil {
			return err
		}
		return tx.First(&r, reviewID).Error
	})
	return r, err
}

// MarkPRReadyForReview removes the draft status from a pull request.
func (s *Service) MarkPRReadyForReview(ctx context.Context, prID uint) error {
	draft := false
	return s.UpdatePRByID(ctx, prID, nil, &draft)
}

// CountIssueComments returns the number of comments on an issue.
func (s *Service) CountIssueComments(ctx context.Context, repoID uint, issueNumber int) int64 {
	var count int64
	s.DBForCtx(ctx).Model(&db.IssueComment{}).
		Where("repository_id = ? AND issue_number = ?", repoID, issueNumber).
		Count(&count)
	return count
}

// CountIssueCommentsBatch returns a map of issue_number -> comment count for multiple issues in a single query.
func (s *Service) CountIssueCommentsBatch(ctx context.Context, repoID uint, issueNumbers []int) (map[int]int64, error) {
	result := make(map[int]int64)
	if len(issueNumbers) == 0 {
		return result, nil
	}

	type CommentCount struct {
		IssueNumber int
		Count       int64
	}
	var counts []CommentCount

	err := s.DBForCtx(ctx).Model(&db.IssueComment{}).
		Select("issue_number, COUNT(*) as count").
		Where("repository_id = ? AND issue_number IN ?", repoID, issueNumbers).
		Group("issue_number").
		Find(&counts).Error
	if err != nil {
		return nil, err
	}

	for _, c := range counts {
		result[c.IssueNumber] = c.Count
	}
	return result, nil
}

// CountPRComments returns the number of issue-style comments on a PR.
func (s *Service) CountPRComments(ctx context.Context, repoID uint, prNumber int) int64 {
	var count int64
	s.DBForCtx(ctx).Model(&db.IssueComment{}).
		Where("repository_id = ? AND issue_number = ?", repoID, prNumber).
		Count(&count)
	return count
}

// CountPRCommentsBatch returns comment counts for multiple PRs in one query.
func (s *Service) CountPRCommentsBatch(ctx context.Context, repoID uint, prNumbers []int) map[int]int64 {
	result := make(map[int]int64, len(prNumbers))
	if len(prNumbers) == 0 {
		return result
	}
	type row struct {
		IssueNumber int
		Count       int64
	}
	var rows []row
	s.DBForCtx(ctx).Model(&db.IssueComment{}).
		Select("issue_number, COUNT(*) as count").
		Where("repository_id = ? AND issue_number IN ?", repoID, prNumbers).
		Group("issue_number").
		Find(&rows)
	for _, r := range rows {
		result[r.IssueNumber] = r.Count
	}
	return result
}

// CountPRReviewComments returns the number of review (line-level) comments on a PR.
func (s *Service) CountPRReviewComments(ctx context.Context, prID uint) int64 {
	var count int64
	s.DBForCtx(ctx).Model(&db.PRReviewComment{}).
		Where("pull_request_id = ?", prID).
		Count(&count)
	return count
}

// CountPRReviewCommentsBatch returns review comment counts for multiple PRs in one query.
func (s *Service) CountPRReviewCommentsBatch(ctx context.Context, prIDs []uint) map[uint]int64 {
	result := make(map[uint]int64, len(prIDs))
	if len(prIDs) == 0 {
		return result
	}
	type row struct {
		PullRequestID uint
		Count         int64
	}
	var rows []row
	s.DBForCtx(ctx).Model(&db.PRReviewComment{}).
		Select("pull_request_id, COUNT(*) as count").
		Where("pull_request_id IN ?", prIDs).
		Group("pull_request_id").
		Find(&rows)
	for _, r := range rows {
		result[r.PullRequestID] = r.Count
	}
	return result
}
