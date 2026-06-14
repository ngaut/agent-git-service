package testharness

import (
	"os"
	"sync"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/embedding"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness/testdb"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
	"gorm.io/gorm"
)

var serviceSchemaTemplate struct {
	once sync.Once
	name string
	pool *testdb.SchemaPool
	err  error
}

// ServiceConfig tunes the bare-service fixture produced by NewService. A zero
// value yields an isolated migrated TiDB database, an isolated gitstore, and the
// NopEmbedder.
type ServiceConfig struct {
	// MaxOpenConns optionally overrides the default TiDB test connection pool
	// limit for tests that need stricter concurrency behavior.
	MaxOpenConns int

	// Embedder overrides the default NopEmbedder. Most tests want the default;
	// only tests exercising the embedding code path set this.
	Embedder embedding.Embedder
}

// NewService builds a bare *service.Service wired to an isolated TiDB database
// and an isolated gitstore under a temp directory. The
// returned cleanup func resets the database and removes the temp directory.
// A package-level template is migrated through db.Migrate once, then test
// databases are pooled and data-reset between tests.
//
// Callers that need the full HTTP surface should keep using testharness.New;
// this fixture is for service-layer tests that never touch the router.
func NewService(tb testing.TB, cfg ServiceConfig) (*service.Service, func()) {
	tb.Helper()

	tmpDir, err := os.MkdirTemp("", "gh-service-test-")
	if err != nil {
		tb.Fatalf("testharness: mkdir temp: %v", err)
	}

	gdb, dbCleanup := serviceTemplatePool(tb).Open(tb)
	sqlDB, err := gdb.DB()
	if err != nil {
		tb.Fatalf("testharness: get underlying sql.DB: %v", err)
	}
	if cfg.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
		sqlDB.SetMaxIdleConns(cfg.MaxOpenConns)
	}

	store, err := gitstore.New(tmpDir)
	if err != nil {
		tb.Fatalf("testharness: gitstore.New: %v", err)
	}

	embedder := cfg.Embedder
	if embedder == nil {
		embedder = embedding.NopEmbedder{}
	}

	wikiBlob := wikicatalog.NewBlobStore(tmpDir)
	wikiCat := wikicatalog.New(gdb, wikiBlob)
	// Mirror the production wiring so tests exercise the catalog's
	// context-aware DB resolution. Context-injected DBs (via
	// ContextWithDB) reach the catalog, not just the static gdb.

	svc := &service.Service{
		DB:             gdb,
		Git:            store,
		WikiCatalog:    wikiCat,
		WikiBlob:       wikiBlob,
		BaseURL:        "http://localhost:8080",
		AttachmentRoot: tmpDir,
		Embedder:       embedder,
	}
	wikiCat.DBFor = svc.DBForCtx
	// Mirror the production hook so writes through ApplyChangeSet
	// materialize onto the wiki git repo and feed the search index;
	// otherwise tests that PUT via REST and then read via the legacy
	// git path see 404s.
	wikiCat.OnChangeSetCommitted = svc.WikiCatalogPostCommit

	cleanup := func() {
		dbCleanup()
		_ = os.RemoveAll(tmpDir)
	}
	return svc, cleanup
}

// OpenServiceDB returns an isolated migrated TiDB database from the same schema
// pool used by NewService. It is for tests that need a tenant DB or bare DB
// without another gitstore-backed Service fixture.
func OpenServiceDB(tb testing.TB) (*gorm.DB, func()) {
	tb.Helper()
	return serviceTemplatePool(tb).Open(tb)
}

func serviceTemplatePool(tb testing.TB) *testdb.SchemaPool {
	tb.Helper()
	serviceSchemaTemplate.once.Do(func() {
		gdb, cleanup := testdb.OpenRaw(tb, "service_template")
		_ = cleanup
		if err := gdb.Raw("SELECT DATABASE()").Scan(&serviceSchemaTemplate.name).Error; err != nil {
			serviceSchemaTemplate.err = err
			return
		}
		if err := db.Migrate(gdb); err != nil {
			serviceSchemaTemplate.err = err
			return
		}
		db.InitVector(gdb, 3)
		serviceSchemaTemplate.pool = &testdb.SchemaPool{
			TemplateDB: serviceSchemaTemplate.name,
			Prefix:     "service",
		}
		if sqlDB, err := gdb.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if serviceSchemaTemplate.err != nil {
		tb.Fatalf("testharness: prepare service schema template: %v", serviceSchemaTemplate.err)
	}
	if serviceSchemaTemplate.name == "" {
		tb.Fatalf("testharness: service schema template has no database name")
	}
	if serviceSchemaTemplate.pool == nil {
		tb.Fatalf("testharness: service schema pool was not initialized")
	}
	return serviceSchemaTemplate.pool
}
