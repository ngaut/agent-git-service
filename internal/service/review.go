package service

import (
	"context"
	"strings"

	"gh-server/internal/db"
)

// normalizeReviewEvent maps GitHub REST API review event values to database state values.
// REST API uses: APPROVE, REQUEST_CHANGES, COMMENT, DISMISS
// Database stores: APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED
func normalizeReviewEvent(event string) string {
	switch strings.ToUpper(event) {
	case "APPROVE":
		return db.ReviewApproved
	case "REQUEST_CHANGES":
		return db.ReviewChangesRequested
	case "COMMENT":
		return db.ReviewCommented
	case "DISMISS":
		return db.ReviewDismissed
	default:
		// Already in database format or unknown - return as-is
		return strings.ToUpper(event)
	}
}

// RequestReview adds a review request for the given login on a PR.
func (s *Service) RequestReview(ctx context.Context, prID uint, login string) error {
	if _, err := s.GetPRByID(ctx, prID); err != nil {
		return err
	}
	// Upsert: ignore if already requested (check login)
	var existing db.ReviewRequest
	result := s.DBForCtx(ctx).Where("pull_request_id = ? AND login = ?", prID, login).First(&existing)
	if result.Error == nil {
		return nil // already requested
	}
	return s.DBForCtx(ctx).Create(&db.ReviewRequest{
		PullRequestID: prID,
		Login:         login,
	}).Error
}

// RequestTeamReview adds a review request for the given team slug on a PR.
func (s *Service) RequestTeamReview(ctx context.Context, prID uint, teamSlug string) error {
	if _, err := s.GetPRByID(ctx, prID); err != nil {
		return err
	}
	var existing db.ReviewRequest
	result := s.DBForCtx(ctx).Where("pull_request_id = ? AND team_slug = ?", prID, teamSlug).First(&existing)
	if result.Error == nil {
		return nil // already requested
	}
	return s.DBForCtx(ctx).Create(&db.ReviewRequest{
		PullRequestID: prID,
		TeamSlug:      teamSlug,
	}).Error
}

// RemoveReviewRequest removes a review request for the given login on a PR.
func (s *Service) RemoveReviewRequest(ctx context.Context, prID uint, login string) error {
	if _, err := s.GetPRByID(ctx, prID); err != nil {
		return err
	}
	return s.DBForCtx(ctx).
		Where("pull_request_id = ? AND login = ?", prID, login).
		Delete(&db.ReviewRequest{}).Error
}

// RemoveTeamReviewRequest removes a review request for the given team slug on a PR.
func (s *Service) RemoveTeamReviewRequest(ctx context.Context, prID uint, teamSlug string) error {
	if _, err := s.GetPRByID(ctx, prID); err != nil {
		return err
	}
	return s.DBForCtx(ctx).
		Where("pull_request_id = ? AND team_slug = ?", prID, teamSlug).
		Delete(&db.ReviewRequest{}).Error
}

// ListReviewRequests returns all pending review requests for a PR.
func (s *Service) ListReviewRequests(ctx context.Context, prID uint) ([]db.ReviewRequest, error) {
	if _, err := s.GetPRByID(ctx, prID); err != nil {
		return nil, err
	}
	return s.ListReviewRequestsByPRID(ctx, prID)
}

// ListReviewRequestsByPRID queries review requests without re-validating
// PR existence. Use when PR ID is already known to be valid (e.g., from
// a prior GetPR call in the same handler).
func (s *Service) ListReviewRequestsByPRID(ctx context.Context, prID uint) ([]db.ReviewRequest, error) {
	var reqs []db.ReviewRequest
	err := s.DBForCtx(ctx).Where("pull_request_id = ?", prID).Find(&reqs).Error
	return reqs, err
}

// ListReviewRequestsBatch returns review requests for multiple PRs in one query.
func (s *Service) ListReviewRequestsBatch(ctx context.Context, prIDs []uint) (map[uint][]db.ReviewRequest, error) {
	result := make(map[uint][]db.ReviewRequest, len(prIDs))
	if len(prIDs) == 0 {
		return result, nil
	}
	var reqs []db.ReviewRequest
	if err := s.DBForCtx(ctx).Where("pull_request_id IN ?", prIDs).Find(&reqs).Error; err != nil {
		return nil, err
	}
	for _, r := range reqs {
		result[r.PullRequestID] = append(result[r.PullRequestID], r)
	}
	return result, nil
}

// AddPRReview stores a submitted PR review in the DB.
func (s *Service) AddPRReview(ctx context.Context, prID uint, authorLogin, state, body, commitSHA string) (db.PullRequestReview, error) {
	if _, err := s.GetPRByID(ctx, prID); err != nil {
		return db.PullRequestReview{}, err
	}
	review := db.PullRequestReview{
		PullRequestID: prID,
		AuthorLogin:   authorLogin,
		State:         normalizeReviewEvent(state),
		Body:          db.LargeText(body),
		CommitSHA:     commitSHA,
	}
	if err := s.DBForCtx(ctx).Create(&review).Error; err != nil {
		return db.PullRequestReview{}, err
	}
	return review, nil
}

// UpdatePRReview updates the body of a PR review.
func (s *Service) UpdatePRReview(ctx context.Context, reviewID uint, body string) (db.PullRequestReview, error) {
	var r db.PullRequestReview
	if err := s.DBForCtx(ctx).First(&r, reviewID).Error; err != nil {
		return r, wrapErrf(err, "review #%d", reviewID)
	}
	r.Body = db.LargeText(body)
	if err := s.DBForCtx(ctx).Save(&r).Error; err != nil {
		return r, err
	}
	return r, nil
}

// ListPRReviews returns all reviews for a PR ordered by creation time.
func (s *Service) ListPRReviews(ctx context.Context, prID uint) ([]db.PullRequestReview, error) {
	var reviews []db.PullRequestReview
	err := s.DBForCtx(ctx).Where("pull_request_id = ?", prID).Order("id asc").Find(&reviews).Error
	return reviews, err
}
