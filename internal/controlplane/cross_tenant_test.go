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

var crossTenantSeq uint64

// setupMultiTenantTest creates a test environment with two isolated tenants
// (tenant A and tenant B) with separate databases and credentials.
func setupMultiTenantTest(t *testing.T) (*controlplane.DBRouter, *service.Service, *service.Service, http.Handler, func()) {
	t.Helper()

	// Create control plane DB
	cpDBPath := fmt.Sprintf("file:cp_mem_%d?mode=memory&cache=shared", atomic.AddUint64(&crossTenantSeq, 1))
	cpDB, err := gorm.Open(sqlite.Open(cpDBPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open control plane DB: %v", err)
	}

	// Migrate control plane models
	if err := cpDB.AutoMigrate(&controlplane.CPUser{}, &controlplane.CPToken{}); err != nil {
		t.Fatalf("failed to migrate control plane DB: %v", err)
	}

	// Create tenant A DB
	tenantADBPath := fmt.Sprintf("file:tenantA_mem_%d?mode=memory&cache=shared", atomic.AddUint64(&crossTenantSeq, 1))
	tenantADB, err := gorm.Open(sqlite.Open(tenantADBPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open tenant A DB: %v", err)
	}
	if err := db.Migrate(tenantADB); err != nil {
		t.Fatalf("failed to migrate tenant A DB: %v", err)
	}

	// Create tenant B DB
	tenantBDBPath := fmt.Sprintf("file:tenantB_mem_%d?mode=memory&cache=shared", atomic.AddUint64(&crossTenantSeq, 1))
	tenantBDB, err := gorm.Open(sqlite.Open(tenantBDBPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open tenant B DB: %v", err)
	}
	if err := db.Migrate(tenantBDB); err != nil {
		t.Fatalf("failed to migrate tenant B DB: %v", err)
	}

	// Create gitstore (shared for simplicity, but DB isolation enforces tenant boundary)
	tmpDir, err := os.MkdirTemp("", "multitenant-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	gitStore, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatalf("failed to create gitstore: %v", err)
	}

	// Create services for each tenant
	svcA := &service.Service{
		DB:      tenantADB,
		Git:     gitStore,
		BaseURL: "http://localhost:8080",
	}
	svcB := &service.Service{
		DB:      tenantBDB,
		Git:     gitStore,
		BaseURL: "http://localhost:8080",
	}

	// Create control plane router with tenant DB routing
	tenantDBs := map[string]*gorm.DB{
		"tenant-a": tenantADB,
		"tenant-b": tenantBDB,
	}
	var openDBCalls atomic.Int64
	testOpenDB := func(dsn string) (*gorm.DB, error) {
		openDBCalls.Add(1)
		// Route based on DSN
		if db, ok := tenantDBs[dsn]; ok {
			return db, nil
		}
		return tenantADB, nil // default
	}
	cpRouter := controlplane.NewDBRouter(cpDB, testOpenDB, true, controlplane.RouterConfig{MaxAgents: 10})

	// Seed tenant A agent
	tenantAUser := controlplane.CPUser{Login: "tenant-a", Email: "a@test.com", DSN: "tenant-a", State: controlplane.AgentStateActive}
	cpDB.Create(&tenantAUser)
	cpDB.Create(&controlplane.CPToken{CPUserID: tenantAUser.ID, Value: "tenant-a-token"})

	// Seed tenant B agent
	tenantBUser := controlplane.CPUser{Login: "tenant-b", Email: "b@test.com", DSN: "tenant-b", State: controlplane.AgentStateActive}
	cpDB.Create(&tenantBUser)
	cpDB.Create(&controlplane.CPToken{CPUserID: tenantBUser.ID, Value: "tenant-b-token"})

	// Wire handlers (using svcA as primary, but router handles tenant isolation)
	handlers := &rest.Deps{Svc: svcA, Router: cpRouter}
	gqlSrv := graphql.NewServer(svcA)
	gitHandler := githttp.New(gitStore, svcA)
	oauthHandler := oauth.New(svcA)

	mux := router.RegisterRoutes(chi.NewRouter(), handlers, gitHandler, gqlSrv, oauthHandler, cpRouter, "http://console.localhost")

	cleanup := func() {
		cpRouter.Close()
		os.RemoveAll(tmpDir)
		if sqlDB, err := cpDB.DB(); err == nil {
			sqlDB.Close()
		}
		if sqlDB, err := tenantADB.DB(); err == nil {
			sqlDB.Close()
		}
		if sqlDB, err := tenantBDB.DB(); err == nil {
			sqlDB.Close()
		}
	}

	return cpRouter, svcA, svcB, mux, cleanup
}

// TestCrossTenantGitPush_IsolationDenied tests that tenant B cannot push to
// tenant A's repository.
func TestCrossTenantGitPush_IsolationDenied(t *testing.T) {
	_, svcA, _, mux, cleanup := setupMultiTenantTest(t)
	defer cleanup()

	// Create repo for tenant A
	repoA := db.Repository{
		OwnerID:       1,
		FullName:      "tenant-a/repo-a",
		Name:          "repo-a",
		DefaultBranch: "main",
	}
	svcA.DB.Create(&repoA)

	// Initialize git repo for tenant A
	if err := svcA.Git.Init(context.Background(), "tenant-a/repo-a", "main", false); err != nil {
		t.Fatalf("failed to init repo A: %v", err)
	}

	// Record tenant A refs before (should be empty or just HEAD)
	req := httptest.NewRequest("GET", "/tenant-a/repo-a/info/refs?service=git-upload-pack", nil)
	req.Header.Set("Authorization", "token tenant-a-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Logf("initial refs request returned %d: %s", w.Code, w.Body.String())
	}
	refsBefore := w.Body.String()

	// Attempt git-upload-pack (simulating push preparation) with tenant B credentials
	// This should fail because tenant B doesn't have access to tenant-a's repo
	reqPush := httptest.NewRequest("POST", "/tenant-a/repo-a/git-receive-pack", nil)
	reqPush.Header.Set("Authorization", "token tenant-b-token")
	reqPush.Header.Set("Content-Type", "application/x-git-receive-pack-request")

	wPush := httptest.NewRecorder()
	mux.ServeHTTP(wPush, reqPush)

	// Cross-tenant push should be denied (401, 403, or 404 for tenant isolation)
	if wPush.Code != http.StatusUnauthorized && wPush.Code != http.StatusForbidden && wPush.Code != http.StatusNotFound {
		t.Errorf("expected cross-tenant push to be denied (401/403/404), got %d: %s", wPush.Code, wPush.Body.String())
	}

	// Verify tenant A refs are unchanged
	reqAfter := httptest.NewRequest("GET", "/tenant-a/repo-a/info/refs?service=git-upload-pack", nil)
	reqAfter.Header.Set("Authorization", "token tenant-a-token")
	wAfter := httptest.NewRecorder()
	mux.ServeHTTP(wAfter, reqAfter)

	refsAfter := wAfter.Body.String()
	if refsAfter != refsBefore {
		t.Errorf("tenant A refs changed after cross-tenant push attempt:\nbefore: %s\nafter: %s", refsBefore, refsAfter)
	}
}

// TestCrossTenantGitPush_NoSideEffects verifies that cross-tenant push attempts
// produce no side effects in the target tenant's database.
func TestCrossTenantGitPush_NoSideEffects(t *testing.T) {
	_, svcA, _, mux, cleanup := setupMultiTenantTest(t)
	defer cleanup()

	// Create repo for tenant A
	repoA := db.Repository{
		OwnerID:       1,
		FullName:      "tenant-a/repo-a",
		Name:          "repo-a",
		DefaultBranch: "main",
	}
	svcA.DB.Create(&repoA)

	// Initialize git repo for tenant A
	if err := svcA.Git.Init(context.Background(), "tenant-a/repo-a", "main", false); err != nil {
		t.Fatalf("failed to init repo A: %v", err)
	}

	// Count repos and other entities in tenant A before
	var repoCountBefore, workflowCountBefore int64
	svcA.DB.Model(&db.Repository{}).Count(&repoCountBefore)
	svcA.DB.Model(&db.Workflow{}).Count(&workflowCountBefore)

	// Attempt cross-tenant operations with tenant B credentials
	testOps := []struct {
		path   string
		method string
	}{
		{"/tenant-a/repo-a/git-receive-pack", "POST"},
		{"/tenant-a/repo-a/info/refs?service=git-upload-pack", "GET"},
		{"/api/v3/repos/tenant-a/repo-a", "GET"},
		{"/api/v3/repos/tenant-a/repo-a/issues", "GET"},
	}

	for _, op := range testOps {
		var req *http.Request
		if strings.Contains(op.path, "git-") {
			req = httptest.NewRequest(op.method, op.path, nil)
			req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
		} else {
			req = httptest.NewRequest(op.method, op.path, nil)
		}
		req.Header.Set("Authorization", "token tenant-b-token")

		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		// Should be denied (401, 403, or 404 for tenant isolation)
		if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
			t.Errorf("path %s should be denied (401/403/404), got %d", op.path, w.Code)
		}
	}

	// Count repos and entities in tenant A after - should be unchanged
	var repoCountAfter, workflowCountAfter int64
	svcA.DB.Model(&db.Repository{}).Count(&repoCountAfter)
	svcA.DB.Model(&db.Workflow{}).Count(&workflowCountAfter)

	if repoCountAfter != repoCountBefore {
		t.Errorf("tenant A repo count changed: before=%d, after=%d", repoCountBefore, repoCountAfter)
	}
	if workflowCountAfter != workflowCountBefore {
		t.Errorf("tenant A workflow count changed: before=%d, after=%d", workflowCountBefore, workflowCountAfter)
	}
}

// TestCrossTenantGitPush_SameNameIsolation tests that repos with the same name
// in different tenants are properly isolated.
func TestCrossTenantGitPush_SameNameIsolation(t *testing.T) {
	_, svcA, svcB, mux, cleanup := setupMultiTenantTest(t)
	defer cleanup()

	// Create repos with same name in both tenants
	repoA := db.Repository{
		OwnerID:       1,
		FullName:      "tenant-a/shared-repo",
		Name:          "shared-repo",
		DefaultBranch: "main",
	}
	svcA.DB.Create(&repoA)

	repoB := db.Repository{
		OwnerID:       1,
		FullName:      "tenant-b/shared-repo",
		Name:          "shared-repo",
		DefaultBranch: "main",
	}
	svcB.DB.Create(&repoB)

	// Initialize git repos
	if err := svcA.Git.Init(context.Background(), "tenant-a/shared-repo", "main", false); err != nil {
		t.Fatalf("failed to init repo A: %v", err)
	}
	if err := svcB.Git.Init(context.Background(), "tenant-b/shared-repo", "main", false); err != nil {
		t.Fatalf("failed to init repo B: %v", err)
	}

	// Tenant B should only be able to access tenant-b/shared-repo
	req := httptest.NewRequest("GET", "/tenant-b/shared-repo/info/refs?service=git-upload-pack", nil)
	req.Header.Set("Authorization", "token tenant-b-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("tenant B should access tenant-b/shared-repo, got %d: %s", w.Code, w.Body.String())
	}

	// Tenant B should NOT be able to access tenant-a/shared-repo
	reqCross := httptest.NewRequest("GET", "/tenant-a/shared-repo/info/refs?service=git-upload-pack", nil)
	reqCross.Header.Set("Authorization", "token tenant-b-token")
	wCross := httptest.NewRecorder()
	mux.ServeHTTP(wCross, reqCross)

	if wCross.Code != http.StatusUnauthorized && wCross.Code != http.StatusForbidden && wCross.Code != http.StatusNotFound {
		t.Errorf("tenant B should NOT access tenant-a/shared-repo, got %d: %s", wCross.Code, wCross.Body.String())
	}
}

// TestCrossTenantGitPush_DenialMatrix tests the full cross-tenant denial matrix
// across different operations and tenant combinations.
func TestCrossTenantGitPush_DenialMatrix(t *testing.T) {
	_, svcA, svcB, mux, cleanup := setupMultiTenantTest(t)
	defer cleanup()

	// Create repos for both tenants
	repoA := db.Repository{
		OwnerID:       1,
		FullName:      "tenant-a/matrix-repo",
		Name:          "matrix-repo",
		DefaultBranch: "main",
	}
	svcA.DB.Create(&repoA)

	repoB := db.Repository{
		OwnerID:       1,
		FullName:      "tenant-b/matrix-repo",
		Name:          "matrix-repo",
		DefaultBranch: "main",
	}
	svcB.DB.Create(&repoB)

	// Initialize git repos
	if err := svcA.Git.Init(context.Background(), "tenant-a/matrix-repo", "main", false); err != nil {
		t.Fatalf("failed to init repo A: %v", err)
	}
	if err := svcB.Git.Init(context.Background(), "tenant-b/matrix-repo", "main", false); err != nil {
		t.Fatalf("failed to init repo B: %v", err)
	}

	// Test matrix: (source tenant, target tenant, should allow)
	tests := []struct {
		name         string
		sourceToken  string
		targetOwner  string
		repo         string
		shouldAllow  bool
		expectedCode int
	}{
		// Same tenant - should allow
		{"tenant-a to tenant-a (same)", "tenant-a-token", "tenant-a", "matrix-repo", true, http.StatusOK},
		{"tenant-b to tenant-b (same)", "tenant-b-token", "tenant-b", "matrix-repo", true, http.StatusOK},
		// Cross tenant - should deny (401, 403, or 404 for tenant isolation)
		{"tenant-b to tenant-a (cross)", "tenant-b-token", "tenant-a", "matrix-repo", false, http.StatusNotFound},
		{"tenant-a to tenant-b (cross)", "tenant-a-token", "tenant-b", "matrix-repo", false, http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := fmt.Sprintf("/%s/%s/info/refs?service=git-upload-pack", tt.targetOwner, tt.repo)
			req := httptest.NewRequest("GET", path, nil)
			req.Header.Set("Authorization", "token "+tt.sourceToken)

			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			// For cross-tenant, expect denial (401/403/404)
			if !tt.shouldAllow {
				if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
					t.Errorf("expected denial (401/403/404), got %d: %s", w.Code, w.Body.String())
				}
			} else {
				if w.Code != tt.expectedCode {
					t.Errorf("expected status %d, got %d: %s", tt.expectedCode, w.Code, w.Body.String())
				}
			}
		})
	}
}

