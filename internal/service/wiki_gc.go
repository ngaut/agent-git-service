package service

// Wiki catalog GC — service-layer entry point. Wraps wikicatalog.Catalog.GCRun
// so an admin endpoint or scheduled job can invoke it without depending on
// catalog internals.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/wikicatalog"
)

// Default TTLs documented in wikicatalog/gc.go. The service-level
// defaults are conservative; operators may override via the
// WikiGCOptions struct.
const (
	defaultWikiPendingTTL  = time.Hour
	defaultWikiRefcountTTL = time.Hour
)

// WikiGCOptions tunes a GC run. Zero values pick the defaults
// documented above.
type WikiGCOptions struct {
	PendingTTL  time.Duration
	RefcountTTL time.Duration
}

// WikiCatalogPostCommit is the catalog's post-commit hook, wired in
// main.go. It drives every side effect that the legacy git-backed
// handlers used to schedule synchronously: today, the search index.
// Future work: webhook dispatch, cache invalidation, etc.
//
// Errors here surface back to the caller of ApplyChangeSet so an
// operator can see what failed, but they do NOT roll back the
// catalog state — the catalog is already committed by the time this
// runs.
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
	return nil
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
