package controlplane_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"gh-server/internal/controlplane"
	"gh-server/internal/db"
	"gh-server/internal/githttp"
	"gh-server/internal/gitstore"
	"gh-server/internal/graphql"
	"gh-server/internal/oauth"
	"gh-server/internal/rest"
	"gh-server/internal/router"
	"gh-server/internal/service"
)

var testSeq uint64

// setupControlPlaneTest creates a test environment with control plane router
// and seeds agents with different states.
func setupControlPlaneTest(t *testing.T) (*controlplane.DBRouter, *service.Service, http.Handler, func()) {
	t.Helper()

	// Create control plane DB
	cpDBPath := fmt.Sprintf("file:cp_mem_%d?mode=memory&cache=shared", atomic.AddUint64(&testSeq, 1))
	cpDB, err := gorm.Open(sqlite.Open(cpDBPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open control plane DB: %v", err)
	}

	// Migrate control plane models
	if err := cpDB.AutoMigrate(&controlplane.CPUser{}, &controlplane.CPToken{}); err != nil {
		t.Fatalf("failed to migrate control plane DB: %v", err)
	}

	// Create tenant DB
	tenantDBPath := fmt.Sprintf("file:tenant_mem_%d?mode=memory&cache=shared", atomic.AddUint64(&testSeq, 1))
	tenantDB, err := gorm.Open(sqlite.Open(tenantDBPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open tenant DB: %v", err)
	}

	// Migrate tenant models
	if err := db.Migrate(tenantDB); err != nil {
		t.Fatalf("failed to migrate tenant DB: %v", err)
	}

	// Create gitstore
	tmpDir, err := os.MkdirTemp("", "controlplane-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	gitStore, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatalf("failed to create gitstore: %v", err)
	}

	// Create service with tenant DB
	svc := &service.Service{
		DB:      tenantDB,
		Git:     gitStore,
		BaseURL: "http://localhost:8080",
	}

	// Create control plane router
	var openDBCalls atomic.Int64
	testOpenDB := func(dsn string) (*gorm.DB, error) {
		openDBCalls.Add(1)
		// Return the same tenant DB for all opens (simulating real multi-tenant)
		return tenantDB, nil
	}
	cpRouter := controlplane.NewDBRouter(cpDB, testOpenDB, true, controlplane.RouterConfig{MaxAgents: 10})

	// Seed active agent
	activeUser := controlplane.CPUser{Login: "active-agent", Email: "active@test.com", DSN: "tenant-dsn", State: controlplane.AgentStateActive}
	cpDB.Create(&activeUser)
	cpDB.Create(&controlplane.CPToken{CPUserID: activeUser.ID, Value: "active-token"})

	// Seed pending agent
	pendingUser := controlplane.CPUser{Login: "pending-agent", Email: "pending@test.com", DSN: "tenant-dsn", State: controlplane.AgentStatePending}
	cpDB.Create(&pendingUser)
	cpDB.Create(&controlplane.CPToken{CPUserID: pendingUser.ID, Value: "pending-token"})

	// Seed failed agent
	failedUser := controlplane.CPUser{Login: "failed-agent", Email: "failed@test.com", DSN: "tenant-dsn", State: controlplane.AgentStateFailed}
	cpDB.Create(&failedUser)
	cpDB.Create(&controlplane.CPToken{CPUserID: failedUser.ID, Value: "failed-token"})

	// Wire handlers
	handlers := &rest.Deps{Svc: svc, Router: cpRouter}
	gqlSrv := graphql.NewServer(svc)
	gitHandler := githttp.New(gitStore, svc)
	oauthHandler := oauth.New(svc)

	mux := router.RegisterRoutes(chi.NewRouter(), handlers, gitHandler, gqlSrv, oauthHandler, cpRouter, "http://console.localhost")

	cleanup := func() {
		cpRouter.Close()
		os.RemoveAll(tmpDir)
		if sqlDB, err := cpDB.DB(); err == nil {
			sqlDB.Close()
		}
		if sqlDB, err := tenantDB.DB(); err == nil {
			sqlDB.Close()
		}
	}

	return cpRouter, svc, mux, cleanup
}

// TestControlPlane_NonActiveToken_DeniedOnUserEndpoint tests that non-active
// control-plane tokens are denied on /api/v3/user endpoint.
func TestControlPlane_NonActiveToken_DeniedOnUserEndpoint(t *testing.T) {
	_, _, mux, cleanup := setupControlPlaneTest(t)
	defer cleanup()

	tests := []struct {
		name         string
		token        string
		expectedCode int
		expectedMsg  string
	}{
		{"active token allowed", "active-token", http.StatusOK, ""},
		{"pending token denied", "pending-token", http.StatusUnauthorized, "Bad credentials"},
		{"failed token denied", "failed-token", http.StatusUnauthorized, "Bad credentials"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v3/user", nil)
			req.Header.Set("Authorization", "token "+tt.token)
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != tt.expectedCode {
				t.Errorf("expected status %d, got %d: %s", tt.expectedCode, w.Code, w.Body.String())
			}

			if tt.expectedCode != http.StatusOK {
				var resp map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &resp); err == nil {
					if msg, ok := resp["message"].(string); ok && msg != tt.expectedMsg {
						t.Errorf("expected message %q, got %q", tt.expectedMsg, msg)
					}
				}
			}
		})
	}
}

