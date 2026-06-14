package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"gorm.io/gorm"

	agsauth "github.com/ngaut/agent-git-service/auth"
	"github.com/ngaut/agent-git-service/config"
	"github.com/ngaut/agent-git-service/internal/connectedlogin"
	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/embedding"
	"github.com/ngaut/agent-git-service/internal/githttp"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/graphql"
	"github.com/ngaut/agent-git-service/internal/metrics"
	srvmiddleware "github.com/ngaut/agent-git-service/internal/middleware"
	"github.com/ngaut/agent-git-service/internal/oauth"
	"github.com/ngaut/agent-git-service/internal/oidc"
	"github.com/ngaut/agent-git-service/internal/rest"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/router"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

// gitSHA is set at build time via -ldflags.
var gitSHA = "unknown"

// Server exposes a programmatic server instance for embedders.
type Server struct {
	deps    *bootstrapDeps
	handler http.Handler
}

// Authenticator authenticates a request using host-provided identity. ok=false
// means no embedded identity was present and AGS should continue with its
// built-in token flow when applicable.
type Authenticator interface {
	Authenticate(*http.Request) (agsauth.Identity, bool, error)
}

type options struct {
	authenticator Authenticator
}

// Option configures the embeddable server surface.
type Option func(*options)

// WithAuthenticator installs a host-provided request authenticator.
func WithAuthenticator(authenticator Authenticator) Option {
	return func(opts *options) {
		opts.authenticator = authenticator
	}
}

// bootstrapDeps holds all initialized dependencies for the application.
type bootstrapDeps struct {
	Cfg          config.Config
	Options      options
	DB           *gorm.DB
	Embedder     embedding.Embedder
	Store        *gitstore.Store
	SrvCtx       context.Context
	SrvCancel    context.CancelFunc
	SvcDeps      *service.Service
	GqlSrv       *graphql.Server
	GitHandler   *githttp.Handler
	OauthHandler *oauth.Handler
	Handlers     *rest.Deps
	Mux          http.Handler
	Servers      []*http.Server
	Labels       []string
}

// bootstrapResult is returned by bootstrap and contains all initialized components.
type bootstrapResult struct {
	Deps    *bootstrapDeps
	Err     error
	Partial *bootstrapDeps // Contains successfully initialized deps if bootstrap failed midway
}

type coreDeps struct {
	cfg       config.Config
	cfgLoaded bool
	db        *gorm.DB
	embedder  embedding.Embedder
	store     *gitstore.Store
}

type serviceDeps struct {
	svc          *service.Service
	gqlSrv       *graphql.Server
	gitHandler   *githttp.Handler
	oauthHandler *oauth.Handler
}

type muxDeps struct {
	handlers *rest.Deps
	router   *chi.Mux
	mux      http.Handler
}

type serverDeps struct {
	servers []*http.Server
	labels  []string
}

type embeddedAuthenticatorAdapter struct {
	authenticator Authenticator
}

func (a embeddedAuthenticatorAdapter) Authenticate(r *http.Request) (srvmiddleware.EmbeddedIdentity, bool, error) {
	identity, ok, err := a.authenticator.Authenticate(r)
	if err != nil || !ok {
		return srvmiddleware.EmbeddedIdentity{}, ok, err
	}
	return srvmiddleware.EmbeddedIdentity{
		Provider:  identity.Provider,
		Subject:   identity.Subject,
		Login:     identity.Login,
		Name:      identity.Name,
		Email:     identity.Email,
		Groups:    append([]string(nil), identity.Groups...),
		SiteAdmin: identity.SiteAdmin,
	}, true, nil
}

func embeddedAuthConfig(opts options) srvmiddleware.EmbeddedAuthConfig {
	if opts.authenticator == nil {
		return srvmiddleware.EmbeddedAuthConfig{}
	}
	return srvmiddleware.EmbeddedAuthConfig{
		Authenticator: embeddedAuthenticatorAdapter{authenticator: opts.authenticator},
	}
}