// TestCrossTenantGraphQL_IsolationDenied tests that tenant B cannot access
// tenant A's data via GraphQL queries.
// Note: GraphQL returns null for inaccessible resources (GitHub-compatible behavior)
// rather than 401/403/404 to avoid leaking existence information.
func TestCrossTenantGraphQL_IsolationDenied(t *testing.T) {
	_, svcA, svcB, mux, cleanup := setupMultiTenantTest(t)
	defer cleanup()

	// Create repos in each tenant's DB directly (simulating what would happen
	// when each tenant creates their own repos)
	repoA := db.Repository{
		OwnerID:       1,
		FullName:      "tenant-a/repo-a",
		Name:          "repo-a",
		DefaultBranch: "main",
		Private:       false,
	}
	svcA.DB.Create(&repoA)

	repoB := db.Repository{
		OwnerID:       1,
		FullName:      "tenant-b/repo-b",
		Name:          "repo-b",
		DefaultBranch: "main",
		Private:       false,
	}
	svcB.DB.Create(&repoB)

	// Tenant B should NOT be able to access tenant A's repo via GraphQL
	// GitHub returns null for inaccessible repos (not 401/403/404)
	queryB := `{ repository(owner: "tenant-a", name: "repo-a") { id, name } }`
	reqBodyB := map[string]any{"query": queryB}
	bodyB, _ := json.Marshal(reqBodyB)

	reqB := httptest.NewRequest("POST", "/api/graphql", bytes.NewReader(bodyB))
	reqB.Header.Set("Content-Type", "application/json")
	reqB.Header.Set("Authorization", "token tenant-b-token")

	wB := httptest.NewRecorder()
	mux.ServeHTTP(wB, reqB)

	// GraphQL returns 200 with null data for inaccessible resources
	if wB.Code != http.StatusOK {
		t.Errorf("expected 200 OK (GitHub returns null for inaccessible), got %d: %s", wB.Code, wB.Body.String())
	}

	var respB map[string]any
	json.Unmarshal(wB.Body.Bytes(), &respB)
	dataB, ok := respB["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data in response, got %v", respB)
	}

	// Repository should be null (tenant isolation)
	if dataB["repository"] != nil {
		t.Errorf("tenant B should NOT access tenant-a/repo-a via GraphQL, got %v", dataB["repository"])
	}

	// Conversely, tenant A should NOT be able to access tenant B's repo
	queryA := `{ repository(owner: "tenant-b", name: "repo-b") { id, name } }`
	reqBodyA := map[string]any{"query": queryA}
	bodyA, _ := json.Marshal(reqBodyA)

	reqA := httptest.NewRequest("POST", "/api/graphql", bytes.NewReader(bodyA))
	reqA.Header.Set("Content-Type", "application/json")
	reqA.Header.Set("Authorization", "token tenant-a-token")

	wA := httptest.NewRecorder()
	mux.ServeHTTP(wA, reqA)

	var respA map[string]any
	json.Unmarshal(wA.Body.Bytes(), &respA)
	dataA, ok := respA["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data in response, got %v", respA)
	}

	if dataA["repository"] != nil {
		t.Errorf("tenant A should NOT access tenant-b/repo-b via GraphQL, got %v", dataA["repository"])
	}
}

