package service_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/service"
	"gh-server/internal/testharness"

	sqlite3 "github.com/mattn/go-sqlite3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type semanticWikiEmbedder struct{}

var wikiSearchSQLiteSeq uint64

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

	if err := svc.DB.Model(&db.WikiSearchDocument{}).Where("slug = ?", "ops/session-expiry").Update("embedding", nil).Error; err != nil {
		t.Fatalf("clear embedding for auto reindex: %v", err)
	}

	svc.QueueWikiSearchAutoReindex()
	svc.Wg.Wait()

	var stored db.WikiSearchDocument
	if err := svc.DB.Where("slug = ?", "ops/session-expiry").First(&stored).Error; err != nil {
		t.Fatalf("load search doc after auto reindex refill: %v", err)
	}
	if stored.Embedding == "" {
		t.Fatal("expected auto reindex to refill embeddings without database vector distance support")
	}

	if err := svc.DB.Where("repository_id > 0").Delete(&db.WikiSearchDocument{}).Error; err != nil {
		t.Fatalf("clear search docs for auto reindex recreation: %v", err)
	}

	svc.QueueWikiSearchAutoReindex()
	svc.Wg.Wait()

	stored = db.WikiSearchDocument{}
	if err := svc.DB.Where("slug = ?", "ops/session-expiry").First(&stored).Error; err != nil {
		t.Fatalf("load search doc after auto reindex recreate: %v", err)
	}
	if stored.Embedding == "" {
		t.Fatal("expected auto reindex to recreate wiki search docs without database vector distance support")
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

func TestWikiSearchAutoReindexFillsMissingEmbeddings(t *testing.T) {
	driverName := fmt.Sprintf("sqlite3_wiki_auto_vec_%d", time.Now().UnixNano())
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
		Name:       "wiki-auto-reindex",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-auto-reindex"
	if _, err := svc.PutWikiPage(ctx, full, "ops/session-expiry", "# Sessions\n\nSession expiry depends on tenant policy.", "create sessions", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	if err := svc.DB.Model(&db.WikiSearchDocument{}).Where("slug = ?", "ops/session-expiry").Update("embedding", nil).Error; err != nil {
		t.Fatalf("clear embedding: %v", err)
	}

	svc.QueueWikiSearchAutoReindex()
	svc.Wg.Wait()

	var stored db.WikiSearchDocument
	if err := svc.DB.Where("slug = ?", "ops/session-expiry").First(&stored).Error; err != nil {
		t.Fatalf("load search doc: %v", err)
	}
	if stored.Embedding == "" {
		t.Fatal("expected auto reindex to refill wiki search embedding")
	}

	if err := svc.DB.Where("repository_id > 0").Delete(&db.WikiSearchDocument{}).Error; err != nil {
		t.Fatalf("delete search docs: %v", err)
	}
	svc.QueueWikiSearchAutoReindex()
	svc.Wg.Wait()

	stored = db.WikiSearchDocument{}
	if err := svc.DB.Where("slug = ?", "ops/session-expiry").First(&stored).Error; err != nil {
		t.Fatalf("load recreated search doc: %v", err)
	}
	if stored.Embedding == "" {
		t.Fatal("expected auto reindex to recreate empty wiki search index")
	}
}

func TestWikiSearchAutoReindexRepairsLegacyEmptyEmbeddings(t *testing.T) {
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
		Name:       "wiki-auto-reindex-legacy-empty",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-auto-reindex-legacy-empty"
	if _, err := svc.PutWikiPage(ctx, full, "ops/session-expiry", "# Sessions\n\nSession expiry depends on tenant policy.", "create sessions", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	if err := svc.DB.Model(&db.WikiSearchDocument{}).Where("slug = ?", "ops/session-expiry").Update("embedding", "").Error; err != nil {
		t.Fatalf("clear legacy embedding: %v", err)
	}

	svc.QueueWikiSearchAutoReindex()
	svc.Wg.Wait()

	var stored db.WikiSearchDocument
	if err := svc.DB.Where("slug = ?", "ops/session-expiry").First(&stored).Error; err != nil {
		t.Fatalf("load search doc: %v", err)
	}
	if stored.Embedding == "" {
		t.Fatal("expected auto reindex to refill legacy empty wiki search embedding")
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

func TestWikiSearchAutoReindexIncludesTenantDBs(t *testing.T) {
	driverName := fmt.Sprintf("sqlite3_wiki_auto_reindex_tenant_%d", atomic.AddUint64(&wikiSearchSQLiteSeq, 1))
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
		Embedder: semanticWikiEmbedder{},
		OpenDB: func(dbPath string) (*gorm.DB, error) {
			return gorm.Open(sqlite.Dialector{DriverName: driverName, DSN: dbPath}, &gorm.Config{})
		},
	})
	defer cleanup()

	tenantDB, err := gorm.Open(sqlite.Dialector{
		DriverName: driverName,
		DSN:        fmt.Sprintf("file:wiki_auto_reindex_tenant_%d?mode=memory&cache=shared", atomic.AddUint64(&wikiSearchSQLiteSeq, 1)),
	}, &gorm.Config{})
	if err != nil {
		t.Fatalf("open tenant db: %v", err)
	}
	if err := db.Migrate(tenantDB); err != nil {
		t.Fatalf("migrate tenant db: %v", err)
	}

	svc.TenantContexts = func(context.Context) ([]context.Context, error) {
		tenantCtx := service.ContextWithTenant(context.Background(), "tenantuser")
		tenantCtx = service.ContextWithDB(tenantCtx, tenantDB)
		return []context.Context{tenantCtx}, nil
	}

	ctx := service.ContextWithTenant(context.Background(), "tenantuser")
	ctx = service.ContextWithDB(ctx, tenantDB)
	if err := tenantDB.Create(&db.User{
		Login: "tenantuser",
		Name:  "Tenant User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed tenant owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "tenantuser",
		Name:       "wiki-auto-reindex-tenant",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo(tenant): %v", err)
	}
	full := "tenantuser/wiki-auto-reindex-tenant"
	if _, err := svc.PutWikiPage(ctx, full, "ops/session-expiry", "# Sessions\n\nSession expiry depends on tenant policy.", "create sessions", ""); err != nil {
		t.Fatalf("PutWikiPage(tenant): %v", err)
	}
	svc.Wg.Wait()

	if err := tenantDB.Model(&db.WikiSearchDocument{}).Where("slug = ?", "ops/session-expiry").Update("embedding", nil).Error; err != nil {
		t.Fatalf("clear tenant embedding: %v", err)
	}

	svc.QueueWikiSearchAutoReindex()
	svc.Wg.Wait()

	var stored db.WikiSearchDocument
	if err := tenantDB.Where("slug = ?", "ops/session-expiry").First(&stored).Error; err != nil {
		t.Fatalf("load tenant search doc: %v", err)
	}
	if stored.Embedding == "" {
		t.Fatal("expected auto reindex to refill tenant wiki search embedding")
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
