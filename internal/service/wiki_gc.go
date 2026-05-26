package service

// Wiki catalog GC — service-layer entry point. Wraps wikicatalog.Catalog.GCRun
// so an admin endpoint or scheduled job can invoke it without depending on
// catalog internals.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"gh-server/internal/db"
	"gh-server/internal/gitstore"
	"gh-server/internal/wikicatalog"
)

// Default TTLs documented in wikicatalog/gc.go. The service-level
// defaults are conservative; operators may override via the
// WikiGCOptions struct.
const (
	defaultWikiPendingTTL  = time.Hour
	defaultWikiRefcountTTL = time.Hour

	synthProjectionPending      int16 = 0
	synthProjectionMaterialized int16 = 1
)

// WikiGCOptions tunes a GC run. Zero values pick the defaults
// documented above.
type WikiGCOptions struct {
	PendingTTL  time.Duration
	RefcountTTL time.Duration
}

// WikiCatalogPostCommit is the catalog's post-commit hook, wired in
// main.go. It drives every side effect that the legacy git-backed
// handlers used to schedule synchronously: the search index plus
// materialization of the wiki bare git repo so `git clone` / `git
// pull` against the wiki still works after the catalog cutover.
//
// Errors here surface back to the caller of ApplyChangeSet so an
// operator can see what failed, but they do NOT roll back the
// catalog state — the catalog is already committed by the time this
// runs. A failed git materialization leaves catalog ahead of git; the
// next background migration replay is idempotent and re-materializes.
func (s *Service) WikiCatalogPostCommit(ctx context.Context, repoID uint, result wikicatalog.ChangeSetResult) error {
	// Migration replays historical commits in order. If those writes
	// fan out into unordered goroutines, the final wiki_search_documents
	// row can regress to an older body or a false delete. Keep
	// migration indexing synchronous; runtime REST writes still queue
	// asynchronously to preserve request latency.
	var repo db.Repository
	if err := s.DBForCtx(ctx).Select("id", "full_name").
		First(&repo, "id = ?", repoID).Error; err != nil {
		return fmt.Errorf("wiki post-commit: lookup repo %d: %w", repoID, err)
	}
	for _, ch := range result.Changes {
		switch ch.Op {
		case wikicatalog.OpUpsert, wikicatalog.OpRename:
			// On rename, the old slug's search document must be
			// removed before indexing the new one or `wiki/search`
			// will surface both.
			if ch.Op == wikicatalog.OpRename && ch.PrevSlug != "" && ch.PrevSlug != ch.Slug {
				if result.Source == wikicatalog.SourceMigration {
					if err := s.deleteWikiSearchDocument(ctx, repo.FullName, ch.PrevSlug); err != nil {
						return fmt.Errorf("wiki post-commit: delete prev search doc for %s: %w", ch.PrevSlug, err)
					}
				} else {
					s.queueWikiSearchDelete(ctx, repo.FullName, ch.PrevSlug)
				}
			}
			// Mirror the legacy WikiPage shape for the search indexer.
			page := WikiPage{
				Slug:  ch.Slug,
				Title: wikicatalog.TitleFromSlug(ch.Slug),
				SHA:   ch.BlobSHA,
			}
			// Best-effort body read for the indexer: prefer inline,
			// fall back to CAS. If both miss (race with GC), skip
			// reindex rather than fail the post-commit chain.
			body, ok := s.wikiBodyForReindex(ctx, ch.PageID, ch.BlobSHA)
			if ok {
				page.Body = body
			}
			if result.Source == wikicatalog.SourceMigration {
				if err := s.upsertWikiSearchDocument(ctx, repo.FullName, page); err != nil {
					return fmt.Errorf("wiki post-commit: upsert search doc for %s: %w", ch.Slug, err)
				}
				continue
			}
			s.queueWikiSearchUpsert(ctx, repo.FullName, page)
		case wikicatalog.OpDelete:
			if result.Source == wikicatalog.SourceMigration {
				if err := s.deleteWikiSearchDocument(ctx, repo.FullName, ch.Slug); err != nil {
					return fmt.Errorf("wiki post-commit: delete search doc for %s: %w", ch.Slug, err)
				}
				continue
			}
			s.queueWikiSearchDelete(ctx, repo.FullName, ch.Slug)
		}
	}
	// Migration replays existing git commits into the catalog — the
	// git repo already holds the trees, so re-materializing would
	// duplicate history. Runtime REST writes (Source = rest/batch)
	// originate in the catalog and must land in git for clone/pull.
	if result.Source != wikicatalog.SourceMigration {
		if err := s.materializeChangesetToGit(ctx, repo.FullName, result); err != nil {
			return fmt.Errorf("wiki post-commit: materialize git for %s: %w", repo.FullName, err)
		}
	}
	return nil
}

