package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gh-server/internal/db"
	applog "gh-server/internal/logging"
	"gh-server/internal/wikicatalog"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	WikiCompactionJobQueued    = "queued"
	WikiCompactionJobRunning   = "running"
	WikiCompactionJobSucceeded = "succeeded"
	WikiCompactionJobFailed    = "failed"
)

const (
	wikiCompactionJobHeartbeatInterval = 30 * time.Second
	wikiCompactionJobStaleAfter        = 5 * time.Minute
)

func (s *Service) StartWikiCompaction(ctx context.Context, repoFullName string) (db.WikiCompactionJob, error) {
	if s.WikiCatalog == nil {
		return db.WikiCompactionJob{}, errors.New("wiki catalog unavailable")
	}
	if err := s.ensureWikiCatalogCurrent(ctx, repoFullName); err != nil {
		return db.WikiCompactionJob{}, err
	}
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return db.WikiCompactionJob{}, err
	}

	var job db.WikiCompactionJob
	var created bool
	err = s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		var repoHead db.WikiRepoHead
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("repository_id = ?", rep.ID).
			Take(&repoHead).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}

		if err := tx.Where("repository_id = ? AND status IN ?", rep.ID, []string{WikiCompactionJobQueued, WikiCompactionJobRunning}).
			Order("created_at DESC").
			Take(&job).Error; err == nil {
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		job = db.WikiCompactionJob{
			ID:           uuid.NewString(),
			RepositoryID: rep.ID,
			Status:       WikiCompactionJobQueued,
		}
		if user, ok := UserFromContext(ctx); ok && user.ID != 0 {
			job.RequestedByID = &user.ID
		}
		if err := tx.Create(&job).Error; err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return db.WikiCompactionJob{}, err
	}

	if created || job.Status == WikiCompactionJobQueued || isWikiCompactionJobStale(job, time.Now().UTC()) {
		s.kickWikiCompactionJob(ctx, rep, job)
	}
	return job, nil
}

func (s *Service) GetWikiCompactionJob(ctx context.Context, repoFullName, jobID string) (db.WikiCompactionJob, error) {
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return db.WikiCompactionJob{}, err
	}
	var job db.WikiCompactionJob
	if err := s.DBForCtx(ctx).Where("repository_id = ? AND id = ?", rep.ID, jobID).Take(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return db.WikiCompactionJob{}, ErrNotFound
		}
		return db.WikiCompactionJob{}, err
	}
	return job, nil
}

func (s *Service) kickWikiCompactionJob(ctx context.Context, repo db.Repository, job db.WikiCompactionJob) {
	key := s.wikiRepoKey(ctx, repo)
	if !s.claimWikiBackgroundCompaction(key, job.ID) {
		return
	}

	bgCtx := applog.CloneContext(s.ServerCtx(), ctx)
	if tenantDB, ok := DBFromContext(ctx); ok {
		bgCtx = ContextWithDB(bgCtx, tenantDB)
	}
	if user, ok := UserFromContext(ctx); ok {
		bgCtx = ContextWithUser(bgCtx, user)
	}

	s.Wg.Add(1)
	go func() {
		defer s.Wg.Done()
		defer s.releaseWikiBackgroundCompaction(key, job.ID)

		now := time.Now().UTC()
		if err := s.DBForCtx(bgCtx).Model(&db.WikiCompactionJob{}).
			Where("id = ?", job.ID).
			Updates(map[string]any{
				"status":     WikiCompactionJobRunning,
				"started_at": now,
				"updated_at": now,
			}).Error; err != nil {
			slog.ErrorContext(bgCtx, "wiki compaction job failed to start", "repo", repo.FullName, "job_id", job.ID, "error", err)
			return
		}

		if s.testWikiCompactionJobStarted != nil {
			s.testWikiCompactionJobStarted(job.ID)
		}
		if s.testWikiCompactionJobContinue != nil {
			s.testWikiCompactionJobContinue(job.ID)
		}

		stopHeartbeat := make(chan struct{})
		defer close(stopHeartbeat)
		go s.heartbeatWikiCompactionJob(bgCtx, job.ID, stopHeartbeat)

		result, err := s.CompactWikiHistory(bgCtx, repo.FullName)
		finishedAt := time.Now().UTC()
		updates := map[string]any{
			"finished_at": finishedAt,
			"updated_at":  finishedAt,
		}
		if err != nil {
			updates["status"] = WikiCompactionJobFailed
			updates["error_message"] = err.Error()
			if updateErr := s.DBForCtx(bgCtx).Model(&db.WikiCompactionJob{}).Where("id = ?", job.ID).Updates(updates).Error; updateErr != nil {
				slog.ErrorContext(bgCtx, "wiki compaction job failed to persist failure", "repo", repo.FullName, "job_id", job.ID, "error", updateErr, "cause", err)
				return
			}
			slog.ErrorContext(bgCtx, "wiki compaction job failed", "repo", repo.FullName, "job_id", job.ID, "error", err)
			return
		}

		compactedBefore := result.CompactedBefore
		updates["status"] = WikiCompactionJobSucceeded
		updates["previous_head"] = result.PreviousHead
		updates["new_head"] = result.NewHead
		updates["compacted_before"] = &compactedBefore
		updates["pages"] = result.Pages
		updates["commits_removed"] = result.CommitsRemoved
		updates["error_message"] = ""
		if updateErr := s.DBForCtx(bgCtx).Model(&db.WikiCompactionJob{}).Where("id = ?", job.ID).Updates(updates).Error; updateErr != nil {
			slog.ErrorContext(bgCtx, "wiki compaction job failed to persist success", "repo", repo.FullName, "job_id", job.ID, "error", updateErr)
		}
	}()
}

