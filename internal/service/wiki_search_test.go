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
