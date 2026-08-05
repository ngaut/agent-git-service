package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/embedding"
)

const (
	wikiSearchProjectionKindLexical   = "lexical"
	wikiSearchProjectionKindEmbedding = "embedding"

	wikiSearchProjectionLease            = 2 * time.Minute
	wikiSearchProjectionEmbeddingTimeout = time.Minute
	wikiSearchProjectionPoll             = 5 * time.Second
	wikiSearchProjectionClaimTry         = 3

	wikiSearchProjectionLexicalWorkers   = 4
	wikiSearchProjectionEmbeddingWorkers = 8
)

// StartWikiSearchProjectionWorker resumes durable projection tasks at process
// startup and periodically reclaims tasks whose owner disappeared mid-flight.
func (s *Service) StartWikiSearchProjectionWorker() {
	if s == nil || s.DB == nil || s.Ctx == nil {
		return
	}
	s.wikiSearchProjectionWorkerOnce.Do(func() {
		s.Wg.Add(1)
		go func() {
			defer s.Wg.Done()
			ctx := s.ServerCtx()
			if repaired, err := s.repairWikiSearchProjectionTasks(ctx); err != nil {
				slog.WarnContext(ctx, "wiki search projection repair failed", "error", err)
			} else if repaired > 0 {
				slog.InfoContext(ctx, "wiki search projection repair queued tasks", "rows", repaired)
			}
			s.pollWikiSearchProjectionDrains(ctx, wikiSearchProjectionKindLexical)
			s.pollWikiSearchProjectionDrains(ctx, wikiSearchProjectionKindEmbedding)

			ticker := time.NewTicker(wikiSearchProjectionPoll)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.pollWikiSearchProjectionDrains(ctx, wikiSearchProjectionKindLexical)
					s.pollWikiSearchProjectionDrains(ctx, wikiSearchProjectionKindEmbedding)
				}
			}
		}()
	})
}

func (s *Service) queueWikiSearchProjectionForRepo(ctx context.Context, repoID uint, slug string) {
	if repoID == 0 || slug == "" {
		return
	}
	if err := persistWikiSearchProjectionTasks(s.DBForCtx(ctx), repoID, []string{slug}); err != nil {
		slog.WarnContext(ctx, "wiki search projection task not persisted", "repo_id", repoID, "slug", slug, "error", err)
		return
	}
	s.kickWikiSearchProjection(ctx, 1)
}

func (s *Service) kickWikiSearchProjection(ctx context.Context, taskCount int) {
	if taskCount <= 0 {
		return
	}
	// Persisting one coalesced task replaces the old in-memory queue depth.
	recordWikiWriteValue(ctx, wikiWriteValueSearchQueueDepth, int64(taskCount))
	for range min(taskCount, wikiSearchProjectionLexicalWorkers) {
		s.startWikiSearchProjectionDrain(ctx, wikiSearchProjectionKindLexical)
	}
}

func persistWikiSearchProjectionTasks(database *gorm.DB, repoID uint, slugs []string) error {
	for _, slug := range uniqueStrings(slugs) {
		if err := upsertWikiSearchProjectionTask(
			database,
			repoID,
			slug,
			wikiSearchProjectionKindLexical,
			"",
			"",
		); err != nil {
			return err
		}
	}
	return nil
}

func upsertWikiSearchProjectionTask(database *gorm.DB, repoID uint, slug, kind, revisionSHA, labelDigest string) error {
	now := time.Now().UTC()
	task := db.WikiSearchProjectionTask{
		RepositoryID: repoID,
		Slug:         slug,
		Kind:         kind,
		Generation:   1,
		RevisionSHA:  revisionSHA,
		LabelDigest:  labelDigest,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	return database.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "repository_id"},
			{Name: "slug"},
			{Name: "kind"},
		},
		DoUpdates: clause.Assignments(map[string]any{
			"generation":   gorm.Expr("generation + 1"),
			"revision_sha": revisionSHA,
			"label_digest": labelDigest,
			"updated_at":   now,
		}),
	}).Create(&task).Error
}

