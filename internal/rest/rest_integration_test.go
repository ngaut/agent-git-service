package rest_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/service"
	"gh-server/internal/testharness"

	"github.com/stretchr/testify/require"
)

func requireRateLimitHeaders(t *testing.T, w *httptest.ResponseRecorder) (int, int, int, int64, string) {
	t.Helper()

	limit, err := strconv.Atoi(w.Header().Get("X-RateLimit-Limit"))
	require.NoError(t, err)

	used, err := strconv.Atoi(w.Header().Get("X-RateLimit-Used"))
	require.NoError(t, err)

	remaining, err := strconv.Atoi(w.Header().Get("X-RateLimit-Remaining"))
	require.NoError(t, err)

	reset, err := strconv.ParseInt(w.Header().Get("X-RateLimit-Reset"), 10, 64)
	require.NoError(t, err)

	return limit, used, remaining, reset, w.Header().Get("X-RateLimit-Resource")
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

// ─── Host Rewrite (api.github.localhost) ────────────────────────────────────

func TestHostRewrite_RESTEndpoint(t *testing.T) {
	h := testharness.New(t)

	// GET /user via api.github.localhost — should be rewritten to /api/v3/user
	w := h.DoRESTWithHost(t, "GET", "api.github.localhost", "/user", nil)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := testharness.DecodeJSON(t, w)
	if body["login"] != "testuser" {
		t.Errorf("login: got %v, want testuser", body["login"])
	}
}

func TestHostRewrite_Discovery(t *testing.T) {
	h := testharness.New(t)

	// GET / via api.github.localhost — unauthenticated discovery must work
	// without an Authorization header (the discovery endpoint uses optional auth).
	w := h.DoRESTNoAuthWithHost(t, "GET", "api.github.localhost", "/")
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := testharness.DecodeJSON(t, w)
	if _, ok := body["current_user_url"]; !ok {
		t.Error("response missing current_user_url")
	}
}

func TestRESTResponses_IncludeRateLimitHeaders(t *testing.T) {
	h := testharness.New(t)

	w := h.DoREST(t, "GET", "/api/v3/user", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	limit, used, remaining, reset, resource := requireRateLimitHeaders(t, w)
	require.Equal(t, 5000, limit)
	require.Equal(t, 1, used)
	require.Equal(t, 4999, remaining)
	require.Greater(t, reset, time.Now().Unix())
	require.Equal(t, "core", resource)
}

func TestRateLimitEndpoint_HeadersMatchPayload(t *testing.T) {
	h := testharness.New(t)

	w := h.DoRESTNoAuth(t, "GET", "/api/v3/rate_limit")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	limit, used, remaining, reset, resource := requireRateLimitHeaders(t, w)
	body := testharness.DecodeJSON(t, w)

	rate, ok := body["rate"].(map[string]any)
	require.True(t, ok, "rate payload missing")
	require.Equal(t, float64(limit), rate["limit"])
	require.Equal(t, float64(used), rate["used"])
	require.Equal(t, float64(remaining), rate["remaining"])
	require.Equal(t, float64(reset), rate["reset"])
	require.Equal(t, resource, rate["resource"])
	require.Equal(t, 60, limit)
	require.Equal(t, "core", resource)

	resources, ok := body["resources"].(map[string]any)
	require.True(t, ok, "resources payload missing")
	core, ok := resources["core"].(map[string]any)
	require.True(t, ok, "core resource missing")
	require.Equal(t, rate, core)
	search, ok := resources["search"].(map[string]any)
	require.True(t, ok, "search resource missing")
	require.Equal(t, float64(10), search["limit"])
	require.Equal(t, "search", search["resource"])
	codeSearch, ok := resources["code_search"].(map[string]any)
	require.True(t, ok, "code_search resource missing")
	require.Equal(t, float64(60), codeSearch["limit"])
	require.Equal(t, "code_search", codeSearch["resource"])
	graphql, ok := resources["graphql"].(map[string]any)
	require.True(t, ok, "graphql resource missing")
	require.Equal(t, float64(0), graphql["limit"])
	require.Equal(t, "graphql", graphql["resource"])
}

func TestAuthenticatedRateLimitEndpoint_ReportsSearchBudget(t *testing.T) {
	h := testharness.New(t)

	w := h.DoREST(t, "GET", "/api/v3/rate_limit", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	body := testharness.DecodeJSON(t, w)
	resources, ok := body["resources"].(map[string]any)
	require.True(t, ok, "resources payload missing")

	search, ok := resources["search"].(map[string]any)
	require.True(t, ok, "search resource missing")
	require.Equal(t, float64(300), search["limit"])
	require.Equal(t, "search", search["resource"])
}

func TestNotifications_ListAndMarkRead(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	author := db.User{Login: "notif-author", Name: "notif-author", Type: db.TypeUser}
	require.NoError(t, h.DB.Create(&author).Error)

	_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "notifications",
		AutoInit:   true,
	})
	require.NoError(t, err)

	_, err = h.Svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: h.User.Login + "/notifications",
		Title:        "REST notification",
		Body:         "hello @testuser",
		AuthorLogin:  author.Login,
	})
	require.NoError(t, err)

	w := h.DoREST(t, "GET", "/api/v3/notifications?all=true", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var items []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&items))
	require.Len(t, items, 1)
	require.Equal(t, true, items[0]["unread"])
	require.Equal(t, "mention", items[0]["reason"])

	subject := items[0]["subject"].(map[string]any)
	require.Equal(t, "Issue", subject["type"])
	require.NotEmpty(t, subject["latest_comment_url"])

	repo := items[0]["repository"].(map[string]any)
	require.Equal(t, h.User.Login+"/notifications", repo["full_name"])

	w = h.DoREST(t, "PUT", "/api/v3/notifications", nil)
	require.Equal(t, http.StatusResetContent, w.Code)

	w = h.DoREST(t, "GET", "/api/v3/notifications", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, json.NewDecoder(w.Body).Decode(&items))
	require.Len(t, items, 0)

	w = h.DoREST(t, "GET", "/api/v3/notifications?all=true", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, json.NewDecoder(w.Body).Decode(&items))
	require.Len(t, items, 1)
	require.Equal(t, false, items[0]["unread"])
	require.NotNil(t, items[0]["last_read_at"])
}

