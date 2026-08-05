package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/embedding"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

type semanticWikiEmbedder struct{}

func (semanticWikiEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	if strings.Contains(text, "session expiry") || strings.Contains(text, "Session expiry") {
		return []float32{1, 0, 0}, nil
	}
	if strings.Contains(text, "Cache invalidation") || strings.Contains(text, "cache invalidation") {
		return []float32{0, 1, 0}, nil
	}
	return []float32{0, 0, 1}, nil
}

func (semanticWikiEmbedder) Dimensions() int { return 3 }

type noisyWikiEmbedder struct{}

func (noisyWikiEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	switch {
	case strings.TrimSpace(text) == "xiangz":
		return []float32{9, 9, 9}, nil
	case strings.Contains(text, "xiangz"):
		return []float32{1, 0, 0}, nil
	case strings.Contains(text, "# x"):
		return []float32{0, 1, 0}, nil
	default:
		return []float32{0, 0, 1}, nil
	}
}

func (noisyWikiEmbedder) Dimensions() int { return 3 }

type semanticPaginationEmbedder struct{}

func (semanticPaginationEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	if strings.TrimSpace(text) == "semantic offset query" {
		return []float32{1, 0, 0}, nil
	}
	return []float32{0, 0, 1}, nil
}

func (semanticPaginationEmbedder) Dimensions() int { return 3 }

type hybridFusionFallbackEmbedder struct{}

func (hybridFusionFallbackEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	if strings.TrimSpace(text) == "fusion" {
		return []float32{1, 0, 0}, nil
	}
	return []float32{0, 0, 1}, nil
}

func (hybridFusionFallbackEmbedder) Dimensions() int { return 3 }

type recordingWikiEmbedder struct {
	mu       sync.Mutex
	vec      []float32
	called   int
	lastText string
}

func (r *recordingWikiEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.called++
	r.lastText = text
	return r.vec, nil
}

func (r *recordingWikiEmbedder) Dimensions() int { return len(r.vec) }

func (r *recordingWikiEmbedder) LastCall() (string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastText, r.called
}

type concurrentWikiEmbedder struct {
	delay         time.Duration
	mu            sync.Mutex
	called        int
	inFlight      int
	maxConcurrent int
}

func (e *concurrentWikiEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	e.mu.Lock()
	e.called++
	e.inFlight++
	if e.inFlight > e.maxConcurrent {
		e.maxConcurrent = e.inFlight
	}
	e.mu.Unlock()

	time.Sleep(e.delay)

	e.mu.Lock()
	e.inFlight--
	e.mu.Unlock()
	return []float32{0.1, 0.2, 0.3}, nil
}

func (e *concurrentWikiEmbedder) Dimensions() int { return 3 }

func (e *concurrentWikiEmbedder) Stats() (called, maxConcurrent int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.called, e.maxConcurrent
}