func (s *Service) startWikiSearchProjectionDrain(ctx context.Context, kind string) {
	if s == nil || s.DBForCtx(ctx) == nil || ctx.Err() != nil {
		return
	}
	bgCtx := s.detachWikiBackgroundContext(ctx)
	baseKey := s.wikiSearchProjectionDrainKey(bgCtx, kind)

	s.wikiSearchProjectionMu.Lock()
	if s.wikiSearchProjectionDrains == nil {
		s.wikiSearchProjectionDrains = make(map[string]bool)
	}
	if s.wikiSearchProjectionWake == nil {
		s.wikiSearchProjectionWake = make(map[string]uint64)
	}
	for slot := 0; slot < wikiSearchProjectionWorkers(kind); slot++ {
		slotKey := fmt.Sprintf("%s:%d", baseKey, slot)
		if s.wikiSearchProjectionDrains[slotKey] {
			continue
		}
		s.wikiSearchProjectionDrains[slotKey] = true
		observedWake := s.wikiSearchProjectionWake[baseKey]
		s.Wg.Add(1)
		s.wikiSearchProjectionMu.Unlock()
		go s.runWikiSearchProjectionDrainSlot(bgCtx, kind, baseKey, slotKey, observedWake)
		return
	}
	// All workers are occupied. The wake generation prevents the final worker
	// from exiting after an enqueue that observed every slot as running.
	s.wikiSearchProjectionWake[baseKey]++
	s.wikiSearchProjectionMu.Unlock()
}

func wikiSearchProjectionWorkers(kind string) int {
	if kind == wikiSearchProjectionKindEmbedding {
		return wikiSearchProjectionEmbeddingWorkers
	}
	return wikiSearchProjectionLexicalWorkers
}

func (s *Service) pollWikiSearchProjectionDrains(ctx context.Context, kind string) {
	if ctx.Err() != nil {
		return
	}
	now := time.Now().UTC()
	var taskIDs []uint64
	if err := s.DBForCtx(ctx).
		Model(&db.WikiSearchProjectionTask{}).
		Where("kind = ? AND (lease_expires_at IS NULL OR lease_expires_at < ?)", kind, now).
		Order("updated_at ASC, id ASC").
		Limit(wikiSearchProjectionWorkers(kind)).
		Pluck("id", &taskIDs).Error; err != nil {
		slog.WarnContext(ctx, "wiki search projection poll failed", "kind", kind, "error", err)
		return
	}
	for range len(taskIDs) {
		s.startWikiSearchProjectionDrain(ctx, kind)
	}
}

func (s *Service) runWikiSearchProjectionDrainSlot(ctx context.Context, kind, baseKey, slotKey string, observedWake uint64) {
	defer s.Wg.Done()
	for {
		restart := s.runWikiSearchProjectionDrain(ctx, kind)

		s.wikiSearchProjectionMu.Lock()
		currentWake := s.wikiSearchProjectionWake[baseKey]
		if restart && ctx.Err() == nil && currentWake != observedWake {
			observedWake = currentWake
			s.wikiSearchProjectionMu.Unlock()
			continue
		}
		delete(s.wikiSearchProjectionDrains, slotKey)
		s.wikiSearchProjectionMu.Unlock()
		return
	}
}

func (s *Service) wikiSearchProjectionDrainKey(ctx context.Context, kind string) string {
	database := s.DBForCtx(ctx)
	sqlDB, err := database.DB()
	if err != nil {
		return fmt.Sprintf("%p:%s", database, kind)
	}
	return fmt.Sprintf("%p:%s", sqlDB, kind)
}

func (s *Service) runWikiSearchProjectionDrain(ctx context.Context, kind string) bool {
	for ctx.Err() == nil {
		task, err := s.claimWikiSearchProjectionTask(ctx, kind)
		if err != nil {
			slog.WarnContext(ctx, "wiki search projection claim failed", "kind", kind, "error", err)
			return false
		}
		if task == nil {
			return true
		}
		if err := s.processWikiSearchProjectionTask(ctx, *task); err != nil {
			_ = s.releaseWikiSearchProjectionTask(ctx, *task)
			slog.WarnContext(ctx, "wiki search projection failed",
				"kind", task.Kind,
				"repo_id", task.RepositoryID,
				"slug", task.Slug,
				"error", err,
			)
			return false
		}
	}
	return false
}

func (s *Service) claimWikiSearchProjectionTask(ctx context.Context, kind string) (*db.WikiSearchProjectionTask, error) {
	database := s.DBForCtx(ctx)
	for attempt := 0; attempt < wikiSearchProjectionClaimTry; attempt++ {
		now := time.Now().UTC()
		var task db.WikiSearchProjectionTask
		result := database.
			Where("kind = ? AND (lease_expires_at IS NULL OR lease_expires_at < ?)", kind, now).
			Order("updated_at ASC, id ASC").
			Limit(1).
			Find(&task)
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 0 {
			return nil, nil
		}

		token := uuid.NewString()
		expires := now.Add(wikiSearchProjectionLease)
		result = database.Model(&db.WikiSearchProjectionTask{}).
			Where("id = ? AND (lease_expires_at IS NULL OR lease_expires_at < ?)", task.ID, now).
			Updates(map[string]any{
				"lease_token":      token,
				"lease_expires_at": expires,
			})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 1 {
			task.LeaseToken = token
			task.LeaseExpiresAt = &expires
			return &task, nil
		}
	}
	return nil, nil
}