func initCoreDepsFromConfig(cfg config.Config) (coreDeps, error) {
	var deps coreDeps
	deps.cfg = cfg
	deps.cfgLoaded = true

	// Refuse insecure token bypass mode in production.
	if cfg.AllowAnyToken && cfg.Environment == "production" {
		return deps, fmt.Errorf("ALLOW_ANY_TOKEN=true is not allowed when ENVIRONMENT=production")
	}

	database, err := db.Init(cfg.DBdsn)
	if err != nil {
		return deps, fmt.Errorf("db: %w", err)
	}
	deps.db = database

	if cfg.Environment == "development" {
		if err := db.Seed(database, cfg.AdminLogin, cfg.AdminToken); err != nil {
			return deps, fmt.Errorf("seed: %w", err)
		}
		slog.Info("seed applied", "environment", cfg.Environment)
	} else {
		slog.Info("seed skipped", "environment", cfg.Environment)
	}

	var embedder embedding.Embedder
	if cfg.EmbeddingAPIKey != "" {
		opts := []embedding.OpenAIOption{
			embedding.WithBaseURL(cfg.EmbeddingBaseURL),
			embedding.WithModel(cfg.EmbeddingModel),
		}
		if cfg.EmbeddingDimensions > 0 {
			opts = append(opts, embedding.WithDimensions(cfg.EmbeddingDimensions))
		}
		embedder = embedding.NewOpenAI(cfg.EmbeddingAPIKey, opts...)
		// Attempt to add VECTOR columns (best-effort, idempotent).
		if dims := embedder.Dimensions(); dims > 0 {
			db.InitVector(database, dims)
			slog.Info("embedding enabled", "model", cfg.EmbeddingModel, "dimensions", dims)
		} else {
			slog.Info("embedding enabled", "model", cfg.EmbeddingModel, "dimensions", "auto")
		}
	} else {
		embedder = embedding.NopEmbedder{}
		slog.Info("embedding disabled", "reason", "EMBEDDING_API_KEY not set")
	}
	deps.embedder = embedder

	store, err := gitstore.New(cfg.GitRepoDir)
	if err != nil {
		return deps, fmt.Errorf("gitstore: %w", err)
	}
	deps.store = store

	return deps, nil
}

