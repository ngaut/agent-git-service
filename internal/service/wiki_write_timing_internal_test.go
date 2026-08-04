package service

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	applog "github.com/ngaut/agent-git-service/internal/logging"
)

func TestWikiWriteTimingConcurrentCollection(t *testing.T) {
	ctx := applog.WithRequestContext(context.Background())
	ctx, timing := withWikiWriteTiming(ctx, "put")

	const workers = 32
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(value int64) {
			defer wg.Done()
			jobCtx := cloneWikiWriteTiming(context.Background(), ctx)
			recordWikiWriteDuration(jobCtx, wikiWritePhaseReferenceSync, time.Millisecond)
			recordWikiWriteValue(jobCtx, wikiWriteValueSearchQueueDepth, value)
		}(int64(worker))
	}
	wg.Wait()
	timing.flush(ctx)

	values := make(map[string]slog.Value)
	for _, attr := range applog.SnapshotAttrs(ctx) {
		values[attr.Key] = attr.Value.Resolve()
	}
	if got := values["wiki_write_reference_sync_ms"]; got.Kind() != slog.KindInt64 || got.Int64() != workers {
		t.Fatalf("wiki_write_reference_sync_ms = %v, want %d", got, workers)
	}
	if got := values["wiki_write_search_queue_depth"]; got.Kind() != slog.KindInt64 || got.Int64() != workers-1 {
		t.Fatalf("wiki_write_search_queue_depth = %v, want %d", got, workers-1)
	}
}
