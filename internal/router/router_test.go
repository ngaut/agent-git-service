package router_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ngaut/agent-git-service/internal/connectedlogin"
	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/githttp"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/graphql"
	"github.com/ngaut/agent-git-service/internal/oauth"
	"github.com/ngaut/agent-git-service/internal/rest"
	"github.com/ngaut/agent-git-service/internal/router"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness/testdb"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

var routerSchemaTemplate struct {
	once sync.Once
	pool *testdb.SchemaPool
	err  error
}

func routerTemplatePool(t *testing.T) *testdb.SchemaPool {
	t.Helper()
	routerSchemaTemplate.once.Do(func() {
		gdb, cleanup := testdb.OpenRaw(t, "router_template")
		_ = cleanup
		var templateDB string
		if err := gdb.Raw("SELECT DATABASE()").Scan(&templateDB).Error; err != nil {
			routerSchemaTemplate.err = err
			return
		}
		if err := db.Migrate(gdb); err != nil {
			routerSchemaTemplate.err = err
			return
		}
		routerSchemaTemplate.pool = &testdb.SchemaPool{
			TemplateDB: templateDB,
			Prefix:     "router",
		}
		if sqlDB, err := gdb.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if routerSchemaTemplate.err != nil {
		t.Fatalf("prepare router schema template: %v", routerSchemaTemplate.err)
	}
	if routerSchemaTemplate.pool == nil {
		t.Fatal("router schema pool was not initialized")
	}
	return routerSchemaTemplate.pool
}

// setupTestDeps creates an isolated TiDB database, temp gitstore,
// and all handler dependencies. It seeds an admin user and a test token.
// Callers wire these into a router themselves (see setupRouterTest).
func setupTestDeps(t *testing.T) (*service.Service, *graphql.Server, *rest.Deps, *githttp.Handler, *oauth.Handler) {
	t.Helper()

	gdb, dbCleanup := routerTemplatePool(t).Open(t)
	t.Cleanup(dbCleanup)

	// Seed admin user (required by ExchangeDeviceCode).
	admin := db.User{Login: "admin", Type: db.TypeUser, SiteAdmin: true}
	gdb.Create(&admin)
	// Seed token for host-rewrite tests that hit authenticated routes.
	gdb.Create(&db.Token{UserID: admin.ID, Value: "test-token"})

	tmpDir, err := os.MkdirTemp("", "router-test-")
	if err != nil {
		t.Fatalf("tmpdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	gs, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatalf("gitstore: %v", err)
	}

	wikiBlob := wikicatalog.NewBlobStore(tmpDir)
	wikiCat := wikicatalog.New(gdb, wikiBlob)
	svc := &service.Service{
		DB:          gdb,
		Git:         gs,
		WikiCatalog: wikiCat,
		WikiBlob:    wikiBlob,
		BaseURL:     "http://localhost:8080",
	}
	wikiCat.DBFor = svc.DBForCtx
	wikiCat.OnChangeSetCommitted = svc.WikiCatalogPostCommit

	gqlSrv := graphql.NewServer(svc)
	restDeps := &rest.Deps{Svc: svc, ConsoleBaseURL: "http://console.localhost"}
	gitHandler := githttp.New(gs, svc)
	oauthHandler := &oauth.Handler{Svc: svc}

	return svc, gqlSrv, restDeps, gitHandler, oauthHandler
}

// setupRouterTest creates an isolated TiDB database, temp gitstore,
// and fully-wired router. It seeds an admin user and a test token for
// authenticated route testing.
func setupRouterTest(t *testing.T) (*service.Service, http.Handler) {
	t.Helper()
	svc, gqlSrv, restDeps, gitHandler, oauthHandler := setupTestDeps(t)
	mux := router.RegisterRoutes(chi.NewRouter(), restDeps, gitHandler, gqlSrv, oauthHandler, "http://console.localhost")
	return svc, mux
}

func oauthAuthorizeState(t *testing.T) string {
	t.Helper()
	return "state-" + strings.ReplaceAll(t.Name(), "/", "-")
}

func oauthAuthorizeCodeChallenge(t *testing.T) string {
	t.Helper()
	verifier := "verifier-" + strings.ReplaceAll(t.Name(), "/", "-")
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func oauthAuthorizeRequestPath(t *testing.T, redirectURI string) string {
	t.Helper()
	query := url.Values{
		"redirect_uri":          []string{redirectURI},
		"state":                 []string{oauthAuthorizeState(t)},
		"code_challenge":        []string{oauthAuthorizeCodeChallenge(t)},
		"code_challenge_method": []string{"S256"},
	}
	return "/login/oauth/authorize?" + query.Encode()
}

type routerFakeConnectedLoginProvider struct {
	loginURL string
}

func (f routerFakeConnectedLoginProvider) Provider() string {
	return "provider"
}

func (f routerFakeConnectedLoginProvider) ExchangeCode(ctx context.Context, code string) (connectedlogin.Token, error) {
	return connectedlogin.Token{AccessToken: "connected-access-token"}, nil
}

func (f routerFakeConnectedLoginProvider) Userinfo(ctx context.Context, accessToken string) (connectedlogin.Userinfo, error) {
	return connectedlogin.Userinfo{
		Sub:                   "agent-sub",
		Type:                  "agent",
		ClientID:              "connected-client",
		SubjectNamespace:      "workspace-1",
		SubjectNamespaceClaim: "server_id",
		PreferredUsername:     "agent",
		Name:                  "Connected Agent",
	}, nil
}

func (f routerFakeConnectedLoginProvider) LoginURL(state string) string {
	if state == "" {
		return f.loginURL
	}
	sep := "?"
	if strings.Contains(f.loginURL, "?") {
		sep = "&"
	}
	return f.loginURL + sep + "state=" + state
}

func TestConnectedLoginRoutes(t *testing.T) {
	svc, mux := setupRouterTest(t)

	t.Run("not configured", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/auth/connected/login", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("expected 501, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	svc.ConnectedLogin = routerFakeConnectedLoginProvider{
		loginURL: "https://app.provider.example/oauth/login?client_id=connected-client",
	}

	t.Run("login redirects", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/auth/connected/login", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("expected 302, got %d: %s", rec.Code, rec.Body.String())
		}
		got := rec.Header().Get("Location")
		loc, err := url.Parse(got)
		if err != nil {
			t.Fatalf("parse Location: %v", err)
		}
		if loc.Scheme != "https" || loc.Host != "app.provider.example" || loc.Path != "/oauth/login" {
			t.Fatalf("unexpected redirect target: %q", got)
		}
		if loc.Query().Get("client_id") != "connected-client" {
			t.Fatalf("client_id: got %q", loc.Query().Get("client_id"))
		}
		state := loc.Query().Get("state")
		if len(state) != 32 {
			t.Fatalf("expected 32-char state, got %q", state)
		}
		cookie := rec.Result().Cookies()
		if len(cookie) != 1 {
			t.Fatalf("expected one cookie, got %d", len(cookie))
		}
		if cookie[0].Name != "connected_login_state" || cookie[0].Value != state {
			t.Fatalf("cookie/state mismatch: cookie=%#v state=%q", cookie[0], state)
		}
		if !cookie[0].HttpOnly || cookie[0].SameSite != http.SameSiteLaxMode {
			t.Fatalf("unexpected cookie flags: %#v", cookie[0])
		}
	})

	t.Run("callback creates session", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/auth/connected/callback?code=connected-code&state=expected-state", nil)
		req.AddCookie(&http.Cookie{Name: "connected_login_state", Value: "expected-state"})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("expected 302, got %d: %s", rec.Code, rec.Body.String())
		}
		loc, err := url.Parse(rec.Header().Get("Location"))
		if err != nil {
			t.Fatalf("parse redirect target: %v", err)
		}
		if loc.Scheme != "http" || loc.Host != "console.localhost" || loc.Path != "" {
			t.Fatalf("unexpected console redirect: %q", loc.String())
		}
		if loc.Query().Get("code") == "" || loc.Query().Get("login") == "" {
			t.Fatalf("expected auth code and login in redirect query, got %q", loc.String())
		}
		if loc.Query().Get("type") != "agent" || loc.Query().Get("sub") != "agent-sub" || loc.Query().Get("subject_namespace") != "workspace-1" || loc.Query().Get("server_id") != "workspace-1" {
			t.Fatalf("unexpected callback redirect query: %q", loc.String())
		}
		var codeVerifier string
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == "connected_login_verifier" {
				codeVerifier = cookie.Value
				if !cookie.HttpOnly {
					t.Fatalf("expected verifier cookie to be HttpOnly: %#v", cookie)
				}
				if cookie.Path != "/login/oauth/access_token" {
					t.Fatalf("unexpected verifier cookie path: %#v", cookie)
				}
			}
		}
		if codeVerifier == "" {
			t.Fatal("expected callback response to emit connected_login_verifier cookie")
		}
		var authCode db.AuthorizationCode
		if err := svc.DB.First(&authCode, "code = ?", loc.Query().Get("code")).Error; err != nil {
			t.Fatalf("load auth code: %v", err)
		}
		if authCode.UserID == nil || *authCode.UserID == 0 {
			t.Fatalf("expected auth code to be bound to a user, got %#v", authCode)
		}
		if authCode.CodeChallengeMethod != "S256" {
			t.Fatalf("expected PKCE S256 auth code, got %#v", authCode)
		}
		sum := sha256.Sum256([]byte(codeVerifier))
		if authCode.CodeChallenge != base64.RawURLEncoding.EncodeToString(sum[:]) {
			t.Fatalf("code challenge mismatch: got %q", authCode.CodeChallenge)
		}
		var tokenCount int64
		if err := svc.DB.Model(&db.Token{}).Where("user_id = ?", *authCode.UserID).Count(&tokenCount).Error; err != nil {
			t.Fatalf("count transient tokens: %v", err)
		}
		if tokenCount != 0 {
			t.Fatalf("expected no durable tokens left after callback handoff, found %d", tokenCount)
		}

		exchangeBody, _ := json.Marshal(map[string]string{
			"code": authCode.Code,
		})
		exchangeReq := httptest.NewRequest(http.MethodPost, "/login/oauth/access_token", bytes.NewReader(exchangeBody))
		exchangeReq.Header.Set("Content-Type", "application/json")
		exchangeReq.AddCookie(&http.Cookie{Name: "connected_login_verifier", Value: codeVerifier})
		exchangeRec := httptest.NewRecorder()
		mux.ServeHTTP(exchangeRec, exchangeReq)
		if exchangeRec.Code != http.StatusOK {
			t.Fatalf("exchange auth code: expected 200, got %d: %s", exchangeRec.Code, exchangeRec.Body.String())
		}
		cleared := false
		for _, cookie := range exchangeRec.Result().Cookies() {
			if cookie.Name == "connected_login_verifier" && cookie.MaxAge < 0 {
				cleared = true
			}
		}
		if !cleared {
			t.Fatal("expected access token exchange to clear connected_login_verifier cookie")
		}
	})

	t.Run("direct callback without browser state returns durable token JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/auth/connected/callback?code=connected-agent-code&state=agent-state", nil)
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode token response: %v", err)
		}
		token, _ := body["token"].(string)
		if token == "" {
			t.Fatalf("expected durable token in callback JSON, got %#v", body)
		}
		if body["type"] != "agent" || body["sub"] != "agent-sub" || body["subject_namespace"] != "workspace-1" || body["server_id"] != "workspace-1" {
			t.Fatalf("unexpected callback JSON metadata: %#v", body)
		}
		var dbToken db.Token
		if err := svc.DB.First(&dbToken, "value = ?", token).Error; err != nil {
			t.Fatalf("expected durable token to remain usable: %v", err)
		}
	})

	t.Run("callback requires code", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/auth/connected/callback?state=expected-state", nil)
		req.AddCookie(&http.Cookie{Name: "connected_login_state", Value: "expected-state"})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("callback requires matching state", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/auth/connected/callback?code=connected-code&state=wrong-state", nil)
		req.AddCookie(&http.Cookie{Name: "connected_login_state", Value: "expected-state"})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}

// ---------------------------------------------------------------------------
// OAuth: device code
// ---------------------------------------------------------------------------

func TestOAuth_DeviceCodeThroughRouter(t *testing.T) {
	_, mux := setupRouterTest(t)

	req := httptest.NewRequest("POST", "/login/device/code", strings.NewReader("client_id=test&scope=repo"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"device_code", "user_code", "verification_uri", "verification_uri_complete", "expires_in", "interval"} {
		if _, ok := body[key]; !ok {
			t.Errorf("missing field %q", key)
		}
	}
	userCode, _ := body["user_code"].(string)
	if len(userCode) != 9 || userCode[4] != '-' {
		t.Errorf("unexpected user_code format: %q", userCode)
	}
}

func TestOAuth_DeviceVerificationRateLimited(t *testing.T) {
	_, mux := setupRouterTest(t)

	for attempt := 1; attempt <= 5; attempt++ {
		req := httptest.NewRequest(http.MethodGet, "/login/device", nil)
		req.Header.Set("Authorization", "token test-token")
		req.RemoteAddr = "198.51.100.10:12345"
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("attempt %d: expected 200, got %d: %s", attempt, w.Code, w.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/login/device", nil)
	req.Header.Set("Authorization", "token test-token")
	req.RemoteAddr = "198.51.100.10:12345"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", w.Code, w.Body.String())
	}
}

func TestOAuth_DeviceApproveAPIThroughRouter(t *testing.T) {
	_, mux := setupRouterTest(t)

	r1 := httptest.NewRequest(http.MethodPost, "/login/device/code", nil)
	w1 := httptest.NewRecorder()
	mux.ServeHTTP(w1, r1)
	if w1.Code != http.StatusOK {
		t.Fatalf("device code: expected 200, got %d: %s", w1.Code, w1.Body.String())
	}
	var codeResp map[string]any
	if err := json.NewDecoder(w1.Body).Decode(&codeResp); err != nil {
		t.Fatalf("decode device response: %v", err)
	}
	deviceCode := codeResp["device_code"].(string)
	userCode := codeResp["user_code"].(string)

	approveBody, _ := json.Marshal(map[string]string{"user_code": userCode})
	approveReq := httptest.NewRequest(http.MethodPost, "/api/ext/v1/oauth/device/approve", bytes.NewReader(approveBody))
	approveReq.Header.Set("Authorization", "token test-token")
	approveReq.Header.Set("Content-Type", "application/json")
	approveW := httptest.NewRecorder()
	mux.ServeHTTP(approveW, approveReq)
	if approveW.Code != http.StatusOK {
		t.Fatalf("approve: expected 200, got %d: %s", approveW.Code, approveW.Body.String())
	}
	var approveResp map[string]any
	if err := json.NewDecoder(approveW.Body).Decode(&approveResp); err != nil {
		t.Fatalf("decode approve response: %v", err)
	}
	if approveResp["status"] != "approved" {
		t.Fatalf("approve status = %v, want approved", approveResp["status"])
	}

	exchangeBody, _ := json.Marshal(map[string]string{"device_code": deviceCode})
	exchangeReq := httptest.NewRequest(http.MethodPost, "/login/oauth/access_token", bytes.NewReader(exchangeBody))
	exchangeReq.Header.Set("Content-Type", "application/json")
	exchangeW := httptest.NewRecorder()
	mux.ServeHTTP(exchangeW, exchangeReq)
	if exchangeW.Code != http.StatusOK {
		t.Fatalf("exchange: expected 200, got %d: %s", exchangeW.Code, exchangeW.Body.String())
	}
}

func TestOAuth_DeviceApproveAPIThroughRouter_InvalidJSON(t *testing.T) {
	_, mux := setupRouterTest(t)

	req := httptest.NewRequest(http.MethodPost, "/api/ext/v1/oauth/device/approve", strings.NewReader(`{"user_code"`))
	req.Header.Set("Authorization", "token test-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestOAuth_DeviceRejectAPIThroughRouter(t *testing.T) {
	_, mux := setupRouterTest(t)

	r1 := httptest.NewRequest(http.MethodPost, "/login/device/code", nil)
	w1 := httptest.NewRecorder()
	mux.ServeHTTP(w1, r1)
	if w1.Code != http.StatusOK {
		t.Fatalf("device code: expected 200, got %d: %s", w1.Code, w1.Body.String())
	}
	var codeResp map[string]any
	if err := json.NewDecoder(w1.Body).Decode(&codeResp); err != nil {
		t.Fatalf("decode device response: %v", err)
	}
	deviceCode := codeResp["device_code"].(string)
	userCode := codeResp["user_code"].(string)

	rejectBody, _ := json.Marshal(map[string]string{"user_code": userCode, "reason": "user declined"})
	rejectReq := httptest.NewRequest(http.MethodPost, "/api/ext/v1/oauth/device/reject", bytes.NewReader(rejectBody))
	rejectReq.Header.Set("Authorization", "token test-token")
	rejectReq.Header.Set("Content-Type", "application/json")
	rejectW := httptest.NewRecorder()
	mux.ServeHTTP(rejectW, rejectReq)
	if rejectW.Code != http.StatusOK {
		t.Fatalf("reject: expected 200, got %d: %s", rejectW.Code, rejectW.Body.String())
	}
	var rejectResp map[string]any
	if err := json.NewDecoder(rejectW.Body).Decode(&rejectResp); err != nil {
		t.Fatalf("decode reject response: %v", err)
	}
	if rejectResp["status"] != "rejected" {
		t.Fatalf("reject status = %v, want rejected", rejectResp["status"])
	}

	exchangeBody, _ := json.Marshal(map[string]string{"device_code": deviceCode})
	exchangeReq := httptest.NewRequest(http.MethodPost, "/login/oauth/access_token", bytes.NewReader(exchangeBody))
	exchangeReq.Header.Set("Content-Type", "application/json")
	exchangeW := httptest.NewRecorder()
	mux.ServeHTTP(exchangeW, exchangeReq)
	if exchangeW.Code != http.StatusBadRequest {
		t.Fatalf("exchange: expected 400, got %d: %s", exchangeW.Code, exchangeW.Body.String())
	}
	var exchangeResp map[string]any
	if err := json.NewDecoder(exchangeW.Body).Decode(&exchangeResp); err != nil {
		t.Fatalf("decode exchange response: %v", err)
	}
	if exchangeResp["error"] != "access_denied" {
		t.Fatalf("exchange error = %v, want access_denied", exchangeResp["error"])
	}
}

func TestOAuth_DeviceRejectAPIThroughRouter_InvalidJSON(t *testing.T) {
	_, mux := setupRouterTest(t)

	req := httptest.NewRequest(http.MethodPost, "/api/ext/v1/oauth/device/reject", strings.NewReader(`{"user_code"`))
	req.Header.Set("Authorization", "token test-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRegisterRoutes_ConditionalETagOnAuthenticatedJSONRoute(t *testing.T) {
	_, mux := setupRouterTest(t)

	firstReq := httptest.NewRequest(http.MethodGet, "/api/v3/user", nil)
	firstReq.Header.Set("Authorization", "token test-token")
	firstRes := httptest.NewRecorder()
	mux.ServeHTTP(firstRes, firstReq)

	if firstRes.Code != http.StatusOK {
		t.Fatalf("expected 200 on first request, got %d: %s", firstRes.Code, firstRes.Body.String())
	}
	etag := firstRes.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag header on authenticated JSON route")
	}
	if !strings.Contains(firstRes.Header().Get("Vary"), "Authorization") {
		t.Fatalf("expected Vary to include Authorization, got %q", firstRes.Header().Get("Vary"))
	}

	secondReq := httptest.NewRequest(http.MethodGet, "/api/v3/user", nil)
	secondReq.Header.Set("Authorization", "token test-token")
	secondReq.Header.Set("If-None-Match", etag)
	secondRes := httptest.NewRecorder()
	mux.ServeHTTP(secondRes, secondReq)

	if secondRes.Code != http.StatusNotModified {
		t.Fatalf("expected 304 on conditional request, got %d: %s", secondRes.Code, secondRes.Body.String())
	}
	if secondRes.Body.Len() != 0 {
		t.Fatalf("expected empty 304 body, got %q", secondRes.Body.String())
	}
}

func TestRegisterRoutes_DoesNotExposeCustomRESTPrefix(t *testing.T) {
	_, mux := setupRouterTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unsupported custom REST prefix, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPIRoot_StaysGitHubCompatibleDiscovery(t *testing.T) {
	_, mux := setupRouterTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v3/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := body["openapi_url"]; got != "http://localhost:8080/api/v3/openapi.json" {
		t.Fatalf("expected v3 OpenAPI URL to point at /api/v3/openapi.json, got %v", got)
	}
	if _, ok := body["extension_openapi_url"]; ok {
		t.Fatalf("expected extension OpenAPI URL to be omitted from /api/v3 discovery, got %v", body["extension_openapi_url"])
	}
	if got := body["current_user_url"]; got != "http://localhost:8080/api/v3/user" {
		t.Fatalf("expected v3 discovery URLs to stay on /api/v3, got %v", got)
	}
}

func TestExtensionAPIRoot_IncludesExtensionOpenAPIURL(t *testing.T) {
	_, mux := setupRouterTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/ext/v1/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := body["api_url"]; got != "http://localhost:8080/api/ext/v1" {
		t.Fatalf("expected extension API URL to point at /api/ext/v1, got %v", got)
	}
	if got := body["openapi_url"]; got != "http://localhost:8080/api/ext/v1/openapi.json" {
		t.Fatalf("expected extension OpenAPI URL to point at extension spec, got %v", got)
	}
	if got := body["github_compatible_api_url"]; got != "http://localhost:8080/api/v3" {
		t.Fatalf("expected GitHub-compatible API URL to point at /api/v3, got %v", got)
	}
}

func TestOpenAPIEndpoint_ServesPublishedSpec(t *testing.T) {
	_, mux := setupRouterTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/ext/v1/openapi.json", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected application/json, got %q", got)
	}

	var spec map[string]any
	if err := json.NewDecoder(w.Body).Decode(&spec); err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	if spec["openapi"] != "3.0.3" {
		t.Fatalf("expected openapi=3.0.3, got %v", spec["openapi"])
	}
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatal("expected paths object in spec")
	}
	if _, ok := paths["/api/ext/v1/agent-bindings/confirm"]; !ok {
		t.Fatal("expected agent binding extension route in spec")
	}
	if _, ok := paths["/api/v3/agent-bindings/confirm"]; ok {
		t.Fatal("did not expect legacy /api/v3 agent binding route in spec")
	}
	removedPaths := []string{
		"/api/ext/v1/presence/heartbeat",
		"/api/ext/v1/issues/{id}/typing",
		"/api/ext/v1/issues/{id}/attachments",
		"/api/ext/v1/repos/{owner}/{repo}/attachments",
		"/api/ext/v1/repositories/{repo_id}/attachments",
		"/api/ext/v1/issues/{issue_id}/presence",
		"/api/ext/v1/attachments/{uuid}",
		"/api/ext/v1/users/{user_id}/last-seen",
		"/api/ext/v1/user/presence/privacy",
		"/api/ext/v1/repos/{owner}/{repo}/issues/{number}/read",
		"/api/ext/v1/repos/{owner}/{repo}/issues/{number}/read-state",
		"/api/ext/v1/repos/{owner}/{repo}/issues/{number}/participants/read-state",
		"/api/ext/v1/repos/{owner}/{repo}/issues/{number}/unread-count",
	}
	for _, path := range removedPaths {
		if _, ok := paths[path]; ok {
			t.Fatalf("did not expect removed extension route %s in spec", path)
		}
	}
}

func TestGitHubCompatibleOpenAPIEndpoint_ServesV3Spec(t *testing.T) {
	_, mux := setupRouterTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v3/openapi.json", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected application/json, got %q", got)
	}

	var spec map[string]any
	if err := json.NewDecoder(w.Body).Decode(&spec); err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	info, ok := spec["info"].(map[string]any)
	if !ok {
		t.Fatal("expected info object in spec")
	}
	if got := info["title"]; got != "agent-git-service GitHub-Compatible REST API" {
		t.Fatalf("expected GitHub-compatible title, got %v", got)
	}
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatal("expected paths object in spec")
	}
	if _, ok := paths["/api/v3/openapi.json"]; !ok {
		t.Fatal("expected v3 OpenAPI route in v3 spec")
	}
	if _, ok := paths["/api/v3/repos/{owner}/{repo}/issues"]; !ok {
		t.Fatal("expected GitHub-compatible issues route in v3 spec")
	}
	if _, ok := paths["/api/v3/repos/{owner}/{repo}/pulls/{number}/ready_for_review"]; ok {
		t.Fatal("did not expect removed ready_for_review REST shim route in v3 spec")
	}
	if _, ok := paths["/api/ext/v1/agent-bindings/confirm"]; ok {
		t.Fatal("did not expect extension route in v3 spec")
	}
}