func initServiceDeps(cfg config.Config, database *gorm.DB, store *gitstore.Store, embedder embedding.Embedder, srvCtx context.Context) (serviceDeps, error) {
	var deps serviceDeps
	dataRoot := cfg.GitRepoDir
	if strings.TrimSpace(dataRoot) == "" {
		dataRoot = "."
	}

	// Wiki catalog: a content-addressed blob store on the filesystem
	// plus the catalog primitive backed by the same database. The
	// blob root sits alongside the attachment root by convention so
	// operators only need to mount one persistent volume.
	wikiBlob := wikicatalog.NewBlobStore(dataRoot)
	wikiCat := wikicatalog.New(database, wikiBlob)

	svcDeps := &service.Service{
		Ctx:                 srvCtx,
		DB:                  database,
		Git:                 store,
		WikiCatalog:         wikiCat,
		WikiBlob:            wikiBlob,
		BaseURL:             cfg.BaseURL,
		AttachmentRoot:      dataRoot,
		Embedder:            embedder,
		AllowAnyToken:       cfg.AllowAnyToken,
		WorkflowExecEnabled: cfg.EnableWorkflowExec,
		WorkflowExecImage:   cfg.WorkflowExecImage,
		WorkflowExecTimeout: cfg.WorkflowExecTimeout,
		WorkflowExecCPUs:    cfg.WorkflowExecCPUs,
		WorkflowExecMemory:  cfg.WorkflowExecMemory,
		WorkflowExecPids:    cfg.WorkflowExecPidsLimit,
		WorkflowExecNoFile:  cfg.WorkflowExecNoFile,
		WorkflowExecTmpfs:   cfg.WorkflowExecTmpfsSize,
	}
	// Post-commit hook: drive the wiki search index from catalog
	// writes so Step 4 cutover does not leave wiki_search_documents
	// stale. The hook is best-effort — failures log and do not roll
	// back the catalog commit.
	wikiCat.OnChangeSetCommitted = svcDeps.WikiCatalogPostCommit
	// Route catalog writes through the same context-aware DB accessor the
	// service layer uses so transactions and request cancellation propagate.
	wikiCat.DBFor = svcDeps.DBForCtx
	// Initialize PresenceHub for collaborative conversation presence
	svcDeps.PresenceHub = service.NewPresenceHub(database)
	deps.svc = svcDeps

	if cfg.AllowAnyToken {
		slog.Warn("ALLOW_ANY_TOKEN is enabled; any non-empty token is accepted when no tokens exist in DB")
	}
	if cfg.EnableWorkflowExec {
		slog.Info("workflow execution sandbox enabled",
			"image", cfg.WorkflowExecImage,
			"timeout", cfg.WorkflowExecTimeout,
			"cpus", cfg.WorkflowExecCPUs,
			"memory", cfg.WorkflowExecMemory,
			"pids_limit", cfg.WorkflowExecPidsLimit,
			"nofile_limit", cfg.WorkflowExecNoFile,
			"tmpfs_size", cfg.WorkflowExecTmpfsSize,
		)
	} else {
		slog.Warn("workflow execution disabled; set ENABLE_WORKFLOW_EXEC=1 to allow sandboxed workflow steps")
	}
	if cfg.ConnectedLoginEnabled() {
		c, err := connectedlogin.New(connectedlogin.Config{
			Provider:                  cfg.ConnectedLoginProvider,
			Origin:                    cfg.ConnectedLoginOrigin,
			APIOrigin:                 cfg.ConnectedLoginAPIOrigin,
			ClientID:                  cfg.ConnectedLoginClientID,
			ClientSecret:              cfg.ConnectedLoginClientSecret,
			CallbackBaseURL:           cfg.BaseURL,
			LoginPath:                 cfg.ConnectedLoginLoginPath,
			TokenPath:                 cfg.ConnectedLoginTokenPath,
			UserinfoPath:              cfg.ConnectedLoginUserinfoPath,
			ReturnToParam:             cfg.ConnectedLoginReturnToParam,
			SubjectClaim:              cfg.ConnectedLoginSubjectClaim,
			SubjectNamespaceClaim:     cfg.ConnectedLoginSubjectNamespaceClaim,
			SubjectNamespaceSlugClaim: cfg.ConnectedLoginSubjectNamespaceSlugClaim,
			ActorTypeClaim:            cfg.ConnectedLoginActorTypeClaim,
			HumanTypeValue:            cfg.ConnectedLoginHumanTypeValue,
			AgentTypeValue:            cfg.ConnectedLoginAgentTypeValue,
			ClientIDClaim:             cfg.ConnectedLoginClientIDClaim,
			ClientNameClaim:           cfg.ConnectedLoginClientNameClaim,
			PreferredUsernameClaim:    cfg.ConnectedLoginPreferredUsernameClaim,
			NameClaim:                 cfg.ConnectedLoginNameClaim,
			PictureClaim:              cfg.ConnectedLoginPictureClaim,
			AvatarURLClaim:            cfg.ConnectedLoginAvatarURLClaim,
			DescriptionClaim:          cfg.ConnectedLoginDescriptionClaim,
			ScopeClaim:                cfg.ConnectedLoginScopeClaim,
		})
		if err != nil {
			return deps, fmt.Errorf("connectedlogin: %w", err)
		}
		svcDeps.ConnectedLogin = c
		slog.Info("connected login enabled",
			"provider", cfg.ConnectedLoginProvider,
			"client_id", cfg.ConnectedLoginClientID,
			"origin", cfg.ConnectedLoginOrigin,
			"callback", c.CallbackURL(),
		)
	} else {
		slog.Info("connected login disabled", "reason", "CONNECTED_LOGIN_* configuration not set")
	}
	if cfg.OIDCProvider != "" && cfg.OIDCClientID != "" && (cfg.OIDCIssuer != "" || cfg.OIDCDiscoveryURL != "") {
		c, err := oidc.New(oidc.Config{
			Provider:          cfg.OIDCProvider,
			Issuer:            cfg.OIDCIssuer,
			DiscoveryURL:      cfg.OIDCDiscoveryURL,
			ClientID:          cfg.OIDCClientID,
			ClientSecret:      cfg.OIDCClientSecret,
			Audience:          cfg.OIDCAudience,
			Scopes:            cfg.OIDCScopes,
			AllowInsecureHTTP: cfg.OIDCAllowInsecureHTTP,
		})
		if err != nil {
			return deps, fmt.Errorf("oidc: %w", err)
		}
		svcDeps.OIDC = c
		slog.Info("oidc enabled", "provider", cfg.OIDCProvider, "issuer", cfg.OIDCIssuer)
	} else {
		slog.Info("oidc disabled", "reason", "OIDC_ISSUER/OIDC_CLIENT_ID not set")
	}
	deps.gqlSrv = graphql.NewServer(svcDeps)
	deps.gitHandler = githttp.New(store, svcDeps)
	deps.oauthHandler = oauth.New(svcDeps)

	return deps, nil
}

