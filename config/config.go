// Package config provides typed configuration loaded from environment variables.
package config

import (
	"fmt"
	"net/url"
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

	OIDCProvider          string
	OIDCIssuer            string
	OIDCDiscoveryURL      string
	OIDCClientID          string
	OIDCClientSecret      string
	OIDCAudience          string
	OIDCScopes            string
	OIDCAllowInsecureHTTP bool

	// ConnectedLogin configures an OAuth-style browser login provider that does
	// not expose enough standard OIDC surface for the generic OIDC client.
	// Origin, APIOrigin, ClientID, and ClientSecret must be set together to
	// enable /auth/connected/login and /auth/connected/callback.
	ConnectedLoginProvider                  string
	ConnectedLoginOrigin                    string
	ConnectedLoginAPIOrigin                 string
	ConnectedLoginClientID                  string
	ConnectedLoginClientSecret              string
	ConnectedLoginLoginPath                 string
	ConnectedLoginTokenPath                 string
	ConnectedLoginUserinfoPath              string
	ConnectedLoginReturnToParam             string
	ConnectedLoginSubjectClaim              string
	ConnectedLoginSubjectNamespaceClaim     string
	ConnectedLoginSubjectNamespaceSlugClaim string
	ConnectedLoginActorTypeClaim            string
	ConnectedLoginHumanTypeValue            string
	ConnectedLoginAgentTypeValue            string
	ConnectedLoginClientIDClaim             string
	ConnectedLoginClientNameClaim           string
	ConnectedLoginPreferredUsernameClaim    string
	ConnectedLoginNameClaim                 string
	ConnectedLoginPictureClaim              string
	ConnectedLoginAvatarURLClaim            string
	ConnectedLoginDescriptionClaim          string
	ConnectedLoginScopeClaim                string

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
		ConsoleBaseURL: os.Getenv("CONSOLE_BASE_URL"),
		DBdsn:          os.Getenv("DB_DSN"),
		GitRepoDir:     os.Getenv("GIT_REPO_DIR"),
		ListenMode:     os.Getenv("LISTEN_MODE"),
		AllowAnyToken:  os.Getenv("ALLOW_ANY_TOKEN") == "true" || os.Getenv("ALLOW_ANY_TOKEN") == "1",
		OAuthPreapproveDeviceCodes: os.Getenv("OAUTH_PREAPPROVE_DEVICE_CODES") == "true" ||
			os.Getenv("OAUTH_PREAPPROVE_DEVICE_CODES") == "1",
		AdminLogin:                              os.Getenv("ADMIN_LOGIN"),
		AdminToken:                              os.Getenv("ADMIN_TOKEN"),
		Environment:                             os.Getenv("ENVIRONMENT"),
		ControlPlaneDSN:                         os.Getenv("CONTROL_PLANE_DSN"),
		EmbeddingAPIKey:                         os.Getenv("EMBEDDING_API_KEY"),
		EmbeddingBaseURL:                        os.Getenv("EMBEDDING_BASE_URL"),
		EmbeddingModel:                          os.Getenv("EMBEDDING_MODEL"),
		OIDCProvider:                            os.Getenv("OIDC_PROVIDER"),
		OIDCIssuer:                              os.Getenv("OIDC_ISSUER"),
		OIDCDiscoveryURL:                        os.Getenv("OIDC_DISCOVERY_URL"),
		OIDCClientID:                            os.Getenv("OIDC_CLIENT_ID"),
		OIDCClientSecret:                        os.Getenv("OIDC_CLIENT_SECRET"),
		OIDCAudience:                            os.Getenv("OIDC_AUDIENCE"),
		OIDCScopes:                              os.Getenv("OIDC_SCOPES"),
		OIDCAllowInsecureHTTP:                   os.Getenv("OIDC_ALLOW_INSECURE_HTTP") == "true" || os.Getenv("OIDC_ALLOW_INSECURE_HTTP") == "1",
		ConnectedLoginProvider:                  os.Getenv("CONNECTED_LOGIN_PROVIDER"),
		ConnectedLoginOrigin:                    os.Getenv("CONNECTED_LOGIN_ORIGIN"),
		ConnectedLoginAPIOrigin:                 os.Getenv("CONNECTED_LOGIN_API_ORIGIN"),
		ConnectedLoginClientID:                  os.Getenv("CONNECTED_LOGIN_CLIENT_ID"),
		ConnectedLoginClientSecret:              os.Getenv("CONNECTED_LOGIN_CLIENT_SECRET"),
		ConnectedLoginLoginPath:                 os.Getenv("CONNECTED_LOGIN_LOGIN_PATH"),
		ConnectedLoginTokenPath:                 os.Getenv("CONNECTED_LOGIN_TOKEN_PATH"),
		ConnectedLoginUserinfoPath:              os.Getenv("CONNECTED_LOGIN_USERINFO_PATH"),
		ConnectedLoginReturnToParam:             os.Getenv("CONNECTED_LOGIN_RETURN_TO_PARAM"),
		ConnectedLoginSubjectClaim:              os.Getenv("CONNECTED_LOGIN_SUBJECT_CLAIM"),
		ConnectedLoginSubjectNamespaceClaim:     os.Getenv("CONNECTED_LOGIN_SUBJECT_NAMESPACE_CLAIM"),
		ConnectedLoginSubjectNamespaceSlugClaim: os.Getenv("CONNECTED_LOGIN_SUBJECT_NAMESPACE_SLUG_CLAIM"),
		ConnectedLoginActorTypeClaim:            os.Getenv("CONNECTED_LOGIN_ACTOR_TYPE_CLAIM"),
		ConnectedLoginHumanTypeValue:            os.Getenv("CONNECTED_LOGIN_HUMAN_TYPE_VALUE"),
		ConnectedLoginAgentTypeValue:            os.Getenv("CONNECTED_LOGIN_AGENT_TYPE_VALUE"),
		ConnectedLoginClientIDClaim:             os.Getenv("CONNECTED_LOGIN_CLIENT_ID_CLAIM"),
		ConnectedLoginClientNameClaim:           os.Getenv("CONNECTED_LOGIN_CLIENT_NAME_CLAIM"),
		ConnectedLoginPreferredUsernameClaim:    os.Getenv("CONNECTED_LOGIN_PREFERRED_USERNAME_CLAIM"),
		ConnectedLoginNameClaim:                 os.Getenv("CONNECTED_LOGIN_NAME_CLAIM"),
		ConnectedLoginPictureClaim:              os.Getenv("CONNECTED_LOGIN_PICTURE_CLAIM"),
		ConnectedLoginAvatarURLClaim:            os.Getenv("CONNECTED_LOGIN_AVATAR_URL_CLAIM"),
		ConnectedLoginDescriptionClaim:          os.Getenv("CONNECTED_LOGIN_DESCRIPTION_CLAIM"),
		ConnectedLoginScopeClaim:                os.Getenv("CONNECTED_LOGIN_SCOPE_CLAIM"),
		EnableWorkflowExec:                      os.Getenv("ENABLE_WORKFLOW_EXEC") == "true" || os.Getenv("ENABLE_WORKFLOW_EXEC") == "1",
		WorkflowExecImage:                       os.Getenv("WORKFLOW_EXEC_IMAGE"),
		WorkflowExecCPUs:                        os.Getenv("WORKFLOW_EXEC_CPUS"),
		WorkflowExecMemory:                      os.Getenv("WORKFLOW_EXEC_MEMORY"),
		WorkflowExecTmpfsSize:                   os.Getenv("WORKFLOW_EXEC_TMPFS_SIZE"),
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
	cfg.ConsoleBaseURL = firstNonEmpty(cfg.ConsoleBaseURL, "http://localhost:5173")
	cfg.GitRepoDir = firstNonEmpty(cfg.GitRepoDir, "gitrepos")
	cfg.ListenMode = firstNonEmpty(cfg.ListenMode, "development")
	cfg.Environment = strings.ToLower(strings.TrimSpace(firstNonEmpty(cfg.Environment, "production")))
	cfg.EmbeddingBaseURL = firstNonEmpty(cfg.EmbeddingBaseURL, "https://api.openai.com")
	cfg.EmbeddingModel = firstNonEmpty(cfg.EmbeddingModel, "text-embedding-3-small")
	cfg.OIDCScopes = firstNonEmpty(strings.TrimSpace(cfg.OIDCScopes), "openid profile email")
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
	if strings.TrimSpace(cfg.OIDCProvider) == "" && (cfg.OIDCIssuer != "" || cfg.OIDCDiscoveryURL != "" || cfg.OIDCClientID != "") {
		cfg.OIDCProvider = defaultOIDCProvider(cfg.OIDCIssuer, cfg.OIDCDiscoveryURL)
	}
	cfg.ConnectedLoginProvider = strings.TrimSpace(cfg.ConnectedLoginProvider)
	cfg.ConnectedLoginOrigin = strings.TrimSpace(cfg.ConnectedLoginOrigin)
	cfg.ConnectedLoginAPIOrigin = strings.TrimSpace(cfg.ConnectedLoginAPIOrigin)
	cfg.ConnectedLoginClientID = strings.TrimSpace(cfg.ConnectedLoginClientID)
	cfg.ConnectedLoginClientSecret = strings.TrimSpace(cfg.ConnectedLoginClientSecret)
	cfg.ConnectedLoginLoginPath = strings.TrimSpace(cfg.ConnectedLoginLoginPath)
	cfg.ConnectedLoginTokenPath = strings.TrimSpace(cfg.ConnectedLoginTokenPath)
	cfg.ConnectedLoginUserinfoPath = strings.TrimSpace(cfg.ConnectedLoginUserinfoPath)
	cfg.ConnectedLoginReturnToParam = strings.TrimSpace(cfg.ConnectedLoginReturnToParam)
	cfg.ConnectedLoginSubjectClaim = strings.TrimSpace(cfg.ConnectedLoginSubjectClaim)
	cfg.ConnectedLoginSubjectNamespaceClaim = strings.TrimSpace(cfg.ConnectedLoginSubjectNamespaceClaim)
	cfg.ConnectedLoginSubjectNamespaceSlugClaim = strings.TrimSpace(cfg.ConnectedLoginSubjectNamespaceSlugClaim)
	cfg.ConnectedLoginActorTypeClaim = strings.TrimSpace(cfg.ConnectedLoginActorTypeClaim)
	cfg.ConnectedLoginHumanTypeValue = strings.TrimSpace(cfg.ConnectedLoginHumanTypeValue)
	cfg.ConnectedLoginAgentTypeValue = strings.TrimSpace(cfg.ConnectedLoginAgentTypeValue)
	cfg.ConnectedLoginClientIDClaim = strings.TrimSpace(cfg.ConnectedLoginClientIDClaim)
	cfg.ConnectedLoginClientNameClaim = strings.TrimSpace(cfg.ConnectedLoginClientNameClaim)
	cfg.ConnectedLoginPreferredUsernameClaim = strings.TrimSpace(cfg.ConnectedLoginPreferredUsernameClaim)
	cfg.ConnectedLoginNameClaim = strings.TrimSpace(cfg.ConnectedLoginNameClaim)
	cfg.ConnectedLoginPictureClaim = strings.TrimSpace(cfg.ConnectedLoginPictureClaim)
	cfg.ConnectedLoginAvatarURLClaim = strings.TrimSpace(cfg.ConnectedLoginAvatarURLClaim)
	cfg.ConnectedLoginDescriptionClaim = strings.TrimSpace(cfg.ConnectedLoginDescriptionClaim)
	cfg.ConnectedLoginScopeClaim = strings.TrimSpace(cfg.ConnectedLoginScopeClaim)
	if err := validateConnectedLoginConfig(cfg); err != nil {
		return Config{}, err
	}
	if cfg.ConnectedLoginEnabled() {
		cfg.ConnectedLoginProvider = firstNonEmpty(cfg.ConnectedLoginProvider, "connected")
		cfg.ConnectedLoginLoginPath = firstNonEmpty(cfg.ConnectedLoginLoginPath, "/oauth/login")
		cfg.ConnectedLoginTokenPath = firstNonEmpty(cfg.ConnectedLoginTokenPath, "/api/oauth/token")
		cfg.ConnectedLoginUserinfoPath = firstNonEmpty(cfg.ConnectedLoginUserinfoPath, "/api/oauth/userinfo")
		cfg.ConnectedLoginReturnToParam = firstNonEmpty(cfg.ConnectedLoginReturnToParam, "return_to")
		cfg.ConnectedLoginSubjectClaim = firstNonEmpty(cfg.ConnectedLoginSubjectClaim, "sub")
		cfg.ConnectedLoginActorTypeClaim = firstNonEmpty(cfg.ConnectedLoginActorTypeClaim, "type")
		cfg.ConnectedLoginHumanTypeValue = firstNonEmpty(cfg.ConnectedLoginHumanTypeValue, "human")
		cfg.ConnectedLoginAgentTypeValue = firstNonEmpty(cfg.ConnectedLoginAgentTypeValue, "agent")
		cfg.ConnectedLoginClientIDClaim = firstNonEmpty(cfg.ConnectedLoginClientIDClaim, "client_id")
		cfg.ConnectedLoginClientNameClaim = firstNonEmpty(cfg.ConnectedLoginClientNameClaim, "client_name")
		cfg.ConnectedLoginPreferredUsernameClaim = firstNonEmpty(cfg.ConnectedLoginPreferredUsernameClaim, "preferred_username")
		cfg.ConnectedLoginNameClaim = firstNonEmpty(cfg.ConnectedLoginNameClaim, "name")
		cfg.ConnectedLoginPictureClaim = firstNonEmpty(cfg.ConnectedLoginPictureClaim, "picture")
		cfg.ConnectedLoginAvatarURLClaim = firstNonEmpty(cfg.ConnectedLoginAvatarURLClaim, "avatar_url")
		cfg.ConnectedLoginDescriptionClaim = firstNonEmpty(cfg.ConnectedLoginDescriptionClaim, "description")
		cfg.ConnectedLoginScopeClaim = firstNonEmpty(cfg.ConnectedLoginScopeClaim, "scope")
	}
	return cfg, nil
}