func (s *Service) heartbeatWikiCompactionJob(ctx context.Context, jobID string, stop <-chan struct{}) {
	ticker := time.NewTicker(wikiCompactionJobHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now().UTC()
			if err := s.DBForCtx(ctx).Model(&db.WikiCompactionJob{}).
				Where("id = ? AND status = ?", jobID, WikiCompactionJobRunning).
				Updates(map[string]any{
					"updated_at": now,
				}).Error; err != nil {
				slog.WarnContext(ctx, "wiki compaction job heartbeat update failed", "job_id", jobID, "error", err)
			}
		}
	}
}

func isWikiCompactionJobStale(job db.WikiCompactionJob, now time.Time) bool {
	if job.Status != WikiCompactionJobRunning {
		return false
	}
	if !job.UpdatedAt.IsZero() && now.Sub(job.UpdatedAt) > wikiCompactionJobStaleAfter {
		return true
	}
	if job.StartedAt != nil && now.Sub(*job.StartedAt) > wikiCompactionJobStaleAfter {
		return true
	}
	return false
}

func (s *Service) CompactWikiHistory(ctx context.Context, repoFullName string) (WikiCompactResult, error) {
	if s.WikiCatalog == nil {
		return WikiCompactResult{}, errors.New("wiki catalog unavailable")
	}
	if err := s.ensureWikiCatalogCurrent(ctx, repoFullName); err != nil {
		return WikiCompactResult{}, err
	}
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return WikiCompactResult{}, err
	}
	return s.compactWikiHistoryForRepo(ctx, rep, repoFullName)
}

