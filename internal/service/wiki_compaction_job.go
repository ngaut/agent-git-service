package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	applog "github.com/ngaut/agent-git-service/internal/logging"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"

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
	return s.startWikiCompactionEnabled(ctx, repoFullName)
}

func (s *Service) startWikiCompactionEnabled(ctx context.Context, repoFullName string) (db.WikiCompactionJob, error) {
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
	if scopedDB, ok := DBFromContext(ctx); ok {
		bgCtx = ContextWithDB(bgCtx, scopedDB)
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
	return s.compactWikiHistoryEnabled(ctx, repoFullName)
}

func (s *Service) compactWikiHistoryEnabled(ctx context.Context, repoFullName string) (WikiCompactResult, error) {
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
	var (
		result WikiCompactResult
	)
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
		if previousChangeset.Source == string(wikicatalog.SourceCompact) && previousChangeset.SynthFormatVer < synthProjectionMaterialized {
			result = WikiCompactResult{
				PreviousHead:    previousChangeset.SynthCommitSHA,
				NewHead:         previousChangeset.SynthCommitSHA,
				CompactedBefore: previousChangeset.CommittedAt,
			}
			return s.resumePendingWikiCompactProjection(ctx, repoFullName, previousChangeset)
		}

		pageIDs := make([]uint64, 0, len(livePages))
		for _, page := range livePages {
			pageIDs = append(pageIDs, page.PageID)
		}
		var allPageIDs []uint64
		if err := s.DBForCtx(ctx).Model(&db.WikiPage{}).
			Unscoped().
			Where("repository_id = ?", rep.ID).
			Order("page_id ASC").
			Pluck("page_id", &allPageIDs).Error; err != nil {
			return err
		}
		var revisionCount int64
		if err := s.DBForCtx(ctx).Model(&db.WikiPageRevision{}).
			Where("page_id IN ?", allPageIDs).
			Count(&revisionCount).Error; err != nil {
			return err
		}
		if revisionCount == 0 {
			return ErrNotFound
		}

		type latestRevisionRow struct {
			PageID     uint64
			RevisionID uint64
		}
		var latestRevisionRows []latestRevisionRow
		if err := s.DBForCtx(ctx).Model(&db.WikiPageRevision{}).
			Select("page_id, MAX(revision_id) AS revision_id").
			Where("page_id IN ?", pageIDs).
			Group("page_id").
			Find(&latestRevisionRows).Error; err != nil {
			return err
		}
		if len(latestRevisionRows) == 0 {
			return ErrNotFound
		}

		nextRevisionByPage := make(map[uint64]uint64, len(latestRevisionRows))
		for _, rev := range latestRevisionRows {
			nextRevisionByPage[rev.PageID] = rev.RevisionID + 1
		}

		newProjectionSHA, err := s.createWikiCompactCommitObject(ctx, repoFullName, now, livePages)
		if err != nil {
			return err
		}
		compactedRef := wikiCompactProjectionRef(now)

		newChangeset := db.WikiChangeset{
			RepositoryID:   rep.ID,
			ParentID:       &repoHead.HeadChangesetID,
			Message:        db.LargeText(fmt.Sprintf("Compact wiki history at %s", now.Format(time.RFC3339))),
			AuthorID:       s.resolveWikiAuthor(ctx),
			CommittedAt:    now,
			PageCount:      len(livePages),
			Source:         string(wikicatalog.SourceCompact),
			SynthCommitSHA: newProjectionSHA,
		}

		result = WikiCompactResult{
			PreviousHead:    previousChangeset.SynthCommitSHA,
			NewHead:         newProjectionSHA,
			CompactedBefore: now,
			Pages:           len(livePages),
			CommitsRemoved:  int(revisionCount) - len(livePages),
		}

		if err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&newChangeset).Error; err != nil {
				return err
			}
			headUpdate := tx.Model(&db.WikiRepoHead{}).
				Where("repository_id = ? AND head_changeset_id = ?", rep.ID, repoHead.HeadChangesetID).
				Updates(map[string]any{
					"head_changeset_id": newChangeset.ChangesetID,
					"updated_at":        now,
				})
			if headUpdate.Error != nil {
				return headUpdate.Error
			}
			if headUpdate.RowsAffected != 1 {
				return ErrConflict
			}

			newRevisions := make([]db.WikiPageRevision, 0, len(livePages))
			for _, page := range livePages {
				nextRevisionID, ok := nextRevisionByPage[page.PageID]
				if !ok {
					nextRevisionID = page.HeadRevisionID + 1
				}
				newRevisions = append(newRevisions, db.WikiPageRevision{
					PageID:      page.PageID,
					RevisionID:  nextRevisionID,
					ChangesetID: newChangeset.ChangesetID,
					BlobSHA:     page.HeadBlobSHA,
					BodySize:    page.BodySize,
					BodyInline:  page.BodyInline,
					SlugAtRev:   page.Slug,
					CommitSHA:   newChangeset.SynthCommitSHA,
					Op:          "compact",
					AuthorID:    newChangeset.AuthorID,
					CommittedAt: now,
				})
			}
			if err := tx.CreateInBatches(newRevisions, 200).Error; err != nil {
				return err
			}
			if err := updateWikiPagesForCompaction(tx, newRevisions, newChangeset.ChangesetID, newChangeset.AuthorID, now); err != nil {
				return err
			}
			if err := tx.Model(&db.WikiPageRevision{}).
				Where("page_id IN ? AND changeset_id <= ? AND superseded_by_changeset_id IS NULL", allPageIDs, repoHead.HeadChangesetID).
				Update("superseded_by_changeset_id", newChangeset.ChangesetID).Error; err != nil {
				return err
			}
			if err := tx.Model(&db.WikiChangeset{}).
				Where("repository_id = ? AND changeset_id <= ? AND superseded_by_changeset_id IS NULL", rep.ID, repoHead.HeadChangesetID).
				Update("superseded_by_changeset_id", newChangeset.ChangesetID).Error; err != nil {
				return err
			}
			return nil
		}); err != nil {
			return err
		}
		if err := s.updateWikiCompactRefLocked(ctx, repoFullName, compactedRef, newProjectionSHA); err != nil {
			return err
		}
		return s.DBForCtx(ctx).Model(&db.WikiChangeset{}).
			Where("changeset_id = ?", newChangeset.ChangesetID).
			Update("synth_format_ver", synthProjectionMaterialized).Error
	})
	if err != nil {
		return WikiCompactResult{}, err
	}
	s.invalidateWikiBacklinks(repoFullName)
	return result, nil
}

