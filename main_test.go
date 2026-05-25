package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"gh-server/internal/config"
	"gh-server/internal/controlplane"
	"gh-server/internal/crypto"
	"gh-server/internal/embedding"
	"gh-server/internal/githttp"
	"gh-server/internal/gitstore"
	"gh-server/internal/graphql"
	"gh-server/internal/oauth"
	"gh-server/internal/rest"
	"gh-server/internal/router"
	"gh-server/internal/service"
)

func TestMain_SignalDrivenShutdown(t *testing.T) {
	setupBootstrapEnv(t, map[string]string{
		"BASE_URL": "http://localhost:0",
		"PORT":     "0",
	})

	sigCh := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(sigCh, ShutdownConfig{GracePeriod: 200 * time.Millisecond})
	}()

	sigCh <- syscall.SIGTERM

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for shutdown")
	}
}

// ============================================================================
// Shutdown Tests
// ============================================================================

func TestShutdown_Graceful_Success(t *testing.T) {
	// Create a minimal bootstrap deps for testing shutdown.
	mainDB := openTestDB(t)
	tmpDir := t.TempDir()

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatalf("gitstore: %v", err)
	}

	srvCtx, srvCancel := context.WithCancel(context.Background())
	svcDeps := &service.Service{
		Ctx: srvCtx,
		DB:  mainDB,
		Git: store,
		Wg:  sync.WaitGroup{},
	}

	// Create a test server that we can shutdown.
	testMux := http.NewServeMux()
	testMux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	testServer := &http.Server{Addr: ":0", Handler: testMux}

	deps := &BootstrapDeps{
		SrvCtx:    srvCtx,
		SrvCancel: srvCancel,
		SvcDeps:   svcDeps,
		Servers:   []*http.Server{testServer},
	}

	// Start the server.
	go func() {
		_ = testServer.ListenAndServe()
	}()

	// Give server time to start.
	time.Sleep(100 * time.Millisecond)

	// Shutdown with generous grace period.
	result := Shutdown(deps, ShutdownConfig{GracePeriod: 5 * time.Second})

	if len(result.HTTPShutdownErrors) > 0 {
		t.Errorf("expected no HTTP shutdown errors, got: %v", result.HTTPShutdownErrors)
	}
	if !result.BgDrained {
		t.Error("expected background goroutines to be drained")
	}
	if !result.ContextCanceled {
		t.Error("expected context to be canceled")
	}
}

func TestShutdown_BackgroundDrain_Timeout(t *testing.T) {
	mainDB := openTestDB(t)
	tmpDir := t.TempDir()

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatalf("gitstore: %v", err)
	}

	srvCtx, srvCancel := context.WithCancel(context.Background())
	svcDeps := &service.Service{
		Ctx: srvCtx,
		DB:  mainDB,
		Git: store,
		Wg:  sync.WaitGroup{},
	}

	// Simulate a background worker that never finishes.
	svcDeps.Wg.Add(1)
	go func() {
		defer svcDeps.Wg.Done()
		<-srvCtx.Done() // Only exits when context is canceled
	}()

	testMux := http.NewServeMux()
	testServer := &http.Server{Addr: ":0", Handler: testMux}

	deps := &BootstrapDeps{
		SrvCtx:    srvCtx,
		SrvCancel: srvCancel,
		SvcDeps:   svcDeps,
		Servers:   []*http.Server{testServer},
	}

	go func() {
		_ = testServer.ListenAndServe()
	}()

	time.Sleep(100 * time.Millisecond)

	// Shutdown with very short grace period to trigger timeout.
	result := Shutdown(deps, ShutdownConfig{GracePeriod: 100 * time.Millisecond})

	if !result.BgDrainTimedOut {
		t.Error("expected background drain to timeout")
	}
	if !result.ContextCanceled {
		t.Error("expected context to be canceled despite timeout")
	}
}

// ============================================================================
// Existing Readyz Tests (unchanged)
// ============================================================================

func TestReadyz_SingleDB_Healthy(t *testing.T) {
	mainDB := openTestDB(t)

	handler := readyzHandler(ReadyzConfig{
		MainDB: mainDB,
	})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "ready" {
		t.Errorf("expected status=ready, got %v", body["status"])
	}
	checks := body["checks"].(map[string]any)
	if _, ok := checks["control_plane_db"]; ok {
		t.Error("control_plane_db check should not be present in single-DB mode")
	}
}

