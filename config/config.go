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
	dbDSN := os.Getenv("DB_DSN")
	if dbDSN == "" {
		return Config{}, fmt.Errorf("required environment variable not set: DB_DSN")
	}
	listenMode := getEnv("LISTEN_MODE", "development")
	if listenMode != "production" && listenMode != "development" {
		return Config{}, fmt.Errorf("invalid LISTEN_MODE %q: must be \"production\" or \"development\"", listenMode)
	}
	env := strings.ToLower(strings.TrimSpace(getEnv("ENVIRONMENT", "production")))
	if env != "production" && env != "development" {
		return Config{}, fmt.Errorf("invalid ENVIRONMENT %q: must be \"production\" or \"development\"", env)
	}
	embeddingDims := 0
	if v := os.Getenv("EMBEDDING_DIMENSIONS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return Config{}, fmt.Errorf("invalid EMBEDDING_DIMENSIONS %q: must be a non-negative integer", v)
		}
		embeddingDims = n
	}
	workflowExecTimeout := 2 * time.Minute
	if v := os.Getenv("WORKFLOW_EXEC_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("invalid WORKFLOW_EXEC_TIMEOUT %q: must be a positive duration", v)
		}
		workflowExecTimeout = d
	}
	workflowExecPidsLimit := 128
	if v := os.Getenv("WORKFLOW_EXEC_PIDS_LIMIT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("invalid WORKFLOW_EXEC_PIDS_LIMIT %q: must be a positive integer", v)
		}
		workflowExecPidsLimit = n
	}
	workflowExecNoFile := 1024
	if v := os.Getenv("WORKFLOW_EXEC_NOFILE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("invalid WORKFLOW_EXEC_NOFILE %q: must be a positive integer", v)
		}
		workflowExecNoFile = n
	}
	return Config{
		Environment:    env,
		ListenMode:     listenMode,
		Port:           getEnv("PORT", "8080"),
		BaseURL:        getEnv("BASE_URL", "http://localhost:8080"),
		ConsoleBaseURL: getEnv("CONSOLE_BASE_URL", "http://localhost:5173"),
		DBdsn:          dbDSN,
		GitRepoDir:     getEnv("GIT_REPO_DIR", "gitrepos"),
		AllowAnyToken:  os.Getenv("ALLOW_ANY_TOKEN") == "true" || os.Getenv("ALLOW_ANY_TOKEN") == "1",
		OAuthPreapproveDeviceCodes: os.Getenv("OAUTH_PREAPPROVE_DEVICE_CODES") == "true" ||
			os.Getenv("OAUTH_PREAPPROVE_DEVICE_CODES") == "1",
		AdminLogin:            os.Getenv("ADMIN_LOGIN"),
		AdminToken:            os.Getenv("ADMIN_TOKEN"),
		ControlPlaneDSN:       os.Getenv("CONTROL_PLANE_DSN"),
		EmbeddingAPIKey:       os.Getenv("EMBEDDING_API_KEY"),
		EmbeddingBaseURL:      getEnv("EMBEDDING_BASE_URL", "https://api.openai.com"),
		EmbeddingModel:        getEnv("EMBEDDING_MODEL", "text-embedding-3-small"),
		EmbeddingDimensions:   embeddingDims,
		Auth0Issuer:           os.Getenv("AUTH0_ISSUER"),
		Auth0ClientID:         os.Getenv("AUTH0_CLIENT_ID"),
		Auth0Audience:         os.Getenv("AUTH0_AUDIENCE"),
		EnableWorkflowExec:    os.Getenv("ENABLE_WORKFLOW_EXEC") == "true" || os.Getenv("ENABLE_WORKFLOW_EXEC") == "1",
		WorkflowExecImage:     getEnv("WORKFLOW_EXEC_IMAGE", "bash:5.2"),
		WorkflowExecTimeout:   workflowExecTimeout,
		WorkflowExecCPUs:      getEnv("WORKFLOW_EXEC_CPUS", "1.0"),
		WorkflowExecMemory:    getEnv("WORKFLOW_EXEC_MEMORY", "256m"),
		WorkflowExecPidsLimit: workflowExecPidsLimit,
		WorkflowExecNoFile:    workflowExecNoFile,
		WorkflowExecTmpfsSize: getEnv("WORKFLOW_EXEC_TMPFS_SIZE", "64m"),
	}, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