func (s *Service) compactWikiHistoryForRepo(ctx context.Context, rep db.Repository, repoFullName string) (WikiCompactResult, error) {
	var result WikiCompactResult
	err := s.withWikiCatalogWriteLock(ctx, repoFullName, func() error {
		now := time.Now().UTC()

		var livePages []db.WikiPage
		if err := s.DBForCtx(ctx).
			Where("repository_id = ? AND deleted_at IS NULL", rep.ID).
			Order("page_id ASC").
			Find(&livePages).Error; err != nil {
			return err
		}
		if len(livePages) == 0 {
			return ErrNotFound
		}

		var repoHead db.WikiRepoHead
		if err := s.DBForCtx(ctx).Where("repository_id = ?", rep.ID).Take(&repoHead).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}

		var previousChangeset db.WikiChangeset
		if err := s.DBForCtx(ctx).Where("changeset_id = ?", repoHead.HeadChangesetID).Take(&previousChangeset).Error; err != nil {
			return err
		}

		var revisionRows []db.WikiPageRevision
		pageIDs := make([]uint64, 0, len(livePages))
		for _, page := range livePages {
			pageIDs = append(pageIDs, page.PageID)
		}
		if err := s.DBForCtx(ctx).
			Where("page_id IN ?", pageIDs).
			Order("page_id ASC, revision_id DESC").
			Find(&revisionRows).Error; err != nil {
			return err
		}
		if len(revisionRows) == 0 {
			return ErrNotFound
		}

		nextRevisionByPage := make(map[uint64]uint64, len(livePages))
		for _, rev := range revisionRows {
			if _, ok := nextRevisionByPage[rev.PageID]; !ok {
				nextRevisionByPage[rev.PageID] = rev.RevisionID + 1
			}
		}

		newCommitSHA, err := s.createWikiCompactCommitObject(ctx, repoFullName, now)
		if err != nil {
			return err
		}

		newChangeset := db.WikiChangeset{
			RepositoryID:   rep.ID,
			Message:        db.LargeText(fmt.Sprintf("Compact wiki history at %s", now.Format(time.RFC3339))),
			AuthorID:       s.resolveWikiAuthor(ctx),
			CommittedAt:    now,
			PageCount:      len(livePages),
			Source:         string(wikicatalog.SourceAdmin),
			SynthCommitSHA: newCommitSHA,
			SynthFormatVer: synthProjectionMaterialized,
		}

		return s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&newChangeset).Error; err != nil {
				return err
			}
			if err := tx.Model(&db.WikiRepoHead{}).
				Where("repository_id = ? AND head_changeset_id = ?", rep.ID, repoHead.HeadChangesetID).
				Updates(map[string]any{
					"head_changeset_id": newChangeset.ChangesetID,
					"updated_at":        now,
				}).Error; err != nil {
				return err
			}

			if err := tx.Where("page_id IN ?", pageIDs).Delete(&db.WikiPageRevision{}).Error; err != nil {
				return err
			}

			for _, page := range livePages {
				nextRevisionID, ok := nextRevisionByPage[page.PageID]
				if !ok {
					nextRevisionID = page.HeadRevisionID + 1
				}
				newRevision := db.WikiPageRevision{
					PageID:      page.PageID,
					RevisionID:  nextRevisionID,
					ChangesetID: newChangeset.ChangesetID,
					BlobSHA:     page.HeadBlobSHA,
					BodySize:    page.BodySize,
					BodyInline:  page.BodyInline,
					SlugAtRev:   page.Slug,
					CommitSHA:   newCommitSHA,
					Op:          "update",
					AuthorID:    newChangeset.AuthorID,
					CommittedAt: now,
				}
				if err := tx.Create(&newRevision).Error; err != nil {
					return err
				}
				if err := tx.Model(&db.WikiPage{}).
					Where("page_id = ?", page.PageID).
					Updates(map[string]any{
						"head_revision_id":  newRevision.RevisionID,
						"head_changeset_id": newChangeset.ChangesetID,
						"last_author_id":    newChangeset.AuthorID,
						"updated_at":        now,
					}).Error; err != nil {
					return err
				}
			}
			if err := tx.Where("repository_id = ? AND changeset_id <> ?", rep.ID, newChangeset.ChangesetID).Delete(&db.WikiChangeset{}).Error; err != nil {
				return err
			}

			result = WikiCompactResult{
				PreviousHead:    previousChangeset.SynthCommitSHA,
				NewHead:         newCommitSHA,
				CompactedBefore: now,
				Pages:           len(livePages),
				CommitsRemoved:  len(revisionRows) - len(livePages),
			}
			return nil
		})
	})
	if err != nil {
		return WikiCompactResult{}, err
	}
	if err := s.updateWikiCompactRef(ctx, repoFullName, result.NewHead); err != nil {
		if repairErr := s.repairWikiCatalogFromGit(ctx, repoFullName); repairErr != nil {
			return WikiCompactResult{}, fmt.Errorf("compact wiki history: update git ref: %w (catalog repair failed: %v)", err, repairErr)
		}
		return WikiCompactResult{}, err
	}
	s.invalidateWikiBacklinks(repoFullName)
	return result, nil
}
