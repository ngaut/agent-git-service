// Package config provides typed configuration loaded from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all server configuration.
type Config struct {
	Port       string
	BaseURL    string
	RESTPrefix string
	DBdsn      string
	GitRepoDir string

	// ListenMode controls listener setup: "development" (default) starts
	// multiple listeners with TLS; "production" starts a single HTTP listener.
	ListenMode string

	// AllowAnyToken, when true, accepts any non-empty token when no
	// tokens exist in the database (dev-mode convenience).
	// Default is false (production-secure).
	AllowAnyToken bool

	// OAuthPreapproveDeviceCodes restores the legacy insecure local-dev device
	// flow that auto-approves newly-created device codes.
	OAuthPreapproveDeviceCodes bool

	// AdminLogin and AdminToken override the default seed credentials.
	// When both are empty the legacy testadmin / mytoken values are used.
	AdminLogin string
	AdminToken string

	// Environment controls operational behaviour that differs between
	// deployments. Allowed values: "production" (default, fail-closed) and
	// "development". When set to "development", test seed data is inserted
	// at startup. The default is "production" so that an unset variable
	// never silently seeds credentials.
	Environment string

	// ControlPlaneDSN, when set, enables multi-agent mode.
	// Requests are routed to per-agent TiDB instances via the control plane.
	// When empty, the system runs in single-DB mode (current behavior).
	ControlPlaneDSN string

	// Embedding provider configuration (all optional).
	// When EmbeddingAPIKey is empty, vector search is disabled and
	// search falls back to lexical-only matching.
	EmbeddingAPIKey  string
	EmbeddingBaseURL string
	EmbeddingModel   string
	// EmbeddingDimensions overrides the embedding vector size (0 = auto-detect).
	EmbeddingDimensions int

	// Auth0 configuration (optional; required for human login flows).
	Auth0Issuer   string
	Auth0ClientID string
	Auth0Audience string

	// ConsoleBaseURL is the base URL of the console frontend used for browser redirects.
	ConsoleBaseURL string

	// Workflow execution sandbox configuration. Execution is fail-closed by
	// default and only enabled when ENABLE_WORKFLOW_EXEC is set.
	EnableWorkflowExec    bool
	WorkflowExecImage     string
	WorkflowExecTimeout   time.Duration
	WorkflowExecCPUs      string
	WorkflowExecMemory    string
	WorkflowExecPidsLimit int
	WorkflowExecNoFile    int
	WorkflowExecTmpfsSize string
}

// New reads environment variables and returns a fully-populated Config.
// It returns an error if any required variable (DB_DSN) is missing.
func New() (Config, error) {
	cfg := Config{
		Port:           os.Getenv("PORT"),
		BaseURL:        os.Getenv("BASE_URL"),
		RESTPrefix:     os.Getenv("REST_PREFIX"),
		ConsoleBaseURL: os.Getenv("CONSOLE_BASE_URL"),
		DBdsn:          os.Getenv("DB_DSN"),
		GitRepoDir:     os.Getenv("GIT_REPO_DIR"),
		ListenMode:     os.Getenv("LISTEN_MODE"),
		AllowAnyToken:  os.Getenv("ALLOW_ANY_TOKEN") == "true" || os.Getenv("ALLOW_ANY_TOKEN") == "1",
		OAuthPreapproveDeviceCodes: os.Getenv("OAUTH_PREAPPROVE_DEVICE_CODES") == "true" ||
			os.Getenv("OAUTH_PREAPPROVE_DEVICE_CODES") == "1",
		AdminLogin:            os.Getenv("ADMIN_LOGIN"),
		AdminToken:            os.Getenv("ADMIN_TOKEN"),
		Environment:           os.Getenv("ENVIRONMENT"),
		ControlPlaneDSN:       os.Getenv("CONTROL_PLANE_DSN"),
		EmbeddingAPIKey:       os.Getenv("EMBEDDING_API_KEY"),
		EmbeddingBaseURL:      os.Getenv("EMBEDDING_BASE_URL"),
		EmbeddingModel:        os.Getenv("EMBEDDING_MODEL"),
		Auth0Issuer:           os.Getenv("AUTH0_ISSUER"),
		Auth0ClientID:         os.Getenv("AUTH0_CLIENT_ID"),
		Auth0Audience:         os.Getenv("AUTH0_AUDIENCE"),
		EnableWorkflowExec:    os.Getenv("ENABLE_WORKFLOW_EXEC") == "true" || os.Getenv("ENABLE_WORKFLOW_EXEC") == "1",
		WorkflowExecImage:     os.Getenv("WORKFLOW_EXEC_IMAGE"),
		WorkflowExecCPUs:      os.Getenv("WORKFLOW_EXEC_CPUS"),
		WorkflowExecMemory:    os.Getenv("WORKFLOW_EXEC_MEMORY"),
		WorkflowExecTmpfsSize: os.Getenv("WORKFLOW_EXEC_TMPFS_SIZE"),
	}
	if v := os.Getenv("EMBEDDING_DIMENSIONS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid EMBEDDING_DIMENSIONS %q: must be a non-negative integer", v)
		}
		cfg.EmbeddingDimensions = n
	}
	if v := os.Getenv("WORKFLOW_EXEC_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid WORKFLOW_EXEC_TIMEOUT %q: must be a positive duration", v)
		}
		cfg.WorkflowExecTimeout = d
	}
	if v := os.Getenv("WORKFLOW_EXEC_PIDS_LIMIT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("invalid WORKFLOW_EXEC_PIDS_LIMIT %q: must be a positive integer", v)
		}
		cfg.WorkflowExecPidsLimit = n
	}
	if v := os.Getenv("WORKFLOW_EXEC_NOFILE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("invalid WORKFLOW_EXEC_NOFILE %q: must be a positive integer", v)
		}
		cfg.WorkflowExecNoFile = n
	}
	return Normalize(cfg)
}

