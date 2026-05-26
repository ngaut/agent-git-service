package server

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var testDBCounter atomic.Int64

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:readyz_%d?mode=memory&cache=shared", testDBCounter.Add(1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

func overrideControlPlaneHooks(t *testing.T, openCP func(string) (*gorm.DB, error), openTenant func(string) (*gorm.DB, error), migrate func(*gorm.DB) error) {
	t.Helper()
	origOpenCP := openControlPlaneDB
	origOpenTenant := openControlPlaneTenantDB
	origMigrate := controlPlaneAutoMigrate

	if openCP != nil {
		openControlPlaneDB = openCP
	}
	if openTenant != nil {
		openControlPlaneTenantDB = openTenant
	}
	if migrate != nil {
		controlPlaneAutoMigrate = migrate
	}

	t.Cleanup(func() {
		openControlPlaneDB = origOpenCP
		openControlPlaneTenantDB = origOpenTenant
		controlPlaneAutoMigrate = origMigrate
	})
}

// setupBootstrapEnv sets up standard bootstrap environment variables for tests.
// overrides allows specific env vars to be set to custom values.
func setupBootstrapEnv(t *testing.T, overrides map[string]string) {
	t.Helper()

	defaults := map[string]string{
		"DB_DSN":       "file:test?mode=memory&cache=shared",
		"ADMIN_LOGIN":  "admin",
		"ADMIN_TOKEN":  "token",
		"GIT_REPO_DIR": t.TempDir(),
		"BASE_URL":     "http://localhost:8080",
		"PORT":         "8080",
		"LISTEN_MODE":  "production",
		"ENVIRONMENT":  "development",
	}

	for k, v := range overrides {
		defaults[k] = v
	}

	for k, v := range defaults {
		os.Setenv(k, v)
	}

	t.Cleanup(func() {
		for k := range defaults {
			os.Unsetenv(k)
		}
	})
}

// ============================================================================
// Bootstrap Tests
// ============================================================================

func TestBootstrap_Success_Minimal(t *testing.T) {
	// Set up minimal required environment variables.
	setupBootstrapEnv(t, map[string]string{
		"DB_DSN": "file:test_bootstrap_success?mode=memory&cache=shared",
	})

	result := bootstrap()
	if result.Err != nil {
		t.Fatalf("bootstrap failed: %v", result.Err)
	}
	if result.Deps == nil {
		t.Fatal("bootstrap deps is nil")
	}
	if result.Partial != nil {
		t.Fatal("partial should be nil on success")
	}

	// Verify key dependencies are initialized.
	if result.Deps.Cfg.DBdsn == "" {
		t.Error("config should be initialized")
	}
	if result.Deps.DB == nil {
		t.Error("database should be initialized")
	}
	if result.Deps.Store == nil {
		t.Error("gitstore should be initialized")
	}
	if result.Deps.SvcDeps == nil {
		t.Error("service deps should be initialized")
	}
	if result.Deps.Mux == nil {
		t.Error("mux should be initialized")
	}
	if result.Deps.Servers == nil || len(result.Deps.Servers) == 0 {
		t.Error("servers should be initialized")
	}

	// Cleanup.
	if result.Deps.SrvCancel != nil {
		result.Deps.SrvCancel()
	}
}

func TestBootstrap_Failure_ConfigMissing(t *testing.T) {
	// Set explicit empty value so .env loading cannot repopulate DB_DSN.
	t.Setenv("DB_DSN", "")

	result := bootstrap()
	if result.Err == nil {
		t.Fatal("expected bootstrap to fail without DB_DSN")
	}
	if result.Deps == nil {
		t.Error("deps should be allocated even on failure")
	}
	if result.Partial != nil {
		t.Error("partial should be nil when config fails")
	}
}

func TestBootstrap_Failure_AllowAnyTokenInProduction(t *testing.T) {
	setupBootstrapEnv(t, map[string]string{
		"ENVIRONMENT":     "production",
		"ALLOW_ANY_TOKEN": "true",
	})

	result := bootstrap()
	if result.Err == nil {
		t.Fatal("expected bootstrap to fail when ALLOW_ANY_TOKEN is enabled in production")
	}
	if !strings.Contains(result.Err.Error(), "ALLOW_ANY_TOKEN=true is not allowed") {
		t.Fatalf("expected ALLOW_ANY_TOKEN production guard error, got %v", result.Err)
	}
	if result.Partial == nil {
		t.Fatal("expected partial deps with loaded config")
	}
	if result.Partial.Cfg.Environment != "production" {
		t.Fatalf("expected partial config to retain production environment, got %q", result.Partial.Cfg.Environment)
	}
	if !result.Partial.Cfg.AllowAnyToken {
		t.Fatal("expected partial config to retain ALLOW_ANY_TOKEN=true")
	}
	if result.Partial.DB != nil {
		t.Fatal("expected DB initialization to be skipped when the production guard fails")
	}
}

func TestBootstrap_Failure_DBConnection(t *testing.T) {
	setupBootstrapEnv(t, map[string]string{
		"DB_DSN": "invalid://connection-string",
	})

	result := bootstrap()
	if result.Err == nil {
		t.Fatal("expected bootstrap to fail with invalid DB connection")
	}
	// Config was loaded successfully before DB failed, so Partial should have config.
	if result.Partial == nil {
		t.Error("partial should contain config on DB failure")
	} else if result.Partial.Cfg.DBdsn != "invalid://connection-string" {
		t.Error("partial should contain config with the invalid DB DSN")
	}
}