func TestCORS_ExposesRequestIDHeader(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://console.example.com")
	_, mux := setupRouterTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v3/", nil)
	req.Header.Set("Origin", "https://console.example.com")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://console.example.com" {
		t.Fatalf("expected configured origin to be echoed, got %q", got)
	}
	if got := w.Header().Get("Access-Control-Expose-Headers"); got != "X-Request-Id" {
		t.Fatalf("expected request ID header to be exposed, got %q", got)
	}
}

func TestOpenAPISpec_CoversProtectedExtensionRoutes(t *testing.T) {
	_, gqlSrv, restDeps, gitHandler, oauthHandler := setupTestDeps(t)
	rawRouter := chi.NewRouter()
	mux := router.RegisterRoutes(rawRouter, restDeps, gitHandler, gqlSrv, oauthHandler, "http://console.localhost")

	req := httptest.NewRequest(http.MethodGet, "/api/ext/v1/openapi.json", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("fetch spec: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var spec struct {
		Paths map[string]map[string]struct {
			Security    []map[string][]string `json:"security"`
			RequestBody *struct {
				Required bool `json:"required"`
				Content  map[string]struct {
					Schema struct {
						Required []string `json:"required"`
					} `json:"schema"`
				} `json:"content"`
			} `json:"requestBody"`
		} `json:"paths"`
		XAgentGitService struct {
			CompatibilityDeltas []struct {
				ID string `json:"id"`
			} `json:"compatibility_deltas"`
		} `json:"x-agent-git-service"`
	}
	if err := json.NewDecoder(w.Body).Decode(&spec); err != nil {
		t.Fatalf("decode spec: %v", err)
	}

	requiredDeltas := map[string]bool{
		"issues-list-omits-body":       false,
		"branch-protection-monolithic": false,
		"extension-canonical-prefix":   false,
	}
	for _, delta := range spec.XAgentGitService.CompatibilityDeltas {
		if _, ok := requiredDeltas[delta.ID]; ok {
			requiredDeltas[delta.ID] = true
		}
	}
	for id, seen := range requiredDeltas {
		if !seen {
			t.Fatalf("expected compatibility delta %q in published spec", id)
		}
	}

	documented := make(map[string]map[string]struct {
		hasSecurity    bool
		hasRequestBody bool
		contentTypes   map[string]bool
		requiredFields map[string]bool
		bodyIsRequired bool
	}, len(spec.Paths))
	for path, ops := range spec.Paths {
		methods := make(map[string]struct {
			hasSecurity    bool
			hasRequestBody bool
			contentTypes   map[string]bool
			requiredFields map[string]bool
			bodyIsRequired bool
		}, len(ops))
		for method, op := range ops {
			meta := struct {
				hasSecurity    bool
				hasRequestBody bool
				contentTypes   map[string]bool
				requiredFields map[string]bool
				bodyIsRequired bool
			}{
				hasSecurity:    len(op.Security) > 0,
				hasRequestBody: op.RequestBody != nil,
				contentTypes:   map[string]bool{},
				requiredFields: map[string]bool{},
			}
			if op.RequestBody != nil {
				meta.bodyIsRequired = op.RequestBody.Required
				for contentType, content := range op.RequestBody.Content {
					meta.contentTypes[contentType] = true
					for _, field := range content.Schema.Required {
						meta.requiredFields[field] = true
					}
				}
			}
			methods[strings.ToUpper(method)] = meta
		}
		documented[path] = methods
	}

	protected := map[string]map[string]bool{}
	walkErr := chi.Walk(rawRouter, func(method string, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !requiresOpenAPIDoc(method, route) {
			return nil
		}
		if protected[route] == nil {
			protected[route] = map[string]bool{}
		}
		protected[route][method] = true
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk routes: %v", walkErr)
	}

	for route, methods := range protected {
		docMethods, ok := documented[route]
		if !ok {
			t.Fatalf("protected route %s missing from published OpenAPI spec", route)
		}
		for method := range methods {
			if _, ok := docMethods[method]; !ok {
				t.Fatalf("protected route %s method %s missing from published OpenAPI spec", route, method)
			}
		}
	}

	protectedAuthChecks := map[string][]string{
		"/api/ext/v1/agent-invites":                            {http.MethodPost},
		"/api/ext/v1/agent-bindings/confirm":                   {http.MethodPost},
		"/api/ext/v1/oauth/device/approve":                     {http.MethodPost},
		"/api/ext/v1/oauth/device/reject":                      {http.MethodPost},
		"/api/ext/v1/user/orgs":                                {http.MethodPost},
		"/api/ext/v1/user/tokens":                              {http.MethodGet, http.MethodPost, http.MethodDelete},
		"/api/ext/v1/repos/{owner}/{repo}/team-sharing/enable": {http.MethodPost},
	}
	for route, methods := range protectedAuthChecks {
		for _, method := range methods {
			if !documented[route][method].hasSecurity {
				t.Fatalf("expected %s %s to declare authentication in published OpenAPI spec", method, route)
			}
		}
	}

	bodyChecks := []struct {
		route          string
		method         string
		contentType    string
		required       bool
		requiredFields []string
	}{
		{route: "/api/ext/v1/agent-bindings/confirm", method: http.MethodPost, contentType: "application/json", required: true, requiredFields: []string{"invite_token"}},
		{route: "/api/ext/v1/oidc/session", method: http.MethodPost, contentType: "application/json", required: true, requiredFields: []string{"device_code"}},
		{route: "/api/ext/v1/oidc/callback", method: http.MethodPost, contentType: "application/json", required: true, requiredFields: []string{"id_token"}},
		{route: "/api/ext/v1/oauth/device/approve", method: http.MethodPost, contentType: "application/json", required: true, requiredFields: []string{"user_code"}},
		{route: "/api/ext/v1/oauth/device/approve", method: http.MethodPost, contentType: "application/x-www-form-urlencoded", required: true, requiredFields: []string{"user_code"}},
		{route: "/api/ext/v1/oauth/device/reject", method: http.MethodPost, contentType: "application/json", required: true, requiredFields: []string{"user_code"}},
		{route: "/api/ext/v1/oauth/device/reject", method: http.MethodPost, contentType: "application/x-www-form-urlencoded", required: true, requiredFields: []string{"user_code"}},
		{route: "/api/ext/v1/user/orgs", method: http.MethodPost, contentType: "application/json", required: true, requiredFields: []string{"login"}},
		{route: "/api/ext/v1/user/tokens", method: http.MethodPost, contentType: "application/json", required: true},
		{route: "/api/ext/v1/user/tokens", method: http.MethodDelete, contentType: "application/json", required: true},
		{route: "/api/ext/v1/repos/{owner}/{repo}/wiki/move", method: http.MethodPost, contentType: "application/json", required: true, requiredFields: []string{"from", "to", "if_match"}},
		{route: "/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}", method: http.MethodPut, contentType: "application/json", required: true, requiredFields: []string{"body"}},
		{route: "/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}/move", method: http.MethodPost, contentType: "application/json", required: true, requiredFields: []string{"new_slug", "if_match"}},
		{route: "/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}/labels", method: http.MethodPost, contentType: "application/json", required: true, requiredFields: []string{"labels"}},
		{route: "/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}/labels", method: http.MethodPut, contentType: "application/json", required: true, requiredFields: []string{"labels"}},
	}
	for _, check := range bodyChecks {
		op, ok := documented[check.route][check.method]
		if !ok {
			t.Fatalf("missing documented operation %s %s", check.method, check.route)
		}
		if !op.hasRequestBody {
			t.Fatalf("expected %s %s to declare a requestBody", check.method, check.route)
		}
		if !op.contentTypes[check.contentType] {
			t.Fatalf("expected %s %s to declare content type %s", check.method, check.route, check.contentType)
		}
		if op.bodyIsRequired != check.required {
			t.Fatalf("expected %s %s requestBody required=%t, got %t", check.method, check.route, check.required, op.bodyIsRequired)
		}
		for _, field := range check.requiredFields {
			if !op.requiredFields[field] {
				t.Fatalf("expected %s %s requestBody to require field %q", check.method, check.route, field)
			}
		}
	}
}

func requiresOpenAPIDoc(method, route string) bool {
	switch {
	case route == "/api/ext/v1" || route == "/api/ext/v1/":
		return true
	case route == "/api/ext/v1/openapi.json":
		return true
	case strings.HasPrefix(route, "/api/ext/v1/agents"):
		return true
	case strings.HasPrefix(route, "/api/ext/v1/agent-invites"):
		return true
	case strings.HasPrefix(route, "/api/ext/v1/agent-bindings/"):
		return true
	case strings.HasPrefix(route, "/api/ext/v1/oidc/"):
		return true
	case strings.HasPrefix(route, "/api/ext/v1/oauth/"):
		return true
	case route == "/api/ext/v1/viewer/summary":
		return true
	case route == "/api/ext/v1/user/orgs":
		return method == http.MethodPost
	case route == "/api/ext/v1/user/agents":
		return true
	case route == "/api/ext/v1/user/tokens":
		return true
	case route == "/api/ext/v1/notifications/summary":
		return true
	case route == "/api/ext/v1/orgs/{org}/management-summary":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/summary":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/issues/{number}/thread":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/team-sharing/enable":
		return true
	case route == "/api/ext/v1/admin/wiki/repos/{owner}/{repo}/repair-locks":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/wiki/state":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/wiki/tree":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/wiki/pages":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/wiki/pages/batch":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/wiki/catalog":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/wiki/reconcile/request":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/wiki/reconcile":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/wiki/search":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/wiki/move":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/wiki/compact":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/wiki/compact/{jobID}":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}/labels":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}/labels/{name}":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}/history":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}/move":
		return true
	case route == "/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}/backlinks":
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// OAuth: access token exchange
// ---------------------------------------------------------------------------

func TestOAuth_AccessTokenExchange(t *testing.T) {
	svc, mux := setupRouterTest(t)

	// Step 1: obtain a device code through the router.
	r1 := httptest.NewRequest("POST", "/login/device/code", nil)
	w1 := httptest.NewRecorder()
	mux.ServeHTTP(w1, r1)
	if w1.Code != http.StatusOK {
		t.Fatalf("device code: expected 200, got %d", w1.Code)
	}
	var codeResp map[string]any
	json.NewDecoder(w1.Body).Decode(&codeResp)
	deviceCode := codeResp["device_code"].(string)

	// Step 1.5: approve the device code (simulate user verification)
	var admin db.User
	if err := svc.DB.First(&admin, "login = ?", "admin").Error; err != nil {
		t.Fatalf("load admin user: %v", err)
	}
	_, err := svc.ApproveDeviceCode(t.Context(), deviceCode, admin.ID, admin.Login)
	if err != nil {
		t.Fatalf("failed to approve device code: %v", err)
	}

	// Step 2: exchange device code for access token (JSON body).
	body, _ := json.Marshal(map[string]string{"device_code": deviceCode})
	r2 := httptest.NewRequest("POST", "/login/oauth/access_token", bytes.NewReader(body))
	r2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, r2)

	if w2.Code != http.StatusOK {
		t.Fatalf("exchange: expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
	var tokenResp map[string]any
	json.NewDecoder(w2.Body).Decode(&tokenResp)
	if tokenResp["access_token"] == nil || tokenResp["access_token"] == "" {
		t.Error("expected non-empty access_token")
	}
	if tokenResp["token_type"] != "bearer" {
		t.Errorf("expected token_type=bearer, got %v", tokenResp["token_type"])
	}
}

func TestOAuth_AccessTokenInvalidCode(t *testing.T) {
	_, mux := setupRouterTest(t)

	body, _ := json.Marshal(map[string]string{"device_code": "nonexistent"})
	req := httptest.NewRequest("POST", "/login/oauth/access_token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] != "bad_verification_code" {
		t.Errorf("expected error=bad_verification_code, got %v", resp["error"])
	}
}

func TestOAuth_AccessTokenPending(t *testing.T) {
	svc, mux := setupRouterTest(t)

	// Seed device code with empty AccessToken (not yet approved).
	svc.DB.Create(&db.DeviceCode{
		DeviceCode: "pending-code-router",
		UserCode:   "ABCD-EFGH",
		ExpiresAt:  time.Now().Add(15 * time.Minute),
	})

	body, _ := json.Marshal(map[string]string{"device_code": "pending-code-router"})
	req := httptest.NewRequest("POST", "/login/oauth/access_token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] != "authorization_pending" {
		t.Errorf("expected error=authorization_pending, got %v", resp["error"])
	}
}

// ---------------------------------------------------------------------------
// OAuth: authorize redirects
// ---------------------------------------------------------------------------

func TestOAuth_AuthorizeNoRedirect(t *testing.T) {
	_, mux := setupRouterTest(t)

	req := httptest.NewRequest("GET", "/login/oauth/authorize", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestOAuth_AuthorizeSameOrigin(t *testing.T) {
	_, mux := setupRouterTest(t)

	req := httptest.NewRequest("GET", oauthAuthorizeRequestPath(t, "http://example.com/callback"), nil)
	req.Host = "example.com"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	parsed, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse redirect location: %v", err)
	}
	if parsed.Scheme != "http" || parsed.Host != "example.com" || parsed.Path != "/callback" {
		t.Fatalf("unexpected redirect location: %q", loc)
	}
	query := parsed.Query()
	if query.Get("code") == "" {
		t.Fatalf("redirect missing code: %q", loc)
	}
	if query.Get("state") != oauthAuthorizeState(t) {
		t.Fatalf("redirect missing state echo: %q", loc)
	}
}

func TestOAuth_AuthorizeLocalhostAllowed(t *testing.T) {
	_, mux := setupRouterTest(t)

	req := httptest.NewRequest("GET", oauthAuthorizeRequestPath(t, "http://localhost:9999/cb"), nil)
	req.Host = "example.com"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("expected 302, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	parsed, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse redirect location: %v", err)
	}
	if parsed.Host != "localhost:9999" || parsed.Path != "/cb" {
		t.Fatalf("unexpected redirect location: %q", loc)
	}
	if parsed.Query().Get("state") != oauthAuthorizeState(t) {
		t.Fatalf("redirect missing state echo: %q", loc)
	}
}

func TestOAuth_AuthorizeCrossOriginBlocked(t *testing.T) {
	_, mux := setupRouterTest(t)

	req := httptest.NewRequest("GET", oauthAuthorizeRequestPath(t, "http://evil.com/cb"), nil)
	req.Host = "example.com"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Host rewrite: api.github.localhost
// ---------------------------------------------------------------------------

func TestHostRewrite_GraphQL(t *testing.T) {
	// Build the router with a path-capturing middleware so we can assert that
	// the host rewrite actually transforms /graphql → /api/graphql.
	// The middleware sits on the chi router (inside the host-rewrite wrapper),
	// so it observes req.URL.Path AFTER the rewrite but BEFORE route dispatch.
	_, gqlSrv, restDeps, gitHandler, oauthHandler := setupTestDeps(t)

	var capturedPath string
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if strings.HasSuffix(req.URL.Path, "graphql") {
				capturedPath = req.URL.Path
			}
			next.ServeHTTP(w, req)
		})
	})
	mux := router.RegisterRoutes(r, restDeps, gitHandler, gqlSrv, oauthHandler, "http://console.localhost")

	// POST /graphql on api.github.localhost must be rewritten to /api/graphql.
	body, _ := json.Marshal(map[string]any{"query": `{ viewer { login } }`})
	req := httptest.NewRequest("POST", "/graphql", bytes.NewReader(body))
	req.Host = "api.github.localhost"
	req.Header.Set("Authorization", "token test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["data"]; !ok {
		t.Errorf("expected GraphQL 'data' key in response, got keys: %v", resp)
	}

	// Core contract: the middleware must have seen /api/graphql, proving the
	// host rewrite transformed /graphql → /api/graphql (not the direct /graphql route).
	if capturedPath != "/api/graphql" {
		t.Errorf("expected rewritten path /api/graphql, got %q", capturedPath)
	}
}