// Normalize applies defaults and validates a programmatically supplied config.
func Normalize(cfg Config) (Config, error) {
	cfg.Port = firstNonEmpty(cfg.Port, "8080")
	cfg.BaseURL = firstNonEmpty(cfg.BaseURL, "http://localhost:8080")
	cfg.RESTPrefix = normalizeRESTPrefix(firstNonEmpty(cfg.RESTPrefix, "/api/v3"))
	cfg.ConsoleBaseURL = firstNonEmpty(cfg.ConsoleBaseURL, "http://localhost:5173")
	cfg.GitRepoDir = firstNonEmpty(cfg.GitRepoDir, "gitrepos")
	cfg.ListenMode = firstNonEmpty(cfg.ListenMode, "development")
	cfg.Environment = strings.ToLower(strings.TrimSpace(firstNonEmpty(cfg.Environment, "production")))
	cfg.EmbeddingBaseURL = firstNonEmpty(cfg.EmbeddingBaseURL, "https://api.openai.com")
	cfg.EmbeddingModel = firstNonEmpty(cfg.EmbeddingModel, "text-embedding-3-small")
	cfg.WorkflowExecImage = firstNonEmpty(cfg.WorkflowExecImage, "bash:5.2")
	if cfg.WorkflowExecTimeout == 0 {
		cfg.WorkflowExecTimeout = 2 * time.Minute
	}
	if cfg.WorkflowExecCPUs == "" {
		cfg.WorkflowExecCPUs = "1.0"
	}
	if cfg.WorkflowExecMemory == "" {
		cfg.WorkflowExecMemory = "256m"
	}
	if cfg.WorkflowExecPidsLimit == 0 {
		cfg.WorkflowExecPidsLimit = 128
	}
	if cfg.WorkflowExecNoFile == 0 {
		cfg.WorkflowExecNoFile = 1024
	}
	if cfg.WorkflowExecTmpfsSize == "" {
		cfg.WorkflowExecTmpfsSize = "64m"
	}
	if cfg.DBdsn == "" {
		return Config{}, fmt.Errorf("required environment variable not set: DB_DSN")
	}
	if cfg.ListenMode != "production" && cfg.ListenMode != "development" {
		return Config{}, fmt.Errorf("invalid LISTEN_MODE %q: must be \"production\" or \"development\"", cfg.ListenMode)
	}
	if cfg.Environment != "production" && cfg.Environment != "development" {
		return Config{}, fmt.Errorf("invalid ENVIRONMENT %q: must be \"production\" or \"development\"", cfg.Environment)
	}
	if cfg.EmbeddingDimensions < 0 {
		return Config{}, fmt.Errorf("invalid EMBEDDING_DIMENSIONS %d: must be a non-negative integer", cfg.EmbeddingDimensions)
	}
	if cfg.WorkflowExecTimeout <= 0 {
		return Config{}, fmt.Errorf("invalid WORKFLOW_EXEC_TIMEOUT %q: must be a positive duration", cfg.WorkflowExecTimeout)
	}
	if cfg.WorkflowExecPidsLimit <= 0 {
		return Config{}, fmt.Errorf("invalid WORKFLOW_EXEC_PIDS_LIMIT %d: must be a positive integer", cfg.WorkflowExecPidsLimit)
	}
	if cfg.WorkflowExecNoFile <= 0 {
		return Config{}, fmt.Errorf("invalid WORKFLOW_EXEC_NOFILE %d: must be a positive integer", cfg.WorkflowExecNoFile)
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func normalizeRESTPrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return "/api/v3"
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		return "/"
	}
	return prefix
}
