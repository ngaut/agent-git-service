package service_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/embedding"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

// TestDBForCtxUsesScopedDB verifies that DBForCtx returns the scoped DB
// when one is injected into the context.
func TestDBForCtxUsesScopedDB(t *testing.T) {
	// Create two distinct SQLite DBs to verify the right one is returned.
	defaultDB, err := gorm.Open(sqlite.Open("file:scoped_db_default?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open default db: %v", err)
	}

	scopedDB, err := gorm.Open(sqlite.Open("file:scoped_db_override?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open scoped db: %v", err)
	}

	svc := &service.Service{DB: defaultDB}

	// Inject scoped DB into context
	ctx := service.ContextWithDB(context.Background(), scopedDB)

	// Verify DBForCtx returns the scoped DB
	got := svc.DBForCtx(ctx)

	// The returned DB should be derived from scopedDB, not defaultDB.
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

func TestWikiBackgroundMigrationStateUsesContextDB_Issue1448(t *testing.T) {
	defaultDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "default.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open default db: %v", err)
	}
	scopedDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "scoped.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open scoped db: %v", err)
	}
	for _, gdb := range []*gorm.DB{defaultDB, scopedDB} {
		if err := db.Migrate(gdb); err != nil {
			t.Fatalf("migrate db: %v", err)
		}
	}

	store, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	blobStore := wikicatalog.NewBlobStore(t.TempDir())
	wikiCat := wikicatalog.New(defaultDB, blobStore)
	svc := &service.Service{
		Ctx:            context.Background(),
		DB:             defaultDB,
		Git:            store,
		WikiCatalog:    wikiCat,
		WikiBlob:       blobStore,
		AttachmentRoot: t.TempDir(),
		BaseURL:        "http://localhost:8080",
		Embedder:       embedding.NopEmbedder{},
	}
	wikiCat.DBFor = svc.DBForCtx
	wikiCat.OnChangeSetCommitted = svc.WikiCatalogPostCommit

	defaultCtx := context.Background()
	scopedCtx := service.ContextWithDB(context.Background(), scopedDB)
	for _, tc := range []struct {
		ctx  context.Context
		gdb  *gorm.DB
		user string
	}{
		{ctx: defaultCtx, gdb: defaultDB, user: "shared-owner"},
		{ctx: scopedCtx, gdb: scopedDB, user: "shared-owner"},
	} {
		owner := db.User{Login: tc.user, Name: tc.user, Type: db.TypeUser}
		if err := tc.gdb.Create(&owner).Error; err != nil {
			t.Fatalf("create %s owner: %v", tc.user, err)
		}
		if _, err := svc.CreateRepo(tc.ctx, service.CreateRepoInput{OwnerLogin: owner.Login, Name: "shared-name", AutoInit: true}); err != nil {
			t.Fatalf("CreateRepo %s: %v", tc.user, err)
		}
		repoFullName := owner.Login + "/shared-name"
		if err := tc.gdb.Model(&db.Repository{}).Where("full_name = ?", repoFullName).Update("has_wiki", true).Error; err != nil {
			t.Fatalf("set has_wiki for %s: %v", repoFullName, err)
		}
	}

	defaultRepo := "shared-owner/shared-name"
	scopedRepo := "shared-owner/shared-name"
	if !svc.ClaimWikiBackgroundMigrationForTest(defaultCtx, defaultRepo) {
		t.Fatal("expected to claim default DB migration slot")
	}
	defer svc.ReleaseWikiBackgroundMigrationForTest(defaultCtx, defaultRepo)

	if !svc.IsWikiBackgroundMigrationRunning(defaultCtx, defaultRepo) {
		t.Fatal("expected default DB migration state to be visible in default context")
	}
	if svc.IsWikiBackgroundMigrationRunning(scopedCtx, scopedRepo) {
		t.Fatal("context-scoped migration state leaked from default DB into scoped DB")
	}
}