func TestReadyz_WithControlPlane_BothHealthy(t *testing.T) {
	mainDB := openTestDB(t)
	cpDB := openTestDB(t)
	if err := cpDB.AutoMigrate(&controlplane.CPUser{}, &controlplane.CPToken{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	openTenant := func(dsn string) (*gorm.DB, error) { return openTestDB(t), nil }
	router := controlplane.NewDBRouter(cpDB, openTenant, true, controlplane.RouterConfig{MaxAgents: 10})
	defer router.Close()

	handler := readyzHandler(ReadyzConfig{
		MainDB:   mainDB,
		DBRouter: router,
	})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "ready" {
		t.Errorf("expected status=ready, got %v", body["status"])
	}
	checks := body["checks"].(map[string]any)
	cpCheck := checks["control_plane_db"].(map[string]any)
	if cpCheck["status"] != "ok" {
		t.Errorf("expected control_plane_db status=ok, got %v", cpCheck["status"])
	}
}

func TestReadyz_ControlPlaneDown_Returns503(t *testing.T) {
	mainDB := openTestDB(t)

	// Create a control-plane DB and then close it to simulate failure.
	cpDB := openTestDB(t)
	if err := cpDB.AutoMigrate(&controlplane.CPUser{}, &controlplane.CPToken{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	openTenant := func(dsn string) (*gorm.DB, error) { return openTestDB(t), nil }
	router := controlplane.NewDBRouter(cpDB, openTenant, true, controlplane.RouterConfig{MaxAgents: 10})
	defer router.Close()

	// Close the underlying control-plane SQL connection to simulate DB down.
	sqlDB, err := cpDB.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	sqlDB.Close()

	handler := readyzHandler(ReadyzConfig{
		MainDB:   mainDB,
		DBRouter: router,
	})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "not_ready" {
		t.Errorf("expected status=not_ready, got %v", body["status"])
	}
	checks := body["checks"].(map[string]any)
	cpCheck := checks["control_plane_db"].(map[string]any)
	if cpCheck["status"] != "unavailable" {
		t.Errorf("expected control_plane_db status=unavailable, got %v", cpCheck["status"])
	}
	// Main DB should still be ok
	mainCheck := checks["main_db"].(map[string]any)
	if mainCheck["status"] != "ok" {
		t.Errorf("expected main_db status=ok, got %v", mainCheck["status"])
	}
}

func TestReadyz_MainDBDown_Returns503(t *testing.T) {
	mainDB := openTestDB(t)
	// Close main DB to simulate failure.
	sqlDB, err := mainDB.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	sqlDB.Close()

	handler := readyzHandler(ReadyzConfig{
		MainDB: mainDB,
	})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "not_ready" {
		t.Errorf("expected status=not_ready, got %v", body["status"])
	}
}

func TestRouterComposition_ReadyzAfterRegisterRoutes(t *testing.T) {
	mainDB := openTestDB(t)
	tmpDir, err := os.MkdirTemp("", "main-router-test-")
	if err != nil {
		t.Fatalf("tmpdir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	gs, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatalf("gitstore: %v", err)
	}

	svc := &service.Service{DB: mainDB, Git: gs, BaseURL: "http://localhost:8080"}
	gqlSrv := graphql.NewServer(svc)
	restDeps := &rest.Deps{Svc: svc}
	gitHandler := githttp.New(gs, svc)
	oauthHandler := &oauth.Handler{Svc: svc}

	r := chi.NewRouter()
	mux := router.RegisterRoutes(r, restDeps, gitHandler, gqlSrv, oauthHandler, nil, "http://console.localhost")
	r.Get("/readyz", readyzHandler(ReadyzConfig{
		MainDB: mainDB,
	}))

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ============================================================================
// Bootstrap Helper Tests (Issue #857)
// ============================================================================

func TestBuildPartialDeps_NilInput(t *testing.T) {
	result := buildPartialDeps(nil)
	if result != nil {
		t.Errorf("expected nil result for nil input, got %v", result)
	}
}

func TestBuildPartialDeps_NonNilInput(t *testing.T) {
	mainDB := openTestDB(t)
	tmpDir := t.TempDir()

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatalf("gitstore: %v", err)
	}

	srvCtx, srvCancel := context.WithCancel(context.Background())
	svcDeps := &service.Service{
		Ctx: srvCtx,
		DB:  mainDB,
		Git: store,
	}

	input := &BootstrapDeps{
		Cfg:          config.Config{DBdsn: "test-dsn"},
		DB:           mainDB,
		Embedder:     embedding.NopEmbedder{},
		Store:        store,
		SrvCtx:       srvCtx,
		SrvCancel:    srvCancel,
		SvcDeps:      svcDeps,
		GqlSrv:       graphql.NewServer(svcDeps),
		GitHandler:   githttp.New(store, svcDeps),
		OauthHandler: &oauth.Handler{Svc: svcDeps},
		Handlers:     &rest.Deps{Svc: svcDeps},
		Mux:          http.NewServeMux(),
		Servers:      []*http.Server{{Addr: ":8080"}},
		Labels:       []string{"http://localhost:8080"},
	}

	result := buildPartialDeps(input)

	if result == nil {
		t.Fatal("expected non-nil result for non-nil input")
	}
	if result.Cfg.DBdsn != input.Cfg.DBdsn {
		t.Errorf("expected Cfg.DBdsn=%q, got %q", input.Cfg.DBdsn, result.Cfg.DBdsn)
	}
	if result.DB != input.DB {
		t.Error("expected DB to be copied")
	}
	if result.Embedder == nil {
		t.Error("expected Embedder to be copied")
	}
	if result.Store != input.Store {
		t.Error("expected Store to be copied")
	}
	if result.SrvCtx != input.SrvCtx {
		t.Error("expected SrvCtx to be copied")
	}
	if result.SrvCancel == nil && input.SrvCancel != nil {
		t.Error("expected SrvCancel to be copied")
	}
	if result.SvcDeps != input.SvcDeps {
		t.Error("expected SvcDeps to be copied")
	}
	if result.GqlSrv != input.GqlSrv {
		t.Error("expected GqlSrv to be copied")
	}
	if result.GitHandler != input.GitHandler {
		t.Error("expected GitHandler to be copied")
	}
	if result.OauthHandler != input.OauthHandler {
		t.Error("expected OauthHandler to be copied")
	}
	if result.Handlers != input.Handlers {
		t.Error("expected Handlers to be copied")
	}
	if result.Mux != input.Mux {
		t.Error("expected Mux to be copied")
	}
	if len(result.Servers) != len(input.Servers) {
		t.Error("expected Servers to be copied")
	}
	if len(result.Labels) != len(input.Labels) {
		t.Error("expected Labels to be copied")
	}
}

func TestInitServiceDeps_UsesConfiguredDataRootForWikiStorage(t *testing.T) {
	mainDB := openTestDB(t)
	dataRoot := t.TempDir()
	store, err := gitstore.New(dataRoot)
	if err != nil {
		t.Fatalf("gitstore: %v", err)
	}

	srvCtx, srvCancel := context.WithCancel(context.Background())
	defer srvCancel()

	deps, err := initServiceDeps(config.Config{
		BaseURL:    "http://localhost:8080",
		GitRepoDir: dataRoot,
	}, mainDB, store, embedding.NopEmbedder{}, srvCtx)
	if err != nil {
		t.Fatalf("initServiceDeps: %v", err)
	}
	if deps.svc.AttachmentRoot != dataRoot {
		t.Fatalf("AttachmentRoot = %q, want %q", deps.svc.AttachmentRoot, dataRoot)
	}
	if deps.svc.WikiBlob == nil {
		t.Fatal("WikiBlob should be configured")
	}
	if deps.svc.WikiBlob.Root() != dataRoot {
		t.Fatalf("WikiBlob root = %q, want %q", deps.svc.WikiBlob.Root(), dataRoot)
	}
}

func TestBootstrapResult_SetPartial(t *testing.T) {
	mainDB := openTestDB(t)
	tmpDir := t.TempDir()

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatalf("gitstore: %v", err)
	}

	srvCtx, srvCancel := context.WithCancel(context.Background())
	svcDeps := &service.Service{
		Ctx: srvCtx,
		DB:  mainDB,
		Git: store,
	}

	deps := &BootstrapDeps{
		Cfg:       config.Config{DBdsn: "test-dsn"},
		DB:        mainDB,
		Embedder:  embedding.NopEmbedder{},
		Store:     store,
		SrvCtx:    srvCtx,
		SrvCancel: srvCancel,
		SvcDeps:   svcDeps,
	}

	result := &BootstrapResult{
		Deps: deps,
		Err:  errors.New("test error"),
	}

	result.setPartial()

	if result.Partial == nil {
		t.Fatal("expected Partial to be set")
	}
	if result.Partial.Cfg.DBdsn != deps.Cfg.DBdsn {
		t.Errorf("expected Partial.Cfg.DBdsn=%q, got %q", deps.Cfg.DBdsn, result.Partial.Cfg.DBdsn)
	}
	if result.Partial.DB != deps.DB {
		t.Error("expected Partial.DB to match deps.DB")
	}
}

func TestControlPlaneGormConfig(t *testing.T) {
	cfg := controlPlaneGormConfig()

	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	// Verify logger is configured
	if cfg.Logger == nil {
		t.Fatal("expected Logger to be configured")
	}

	loggerWithConfig, ok := cfg.Logger.(interface{ Config() gormlogger.Config })
	if !ok {
		t.Fatal("logger should expose Config() for configuration inspection")
	}

	loggerCfg := loggerWithConfig.Config()
	if loggerCfg.LogLevel != gormlogger.Warn {
		t.Errorf("expected LogLevel=Warn (%d), got %d", gormlogger.Warn, loggerCfg.LogLevel)
	}
	if loggerCfg.Colorful {
		t.Error("expected Colorful=false")
	}
	if !loggerCfg.ParameterizedQueries {
		t.Error("expected ParameterizedQueries=true")
	}
	if !loggerCfg.IgnoreRecordNotFoundError {
		t.Error("expected IgnoreRecordNotFoundError=true")
	}
}

func TestOpenControlPlane_Failure_InvalidDSN(t *testing.T) {
	// Test that openControlPlane fails with an invalid DSN format.
	// Note: Testing success path requires a real MySQL server.
	_, err := openControlPlane("invalid://dsn-format-that-will-fail")
	if err == nil {
		t.Fatal("expected error with invalid DSN, got nil")
	}
}

func TestOpenControlPlaneTenantDB_EncryptedDSN(t *testing.T) {
	wantDSN := "root:@tcp(127.0.0.1:4000)/tenant_a?parseTime=true&timeout=10s"
	encryptedDSN, err := crypto.EncryptSecret(wantDSN)
	if err != nil {
		t.Fatalf("EncryptSecret() error = %v", err)
	}

	original := openControlPlaneDB
	t.Cleanup(func() {
		openControlPlaneDB = original
	})

	var gotDSN string
	openControlPlaneDB = func(dsn string) (*gorm.DB, error) {
		gotDSN = dsn
		return openTestDB(t), nil
	}

	if _, err := openControlPlaneTenantDB(encryptedDSN); err != nil {
		t.Fatalf("openControlPlaneTenantDB() error = %v", err)
	}
	if gotDSN != wantDSN {
		t.Fatalf("openControlPlaneTenantDB() opened %q, want %q", gotDSN, wantDSN)
	}
}

func TestOpenControlPlaneTenantDB_PlaintextDSNBackwardCompatible(t *testing.T) {
	wantDSN := "root:@tcp(127.0.0.1:4000)/tenant_b?parseTime=true&timeout=10s"

	original := openControlPlaneDB
	t.Cleanup(func() {
		openControlPlaneDB = original
	})

	var gotDSN string
	openControlPlaneDB = func(dsn string) (*gorm.DB, error) {
		gotDSN = dsn
		return openTestDB(t), nil
	}

	if _, err := openControlPlaneTenantDB(wantDSN); err != nil {
		t.Fatalf("openControlPlaneTenantDB() plaintext fallback error = %v", err)
	}
	if gotDSN != wantDSN {
		t.Fatalf("openControlPlaneTenantDB() opened %q, want %q", gotDSN, wantDSN)
	}
}

func TestOpenControlPlaneTenantDB_InvalidGarbageStillFails(t *testing.T) {
	original := openControlPlaneDB
	t.Cleanup(func() {
		openControlPlaneDB = original
	})

	openControlPlaneDB = func(dsn string) (*gorm.DB, error) {
		t.Fatalf("openControlPlaneDB should not be called for invalid input, got %q", dsn)
		return nil, nil
	}

	if _, err := openControlPlaneTenantDB("not-a-valid-encrypted-value!!!"); err == nil {
		t.Fatal("expected invalid garbage input to fail")
	}
}

// ============================================================================
// Main Entry Point Tests (Issue #857)
// ============================================================================

func TestMain_EntryPoint(t *testing.T) {
	// This test verifies that main() can be invoked and exits cleanly
	// when receiving a shutdown signal.

	// Set up environment for successful bootstrap
	setupBootstrapEnv(t, map[string]string{
		"DB_DSN":      "file:test_main_entry?mode=memory&cache=shared",
		"LISTEN_MODE": "production",
		"PORT":        "0",
	})

	// Create a channel to track when main exits
	exited := make(chan struct{}, 1)

	// Run main in a goroutine
	go func() {
		main()
		exited <- struct{}{}
	}()

	// Give main time to start servers
	time.Sleep(500 * time.Millisecond)

	// Send SIGTERM to the process to trigger shutdown
	// Note: This sends signal to the entire process, which will be received
	// by main's signal channel.
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("failed to find process: %v", err)
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to send SIGTERM: %v", err)
	}

	// Wait for main to exit
	select {
	case <-exited:
		// Success - main exited cleanly
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for main to exit")
	}
}