// httpMuxConfig holds all dependencies required to build the HTTP multiplexer.
// This struct reduces parameter count in buildHTTPMux and related functions.
type httpMuxConfig struct {
	Cfg          config.Config
	Database     *gorm.DB
	ServiceDeps  *service.Service
	GQLServer    *graphql.Server
	GitHandler   *githttp.Handler
	OAuthHandler *oauth.Handler
	Version      string
	EmbeddedAuth srvmiddleware.EmbeddedAuthConfig
}

func buildHTTPMux(cfg httpMuxConfig) (muxDeps, error) {
	handlers := &rest.Deps{
		Svc:            cfg.ServiceDeps,
		ConsoleBaseURL: cfg.Cfg.ConsoleBaseURL,
		Presence:       &rest.PresenceHandlers{Svc: cfg.ServiceDeps, Hub: cfg.ServiceDeps.PresenceHub},
	}

	r := chi.NewRouter()
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.RequestID)
	r.Use(srvmiddleware.RequestIDResponseHeader())
	r.Use(srvmiddleware.RequestLogging())
	r.Use(srvmiddleware.Recoverer())
	metricsHandler := metrics.Init()
	r.Use(srvmiddleware.MetricsInstrumentation())

	hostMux := router.RegisterRoutes(r, handlers, cfg.GitHandler, cfg.GQLServer, cfg.OAuthHandler, cfg.Cfg.ConsoleBaseURL, cfg.EmbeddedAuth)
	mux := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		transform.Wrap(cfg.Cfg.BaseURL, func() {
			hostMux.ServeHTTP(w, req)
		})
	})
	r.Get("/metrics", metricsHandler.ServeHTTP)

	// Register readiness probe.
	r.Get("/readyz", readyzHandler(readyzConfig{
		MainDB:  cfg.Database,
		Version: cfg.Version,
	}))

	return muxDeps{handlers: handlers, router: r, mux: mux}, nil
}

func buildServers(cfg config.Config, mux http.Handler) (serverDeps, error) {
	addr := ":" + cfg.Port

	if cfg.ListenMode == "production" {
		return serverDeps{
			servers: []*http.Server{
				{Addr: addr, Handler: mux},
			},
			labels: []string{
				fmt.Sprintf("http://0.0.0.0:%s", cfg.Port),
			},
		}, nil
	}

	tlsCert, err := tls.LoadX509KeyPair("cert.pem", "key.pem")
	if err != nil {
		return serverDeps{}, fmt.Errorf("TLS: %w", err)
	}
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{tlsCert}}

	return serverDeps{
		servers: []*http.Server{
			{Addr: ":443", Handler: mux, TLSConfig: tlsCfg},
			{Addr: addr, Handler: mux, TLSConfig: tlsCfg},
			{Addr: ":8081", Handler: mux, TLSConfig: tlsCfg},
			{Addr: ":80", Handler: mux},
			{Addr: ":4003", Handler: mux},
		},
		labels: []string{
			"https://github.localhost:443",
			fmt.Sprintf("https://localhost:%s", cfg.Port),
			"https://github.localhost:8081",
			"http://github.localhost:80",
			"http://github.localhost:4003",
		},
	}, nil
}

// bootstrap initializes all application dependencies in order.
// It returns a bootstrapResult with either fully initialized deps or partial deps on failure.
func bootstrap() bootstrapResult {
	cfg, err := config.New()
	if err != nil {
		return bootstrapResult{Deps: &bootstrapDeps{}, Err: fmt.Errorf("config: %w", err)}
	}
	return bootstrapWithConfig(cfg, options{})
}