// TestCrossTenantGraphQL_SameNameIsolation tests that repos with the same name
// in different tenants are properly isolated via GraphQL.
func TestCrossTenantGraphQL_SameNameIsolation(t *testing.T) {
	_, svcA, svcB, mux, cleanup := setupMultiTenantTest(t)
	defer cleanup()

	// Create repos with same name in both tenants
	repoA := db.Repository{
		OwnerID:       1,
		FullName:      "tenant-a/shared-repo",
		Name:          "shared-repo",
		DefaultBranch: "main",
		Private:       false,
	}
	svcA.DB.Create(&repoA)

	repoB := db.Repository{
		OwnerID:       1,
		FullName:      "tenant-b/shared-repo",
		Name:          "shared-repo",
		DefaultBranch: "main",
		Private:       false,
	}
	svcB.DB.Create(&repoB)

	// Tenant B should NOT be able to access tenant A's repo (same name, different tenant)
	queryCross := `{ repository(owner: "tenant-a", name: "shared-repo") { id, name } }`
	reqBodyCross := map[string]any{"query": queryCross}
	bodyCross, _ := json.Marshal(reqBodyCross)

	reqCross := httptest.NewRequest("POST", "/api/graphql", bytes.NewReader(bodyCross))
	reqCross.Header.Set("Content-Type", "application/json")
	reqCross.Header.Set("Authorization", "token tenant-b-token")

	wCross := httptest.NewRecorder()
	mux.ServeHTTP(wCross, reqCross)

	// GraphQL returns 200 with null data for inaccessible resources
	if wCross.Code != http.StatusOK {
		t.Errorf("expected 200 OK (GitHub returns null for inaccessible), got %d: %s", wCross.Code, wCross.Body.String())
	}

	var respCross map[string]any
	json.Unmarshal(wCross.Body.Bytes(), &respCross)
	dataCross, ok := respCross["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data in response, got %v", respCross)
	}

	// Repository should be null (tenant isolation)
	if dataCross["repository"] != nil {
		t.Errorf("tenant B should NOT access tenant-a/shared-repo via GraphQL, got %v", dataCross["repository"])
	}

	// Conversely, tenant A should NOT be able to access tenant B's repo
	queryCross2 := `{ repository(owner: "tenant-b", name: "shared-repo") { id, name } }`
	reqBodyCross2 := map[string]any{"query": queryCross2}
	bodyCross2, _ := json.Marshal(reqBodyCross2)

	reqCross2 := httptest.NewRequest("POST", "/api/graphql", bytes.NewReader(bodyCross2))
	reqCross2.Header.Set("Content-Type", "application/json")
	reqCross2.Header.Set("Authorization", "token tenant-a-token")

	wCross2 := httptest.NewRecorder()
	mux.ServeHTTP(wCross2, reqCross2)

	var respCross2 map[string]any
	json.Unmarshal(wCross2.Body.Bytes(), &respCross2)
	dataCross2, ok := respCross2["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data in response, got %v", respCross2)
	}

	if dataCross2["repository"] != nil {
		t.Errorf("tenant A should NOT access tenant-b/shared-repo via GraphQL, got %v", dataCross2["repository"])
	}
}