func TestHostRewrite_REST(t *testing.T) {
	_, mux := setupRouterTest(t)

	// GET /meta on api.github.localhost should be rewritten to /api/v3/meta.
	req := httptest.NewRequest("GET", "/meta", nil)
	req.Host = "api.github.localhost"
	req.Header.Set("Authorization", "token test-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["installed_version"]; !ok {
		t.Errorf("expected 'installed_version' field in /meta response")
	}
}

func TestExtensionWikiNestedSlugPreservesEncodedSlashOnAPIHost(t *testing.T) {
	svc, mux := setupRouterTest(t)
	ctx := context.Background()

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "admin",
		Name:       "host-wiki",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, "admin/host-wiki", "guides/setup", "# Setup\n", "create setup", ""); err != nil {
		t.Fatalf("seed wiki page: %v", err)
	}
	svc.Wg.Wait()

	req := httptest.NewRequest("GET", "/api/ext/v1/repos/admin/host-wiki/wiki/pages/guides%2Fsetup", nil)
	req.Host = "api.github.localhost"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := resp["slug"]; got != "guides/setup" {
		t.Fatalf("slug = %v, want guides/setup", got)
	}
}

func TestHostRewrite_AlreadyPrefixed(t *testing.T) {
	_, mux := setupRouterTest(t)

	// GET /api/v3/meta on api.github.localhost should NOT be double-prefixed.
	req := httptest.NewRequest("GET", "/api/v3/meta", nil)
	req.Host = "api.github.localhost"
	req.Header.Set("Authorization", "token test-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["installed_version"]; !ok {
		t.Errorf("expected 'installed_version' in already-prefixed response")
	}
}

