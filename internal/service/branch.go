package service

import (
	"context"
	"errors"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
)

// GetBranchProtection returns the branch protection rules for a specific branch.
func (s *Service) GetBranchProtection(ctx context.Context, repoID uint, branch string) (*db.BranchProtection, error) {
	var bp db.BranchProtection
	err := s.DBForCtx(ctx).
		Where("repository_id = ? AND branch_name = ?", repoID, branch).
		First(&bp).Error
	if err != nil {
		return nil, wrapErr(err)
	}
	return &bp, nil
}

// UpdateBranchProtection creates or updates branch protection rules.
func (s *Service) UpdateBranchProtection(ctx context.Context, bp *db.BranchProtection) error {
	// Upsert based on unique index (repository_id, branch_name)
	var existing db.BranchProtection
	err := s.DBForCtx(ctx).
		Where("repository_id = ? AND branch_name = ?", bp.RepositoryID, bp.BranchName).
		First(&existing).Error

	if err == nil {
		// Update mutable fields only. Save() would overwrite created_at with zero value
		// when bp is a fresh struct from request payload.
		updates := db.BranchProtection{
			RequiredStatusChecksJSON: bp.RequiredStatusChecksJSON,
			EnforceAdmins:            bp.EnforceAdmins,
			RequiredSignatures:       bp.RequiredSignatures,
			RequiredPullRequestJSON:  bp.RequiredPullRequestJSON,
			RestrictionsJSON:         bp.RestrictionsJSON,
		}
		if err := s.DBForCtx(ctx).Model(&existing).
			Select("RequiredStatusChecksJSON", "EnforceAdmins", "RequiredSignatures", "RequiredPullRequestJSON", "RestrictionsJSON").
			Updates(updates).Error; err != nil {
			return err
		}
		bp.ID = existing.ID
		bp.CreatedAt = existing.CreatedAt
		bp.UpdatedAt = existing.UpdatedAt
		return nil
	}

	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	// Create
	return s.DBForCtx(ctx).Create(bp).Error
}

// DeleteBranchProtection removes the branch protection rules for a branch.
func (s *Service) DeleteBranchProtection(ctx context.Context, repoID uint, branch string) error {
	return s.DBForCtx(ctx).
		Where("repository_id = ? AND branch_name = ?", repoID, branch).
		Delete(&db.BranchProtection{}).Error
}
