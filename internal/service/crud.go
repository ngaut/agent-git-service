package service

import (
	"context"
	"fmt"
	"gh-server/internal/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// checkAffected returns ErrNotFound when a GORM result affected zero rows.
// Used by delete and update helpers to map "no rows" to a 404.
func checkAffected(res *gorm.DB) error {
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// getByID fetches a single record by primary key, wrapping GORM's
// ErrRecordNotFound as ErrNotFound for consistent HTTP 404 mapping.
func getByID[T any](s *Service, ctx context.Context, id uint) (T, error) {
	var v T
	err := s.DBForCtx(ctx).First(&v, id).Error
	return v, wrapErr(err)
}

// deleteByID deletes a single record by primary key.
// Returns ErrNotFound if no rows were affected.
func deleteByID[T any](s *Service, ctx context.Context, id uint) error {
	return checkAffected(s.DBForCtx(ctx).Delete(new(T), id))
}

// nextIssueOrPRNumber returns the next sequential number for an issue or pull request within a repo.
// GitHub enforces a unified sequence namespace for issues and PRs.
func nextIssueOrPRNumber(s *Service, ctx context.Context, repoID uint) (int, error) {
	return nextIssueOrPRNumberTx(s.DBForCtx(ctx), repoID)
}

// nextIssueOrPRNumberTx is the tx-aware variant of nextIssueOrPRNumber.
// Combines both MAX queries into a single round-trip using cross-database compatible SQL.
func nextIssueOrPRNumberTx(tx *gorm.DB, repoID uint) (int, error) {
	var maxNum int
	err := tx.Raw(
		"SELECT CASE WHEN "+
			"COALESCE((SELECT MAX(number) FROM issues WHERE repository_id = ?), 0) > "+
			"COALESCE((SELECT MAX(number) FROM pull_requests WHERE repository_id = ?), 0) "+
			"THEN COALESCE((SELECT MAX(number) FROM issues WHERE repository_id = ?), 0) "+
			"ELSE COALESCE((SELECT MAX(number) FROM pull_requests WHERE repository_id = ?), 0) "+
			"END + 1",
		repoID, repoID, repoID, repoID,
	).Scan(&maxNum).Error
	if err != nil {
		return 0, fmt.Errorf("nextIssueOrPRNumber: %w", err)
	}
	return maxNum, nil
}

// lockRepoForNumbering locks the repo row so issue/PR number allocation and insert
// can run under a single serialized transaction path on backends that support FOR UPDATE.
func lockRepoForNumbering(tx *gorm.DB, repoID uint) error {
	var repo db.Repository
	q := tx.Select("id")
	if tx.Dialector.Name() != "sqlite" {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return q.First(&repo, repoID).Error
}

// nextNumber returns COALESCE(MAX(number), 0) + 1 for a given model within a repo.
// Used for numbers scoped by repository that are not issues/PRs (e.g., Milestones).
func nextNumber[T any](s *Service, ctx context.Context, repoID uint) (int, error) {
	var maxNum int
	if err := s.DBForCtx(ctx).Model(new(T)).
		Where("repository_id = ?", repoID).
		Select("COALESCE(MAX(number), 0)").Scan(&maxNum).Error; err != nil {
		return 0, fmt.Errorf("nextNumber: %w", err)
	}
	return maxNum + 1, nil
}