func TestHostRewrite_PortStripped(t *testing.T) {
	_, mux := setupRouterTest(t)

	// api.github.localhost:8080 should behave identically to api.github.localhost.
	req := httptest.NewRequest("GET", "/meta", nil)
	req.Host = "api.github.localhost:8080"
	req.Header.Set("Authorization", "token test-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["installed_version"]; !ok {
		t.Errorf("expected 'installed_version' in port-stripped response")
	}
}

func TestHostRewrite_NonApiHost(t *testing.T) {
	_, mux := setupRouterTest(t)

	// github.localhost (no api. prefix) should NOT rewrite /meta.
	req := httptest.NewRequest("GET", "/meta", nil)
	req.Host = "github.localhost"
	req.Header.Set("Authorization", "token test-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Console redirects (git domain → console vault)
// ---------------------------------------------------------------------------

func TestConsoleRedirects(t *testing.T) {
	_, mux := setupRouterTest(t)

	cases := []struct {
		name string
		path string
		want string
	}{
		{
			name: "repo redirect preserves query",
			path: "/alice/testrepo?token=abc",
			want: "http://console.localhost/vault/alice/testrepo?token=abc",
		},
		{
			name: "repo redirect trims .git",
			path: "/alice/testrepo.git",
			want: "http://console.localhost/vault/alice/testrepo",
		},
		{
			name: "issue redirect maps to memories",
			path: "/alice/testrepo/issues/42?token=abc",
			want: "http://console.localhost/vault/alice/testrepo/memories/42?token=abc",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.path, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusFound {
				t.Fatalf("expected 302, got %d: %s", w.Code, w.Body.String())
			}
			if loc := w.Header().Get("Location"); loc != tc.want {
				t.Fatalf("expected Location %q, got %q", tc.want, loc)
			}
		})
	}
}

func TestGitHTTP_SingleModeAllowsUnauthenticatedRequests(t *testing.T) {
	_, mux := setupRouterTest(t)

	req := httptest.NewRequest("GET", "/testowner/testrepo.git/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected unauthenticated pass-through to handler (404 for missing repo), got %d: %s", w.Code, w.Body.String())
	}
}