func TestNotifications_ConditionalETag(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	author := db.User{Login: "notif-etag-author", Email: "notif-etag-author@example.com"}
	require.NoError(t, h.DB.Create(&author).Error)

	_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "notifications-etag",
		AutoInit:   true,
	})
	require.NoError(t, err)

	_, err = h.Svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: h.User.Login + "/notifications-etag",
		Title:        "REST notification ETag",
		Body:         "hello @testuser",
		AuthorLogin:  author.Login,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v3/notifications?all=true", nil)
	req.Header.Set("Authorization", "token "+h.Token)
	req.Header.Set("Content-Type", "application/json")
	first := httptest.NewRecorder()
	h.Mux.ServeHTTP(first, req)

	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag)
	require.Contains(t, first.Header().Get("Vary"), "Authorization")

	req = httptest.NewRequest(http.MethodGet, "/api/v3/notifications?all=true", nil)
	req.Header.Set("Authorization", "token "+h.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	h.Mux.ServeHTTP(second, req)

	require.Equal(t, http.StatusNotModified, second.Code, second.Body.String())
	require.Empty(t, second.Body.String())
	require.Equal(t, etag, second.Header().Get("ETag"))
}

func TestHostRewrite_WithPort(t *testing.T) {
	h := testharness.New(t)

	// Port should be stripped before host comparison.
	w := h.DoRESTWithHost(t, "GET", "api.github.localhost:3000", "/user", nil)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := testharness.DecodeJSON(t, w)
	if body["login"] != "testuser" {
		t.Errorf("login: got %v, want testuser", body["login"])
	}
}

func TestHostRewrite_ApiPrefixPassthrough(t *testing.T) {
	h := testharness.New(t)

	// Paths already starting with /api/ should NOT be double-prefixed.
	w := h.DoRESTWithHost(t, "GET", "api.github.localhost", "/api/v3/user", nil)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := testharness.DecodeJSON(t, w)
	if body["login"] != "testuser" {
		t.Errorf("login: got %v, want testuser", body["login"])
	}
}

func TestHostRewrite_RepoEndpoint(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "host-test-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	// GET /repos/testuser/host-test-repo via api.github.localhost
	w := h.DoRESTWithHost(t, "GET", "api.github.localhost", "/repos/testuser/host-test-repo", nil)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := testharness.DecodeJSON(t, w)
	if body["full_name"] != "testuser/host-test-repo" {
		t.Errorf("full_name: got %v, want testuser/host-test-repo", body["full_name"])
	}
}

// ─── OAuth Routes ───────────────────────────────────────────────────────────