func TestBootstrap_Failure_GitstoreInvalidDir(t *testing.T) {
	setupBootstrapEnv(t, map[string]string{
		"GIT_REPO_DIR": "/nonexistent/path/that/does/not/exist",
	})

	result := bootstrap()
	if result.Err == nil {
		t.Fatal("expected bootstrap to fail with invalid git repo dir")
	}
	if result.Partial == nil {
		t.Error("partial should contain deps up to gitstore failure")
	} else if result.Partial.DB == nil {
		t.Error("partial should contain DB")
	} else if result.Partial.Cfg.DBdsn == "" {
		t.Error("partial should contain config")
	}
}

func TestBootstrap_Failure_TLS_MissingCerts(t *testing.T) {
	setupBootstrapEnv(t, map[string]string{
		"LISTEN_MODE": "development", // Requires TLS certs
	})
	// Run from an empty temp dir so local cert.pem/key.pem in repo do not mask this case.
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	tempWD := t.TempDir()
	if err := os.Chdir(tempWD); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origWD)
	})

	result := bootstrap()
	if result.Err == nil {
		t.Fatal("expected bootstrap to fail without TLS certs in development mode")
	}
	if result.Partial == nil {
		t.Error("partial should contain deps up to TLS failure")
	} else if result.Partial.DB == nil {
		t.Error("partial should contain DB")
	} else if result.Partial.Store == nil {
		t.Error("partial should contain Store")
	}
}

func TestBootstrap_ListenerBindFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	portStr := strconv.Itoa(port)

	setupBootstrapEnv(t, map[string]string{
		"BASE_URL": "http://localhost:" + portStr,
		"PORT":     portStr,
	})

	result := bootstrap()
	if result.Err != nil {
		t.Fatalf("bootstrap failed: %v", result.Err)
	}
	if len(result.Deps.Servers) == 0 {
		t.Fatal("expected at least one server to be configured")
	}

	err = result.Deps.Servers[0].ListenAndServe()
	if err == nil {
		t.Fatal("expected ListenAndServe to fail due to port already in use")
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("expected EADDRINUSE, got %v", err)
	}

	if result.Deps.SrvCancel != nil {
		result.Deps.SrvCancel()
	}
}

func TestBootstrap_WithControlPlane_Success(t *testing.T) {
	t.Skip("requires MySQL instance for control plane DB")
}

func TestBootstrap_ControlPlane_Enabled(t *testing.T) {
	overrideControlPlaneHooks(t,
		func(dsn string) (*gorm.DB, error) {
			return gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		},
		func(dsn string) (*gorm.DB, error) {
			return gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		},
		nil,
	)

	setupBootstrapEnv(t, map[string]string{
		"CONTROL_PLANE_DSN": fmt.Sprintf("file:test_cp_ctrl_%d?mode=memory&cache=shared", testDBCounter.Add(1)),
	})

	result := bootstrap()
	if result.Err != nil {
		t.Fatalf("bootstrap failed: %v", result.Err)
	}
	if result.Deps.DBRouter == nil {
		t.Fatal("expected DBRouter to be initialized when control plane is enabled")
	}
	if result.Deps.Handlers == nil || result.Deps.Handlers.Router == nil {
		t.Fatal("expected handlers to include control plane router")
	}
	if result.Deps.SrvCancel != nil {
		result.Deps.SrvCancel()
	}
}

func TestBootstrap_ControlPlane_DBFailure(t *testing.T) {
	overrideControlPlaneHooks(t,
		func(_ string) (*gorm.DB, error) {
			return nil, errors.New("control plane unreachable")
		},
		nil,
		nil,
	)

	setupBootstrapEnv(t, map[string]string{
		"CONTROL_PLANE_DSN": "tcp(127.0.0.1:3306)/invalid",
	})

	result := bootstrap()
	if result.Err == nil {
		t.Fatal("expected bootstrap to fail when control plane DB cannot be opened")
	}
	if result.Partial == nil {
		t.Fatal("expected partial deps on control plane DB failure")
	}
	if result.Partial.DB == nil {
		t.Error("expected main DB to be initialized before control plane failure")
	}
	if result.Partial.Store == nil {
		t.Error("expected gitstore to be initialized before control plane failure")
	}
}

func TestBootstrap_ControlPlane_MigrateFailure(t *testing.T) {
	overrideControlPlaneHooks(t,
		func(dsn string) (*gorm.DB, error) {
			return gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		},
		func(dsn string) (*gorm.DB, error) {
			return gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		},
		func(_ *gorm.DB) error {
			return errors.New("auto-migrate failed")
		},
	)

	setupBootstrapEnv(t, map[string]string{
		"CONTROL_PLANE_DSN": fmt.Sprintf("file:test_cp_mig_ctrl_%d?mode=memory&cache=shared", testDBCounter.Add(1)),
	})

	result := bootstrap()
	if result.Err == nil {
		t.Fatal("expected bootstrap to fail when control plane migration fails")
	}
	if result.Partial == nil {
		t.Fatal("expected partial deps on control plane migration failure")
	}
	if result.Partial.DB == nil {
		t.Error("expected main DB to be initialized before control plane migration failure")
	}
}

func TestBootstrap_WithEmbedding_Success(t *testing.T) {
	setupBootstrapEnv(t, map[string]string{
		"EMBEDDING_API_KEY":  "test-key",
		"EMBEDDING_BASE_URL": "http://localhost:1234",
		"EMBEDDING_MODEL":    "test-model",
	})

	result := bootstrap()
	if result.Err != nil {
		t.Fatalf("bootstrap with embedding failed: %v", result.Err)
	}
	if result.Deps.Embedder == nil {
		t.Error("embedder should be initialized")
	}

	// Cleanup.
	if result.Deps.SrvCancel != nil {
		result.Deps.SrvCancel()
	}
}
