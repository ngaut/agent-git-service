package service

import (
	"context"

	"github.com/ngaut/agent-git-service/internal/db"
)

// CreateCommitStatus creates a new commit status.
func (s *Service) CreateCommitStatus(ctx context.Context, status *db.CommitStatus) error {
	if err := s.DBForCtx(ctx).Create(status).Error; err != nil {
		return err
	}
	s.ReevaluateAutoMergeForSHA(ctx, status.RepositoryID, status.CommitSHA)
	return nil
}

// ListCommitStatuses lists commit statuses for a given repository and SHA.
func (s *Service) ListCommitStatuses(ctx context.Context, repoID uint, sha string) ([]db.CommitStatus, error) {
	var statuses []db.CommitStatus
	err := s.DBForCtx(ctx).
		Where("repository_id = ? AND commit_sha = ?", repoID, sha).
		Order("created_at DESC").
		Preload("Creator").
		Find(&statuses).Error
	return statuses, err
}