func createWikiSearchDocument(t *testing.T, svc *service.Service, doc db.WikiSearchDocument, what string) {
	t.Helper()
	tx := svc.DB
	if doc.Embedding == "" || !svc.DB.Migrator().HasColumn("wiki_search_documents", "embedding") {
		tx = tx.Omit("Embedding")
	}
	if err := tx.Create(&doc).Error; err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

func createWikiSearchDocuments(t *testing.T, svc *service.Service, docs []db.WikiSearchDocument, batchSize int, what string) {
	t.Helper()
	omitEmbedding := !svc.DB.Migrator().HasColumn("wiki_search_documents", "embedding")
	for _, doc := range docs {
		if doc.Embedding == "" {
			omitEmbedding = true
			break
		}
	}
	tx := svc.DB
	if omitEmbedding {
		tx = tx.Omit("Embedding")
	}
	if batchSize > 0 {
		if err := tx.CreateInBatches(docs, batchSize).Error; err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		return
	}
	if err := tx.Create(&docs).Error; err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

func TestWikiSearchTruncatesLongPageEmbeddingInput(t *testing.T) {
	recorder := &recordingWikiEmbedder{vec: []float32{0.1, 0.2, 0.3}}
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{
		Embedder: recorder,
	})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-token-truncate",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	body := "# Long Page\n\n" + strings.Repeat(" token", embedding.MaxInputTokens+512)
	fullInput := "Long Page\n\n" + body
	if tokens, err := embedding.CountInputTokens(fullInput); err != nil {
		t.Fatalf("count original tokens: %v", err)
	} else if tokens <= embedding.MaxInputTokens {
		t.Fatalf("test fixture has %d tokens, want > %d", tokens, embedding.MaxInputTokens)
	}

	full := "testuser/wiki-token-truncate"
	if _, err := svc.PutWikiPage(ctx, full, "long-page", body, "create long page", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	lastText, called := recorder.LastCall()
	if called == 0 {
		t.Fatal("expected wiki search indexer to call embedder")
	}
	tokens, err := embedding.CountInputTokens(lastText)
	if err != nil {
		t.Fatalf("count truncated tokens: %v", err)
	}
	if tokens > embedding.MaxInputTokens {
		t.Fatalf("wiki embedding text has %d tokens, want <= %d", tokens, embedding.MaxInputTokens)
	}
	if len(lastText) >= len(fullInput) {
		t.Fatalf("expected wiki embedding input to be truncated")
	}
	if !strings.HasPrefix(lastText, "Long Page\n") {
		t.Fatalf("wiki embedding input prefix = %q", lastText[:min(len(lastText), 32)])
	}

	var stored db.WikiSearchDocument
	if err := svc.DB.Where("slug = ?", "long-page").First(&stored).Error; err != nil {
		t.Fatalf("load search doc: %v", err)
	}
	if stored.Embedding == "" {
		t.Fatal("expected embedding to be stored for token-truncated long page")
	}
}

func TestWikiSearchLifecycleAndFallback_Issue1362(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-search",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-search"

	page, err := svc.PutWikiPage(ctx, full, "tutorial/auth", "# Authentication\n\nThe session token expires after 30 minutes of inactivity.", "create auth", "")
	if err != nil {
		t.Fatalf("PutWikiPage(create): %v", err)
	}
	svc.Wg.Wait()

	resp, err := svc.SearchWikiPages(ctx, full, "token expires", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(create): %v", err)
	}
	if resp.Method != "substring" {
		t.Fatalf("method = %q, want substring", resp.Method)
	}
	if len(resp.Results) != 1 || resp.Results[0].Slug != "tutorial/auth" {
		t.Fatalf("results after create = %#v, want tutorial/auth", resp.Results)
	}
	if resp.Results[0].Title != "Auth" {
		t.Fatalf("search title = %q, want Auth", resp.Results[0].Title)
	}

	page, err = svc.PutWikiPage(ctx, full, "tutorial/auth", "# Authentication\n\nRefresh tokens rotate automatically; expiry wording removed.", "update auth", page.SHA)
	if err != nil {
		t.Fatalf("PutWikiPage(update): %v", err)
	}
	svc.Wg.Wait()

	resp, err = svc.SearchWikiPages(ctx, full, "token expires", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(update): %v", err)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("results after update = %#v, want empty", resp.Results)
	}

	moved, err := svc.MoveWikiPage(ctx, full, "tutorial/auth", "guides/authentication", page.SHA, "move auth")
	if err != nil {
		t.Fatalf("MoveWikiPage: %v", err)
	}
	svc.Wg.Wait()

	resp, err = svc.SearchWikiPages(ctx, full, "refresh tokens", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(move): %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Slug != moved.Moved.Slug {
		t.Fatalf("results after move = %#v, want %q", resp.Results, moved.Moved.Slug)
	}

	if err := svc.DeleteWikiPage(ctx, full, moved.Moved.Slug, "delete auth"); err != nil {
		t.Fatalf("DeleteWikiPage: %v", err)
	}
	svc.Wg.Wait()

	resp, err = svc.SearchWikiPages(ctx, full, "refresh tokens", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(delete): %v", err)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("results after delete = %#v, want empty", resp.Results)
	}
}

func TestWikiSearchReturnsResultsWhenIndexUnavailable(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-search-fallback",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-search-fallback"

	if _, err := svc.PutWikiPage(ctx, full, "tutorial/auth", "# Authentication\n\nThe session token expires after 30 minutes of inactivity.", "create auth", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}
	svc.Wg.Wait()
	if err := svc.DB.Migrator().DropTable(&db.WikiSearchDocument{}); err != nil {
		t.Fatalf("drop wiki search table: %v", err)
	}

	resp, err := svc.SearchWikiPages(ctx, full, "token expires", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(index unavailable): %v", err)
	}
	if resp.Method != "substring" {
		t.Fatalf("method = %q, want substring", resp.Method)
	}
	if len(resp.Results) != 1 || resp.Results[0].Slug != "tutorial/auth" {
		t.Fatalf("fallback results = %#v, want tutorial/auth", resp.Results)
	}
	if resp.Results[0].Title != "Auth" {
		t.Fatalf("fallback title = %q, want Auth", resp.Results[0].Title)
	}
}

func TestWikiSearchUsesCatalogContentWhenIndexUnavailable(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-search-catalog-first",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-search-catalog-first"
	if _, err := svc.PutWikiPage(ctx, full, "tutorial/auth", "# Authentication\n\nGit body does not contain the catalog query.", "create auth", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	catalogBody := []byte("# Authentication\n\nCatalog only needle lives in the DB catalog body.")
	if err := svc.DB.Model(&db.WikiPage{}).
		Where("repository_id = ? AND slug = ?", repo.ID, "tutorial/auth").
		Updates(map[string]any{
			"body_inline": catalogBody,
			"body_size":   len(catalogBody),
		}).Error; err != nil {
		t.Fatalf("mutate catalog body: %v", err)
	}
	if err := svc.DB.Migrator().DropTable(&db.WikiSearchDocument{}); err != nil {
		t.Fatalf("drop wiki search table: %v", err)
	}

	resp, err := svc.SearchWikiPages(ctx, full, "catalog only needle", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(index unavailable): %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Slug != "tutorial/auth" {
		t.Fatalf("catalog fallback results = %#v, want tutorial/auth", resp.Results)
	}
	if !strings.Contains(resp.Results[0].Snippet, "<mark>Catalog</mark> <mark>only</mark> <mark>needle</mark>") {
		t.Fatalf("snippet = %q, want catalog body snippet", resp.Results[0].Snippet)
	}
}

func TestWikiSearchHydratesReturnedSnippetFromLivePage(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-search-hydrate",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-search-hydrate"

	if _, err := svc.PutWikiPage(ctx, full, "guides/auth", "# Auth\n\nCurrent token flow uses refresh tokens for rotation.", "create auth", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	if err := svc.DB.Model(&db.WikiSearchDocument{}).
		Where("repository_id > 0 AND slug = ?", "guides/auth").
		Updates(map[string]any{
			"title": "Stale Auth",
			"body":  "Stale token flow from the old index snapshot.",
		}).Error; err != nil {
		t.Fatalf("mutate search doc: %v", err)
	}

	resp, err := svc.SearchWikiPages(ctx, full, "token flow", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %#v, want one result", resp.Results)
	}
	if !strings.Contains(resp.Results[0].Snippet, "uses refresh") {
		t.Fatalf("snippet = %q, want current git body", resp.Results[0].Snippet)
	}
	if strings.Contains(resp.Results[0].Snippet, "Stale token flow") {
		t.Fatalf("snippet = %q, should not use stale indexed body", resp.Results[0].Snippet)
	}
}

func TestWikiSearchPrefersGitLexicalResultsOverStaleIndexedRows(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-search-git-first",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-search-git-first"

	page, err := svc.PutWikiPage(ctx, full, "guides/auth", "# Auth\n\nLegacy token expiry wording.", "create auth", "")
	if err != nil {
		t.Fatalf("PutWikiPage(create): %v", err)
	}
	svc.Wg.Wait()

	page, err = svc.PutWikiPage(ctx, full, "guides/auth", "# Auth\n\nRefresh token rotation only.", "update auth", page.SHA)
	if err != nil {
		t.Fatalf("PutWikiPage(update): %v", err)
	}
	svc.Wg.Wait()

	if err := svc.DB.Model(&db.WikiSearchDocument{}).
		Where("repository_id > 0 AND slug = ?", "guides/auth").
		Updates(map[string]any{
			"title": "Auth",
			"body":  "Legacy token expiry wording.",
		}).Error; err != nil {
		t.Fatalf("mutate search doc stale: %v", err)
	}

	resp, err := svc.SearchWikiPages(ctx, full, "legacy token expiry", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(stale query): %v", err)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("results for stale query = %#v, want empty because git no longer matches", resp.Results)
	}

	resp, err = svc.SearchWikiPages(ctx, full, "refresh token rotation", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(live query): %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Slug != "guides/auth" {
		t.Fatalf("results for live query = %#v, want guides/auth", resp.Results)
	}
}

func TestWikiSearchDoesNotReturnGitOnlyPageWhenCatalogHasLiveRows(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-search-live-git",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-search-live-git"
	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nCatalog body only.", "create home", ""); err != nil {
		t.Fatalf("PutWikiPage(home): %v", err)
	}
	svc.Wg.Wait()

	if _, err := svc.Git.WriteFile(ctx, full+".wiki", "master", "guides/live.md", "add live page", []byte("# Live\n\nFresh git-only search text.")); err != nil {
		t.Fatalf("git write live page: %v", err)
	}
	if !svc.ClaimWikiBackgroundGitIngestForTest(ctx, full) {
		t.Fatal("claim background wiki migration slot")
	}
	defer svc.ReleaseWikiBackgroundGitIngestForTest(ctx, full)

	resp, err := svc.SearchWikiPages(ctx, full, "fresh git-only search text", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("results = %#v, want no git-only page once catalog has live rows", resp.Results)
	}

	resp, err = svc.SearchWikiPages(ctx, full, "catalog body only", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(catalog): %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Slug != "home" {
		t.Fatalf("catalog results = %#v, want home", resp.Results)
	}
	svc.Wg.Wait()
}

func TestWikiSearchFallsBackToGitWithoutCatalogRows(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-search-legacy-git",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-search-legacy-git"
	if err := svc.Git.Init(ctx, full+".wiki", "master", false); err != nil {
		t.Fatalf("init wiki: %v", err)
	}
	if _, err := svc.Git.WriteFile(ctx, full+".wiki", "master", "guides/live.md", "add live page", []byte("# Live\n\nLegacy git-only search text.")); err != nil {
		t.Fatalf("git write live page: %v", err)
	}

	resp, err := svc.SearchWikiPages(ctx, full, "legacy git-only search text", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Slug != "guides/live" {
		t.Fatalf("results = %#v, want legacy git page", resp.Results)
	}
	if !strings.Contains(resp.Results[0].Snippet, "<mark>Legacy</mark> <mark>git-only</mark> <mark>search</mark> <mark>text</mark>") {
		t.Fatalf("snippet = %q, want legacy git body", resp.Results[0].Snippet)
	}
}

func TestWikiSearchUsesCatalogBodyWhenSearchIndexLagsCatalogPage(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-search-live-snippet",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-search-live-snippet"
	if _, err := svc.PutWikiPage(ctx, full, "guides/auth", "# Auth\n\nCatalog body only.", "create auth", ""); err != nil {
		t.Fatalf("PutWikiPage(create): %v", err)
	}
	svc.Wg.Wait()

	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	staleIndexedBody := []byte("# Auth\n\nFresh git-only snippet text.")
	if err := svc.DB.Model(&db.WikiSearchDocument{}).
		Where("repository_id = ? AND slug = ?", repo.ID, "guides/auth").
		Updates(map[string]any{
			"body":         db.LargeText(staleIndexedBody),
			"revision_sha": wikicatalog.HashContent(staleIndexedBody),
		}).Error; err != nil {
		t.Fatalf("mutate stale search document: %v", err)
	}

	resp, err := svc.SearchWikiPages(ctx, full, "fresh git-only snippet text", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("results = %#v, want no git-only snippet once catalog is live", resp.Results)
	}

	resp, err = svc.SearchWikiPages(ctx, full, "catalog body only", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(catalog): %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Slug != "guides/auth" {
		t.Fatalf("catalog results = %#v, want guides/auth", resp.Results)
	}
	if strings.Contains(resp.Results[0].Snippet, "Fresh git-only snippet text.") {
		t.Fatalf("snippet = %q, should not use git projection body", resp.Results[0].Snippet)
	}
	svc.Wg.Wait()
}

func TestWikiSearchDropsStaleIndexedRowsForDeletedPages(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-search-stale-delete",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-search-stale-delete"

	page, err := svc.PutWikiPage(ctx, full, "guides/auth", "# Auth\n\nDelete me after indexing.", "create auth", "")
	if err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	if err := svc.DeleteWikiPage(ctx, full, "guides/auth", "delete auth"); err != nil {
		t.Fatalf("DeleteWikiPage: %v", err)
	}
	svc.Wg.Wait()

	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	createWikiSearchDocument(t, svc, db.WikiSearchDocument{
		RepositoryID: repo.ID,
		Slug:         "guides/auth",
		Title:        "Auth",
		Body:         db.LargeText("Delete me after indexing."),
		RevisionSHA:  page.SHA,
	}, "reinsert stale doc")

	resp, err := svc.SearchWikiPages(ctx, full, "delete me", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("results = %#v, want empty after filtering deleted git page", resp.Results)
	}
}

func TestWikiSearchBackfillsPageAfterFilteringStaleIndexedRows(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-search-stale-backfill",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-search-stale-backfill"
	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	if _, err := svc.PutWikiPage(ctx, full, "guides/live", "# Live\n\nBackfill me after stale rows.", "create live", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	baseTime := time.Date(2026, time.January, 7, 0, 0, 0, 0, time.UTC)
	staleDocs := make([]db.WikiSearchDocument, 0, 20)
	for i := 0; i < 20; i++ {
		staleDocs = append(staleDocs, db.WikiSearchDocument{
			RepositoryID: repo.ID,
			Slug:         fmt.Sprintf("guides/stale-%02d", i),
			Title:        fmt.Sprintf("Stale %02d", i),
			Body:         db.LargeText("Backfill me after stale rows."),
			CreatedAt:    baseTime.Add(time.Duration(20-i) * time.Second),
			UpdatedAt:    baseTime.Add(time.Duration(20-i) * time.Second),
		})
	}
	createWikiSearchDocuments(t, svc, staleDocs, 20, "seed stale docs")
	if err := svc.DB.Model(&db.WikiSearchDocument{}).
		Where("repository_id = ? AND slug = ?", repo.ID, "guides/live").
		Updates(map[string]any{
			"body":       "Backfill me after stale rows.",
			"updated_at": baseTime,
		}).Error; err != nil {
		t.Fatalf("downgrade live doc ordering: %v", err)
	}

	resp, err := svc.SearchWikiPages(ctx, full, "backfill me after stale rows", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1 live result after backfill", len(resp.Results))
	}
	if resp.Results[0].Slug != "guides/live" {
		t.Fatalf("results[0].Slug = %q, want guides/live", resp.Results[0].Slug)
	}
}

func TestWikiSearchUsesCatalogLexicalWhenIndexedRowsMissLivePage(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-search-live-catalog-recall",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-search-live-catalog-recall"
	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	if _, err := svc.PutWikiPage(ctx, full, "guides/live", "# Live\n\nFresh catalog-only recall text.", "create live", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	if err := svc.DB.Where("repository_id = ? AND slug = ?", repo.ID, "guides/live").Delete(&db.WikiSearchDocument{}).Error; err != nil {
		t.Fatalf("delete live search doc: %v", err)
	}
	createWikiSearchDocument(t, svc, db.WikiSearchDocument{
		RepositoryID: repo.ID,
		Slug:         "guides/stale",
		Title:        "Stale",
		Body:         db.LargeText("Fresh catalog-only recall text."),
		RevisionSHA:  "stale-sha",
	}, "seed stale search doc")

	resp, err := svc.SearchWikiPages(ctx, full, "fresh catalog-only recall text", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(resp.Results))
	}
	if resp.Results[0].Slug != "guides/live" {
		t.Fatalf("results[0].Slug = %q, want guides/live", resp.Results[0].Slug)
	}
}

func TestWikiSearchLargeCurrentIndexMissDoesNotScanCatalog(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	created, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-search-large-index",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-search-large-index"
	repoID := created.ID

	pages := make([]db.WikiPage, 0, 102)
	docs := make([]db.WikiSearchDocument, 0, 101)
	now := time.Now().UTC()
	for i := 0; i < 101; i++ {
		slug := fmt.Sprintf("guides/page-%03d", i)
		body := []byte(fmt.Sprintf("ordinary indexed body %03d", i))
		sha := wikicatalog.HashContent(body)
		pages = append(pages, db.WikiPage{
			RepositoryID:    repoID,
			Slug:            slug,
			Title:           fmt.Sprintf("Page %03d", i),
			HeadBlobSHA:     sha,
			BodySize:        len(body),
			BodyInline:      body,
			HeadRevisionID:  1,
			HeadChangesetID: 1,
			CreatedAt:       now,
			UpdatedAt:       now,
		})
		docs = append(docs, db.WikiSearchDocument{
			RepositoryID: repoID,
			Slug:         slug,
			Title:        fmt.Sprintf("Page %03d", i),
			Body:         db.LargeText(body),
			RevisionSHA:  sha,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}
	catalogOnlyBody := []byte("catalog only needle should not trigger a large fallback scan")
	pages = append(pages, db.WikiPage{
		RepositoryID:    repoID,
		Slug:            "guides/catalog-only",
		Title:           "Catalog Only",
		HeadBlobSHA:     wikicatalog.HashContent(catalogOnlyBody),
		BodySize:        len(catalogOnlyBody),
		BodyInline:      catalogOnlyBody,
		HeadRevisionID:  1,
		HeadChangesetID: 1,
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	if err := svc.DB.CreateInBatches(pages, 50).Error; err != nil {
		t.Fatalf("seed wiki pages: %v", err)
	}
	createWikiSearchDocuments(t, svc, docs, 50, "seed wiki search docs")

	resp, err := svc.SearchWikiPages(ctx, full, "catalog only needle", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("results = %#v, want empty because large indexed repos should not catalog-scan on misses", resp.Results)
	}
}

func TestWikiSearchSemanticBackfillsPageAfterFilteringStaleIndexedRows(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{
		Embedder: semanticPaginationEmbedder{},
	})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-semantic-stale-backfill",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-semantic-stale-backfill"
	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	if _, err := svc.PutWikiPage(ctx, full, "guides/live", "# Live\n\nCurrent live page body.", "create live", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	baseTime := time.Date(2026, time.January, 8, 0, 0, 0, 0, time.UTC)
	staleDocs := make([]db.WikiSearchDocument, 0, 20)
	for i := 0; i < 20; i++ {
		staleDocs = append(staleDocs, db.WikiSearchDocument{
			RepositoryID: repo.ID,
			Slug:         fmt.Sprintf("guides/stale-semantic-%02d", i),
			Title:        fmt.Sprintf("Stale Semantic %02d", i),
			Body:         db.LargeText("semantic-only stale row"),
			Embedding:    "[1,0,0]",
			CreatedAt:    baseTime.Add(time.Duration(20-i) * time.Second),
			UpdatedAt:    baseTime.Add(time.Duration(20-i) * time.Second),
		})
	}
	createWikiSearchDocuments(t, svc, staleDocs, 20, "seed stale docs")
	if err := svc.DB.Model(&db.WikiSearchDocument{}).
		Where("repository_id = ? AND slug = ?", repo.ID, "guides/live").
		Updates(map[string]any{
			"body":       "semantic-only live row",
			"embedding":  "[1,0,0]",
			"updated_at": baseTime,
		}).Error; err != nil {
		t.Fatalf("downgrade live doc ordering: %v", err)
	}

	resp, err := svc.SearchWikiPages(ctx, full, "semantic offset query", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if resp.Method != "vector" {
		t.Fatalf("method = %q, want vector", resp.Method)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1 live semantic result after backfill", len(resp.Results))
	}
	if resp.Results[0].Slug != "guides/live" {
		t.Fatalf("results[0].Slug = %q, want guides/live", resp.Results[0].Slug)
	}
}

func TestWikiSearchVectorUnavailableFallsBackToLexicalAndReindex_Issue1362(t *testing.T) {
	embedder := semanticWikiEmbedder{}
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{Embedder: embedder})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-semantic",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-semantic"

	if _, err := svc.PutWikiPage(ctx, full, "ops/session-expiry", "# Sessions\n\nSession expiry depends on workspace policy.", "create sessions", ""); err != nil {
		t.Fatalf("PutWikiPage(first): %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "ops/cache", "# Cache\n\nCache invalidation guide.", "create cache", ""); err != nil {
		t.Fatalf("PutWikiPage(second): %v", err)
	}
	svc.Wg.Wait()

	resp, err := svc.SearchWikiPages(ctx, full, "how do we handle session expiry", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(vector unavailable): %v", err)
	}
	if resp.Method != "vector" {
		t.Fatalf("method = %q, want vector when in-process semantic fallback is available", resp.Method)
	}
	if len(resp.Results) == 0 || resp.Results[0].Slug != "ops/session-expiry" {
		t.Fatalf("vector-unavailable results = %#v, want semantic result for ops/session-expiry", resp.Results)
	}

	resp, err = svc.SearchWikiPages(ctx, full, "session expiry", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(lexical): %v", err)
	}
	if resp.Method != "vector" {
		t.Fatalf("lexical method = %q, want vector", resp.Method)
	}
	if len(resp.Results) == 0 || resp.Results[0].Slug != "ops/session-expiry" {
		t.Fatalf("lexical results = %#v, want ops/session-expiry first", resp.Results)
	}

	if err := svc.DB.Where("repository_id > 0").Delete(&db.WikiSearchDocument{}).Error; err != nil {
		t.Fatalf("clear search docs: %v", err)
	}
	count, err := svc.ReindexWikiSearch(ctx, full)
	if err != nil {
		t.Fatalf("ReindexWikiSearch: %v", err)
	}
	if count != 2 {
		t.Fatalf("ReindexWikiSearch count = %d, want 2", count)
	}
	resp, err = svc.SearchWikiPages(ctx, full, "session expiry", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(after reindex): %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected results after reindex")
	}
}

func TestReindexWikiSearchSkipsUnchangedDocuments(t *testing.T) {
	recorder := &recordingWikiEmbedder{vec: []float32{0.1, 0.2, 0.3}}
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{Embedder: recorder})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-reindex-skip",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-reindex-skip"

	if _, err := svc.PutWikiPage(ctx, full, "guides/one", "# One\n\nBody one.", "create one", ""); err != nil {
		t.Fatalf("PutWikiPage(one): %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "guides/two", "# Two\n\nBody two.", "create two", ""); err != nil {
		t.Fatalf("PutWikiPage(two): %v", err)
	}
	svc.Wg.Wait()

	if got := recorder.called; got != 2 {
		t.Fatalf("initial embed calls = %d, want 2", got)
	}
	count, err := svc.ReindexWikiSearch(ctx, full)
	if err != nil {
		t.Fatalf("ReindexWikiSearch: %v", err)
	}
	if count != 2 {
		t.Fatalf("ReindexWikiSearch count = %d, want 2", count)
	}
	if got := recorder.called; got != 2 {
		t.Fatalf("embed calls after unchanged reindex = %d, want 2", got)
	}
}

func TestReindexWikiSearchRefreshesLabelOnlyChanges(t *testing.T) {
	recorder := &recordingWikiEmbedder{vec: []float32{0.1, 0.2, 0.3}}
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{Embedder: recorder})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-reindex-label-refresh",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-reindex-label-refresh"

	if _, err := svc.PutWikiPage(ctx, full, "guides/one", "# One\n\nBody one.", "create one", ""); err != nil {
		t.Fatalf("PutWikiPage(one): %v", err)
	}
	svc.Wg.Wait()

	if got := recorder.called; got != 1 {
		t.Fatalf("initial embed calls = %d, want 1", got)
	}
	if _, err := svc.CreateLabel(ctx, full, "ops", "0052CC", "Operations runbook"); err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if _, err := svc.SetWikiPageLabels(ctx, full, "guides/one", []string{"ops"}); err != nil {
		t.Fatalf("SetWikiPageLabels: %v", err)
	}
	svc.Wg.Wait()

	if got := recorder.called; got != 2 {
		t.Fatalf("embed calls after label update = %d, want 2", got)
	}
	count, err := svc.ReindexWikiSearch(ctx, full)
	if err != nil {
		t.Fatalf("ReindexWikiSearch: %v", err)
	}
	if count != 1 {
		t.Fatalf("ReindexWikiSearch count = %d, want 1", count)
	}
	if got := recorder.called; got != 2 {
		t.Fatalf("embed calls after label-only reindex = %d, want 2", got)
	}
}

func TestReindexWikiSearchUsesConcurrentUpserts(t *testing.T) {
	embedder := &concurrentWikiEmbedder{delay: 25 * time.Millisecond}
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{Embedder: embedder})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-reindex-concurrent",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-reindex-concurrent"

	for i := 0; i < 6; i++ {
		slug := fmt.Sprintf("guides/page-%d", i)
		body := fmt.Sprintf("# Page %d\n\nBody %d.", i, i)
		if _, err := svc.PutWikiPage(ctx, full, slug, body, "seed page", ""); err != nil {
			t.Fatalf("PutWikiPage(%s): %v", slug, err)
		}
	}
	svc.Wg.Wait()

	if err := svc.DB.Where("repository_id > 0").Delete(&db.WikiSearchDocument{}).Error; err != nil {
		t.Fatalf("clear search docs: %v", err)
	}
	beforeCalls, _ := embedder.Stats()
	count, err := svc.ReindexWikiSearch(ctx, full)
	if err != nil {
		t.Fatalf("ReindexWikiSearch: %v", err)
	}
	if count != 6 {
		t.Fatalf("ReindexWikiSearch count = %d, want 6", count)
	}
	afterCalls, maxConcurrent := embedder.Stats()
	if afterCalls-beforeCalls != 6 {
		t.Fatalf("reindex embed calls = %d, want 6", afterCalls-beforeCalls)
	}
	if maxConcurrent < 2 {
		t.Fatalf("max concurrent embeds = %d, want at least 2", maxConcurrent)
	}
}

func TestWikiSearchSemanticUsesTiDBVectorDistance(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{
		Embedder: semanticWikiEmbedder{},
	})
	defer cleanup()
	db.InitVector(svc.DB, 3)
	if !db.SupportsVectorDistance(svc.DB) {
		t.Skip("TiDB vector distance is unavailable")
	}
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-db-vector",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-db-vector"

	if _, err := svc.PutWikiPage(ctx, full, "ops/session-expiry", "# Sessions\n\nSession expiry depends on workspace policy.", "create sessions", ""); err != nil {
		t.Fatalf("PutWikiPage(session): %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "ops/cache", "# Cache\n\nCache invalidation guide.", "create cache", ""); err != nil {
		t.Fatalf("PutWikiPage(cache): %v", err)
	}
	svc.Wg.Wait()

	resp, err := svc.SearchWikiPages(ctx, full, "how do we handle session expiry", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if resp.Method != "vector" {
		t.Fatalf("method = %q, want vector", resp.Method)
	}
	if len(resp.Results) == 0 || resp.Results[0].Slug != "ops/session-expiry" {
		t.Fatalf("semantic results = %#v, want ops/session-expiry first", resp.Results)
	}
}

func TestWikiSearchSemanticDBPaginationBeyondExactWindow(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{
		Embedder: semanticPaginationEmbedder{},
	})
	defer cleanup()
	db.InitVector(svc.DB, 3)
	if !db.SupportsVectorDistance(svc.DB) {
		t.Skip("TiDB vector distance is unavailable")
	}
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-db-pagination",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-db-pagination"
	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	baseTime := time.Date(2026, time.January, 3, 0, 0, 0, 0, time.UTC)
	docs := make([]db.WikiSearchDocument, 0, 1005)
	for i := 0; i < 1005; i++ {
		docs = append(docs, db.WikiSearchDocument{
			RepositoryID: repo.ID,
			Slug:         fmt.Sprintf("db-semantic-%04d", i),
			Title:        fmt.Sprintf("DB Semantic %04d", i),
			Body:         db.LargeText("unrelated body text"),
			Embedding:    "[1,0,0]",
			CreatedAt:    baseTime.Add(time.Duration(i) * time.Second),
			UpdatedAt:    baseTime.Add(time.Duration(i) * time.Second),
		})
	}
	createWikiSearchDocuments(t, svc, docs, 200, "seed wiki search docs")

	resp, err := svc.SearchWikiPages(ctx, full, "semantic offset query", 20, 1000)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if resp.Method != "vector" {
		t.Fatalf("method = %q, want vector", resp.Method)
	}
	if len(resp.Results) != 5 {
		t.Fatalf("len(results) = %d, want 5", len(resp.Results))
	}
	want := []string{"db-semantic-0004", "db-semantic-0003", "db-semantic-0002", "db-semantic-0001", "db-semantic-0000"}
	for i, slug := range want {
		if resp.Results[i].Slug != slug {
			t.Fatalf("results[%d].Slug = %q, want %q", i, resp.Results[i].Slug, slug)
		}
	}
}

func TestWikiSearchMatchesSlugSegmentsWithoutTitleOrBodyHit(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-slug-match",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-slug-match"
	if _, err := svc.PutWikiPage(ctx, full, "guides/plain-page", "# Overview\n\nBody text without the path token.", "create page", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	resp, err := svc.SearchWikiPages(ctx, full, "guides", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(resp.Results))
	}
	if resp.Results[0].Slug != "guides/plain-page" {
		t.Fatalf("results[0].Slug = %q, want guides/plain-page", resp.Results[0].Slug)
	}
}

func TestWikiSearchHybridKeepsLexicalMatchAndFiltersWeakSemanticOnly(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{
		Embedder: noisyWikiEmbedder{},
	})
	defer cleanup()
	db.InitVector(svc.DB, 3)
	if !db.SupportsVectorDistance(svc.DB) {
		t.Skip("TiDB vector distance is unavailable")
	}
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-hybrid",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-hybrid"
	if _, err := svc.PutWikiPage(ctx, full, "hello", "... xiangz", "create hello", ""); err != nil {
		t.Fatalf("PutWikiPage(hello): %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "hello1", "# x", "create hello1", ""); err != nil {
		t.Fatalf("PutWikiPage(hello1): %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "x/y", "# y", "create y", ""); err != nil {
		t.Fatalf("PutWikiPage(y): %v", err)
	}
	svc.Wg.Wait()

	resp, err := svc.SearchWikiPages(ctx, full, "xiangz", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if resp.Method != "vector" {
		t.Fatalf("method = %q, want vector hybrid path", resp.Method)
	}
	if len(resp.Results) != 1 || resp.Results[0].Slug != "hello" {
		t.Fatalf("results = %#v, want only lexical xiangz match", resp.Results)
	}
}

func TestWikiSearchHybridFallbackUsesFullSemanticRanking(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{
		Embedder: hybridFusionFallbackEmbedder{},
	})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-hybrid-fallback",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-hybrid-fallback"
	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	baseTime := time.Date(2026, time.January, 4, 0, 0, 0, 0, time.UTC)
	docs := []db.WikiSearchDocument{
		{RepositoryID: repo.ID, Slug: "a-first", Title: "A First", Body: db.LargeText("fusion"), Embedding: "[0.8,0.2,0]", CreatedAt: baseTime.Add(4 * time.Second), UpdatedAt: baseTime.Add(4 * time.Second)},
		{RepositoryID: repo.ID, Slug: "b-second", Title: "B Second", Body: db.LargeText("fusion"), Embedding: "[0.7,0.3,0]", CreatedAt: baseTime.Add(3 * time.Second), UpdatedAt: baseTime.Add(3 * time.Second)},
		{RepositoryID: repo.ID, Slug: "c-third", Title: "C Third", Body: db.LargeText("fusion"), Embedding: "[1,0,0]", CreatedAt: baseTime.Add(2 * time.Second), UpdatedAt: baseTime.Add(2 * time.Second)},
		{RepositoryID: repo.ID, Slug: "d-fourth", Title: "D Fourth", Body: db.LargeText("fusion"), Embedding: "[0.9,0.1,0]", CreatedAt: baseTime.Add(1 * time.Second), UpdatedAt: baseTime.Add(1 * time.Second)},
	}
	createWikiSearchDocuments(t, svc, docs, 0, "seed wiki search docs")

	resp, err := svc.SearchWikiPages(ctx, full, "fusion", 2, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if resp.Method != "vector" {
		t.Fatalf("method = %q, want vector", resp.Method)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(resp.Results))
	}
	want := []string{"a-first", "c-third"}
	for i, slug := range want {
		if resp.Results[i].Slug != slug {
			t.Fatalf("results[%d].Slug = %q, want %q", i, resp.Results[i].Slug, slug)
		}
	}
}

func TestWikiSearchLexicalPaginationBeyondSemanticWindow(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-lexical-pagination",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-lexical-pagination"
	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	baseTime := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	docs := make([]db.WikiSearchDocument, 0, 1005)
	for i := 0; i < 1005; i++ {
		docs = append(docs, db.WikiSearchDocument{
			RepositoryID: repo.ID,
			Slug:         fmt.Sprintf("page-%04d", i),
			Title:        fmt.Sprintf("Page %04d", i),
			Body:         db.LargeText("needle"),
			CreatedAt:    baseTime.Add(time.Duration(i) * time.Second),
			UpdatedAt:    baseTime.Add(time.Duration(i) * time.Second),
		})
	}
	createWikiSearchDocuments(t, svc, docs, 200, "seed wiki search docs")

	resp, err := svc.SearchWikiPages(ctx, full, "needle", 20, 1000)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if resp.Method != "substring" {
		t.Fatalf("method = %q, want substring", resp.Method)
	}
	if len(resp.Results) != 5 {
		t.Fatalf("len(results) = %d, want 5", len(resp.Results))
	}
	want := []string{"page-0004", "page-0003", "page-0002", "page-0001", "page-0000"}
	for i, slug := range want {
		if resp.Results[i].Slug != slug {
			t.Fatalf("results[%d].Slug = %q, want %q", i, resp.Results[i].Slug, slug)
		}
	}
}

func TestWikiSearchSemanticFallbackPaginationBeyondExactWindow(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{
		Embedder: semanticPaginationEmbedder{},
	})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-semantic-pagination",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-semantic-pagination"
	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	baseTime := time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC)
	docs := make([]db.WikiSearchDocument, 0, 1005)
	for i := 0; i < 1005; i++ {
		docs = append(docs, db.WikiSearchDocument{
			RepositoryID: repo.ID,
			Slug:         fmt.Sprintf("semantic-%04d", i),
			Title:        fmt.Sprintf("Semantic %04d", i),
			Body:         db.LargeText("unrelated body text"),
			Embedding:    "[1,0,0]",
			CreatedAt:    baseTime.Add(time.Duration(i) * time.Second),
			UpdatedAt:    baseTime.Add(time.Duration(i) * time.Second),
		})
	}
	createWikiSearchDocuments(t, svc, docs, 200, "seed wiki search docs")

	resp, err := svc.SearchWikiPages(ctx, full, "semantic offset query", 20, 1000)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if resp.Method != "vector" {
		t.Fatalf("method = %q, want vector", resp.Method)
	}
	if len(resp.Results) != 5 {
		t.Fatalf("len(results) = %d, want 5", len(resp.Results))
	}
	want := []string{"semantic-0004", "semantic-0003", "semantic-0002", "semantic-0001", "semantic-0000"}
	for i, slug := range want {
		if resp.Results[i].Slug != slug {
			t.Fatalf("results[%d].Slug = %q, want %q", i, resp.Results[i].Slug, slug)
		}
	}
}

func TestWikiSearchSemanticDBPaginationReordersAfterLabelBoost(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{
		Embedder: semanticPaginationEmbedder{},
	})
	defer cleanup()
	db.InitVector(svc.DB, 3)
	if !db.SupportsVectorDistance(svc.DB) {
		t.Skip("TiDB vector distance is unavailable")
	}
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-db-label-pagination",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-db-label-pagination"
	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	baseTime := time.Date(2026, time.January, 5, 0, 0, 0, 0, time.UTC)
	docs := []db.WikiSearchDocument{
		{RepositoryID: repo.ID, Slug: "rank-a", Title: "Rank A", Body: db.LargeText("unrelated"), Embedding: "[1,0,0]", CreatedAt: baseTime.Add(5 * time.Second), UpdatedAt: baseTime.Add(5 * time.Second)},
		{RepositoryID: repo.ID, Slug: "rank-b", Title: "Rank B", Body: db.LargeText("unrelated"), Embedding: "[0.95,0.05,0]", CreatedAt: baseTime.Add(4 * time.Second), UpdatedAt: baseTime.Add(4 * time.Second)},
		{RepositoryID: repo.ID, Slug: "rank-c", Title: "Rank C", Body: db.LargeText("unrelated"), Embedding: "[0.7,0.3,0]", CreatedAt: baseTime.Add(3 * time.Second), UpdatedAt: baseTime.Add(3 * time.Second)},
		{RepositoryID: repo.ID, Slug: "rank-d", Title: "Rank D", Body: db.LargeText("unrelated"), Embedding: "[0.69,0.31,0]", CreatedAt: baseTime.Add(2 * time.Second), UpdatedAt: baseTime.Add(2 * time.Second)},
		{RepositoryID: repo.ID, Slug: "rank-e", Title: "Rank E", Body: db.LargeText("unrelated"), Embedding: "[0.68,0.32,0]", CreatedAt: baseTime.Add(1 * time.Second), UpdatedAt: baseTime.Add(1 * time.Second)},
	}
	createWikiSearchDocuments(t, svc, docs, 0, "seed wiki search docs")
	if _, err := svc.CreateLabel(ctx, full, "offset-query", "0052CC", "label match"); err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	label, err := svc.GetLabel(ctx, full, "offset-query")
	if err != nil {
		t.Fatalf("GetLabel: %v", err)
	}
	if err := svc.DB.Create(&db.WikiPageLabel{
		RepositoryID: repo.ID,
		Slug:         "rank-e",
		LabelID:      label.ID,
	}).Error; err != nil {
		t.Fatalf("create wiki label relation: %v", err)
	}

	resp, err := svc.SearchWikiPages(ctx, full, "semantic offset query", 2, 2)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if resp.Method != "vector" {
		t.Fatalf("method = %q, want vector", resp.Method)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(resp.Results))
	}
	want := []string{"rank-b", "rank-c"}
	for i, slug := range want {
		if resp.Results[i].Slug != slug {
			t.Fatalf("results[%d].Slug = %q, want %q", i, resp.Results[i].Slug, slug)
		}
	}
}

func TestWikiSearchSemanticDBPaginationPromotesLabelBoostBeyondOldPrefix(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{
		Embedder: semanticPaginationEmbedder{},
	})
	defer cleanup()
	db.InitVector(svc.DB, 3)
	if !db.SupportsVectorDistance(svc.DB) {
		t.Skip("TiDB vector distance is unavailable")
	}
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-db-label-boost-promotion",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-db-label-boost-promotion"
	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if _, err := svc.CreateLabel(ctx, full, "semantic", "d73a4a", ""); err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	label, err := svc.GetLabel(ctx, full, "semantic")
	if err != nil {
		t.Fatalf("GetLabel: %v", err)
	}

	baseTime := time.Date(2026, time.January, 6, 0, 0, 0, 0, time.UTC)
	docs := make([]db.WikiSearchDocument, 0, 260)
	for i := 0; i < 260; i++ {
		embeddingValue := "[0.90,0,0]"
		slug := fmt.Sprintf("boosted-%03d", i)
		if i == 240 {
			embeddingValue = "[0.79,0,0]"
			slug = "boosted-winner"
		}
		docs = append(docs, db.WikiSearchDocument{
			RepositoryID: repo.ID,
			Slug:         slug,
			Title:        fmt.Sprintf("Boosted %03d", i),
			Body:         db.LargeText("unrelated body text"),
			Embedding:    embeddingValue,
			CreatedAt:    baseTime.Add(time.Duration(i) * time.Second),
			UpdatedAt:    baseTime.Add(time.Duration(i) * time.Second),
		})
	}
	createWikiSearchDocuments(t, svc, docs, 50, "seed wiki search docs")
	if err := svc.DB.Create(&db.WikiPageLabel{
		RepositoryID: repo.ID,
		Slug:         "boosted-winner",
		LabelID:      label.ID,
	}).Error; err != nil {
		t.Fatalf("create wiki label relation: %v", err)
	}

	resp, err := svc.SearchWikiPages(ctx, full, "semantic offset query", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if resp.Method != "vector" {
		t.Fatalf("method = %q, want vector", resp.Method)
	}
	if len(resp.Results) != 20 {
		t.Fatalf("len(results) = %d, want 20", len(resp.Results))
	}
	if resp.Results[0].Slug != "boosted-winner" {
		t.Fatalf("results[0].Slug = %q, want boosted-winner", resp.Results[0].Slug)
	}
}

func TestWikiSearchUpdateClearsStaleEmbeddingOnEmbedFailure(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{
		Embedder: semanticWikiEmbedder{},
	})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-stale-embedding",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-stale-embedding"

	page, err := svc.PutWikiPage(ctx, full, "ops/session-expiry", "# Sessions\n\nSession expiry depends on workspace policy.", "create sessions", "")
	if err != nil {
		t.Fatalf("PutWikiPage(create): %v", err)
	}
	svc.Wg.Wait()

	var stored db.WikiSearchDocument
	if err := svc.DB.Where("slug = ?", "ops/session-expiry").First(&stored).Error; err != nil {
		t.Fatalf("load search doc after create: %v", err)
	}
	if stored.Embedding == "" {
		t.Fatal("expected initial embedding to be stored")
	}

	svc.Embedder = &service.FakeEmbedder{Err: errors.New("embed failed")}
	if _, err := svc.PutWikiPage(ctx, full, "ops/session-expiry", "# Sessions\n\nRefresh tokens rotate automatically.", "update sessions", page.SHA); err != nil {
		t.Fatalf("PutWikiPage(update): %v", err)
	}
	svc.Wg.Wait()

	stored = db.WikiSearchDocument{}
	if err := svc.DB.Where("slug = ?", "ops/session-expiry").First(&stored).Error; err != nil {
		t.Fatalf("load search doc after failed re-embed: %v", err)
	}
	if stored.Embedding != "" {
		t.Fatalf("embedding = %q, want cleared on embed failure", stored.Embedding)
	}
	if !strings.Contains(string(stored.Body), "Refresh tokens rotate automatically.") {
		t.Fatalf("body = %q, want updated content", stored.Body)
	}
}

func TestWikiSearchUsesStoredTitleAndReindexRepairsStaleTitle(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-stale-title",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-stale-title"

	if _, err := svc.PutWikiPage(ctx, full, "guides/plain-page", "# Legacy Heading\n\nBody text.", "create page", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if err := svc.DB.Model(&db.WikiSearchDocument{}).
		Where("repository_id = ? AND slug = ?", repo.ID, "guides/plain-page").
		Update("title", "Legacy Heading").
		Error; err != nil {
		t.Fatalf("force stale title: %v", err)
	}

	resp, err := svc.SearchWikiPages(ctx, full, "plain page", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Slug != "guides/plain-page" {
		t.Fatalf("results = %#v, want guides/plain-page", resp.Results)
	}
	if resp.Results[0].Title != "Plain Page" {
		t.Fatalf("result title = %q, want slug-derived title without search-time writeback", resp.Results[0].Title)
	}

	var stored db.WikiSearchDocument
	if err := svc.DB.Where("repository_id = ? AND slug = ?", repo.ID, "guides/plain-page").First(&stored).Error; err != nil {
		t.Fatalf("reload stored doc: %v", err)
	}
	if stored.Title != "Legacy Heading" {
		t.Fatalf("stored title = %q, want search not to write back title normalization", stored.Title)
	}

	if _, err := svc.ReindexWikiSearch(ctx, full); err != nil {
		t.Fatalf("ReindexWikiSearch: %v", err)
	}
	stored = db.WikiSearchDocument{}
	if err := svc.DB.Where("repository_id = ? AND slug = ?", repo.ID, "guides/plain-page").First(&stored).Error; err != nil {
		t.Fatalf("reload stored doc after reindex: %v", err)
	}
	if stored.Title != "Plain Page" {
		t.Fatalf("stored title after reindex = %q, want Plain Page", stored.Title)
	}
}

func TestWikiSearchCurrentIndexLabelFilterPreservesOlderMatch(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "testuser", Name: "Test User", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "testuser", Name: "wiki-current-index-label-filter", AutoInit: true}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-current-index-label-filter"
	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	label := db.Label{RepositoryID: repo.ID, Name: "ops", Description: "Operations"}
	if err := svc.DB.Create(&label).Error; err != nil {
		t.Fatalf("seed label: %v", err)
	}

	body := []byte("# Runbook\n\nrunbook details")
	sha := wikicatalog.HashContent(body)
	baseTime := time.Date(2026, time.January, 8, 0, 0, 0, 0, time.UTC)
	pages := make([]db.WikiPage, 0, 1005)
	docs := make([]db.WikiSearchDocument, 0, 1005)
	for i := 0; i < 1005; i++ {
		slug := fmt.Sprintf("runbook-%04d", i)
		now := baseTime.Add(time.Duration(i) * time.Minute)
		pages = append(pages, db.WikiPage{
			RepositoryID:    repo.ID,
			Slug:            slug,
			Title:           fmt.Sprintf("Runbook %d", i),
			HeadBlobSHA:     sha,
			BodySize:        len(body),
			BodyInline:      body,
			HeadRevisionID:  uint64(i + 1),
			HeadChangesetID: uint64(i + 1),
			CreatedAt:       now,
			UpdatedAt:       now,
		})
		docs = append(docs, db.WikiSearchDocument{
			RepositoryID: repo.ID,
			Slug:         slug,
			Title:        fmt.Sprintf("Runbook %d", i),
			Body:         db.LargeText(body),
			RevisionSHA:  sha,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}
	if err := svc.DB.CreateInBatches(pages, 200).Error; err != nil {
		t.Fatalf("seed wiki pages: %v", err)
	}
	createWikiSearchDocuments(t, svc, docs, 200, "seed wiki search docs")
	if err := svc.DB.Create(&db.WikiPageLabel{RepositoryID: repo.ID, Slug: "runbook-0000", LabelID: label.ID}).Error; err != nil {
		t.Fatalf("seed wiki page label: %v", err)
	}

	resp, err := svc.SearchWikiPagesWithOptions(ctx, full, "runbook", service.WikiSearchOptions{
		Limit:  20,
		Labels: []string{"ops"},
	})
	if err != nil {
		t.Fatalf("SearchWikiPagesWithOptions: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(resp.Results))
	}
	if resp.Results[0].Slug != "runbook-0000" {
		t.Fatalf("results[0].Slug = %q, want runbook-0000", resp.Results[0].Slug)
	}
}

func TestWikiSearchCurrentIndexMultiTokenPreservesOlderIntersectionMatch(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "testuser", Name: "Test User", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "testuser", Name: "wiki-current-index-multi-token", AutoInit: true}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-current-index-multi-token"
	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	baseTime := time.Date(2026, time.January, 9, 0, 0, 0, 0, time.UTC)
	commonBody := []byte("# Runbook\n\nrunbook only")
	commonSHA := wikicatalog.HashContent(commonBody)
	pages := make([]db.WikiPage, 0, 1006)
	docs := make([]db.WikiSearchDocument, 0, 1006)
	for i := 0; i < 1005; i++ {
		slug := fmt.Sprintf("runbook-%04d", i)
		now := baseTime.Add(time.Duration(i) * time.Minute)
		pages = append(pages, db.WikiPage{
			RepositoryID:    repo.ID,
			Slug:            slug,
			Title:           fmt.Sprintf("Runbook %d", i),
			HeadBlobSHA:     commonSHA,
			BodySize:        len(commonBody),
			BodyInline:      commonBody,
			HeadRevisionID:  uint64(i + 1),
			HeadChangesetID: uint64(i + 1),
			CreatedAt:       now,
			UpdatedAt:       now,
		})
		docs = append(docs, db.WikiSearchDocument{
			RepositoryID: repo.ID,
			Slug:         slug,
			Title:        fmt.Sprintf("Runbook %d", i),
			Body:         db.LargeText(commonBody),
			RevisionSHA:  commonSHA,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}

	targetBody := []byte("# Runbook\n\nrunbook special-token")
	targetSHA := wikicatalog.HashContent(targetBody)
	targetTime := baseTime.Add(-time.Minute)
	pages = append(pages, db.WikiPage{
		RepositoryID:    repo.ID,
		Slug:            "runbook-special",
		Title:           "Runbook Special",
		HeadBlobSHA:     targetSHA,
		BodySize:        len(targetBody),
		BodyInline:      targetBody,
		HeadRevisionID:  5000,
		HeadChangesetID: 5000,
		CreatedAt:       targetTime,
		UpdatedAt:       targetTime,
	})
	docs = append(docs, db.WikiSearchDocument{
		RepositoryID: repo.ID,
		Slug:         "runbook-special",
		Title:        "Runbook Special",
		Body:         db.LargeText(targetBody),
		RevisionSHA:  targetSHA,
		CreatedAt:    targetTime,
		UpdatedAt:    targetTime,
	})
	if err := svc.DB.CreateInBatches(pages, 200).Error; err != nil {
		t.Fatalf("seed wiki pages: %v", err)
	}
	createWikiSearchDocuments(t, svc, docs, 200, "seed wiki search docs")

	resp, err := svc.SearchWikiPages(ctx, full, "runbook special-token", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(resp.Results))
	}
	if resp.Results[0].Slug != "runbook-special" {
		t.Fatalf("results[0].Slug = %q, want runbook-special", resp.Results[0].Slug)
	}
}
