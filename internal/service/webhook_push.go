package service

import (
	"context"
	"time"

	"gh-server/internal/db"
)

// UpdateRepositoryPushedAt records the most recent successful push time.
func (s *Service) UpdateRepositoryPushedAt(ctx context.Context, repoID uint, pushedAt time.Time) error {
	return s.DBForCtx(ctx).
		Model(&db.Repository{}).
		Where("id = ?", repoID).
		Update("pushed_at", &pushedAt).Error
}

// ListOpenPRsByHead returns open pull requests whose head branch matches a pushed ref.
func (s *Service) ListOpenPRsByHead(ctx context.Context, headRepoID uint, headRef string) ([]db.PullRequest, error) {
	var prs []db.PullRequest
	err := preloadPRFull(s.DBForCtx(ctx)).
		Where("head_repository_id = ? AND head_ref = ? AND state = ? AND merged = ?", headRepoID, headRef, db.StateOpen, false).
		Find(&prs).Error
	return prs, err
}

// SyncPRHeadAfterPush refreshes PR head metadata after a git push to its head branch.
func (s *Service) SyncPRHeadAfterPush(ctx context.Context, prID uint, headRepoFullName string) (db.PullRequest, error) {
	pr, err := s.GetPRByID(ctx, prID)
	if err != nil {
		return db.PullRequest{}, err
	}
	sha, err := s.Git.HeadSHA(ctx, headRepoFullName, pr.HeadRef)
	if err != nil {
		return pr, nil
	}
	if err := s.DBForCtx(ctx).Model(&db.PullRequest{}).Where("id = ?", pr.ID).Update("head_sha", sha).Error; err != nil {
		return pr, err
	}
	pr.HeadSHA = sha
	if err := s.updatePRCommitDataFromLoaded(ctx, pr.Repository.FullName, headRepoFullName, pr); err != nil {
		return pr, err
	}
	return s.GetPRByID(ctx, prID)
}