func (s *Service) processWikiSearchProjectionTask(ctx context.Context, task db.WikiSearchProjectionTask) error {
	switch task.Kind {
	case wikiSearchProjectionKindLexical:
		return s.processWikiSearchLexicalTask(ctx, task)
	case wikiSearchProjectionKindEmbedding:
		return s.processWikiSearchEmbeddingTask(ctx, task)
	default:
		return fmt.Errorf("unknown wiki search projection kind %q", task.Kind)
	}
}

func (s *Service) processWikiSearchLexicalTask(ctx context.Context, task db.WikiSearchProjectionTask) error {
	page, found, err := s.currentWikiSearchProjectionPage(ctx, task.RepositoryID, task.Slug)
	if err != nil {
		return err
	}
	if !found {
		return s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("repository_id = ? AND slug = ?", task.RepositoryID, task.Slug).
				Delete(&db.WikiSearchDocument{}).Error; err != nil {
				return err
			}
			if err := tx.Where(
				"repository_id = ? AND slug = ? AND kind = ?",
				task.RepositoryID,
				task.Slug,
				wikiSearchProjectionKindEmbedding,
			).Delete(&db.WikiSearchProjectionTask{}).Error; err != nil {
				return err
			}
			return completeWikiSearchProjectionTask(tx, task)
		})
	}

	queuedEmbedding := false
	labelDigest := wikiPageLabelsText(page.Labels)
	err = s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := ContextWithDB(ctx, tx)
		if err := s.upsertWikiSearchLexicalDocument(txCtx, task.RepositoryID, page); err != nil {
			return err
		}
		completed, err := deleteClaimedWikiSearchProjectionTask(tx, task)
		if err != nil {
			return err
		}
		if !completed {
			return releaseClaimedWikiSearchProjectionTask(tx, task)
		}
		if s.Embedder == nil || embedding.IsNop(s.Embedder) {
			return nil
		}
		if err := upsertWikiSearchProjectionTask(
			tx,
			task.RepositoryID,
			task.Slug,
			wikiSearchProjectionKindEmbedding,
			page.SHA,
			labelDigest,
		); err != nil {
			return err
		}
		queuedEmbedding = true
		return nil
	})
	if err == nil && queuedEmbedding {
		s.startWikiSearchProjectionDrain(ctx, wikiSearchProjectionKindEmbedding)
	}
	return err
}

func (s *Service) processWikiSearchEmbeddingTask(ctx context.Context, task db.WikiSearchProjectionTask) error {
	var doc db.WikiSearchDocument
	err := s.DBForCtx(ctx).
		Select("repository_id", "slug", "title", "body", "revision_sha", "label_digest").
		Where("repository_id = ? AND slug = ?", task.RepositoryID, task.Slug).
		Take(&doc).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return completeWikiSearchProjectionTask(s.DBForCtx(ctx), task)
	}
	if err != nil {
		return err
	}
	if doc.RevisionSHA != task.RevisionSHA || doc.LabelDigest != task.LabelDigest {
		return completeWikiSearchProjectionTask(s.DBForCtx(ctx), task)
	}
	if s.Embedder == nil || embedding.IsNop(s.Embedder) {
		return completeWikiSearchProjectionTask(s.DBForCtx(ctx), task)
	}

	page := WikiPage{
		Slug: doc.Slug,
		Body: string(doc.Body),
		SHA:  doc.RevisionSHA,
	}
	embedCtx, cancel := context.WithTimeout(ctx, wikiSearchProjectionEmbeddingTimeout)
	vec, err := s.embedWithRetry(embedCtx, doc.Title+"\n"+doc.LabelDigest+"\n"+string(doc.Body))
	cancel()
	if err != nil {
		slog.WarnContext(ctx, "wiki search embedding unavailable; lexical document retained",
			"repo_id", task.RepositoryID,
			"slug", task.Slug,
			"retryable", embedding.IsRetryableError(err),
			"error", err,
		)
		return completeWikiSearchProjectionTask(s.DBForCtx(ctx), task)
	}
	if len(vec) > 0 {
		targetDB := s.DBForCtx(ctx)
		s.ensureVectorInit(targetDB, len(vec))
		if !s.wikiSearchEmbeddingColumnAvailable(targetDB) {
			return completeWikiSearchProjectionTask(targetDB, task)
		}
		if err := targetDB.Model(&db.WikiSearchDocument{}).
			Where(
				"repository_id = ? AND slug = ? AND revision_sha = ? AND label_digest = ?",
				task.RepositoryID,
				page.Slug,
				page.SHA,
				task.LabelDigest,
			).
			UpdateColumn("embedding", embedding.FormatVector(vec)).Error; err != nil {
			return err
		}
	}
	return completeWikiSearchProjectionTask(s.DBForCtx(ctx), task)
}

