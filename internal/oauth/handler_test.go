package oauth_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/oauth"
	"github.com/ngaut/agent-git-service/internal/service"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// assertHex checks that s is exactly n characters of lowercase hex [0-9a-f].
func assertHex(t *testing.T, name, s string, n int) {
	t.Helper()
	if len(s) != n {
		t.Errorf("%s: expected length %d, got %d (%q)", name, n, len(s), s)
	}
	if !regexp.MustCompile(`^[0-9a-f]+$`).MatchString(s) {
		t.Errorf("%s: expected only hex chars [0-9a-f], got %q", name, s)
	}
}

var testDBCounter atomic.Int64

func setupTestService(t *testing.T) *service.Service {
	t.Helper()
	dsn := fmt.Sprintf("file:oauth_test_%d?mode=memory&cache=shared", testDBCounter.Add(1))
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	if err := gdb.AutoMigrate(&db.User{}, &db.Token{}, &db.DeviceCode{}, &db.DeviceCodeAuditLog{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	if err := gdb.AutoMigrate(&db.AuthorizationCode{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	// Create a user for token exchange
	gdb.Create(&db.User{Login: "admin", Type: "User", SiteAdmin: true})

	tmpDir, err := os.MkdirTemp("", "oauth-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	gs, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatalf("failed to init gitstore: %v", err)
	}
	return &service.Service{DB: gdb, Git: gs, BaseURL: "http://localhost:8080"}
}

func TestRequestDeviceCode(t *testing.T) {
	svc := setupTestService(t)
	h := oauth.New(svc)

	r := httptest.NewRequest("POST", "/login/device/code", strings.NewReader("client_id=test&scope=repo"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.RequestDeviceCode(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Must have required fields
	for _, key := range []string{"device_code", "user_code", "verification_uri", "expires_in", "interval"} {
		if _, ok := body[key]; !ok {
			t.Errorf("missing field %q in response", key)
		}
	}

	// device_code must be exactly 32 lowercase hex characters
	deviceCode, _ := body["device_code"].(string)
	assertHex(t, "device_code", deviceCode, 32)

	// user_code should match XXXX-XXXX pattern
	userCode, _ := body["user_code"].(string)
	if len(userCode) != 9 || userCode[4] != '-' {
		t.Errorf("unexpected user_code format: %q", userCode)
	}

	// verification_uri should use the request host
	verifyURI, _ := body["verification_uri"].(string)
	if !strings.HasSuffix(verifyURI, "/login/device") {
		t.Errorf("unexpected verification_uri: %q", verifyURI)
	}
}

func TestAccessToken_JSONBody(t *testing.T) {
	svc := setupTestService(t)
	h := oauth.New(svc)

	// First, create a device code via RequestDeviceCode
	r1 := httptest.NewRequest("POST", "/login/device/code", nil)
	w1 := httptest.NewRecorder()
	h.RequestDeviceCode(w1, r1)
	var codeResp map[string]any
	if err := json.NewDecoder(w1.Body).Decode(&codeResp); err != nil {
		t.Fatalf("failed to decode device code response: %v", err)
	}
	deviceCode := codeResp["device_code"].(string)

	// Approve the device code (simulate user verification)
	_, err := svc.ApproveDeviceCode(context.Background(), deviceCode, 1, "admin")
	if err != nil {
		t.Fatalf("failed to approve device code: %v", err)
	}

	// Exchange it via JSON body
	jsonBody := `{"device_code":"` + deviceCode + `"}`
	r2 := httptest.NewRequest("POST", "/login/oauth/access_token", strings.NewReader(jsonBody))
	r2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	h.AccessToken(w2, r2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w2.Code, w2.Body.String())
	}
	var tokenResp map[string]any
	if err := json.NewDecoder(w2.Body).Decode(&tokenResp); err != nil {
		t.Fatalf("failed to decode token response: %v", err)
	}
	if tokenResp["access_token"] == nil || tokenResp["access_token"] == "" {
		t.Error("expected non-empty access_token")
	}
	// access_token must be exactly 40 lowercase hex characters
	accessToken, _ := tokenResp["access_token"].(string)
	assertHex(t, "access_token", accessToken, 40)

	if tokenResp["token_type"] != "bearer" {
		t.Errorf("expected token_type=bearer, got %v", tokenResp["token_type"])
	}
}

func TestAccessToken_FormBody(t *testing.T) {
	svc := setupTestService(t)
	h := oauth.New(svc)

	// Create a device code
	r1 := httptest.NewRequest("POST", "/login/device/code", nil)
	w1 := httptest.NewRecorder()
	h.RequestDeviceCode(w1, r1)
	var codeResp map[string]any
	if err := json.NewDecoder(w1.Body).Decode(&codeResp); err != nil {
		t.Fatalf("failed to decode device code response: %v", err)
	}
	deviceCode := codeResp["device_code"].(string)

	// Approve the device code (simulate user verification)
	_, err := svc.ApproveDeviceCode(context.Background(), deviceCode, 1, "admin")
	if err != nil {
		t.Fatalf("failed to approve device code: %v", err)
	}

	// Exchange it via form body
	r2 := httptest.NewRequest("POST", "/login/oauth/access_token", strings.NewReader("device_code="+deviceCode))
	r2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w2 := httptest.NewRecorder()
	h.AccessToken(w2, r2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w2.Code)
	}
}

func TestAccessToken_InvalidCode(t *testing.T) {
	svc := setupTestService(t)
	h := oauth.New(svc)

	r := httptest.NewRequest("POST", "/login/oauth/access_token",
		strings.NewReader(`{"device_code":"nonexistent"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.AccessToken(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body["error"] != "bad_verification_code" {
		t.Errorf("expected error=bad_verification_code, got %v", body["error"])
	}
}

func TestAccessToken_AuthorizationPending(t *testing.T) {
	svc := setupTestService(t)
	h := oauth.New(svc)

	// Insert a device code directly with empty AccessToken to simulate pending approval
	svc.DB.Create(&db.DeviceCode{
		DeviceCode: "pending-code-123",
		UserCode:   "ABCD-EFGH",
		ExpiresAt:  time.Now().Add(15 * time.Minute),
		// AccessToken intentionally empty
	})

	r := httptest.NewRequest("POST", "/login/oauth/access_token",
		strings.NewReader(`{"device_code":"pending-code-123"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.AccessToken(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body["error"] != "authorization_pending" {
		t.Errorf("expected error=authorization_pending, got %v", body["error"])
	}
}

func TestAccessToken_AuthorizationCodePKCE(t *testing.T) {
	svc := setupTestService(t)
	h := oauth.New(svc)

	var user db.User
	if err := svc.DB.First(&user, "login = ?", "admin").Error; err != nil {
		t.Fatalf("load user: %v", err)
	}

	codeVerifier := "verifier-" + strings.ReplaceAll(t.Name(), "/", "-")
	sum := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(sum[:])

	req := httptest.NewRequest("GET", "/login/oauth/authorize?redirect_uri=http%3A%2F%2Fexample.com%2Fcallback&state=test-state&code_challenge="+url.QueryEscape(codeChallenge)+"&code_challenge_method=S256", nil)
	req.Host = "example.com"
	req = req.WithContext(service.ContextWithUser(req.Context(), user))
	w := httptest.NewRecorder()
	h.Authorize(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", w.Code)
	}
	redirectLocation := w.Header().Get("Location")
	parsed, err := url.Parse(redirectLocation)
	if err != nil {
		t.Fatalf("parse redirect location: %v", err)
	}
	authCode := parsed.Query().Get("code")
	if authCode == "" {
		t.Fatal("expected authorization code in redirect")
	}

	var stored db.AuthorizationCode
	if err := svc.DB.First(&stored, "code = ?", authCode).Error; err != nil {
		t.Fatalf("load authorization code: %v", err)
	}
	if stored.CodeChallenge != codeChallenge {
		t.Fatalf("stored code_challenge = %q, want %q", stored.CodeChallenge, codeChallenge)
	}
	if stored.CodeChallengeMethod != "S256" {
		t.Fatalf("stored code_challenge_method = %q, want S256", stored.CodeChallengeMethod)
	}

	exchangeReq := httptest.NewRequest("POST", "/login/oauth/access_token",
		strings.NewReader(`{"code":"`+authCode+`","code_verifier":"`+codeVerifier+`"}`))
	exchangeReq.Header.Set("Content-Type", "application/json")
	exchangeW := httptest.NewRecorder()
	h.AccessToken(exchangeW, exchangeReq)

	if exchangeW.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", exchangeW.Code, exchangeW.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(exchangeW.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["access_token"] == "" {
		t.Fatal("expected non-empty access_token")
	}
}

func TestAccessToken_AuthorizationCodeRequiresMatchingVerifier(t *testing.T) {
	svc := setupTestService(t)
	h := oauth.New(svc)

	user := db.User{Login: "pkce-user", Type: db.TypeUser}
	if err := svc.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	codeVerifier := "correct-verifier-123"
	sum := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(sum[:])

	makeCode := func(codeValue string) {
		t.Helper()
		if err := svc.DB.Create(&db.AuthorizationCode{
			Code:                codeValue,
			UserID:              &user.ID,
			RedirectURI:         "http://example.com/callback",
			CodeChallenge:       codeChallenge,
			CodeChallengeMethod: "S256",
			ExpiresAt:           time.Now().UTC().Add(service.AuthorizationCodeTTL),
			CreatedAt:           time.Now().UTC(),
		}).Error; err != nil {
			t.Fatalf("create authorization code: %v", err)
		}
	}

	makeCode("missing-verifier")
	reqMissing := httptest.NewRequest("POST", "/login/oauth/access_token", strings.NewReader(`{"code":"missing-verifier"}`))
	reqMissing.Header.Set("Content-Type", "application/json")
	wMissing := httptest.NewRecorder()
	h.AccessToken(wMissing, reqMissing)
	if wMissing.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing verifier, got %d", wMissing.Code)
	}
	var bodyMissing map[string]any
	if err := json.NewDecoder(wMissing.Body).Decode(&bodyMissing); err != nil {
		t.Fatalf("decode missing-verifier body: %v", err)
	}
	if bodyMissing["error"] != "bad_verification_code" {
		t.Fatalf("expected bad_verification_code, got %v", bodyMissing["error"])
	}

	makeCode("wrong-verifier")
	reqWrong := httptest.NewRequest("POST", "/login/oauth/access_token",
		strings.NewReader(`{"code":"wrong-verifier","code_verifier":"wrong"}`))
	reqWrong.Header.Set("Content-Type", "application/json")
	wWrong := httptest.NewRecorder()
	h.AccessToken(wWrong, reqWrong)
	if wWrong.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for wrong verifier, got %d", wWrong.Code)
	}
	var bodyWrong map[string]any
	if err := json.NewDecoder(wWrong.Body).Decode(&bodyWrong); err != nil {
		t.Fatalf("decode wrong-verifier body: %v", err)
	}
	if bodyWrong["error"] != "bad_verification_code" {
		t.Fatalf("expected bad_verification_code, got %v", bodyWrong["error"])
	}
}

func TestAuthorize_NoRedirectURI(t *testing.T) {
	svc := setupTestService(t)
	h := oauth.New(svc)

	r := httptest.NewRequest("GET", "/login/oauth/authorize", nil)
	w := httptest.NewRecorder()
	h.Authorize(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 when no redirect_uri, got %d", w.Code)
	}
}

func TestAuthorize_SameOriginRedirect(t *testing.T) {
	svc := setupTestService(t)
	h := oauth.New(svc)

	// PKCE and state are now required
	r := httptest.NewRequest("GET", "/login/oauth/authorize?redirect_uri=http%3A%2F%2Fexample.com%2Fcallback&state=test-state&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256", nil)
	r.Host = "example.com"
	w := httptest.NewRecorder()
	h.Authorize(w, r)

	if w.Code != http.StatusFound {
		t.Errorf("expected 302, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "http://example.com/callback?") {
		t.Errorf("unexpected redirect location: %q", loc)
	}
	if !strings.Contains(loc, "state=test-state") {
		t.Errorf("expected state to be echoed in redirect, got %q", loc)
	}
	// extract and validate code
	u, _ := url.Parse(loc)
	code := u.Query().Get("code")
	assertHex(t, "redirect_code", code, 32)
}

func TestAuthorize_LocalhostRedirect(t *testing.T) {
	svc := setupTestService(t)
	h := oauth.New(svc)

	// PKCE and state are now required
	r := httptest.NewRequest("GET", "/login/oauth/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A9999%2Fcallback&state=test-state&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256", nil)
	r.Host = "example.com"
	w := httptest.NewRecorder()
	h.Authorize(w, r)

	if w.Code != http.StatusFound {
		t.Errorf("expected 302 for localhost redirect, got %d", w.Code)
	}
}

func TestAuthorize_RedirectWithExistingQueryParam(t *testing.T) {
	svc := setupTestService(t)
	h := oauth.New(svc)

	// redirect_uri already contains a query param; state is provided separately and echoed
	// PKCE and state are now required
	r := httptest.NewRequest("GET", "/login/oauth/authorize?redirect_uri=http%3A%2F%2Fexample.com%2Fcallback%3Ffoo%3Dbar&state=test-state&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256", nil)
	r.Host = "example.com"
	w := httptest.NewRecorder()
	h.Authorize(w, r)

	if w.Code != http.StatusFound {
		t.Errorf("expected 302, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "http://example.com/callback?") {
		t.Errorf("expected redirect to callback with query params, got %q", loc)
	}
	if !strings.Contains(loc, "foo=bar") {
		t.Errorf("expected original query param preserved, got %q", loc)
	}
	if !strings.Contains(loc, "state=test-state") {
		t.Errorf("expected state to be echoed in redirect, got %q", loc)
	}
}

func TestAuthorize_CrossOriginBlocked(t *testing.T) {
	svc := setupTestService(t)
	h := oauth.New(svc)

	// Include required PKCE params to test cross-origin check specifically
	r := httptest.NewRequest("GET", "/login/oauth/authorize?redirect_uri=http%3A%2F%2Fevil.com%2Fcallback&state=test-state&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256", nil)
	r.Host = "example.com"
	w := httptest.NewRecorder()
	h.Authorize(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for cross-origin redirect, got %d", w.Code)
	}
}

func TestAuthorize_MissingState(t *testing.T) {
	svc := setupTestService(t)
	h := oauth.New(svc)

	r := httptest.NewRequest("GET", "/login/oauth/authorize?redirect_uri=http%3A%2F%2Fexample.com%2Fcallback&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256", nil)
	r.Host = "example.com"
	w := httptest.NewRecorder()
	h.Authorize(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing state, got %d", w.Code)
	}
}

func TestAuthorize_MissingPKCE(t *testing.T) {
	svc := setupTestService(t)
	h := oauth.New(svc)

	r := httptest.NewRequest("GET", "/login/oauth/authorize?redirect_uri=http%3A%2F%2Fexample.com%2Fcallback&state=test-state", nil)
	r.Host = "example.com"
	w := httptest.NewRecorder()
	h.Authorize(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing PKCE, got %d", w.Code)
	}
}

func TestAuthorize_InvalidPKCEMethod(t *testing.T) {
	svc := setupTestService(t)
	h := oauth.New(svc)

	r := httptest.NewRequest("GET", "/login/oauth/authorize?redirect_uri=http%3A%2F%2Fexample.com%2Fcallback&state=test-state&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=plain", nil)
	r.Host = "example.com"
	w := httptest.NewRecorder()
	h.Authorize(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid PKCE method, got %d", w.Code)
	}
}

func TestAccessToken_InvalidJSONBody(t *testing.T) {
	svc := setupTestService(t)
	h := oauth.New(svc)

	r := httptest.NewRequest("POST", "/login/oauth/access_token",
		strings.NewReader(`{invalid json}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.AccessToken(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body["error"] != "bad_verification_code" {
		t.Errorf("expected error=bad_verification_code, got %v", body["error"])
	}
}

func TestAccessToken_BodySizeLimitExceeded(t *testing.T) {
	svc := setupTestService(t)
	h := oauth.New(svc)

	// Create a body larger than 1MB (1<<20 bytes)
	largeBody := strings.Repeat("x", 1<<20+100)
	r := httptest.NewRequest("POST", "/login/oauth/access_token",
		strings.NewReader(`{"device_code":"`+largeBody+`"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.AccessToken(w, r)

	// Should return 400 due to body size limit
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for body size limit exceeded, got %d", w.Code)
	}
}

func TestAuthorize_InvalidRedirectURI_Malformed(t *testing.T) {
	svc := setupTestService(t)
	h := oauth.New(svc)

	// Malformed URL (missing scheme)
	r := httptest.NewRequest("GET", "/login/oauth/authorize?redirect_uri=not-a-valid-url", nil)
	w := httptest.NewRecorder()
	h.Authorize(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for malformed redirect_uri, got %d", w.Code)
	}
}

func TestAuthorize_InvalidRedirectURI_InvalidScheme(t *testing.T) {
	svc := setupTestService(t)
	h := oauth.New(svc)

	testCases := []struct {
		name string
		uri  string
	}{
		{"javascript scheme", "javascript:alert(1)"},
		{"data scheme", "data:text/html,<h1>test</h1>"},
		{"file scheme", "file:///etc/passwd"},
		{"empty scheme", "://example.com"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/login/oauth/authorize?redirect_uri="+url.QueryEscape(tc.uri), nil)
			w := httptest.NewRecorder()
			h.Authorize(w, r)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400 for %s (%q), got %d", tc.name, tc.uri, w.Code)
			}
		})
	}
}
