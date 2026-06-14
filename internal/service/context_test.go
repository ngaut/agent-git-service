package service_test

import (
	"context"
	"testing"

	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness/testdb"
)

func TestDBForCtx_WithContextDB(t *testing.T) {
	defaultDB, defaultCleanup := testdb.OpenRaw(t, "ctx_default")
	t.Cleanup(defaultCleanup)
	tenantDB, tenantCleanup := testdb.OpenRaw(t, "ctx_tenant")
	t.Cleanup(tenantCleanup)

	svc := &service.Service{DB: defaultDB}

	// Inject tenant DB into context
	ctx := service.ContextWithDB(context.Background(), tenantDB)
	got := svc.DBForCtx(ctx)

	// The returned DB should be derived from tenantDB, not defaultDB.
	// We verify by checking the underlying sql.DB pointer.
	gotSQL, _ := got.DB()
	tenantSQL, _ := tenantDB.DB()
	defaultSQL, _ := defaultDB.DB()

	if gotSQL != tenantSQL {
		t.Error("DBForCtx did not return the context-injected DB")
	}
	if gotSQL == defaultSQL {
		t.Error("DBForCtx returned the default DB instead of the context DB")
	}
}

func TestDBForCtx_FallsBackToServiceDB(t *testing.T) {
	defaultDB, cleanup := testdb.OpenRaw(t, "ctx_fallback")
	t.Cleanup(cleanup)

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
	db, cleanup := testdb.OpenRaw(t, "ctx_roundtrip")
	t.Cleanup(cleanup)

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
