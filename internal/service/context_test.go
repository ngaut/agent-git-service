package service_test

import (
	"context"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/service"
)

func TestDBForCtx_WithContextDB(t *testing.T) {
	// Create two distinct SQLite DBs to verify the right one is returned.
	defaultDB, err := gorm.Open(sqlite.Open("file:dbforctx_default?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open default db: %v", err)
	}
	scopedDB, err := gorm.Open(sqlite.Open("file:dbforctx_scoped?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open scoped db: %v", err)
	}

	svc := &service.Service{DB: defaultDB}

	// Inject scoped DB into context.
	ctx := service.ContextWithDB(context.Background(), scopedDB)
	got := svc.DBForCtx(ctx)

	// The returned DB should be derived from scopedDB, not defaultDB.
	// We verify by checking the underlying sql.DB pointer.
	gotSQL, _ := got.DB()
	scopedSQL, _ := scopedDB.DB()
	defaultSQL, _ := defaultDB.DB()

	if gotSQL != scopedSQL {
		t.Error("DBForCtx did not return the context-injected DB")
	}
	if gotSQL == defaultSQL {
		t.Error("DBForCtx returned the default DB instead of the context DB")
	}
}

func TestDBForCtx_FallsBackToServiceDB(t *testing.T) {
	defaultDB, err := gorm.Open(sqlite.Open("file:dbforctx_fallback?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	svc := &service.Service{DB: defaultDB}

	// No context DB injected — should fall back to s.DB
	got := svc.DBForCtx(context.Background())
	gotSQL, _ := got.DB()
	defaultSQL, _ := defaultDB.DB()

	if gotSQL != defaultSQL {
		t.Error("DBForCtx did not fall back to s.DB when no context DB was set")
	}
}

func TestContextWithDB_RoundTrip(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:ctx_roundtrip?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	ctx := service.ContextWithDB(context.Background(), db)
	got, ok := service.DBFromContext(ctx)
	if !ok {
		t.Fatal("DBFromContext returned false")
	}
	if got != db {
		t.Error("DBFromContext returned a different DB than what was injected")
	}
}

func TestDBFromContext_Empty(t *testing.T) {
	_, ok := service.DBFromContext(context.Background())
	if ok {
		t.Error("DBFromContext should return false for empty context")
	}
}
