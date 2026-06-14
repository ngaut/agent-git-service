package service

import (
	"context"
	"fmt"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"

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

// Issue and PR numbers share one repository-scoped namespace, so allocation
// must use a single row instead of independent MAX(number) queries.
func nextIssueOrPRNumberTx(tx *gorm.DB, repoID uint) (int, error) {
	if err := ensureIssuePRNumberCounterTx(tx, repoID); err != nil {
		return 0, fmt.Errorf("nextIssueOrPRNumber: ensure counter: %w", err)
	}

	now := time.Now().UTC()
	switch tx.Dialector.Name() {
	case "mysql":
		res := tx.Exec(
			"UPDATE issue_pr_number_counters SET next_number = LAST_INSERT_ID(next_number + 1), updated_at = ? WHERE repository_id = ?",
			now, repoID,
		)
		if res.Error != nil {
			return 0, fmt.Errorf("nextIssueOrPRNumber: advance counter: %w", res.Error)
		}
		if res.RowsAffected != 1 {
			return 0, ErrNotFound
		}
		var nextAfter int
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&nextAfter).Error; err != nil {
			return 0, fmt.Errorf("nextIssueOrPRNumber: read counter: %w", err)
		}
		return nextAfter - 1, nil
	default:
		var counter db.IssuePRNumberCounter
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&counter, "repository_id = ?", repoID).Error; err != nil {
			return 0, err
		}
		number := counter.NextNumber
		if err := tx.Model(&db.IssuePRNumberCounter{}).
			Where("repository_id = ?", repoID).
			Updates(map[string]any{
				"next_number": number + 1,
				"updated_at":  now,
			}).Error; err != nil {
			return 0, fmt.Errorf("nextIssueOrPRNumber: advance counter: %w", err)
		}
		return number, nil
	}
}

func ensureIssuePRNumberCounterTx(tx *gorm.DB, repoID uint) error {
	maxNum, err := maxIssueOrPRNumberTx(tx, repoID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	counter := db.IssuePRNumberCounter{
		RepositoryID: repoID,
		NextNumber:   maxNum + 1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&counter).Error; err != nil {
		return err
	}

	var persisted db.IssuePRNumberCounter
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&persisted, "repository_id = ?", repoID).Error; err != nil {
		return err
	}
	if persisted.NextNumber > maxNum {
		return nil
	}
	return tx.Model(&db.IssuePRNumberCounter{}).
		Where("repository_id = ?", repoID).
		Updates(map[string]any{
			"next_number": maxNum + 1,
			"updated_at":  now,
		}).Error
}

func maxIssueOrPRNumberTx(tx *gorm.DB, repoID uint) (int, error) {
	var maxNum int
	err := tx.Raw(
		"SELECT CASE WHEN "+
			"COALESCE((SELECT MAX(number) FROM issues WHERE repository_id = ?), 0) > "+
			"COALESCE((SELECT MAX(number) FROM pull_requests WHERE repository_id = ?), 0) "+
			"THEN COALESCE((SELECT MAX(number) FROM issues WHERE repository_id = ?), 0) "+
			"ELSE COALESCE((SELECT MAX(number) FROM pull_requests WHERE repository_id = ?), 0) "+
			"END",
		repoID, repoID, repoID, repoID,
	).Scan(&maxNum).Error
	if err != nil {
		return 0, fmt.Errorf("maxIssueOrPRNumber: %w", err)
	}
	return maxNum, nil
}

// lockRepoForNumbering locks the repo row so issue/PR number allocation and insert
// can run under a single serialized transaction path on backends that support FOR UPDATE.
func lockRepoForNumbering(tx *gorm.DB, repoID uint) error {
	var repo db.Repository
	q := tx.Select("id")
	q = q.Clauses(clause.Locking{Strength: "UPDATE"})
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
