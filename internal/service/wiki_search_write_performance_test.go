package service_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/embedding"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

type blockingFirstWikiEmbedder struct {
	started     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
	mu          sync.Mutex
	calls       int
}

func newBlockingFirstWikiEmbedder() *blockingFirstWikiEmbedder {
	return &blockingFirstWikiEmbedder{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (e *blockingFirstWikiEmbedder) Embed(ctx context.Context, _ string) ([]float32, error) {
	e.mu.Lock()
	e.calls++
	first := e.calls == 1
	e.mu.Unlock()
	if first {
		close(e.started)
		select {
		case <-e.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []float32{1, 0, 0}, nil
}

func (e *blockingFirstWikiEmbedder) Dimensions() int { return 3 }

func (e *blockingFirstWikiEmbedder) Release() {
	e.releaseOnce.Do(func() {
		close(e.release)
	})
}

func TestWikiSearchEmbeddingDoesNotBlockFollowingWikiWrite(t *testing.T) {
	embedder := newBlockingFirstWikiEmbedder()
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{
		Embedder: embedder,
	})
	defer cleanup()
	defer embedder.Release()

	ctx := context.Background()
	owner := db.User{Login: "wiki-search-queue-owner", Name: "Wiki Search Queue Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-search-queue"
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-search-queue",
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	first, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nFirst body.\n", "create home", "")
	if err != nil {
		t.Fatalf("PutWikiPage(first): %v", err)
	}
	select {
	case <-embedder.started:
	case <-time.After(5 * time.Second):
		t.Fatal("search embedding did not start")
	}
	assertWikiSearchBodyEventually(t, svc, "home", "First body.", 2*time.Second)

	secondDone := make(chan error, 1)
	go func() {
		_, writeErr := svc.PutWikiPage(ctx, full, "home", "# Home\n\nSecond body.\n", "update home", first.SHA)
		secondDone <- writeErr
	}()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("PutWikiPage(second): %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("following wiki write blocked on search embedding")
	}
	assertWikiSearchBodyEventually(t, svc, "home", "Second body.", 2*time.Second)

	embedder.Release()
	svc.Wg.Wait()

	var stored db.WikiSearchDocument
	if err := svc.DB.
		Where("slug = ?", "home").
		First(&stored).Error; err != nil {
		t.Fatalf("load search document: %v", err)
	}
	if !strings.Contains(string(stored.Body), "Second body.") {
		t.Fatalf("search body = %q, want latest wiki body", stored.Body)
	}
}

func TestWikiSearchInsufficientQuotaDoesNotRetry(t *testing.T) {
	embedder := &service.FakeEmbedder{Err: &embedding.APIError{
		StatusCode: 429,
		Code:       "insufficient_quota",
		Type:       "insufficient_quota",
	}}
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{Embedder: embedder})
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "wiki-search-quota-owner", Name: "Wiki Search Quota Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-search-quota"
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-search-quota",
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nLexical content.\n", "create home", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}
	svc.Wg.Wait()
	if embedder.Called != 1 {
		t.Fatalf("embedding calls = %d, want 1 for permanent quota failure", embedder.Called)
	}
	assertWikiSearchBodyEventually(t, svc, "home", "Lexical content.", time.Second)
}

func assertWikiSearchBodyEventually(t *testing.T, svc *service.Service, slug, body string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var stored db.WikiSearchDocument
		err := svc.DB.Where("slug = ?", slug).First(&stored).Error
		if err == nil && strings.Contains(string(stored.Body), body) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("wiki search document %q did not contain %q before timeout; last error: %v", slug, body, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