// TestControlPlane_NonActiveToken_DeniedOnGitRoutes tests that non-active
// control-plane tokens are denied on git transport routes.
func TestControlPlane_NonActiveToken_DeniedOnGitRoutes(t *testing.T) {
	_, svc, mux, cleanup := setupControlPlaneTest(t)
	defer cleanup()

	// Create a test repository in tenant DB
	repo := db.Repository{
		OwnerID:       1,
		FullName:      "active-agent/test-repo",
		Name:          "test-repo",
		DefaultBranch: "main",
	}
	svc.DB.Create(&repo)

	// Initialize git repo
	if err := svc.Git.Init(context.Background(), "active-agent/test-repo", "main", false); err != nil {
		t.Fatalf("failed to init repo: %v", err)
	}

	tests := []struct {
		name         string
		token        string
		path         string
		method       string
		expectedCode int
	}{
		{"active token - info/refs", "active-token", "/active-agent/test-repo/info/refs", "GET", http.StatusOK},
		{"pending token - info/refs denied", "pending-token", "/active-agent/test-repo/info/refs", "GET", http.StatusUnauthorized},
		{"failed token - info/refs denied", "failed-token", "/active-agent/test-repo/info/refs", "GET", http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("Authorization", "token "+tt.token)
			// Git requests use service=git-upload-pack or service=git-receive-pack query param
			if strings.Contains(tt.path, "info/refs") {
				req.URL.RawQuery = "service=git-upload-pack"
			}

			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != tt.expectedCode {
				t.Errorf("expected status %d, got %d: %s", tt.expectedCode, w.Code, w.Body.String())
			}
		})
	}
}

// TestControlPlane_NonActiveToken_NoSideEffects verifies that requests with
// non-active tokens produce no side effects in the database.
func TestControlPlane_NonActiveToken_NoSideEffects(t *testing.T) {
	_, svc, mux, cleanup := setupControlPlaneTest(t)
	defer cleanup()

	// Count repos before
	var repoCountBefore int64
	svc.DB.Model(&db.Repository{}).Count(&repoCountBefore)

	// Attempt to create a repo with pending token (should fail)
	req := httptest.NewRequest("POST", "/api/v3/user/repos", bytes.NewReader([]byte(`{"name":"should-not-exist"}`)))
	req.Header.Set("Authorization", "token pending-token")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}

	// Count repos after - should be unchanged
	var repoCountAfter int64
	svc.DB.Model(&db.Repository{}).Count(&repoCountAfter)

	if repoCountAfter != repoCountBefore {
		t.Errorf("repository count changed: before=%d, after=%d (should be unchanged)", repoCountBefore, repoCountAfter)
	}

	// Also test with failed token
	req2 := httptest.NewRequest("POST", "/api/v3/user/repos", bytes.NewReader([]byte(`{"name":"should-not-exist-2"}`)))
	req2.Header.Set("Authorization", "token failed-token")
	req2.Header.Set("Content-Type", "application/json")

	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w2.Code, w2.Body.String())
	}

	var repoCountFinal int64
	svc.DB.Model(&db.Repository{}).Count(&repoCountFinal)

	if repoCountFinal != repoCountBefore {
		t.Errorf("repository count changed after failed token: before=%d, after=%d (should be unchanged)", repoCountBefore, repoCountFinal)
	}
}

// TestControlPlane_NonActiveToken_DenialMatrix tests the full denial matrix
// across different endpoints and token states.
func TestControlPlane_NonActiveToken_DenialMatrix(t *testing.T) {
	_, svc, mux, cleanup := setupControlPlaneTest(t)
	defer cleanup()

	// Create a test repository
	repo := db.Repository{
		OwnerID:       1,
		FullName:      "active-agent/matrix-repo",
		Name:          "matrix-repo",
		DefaultBranch: "main",
	}
	svc.DB.Create(&repo)

	endpoints := []struct {
		name         string
		method       string
		path         string
		body         string
		expectedCode int
	}{
		{"user endpoint", "GET", "/api/v3/user", "", http.StatusOK},
		{"create repo", "POST", "/api/v3/user/repos", `{"name":"test"}`, http.StatusCreated},
		{"list repos", "GET", "/api/v3/user/repos", "", http.StatusOK},
		{"git info/refs", "GET", "/active-agent/matrix-repo/info/refs?service=git-upload-pack", "", http.StatusOK},
	}

	tokenStates := []struct {
		name         string
		token        string
		shouldAllow  bool
		expectedCode int
	}{
		{"active", "active-token", true, http.StatusOK},
		{"pending", "pending-token", false, http.StatusUnauthorized},
		{"failed", "failed-token", false, http.StatusUnauthorized},
	}

	for _, ep := range endpoints {
		for _, ts := range tokenStates {
			t.Run(fmt.Sprintf("%s_%s", ep.name, ts.name), func(t *testing.T) {
				var req *http.Request
				if ep.body != "" {
					req = httptest.NewRequest(ep.method, ep.path, bytes.NewReader([]byte(ep.body)))
				} else {
					req = httptest.NewRequest(ep.method, ep.path, nil)
				}
				req.Header.Set("Authorization", "token "+ts.token)
				req.Header.Set("Content-Type", "application/json")

				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)

				// For non-active tokens, expect unauthorized
				expectedCode := ep.expectedCode
				if !ts.shouldAllow {
					expectedCode = http.StatusUnauthorized
				}

				if w.Code != expectedCode {
					t.Errorf("expected status %d, got %d: %s", expectedCode, w.Code, w.Body.String())
				}
			})
		}
	}
}
