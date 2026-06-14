package middleware

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/service"

	"github.com/go-chi/chi/v5"
	"github.com/ngaut/agent-git-service/internal/testharness/testdb"
)

func base64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// okHandler is a simple handler that writes 200 OK.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
})

func withOwnerParam(r *http.Request, owner string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("owner", owner)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func seedUserWithToken(t *testing.T, svc *service.Service, login string, token string) db.User {
	t.Helper()
	user := db.User{
		Login: login,
		Name:  login,
		Type:  db.TypeUser,
	}
	if err := svc.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if token != "" {
		if err := svc.DB.Create(&db.Token{UserID: user.ID, Value: token}).Error; err != nil {
			t.Fatalf("create token: %v", err)
		}
	}
	return user
}

func setupTestService(t *testing.T) *service.Service {
	t.Helper()
	gdb, cleanup := testdb.OpenRaw(t, "middleware")
	t.Cleanup(cleanup)
	if err := gdb.AutoMigrate(&db.User{}, &db.Token{}, &db.DeviceCode{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	tmpDir, err := os.MkdirTemp("", "middleware-test-")
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

func TestExtractToken(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"empty", "", ""},
		{"token prefix", "token ghp_abc123", "ghp_abc123"},
		{"Token prefix mixed case", "Token ghp_abc123", "ghp_abc123"},
		{"bearer prefix", "bearer mytoken", "mytoken"},
		{"Bearer prefix", "Bearer mytoken", "mytoken"},
		{"basic auth extracts password", "Basic dXNlcjpwYXNz", "pass"},
		{"basic auth no colon", "Basic " + base64Encode("nopassword"), ""},
		{"basic auth empty password", "Basic " + base64Encode("user:"), ""},
		{"basic auth bad base64", "Basic !!!invalid!!!", ""},
		{"token with extra spaces", "  token   spaced  ", "spaced"},
		{"token only prefix", "token ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			if tt.header != "" {
				r.Header.Set("Authorization", tt.header)
			}
			got := ExtractToken(r)
			if got != tt.want {
				t.Errorf("ExtractToken(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestTokenAuth_NoHeader(t *testing.T) {
	svc := setupTestService(t)
	handler := TokenAuth(svc)(okHandler)

	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body["message"] != "Requires authentication" {
		t.Errorf("unexpected message: %v", body["message"])
	}
}

func TestTokenAuth_NoTokensInDB_DefaultRejects(t *testing.T) {
	// Default (AllowAnyToken=false): empty token table rejects any token
	svc := setupTestService(t)
	handler := TokenAuth(svc)(okHandler)

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "token anyvalue")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestTokenAuth_NoTokensInDB_AllowAnyToken(t *testing.T) {
	// AllowAnyToken=true: empty token table accepts any non-empty token
	svc := setupTestService(t)
	svc.AllowAnyToken = true
	svc.DB.Create(&db.User{Login: "admin", Name: "admin", Type: db.TypeUser, SiteAdmin: true})
	handler := TokenAuth(svc)(okHandler)

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "token anyvalue")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestTokenAuth_InvalidToken_WithTokensInDB(t *testing.T) {
	svc := setupTestService(t)
	// Insert a real token so validation becomes strict
	seedUserWithToken(t, svc, "valid-user", "valid-token")

	handler := TokenAuth(svc)(okHandler)

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "token wrong-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body["message"] != "Bad credentials" {
		t.Errorf("unexpected message: %v", body["message"])
	}
}

func TestTokenAuth_ValidToken_WithTokensInDB(t *testing.T) {
	svc := setupTestService(t)
	seedUserWithToken(t, svc, "valid-user", "valid-token")

	handler := TokenAuth(svc)(okHandler)

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "token valid-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestOptionalTokenAuth_NoHeader(t *testing.T) {
	svc := setupTestService(t)
	handler := OptionalTokenAuth(svc)(okHandler)

	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (no auth header), got %d", w.Code)
	}
}

func TestOptionalTokenAuth_BasicAuth_ExtractsToken(t *testing.T) {
	// Basic auth is now supported: password portion is used as token.
	// With a valid token in DB, Basic auth should succeed.
	svc := setupTestService(t)
	seedUserWithToken(t, svc, "basic-user", "pass")
	handler := OptionalTokenAuth(svc)(okHandler)

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Basic dXNlcjpwYXNz") // user:pass
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for valid Basic auth token, got %d", w.Code)
	}
}

func TestOptionalTokenAuth_BasicAuth_InvalidToken(t *testing.T) {
	// Basic auth with an invalid token should be rejected.
	svc := setupTestService(t)
	seedUserWithToken(t, svc, "valid-user", "valid-token")
	handler := OptionalTokenAuth(svc)(okHandler)

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Basic dXNlcjpwYXNz") // user:pass (not valid-token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid Basic auth token, got %d", w.Code)
	}
}

func TestOptionalTokenAuth_UnrecognizedScheme(t *testing.T) {
	svc := setupTestService(t)
	handler := OptionalTokenAuth(svc)(okHandler)

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Digest realm=test")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for unrecognized auth scheme, got %d", w.Code)
	}
}

func TestOptionalTokenAuth_NoTokensInDB_DefaultRejects(t *testing.T) {
	// Default (AllowAnyToken=false): optional auth with empty token table rejects
	svc := setupTestService(t)
	handler := OptionalTokenAuth(svc)(okHandler)

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "token anyvalue")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with default secure mode, got %d", w.Code)
	}
}

func TestOptionalTokenAuth_NoTokensInDB_AllowAnyToken(t *testing.T) {
	// AllowAnyToken=true: optional auth with empty token table accepts
	svc := setupTestService(t)
	svc.AllowAnyToken = true
	svc.DB.Create(&db.User{Login: "admin", Name: "admin", Type: db.TypeUser, SiteAdmin: true})
	handler := OptionalTokenAuth(svc)(okHandler)

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "token anyvalue")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with AllowAnyToken, got %d", w.Code)
	}
}

func TestOptionalTokenAuth_InvalidToken_WithTokensInDB(t *testing.T) {
	svc := setupTestService(t)
	seedUserWithToken(t, svc, "valid-user", "valid-token")

	handler := OptionalTokenAuth(svc)(okHandler)

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "token wrong-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid token with tokens in DB, got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body["message"] != "Bad credentials" {
		t.Errorf("unexpected message: %v", body["message"])
	}
}

func TestMaxBodySize(t *testing.T) {
	handler := MaxBodySize(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	// Small body — should pass
	r := httptest.NewRequest("POST", "/", strings.NewReader("short"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for small body, got %d", w.Code)
	}

	// Large body — should fail when handler reads
	r = httptest.NewRequest("POST", "/", strings.NewReader("this body exceeds the ten byte limit"))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413 for large body, got %d", w.Code)
	}
}

func TestMaxBodySizeUnless(t *testing.T) {
	handler := MaxBodySizeUnless(10, func(r *http.Request) bool {
		return r.URL.Path == "/skip"
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("POST", "/skip", strings.NewReader("this body exceeds the ten byte limit"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("expected skipped route to bypass body limit, got %d", w.Code)
	}

	r = httptest.NewRequest("POST", "/enforce", strings.NewReader("this body exceeds the ten byte limit"))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected non-skipped route to enforce body limit, got %d", w.Code)
	}
}
