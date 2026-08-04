package service

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	applog "github.com/ngaut/agent-git-service/internal/logging"
)

const (
	wikiWritePhaseTotal                 = "total"
	wikiWritePhaseRepoLookup            = "repo_lookup"
	wikiWritePhaseLockRepoLookup        = "lock_repo_lookup"
	wikiWritePhaseCatalogLockWait       = "catalog_lock_wait"
	wikiWritePhaseGitLockWait           = "git_lock_wait"
	wikiWritePhaseCriticalSection       = "critical_section"
	wikiWritePhaseRepoInit              = "repo_init"
	wikiWritePhaseReconcile             = "reconcile"
	wikiWritePhaseMutations             = "mutations"
	wikiWritePhasePreflight             = "preflight"
	wikiWritePhaseGitPrepare            = "git_prepare"
	wikiWritePhaseGitPrepareBuild       = "git_prepare_build"
	wikiWritePhaseGitPreparePersist     = "git_prepare_persist"
	wikiWritePhaseGitPersistBarrierWait = "git_persist_barrier_wait"
	wikiWritePhaseCatalogApplyTotal     = "catalog_apply_total"
	wikiWritePhaseCatalogBlobUpload     = "catalog_blob_upload"
	wikiWritePhaseCatalogTransaction    = "catalog_transaction"
	wikiWritePhaseCatalogTxnBody        = "catalog_transaction_body"
	wikiWritePhaseCatalogTxnBoundary    = "catalog_transaction_boundary"
	wikiWritePhaseCatalogChangeset      = "catalog_changeset_insert"
	wikiWritePhaseCatalogHeadCAS        = "catalog_head_cas"
	wikiWritePhaseCatalogChanges        = "catalog_changes"
	wikiWritePhaseCatalogPageWrite      = "catalog_page_write"
	wikiWritePhaseCatalogRevision       = "catalog_revision_insert"
	wikiWritePhaseCatalogBlobRefs       = "catalog_blob_refs"
	wikiWritePhaseCatalogOutlinks       = "catalog_outlinks"
	wikiWritePhaseCatalogInboundLinks   = "catalog_inbound_links"
	wikiWritePhaseCatalogLabels         = "catalog_label_mutation"
	wikiWritePhaseCatalogPendingCleanup = "catalog_pending_blob_cleanup"
	wikiWritePhaseCatalogCommitBarrier  = "catalog_commit_barrier"
	wikiWritePhaseCatalogPostCommit     = "catalog_post_commit"
	wikiWritePhaseGitPublish            = "git_publish"
	wikiWritePhasePostCommitEnqueue     = "post_commit_enqueue"
	wikiWritePhaseReferenceQueueWait    = "reference_queue_wait"
	wikiWritePhaseReferenceEffectsTotal = "reference_effects_total"
	wikiWritePhaseReferenceSync         = "reference_sync"
	wikiWritePhaseSearchEnqueue         = "search_enqueue"
	wikiWritePhasePostCommitWait        = "post_commit_wait"
	wikiWritePhaseLabels                = "labels"

	wikiWriteValueBodyBytes           = "body_bytes"
	wikiWriteValueReferenceQueueDepth = "reference_queue_depth"
	wikiWriteValueSearchQueueDepth    = "search_queue_depth"
)

type wikiWriteTimingContextKey struct{}

type wikiWriteTiming struct {
	mu        sync.Mutex
	operation string
	durations map[string]time.Duration
	values    map[string]int64
}

func withWikiWriteTiming(ctx context.Context, operation string) (context.Context, *wikiWriteTiming) {
	timing := &wikiWriteTiming{
		operation: operation,
		durations: make(map[string]time.Duration),
		values:    make(map[string]int64),
	}
	return context.WithValue(ctx, wikiWriteTimingContextKey{}, timing), timing
}

func cloneWikiWriteTiming(dst, src context.Context) context.Context {
	if timing := wikiWriteTimingFromContext(src); timing != nil {
		return context.WithValue(dst, wikiWriteTimingContextKey{}, timing)
	}
	return dst
}

func wikiWriteTimingFromContext(ctx context.Context) *wikiWriteTiming {
	timing, _ := ctx.Value(wikiWriteTimingContextKey{}).(*wikiWriteTiming)
	return timing
}

func observeWikiWritePhase(ctx context.Context, phase string, started time.Time) {
	recordWikiWriteDuration(ctx, phase, time.Since(started))
}

func recordWikiWriteDuration(ctx context.Context, phase string, duration time.Duration) {
	timing := wikiWriteTimingFromContext(ctx)
	if timing == nil {
		return
	}
	timing.mu.Lock()
	timing.durations[phase] += duration
	timing.mu.Unlock()
}

func recordWikiWriteValue(ctx context.Context, name string, value int64) {
	timing := wikiWriteTimingFromContext(ctx)
	if timing == nil {
		return
	}
	timing.mu.Lock()
	if current, ok := timing.values[name]; !ok || value > current {
		timing.values[name] = value
	}
	timing.mu.Unlock()
}

func (t *wikiWriteTiming) flush(ctx context.Context) {
	if t == nil {
		return
	}

	t.mu.Lock()
	durationKeys := make([]string, 0, len(t.durations))
	for key := range t.durations {
		durationKeys = append(durationKeys, key)
	}
	valueKeys := make([]string, 0, len(t.values))
	for key := range t.values {
		valueKeys = append(valueKeys, key)
	}
	sort.Strings(durationKeys)
	sort.Strings(valueKeys)

	attrs := make([]slog.Attr, 0, 1+len(durationKeys)+len(valueKeys))
	attrs = append(attrs, slog.String("wiki_write_operation", t.operation))
	for _, key := range durationKeys {
		attrs = append(attrs, slog.Int64("wiki_write_"+key+"_ms", t.durations[key].Milliseconds()))
	}
	for _, key := range valueKeys {
		attrs = append(attrs, slog.Int64("wiki_write_"+key, t.values[key]))
	}
	t.mu.Unlock()

	applog.AddAttrs(ctx, attrs...)
}
