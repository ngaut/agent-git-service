package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	agsauth "github.com/ngaut/agent-git-service/auth"
	"github.com/ngaut/agent-git-service/config"
	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/embedding"
	"github.com/ngaut/agent-git-service/internal/githttp"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/graphql"
	"github.com/ngaut/agent-git-service/internal/oauth"
	"github.com/ngaut/agent-git-service/internal/rest"
	"github.com/ngaut/agent-git-service/internal/router"
	"github.com/ngaut/agent-git-service/internal/service"
)

type headerAuthenticator struct {
	header   string
	identity agsauth.Identity
}

func (a headerAuthenticator) Authenticate(r *http.Request) (agsauth.Identity, bool, error) {
	value := r.Header.Get(a.header)
	if value == "" {
		return agsauth.Identity{}, false, nil
	}
	if value == "bad" {
		return agsauth.Identity{}, false, errors.New("bad embedded identity")
	}
	return a.identity, true, nil
}

func TestInitServiceDeps_EnablesGenericOIDC(t *testing.T) {
	mainDB := openTestDB(t)
	tmpDir := t.TempDir()

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatalf("gitstore: %v", err)
	}

	deps, err := initServiceDeps(config.Config{
		BaseURL:               "http://localhost:8080",
		DBdsn:                 "ignored-by-test",
		GitRepoDir:            tmpDir,
		OIDCProvider:          "casdoor",
		OIDCIssuer:            "http://localhost:8891/",
		OIDCClientID:          "oidc-client-id",
		OIDCAllowInsecureHTTP: true,
		WorkflowExecImage:     "bash:5.2",
		WorkflowExecTimeout:   2 * time.Minute,
		WorkflowExecCPUs:      "1.0",
		WorkflowExecMemory:    "256m",
		WorkflowExecPidsLimit: 128,
		WorkflowExecNoFile:    1024,
		WorkflowExecTmpfsSize: "64m",
	}, mainDB, store, nil, context.Background())
	if err != nil {
		t.Fatalf("initServiceDeps: %v", err)
	}

	if deps.svc.OIDC == nil {
		t.Fatal("expected generic OIDC client to be configured")
	}
	if got := deps.svc.OIDC.Provider(); got != "casdoor" {
		t.Fatalf("expected generic OIDC provider casdoor, got %q", got)
	}
}

func TestInitServiceDeps_EnablesConnectedLogin(t *testing.T) {
	mainDB := openTestDB(t)
	tmpDir := t.TempDir()

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatalf("gitstore: %v", err)
	}

	deps, err := initServiceDeps(config.Config{
		BaseURL:                             "https://ags.example.com",
		DBdsn:                               "ignored-by-test",
		GitRepoDir:                          tmpDir,
		ConnectedLoginProvider:              "provider",
		ConnectedLoginOrigin:                "https://app.provider.example",
		ConnectedLoginAPIOrigin:             "https://api.provider.example",
		ConnectedLoginClientID:              "connected-client",
		ConnectedLoginClientSecret:          "connected-secret",
		ConnectedLoginLoginPath:             "/oauth/login",
		ConnectedLoginSubjectNamespaceClaim: "workspace_id",
		WorkflowExecImage:                   "bash:5.2",
		WorkflowExecTimeout:                 2 * time.Minute,
		WorkflowExecCPUs:                    "1.0",
		WorkflowExecMemory:                  "256m",
		WorkflowExecPidsLimit:               128,
		WorkflowExecNoFile:                  1024,
		WorkflowExecTmpfsSize:               "64m",
	}, mainDB, store, nil, context.Background())
	if err != nil {
		t.Fatalf("initServiceDeps: %v", err)
	}

	if deps.svc.ConnectedLogin == nil {
		t.Fatal("expected connected login client to be configured")
	}
	loginURL, err := url.Parse(deps.svc.ConnectedLogin.LoginURL("csrf-state"))
	if err != nil {
		t.Fatalf("parse login URL: %v", err)
	}
	if loginURL.Scheme != "https" || loginURL.Host != "app.provider.example" || loginURL.Path != "/oauth/login" {
		t.Fatalf("unexpected login URL: %s", loginURL.String())
	}
	if got := loginURL.Query().Get("client_id"); got != "connected-client" {
		t.Fatalf("client_id: got %q", got)
	}
	if got := loginURL.Query().Get("return_to"); got != "https://ags.example.com/auth/connected/callback" {
		t.Fatalf("return_to: got %q", got)
	}
	if got := loginURL.Query().Get("state"); got != "csrf-state" {
		t.Fatalf("state: got %q", got)
	}
}