func TestGitReceivePack_BypassesDefaultBodyLimit(t *testing.T) {
	svc, gqlSrv, restDeps, gitHandler, oauthHandler := setupTestDeps(t)

	var admin db.User
	if err := svc.DB.First(&admin, "login = ?", "admin").Error; err != nil {
		t.Fatalf("read admin user: %v", err)
	}
	repo := db.Repository{
		Name:          "bigrepo",
		FullName:      "admin/bigrepo",
		OwnerID:       admin.ID,
		DefaultBranch: "main",
	}
	if err := svc.DB.Create(&repo).Error; err != nil {
		t.Fatalf("create repo: %v", err)
	}

	backendDir := t.TempDir()
	backendPath := backendDir + "/git-http-backend"
	script := "#!/bin/sh\n" +
		"cat >/dev/null\n" +
		"printf \"Status: 200 OK\\r\\n\"\n" +
		"printf \"Content-Type: text/plain\\r\\n\"\n" +
		"printf \"\\r\\n\"\n" +
		"printf \"ok\\n\"\n"
	if err := os.WriteFile(backendPath, []byte(script), 0755); err != nil {
		t.Fatalf("write stub backend: %v", err)
	}
	t.Setenv("GIT_EXEC_PATH", backendDir)

	mux := router.RegisterRoutes(chi.NewRouter(), restDeps, gitHandler, gqlSrv, oauthHandler, "http://console.localhost")
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	const payloadSize = (50 << 20) + 1
	body := io.LimitReader(zeroReader{}, payloadSize)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/admin/bigrepo.git/git-receive-pack", body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "token test-token")
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected oversized git push under 2 GiB to reach backend, got %d: %s", resp.StatusCode, respBody)
	}
}

