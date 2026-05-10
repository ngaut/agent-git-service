package service_test

import (
	"context"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"gh-server/internal/service"
	"gh-server/internal/tenant"
)

func TestDBForCtx_WithContextDB(t *testing.T) {
	// Create two distinct SQLite DBs to verify the right one is returned.
	defaultDB, err := gorm.Open(sqlite.Open("file:dbforctx_default?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open default db: %v", err)
	}
	tenantDB, err := gorm.Open(sqlite.Open("file:dbforctx_tenant?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open tenant db: %v", err)
	}

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

// TestTenantContext_SingleKey guards the fix for the tenantKey split
// (issue #1244): service.ContextWithTenant and tenant.ContextWithTenant must
// write to the same underlying key so middleware (service.ContextWithTenant)
// and gitstore (tenant.FromContext) see the same value.
func TestTenantContext_SingleKey(t *testing.T) {
	ctx := service.ContextWithTenant(context.Background(), "tenant-foo")

	gotViaService, okService := service.TenantFromContext(ctx)
	if !okService || gotViaService != "tenant-foo" {
		t.Errorf("service.TenantFromContext: got (%q, %v), want (\"tenant-foo\", true)", gotViaService, okService)
	}

	gotViaTenant, okTenant := tenant.FromContext(ctx)
	if !okTenant || gotViaTenant != "tenant-foo" {
		t.Errorf("tenant.FromContext did not see service-package write: got (%q, %v), want (\"tenant-foo\", true)", gotViaTenant, okTenant)
	}

	// Symmetric: a tenant-package write must be visible via service.TenantFromContext.
	ctx2 := tenant.ContextWithTenant(context.Background(), "tenant-bar")
	gotViaService2, ok2 := service.TenantFromContext(ctx2)
	if !ok2 || gotViaService2 != "tenant-bar" {
		t.Errorf("service.TenantFromContext did not see tenant-package write: got (%q, %v), want (\"tenant-bar\", true)", gotViaService2, ok2)
	}
}
