package graphql_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ngaut/agent-git-service/internal/testharness"
)

// TestGraphQLAuth_MissingToken_ApiGraphql tests that GraphQL requests to /api/graphql
// with no Authorization header are rejected with 401 Unauthorized.
func TestGraphQLAuth_MissingToken_ApiGraphql(t *testing.T) {
	h := testharness.New(t)

	// Create a GraphQL mutation that requires authentication
	query := `mutation { createRepository(input: { name: "test-repo" }) { repository { id } } }`
	reqBody := map[string]any{"query": query}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/api/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No Authorization header

	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d: %s", w.Code, w.Body.String())
	}

	resp := testharness.DecodeJSON(t, w)
	if resp["message"] != "Requires authentication" {
		t.Errorf("expected message 'Requires authentication', got %v", resp["message"])
	}
}

// TestGraphQLAuth_InvalidToken_ApiGraphql tests that GraphQL requests to /api/graphql
// with an invalid token are rejected with 401 Unauthorized.
func TestGraphQLAuth_InvalidToken_ApiGraphql(t *testing.T) {
	h := testharness.New(t)

	// Create a GraphQL mutation that requires authentication
	query := `mutation { createRepository(input: { name: "test-repo" }) { repository { id } } }`
	reqBody := map[string]any{"query": query}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/api/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token invalid-token-12345")

	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d: %s", w.Code, w.Body.String())
	}

	resp := testharness.DecodeJSON(t, w)
	if resp["message"] != "Bad credentials" {
		t.Errorf("expected message 'Bad credentials', got %v", resp["message"])
	}
}

// TestGraphQLAuth_MissingToken_Graphql tests that GraphQL requests to /graphql
// (host-rewrite path) with no Authorization header are rejected with 401 Unauthorized.
func TestGraphQLAuth_MissingToken_Graphql(t *testing.T) {
	h := testharness.New(t)

	// Create a GraphQL mutation that requires authentication
	query := `mutation { createRepository(input: { name: "test-repo" }) { repository { id } } }`
	reqBody := map[string]any{"query": query}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No Authorization header

	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d: %s", w.Code, w.Body.String())
	}

	resp := testharness.DecodeJSON(t, w)
	if resp["message"] != "Requires authentication" {
		t.Errorf("expected message 'Requires authentication', got %v", resp["message"])
	}
}

// TestGraphQLAuth_InvalidToken_Graphql tests that GraphQL requests to /graphql
// (host-rewrite path) with an invalid token are rejected with 401 Unauthorized.
func TestGraphQLAuth_InvalidToken_Graphql(t *testing.T) {
	h := testharness.New(t)

	// Create a GraphQL mutation that requires authentication
	query := `mutation { createRepository(input: { name: "test-repo" }) { repository { id } } }`
	reqBody := map[string]any{"query": query}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token invalid-token-12345")

	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d: %s", w.Code, w.Body.String())
	}

	resp := testharness.DecodeJSON(t, w)
	if resp["message"] != "Bad credentials" {
		t.Errorf("expected message 'Bad credentials', got %v", resp["message"])
	}
}

// TestGraphQLAuth_NoSideEffects_MissingToken verifies that no database state changes
// occur when a GraphQL mutation is rejected due to missing auth token.
func TestGraphQLAuth_NoSideEffects_MissingToken(t *testing.T) {
	h := testharness.New(t)

	// Count repos before the request
	var countBefore int64
	if err := h.DB.Model(&struct{}{}).Table("repositories").Count(&countBefore).Error; err != nil {
		t.Fatalf("failed to count repos before: %v", err)
	}

	// Create a GraphQL mutation that would create a repo if auth succeeded
	query := `mutation { createRepository(input: { name: "should-not-exist" }) { repository { id } } }`
	reqBody := map[string]any{"query": query}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/api/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No Authorization header

	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d: %s", w.Code, w.Body.String())
	}

	// Count repos after the request
	var countAfter int64
	if err := h.DB.Model(&struct{}{}).Table("repositories").Count(&countAfter).Error; err != nil {
		t.Fatalf("failed to count repos after: %v", err)
	}

	if countAfter != countBefore {
		t.Errorf("repository count changed: before=%d, after=%d (should be unchanged)", countBefore, countAfter)
	}
}

// TestGraphQLAuth_NoSideEffects_InvalidToken verifies that no database state changes
// occur when a GraphQL mutation is rejected due to invalid auth token.
func TestGraphQLAuth_NoSideEffects_InvalidToken(t *testing.T) {
	h := testharness.New(t)

	// Count repos before the request
	var countBefore int64
	if err := h.DB.Model(&struct{}{}).Table("repositories").Count(&countBefore).Error; err != nil {
		t.Fatalf("failed to count repos before: %v", err)
	}

	// Create a GraphQL mutation that would create a repo if auth succeeded
	query := `mutation { createRepository(input: { name: "should-not-exist-2" }) { repository { id } } }`
	reqBody := map[string]any{"query": query}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token invalid-token-xyz")

	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d: %s", w.Code, w.Body.String())
	}

	// Count repos after the request
	var countAfter int64
	if err := h.DB.Model(&struct{}{}).Table("repositories").Count(&countAfter).Error; err != nil {
		t.Fatalf("failed to count repos after: %v", err)
	}

	if countAfter != countBefore {
		t.Errorf("repository count changed: before=%d, after=%d (should be unchanged)", countBefore, countAfter)
	}
}