func TestOAuth_DeviceCodeRequest(t *testing.T) {
	h := testharness.New(t)

	req := httptest.NewRequest("POST", "/login/device/code", strings.NewReader(`{"client_id":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := testharness.DecodeJSON(t, w)
	for _, key := range []string{"device_code", "user_code", "verification_uri", "expires_in", "interval"} {
		if _, ok := body[key]; !ok {
			t.Errorf("response missing %s", key)
		}
	}
}

func TestOAuth_AccessTokenExchange(t *testing.T) {
	h := testharness.New(t)

	// Step 1: Request a device code.
	req1 := httptest.NewRequest("POST", "/login/device/code", strings.NewReader(`{"client_id":"test"}`))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	h.Mux.ServeHTTP(w1, req1)

	if w1.Code != 200 {
		t.Fatalf("device code: expected 200, got %d: %s", w1.Code, w1.Body.String())
	}
	dc := testharness.DecodeJSON(t, w1)
	deviceCode, ok := dc["device_code"].(string)
	if !ok || deviceCode == "" {
		t.Fatal("device_code missing or empty")
	}

	// Step 1.5: Approve the device code (simulate user verification)
	_, err := h.Svc.ApproveDeviceCode(t.Context(), deviceCode, h.User.ID, h.User.Login)
	if err != nil {
		t.Fatalf("failed to approve device code: %v", err)
	}

	// Step 2: Exchange the device code for an access token.
	body2, _ := json.Marshal(map[string]string{"device_code": deviceCode})
	req2 := httptest.NewRequest("POST", "/login/oauth/access_token", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	h.Mux.ServeHTTP(w2, req2)

	if w2.Code != 200 {
		t.Fatalf("access token: expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
	tokenResp := testharness.DecodeJSON(t, w2)
	accessToken, _ := tokenResp["access_token"].(string)
	if accessToken == "" {
		t.Fatal("access_token missing or empty")
	}
	if tokenResp["token_type"] != "bearer" {
		t.Errorf("token_type: got %v, want bearer", tokenResp["token_type"])
	}

	// Step 3: Prove the issued token is usable on an authenticated REST endpoint.
	w3 := h.DoRESTWithToken(t, "GET", "/api/v3/user", accessToken)
	if w3.Code != 200 {
		t.Fatalf("token usability: expected 200, got %d: %s", w3.Code, w3.Body.String())
	}
	userResp := testharness.DecodeJSON(t, w3)
	if userResp["login"] != "testuser" {
		t.Errorf("token usability: login got %v, want testuser", userResp["login"])
	}
}

func TestOAuth_AccessTokenBadCode(t *testing.T) {
	h := testharness.New(t)

	body, _ := json.Marshal(map[string]string{"device_code": "nonexistent-code"})
	req := httptest.NewRequest("POST", "/login/oauth/access_token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	resp := testharness.DecodeJSON(t, w)
	if resp["error"] != "bad_verification_code" {
		t.Errorf("error: got %v, want bad_verification_code", resp["error"])
	}
}

func TestOAuth_AccessTokenFormBody(t *testing.T) {
	h := testharness.New(t)

	// Request device code first.
	req1 := httptest.NewRequest("POST", "/login/device/code", strings.NewReader(`{"client_id":"test"}`))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	h.Mux.ServeHTTP(w1, req1)
	dc := testharness.DecodeJSON(t, w1)
	deviceCode := dc["device_code"].(string)

	// Approve the device code (simulate user verification)
	_, err := h.Svc.ApproveDeviceCode(t.Context(), deviceCode, h.User.ID, h.User.Login)
	if err != nil {
		t.Fatalf("failed to approve device code: %v", err)
	}

	// Exchange via form-encoded body (the way gh CLI sends it).
	req2 := httptest.NewRequest("POST", "/login/oauth/access_token",
		strings.NewReader("device_code="+deviceCode))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w2 := httptest.NewRecorder()
	h.Mux.ServeHTTP(w2, req2)

	if w2.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
	resp := testharness.DecodeJSON(t, w2)
	if resp["access_token"] == nil || resp["access_token"] == "" {
		t.Error("access_token missing or empty")
	}
}

func TestOAuth_AuthorizationPending(t *testing.T) {
	h := testharness.New(t)

	// Seed a device code with empty AccessToken to simulate unapproved state.
	h.DB.Create(&db.DeviceCode{
		DeviceCode: "pending-device-code",
		UserCode:   "PEND-CODE",
		ExpiresAt:  time.Now().Add(15 * time.Minute),
		// AccessToken intentionally empty — not yet approved
	})

	body, _ := json.Marshal(map[string]string{"device_code": "pending-device-code"})
	req := httptest.NewRequest("POST", "/login/oauth/access_token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	resp := testharness.DecodeJSON(t, w)
	if resp["error"] != "authorization_pending" {
		t.Errorf("error: got %v, want authorization_pending", resp["error"])
	}
}

func TestOAuth_AuthorizeNoRedirect(t *testing.T) {
	h := testharness.New(t)

	req := httptest.NewRequest("GET", "/login/oauth/authorize", nil)
	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestOAuth_AuthorizeSameOriginRedirect(t *testing.T) {
	h := testharness.New(t)

	req := httptest.NewRequest("GET", oauthAuthorizeRequestPath(t, "http://example.com/callback"), nil)
	req.Host = "example.com"
	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	parsed, err := url.Parse(loc)
	require.NoError(t, err)
	require.Equal(t, "http", parsed.Scheme)
	require.Equal(t, "example.com", parsed.Host)
	require.Equal(t, "/callback", parsed.Path)
	require.Empty(t, parsed.RawFragment)

	q := parsed.Query()
	require.NotEmpty(t, q.Get("code"))
	require.Equal(t, oauthAuthorizeState(t), q.Get("state"))
	require.Len(t, q, 2)
}

func TestOAuth_AuthorizeRedirectWithQuery(t *testing.T) {
	h := testharness.New(t)

	// redirect_uri already has a query param — code should be appended with &
	req := httptest.NewRequest("GET", oauthAuthorizeRequestPath(t, "http://example.com/callback?foo=bar"), nil)
	req.Host = "example.com"
	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	parsed, err := url.Parse(loc)
	require.NoError(t, err)
	require.Equal(t, "http", parsed.Scheme)
	require.Equal(t, "example.com", parsed.Host)
	require.Equal(t, "/callback", parsed.Path)
	require.Empty(t, parsed.RawFragment)

	q := parsed.Query()
	require.Equal(t, "bar", q.Get("foo"))
	require.NotEmpty(t, q.Get("code"))
	require.Equal(t, oauthAuthorizeState(t), q.Get("state"))
	require.Len(t, q, 3)
}

func TestOAuth_AuthorizeCrossOriginBlocked(t *testing.T) {
	h := testharness.New(t)

	req := httptest.NewRequest("GET", oauthAuthorizeRequestPath(t, "http://evil.com/steal"), nil)
	req.Host = "example.com"
	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// ─── Auth Middleware ────────────────────────────────────────────────────────

func TestAuth_UnauthenticatedDiscovery(t *testing.T) {
	h := testharness.New(t)

	w := h.DoRESTNoAuth(t, "GET", "/api/v3")
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := testharness.DecodeJSON(t, w)
	if _, ok := body["current_user_url"]; !ok {
		t.Error("response missing current_user_url")
	}
}

func TestAuth_ValidToken(t *testing.T) {
	h := testharness.New(t)

	w := h.DoREST(t, "GET", "/api/v3/user", nil)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := testharness.DecodeJSON(t, w)
	if body["login"] != "testuser" {
		t.Errorf("login: got %v, want testuser", body["login"])
	}
}

func TestAuth_InvalidToken(t *testing.T) {
	h := testharness.New(t)

	w := h.DoRESTWithToken(t, "GET", "/api/v3/user", "bad-token")
	if w.Code != 401 {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	body := testharness.DecodeJSON(t, w)
	if body["message"] != "Bad credentials" {
		t.Errorf("message: got %v, want Bad credentials", body["message"])
	}
}

func TestAuth_MissingHeader(t *testing.T) {
	h := testharness.New(t)

	w := h.DoRESTNoAuth(t, "GET", "/api/v3/user")
	if w.Code != 401 {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	body := testharness.DecodeJSON(t, w)
	if body["message"] != "Requires authentication" {
		t.Errorf("message: got %v, want Requires authentication", body["message"])
	}
}

// ─── Public Repo Anonymous Access ──────────────────────────────────────────

func TestPublicRepoAnonymousAccess(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	// Create a public repo with an issue and a label.
	_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "public-anon",
		Private:    false,
		AutoInit:   true,
	})
	require.NoError(t, err)

	_, err = h.Svc.CreateLabel(ctx, "testuser/public-anon", "anon-test-label", "d73a4a", "")
	require.NoError(t, err)
	_, err = h.Svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "testuser/public-anon",
		Title:        "hello",
		Body:         "world",
	})
	require.NoError(t, err)

	// Create a private repo.
	_, err = h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "private-anon",
		Private:    true,
		AutoInit:   true,
	})
	require.NoError(t, err)

	t.Run("GetPublicRepo", func(t *testing.T) {
		w := h.DoRESTNoAuth(t, "GET", "/api/v3/repos/testuser/public-anon")
		require.Equal(t, 200, w.Code, w.Body.String())
		body := testharness.DecodeJSON(t, w)
		require.Equal(t, "testuser/public-anon", body["full_name"])
	})

	t.Run("ListIssuesPublicRepo", func(t *testing.T) {
		w := h.DoRESTNoAuth(t, "GET", "/api/v3/repos/testuser/public-anon/issues")
		require.Equal(t, 200, w.Code, w.Body.String())
	})

	t.Run("GetIssuePublicRepo", func(t *testing.T) {
		w := h.DoRESTNoAuth(t, "GET", "/api/v3/repos/testuser/public-anon/issues/1")
		require.Equal(t, 200, w.Code, w.Body.String())
	})

	t.Run("ListLabelsPublicRepo", func(t *testing.T) {
		w := h.DoRESTNoAuth(t, "GET", "/api/v3/repos/testuser/public-anon/labels")
		require.Equal(t, 200, w.Code, w.Body.String())
	})

	t.Run("GetPrivateRepo_Denied", func(t *testing.T) {
		w := h.DoRESTNoAuth(t, "GET", "/api/v3/repos/testuser/private-anon")
		require.Equal(t, 404, w.Code, "private repo must be hidden from anonymous users")
	})

	t.Run("WritePublicRepo_Denied", func(t *testing.T) {
		w := h.DoRESTNoAuth(t, "POST", "/api/v3/repos/testuser/public-anon/issues")
		require.Equal(t, 401, w.Code, "anonymous writes must be rejected")
	})

	t.Run("AuthenticatedNonOwner_CanReadPublicRepo", func(t *testing.T) {
		// Create a second non-admin user with a token.
		outsider := db.User{Login: "outsider-pub", Name: "outsider-pub", Type: db.TypeUser}
		require.NoError(t, h.DB.Create(&outsider).Error)
		require.NoError(t, h.DB.Create(&db.Token{UserID: outsider.ID, Value: "outsider-pub-token"}).Error)

		w := h.DoRESTWithToken(t, "GET", "/api/v3/repos/testuser/public-anon", "outsider-pub-token")
		require.Equal(t, 200, w.Code, "authenticated non-owner must be able to read public repo")
		body := testharness.DecodeJSON(t, w)
		require.Equal(t, "testuser/public-anon", body["full_name"])
	})

	t.Run("AuthenticatedNonOwner_CannotReadPrivateRepo", func(t *testing.T) {
		outsider := db.User{Login: "outsider-priv", Name: "outsider-priv", Type: db.TypeUser}
		require.NoError(t, h.DB.Create(&outsider).Error)
		require.NoError(t, h.DB.Create(&db.Token{UserID: outsider.ID, Value: "outsider-priv-token"}).Error)

		w := h.DoRESTWithToken(t, "GET", "/api/v3/repos/testuser/private-anon", "outsider-priv-token")
		require.Equal(t, 404, w.Code, "authenticated non-owner must not see private repo")
	})
}

// ─── Repo Lifecycle ─────────────────────────────────────────────────────────

func TestRepoLifecycle(t *testing.T) {
	h := testharness.New(t)

	t.Run("Create", func(t *testing.T) {
		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":        "integ-repo",
			"description": "test",
			"auto_init":   true,
		})
		if w.Code != 201 {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		if body["full_name"] != "testuser/integ-repo" {
			t.Errorf("full_name: got %v, want testuser/integ-repo", body["full_name"])
		}
	})

	t.Run("Get", func(t *testing.T) {
		w := h.DoREST(t, "GET", "/api/v3/repos/testuser/integ-repo", nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		if body["name"] != "integ-repo" {
			t.Errorf("name: got %v, want integ-repo", body["name"])
		}
		if body["description"] != "test" {
			t.Errorf("description: got %v, want test", body["description"])
		}
	})

	t.Run("Update", func(t *testing.T) {
		w := h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/integ-repo", map[string]any{
			"description": "updated",
		})
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		if body["description"] != "updated" {
			t.Errorf("description: got %v, want updated", body["description"])
		}
	})

	t.Run("List", func(t *testing.T) {
		w := h.DoREST(t, "GET", "/api/v3/user/repos", nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		items := testharness.DecodeJSONArray(t, w)
		found := false
		for _, item := range items {
			if item["name"] == "integ-repo" {
				found = true
				break
			}
		}
		if !found {
			t.Error("integ-repo not found in user repos list")
		}
	})

	t.Run("Delete", func(t *testing.T) {
		w := h.DoREST(t, "DELETE", "/api/v3/repos/testuser/integ-repo", nil)
		if w.Code != 204 {
			t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("GetAfterDelete", func(t *testing.T) {
		w := h.DoREST(t, "GET", "/api/v3/repos/testuser/integ-repo", nil)
		require.Equal(t, http.StatusNotFound, w.Code, "deleted repo must return 404 Not Found")
	})
}

// ─── Issue Lifecycle ────────────────────────────────────────────────────────

func TestIssueLifecycle(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	var comment1ID uint
	var comment2ID uint
	var sinceTime time.Time

	// Seed a repo for issues.
	_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "issue-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	_, err = h.Svc.CreateLabel(ctx, "testuser/issue-repo", "skill-label", "ededed", "used by skill tests")
	require.NoError(t, err)

	t.Run("Create", func(t *testing.T) {
		w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/issue-repo/issues", map[string]any{
			"title": "bug report",
			"body":  "details here",
		})
		if w.Code != 201 {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		if body["title"] != "bug report" {
			t.Errorf("title: got %v, want bug report", body["title"])
		}
		if body["state"] != "open" {
			t.Errorf("state: got %v, want open", body["state"])
		}
		if num, ok := body["number"].(float64); !ok || int(num) != 1 {
			t.Errorf("number: got %v, want 1", body["number"])
		}
	})

	t.Run("Get", func(t *testing.T) {
		w := h.DoREST(t, "GET", "/api/v3/repos/testuser/issue-repo/issues/1", nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		if body["title"] != "bug report" {
			t.Errorf("title: got %v, want bug report", body["title"])
		}
	})

	t.Run("List", func(t *testing.T) {
		// Seed a second repo with its own issue to test same-owner repo scoping.
		_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: h.User.Login,
			Name:       "other-issue-repo",
			AutoInit:   true,
		})
		if err != nil {
			t.Fatalf("seed other repo: %v", err)
		}
		wOther := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/other-issue-repo/issues", map[string]any{
			"title": "foreign issue",
		})
		if wOther.Code != 201 {
			t.Fatalf("seed foreign issue: expected 201, got %d: %s", wOther.Code, wOther.Body.String())
		}

		// Seed a different owner and repo to ensure the list honors the owner path segment too.
		otherOwner := db.User{Login: "otheruser", Name: "Other User", Type: "User"}
		require.NoError(t, h.DB.Create(&otherOwner).Error)
		_, err = h.Svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: otherOwner.Login,
			Name:       "cross-owner-issue-repo",
			AutoInit:   true,
		})
		require.NoError(t, err)
		_, err = h.Svc.CreateIssue(ctx, service.CreateIssueInput{
			RepoFullName: "otheruser/cross-owner-issue-repo",
			Title:        "cross-owner issue",
			AuthorLogin:  otherOwner.Login,
		})
		require.NoError(t, err)

		w := h.DoREST(t, "GET", "/api/v3/repos/testuser/issue-repo/issues", nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		items := testharness.DecodeJSONArray(t, w)
		if len(items) < 1 {
			t.Fatal("expected at least 1 issue in list")
		}
		found := false
		for _, item := range items {
			if num, ok := item["number"].(float64); ok && int(num) == 1 {
				found = true
			}
			// Negative assertion: no issue from other-issue-repo should appear.
			if title, _ := item["title"].(string); title == "foreign issue" {
				t.Error("list must not include issues from other repos (found 'foreign issue')")
			}
			if title, _ := item["title"].(string); title == "cross-owner issue" {
				t.Error("list must not include issues from other owners (found 'cross-owner issue')")
			}
		}
		if !found {
			t.Error("list should include the issue we just created (number=1)")
		}
	})

	t.Run("Update", func(t *testing.T) {
		w := h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/issue-repo/issues/1", map[string]any{
			"state": "closed",
		})
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		if body["state"] != "closed" {
			t.Errorf("state: got %v, want closed", body["state"])
		}
	})

	t.Run("UpdateLabelsViaPatch", func(t *testing.T) {
		w := h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/issue-repo/issues/1", map[string]any{
			"labels": []string{"skill-label"},
		})
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		labels, ok := body["labels"].([]any)
		if !ok || len(labels) != 1 {
			t.Fatalf("expected 1 label in PATCH response, got %#v", body["labels"])
		}
		label, ok := labels[0].(map[string]any)
		if !ok || label["name"] != "skill-label" {
			t.Fatalf("expected label skill-label, got %#v", labels[0])
		}

		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/issue-repo/issues/1/labels", nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		items := testharness.DecodeJSONArray(t, w)
		if len(items) != 1 || items[0]["name"] != "skill-label" {
			t.Fatalf("expected persisted label skill-label, got %#v", items)
		}
	})

	t.Run("Comment", func(t *testing.T) {
		w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/issue-repo/issues/1/comments", map[string]any{
			"body": "a comment",
		})
		if w.Code != 201 {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		if body["body"] != "a comment" {
			t.Errorf("body: got %v, want a comment", body["body"])
		}
		idRaw, ok := body["id"].(float64)
		if !ok {
			t.Fatalf("expected numeric id in response, got %#v", body["id"])
		}
		comment1ID = uint(idRaw)

		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/issue-repo/issues/1/comments", map[string]any{
			"body": "second comment",
		})
		if w.Code != 201 {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}
		body = testharness.DecodeJSON(t, w)
		if body["body"] != "second comment" {
			t.Errorf("body: got %v, want second comment", body["body"])
		}
		idRaw, ok = body["id"].(float64)
		if !ok {
			t.Fatalf("expected numeric id in response, got %#v", body["id"])
		}
		comment2ID = uint(idRaw)

		base := time.Date(2024, 1, 2, 12, 0, 0, 0, time.UTC)
		comment1Created := base.Add(0 * time.Minute)
		comment2Created := base.Add(10 * time.Minute)
		comment1Updated := base.Add(30 * time.Minute)
		comment2Updated := base.Add(20 * time.Minute)
		sinceTime = base.Add(25 * time.Minute)

		require.NoError(t, h.DB.Model(&db.IssueComment{}).Where("id = ?", comment1ID).
			UpdateColumns(map[string]any{
				"created_at": comment1Created,
				"updated_at": comment1Updated,
			}).Error)
		require.NoError(t, h.DB.Model(&db.IssueComment{}).Where("id = ?", comment2ID).
			UpdateColumns(map[string]any{
				"created_at": comment2Created,
				"updated_at": comment2Updated,
			}).Error)
	})

	t.Run("ListComments", func(t *testing.T) {
		w := h.DoREST(t, "GET", "/api/v3/repos/testuser/issue-repo/issues/1/comments", nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		items := testharness.DecodeJSONArray(t, w)
		if len(items) != 2 {
			t.Errorf("expected 2 comments, got %d", len(items))
		}
		if len(items) >= 2 {
			firstID, _ := items[0]["id"].(float64)
			secondID, _ := items[1]["id"].(float64)
			if uint(firstID) != comment1ID || uint(secondID) != comment2ID {
				t.Fatalf("default order = [%d %d], want [%d %d]", uint(firstID), uint(secondID), comment1ID, comment2ID)
			}
		}

		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/issue-repo/issues/1/comments?sort=created&direction=desc", nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		items = testharness.DecodeJSONArray(t, w)
		if len(items) >= 2 {
			firstID, _ := items[0]["id"].(float64)
			secondID, _ := items[1]["id"].(float64)
			if uint(firstID) != comment2ID || uint(secondID) != comment1ID {
				t.Fatalf("created desc order = [%d %d], want [%d %d]", uint(firstID), uint(secondID), comment2ID, comment1ID)
			}
		}

		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/issue-repo/issues/1/comments?sort=updated&direction=asc", nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		items = testharness.DecodeJSONArray(t, w)
		if len(items) >= 2 {
			firstID, _ := items[0]["id"].(float64)
			secondID, _ := items[1]["id"].(float64)
			if uint(firstID) != comment2ID || uint(secondID) != comment1ID {
				t.Fatalf("updated asc order = [%d %d], want [%d %d]", uint(firstID), uint(secondID), comment2ID, comment1ID)
			}
		}

		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/issue-repo/issues/1/comments?since="+url.QueryEscape(sinceTime.Format(time.RFC3339Nano)), nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		items = testharness.DecodeJSONArray(t, w)
		if len(items) != 1 {
			t.Fatalf("expected 1 comment after since filter, got %d", len(items))
		}
		if len(items) == 1 {
			firstID, _ := items[0]["id"].(float64)
			if uint(firstID) != comment1ID {
				t.Fatalf("since filter id = %d, want %d", uint(firstID), comment1ID)
			}
		}
	})
}

func TestCreateTagEndpoint(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "tag-repo",
		AutoInit:   true,
	})
	require.NoError(t, err)

	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/tag-repo/tags", map[string]any{
		"tag_name":         "v1.0.0",
		"message":          "release v1",
		"target_commitish": "main",
	})
	require.Equal(t, http.StatusCreated, w.Code, "first create should return 201")
	body := testharness.DecodeJSON(t, w)
	require.Equal(t, "v1.0.0", body["name"])

	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/tag-repo/tags", nil)
	require.Equal(t, http.StatusOK, w.Code)
	items := testharness.DecodeJSONArray(t, w)
	found := false
	for _, item := range items {
		if name, _ := item["name"].(string); name == "v1.0.0" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected created tag to appear in list, got %#v", items)
	}

	w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/tag-repo/tags", map[string]any{
		"tag_name":         "v1.0.0",
		"message":          "release v1",
		"target_commitish": "main",
	})
	require.Equal(t, http.StatusOK, w.Code, "duplicate create should return 200")
}

func TestListTags_NotFoundRepoReturns404(t *testing.T) {
	h := testharness.New(t)

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/does-not-exist/tags", nil)
	require.Equal(t, http.StatusNotFound, w.Code, "missing repo should map to 404")
}

func TestCreateIssue_NotFoundRepoReturns404(t *testing.T) {
	h := testharness.New(t)

	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/does-not-exist/issues", map[string]any{
		"title": "missing repo issue",
	})
	require.Equal(t, http.StatusNotFound, w.Code, "missing repo should map to 404 instead of 422")
}

// ─── PR Lifecycle ───────────────────────────────────────────────────────────

func TestPRLifecycle(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	// Seed: create repo with auto_init so there's a main branch.
	_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "pr-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	_, err = h.Svc.CreateLabel(ctx, "testuser/pr-repo", "pr-skill-label", "ededed", "used by skill tests")
	require.NoError(t, err)

	// Create a feature branch and commit a file to give the PR a real diff.
	if err := h.Svc.Git.CreateBranch(ctx, "testuser/pr-repo", "feature", "main"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	commitMsg := "add feature file"
	if _, err := h.Svc.Git.WriteFile(ctx, "testuser/pr-repo", "feature", "feature.txt", commitMsg, []byte("hello feature")); err != nil {
		t.Fatalf("write file: %v", err)
	}

	t.Run("Create", func(t *testing.T) {
		w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/pr-repo/pulls", map[string]any{
			"title": "feat: add X",
			"body":  "PR body",
			"head":  "feature",
			"base":  "main",
		})
		if w.Code != 201 {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		if body["title"] != "feat: add X" {
			t.Errorf("title: got %v, want feat: add X", body["title"])
		}
		if body["state"] != "open" {
			t.Errorf("state: got %v, want open", body["state"])
		}
		if num, ok := body["number"].(float64); !ok || int(num) != 1 {
			t.Errorf("number: got %v, want 1", body["number"])
		}
	})

	t.Run("Get", func(t *testing.T) {
		w := h.DoREST(t, "GET", "/api/v3/repos/testuser/pr-repo/pulls/1", nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		if body["title"] != "feat: add X" {
			t.Errorf("title: got %v, want feat: add X", body["title"])
		}
	})

	t.Run("List", func(t *testing.T) {
		// Seed a second repo with its own PR to test same-owner repo scoping.
		_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: h.User.Login,
			Name:       "other-pr-repo",
			AutoInit:   true,
		})
		if err != nil {
			t.Fatalf("seed other repo: %v", err)
		}
		if err := h.Svc.Git.CreateBranch(ctx, "testuser/other-pr-repo", "other-feat", "main"); err != nil {
			t.Fatalf("create branch in other repo: %v", err)
		}
		if _, err := h.Svc.Git.WriteFile(ctx, "testuser/other-pr-repo", "other-feat", "x.txt", "x", []byte("x")); err != nil {
			t.Fatalf("write file in other repo: %v", err)
		}
		wOther := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/other-pr-repo/pulls", map[string]any{
			"title": "foreign PR",
			"head":  "other-feat",
			"base":  "main",
		})
		if wOther.Code != 201 {
			t.Fatalf("seed foreign PR: expected 201, got %d: %s", wOther.Code, wOther.Body.String())
		}

		// Seed a different owner and repo to ensure the list honors the owner path segment too.
		otherOwner := db.User{Login: "otherpruser", Name: "Other PR User", Type: "User"}
		require.NoError(t, h.DB.Create(&otherOwner).Error)
		_, err = h.Svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: otherOwner.Login,
			Name:       "cross-owner-pr-repo",
			AutoInit:   true,
		})
		require.NoError(t, err)
		require.NoError(t, h.Svc.Git.CreateBranch(ctx, "otherpruser/cross-owner-pr-repo", "other-feat", "main"))
		_, err = h.Svc.Git.WriteFile(ctx, "otherpruser/cross-owner-pr-repo", "other-feat", "x.txt", "x", []byte("x"))
		require.NoError(t, err)
		_, err = h.Svc.CreatePR(ctx, service.CreatePRInput{
			RepoFullName: "otherpruser/cross-owner-pr-repo",
			Title:        "cross-owner PR",
			HeadRef:      "other-feat",
			BaseRef:      "main",
			AuthorLogin:  otherOwner.Login,
		})
		require.NoError(t, err)

		w := h.DoREST(t, "GET", "/api/v3/repos/testuser/pr-repo/pulls", nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		items := testharness.DecodeJSONArray(t, w)
		if len(items) < 1 {
			t.Fatal("expected at least 1 PR in list")
		}
		found := false
		for _, item := range items {
			if num, ok := item["number"].(float64); ok && int(num) == 1 {
				found = true
			}
			// Negative assertion: no PR from other-pr-repo should appear.
			if title, _ := item["title"].(string); title == "foreign PR" {
				t.Error("list must not include PRs from other repos (found 'foreign PR')")
			}
			if title, _ := item["title"].(string); title == "cross-owner PR" {
				t.Error("list must not include PRs from other owners (found 'cross-owner PR')")
			}
		}
		if !found {
			t.Error("list should include the PR we just created (number=1)")
		}
	})

	t.Run("Update", func(t *testing.T) {
		w := h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/pr-repo/pulls/1", map[string]any{
			"title": "feat: add X (v2)",
		})
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		if body["title"] != "feat: add X (v2)" {
			t.Errorf("title: got %v, want feat: add X (v2)", body["title"])
		}
	})

	t.Run("UpdateLabelsViaIssuePatch", func(t *testing.T) {
		w := h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/pr-repo/issues/1", map[string]any{
			"labels": []string{"pr-skill-label"},
		})
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		labels, ok := body["labels"].([]any)
		if !ok || len(labels) != 1 {
			t.Fatalf("expected 1 label in PATCH response, got %#v", body["labels"])
		}
		label, ok := labels[0].(map[string]any)
		if !ok || label["name"] != "pr-skill-label" {
			t.Fatalf("expected label pr-skill-label, got %#v", labels[0])
		}

		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/pr-repo/issues/1/labels", nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		items := testharness.DecodeJSONArray(t, w)
		if len(items) != 1 || items[0]["name"] != "pr-skill-label" {
			t.Fatalf("expected persisted label pr-skill-label, got %#v", items)
		}
	})

	t.Run("ListFiles", func(t *testing.T) {
		w := h.DoREST(t, "GET", "/api/v3/repos/testuser/pr-repo/pulls/1/files", nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		files := testharness.DecodeJSONArray(t, w)
		if len(files) == 0 {
			t.Fatal("expected at least 1 file in PR diff")
		}
		// Assert the returned file list contains the seeded file.
		foundFeature := false
		for _, f := range files {
			if f["filename"] == "feature.txt" {
				foundFeature = true
				if f["status"] == nil || f["status"] == "" {
					t.Error("feature.txt entry missing status")
				}
				for _, key := range []string{"additions", "deletions", "changes"} {
					if _, ok := f[key].(float64); !ok {
						t.Errorf("feature.txt entry missing or non-numeric %s: %v", key, f[key])
					}
				}
				break
			}
		}
		if !foundFeature {
			names := make([]string, len(files))
			for i, f := range files {
				names[i], _ = f["filename"].(string)
			}
			t.Errorf("expected feature.txt in PR files, got: %v", names)
		}
	})

	t.Run("ListCommits", func(t *testing.T) {
		w := h.DoREST(t, "GET", "/api/v3/repos/testuser/pr-repo/pulls/1/commits", nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		commits := testharness.DecodeJSONArray(t, w)
		if len(commits) == 0 {
			t.Fatal("expected at least 1 commit in PR")
		}
		// Assert the returned commits include the seeded commit message.
		foundCommit := false
		for _, c := range commits {
			sha, _ := c["sha"].(string)
			if sha == "" {
				t.Error("commit entry missing sha")
				continue
			}
			commitObj, ok := c["commit"].(map[string]any)
			if !ok {
				continue
			}
			msg, _ := commitObj["message"].(string)
			if strings.Contains(msg, commitMsg) {
				foundCommit = true
				author, ok := commitObj["author"].(map[string]any)
				if !ok {
					t.Error("seeded commit missing author object")
				} else if author["name"] == nil || author["name"] == "" {
					t.Error("seeded commit author missing name")
				}
				break
			}
		}
		if !foundCommit {
			msgs := make([]string, len(commits))
			for i, c := range commits {
				if co, ok := c["commit"].(map[string]any); ok {
					msgs[i], _ = co["message"].(string)
				}
			}
			t.Errorf("expected commit with message %q in PR commits, got: %v", commitMsg, msgs)
		}
	})

	t.Run("GetDeletedPRCommentDoesNotFallbackToIssueComment", func(t *testing.T) {
		issue, err := h.Svc.CreateIssue(ctx, service.CreateIssueInput{
			RepoFullName: "testuser/pr-repo",
			Title:        "collision issue",
			Body:         "body",
			AuthorLogin:  h.User.Login,
		})
		require.NoError(t, err)
		issueComment, err := h.Svc.CreateIssueComment(ctx, "testuser/pr-repo", issue.Number, "issue comment body", h.User.Login, nil)
		require.NoError(t, err)

		pr, err := h.Svc.GetPR(ctx, "testuser/pr-repo", 1)
		require.NoError(t, err)

		prComment := db.PRReviewComment{
			ID:           issueComment.ID, // force ID collision across tables
			AuthorLogin:  h.User.Login,
			Body:         "pr review comment body",
			CommitID:     pr.HeadSHA,
			Path:         "feature.txt",
			Line:         1,
			OriginalLine: 1,
			Side:         "RIGHT",
		}
		require.NoError(t, h.Svc.CreatePRReviewComment(ctx, pr.ID, &prComment))

		delPath := fmt.Sprintf("/api/v3/repos/testuser/pr-repo/pulls/comments/%d", prComment.ID)
		w := h.DoREST(t, "DELETE", delPath, nil)
		if w.Code != 204 {
			t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
		}

		getPath := fmt.Sprintf("/api/v3/repos/testuser/pr-repo/pulls/comments/%d", prComment.ID)
		w = h.DoREST(t, "GET", getPath, nil)
		if w.Code != 404 {
			t.Fatalf("expected 404 after deleting PR comment, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("MergeInvalidID", func(t *testing.T) {
		before := snapshotPRMergeStateREST(t, h, ctx, "testuser", "pr-repo", 1, "main", "feature")
		if before.merged || before.mergeSHA != "" {
			t.Fatalf("precondition failed: expected unmerged PR, got %+v", before)
		}
		w := h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/pr-repo/pulls/not-a-number/merge", map[string]any{
			"merge_method": "merge",
		})
		if w.Code != 422 {
			t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		msg, _ := body["message"].(string)
		if !strings.Contains(msg, "number must be a number") {
			t.Errorf("message: got %q, want substring %q", msg, "number must be a number")
		}
		after := snapshotPRMergeStateREST(t, h, ctx, "testuser", "pr-repo", 1, "main", "feature")
		if after != before {
			t.Errorf("post-merge state changed: got %+v, want %+v", after, before)
		}
	})

	t.Run("MergeNonexistentID", func(t *testing.T) {
		before := snapshotPRMergeStateREST(t, h, ctx, "testuser", "pr-repo", 1, "main", "feature")
		if before.merged || before.mergeSHA != "" {
			t.Fatalf("precondition failed: expected unmerged PR, got %+v", before)
		}
		w := h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/pr-repo/pulls/999/merge", map[string]any{
			"merge_method": "merge",
		})
		if w.Code != 404 {
			t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		if body["message"] != "Not Found" {
			t.Errorf("message: got %v, want Not Found", body["message"])
		}
		after := snapshotPRMergeStateREST(t, h, ctx, "testuser", "pr-repo", 1, "main", "feature")
		if after != before {
			t.Errorf("post-merge state changed: got %+v, want %+v", after, before)
		}
	})

	t.Run("Merge", func(t *testing.T) {
		w := h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/pr-repo/pulls/1/merge", map[string]any{
			"merge_method": "merge",
		})
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		if body["merged"] != true {
			t.Errorf("merged: got %v, want true", body["merged"])
		}
	})

	t.Run("GetAfterMerge", func(t *testing.T) {
		w := h.DoREST(t, "GET", "/api/v3/repos/testuser/pr-repo/pulls/1", nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		if body["state"] != "closed" {
			t.Errorf("state: got %v, want closed", body["state"])
		}
		if body["merged"] != true {
			t.Errorf("merged: got %v, want true", body["merged"])
		}
	})

	// Regression for issue #1296 Phase A: REST resolve/unresolve for
	// PR review-comment threads.
	t.Run("ReviewCommentResolveUnresolve_Issue1296", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()
		_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: h.User.Login,
			Name:       "pr-resolve-1296",
			AutoInit:   true,
		})
		require.NoError(t, err)
		require.NoError(t, h.Svc.Git.CreateBranch(ctx, "testuser/pr-resolve-1296", "feature", "main"))
		_, err = h.Svc.Git.WriteFile(ctx, "testuser/pr-resolve-1296", "feature", "feature.txt", "feat", []byte("hello"))
		require.NoError(t, err)
		w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/pr-resolve-1296/pulls", map[string]any{
			"title": "feat", "body": "", "head": "feature", "base": "main",
		})
		require.Equal(t, 201, w.Code, "create PR: %s", w.Body.String())

		pr, err := h.Svc.GetPR(ctx, "testuser/pr-resolve-1296", 1)
		require.NoError(t, err)
		comment := db.PRReviewComment{
			AuthorLogin:  h.User.Login,
			Body:         "needs change",
			CommitID:     pr.HeadSHA,
			Path:         "feature.txt",
			Line:         1,
			OriginalLine: 1,
			Side:         "RIGHT",
		}
		require.NoError(t, h.Svc.CreatePRReviewComment(ctx, pr.ID, &comment))

		basePath := fmt.Sprintf("/api/v3/repos/testuser/pr-resolve-1296/pulls/1/comments/%d", comment.ID)

		w = h.DoREST(t, "PUT", basePath+"/resolve", nil)
		require.Equal(t, 200, w.Code, "resolve: %s", w.Body.String())
		body := testharness.DecodeJSON(t, w)
		if body["resolved"] != true {
			t.Errorf("resolved after resolve: got %v, want true", body["resolved"])
		}

		w = h.DoREST(t, "PUT", basePath+"/unresolve", nil)
		require.Equal(t, 200, w.Code, "unresolve: %s", w.Body.String())
		body = testharness.DecodeJSON(t, w)
		if body["resolved"] != false {
			t.Errorf("resolved after unresolve: got %v, want false", body["resolved"])
		}
		if _, ok := body["resolved_by"]; !ok {
			t.Errorf("resolved_by field must be present (null until tracked)")
		}

		// Resource scoping: requesting resolve on a PR number that
		// doesn't own the comment must 404, not silently mutate the
		// foreign thread. Seed a second PR and try its URL with the
		// first PR's commentID.
		require.NoError(t, h.Svc.Git.CreateBranch(ctx, "testuser/pr-resolve-1296", "feature2", "main"))
		_, err = h.Svc.Git.WriteFile(ctx, "testuser/pr-resolve-1296", "feature2", "feature2.txt", "feat2", []byte("hello2"))
		require.NoError(t, err)
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/pr-resolve-1296/pulls", map[string]any{
			"title": "feat2", "body": "", "head": "feature2", "base": "main",
		})
		require.Equal(t, 201, w.Code, "create PR2: %s", w.Body.String())

		foreignPath := fmt.Sprintf("/api/v3/repos/testuser/pr-resolve-1296/pulls/2/comments/%d/resolve", comment.ID)
		w = h.DoREST(t, "PUT", foreignPath, nil)
		require.Equal(t, 404, w.Code, "cross-PR resolve must 404: %s", w.Body.String())

		refetched, err := h.Svc.GetPRReviewComment(ctx, comment.ID)
		require.NoError(t, err)
		if refetched.IsResolved {
			t.Errorf("foreign-PR resolve attempt mutated comment IsResolved=true; want false")
		}
	})
}

// TestGetBranch_WithSlashInName verifies that branch names containing slashes
// are handled correctly (issue #517).
func TestGetBranch_WithSlashInName(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	// Create a repo
	_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "branch-slash-test",
		AutoInit:   true,
	})
	require.NoError(t, err)

	// Create a branch with a slash in the name
	branchName := "feature/meta20260314165226-branch"
	err = h.Svc.Git.CreateBranch(ctx, "testuser/branch-slash-test", branchName, "main")
	require.NoError(t, err)

	// Test GetBranch with slash in branch name
	// The branch name in the URL path will contain the slash
	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/branch-slash-test/branches/"+branchName, nil)
	require.Equal(t, 200, w.Code, "expected 200, got %d: %s", w.Code, w.Body.String())

	body := testharness.DecodeJSON(t, w)
	if body["name"] != branchName {
		t.Errorf("name: got %v, want %s", body["name"], branchName)
	}
	if body["protected"] != false {
		t.Errorf("protected: got %v, want false", body["protected"])
	}
}

// TestGetBranchProtection_WithSlashInName verifies that branch protection
// endpoints work correctly for branch names containing slashes.
func TestGetBranchProtection_WithSlashInName(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	// Create a repo
	repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "branch-protect-slash-test",
		AutoInit:   true,
	})
	require.NoError(t, err)

	// Create a branch with a slash in the name
	branchName := "feature/protected-branch"
	err = h.Svc.Git.CreateBranch(ctx, "testuser/branch-protect-slash-test", branchName, "main")
	require.NoError(t, err)

	// Create branch protection
	bp := &db.BranchProtection{
		RepositoryID:  repo.ID,
		BranchName:    branchName,
		EnforceAdmins: false,
	}
	err = h.Svc.UpdateBranchProtection(ctx, bp)
	require.NoError(t, err)

	// Test GetBranchProtection with slash in branch name
	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/branch-protect-slash-test/branches/"+branchName+"/protection", nil)
	require.Equal(t, 200, w.Code, "expected 200, got %d: %s", w.Code, w.Body.String())

	body := testharness.DecodeJSON(t, w)
	// The URL should contain the full branch name
	url, _ := body["url"].(string)
	if !strings.Contains(url, branchName) {
		t.Errorf("url should contain branch name %q, got: %s", branchName, url)
	}
}

func TestPRMergeConflictFailure(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "pr-merge-conflict-repo",
		AutoInit:   true,
	})
	require.NoError(t, err)

	branchName := "conflict-branch"
	require.NoError(t, h.Svc.Git.CreateBranch(ctx, repo.FullName, branchName, "main"))
	_, err = h.Svc.Git.WriteFile(ctx, repo.FullName, branchName, "conflict.txt", "feature commit", []byte("feature content\n"))
	require.NoError(t, err)
	_, err = h.Svc.Git.WriteFile(ctx, repo.FullName, "main", "conflict.txt", "main commit", []byte("main content\n"))
	require.NoError(t, err)

	pr, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Conflict PR",
		HeadRef:      branchName,
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	require.NoError(t, err)

	before := snapshotPRMergeStateREST(t, h, ctx, h.User.Login, repo.Name, pr.Number, "main", branchName)
	if before.merged || before.mergeSHA != "" {
		t.Fatalf("precondition failed: expected unmerged PR, got %+v", before)
	}
	w := h.DoRESTJSON(t, "PUT", fmt.Sprintf("/api/v3/repos/%s/%s/pulls/%d/merge", h.User.Login, repo.Name, pr.Number), map[string]any{
		"merge_method": "merge",
	})
	if w.Code != 409 {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	body := testharness.DecodeJSON(t, w)
	msg, _ := body["message"].(string)
	if !strings.Contains(strings.ToLower(msg), "conflict") {
		t.Errorf("message: got %q, want substring %q", msg, "conflict")
	}

	after := snapshotPRMergeStateREST(t, h, ctx, h.User.Login, repo.Name, pr.Number, "main", branchName)
	if after != before {
		t.Errorf("post-merge state changed: got %+v, want %+v", after, before)
	}
}

type restPRMergeSnapshot struct {
	merged   bool
	mergeSHA string
	baseSHA  string
	headSHA  string
}

func snapshotPRMergeStateREST(t *testing.T, h *testharness.Harness, ctx context.Context, owner, repo string, number int, baseRef, headRef string) restPRMergeSnapshot {
	t.Helper()

	w := h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/%s/pulls/%d", owner, repo, number), nil)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := testharness.DecodeJSON(t, w)
	merged, _ := body["merged"].(bool)
	mergeSHA, _ := body["merge_commit_sha"].(string)

	fullName := owner + "/" + repo
	baseSHA, err := h.Svc.Git.HeadSHA(ctx, fullName, baseRef)
	require.NoError(t, err)
	headSHA, err := h.Svc.Git.HeadSHA(ctx, fullName, headRef)
	require.NoError(t, err)

	return restPRMergeSnapshot{
		merged:   merged,
		mergeSHA: mergeSHA,
		baseSHA:  baseSHA,
		headSHA:  headSHA,
	}
}