func bootstrapWithConfig(cfg config.Config, opts options) bootstrapResult {
	result := bootstrapResult{
		Deps: &bootstrapDeps{},
	}
	deps := result.Deps
	deps.Options = opts

	// 1. Core dependencies (config, database, embedding, gitstore).
	core, err := initCoreDepsFromConfig(cfg)
	if err != nil {
		result.Err = err
		if core.cfgLoaded {
			partial := &bootstrapDeps{Cfg: core.cfg}
			if core.db != nil {
				partial.DB = core.db
			}
			if core.embedder != nil {
				partial.Embedder = core.embedder
			}
			if core.store != nil {
				partial.Store = core.store
			}
			result.Partial = partial
		}
		return result
	}
	deps.Cfg = core.cfg
	deps.DB = core.db
	deps.Embedder = core.embedder
	deps.Store = core.store

	// 2. Server-level context.
	srvCtx, srvCancel := context.WithCancel(context.Background())
	deps.SrvCtx = srvCtx
	deps.SrvCancel = srvCancel

	// 3. Service dependencies, OIDC, and handlers.
	svc, err := initServiceDeps(core.cfg, core.db, core.store, core.embedder, srvCtx)
	if err != nil {
		result.Err = err
		result.Partial = &bootstrapDeps{Cfg: core.cfg, DB: core.db, Embedder: core.embedder, Store: core.store, SrvCtx: srvCtx, SrvCancel: srvCancel, SvcDeps: svc.svc}
		return result
	}
	deps.SvcDeps = svc.svc
	deps.GqlSrv = svc.gqlSrv
	deps.GitHandler = svc.gitHandler
	deps.OauthHandler = svc.oauthHandler

	// 4. Build router and host-aware mux.
	mux, err := buildHTTPMux(httpMuxConfig{
		Cfg:          core.cfg,
		Database:     core.db,
		ServiceDeps:  svc.svc,
		GQLServer:    svc.gqlSrv,
		GitHandler:   svc.gitHandler,
		OAuthHandler: svc.oauthHandler,
		Version:      gitSHA,
		EmbeddedAuth: embeddedAuthConfig(opts),
	})
	if err != nil {
		result.Err = err
		result.Partial = &bootstrapDeps{
			Cfg:          core.cfg,
			DB:           core.db,
			Embedder:     core.embedder,
			Store:        core.store,
			SrvCtx:       srvCtx,
			SrvCancel:    srvCancel,
			SvcDeps:      svc.svc,
			GqlSrv:       svc.gqlSrv,
			GitHandler:   svc.gitHandler,
			OauthHandler: svc.oauthHandler,
		}
		return result
	}
	deps.Handlers = mux.handlers
	deps.Mux = mux.mux

	// 6. Set up HTTP servers.
	srvs, err := buildServers(core.cfg, mux.mux)
	if err != nil {
		result.Err = err
		result.Partial = &bootstrapDeps{
			Cfg:          core.cfg,
			DB:           core.db,
			Embedder:     core.embedder,
			Store:        core.store,
			SrvCtx:       srvCtx,
			SrvCancel:    srvCancel,
			SvcDeps:      svc.svc,
			GqlSrv:       svc.gqlSrv,
			GitHandler:   svc.gitHandler,
			OauthHandler: svc.oauthHandler,
		}
		return result
	}
	deps.Servers = srvs.servers
	deps.Labels = srvs.labels

	return result
}

// shutdownConfig holds configuration for shutdown behavior.
type shutdownConfig struct {
	GracePeriod time.Duration
}

// shutdownResult captures the results of shutdown operations.
type shutdownResult struct {
	HTTPShutdownErrors []error
	BgDrained          bool
	BgDrainTimedOut    bool
	ContextCanceled    bool
}

// waitForWaitGroup waits for a wait group to complete or context to timeout.
func waitForWaitGroup(ctx context.Context, wg *sync.WaitGroup, name string, drainedFlag *bool, timedOutFlag *bool) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		*drainedFlag = true
		slog.Info("wait group drained", "name", name)
	case <-ctx.Done():
		*timedOutFlag = true
		slog.Warn("wait group drain timed out", "name", name, "error", ctx.Err())
	}
}

// shutdown gracefully stops all servers and waits for background workers.
func shutdown(deps *bootstrapDeps, cfg shutdownConfig) shutdownResult {
	result := shutdownResult{}

	// Shutdown HTTP servers.
	ctx, cancel := context.WithTimeout(context.Background(), cfg.GracePeriod)
	defer cancel()

	for _, srv := range deps.Servers {
		if err := srv.Shutdown(ctx); err != nil {
			result.HTTPShutdownErrors = append(result.HTTPShutdownErrors, err)
		}
	}
	slog.Info("all listeners stopped; waiting for background goroutines")

	// Cancel the server context so background goroutines observe Done and begin draining.
	deps.SrvCancel()
	result.ContextCanceled = true

	// Wait for background goroutines (svcDeps.Wg).
	waitForWaitGroup(ctx, &deps.SvcDeps.Wg, "background goroutines (svcDeps)", &result.BgDrained, &result.BgDrainTimedOut)

	return result
}