func (s *Service) currentWikiSearchProjectionPage(ctx context.Context, repoID uint, slug string) (WikiPage, bool, error) {
	var row db.WikiPage
	err := s.DBForCtx(ctx).
		Where("repository_id = ? AND slug = ? AND deleted_at IS NULL", repoID, slug).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return WikiPage{}, false, nil
	}
	if err != nil {
		return WikiPage{}, false, err
	}
	body, err := s.wikiPageBody(ctx, row)
	if err != nil {
		return WikiPage{}, false, err
	}
	labels, err := s.wikiLabelsForSlugs(ctx, repoID, []string{slug})
	if err != nil {
		return WikiPage{}, false, err
	}
	return WikiPage{
		Slug:      row.Slug,
		Title:     titleFromSlug(row.Slug),
		Body:      string(body),
		UpdatedAt: row.UpdatedAt,
		SHA:       row.HeadBlobSHA,
		Labels:    labels[row.Slug],
	}, true, nil
}

func completeWikiSearchProjectionTask(database *gorm.DB, task db.WikiSearchProjectionTask) error {
	deleted, err := deleteClaimedWikiSearchProjectionTask(database, task)
	if err != nil || deleted {
		return err
	}
	return releaseClaimedWikiSearchProjectionTask(database, task)
}

func deleteClaimedWikiSearchProjectionTask(database *gorm.DB, task db.WikiSearchProjectionTask) (bool, error) {
	result := database.
		Where(
			"id = ? AND generation = ? AND lease_token = ?",
			task.ID,
			task.Generation,
			task.LeaseToken,
		).
		Delete(&db.WikiSearchProjectionTask{})
	return result.RowsAffected == 1, result.Error
}

func (s *Service) releaseWikiSearchProjectionTask(ctx context.Context, task db.WikiSearchProjectionTask) error {
	return releaseClaimedWikiSearchProjectionTask(s.DBForCtx(ctx), task)
}

func releaseClaimedWikiSearchProjectionTask(database *gorm.DB, task db.WikiSearchProjectionTask) error {
	return database.Model(&db.WikiSearchProjectionTask{}).
		Where("id = ? AND lease_token = ?", task.ID, task.LeaseToken).
		Updates(map[string]any{
			"lease_token":      "",
			"lease_expires_at": nil,
		}).Error
}

func (s *Service) repairWikiSearchProjectionTasks(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	live := s.DBForCtx(ctx).Exec(`
INSERT INTO wiki_search_projection_tasks
	(repository_id, slug, kind, generation, created_at, updated_at)
SELECT pages.repository_id, pages.slug, ?, 1, ?, ?
FROM wiki_pages AS pages
LEFT JOIN wiki_search_documents AS docs
	ON docs.repository_id = pages.repository_id AND docs.slug = pages.slug
WHERE pages.deleted_at IS NULL
	AND (docs.id IS NULL OR docs.revision_sha <> pages.head_blob_sha)
ON DUPLICATE KEY UPDATE
	generation = wiki_search_projection_tasks.generation + 1,
	updated_at = VALUES(updated_at)
`, wikiSearchProjectionKindLexical, now, now)
	if live.Error != nil {
		return 0, live.Error
	}

	stale := s.DBForCtx(ctx).Exec(`
INSERT INTO wiki_search_projection_tasks
	(repository_id, slug, kind, generation, created_at, updated_at)
SELECT docs.repository_id, docs.slug, ?, 1, ?, ?
FROM wiki_search_documents AS docs
LEFT JOIN wiki_pages AS pages
	ON pages.repository_id = docs.repository_id
	AND pages.slug = docs.slug
	AND pages.deleted_at IS NULL
WHERE pages.page_id IS NULL
ON DUPLICATE KEY UPDATE
	generation = wiki_search_projection_tasks.generation + 1,
	updated_at = VALUES(updated_at)
`, wikiSearchProjectionKindLexical, now, now)
	if stale.Error != nil {
		return live.RowsAffected, stale.Error
	}
	return live.RowsAffected + stale.RowsAffected, nil
}