func (s *Service) resumePendingWikiCompactProjection(ctx context.Context, repoFullName string, changeset db.WikiChangeset) error {
	if strings.TrimSpace(changeset.SynthCommitSHA) == "" {
		return fmt.Errorf("compact changeset %d is missing synth commit sha", changeset.ChangesetID)
	}
	if err := s.updateWikiCompactRefLocked(ctx, repoFullName, wikiCompactProjectionRef(changeset.CommittedAt), changeset.SynthCommitSHA); err != nil {
		return err
	}
	return s.DBForCtx(ctx).Model(&db.WikiChangeset{}).
		Where("changeset_id = ?", changeset.ChangesetID).
		Update("synth_format_ver", synthProjectionMaterialized).Error
}

func updateWikiPagesForCompaction(tx *gorm.DB, revisions []db.WikiPageRevision, changesetID uint64, authorID *uint, now time.Time) error {
	if len(revisions) == 0 {
		return nil
	}

	args := make([]any, 0, len(revisions)*3+4)
	caseSQL := strings.Builder{}
	caseSQL.WriteString("CASE page_id")
	pageIDs := make([]any, 0, len(revisions))
	for _, rev := range revisions {
		caseSQL.WriteString(" WHEN ? THEN ?")
		args = append(args, rev.PageID, rev.RevisionID)
		pageIDs = append(pageIDs, rev.PageID)
	}
	caseSQL.WriteString(" END")
	args = append(args, changesetID, authorID, now)
	args = append(args, pageIDs...)

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(pageIDs)), ",")
	sql := fmt.Sprintf(
		"UPDATE wiki_pages SET head_revision_id = %s, head_changeset_id = ?, last_author_id = ?, updated_at = ? WHERE page_id IN (%s)",
		caseSQL.String(),
		placeholders,
	)
	return tx.Exec(sql, args...).Error
}
