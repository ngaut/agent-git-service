package service_test

import (
	"context"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"gh-server/internal/service"
)

// TestDBForCtxUsesTenantDB verifies that DBForCtx returns the tenant DB
// when one is injected into the context.
func TestDBForCtxUsesTenantDB(t *testing.T) {
	// Create two distinct SQLite DBs to verify the right one is returned.
	defaultDB, err := gorm.Open(sqlite.Open("file:tenant_db_default?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open default db: %v", err)
	}

	tenantDB, err := gorm.Open(sqlite.Open("file:tenant_db_tenant?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open tenant db: %v", err)
	}

	svc := &service.Service{DB: defaultDB}

	// Inject tenant DB into context
	ctx := service.ContextWithDB(context.Background(), tenantDB)

	// Verify DBForCtx returns the tenant DB
	got := svc.DBForCtx(ctx)

	// The returned DB should be derived from tenantDB, not defaultDB.
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

// TestDBForCtxFallsBackToDefault verifies that DBForCtx falls back to
// the default DB when no tenant DB is in the context.
func TestDBForCtxFallsBackToDefault(t *testing.T) {
	defaultDB, err := gorm.Open(sqlite.Open("file:tenant_db_fallback?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	svc := &service.Service{DB: defaultDB}

	// Create context WITHOUT tenant DB
	ctx := context.Background()

	// Verify DBForCtx falls back to default DB
	got := svc.DBForCtx(ctx)
	gotSQL, _ := got.DB()
	defaultSQL, _ := defaultDB.DB()

	if gotSQL != defaultSQL {
		t.Error("DBForCtx did not fall back to s.DB when no context DB was set")
	}
}

// TestBackgroundGoroutinePropagatesTenantDB verifies that background
// goroutines properly propagate tenant DB context.
func TestBackgroundGoroutinePropagatesTenantDB(t *testing.T) {
	defaultDB, err := gorm.Open(sqlite.Open("file:tenant_db_bg_default?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open default db: %v", err)
	}

	tenantDB, err := gorm.Open(sqlite.Open("file:tenant_db_bg_tenant?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open tenant db: %v", err)
	}

	svc := &service.Service{
		DB:  defaultDB,
		Ctx: context.Background(),
	}

	// Track which DB was used in the background goroutine
	var bgDB *gorm.DB
	done := make(chan struct{})

	// Simulate the pattern used in embedding_hook.go and workflow_dispatch.go
	requestCtx := service.ContextWithDB(context.Background(), tenantDB)
	bgCtx := svc.ServerCtx()
	if db, ok := service.DBFromContext(requestCtx); ok {
		bgCtx = service.ContextWithDB(bgCtx, db)
	}

	svc.Wg.Add(1)
	go func() {
		defer svc.Wg.Done()
		bgDB = svc.DBForCtx(bgCtx)
		close(done)
	}()

	<-done

	if bgDB == nil {
		t.Fatal("Background goroutine did not use any DB")
	}

	// Verify the background goroutine used the tenant DB, not the default
	bgSQL, _ := bgDB.DB()
	tenantSQL, _ := tenantDB.DB()
	defaultSQL, _ := defaultDB.DB()

	if bgSQL != tenantSQL {
		t.Error("Background goroutine did not use the tenant DB")
	}
	if bgSQL == defaultSQL {
		t.Error("Background goroutine used the default DB instead of tenant DB")
	}
}

// TestContextHelpers verifies that context helper functions work correctly.
func TestContextHelpers(t *testing.T) {
	testDB, err := gorm.Open(sqlite.Open("file:tenant_db_helpers?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	// Test ContextWithDB and DBFromContext
	ctx := service.ContextWithDB(context.Background(), testDB)

	retrievedDB, ok := service.DBFromContext(ctx)
	if !ok {
		t.Fatal("DBFromContext returned ok=false for context with DB")
	}

	if retrievedDB != testDB {
		t.Fatal("DBFromContext returned a different DB than what was injected")
	}

	// Test with context without DB
	emptyCtx := context.Background()
	_, ok = service.DBFromContext(emptyCtx)
	if ok {
		t.Fatal("DBFromContext returned ok=true for context without DB")
	}
}
