package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const wikiReferenceRecoveryInterval = time.Minute

type pendingWikiReferenceRepo struct {
	RepositoryID     uint
	FullName         string
	ThroughChangeset uint64
}

// StartWikiReferenceEffectsRecoveryWorker resumes reference projections that
// were committed to the catalog but interrupted before their in-memory job
// finished. Normal writes still execute the effect synchronously; this worker
// only closes the process-crash window.
func (s *Service) StartWikiReferenceEffectsRecoveryWorker() {
	if s == nil || s.DB == nil {
		return
	}
	s.wikiReferenceRecoveryWorkerOnce.Do(func() {
		s.Wg.Add(1)
		go func() {
			defer s.Wg.Done()
			ctx := s.ServerCtx()
			s.recoverPendingWikiReferenceEffectsAndLog(ctx)
			ticker := time.NewTicker(wikiReferenceRecoveryInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.recoverPendingWikiReferenceEffectsAndLog(ctx)
				}
			}
		}()
	})
}

func (s *Service) recoverPendingWikiReferenceEffectsAndLog(ctx context.Context) {
	recovered, err := s.recoverPendingWikiReferenceEffects(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.WarnContext(ctx, "wiki reference effects recovery failed", "error", err)
		}
		return
	}
	if recovered != 0 {
		slog.InfoContext(ctx, "wiki reference effects recovered", "repositories", recovered)
	}
}

func (s *Service) recoverPendingWikiReferenceEffects(ctx context.Context) (int, error) {
	var repos []pendingWikiReferenceRepo
	if err := s.DBForCtx(ctx).
		Table("wiki_repo_heads AS heads").
		Select("heads.repository_id, repositories.full_name, heads.reference_effects_through_changeset_id AS through_changeset").
		Joins("JOIN repositories ON repositories.id = heads.repository_id").
		Where("heads.reference_effects_through_changeset_id IS NOT NULL").
		Order("heads.repository_id ASC").
		Scan(&repos).Error; err != nil {
		return 0, err
	}

	recovered := 0
	var recoveryErrs []error
	for _, repo := range repos {
		repo := repo
		done, err := s.enqueueWikiReferenceJob(wikiReferenceJob{
			ctx:  s.detachWikiBackgroundContext(ctx),
			repo: repo.FullName,
			run: func(jobCtx context.Context) error {
				return s.rebuildWikiReferences(jobCtx, repo)
			},
		})
		if err != nil {
			recoveryErrs = append(recoveryErrs, fmt.Errorf("recover wiki references for %s: %w", repo.FullName, err))
			continue
		}
		if err := <-done; err != nil {
			recoveryErrs = append(recoveryErrs, fmt.Errorf("recover wiki references for %s: %w", repo.FullName, err))
			continue
		}
		recovered++
	}
	return recovered, errors.Join(recoveryErrs...)
}

func (s *Service) rebuildWikiReferences(ctx context.Context, pending pendingWikiReferenceRepo) error {
	return s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := ContextWithDB(ctx, tx)
		var head db.WikiRepoHead
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("repository_id", "reference_effects_through_changeset_id").
			First(&head, "repository_id = ?", pending.RepositoryID).Error; err != nil {
			return fmt.Errorf("claim wiki reference recovery cursor: %w", err)
		}
		if head.ReferenceEffectsThroughChangesetID == nil || *head.ReferenceEffectsThroughChangesetID != pending.ThroughChangeset {
			return nil
		}

		var pages []db.WikiPage
		if err := tx.
			Where("repository_id = ? AND deleted_at IS NULL", pending.RepositoryID).
			Order("page_id").
			Find(&pages).Error; err != nil {
			return fmt.Errorf("load current wiki pages: %w", err)
		}

		deleteResult := tx.
			Where("source_type = ? AND source_repository_id = ?", issueReferenceSourceWikiPage, pending.RepositoryID).
			Delete(&db.IssueReference{})
		if deleteResult.Error != nil {
			if isMissingIssueReferencesTableErr(deleteResult.Error) {
				return s.markWikiReferenceEffectsComplete(txCtx, pending.RepositoryID, pending.ThroughChangeset)
			}
			return fmt.Errorf("delete existing wiki references: %w", deleteResult.Error)
		}

		repo := db.Repository{ID: pending.RepositoryID, FullName: pending.FullName}
		for _, page := range pages {
			body, err := s.wikiPageBody(txCtx, page)
			if err != nil {
				return fmt.Errorf("load wiki body for %s: %w", page.Slug, err)
			}
			if err := s.syncWikiPageReferences(txCtx, repo, page.Slug, string(body), page.UpdatedAt); err != nil {
				return fmt.Errorf("sync wiki references for %s: %w", page.Slug, err)
			}
		}
		return s.markWikiReferenceEffectsComplete(txCtx, pending.RepositoryID, pending.ThroughChangeset)
	})
}

func (s *Service) markWikiReferenceEffectsComplete(ctx context.Context, repoID uint, changesetID uint64) error {
	return s.DBForCtx(ctx).Model(&db.WikiRepoHead{}).
		Where("repository_id = ? AND reference_effects_through_changeset_id = ?", repoID, changesetID).
		UpdateColumn("reference_effects_through_changeset_id", nil).Error
}

func (s *Service) markWikiReferenceEffectsCompleteThrough(ctx context.Context, repoID uint, changesetID uint64) error {
	return s.DBForCtx(ctx).Model(&db.WikiRepoHead{}).
		Where("repository_id = ? AND reference_effects_through_changeset_id <= ?", repoID, changesetID).
		UpdateColumn("reference_effects_through_changeset_id", nil).Error
}
