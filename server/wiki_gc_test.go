package server

import (
	"context"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

func TestStartWikiCatalogGCWorkerStopsWithServerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	svc := &service.Service{
		Ctx:         ctx,
		WikiCatalog: &wikicatalog.Catalog{},
	}

	startWikiCatalogGCWorker(svc)
	cancel()

	done := make(chan struct{})
	go func() {
		svc.Wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("wiki catalog GC worker did not stop after server context cancellation")
	}
}