// TestDBForCtxFallsBackToDefault verifies that DBForCtx falls back to
// the default DB when no scoped DB is in the context.
func TestDBForCtxFallsBackToDefault(t *testing.T) {
	defaultDB, err := gorm.Open(sqlite.Open("file:scoped_db_fallback?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	svc := &service.Service{DB: defaultDB}

	// Create context WITHOUT scoped DB
	ctx := context.Background()

	// Verify DBForCtx falls back to default DB
	got := svc.DBForCtx(ctx)
	gotSQL, _ := got.DB()
	defaultSQL, _ := defaultDB.DB()

	if gotSQL != defaultSQL {
		t.Error("DBForCtx did not fall back to s.DB when no context DB was set")
	}
}

// TestBackgroundGoroutinePropagatesScopedDB verifies that background
// goroutines properly propagate scoped DB context.
func TestBackgroundGoroutinePropagatesScopedDB(t *testing.T) {
	defaultDB, err := gorm.Open(sqlite.Open("file:scoped_db_bg_default?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open default db: %v", err)
	}

	scopedDB, err := gorm.Open(sqlite.Open("file:scoped_db_bg_override?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open scoped db: %v", err)
	}

	svc := &service.Service{
		DB:  defaultDB,
		Ctx: context.Background(),
	}

	// Track which DB was used in the background goroutine
	var bgDB *gorm.DB
	done := make(chan struct{})

	// Simulate the pattern used in embedding_hook.go and workflow_dispatch.go
	requestCtx := service.ContextWithDB(context.Background(), scopedDB)
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

	// Verify the background goroutine used the scoped DB, not the default
	bgSQL, _ := bgDB.DB()
	scopedSQL, _ := scopedDB.DB()
	defaultSQL, _ := defaultDB.DB()

	if bgSQL != scopedSQL {
		t.Error("Background goroutine did not use the scoped DB")
	}
	if bgSQL == defaultSQL {
		t.Error("Background goroutine used the default DB instead of scoped DB")
	}
}

// TestContextHelpers verifies that context helper functions work correctly.
func TestContextHelpers(t *testing.T) {
	testDB, err := gorm.Open(sqlite.Open("file:scoped_db_helpers?mode=memory&cache=shared"), &gorm.Config{})
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

func TestKickBackgroundWikiMigration_UsesCallerDBContext_Issue1448(t *testing.T) {
	defaultDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "default.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open default db: %v", err)
	}
	scopedDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "scoped.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open scoped db: %v", err)
	}
	for _, gdb := range []*gorm.DB{defaultDB, scopedDB} {
		if err := db.Migrate(gdb); err != nil {
			t.Fatalf("migrate db: %v", err)
		}
	}

	store, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	blobStore := wikicatalog.NewBlobStore(t.TempDir())
	wikiCat := wikicatalog.New(defaultDB, blobStore)
	svc := &service.Service{
		Ctx:            context.Background(),
		DB:             defaultDB,
		Git:            store,
		WikiCatalog:    wikiCat,
		WikiBlob:       blobStore,
		AttachmentRoot: t.TempDir(),
		BaseURL:        "http://localhost:8080",
		Embedder:       embedding.NopEmbedder{},
	}
	wikiCat.DBFor = svc.DBForCtx
	wikiCat.OnChangeSetCommitted = svc.WikiCatalogPostCommit

	scopedCtx := service.ContextWithDB(context.Background(), scopedDB)
	owner := db.User{Login: "scoped-owner", Name: "scoped-owner", Type: db.TypeUser}
	if err := scopedDB.Create(&owner).Error; err != nil {
		t.Fatalf("create scoped owner: %v", err)
	}
	if _, err := svc.CreateRepo(scopedCtx, service.CreateRepoInput{OwnerLogin: owner.Login, Name: "scoped-wiki", AutoInit: true}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	repoFullName := owner.Login + "/scoped-wiki"
	if err := scopedDB.Model(&db.Repository{}).Where("full_name = ?", repoFullName).Update("has_wiki", true).Error; err != nil {
		t.Fatalf("set has_wiki: %v", err)
	}
	if _, err := svc.PutWikiPage(scopedCtx, repoFullName, "home", "v1", "create home", ""); err != nil {
		t.Fatalf("PutWikiPage home: %v", err)
	}
	if _, err := svc.Git.WriteFile(context.Background(), repoFullName+".wiki", "master", "about.md", "add about", []byte("about body")); err != nil {
		t.Fatalf("git write about: %v", err)
	}

	started := make(chan struct{}, 1)
	svc.SetWikiBackgroundMigrationStartedHookForTest(func(fullName string) {
		if fullName == repoFullName {
			started <- struct{}{}
		}
	})
	defer svc.SetWikiBackgroundMigrationStartedHookForTest(nil)

	svc.KickBackgroundWikiMigration(scopedCtx, repoFullName)

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for background migration to start")
	}
	svc.Wg.Wait()

	pages, err := svc.ListWikiPages(scopedCtx, repoFullName, service.ListWikiPagesOptions{Recursive: true})
	if err != nil {
		t.Fatalf("ListWikiPages after background migration: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("pages = %+v, want 2 pages after background migration", pages)
	}
}
