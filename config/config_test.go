package config

import (
	"strings"
	"testing"
	"time"
)

func TestNewDefaults(t *testing.T) {
	t.Setenv("DB_DSN", "user:pass@tcp(localhost)/testdb")
	// Set optional vars to empty to verify defaults (t.Setenv restores on cleanup)
	t.Setenv("PORT", "")
	t.Setenv("BASE_URL", "")
	t.Setenv("CONSOLE_BASE_URL", "")
	t.Setenv("GIT_REPO_DIR", "")

	cfg, err := New()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Port != "8080" {
		t.Errorf("expected default Port=8080, got %q", cfg.Port)
	}
	if cfg.BaseURL != "http://localhost:8080" {
		t.Errorf("expected default BaseURL, got %q", cfg.BaseURL)
	}
	if cfg.ConsoleBaseURL != "http://localhost:5173" {
		t.Errorf("expected default ConsoleBaseURL, got %q", cfg.ConsoleBaseURL)
	}
	if cfg.GitRepoDir != "gitrepos" {
		t.Errorf("expected default GitRepoDir=gitrepos, got %q", cfg.GitRepoDir)
	}
	if cfg.DBdsn != "user:pass@tcp(localhost)/testdb" {
		t.Errorf("expected DBdsn from env, got %q", cfg.DBdsn)
	}
	if cfg.OAuthPreapproveDeviceCodes {
		t.Error("expected OAuthPreapproveDeviceCodes=false by default")
	}
	if cfg.EnableWorkflowExec {
		t.Error("expected workflow execution to be disabled by default")
	}
	if cfg.WorkflowExecImage != "bash:5.2" {
		t.Errorf("expected default WorkflowExecImage=bash:5.2, got %q", cfg.WorkflowExecImage)
	}
	if cfg.WorkflowExecTimeout != 2*time.Minute {
		t.Errorf("expected default WorkflowExecTimeout=2m, got %s", cfg.WorkflowExecTimeout)
	}
	if cfg.WorkflowExecCPUs != "1.0" {
		t.Errorf("expected default WorkflowExecCPUs=1.0, got %q", cfg.WorkflowExecCPUs)
	}
	if cfg.WorkflowExecMemory != "256m" {
		t.Errorf("expected default WorkflowExecMemory=256m, got %q", cfg.WorkflowExecMemory)
	}
	if cfg.WorkflowExecPidsLimit != 128 {
		t.Errorf("expected default WorkflowExecPidsLimit=128, got %d", cfg.WorkflowExecPidsLimit)
	}
	if cfg.WorkflowExecNoFile != 1024 {
		t.Errorf("expected default WorkflowExecNoFile=1024, got %d", cfg.WorkflowExecNoFile)
	}
	if cfg.WorkflowExecTmpfsSize != "64m" {
		t.Errorf("expected default WorkflowExecTmpfsSize=64m, got %q", cfg.WorkflowExecTmpfsSize)
	}
}