// materializeChangesetToGit projects a catalog changeset onto the
// legacy wiki bare repo as a single git commit. This keeps `git
// clone` and `git pull` consistent with the catalog after the
// REST-write cutover; the catalog is SOT, git is a materialized
// projection.
//
// Precondition: the caller already holds Git.WithRepoLock for the
// wiki repo. The service-layer write entry points (PutWikiPage,
// DeleteWikiPage, MoveWikiPage, MoveWikiPagePrefix) all take the
// lock around their ApplyChangeSet call so concurrent post-commit
// hooks land git commits in the same order they landed in the
// catalog; re-locking here would deadlock because the gitstore
// mutex is not reentrant.
func (s *Service) materializeChangesetToGit(ctx context.Context, repoFullName string, result wikicatalog.ChangeSetResult) error {
	if s.Git == nil {
		return nil
	}
	full := wikiRepoFullName(repoFullName)
	if err := s.ensureWikiRepo(ctx, repoFullName); err != nil {
		return err
	}
	var changeset db.WikiChangeset
	if err := s.DBForCtx(ctx).Select("message", "committed_at").
		First(&changeset, "changeset_id = ?", result.ChangesetID).Error; err != nil {
		return fmt.Errorf("lookup changeset message: %w", err)
	}
	mutations := make([]gitstore.FileMutation, 0, len(result.Changes)*2)
	for _, ch := range result.Changes {
		switch ch.Op {
		case wikicatalog.OpUpsert:
			body, ok := s.wikiBodyForReindex(ctx, ch.PageID, ch.BlobSHA)
			if !ok {
				return fmt.Errorf("body unavailable for upsert of %q", ch.Slug)
			}
			mutations = append(mutations, gitstore.FileMutation{
				Path:    wikiSlugToPath(ch.Slug),
				Content: []byte(body),
			})
		case wikicatalog.OpDelete:
			mutations = append(mutations, gitstore.FileMutation{
				Path:   wikiSlugToPath(ch.PrevSlug),
				Delete: true,
			})
		case wikicatalog.OpRename:
			body, ok := s.wikiBodyForReindex(ctx, ch.PageID, ch.BlobSHA)
			if !ok {
				return fmt.Errorf("body unavailable for rename of %q", ch.Slug)
			}
			mutations = append(mutations,
				gitstore.FileMutation{Path: wikiSlugToPath(ch.PrevSlug), Delete: true},
				gitstore.FileMutation{Path: wikiSlugToPath(ch.Slug), Content: []byte(body)},
			)
		}
	}
	if len(mutations) == 0 {
		return nil
	}
	gitSHA, err := s.Git.CommitFilesAt(ctx, full, wikiDefaultBranch, string(changeset.Message), mutations, changeset.CommittedAt)
	if err != nil {
		return err
	}
	// Reconcile the catalog's synth_commit_sha (and the per-revision
	// commit_sha) with the materialized git commit SHA. Two effects:
	//   1. A subsequent MigrateWiki run sees this changeset as
	//      already-migrated and skips it; without this MigrateWiki
	//      would double-apply every runtime write.
	//   2. wiki_page_revisions.commit_sha matches the real git SHA so
	//      ref-pinned reads (GetWikiPageAtRef) and the history
	//      endpoint resolve correctly.
	//
	// Both UPDATEs run in one transaction so a back-to-back write
	// burst doesn't queue against SQLite's single-writer lock twice
	// per ApplyChangeSet.
	return s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&db.WikiChangeset{}).
			Where("changeset_id = ?", result.ChangesetID).
			Updates(map[string]any{
				"synth_commit_sha": gitSHA,
				"synth_format_ver": synthProjectionMaterialized,
			}).Error; err != nil {
			return err
		}
		return tx.Model(&db.WikiPageRevision{}).
			Where("changeset_id = ?", result.ChangesetID).
			UpdateColumn("commit_sha", gitSHA).Error
	})
}

// wikiBodyForReindex retrieves the latest body for a page, preferring
// the inline copy on the page row. Used only by the post-commit
// search hook; production reads go through the catalog API.
func (s *Service) wikiBodyForReindex(ctx context.Context, pageID uint64, blobSHA string) (string, bool) {
	var p db.WikiPage
	if err := s.DBForCtx(ctx).Select("body_inline").
		First(&p, "page_id = ?", pageID).Error; err != nil {
		return "", false
	}
	if p.BodyInline != nil {
		return string(p.BodyInline), true
	}
	if s.WikiBlob == nil || blobSHA == "" {
		return "", false
	}
	body, err := s.WikiBlob.Get(ctx, blobSHA)
	if err != nil {
		return "", false
	}
	return string(body), true
}

// RunWikiCatalogGC reclaims orphaned wiki blobs and zero-refcount
// entries. Operators run this on a schedule (recommended: daily) or
// manually after a known large delete/migration. Idempotent.
func (s *Service) RunWikiCatalogGC(ctx context.Context, opts WikiGCOptions) (wikicatalog.GCStats, error) {
	if s.WikiCatalog == nil {
		return wikicatalog.GCStats{}, errors.New("wiki gc: catalog not configured")
	}
	pendingTTL := opts.PendingTTL
	if pendingTTL <= 0 {
		pendingTTL = defaultWikiPendingTTL
	}
	refcountTTL := opts.RefcountTTL
	if refcountTTL <= 0 {
		refcountTTL = defaultWikiRefcountTTL
	}
	return s.WikiCatalog.GCRun(ctx, time.Now().UTC(), pendingTTL, refcountTTL)
}