func TestMain_SignalDrivenShutdown(t *testing.T) {
	setupBootstrapEnv(t, map[string]string{
		"BASE_URL": "http://localhost:0",
		"PORT":     "0",
	})

	sigCh := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(sigCh, shutdownConfig{GracePeriod: 200 * time.Millisecond})
	}()

	sigCh <- struct{}{}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run failed: %v", err)
		}
	case <-time.After(15 * time.Second):
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

	deps := &bootstrapDeps{
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
	result := shutdown(deps, shutdownConfig{GracePeriod: 5 * time.Second})

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

	deps := &bootstrapDeps{
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
	result := shutdown(deps, shutdownConfig{GracePeriod: 100 * time.Millisecond})

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

	handler := readyzHandler(readyzConfig{
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
	if len(checks) != 1 {
		t.Fatalf("expected only main_db check, got %v", checks)
	}
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

	handler := readyzHandler(readyzConfig{
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
	mux := router.RegisterRoutes(r, restDeps, gitHandler, gqlSrv, oauthHandler, "http://console.localhost")
	r.Get("/readyz", readyzHandler(readyzConfig{
		MainDB: mainDB,
	}))

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNew_HandlerUsesHostAwareMuxAndPerServerTransformState(t *testing.T) {
	makeServer := func(t *testing.T, name, baseURL string) *Server {
		t.Helper()
		root := t.TempDir()
		srv, err := New(config.Config{
			DBdsn:       createBootstrapDSN(t, "server_"+name),
			GitRepoDir:  filepath.Join(root, "repos"),
			BaseURL:     baseURL,
			ListenMode:  "production",
			Environment: "production",
		})
		if err != nil {
			t.Fatalf("New(%s): %v", name, err)
		}
		return srv
	}

	alpha := makeServer(t, "alpha", "http://alpha.local")
	beta := makeServer(t, "beta", "http://beta.local")

	assertMeta := func(t *testing.T, srv *Server, want string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "http://api.github.localhost/", nil)
		req.Host = "api.github.localhost"
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode meta response: %v", err)
		}
		if got := body["openapi_url"]; got != want {
			t.Fatalf("expected openapi_url %q, got %v", want, got)
		}
	}

	assertMeta(t, alpha, "http://alpha.local/api/v3/openapi.json")
	assertMeta(t, beta, "http://beta.local/api/v3/openapi.json")
	assertMeta(t, alpha, "http://alpha.local/api/v3/openapi.json")
}

func TestNew_RESTHandlerUsesDefaultPrefixInResponseURLs(t *testing.T) {
	root := t.TempDir()
	srv, err := New(config.Config{
		DBdsn:       createBootstrapDSN(t, "server_rest_prefix"),
		GitRepoDir:  filepath.Join(root, "repos"),
		BaseURL:     "http://embed.local",
		ListenMode:  "production",
		Environment: "production",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	admin := db.User{Login: "admin", Name: "Admin", Type: "User", Status: "active"}
	if err := srv.deps.SvcDeps.DB.FirstOrCreate(&admin, db.User{Login: "admin"}).Error; err != nil {
		t.Fatalf("seed admin user: %v", err)
	}
	if _, err := srv.deps.SvcDeps.CreateRepo(context.Background(), service.CreateRepoInput{
		OwnerLogin: "admin",
		Name:       "prefix-check",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/repos/admin/prefix-check", nil)
	rec := httptest.NewRecorder()
	srv.RESTHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode repo response: %v", err)
	}
	if got := body["issues_url"]; got != "http://embed.local/api/v3/repos/admin/prefix-check/issues{/number}" {
		t.Fatalf("issues_url = %v, want default REST prefix", got)
	}
	if got := body["branches_url"]; got != "http://embed.local/api/v3/repos/admin/prefix-check/branches{/branch}" {
		t.Fatalf("branches_url = %v, want default REST prefix", got)
	}
}

func TestNew_GraphQLHandlerRequiresRouteEquivalentAuth(t *testing.T) {
	root := t.TempDir()
	srv, err := New(config.Config{
		DBdsn:       createBootstrapDSN(t, "server_graphql_auth"),
		GitRepoDir:  filepath.Join(root, "repos"),
		BaseURL:     "http://embed.local",
		ListenMode:  "production",
		Environment: "production",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	query, err := json.Marshal(map[string]any{"query": `{ viewer { login } }`})
	if err != nil {
		t.Fatalf("marshal query: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(query))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.GraphQLHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}

	admin := db.User{Login: "admin", Name: "Admin", Type: "User", Status: "active"}
	if err := srv.deps.SvcDeps.DB.FirstOrCreate(&admin, db.User{Login: "admin"}).Error; err != nil {
		t.Fatalf("seed admin user: %v", err)
	}
	if err := srv.deps.SvcDeps.DB.Create(&db.Token{UserID: admin.ID, Value: "embed-token"}).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}

	authReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(query))
	authReq.Header.Set("Content-Type", "application/json")
	authReq.Header.Set("Authorization", "token embed-token")
	authRec := httptest.NewRecorder()
	srv.GraphQLHandler().ServeHTTP(authRec, authReq)
	if authRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", authRec.Code, authRec.Body.String())
	}
}

func TestNew_GitHTTPHandlerIsGitOnly(t *testing.T) {
	root := t.TempDir()
	srv, err := New(config.Config{
		DBdsn:       createBootstrapDSN(t, "server_git_only"),
		GitRepoDir:  filepath.Join(root, "repos"),
		BaseURL:     "http://embed.local",
		ListenMode:  "production",
		Environment: "production",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v3/user", nil)
	rec := httptest.NewRecorder()
	srv.GitHTTPHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected git-only handler to return 404 for non-git paths, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNew_EmbeddedIdentitySupportsRESTGraphQLAndGitHTTP(t *testing.T) {
	root := t.TempDir()
	srv, err := New(config.Config{
		DBdsn:       createBootstrapDSN(t, "server_embedded_auth"),
		GitRepoDir:  filepath.Join(root, "repos"),
		BaseURL:     "http://embed.local",
		ListenMode:  "production",
		Environment: "production",
	}, WithAuthenticator(headerAuthenticator{
		header: "X-Embedded-User",
		identity: agsauth.Identity{
			Provider: "meshx",
			Subject:  "subject-1",
			Login:    "gateway-user",
			Name:     "Gateway User",
			Email:    "gateway@example.com",
		},
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	restReq := httptest.NewRequest(http.MethodGet, "/user", nil)
	restReq.Header.Set("X-Embedded-User", "ok")
	restRec := httptest.NewRecorder()
	srv.RESTHandler().ServeHTTP(restRec, restReq)
	if restRec.Code != http.StatusOK {
		t.Fatalf("embedded REST auth: expected 200, got %d: %s", restRec.Code, restRec.Body.String())
	}
	var restBody map[string]any
	if err := json.Unmarshal(restRec.Body.Bytes(), &restBody); err != nil {
		t.Fatalf("decode REST body: %v", err)
	}
	if got := restBody["login"]; got != "gateway-user" {
		t.Fatalf("REST login = %v, want gateway-user", got)
	}

	user, err := srv.deps.SvcDeps.GetUser(context.Background(), "gateway-user")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if _, err := srv.deps.SvcDeps.CreateRepo(service.ContextWithUser(context.Background(), user), service.CreateRepoInput{
		OwnerLogin: user.Login,
		Name:       "embedded-private",
		Private:    true,
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if _, err := srv.deps.SvcDeps.CreateRepo(service.ContextWithUser(context.Background(), user), service.CreateRepoInput{
		OwnerLogin: user.Login,
		Name:       "embedded-public",
		Private:    false,
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo public: %v", err)
	}

	query, err := json.Marshal(map[string]any{"query": `{ viewer { login } }`})
	if err != nil {
		t.Fatalf("marshal GraphQL query: %v", err)
	}
	gqlReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(query))
	gqlReq.Header.Set("Content-Type", "application/json")
	gqlReq.Header.Set("X-Embedded-User", "ok")
	gqlRec := httptest.NewRecorder()
	srv.GraphQLHandler().ServeHTTP(gqlRec, gqlReq)
	if gqlRec.Code != http.StatusOK {
		t.Fatalf("embedded GraphQL auth: expected 200, got %d: %s", gqlRec.Code, gqlRec.Body.String())
	}
	if !bytes.Contains(gqlRec.Body.Bytes(), []byte(`"login":"gateway-user"`)) {
		t.Fatalf("embedded GraphQL body missing resolved login: %s", gqlRec.Body.String())
	}

	gitReq := httptest.NewRequest(http.MethodGet, "/gateway-user/embedded-private.git/info/refs?service=git-upload-pack", nil)
	gitReq.Header.Set("X-Embedded-User", "ok")
	gitRec := httptest.NewRecorder()
	srv.GitHTTPHandler().ServeHTTP(gitRec, gitReq)
	if gitRec.Code != http.StatusOK {
		t.Fatalf("embedded Git auth: expected 200, got %d: %s", gitRec.Code, gitRec.Body.String())
	}

	createIssueBody, err := json.Marshal(map[string]any{
		"title": "embedded write",
		"body":  "created via embedded identity",
	})
	if err != nil {
		t.Fatalf("marshal create issue body: %v", err)
	}
	writeReq := httptest.NewRequest(http.MethodPost, "/repos/gateway-user/embedded-public/issues", bytes.NewReader(createIssueBody))
	writeReq.Header.Set("Content-Type", "application/json")
	writeReq.Header.Set("X-Embedded-User", "ok")
	writeRec := httptest.NewRecorder()
	srv.RESTHandler().ServeHTTP(writeRec, writeReq)
	if writeRec.Code != http.StatusCreated {
		t.Fatalf("embedded REST write auth: expected 201, got %d: %s", writeRec.Code, writeRec.Body.String())
	}
	var issueBody map[string]any
	if err := json.Unmarshal(writeRec.Body.Bytes(), &issueBody); err != nil {
		t.Fatalf("decode issue body: %v", err)
	}
	if got := issueBody["title"]; got != "embedded write" {
		t.Fatalf("issue title = %v, want embedded write", got)
	}

	rateReq := httptest.NewRequest(http.MethodGet, "/rate_limit", nil)
	rateReq.Header.Set("X-Embedded-User", "ok")
	rateRec := httptest.NewRecorder()
	srv.RESTHandler().ServeHTTP(rateRec, rateReq)
	if rateRec.Code != http.StatusOK {
		t.Fatalf("embedded rate_limit auth: expected 200, got %d: %s", rateRec.Code, rateRec.Body.String())
	}
	if got := rateRec.Header().Get("X-RateLimit-Limit"); got != "1000" {
		t.Fatalf("embedded rate_limit header = %q, want 1000", got)
	}

	if err := srv.deps.SvcDeps.StarRepo(service.ContextWithUser(context.Background(), user), "gateway-user/embedded-private", user.Login); err != nil {
		t.Fatalf("StarRepo private: %v", err)
	}
	starredReq := httptest.NewRequest(http.MethodGet, "/users/gateway-user/starred", nil)
	starredReq.Header.Set("X-Embedded-User", "ok")
	starredRec := httptest.NewRecorder()
	srv.RESTHandler().ServeHTTP(starredRec, starredReq)
	if starredRec.Code != http.StatusOK {
		t.Fatalf("embedded starred auth: expected 200, got %d: %s", starredRec.Code, starredRec.Body.String())
	}
	var starredBody []map[string]any
	if err := json.Unmarshal(starredRec.Body.Bytes(), &starredBody); err != nil {
		t.Fatalf("decode starred body: %v", err)
	}
	if len(starredBody) != 1 {
		t.Fatalf("starred repo count = %d, want 1", len(starredBody))
	}
	if got := starredBody[0]["full_name"]; got != "gateway-user/embedded-private" {
		t.Fatalf("starred repo full_name = %v, want gateway-user/embedded-private", got)
	}
}

func TestNew_EmbeddedIdentityPreservesAnonymousOptionalRoutes(t *testing.T) {
	root := t.TempDir()
	srv, err := New(config.Config{
		DBdsn:       createBootstrapDSN(t, "server_embedded_anon"),
		GitRepoDir:  filepath.Join(root, "repos"),
		BaseURL:     "http://embed.local",
		ListenMode:  "production",
		Environment: "production",
	}, WithAuthenticator(headerAuthenticator{
		header: "X-Embedded-User",
		identity: agsauth.Identity{
			Provider: "meshx",
			Subject:  "subject-2",
			Login:    "public-owner",
		},
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	owner, err := srv.deps.SvcDeps.ResolveEmbeddedIdentity(context.Background(), service.EmbeddedIdentity{
		Provider: "meshx",
		Subject:  "subject-2",
		Login:    "public-owner",
		Name:     "Public Owner",
	})
	if err != nil {
		t.Fatalf("ResolveEmbeddedIdentity: %v", err)
	}
	if _, err := srv.deps.SvcDeps.CreateRepo(service.ContextWithUser(context.Background(), owner), service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "public-repo",
		Private:    false,
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/repos/public-owner/public-repo", nil)
	rec := httptest.NewRecorder()
	srv.RESTHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("anonymous optional route: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rateReq := httptest.NewRequest(http.MethodGet, "/rate_limit", nil)
	rateRec := httptest.NewRecorder()
	srv.RESTHandler().ServeHTTP(rateRec, rateReq)
	if rateRec.Code != http.StatusOK {
		t.Fatalf("anonymous rate_limit route: expected 200, got %d: %s", rateRec.Code, rateRec.Body.String())
	}
	if got := rateRec.Header().Get("X-RateLimit-Limit"); got != "100" {
		t.Fatalf("anonymous rate_limit header = %q, want 100", got)
	}

	if err := srv.deps.SvcDeps.StarRepo(service.ContextWithUser(context.Background(), owner), "public-owner/public-repo", owner.Login); err != nil {
		t.Fatalf("StarRepo public: %v", err)
	}
	if _, err := srv.deps.SvcDeps.CreateRepo(service.ContextWithUser(context.Background(), owner), service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "private-repo",
		Private:    true,
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo private: %v", err)
	}
	if err := srv.deps.SvcDeps.StarRepo(service.ContextWithUser(context.Background(), owner), "public-owner/private-repo", owner.Login); err != nil {
		t.Fatalf("StarRepo private: %v", err)
	}

	starredReq := httptest.NewRequest(http.MethodGet, "/users/public-owner/starred", nil)
	starredRec := httptest.NewRecorder()
	srv.RESTHandler().ServeHTTP(starredRec, starredReq)
	if starredRec.Code != http.StatusOK {
		t.Fatalf("anonymous starred route: expected 200, got %d: %s", starredRec.Code, starredRec.Body.String())
	}
	var starredBody []map[string]any
	if err := json.Unmarshal(starredRec.Body.Bytes(), &starredBody); err != nil {
		t.Fatalf("decode anonymous starred body: %v", err)
	}
	if len(starredBody) != 1 {
		t.Fatalf("anonymous starred repo count = %d, want 1", len(starredBody))
	}
	if got := starredBody[0]["full_name"]; got != "public-owner/public-repo" {
		t.Fatalf("anonymous starred repo full_name = %v, want public-owner/public-repo", got)
	}
}

func TestStart_BindsAllListenersBeforeServing(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer occupied.Close()

	free1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve free port 1: %v", err)
	}
	addr1 := free1.Addr().String()
	free1.Close()

	free2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve free port 2: %v", err)
	}
	addr2 := free2.Addr().String()
	free2.Close()

	srv := &Server{
		deps: &bootstrapDeps{
			Servers: []*http.Server{
				{Addr: addr1, Handler: http.NewServeMux()},
				{Addr: occupied.Addr().String(), Handler: http.NewServeMux()},
				{Addr: addr2, Handler: http.NewServeMux()},
			},
			Labels: []string{"one", "blocked", "two"},
		},
	}

	err = srv.Start()
	if err == nil {
		t.Fatal("expected Start to fail when one listener cannot bind")
	}
	if srv.started {
		t.Fatal("server should not be marked started on partial bind failure")
	}
	if len(srv.listeners) != 0 {
		t.Fatalf("listeners should not be retained on failure, got %d", len(srv.listeners))
	}

	for _, addr := range []string{addr1, addr2} {
		ln, listenErr := net.Listen("tcp", addr)
		if listenErr != nil {
			t.Fatalf("expected %s to be released after Start failure: %v", addr, listenErr)
		}
		ln.Close()
	}
}

func TestStart_MarksStartedAfterSuccessfulBind(t *testing.T) {
	addr1 := allocateLoopbackAddr(t)
	addr2 := allocateLoopbackAddr(t)
	handler := http.NewServeMux()
	handler.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	srv := &Server{
		deps: &bootstrapDeps{
			Servers: []*http.Server{
				{Addr: addr1, Handler: handler},
				{Addr: addr2, Handler: handler},
			},
			Labels:    []string{"one", "two"},
			SvcDeps:   &service.Service{},
			SrvCancel: func() {},
		},
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	if !srv.started {
		t.Fatal("server should be marked started after successful Start")
	}
	if len(srv.listeners) != 2 {
		t.Fatalf("expected 2 listeners, got %d", len(srv.listeners))
	}

	for _, addr := range []string{addr1, addr2} {
		req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/healthz", addr), nil)
		if err != nil {
			t.Fatalf("build request for %s: %v", addr, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request %s: %v", addr, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status for %s = %d, want %d", addr, resp.StatusCode, http.StatusNoContent)
		}
	}
}

func TestShutdown_CancelsServerContextBeforeWaitingForWorkers(t *testing.T) {
	srvCtx, srvCancel := context.WithCancel(context.Background())
	svc := &service.Service{Ctx: srvCtx}
	svc.Wg.Add(1)
	workerExited := make(chan struct{})
	go func() {
		defer svc.Wg.Done()
		defer close(workerExited)
		<-svc.ServerCtx().Done()
	}()

	srv := &Server{
		deps: &bootstrapDeps{
			SvcDeps:   svc,
			SrvCancel: srvCancel,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	select {
	case <-workerExited:
	case <-time.After(time.Second):
		t.Fatal("expected worker to exit after shutdown canceled server context")
	}
}

func allocateLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate loopback addr: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("release loopback addr: %v", err)
	}
	return addr
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