func TestNewOverrides(t *testing.T) {
	t.Setenv("DB_DSN", "custom-dsn")
	t.Setenv("PORT", "9090")
	t.Setenv("BASE_URL", "https://example.com")
	t.Setenv("CONSOLE_BASE_URL", "https://console.example.com")
	t.Setenv("GIT_REPO_DIR", "/tmp/repos")
	t.Setenv("OAUTH_PREAPPROVE_DEVICE_CODES", "true")
	t.Setenv("ENABLE_WORKFLOW_EXEC", "1")
	t.Setenv("WORKFLOW_EXEC_IMAGE", "custom/bash:latest")
	t.Setenv("WORKFLOW_EXEC_TIMEOUT", "45s")
	t.Setenv("WORKFLOW_EXEC_CPUS", "0.5")
	t.Setenv("WORKFLOW_EXEC_MEMORY", "128m")
	t.Setenv("WORKFLOW_EXEC_PIDS_LIMIT", "64")
	t.Setenv("WORKFLOW_EXEC_NOFILE", "256")
	t.Setenv("WORKFLOW_EXEC_TMPFS_SIZE", "16m")
	t.Setenv("OIDC_PROVIDER", "casdoor")
	t.Setenv("OIDC_ISSUER", "https://door.example.com")
	t.Setenv("OIDC_CLIENT_ID", "oidc-client")
	t.Setenv("OIDC_SCOPES", "openid profile email groups")

	cfg, err := New()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Port != "9090" {
		t.Errorf("expected Port=9090, got %q", cfg.Port)
	}
	if cfg.BaseURL != "https://example.com" {
		t.Errorf("expected BaseURL=https://example.com, got %q", cfg.BaseURL)
	}
	if cfg.ConsoleBaseURL != "https://console.example.com" {
		t.Errorf("expected ConsoleBaseURL=https://console.example.com, got %q", cfg.ConsoleBaseURL)
	}
	if cfg.GitRepoDir != "/tmp/repos" {
		t.Errorf("expected GitRepoDir=/tmp/repos, got %q", cfg.GitRepoDir)
	}
	if cfg.DBdsn != "custom-dsn" {
		t.Errorf("expected DBdsn=custom-dsn, got %q", cfg.DBdsn)
	}
	if !cfg.OAuthPreapproveDeviceCodes {
		t.Error("expected OAuthPreapproveDeviceCodes=true")
	}
	if !cfg.EnableWorkflowExec {
		t.Error("expected workflow execution to be enabled")
	}
	if cfg.WorkflowExecImage != "custom/bash:latest" {
		t.Errorf("expected WorkflowExecImage override, got %q", cfg.WorkflowExecImage)
	}
	if cfg.WorkflowExecTimeout != 45*time.Second {
		t.Errorf("expected WorkflowExecTimeout=45s, got %s", cfg.WorkflowExecTimeout)
	}
	if cfg.WorkflowExecCPUs != "0.5" {
		t.Errorf("expected WorkflowExecCPUs=0.5, got %q", cfg.WorkflowExecCPUs)
	}
	if cfg.WorkflowExecMemory != "128m" {
		t.Errorf("expected WorkflowExecMemory=128m, got %q", cfg.WorkflowExecMemory)
	}
	if cfg.WorkflowExecPidsLimit != 64 {
		t.Errorf("expected WorkflowExecPidsLimit=64, got %d", cfg.WorkflowExecPidsLimit)
	}
	if cfg.WorkflowExecNoFile != 256 {
		t.Errorf("expected WorkflowExecNoFile=256, got %d", cfg.WorkflowExecNoFile)
	}
	if cfg.WorkflowExecTmpfsSize != "16m" {
		t.Errorf("expected WorkflowExecTmpfsSize=16m, got %q", cfg.WorkflowExecTmpfsSize)
	}
	if cfg.OIDCProvider != "casdoor" || cfg.OIDCIssuer != "https://door.example.com" || cfg.OIDCClientID != "oidc-client" {
		t.Fatalf("expected explicit oidc config to be loaded, got %+v", cfg)
	}
	if cfg.OIDCScopes != "openid profile email groups" {
		t.Fatalf("expected explicit oidc scopes, got %q", cfg.OIDCScopes)
	}
}

func TestNewLoadsSlockOAuthConfig(t *testing.T) {
	t.Setenv("DB_DSN", "user:pass@tcp(localhost)/testdb")
	t.Setenv("SLOCK_ORIGIN", " https://app.slock.ai ")
	t.Setenv("SLOCK_API_ORIGIN", " https://api.slock.ai ")
	t.Setenv("SLOCK_CLIENT_ID", "slock-client")
	t.Setenv("SLOCK_CLIENT_SECRET", "slock-secret")

	cfg, err := New()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !cfg.SlockOAuthEnabled() {
		t.Fatal("expected Slock OAuth to be enabled")
	}
	if cfg.SlockOrigin != "https://app.slock.ai" {
		t.Fatalf("SlockOrigin: got %q", cfg.SlockOrigin)
	}
	if cfg.SlockAPIOrigin != "https://api.slock.ai" {
		t.Fatalf("SlockAPIOrigin: got %q", cfg.SlockAPIOrigin)
	}
	if cfg.SlockClientID != "slock-client" {
		t.Fatalf("SlockClientID: got %q", cfg.SlockClientID)
	}
	if cfg.SlockClientSecret != "slock-secret" {
		t.Fatalf("SlockClientSecret: got %q", cfg.SlockClientSecret)
	}
}