// ConnectedLoginEnabled reports whether connected login is configured.
func (c Config) ConnectedLoginEnabled() bool {
	return strings.TrimSpace(c.ConnectedLoginOrigin) != "" &&
		strings.TrimSpace(c.ConnectedLoginAPIOrigin) != "" &&
		strings.TrimSpace(c.ConnectedLoginClientID) != "" &&
		strings.TrimSpace(c.ConnectedLoginClientSecret) != ""
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

func defaultOIDCProvider(issuer, discoveryURL string) string {
	if looksLikeAuth0Issuer(issuer) || looksLikeAuth0Issuer(discoveryURL) {
		return "auth0"
	}
	return "oidc"
}

func looksLikeAuth0Issuer(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	return host == "auth0.com" || strings.HasSuffix(host, ".auth0.com")
}

func validateConnectedLoginConfig(cfg Config) error {
	type envValue struct {
		name  string
		value string
	}
	required := []envValue{
		{name: "CONNECTED_LOGIN_ORIGIN", value: cfg.ConnectedLoginOrigin},
		{name: "CONNECTED_LOGIN_API_ORIGIN", value: cfg.ConnectedLoginAPIOrigin},
		{name: "CONNECTED_LOGIN_CLIENT_ID", value: cfg.ConnectedLoginClientID},
		{name: "CONNECTED_LOGIN_CLIENT_SECRET", value: cfg.ConnectedLoginClientSecret},
	}
	optional := []envValue{
		{name: "CONNECTED_LOGIN_PROVIDER", value: cfg.ConnectedLoginProvider},
		{name: "CONNECTED_LOGIN_LOGIN_PATH", value: cfg.ConnectedLoginLoginPath},
		{name: "CONNECTED_LOGIN_TOKEN_PATH", value: cfg.ConnectedLoginTokenPath},
		{name: "CONNECTED_LOGIN_USERINFO_PATH", value: cfg.ConnectedLoginUserinfoPath},
		{name: "CONNECTED_LOGIN_RETURN_TO_PARAM", value: cfg.ConnectedLoginReturnToParam},
		{name: "CONNECTED_LOGIN_SUBJECT_CLAIM", value: cfg.ConnectedLoginSubjectClaim},
		{name: "CONNECTED_LOGIN_SUBJECT_NAMESPACE_CLAIM", value: cfg.ConnectedLoginSubjectNamespaceClaim},
		{name: "CONNECTED_LOGIN_SUBJECT_NAMESPACE_SLUG_CLAIM", value: cfg.ConnectedLoginSubjectNamespaceSlugClaim},
		{name: "CONNECTED_LOGIN_ACTOR_TYPE_CLAIM", value: cfg.ConnectedLoginActorTypeClaim},
		{name: "CONNECTED_LOGIN_HUMAN_TYPE_VALUE", value: cfg.ConnectedLoginHumanTypeValue},
		{name: "CONNECTED_LOGIN_AGENT_TYPE_VALUE", value: cfg.ConnectedLoginAgentTypeValue},
		{name: "CONNECTED_LOGIN_CLIENT_ID_CLAIM", value: cfg.ConnectedLoginClientIDClaim},
		{name: "CONNECTED_LOGIN_CLIENT_NAME_CLAIM", value: cfg.ConnectedLoginClientNameClaim},
		{name: "CONNECTED_LOGIN_PREFERRED_USERNAME_CLAIM", value: cfg.ConnectedLoginPreferredUsernameClaim},
		{name: "CONNECTED_LOGIN_NAME_CLAIM", value: cfg.ConnectedLoginNameClaim},
		{name: "CONNECTED_LOGIN_PICTURE_CLAIM", value: cfg.ConnectedLoginPictureClaim},
		{name: "CONNECTED_LOGIN_AVATAR_URL_CLAIM", value: cfg.ConnectedLoginAvatarURLClaim},
		{name: "CONNECTED_LOGIN_DESCRIPTION_CLAIM", value: cfg.ConnectedLoginDescriptionClaim},
		{name: "CONNECTED_LOGIN_SCOPE_CLAIM", value: cfg.ConnectedLoginScopeClaim},
	}
	var set, missing []string
	for _, item := range required {
		if strings.TrimSpace(item.value) == "" {
			missing = append(missing, item.name)
			continue
		}
		set = append(set, item.name)
	}
	for _, item := range optional {
		if strings.TrimSpace(item.value) != "" {
			set = append(set, item.name)
		}
	}
	if len(set) > 0 && len(missing) > 0 {
		return fmt.Errorf("connected login: partial configuration; set %v, missing %v", set, missing)
	}
	return nil
}
