package server

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ngaut/agent-git-service/internal/service"
)

const (
	wikiCatalogGCInterval    = time.Hour
	wikiCatalogGCTimeout     = 5 * time.Minute
	wikiCatalogGCPendingTTL  = time.Hour
	wikiCatalogGCRefcountTTL = time.Hour
)

func startRuntimeWorkers(deps *bootstrapDeps) {
	if deps == nil {
		return
	}
	startWikiCatalogGCWorker(deps.SvcDeps)
	if deps.SvcDeps != nil {
		deps.SvcDeps.StartWikiSearchProjectionWorker()
		deps.SvcDeps.StartWikiReferenceEffectsRecoveryWorker()
	}
}

func startWikiCatalogGCWorker(svc *service.Service) {
	if svc == nil || svc.WikiCatalog == nil || svc.Ctx == nil {
		return
	}
	svc.Wg.Add(1)
	go func() {
		defer svc.Wg.Done()
		ticker := time.NewTicker(wikiCatalogGCInterval)
		defer ticker.Stop()
		for {
			select {
			case <-svc.ServerCtx().Done():
				return
			case <-ticker.C:
				runWikiCatalogGC(svc)
			}
		}
	}()
}

func runWikiCatalogGC(svc *service.Service) {
	if svc == nil || svc.WikiCatalog == nil {
		return
	}
	ctx, cancel := context.WithTimeout(svc.ServerCtx(), wikiCatalogGCTimeout)
	defer cancel()

	stats, err := svc.WikiCatalog.GCRun(ctx, time.Now().UTC(), wikiCatalogGCPendingTTL, wikiCatalogGCRefcountTTL)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			slog.DebugContext(ctx, "wiki catalog gc stopped", "error", err)
			return
		}
		slog.WarnContext(ctx, "wiki catalog gc failed", "error", err)
		return
	}
	if stats.InlineRefsReclaimed == 0 && stats.PendingReclaimed == 0 && stats.BlobsReclaimed == 0 {
		slog.DebugContext(ctx, "wiki catalog gc completed",
			"inline_refs_reclaimed", 0,
			"pending_reclaimed", 0,
			"blobs_reclaimed", 0,
		)
		return
	}
	slog.InfoContext(ctx, "wiki catalog gc reclaimed blobs",
		"inline_refs_reclaimed", stats.InlineRefsReclaimed,
		"pending_reclaimed", stats.PendingReclaimed,
		"blobs_reclaimed", stats.BlobsReclaimed,
	)
}
