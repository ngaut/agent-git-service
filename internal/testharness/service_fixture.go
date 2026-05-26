package testharness

import (
	"os"
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"gh-server/internal/db"
	"gh-server/internal/embedding"
	"gh-server/internal/gitstore"
	"gh-server/internal/service"
	"gh-server/internal/wikicatalog"
)

// ServiceConfig tunes the bare-service fixture produced by NewService. A zero
// value yields the same defaults that the current service-package test files
// use: file-backed SQLite with WAL, no foreign-key enforcement, no connection
// cap, and the NopEmbedder. Set fields to opt into stricter modes.
type ServiceConfig struct {
	// ForeignKeys enables PRAGMA foreign_keys=ON. Required for tests that
	// assert cascade-delete behaviour; default is off because SQLite's default
	// behaviour matches what TiDB does on delete.
	ForeignKeys bool

	// MaxOpenConns pins the SQL pool to at most N connections. Tests using
	// foreign_keys=ON typically set this to 1 to avoid locking surprises.
	// Zero means unlimited.
	MaxOpenConns int

	// Embedder overrides the default NopEmbedder. Most tests want the default;
	// only tests exercising the embedding code path set this.
	Embedder embedding.Embedder

	// OpenDB overrides how the underlying SQLite connection is opened. Used
	// by tests that need a custom driver (e.g. a vector-extension shim).
	// When nil, the default sqlite.Open(dbPath) is used.
	OpenDB func(dbPath string) (*gorm.DB, error)
}

// NewService builds a bare *service.Service wired to an isolated SQLite
// database under tb.TempDir() and an isolated gitstore in the same dir. The
// returned cleanup func closes the database and removes the temp directory.
// Migrations are run through db.Migrate so the test schema matches production.
//
// Callers that need the full HTTP surface should keep using testharness.New;
// this fixture is for service-layer tests that never touch the router.
func NewService(tb testing.TB, cfg ServiceConfig) (*service.Service, func()) {
	tb.Helper()

	tmpDir, err := os.MkdirTemp("", "gh-service-test-")
	if err != nil {
		tb.Fatalf("testharness: mkdir temp: %v", err)
	}
	dbPath := filepath.Join(tmpDir, "test.sqlite")

	open := cfg.OpenDB
	if open == nil {
		open = func(p string) (*gorm.DB, error) { return gorm.Open(sqlite.Open(p), &gorm.Config{}) }
	}
	gdb, err := open(dbPath)
	if err != nil {
		tb.Fatalf("testharness: open sqlite: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		tb.Fatalf("testharness: get underlying sql.DB: %v", err)
	}
	if cfg.ForeignKeys {
		if err := gdb.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
			tb.Fatalf("testharness: foreign_keys=ON: %v", err)
		}
	}
	if err := gdb.Exec("PRAGMA busy_timeout = 5000").Error; err != nil {
		tb.Fatalf("testharness: busy_timeout: %v", err)
	}
	if err := gdb.Exec("PRAGMA journal_mode = WAL").Error; err != nil {
		tb.Fatalf("testharness: journal_mode: %v", err)
	}

	if err := db.Migrate(gdb); err != nil {
		tb.Fatalf("testharness: migrate: %v", err)
	}

	// Apply MaxOpenConns AFTER migrations — db.Migrate runs PRAGMA queries that
	// can open a second connection transiently, which would deadlock under
	// MaxOpenConns=1.
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
	// context-aware DB resolution. Tenant-injected DBs (via
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
		_ = sqlDB.Close()
		_ = os.RemoveAll(tmpDir)
	}
	return svc, cleanup
}