func TestNewRejectsPartialSlockOAuthConfig(t *testing.T) {
	t.Setenv("DB_DSN", "user:pass@tcp(localhost)/testdb")
	t.Setenv("SLOCK_ORIGIN", "https://app.slock.ai")
	t.Setenv("SLOCK_API_ORIGIN", "")
	t.Setenv("SLOCK_CLIENT_ID", "slock-client")
	t.Setenv("SLOCK_CLIENT_SECRET", "")

	_, err := New()
	if err == nil {
		t.Fatal("expected partial Slock config to fail")
	}
	if !strings.Contains(err.Error(), "login-with-slock: partial configuration") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOIDCProviderDefaultsToAuth0ForAuth0Issuer(t *testing.T) {
	t.Setenv("DB_DSN", "user:pass@tcp(localhost)/testdb")
	t.Setenv("OIDC_PROVIDER", "")
	t.Setenv("OIDC_ISSUER", "https://tenant.us.auth0.com/")
	t.Setenv("OIDC_CLIENT_ID", "oidc-client")

	cfg, err := New()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OIDCProvider != "auth0" {
		t.Fatalf("expected OIDCProvider=auth0 for Auth0 issuer, got %q", cfg.OIDCProvider)
	}
}

func TestOIDCProviderDefaultsToOIDCForNonAuth0Issuer(t *testing.T) {
	t.Setenv("DB_DSN", "user:pass@tcp(localhost)/testdb")
	t.Setenv("OIDC_PROVIDER", "")
	t.Setenv("OIDC_ISSUER", "https://issuer.example.com")
	t.Setenv("OIDC_CLIENT_ID", "oidc-client")

	cfg, err := New()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OIDCProvider != "oidc" {
		t.Fatalf("expected OIDCProvider=oidc for generic issuer, got %q", cfg.OIDCProvider)
	}
}

func TestNewErrorsWithoutDBDSN(t *testing.T) {
	t.Setenv("DB_DSN", "")

	_, err := New()
	if err == nil {
		t.Fatal("expected error when DB_DSN is not set")
	}
	expected := "required environment variable not set: DB_DSN"
	if err.Error() != expected {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestEnvironmentDefaultsToProduction(t *testing.T) {
	t.Setenv("DB_DSN", "user:pass@tcp(localhost)/testdb")
	t.Setenv("ENVIRONMENT", "")

	cfg, err := New()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Environment != "production" {
		t.Errorf("expected default Environment=production, got %q", cfg.Environment)
	}
}

func TestEnvironmentExplicitDevelopment(t *testing.T) {
	t.Setenv("DB_DSN", "user:pass@tcp(localhost)/testdb")
	t.Setenv("ENVIRONMENT", "development")

	cfg, err := New()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Environment != "development" {
		t.Errorf("expected Environment=development, got %q", cfg.Environment)
	}
}

func TestListenModeDefault(t *testing.T) {
	t.Setenv("DB_DSN", "user:pass@tcp(localhost)/testdb")
	t.Setenv("LISTEN_MODE", "")

	cfg, err := New()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ListenMode != "development" {
		t.Errorf("expected default ListenMode=development, got %q", cfg.ListenMode)
	}
}

func TestListenModeProduction(t *testing.T) {
	t.Setenv("DB_DSN", "user:pass@tcp(localhost)/testdb")
	t.Setenv("LISTEN_MODE", "production")

	cfg, err := New()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ListenMode != "production" {
		t.Errorf("expected ListenMode=production, got %q", cfg.ListenMode)
	}
}

func TestListenModeInvalid(t *testing.T) {
	t.Setenv("DB_DSN", "user:pass@tcp(localhost)/testdb")

	for _, mode := range []string{"Production", "PRODUCTION", "dev", "staging", "typo"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("LISTEN_MODE", mode)
			_, err := New()
			if err == nil {
				t.Fatalf("expected error for LISTEN_MODE=%q, got nil", mode)
			}
		})
	}
}

func TestEnvironmentDefaultProduction(t *testing.T) {
	t.Setenv("DB_DSN", "user:pass@tcp(localhost)/testdb")
	t.Setenv("ENVIRONMENT", "") // unset — must default to production

	cfg, err := New()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Environment != "production" {
		t.Errorf("expected default Environment=production, got %q", cfg.Environment)
	}
}

func TestEnvironmentDevelopment(t *testing.T) {
	t.Setenv("DB_DSN", "user:pass@tcp(localhost)/testdb")
	t.Setenv("ENVIRONMENT", "development")

	cfg, err := New()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Environment != "development" {
		t.Errorf("expected Environment=development, got %q", cfg.Environment)
	}
}

func TestEnvironmentNormalization(t *testing.T) {
	t.Setenv("DB_DSN", "user:pass@tcp(localhost)/testdb")

	for _, val := range []string{"PRODUCTION", "Production", " production ", "DEVELOPMENT", "Development", " development "} {
		t.Run(val, func(t *testing.T) {
			t.Setenv("ENVIRONMENT", val)
			cfg, err := New()
			if err != nil {
				t.Fatalf("unexpected error for ENVIRONMENT=%q: %v", val, err)
			}
			norm := cfg.Environment
			if norm != "production" && norm != "development" {
				t.Errorf("expected normalized value, got %q for input %q", norm, val)
			}
		})
	}
}

func TestEnvironmentInvalid(t *testing.T) {
	t.Setenv("DB_DSN", "user:pass@tcp(localhost)/testdb")

	for _, val := range []string{"staging", "dev", "prod", "test", "typo"} {
		t.Run(val, func(t *testing.T) {
			t.Setenv("ENVIRONMENT", val)
			_, err := New()
			if err == nil {
				t.Fatalf("expected error for ENVIRONMENT=%q, got nil", val)
			}
		})
	}
}

func TestGetEnvReturnsValueWhenSet(t *testing.T) {
	t.Setenv("TEST_CONFIG_KEY", "custom")
	got := getEnv("TEST_CONFIG_KEY", "default")
	if got != "custom" {
		t.Errorf("expected 'custom', got %q", got)
	}
}

func TestGetEnvReturnsFallbackWhenUnset(t *testing.T) {
	t.Setenv("TEST_CONFIG_KEY_UNSET", "")
	got := getEnv("TEST_CONFIG_KEY_UNSET", "fallback")
	if got != "fallback" {
		t.Errorf("expected 'fallback', got %q", got)
	}
}

func TestWorkflowExecTimeoutInvalid(t *testing.T) {
	t.Setenv("DB_DSN", "user:pass@tcp(localhost)/testdb")
	t.Setenv("WORKFLOW_EXEC_TIMEOUT", "nope")

	if _, err := New(); err == nil {
		t.Fatal("expected error for invalid WORKFLOW_EXEC_TIMEOUT")
	}
}

func TestWorkflowExecPidsLimitInvalid(t *testing.T) {
	t.Setenv("DB_DSN", "user:pass@tcp(localhost)/testdb")
	t.Setenv("WORKFLOW_EXEC_PIDS_LIMIT", "0")

	if _, err := New(); err == nil {
		t.Fatal("expected error for invalid WORKFLOW_EXEC_PIDS_LIMIT")
	}
}

func TestWorkflowExecNoFileInvalid(t *testing.T) {
	t.Setenv("DB_DSN", "user:pass@tcp(localhost)/testdb")
	t.Setenv("WORKFLOW_EXEC_NOFILE", "-1")

	if _, err := New(); err == nil {
		t.Fatal("expected error for invalid WORKFLOW_EXEC_NOFILE")
	}
}