// ---------------------------------------------------------------------------
// Router: Avatar redirect
// ---------------------------------------------------------------------------

func TestAvatarRedirect(t *testing.T) {
	_, mux := setupRouterTest(t)

	req := httptest.NewRequest("GET", "/avatars/testuser", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("expected 302 for avatar redirect, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc != "https://avatars.githubusercontent.com/u/1?v=4" {
		t.Errorf("expected redirect to GitHub avatars, got %q", loc)
	}
}

// ---------------------------------------------------------------------------
// Router: 404 handling
// ---------------------------------------------------------------------------

func TestNotFound_ApiRoute(t *testing.T) {
	_, mux := setupRouterTest(t)

	req := httptest.NewRequest("GET", "/api/v3/nonexistent/endpoint", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown API route, got %d", w.Code)
	}

	// Verify JSON response shape
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type=application/json, got %q", ct)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode 404 response: %v", err)
	}
	if resp["message"] != "Not Found" {
		t.Errorf("expected message='Not Found', got %v", resp["message"])
	}
	if resp["documentation_url"] != "https://docs.github.com/rest" {
		t.Errorf("expected documentation_url='https://docs.github.com/rest', got %v", resp["documentation_url"])
	}
}

func TestNotFound_NonApiRoute(t *testing.T) {
	_, mux := setupRouterTest(t)

	req := httptest.NewRequest("GET", "/unknown", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown non-API route, got %d", w.Code)
	}

	// Non-API routes should return standard HTML 404, not JSON
	if ct := w.Header().Get("Content-Type"); ct == "application/json" {
		t.Errorf("expected non-JSON Content-Type for non-API 404, got %q", ct)
	}
}

func TestNotFound_ApiRoot(t *testing.T) {
	_, mux := setupRouterTest(t)

	req := httptest.NewRequest("GET", "/api/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for /api/, got %d", w.Code)
	}

	// Should return JSON since it starts with /api/
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type=application/json, got %q", ct)
	}
}