func run(sigCh <-chan struct{}, shutdownCfg shutdownConfig) error {
	result := bootstrap()
	if result.Err != nil {
		return result.Err
	}
	deps := result.Deps
	startRuntimeWorkers(deps)

	// Start HTTP servers.
	for i, srv := range deps.Servers {
		lbl := deps.Labels[i]
		s := srv
		go func() {
			fmt.Printf("gh-server listening on %s\n", lbl)
			var err error
			if s.TLSConfig != nil {
				err = s.ListenAndServeTLS("", "")
			} else {
				err = s.ListenAndServe()
			}
			if err != nil && err != http.ErrServerClosed {
				slog.Error("listener exited unexpectedly", "listener", lbl, "error", err)
			}
		}()
	}

	// Handle shutdown signals.
	<-sigCh
	slog.Info("shutdown initiated", "grace_period", shutdownCfg.GracePeriod.String())

	shutdownResult := shutdown(deps, shutdownCfg)
	_ = shutdownResult // Can be used for logging/metrics in production
	return nil
}

// RunWikiReindex reindexes wiki search data for one repo or all repos.
func RunWikiReindex(args []string) error {
	result := bootstrap()
	if result.Err != nil {
		return result.Err
	}
	deps := result.Deps
	defer deps.SrvCancel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		count, err := deps.SvcDeps.ReindexWikiSearch(ctx, strings.TrimSpace(args[0]))
		if err != nil {
			return err
		}
		fmt.Printf("wiki-reindex repo=%s indexed=%d\n", strings.TrimSpace(args[0]), count)
		return nil
	}

	count, err := deps.SvcDeps.ReindexAllWikiSearch(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("wiki-reindex indexed=%d\n", count)
	return nil
}

// Run starts the gh-server listeners and blocks until shutdown is requested.
func Run(sigCh <-chan struct{}) error {
	return run(sigCh, shutdownConfig{GracePeriod: 10 * time.Second})
}

// New constructs a server from a caller-supplied config.
func New(cfg config.Config, opts ...Option) (*Server, error) {
	normalized, err := config.Normalize(cfg)
	if err != nil {
		return nil, err
	}
	parsed := options{}
	for _, opt := range opts {
		if opt != nil {
			opt(&parsed)
		}
	}
	result := bootstrapWithConfig(normalized, parsed)
	if result.Err != nil {
		return nil, result.Err
	}
	startRuntimeWorkers(result.Deps)
	return &Server{
		deps:    result.Deps,
		handler: result.Deps.Mux,
	}, nil
}

// Handler returns the fully wired application handler tree.
func (s *Server) Handler() http.Handler { return s.handler }

// Shutdown gracefully stops listeners and background work.
func (s *Server) Shutdown(ctx context.Context) error {
	var errs []string
	for _, srv := range s.deps.Servers {
		if err := srv.Shutdown(ctx); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if s.deps.SrvCancel != nil {
		s.deps.SrvCancel()
	}
	done := make(chan struct{})
	go func() {
		s.deps.SvcDeps.Wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		errs = append(errs, ctx.Err().Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("shutdown: %s", strings.Join(errs, "; "))
	}
	return nil
}

// readyzHandler returns an http.HandlerFunc that pings the main DB. Returns
// 200 when backing stores are reachable, 503 otherwise.
// readyzConfig holds dependencies for the readiness probe handler.
// This struct reduces parameter count and improves clarity.
type readyzConfig struct {
	MainDB  *gorm.DB
	Version string
}

func readyzHandler(cfg readyzConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		w.Header().Set("Content-Type", "application/json")

		type checkResult struct {
			Status string `json:"status"`
			Error  string `json:"error,omitempty"`
		}
		checks := map[string]checkResult{}
		healthy := true

		// Check main DB.
		if sqlDB, err := cfg.MainDB.DB(); err != nil {
			checks["main_db"] = checkResult{Status: "unavailable", Error: err.Error()}
			healthy = false
		} else if err := sqlDB.PingContext(ctx); err != nil {
			checks["main_db"] = checkResult{Status: "unavailable", Error: err.Error()}
			healthy = false
		} else {
			checks["main_db"] = checkResult{Status: "ok"}
		}

		status := "ready"
		code := http.StatusOK
		if !healthy {
			status = "not_ready"
			code = http.StatusServiceUnavailable
		}
		metrics.ObserveReadyz(status)
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  status,
			"version": cfg.Version,
			"checks":  checks,
		})
	}
}