// TestCrossTenantREST_IsolationDenied tests that tenant B cannot access
// tenant A's data via REST API.
func TestCrossTenantREST_IsolationDenied(t *testing.T) {
	_, svcA, _, mux, cleanup := setupMultiTenantTest(t)
	defer cleanup()

	// Create repo for tenant A
	repoA := db.Repository{
		OwnerID:       1,
		FullName:      "tenant-a/repo-a",
		Name:          "repo-a",
		DefaultBranch: "main",
	}
	svcA.DB.Create(&repoA)

	// Tenant A should be able to access their own repo via REST
	reqA := httptest.NewRequest("GET", "/api/v3/repos/tenant-a/repo-a", nil)
	reqA.Header.Set("Authorization", "token tenant-a-token")

	wA := httptest.NewRecorder()
	mux.ServeHTTP(wA, reqA)

	if wA.Code != http.StatusOK {
		t.Errorf("tenant A should access their own repo via REST, got %d: %s", wA.Code, wA.Body.String())
	}

	// Tenant B should NOT be able to access tenant A's repo via REST
	reqB := httptest.NewRequest("GET", "/api/v3/repos/tenant-a/repo-a", nil)
	reqB.Header.Set("Authorization", "token tenant-b-token")

	wB := httptest.NewRecorder()
	mux.ServeHTTP(wB, reqB)

	// Cross-tenant REST request should be denied (401, 403, or 404)
	if wB.Code != http.StatusUnauthorized && wB.Code != http.StatusForbidden && wB.Code != http.StatusNotFound {
		t.Errorf("expected cross-tenant REST request to be denied (401/403/404), got %d: %s", wB.Code, wB.Body.String())
	}
}
