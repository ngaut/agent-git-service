package service_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/embedding"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"

	sqlite3 "github.com/mattn/go-sqlite3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
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

func TestWikiSearchFallsBackToGitScanWhenIndexUnavailable(t *testing.T) {
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

	if _, err := svc.PutWikiPage(ctx, full, "ops/session-expiry", "# Sessions\n\nSession expiry depends on tenant policy.", "create sessions", ""); err != nil {
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

func TestWikiSearchSemanticUsesDatabaseVectorDistance(t *testing.T) {
	var vectorCalls int64
	driverName := fmt.Sprintf("sqlite3_wiki_vec_%d", time.Now().UnixNano())
	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return conn.RegisterFunc("VEC_COSINE_DISTANCE", func(embedding, query string) float64 {
				atomic.AddInt64(&vectorCalls, 1)
				if embedding == query {
					return 0
				}
				return 1
			}, true)
		},
	})

	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{
		Embedder: semanticWikiEmbedder{},
		OpenDB: func(dbPath string) (*gorm.DB, error) {
			return gorm.Open(sqlite.Dialector{DriverName: driverName, DSN: dbPath}, &gorm.Config{})
		},
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
		Name:       "wiki-db-vector",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-db-vector"

	if _, err := svc.PutWikiPage(ctx, full, "ops/session-expiry", "# Sessions\n\nSession expiry depends on tenant policy.", "create sessions", ""); err != nil {
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
	if got := atomic.LoadInt64(&vectorCalls); got < 2 {
		t.Fatalf("VEC_COSINE_DISTANCE calls = %d, want database vector path to run", got)
	}
}

func TestWikiSearchSemanticDBPaginationBeyondExactWindow(t *testing.T) {
	driverName := fmt.Sprintf("sqlite3_wiki_db_pagination_%d", time.Now().UnixNano())
	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return conn.RegisterFunc("VEC_COSINE_DISTANCE", func(embedding, query string) float64 {
				if embedding == query {
					return 0
				}
				return 1
			}, true)
		},
	})

	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{
		Embedder: semanticPaginationEmbedder{},
		OpenDB: func(dbPath string) (*gorm.DB, error) {
			return gorm.Open(sqlite.Dialector{DriverName: driverName, DSN: dbPath}, &gorm.Config{})
		},
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
	if err := svc.DB.CreateInBatches(docs, 200).Error; err != nil {
		t.Fatalf("seed wiki search docs: %v", err)
	}

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
	driverName := fmt.Sprintf("sqlite3_wiki_hybrid_vec_%d", time.Now().UnixNano())
	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return conn.RegisterFunc("VEC_COSINE_DISTANCE", func(embedding, query string) float64 {
				switch embedding {
				case "[1,0,0]":
					return 0.43
				case "[0,1,0]":
					return 0.625
				case "[0,0,1]":
					return 0.735
				default:
					return 1
				}
			}, true)
		},
	})

	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{
		Embedder: noisyWikiEmbedder{},
		OpenDB: func(dbPath string) (*gorm.DB, error) {
			return gorm.Open(sqlite.Dialector{DriverName: driverName, DSN: dbPath}, &gorm.Config{})
		},
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
	if err := svc.DB.Create(&docs).Error; err != nil {
		t.Fatalf("seed wiki search docs: %v", err)
	}

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
	if err := svc.DB.CreateInBatches(docs, 200).Error; err != nil {
		t.Fatalf("seed wiki search docs: %v", err)
	}

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
	if err := svc.DB.CreateInBatches(docs, 200).Error; err != nil {
		t.Fatalf("seed wiki search docs: %v", err)
	}

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
	driverName := fmt.Sprintf("sqlite3_wiki_db_label_pagination_%d", time.Now().UnixNano())
	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return conn.RegisterFunc("VEC_COSINE_DISTANCE", func(embedding, query string) float64 {
				switch embedding {
				case "[1,0,0]":
					return 0.01
				case "[0.95,0.05,0]":
					return 0.02
				case "[0.7,0.3,0]":
					return 0.03
				case "[0.69,0.31,0]":
					return 0.035
				case "[0.68,0.32,0]":
					return 0.04
				default:
					return 1
				}
			}, true)
		},
	})

	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{
		Embedder: semanticPaginationEmbedder{},
		OpenDB: func(dbPath string) (*gorm.DB, error) {
			return gorm.Open(sqlite.Dialector{DriverName: driverName, DSN: dbPath}, &gorm.Config{})
		},
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
	if err := svc.DB.Create(&docs).Error; err != nil {
		t.Fatalf("seed wiki search docs: %v", err)
	}
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
	driverName := fmt.Sprintf("sqlite3_wiki_db_label_boost_promotion_%d", time.Now().UnixNano())
	var vectorCalls int64
	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return conn.RegisterFunc("VEC_COSINE_DISTANCE", func(embedding, query string) float64 {
				atomic.AddInt64(&vectorCalls, 1)
				if embedding == query {
					return 0
				}
				if strings.HasPrefix(embedding, "[0.79,") {
					return 0.21
				}
				return 0.10
			}, true)
		},
	})

	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{
		Embedder: semanticPaginationEmbedder{},
		OpenDB: func(dbPath string) (*gorm.DB, error) {
			return gorm.Open(sqlite.Dialector{DriverName: driverName, DSN: dbPath}, &gorm.Config{})
		},
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
	if err := svc.DB.CreateInBatches(docs, 50).Error; err != nil {
		t.Fatalf("seed wiki search docs: %v", err)
	}
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
	if got := atomic.LoadInt64(&vectorCalls); got < 241 {
		t.Fatalf("VEC_COSINE_DISTANCE calls = %d, want the promoted winner to be ranked past the old 200-row prefix", got)
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

	page, err := svc.PutWikiPage(ctx, full, "ops/session-expiry", "# Sessions\n\nSession expiry depends on tenant policy.", "create sessions", "")
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

func TestWikiSearchSelfHealsStaleStoredTitlesFromSlug(t *testing.T) {
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
		t.Fatalf("result title = %q, want Plain Page", resp.Results[0].Title)
	}

	var stored db.WikiSearchDocument
	if err := svc.DB.Where("repository_id = ? AND slug = ?", repo.ID, "guides/plain-page").First(&stored).Error; err != nil {
		t.Fatalf("reload stored doc: %v", err)
	}
	if stored.Title != "Plain Page" {
		t.Fatalf("stored title = %q, want Plain Page", stored.Title)
	}
}
