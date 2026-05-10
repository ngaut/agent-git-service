package testharness_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gh-server/internal/testharness"
)

// TestHarness_RESTQuery verifies that the harness wires GET /api/v3 (API
// discovery) through the full mux. This endpoint uses OptionalTokenAuth
// and returns discovery URLs — it does NOT include "installed_version"
// (that lives on GET /api/v3/meta).
func TestHarness_RESTQuery(t *testing.T) {
	h := testharness.New(t)

	req := httptest.NewRequest("GET", "/api/v3", nil)
	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v3: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET /api/v3: invalid JSON: %v", err)
	}
	if _, ok := body["current_user_url"]; !ok {
		t.Errorf("GET /api/v3: response missing 'current_user_url' key; got keys: %v", keys(body))
	}
}

// TestHarness_RESTMutation exercises a create-then-read round-trip through
// the full router: POST /api/v3/user/repos → GET /api/v3/repos/{owner}/{repo}.
func TestHarness_RESTMutation(t *testing.T) {
	h := testharness.New(t)

	// Create
	w := h.DoREST(t, "POST", "/api/v3/user/repos", strings.NewReader(`{"name":"smoke-repo"}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/v3/user/repos: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Read back
	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/smoke-repo", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v3/repos/testuser/smoke-repo: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var repo map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &repo); err != nil {
		t.Fatalf("GET repo: invalid JSON: %v", err)
	}
	if name, _ := repo["name"].(string); name != "smoke-repo" {
		t.Errorf("expected repo name 'smoke-repo', got %q", name)
	}
}

// TestHarness_AuthRejection confirms the auth middleware rejects an invalid token.
func TestHarness_AuthRejection(t *testing.T) {
	h := testharness.New(t)

	req := httptest.NewRequest("GET", "/api/v3/user", nil)
	req.Header.Set("Authorization", "token invalid-token")
	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/v3/user with bad token: expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHarness_UnauthDiscovery confirms optional auth allows unauthenticated
// access to the API discovery endpoint.
func TestHarness_UnauthDiscovery(t *testing.T) {
	h := testharness.New(t)

	req := httptest.NewRequest("GET", "/api/v3", nil)
	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v3 (no auth): expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHarness_ServerRebindsURLs verifies that Harness.Server() rebinds all
// URL-producing state to the real httptest server URL, not localhost:8080.
func TestHarness_ServerRebindsURLs(t *testing.T) {
	h := testharness.New(t)

	// Before Server(): BaseURL is the default localhost:8080.
	if h.BaseURL != "http://localhost:8080" {
		t.Fatalf("expected default BaseURL http://localhost:8080, got %q", h.BaseURL)
	}

	srv := h.Server(t)

	// After Server(): BaseURL must match the live server.
	if h.BaseURL != srv.URL {
		t.Fatalf("Harness.BaseURL not rebound: got %q, want %q", h.BaseURL, srv.URL)
	}

	// Create a repo and verify the returned URLs use the live server host.
	w := h.DoREST(t, "POST", "/api/v3/user/repos", strings.NewReader(`{"name":"url-test"}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/v3/user/repos: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/url-test", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET repo: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var repo map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &repo); err != nil {
		t.Fatalf("unmarshal repo: %v", err)
	}

	// The API URL must start with the live server URL, not localhost:8080.
	apiURL, _ := repo["url"].(string)
	if !strings.HasPrefix(apiURL, srv.URL) {
		t.Errorf("repo url %q does not start with live server URL %q", apiURL, srv.URL)
	}

	cloneURL, _ := repo["clone_url"].(string)
	if !strings.HasPrefix(cloneURL, srv.URL) {
		t.Errorf("clone_url %q does not start with live server URL %q", cloneURL, srv.URL)
	}

	// Verify discovery endpoint also uses the live URL.
	resp, err := http.Get(srv.URL + "/api/v3")
	if err != nil {
		t.Fatalf("GET /api/v3 via TCP: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v3 via TCP: expected 200, got %d", resp.StatusCode)
	}

	var discovery map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&discovery); err != nil {
		t.Fatalf("decode discovery: %v", err)
	}
	cuURL, _ := discovery["current_user_url"].(string)
	if !strings.HasPrefix(cuURL, srv.URL) {
		t.Errorf("current_user_url %q does not start with live server URL %q", cuURL, srv.URL)
	}
}

// TestHarness_ParallelIsolation verifies that concurrent harness instances
// using Server() produce URLs matching their own server, not another's.
// Without the transform mutex guard this test would fail nondeterministically
// due to cross-harness contamination of the global transform.baseURL.
func TestHarness_ParallelIsolation(t *testing.T) {
	for i := range 3 {
		t.Run(fmt.Sprintf("instance-%d", i), func(t *testing.T) {
			t.Parallel()

			h := testharness.New(t)
			srv := h.Server(t)

			repoName := fmt.Sprintf("par-repo-%d", i)
			w := h.DoREST(t, "POST", "/api/v3/user/repos",
				strings.NewReader(fmt.Sprintf(`{"name":%q}`, repoName)))
			if w.Code != http.StatusCreated {
				t.Fatalf("POST repos: expected 201, got %d: %s", w.Code, w.Body.String())
			}

			w = h.DoREST(t, "GET", "/api/v3/repos/testuser/"+repoName, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("GET repo: expected 200, got %d: %s", w.Code, w.Body.String())
			}

			var repo map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &repo); err != nil {
				t.Fatalf("unmarshal repo: %v", err)
			}

			apiURL, _ := repo["url"].(string)
			if !strings.HasPrefix(apiURL, srv.URL) {
				t.Errorf("repo url %q does not start with own server URL %q", apiURL, srv.URL)
			}
			cloneURL, _ := repo["clone_url"].(string)
			if !strings.HasPrefix(cloneURL, srv.URL) {
				t.Errorf("clone_url %q does not start with own server URL %q", cloneURL, srv.URL)
			}
		})
	}
}

// TestHarness_MultipleNew verifies that calling New() multiple times within
// the same test does not deadlock. This is a regression test for the P1
// finding where the global transform mutex was held for the entire test
// lifetime, causing a second New() to block forever.
func TestHarness_MultipleNew(t *testing.T) {
	h1 := testharness.New(t)
	h2 := testharness.New(t)

	// Both harnesses must be fully functional.
	w1 := h1.DoREST(t, "POST", "/api/v3/user/repos", strings.NewReader(`{"name":"multi-1"}`))
	if w1.Code != http.StatusCreated {
		t.Fatalf("h1: expected 201, got %d: %s", w1.Code, w1.Body.String())
	}
	w2 := h2.DoREST(t, "POST", "/api/v3/user/repos", strings.NewReader(`{"name":"multi-2"}`))
	if w2.Code != http.StatusCreated {
		t.Fatalf("h2: expected 201, got %d: %s", w2.Code, w2.Body.String())
	}
}

// TestHarness_SubtestNew verifies that a subtest can create its own Harness
// while the parent test also has one — a pattern that previously deadlocked
// because the parent's New() held the global mutex until test cleanup.
func TestHarness_SubtestNew(t *testing.T) {
	h := testharness.New(t)
	w := h.DoREST(t, "POST", "/api/v3/user/repos", strings.NewReader(`{"name":"parent-repo"}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("parent: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	t.Run("subtest", func(t *testing.T) {
		sub := testharness.New(t)
		w := sub.DoREST(t, "POST", "/api/v3/user/repos", strings.NewReader(`{"name":"sub-repo"}`))
		if w.Code != http.StatusCreated {
			t.Fatalf("subtest: expected 201, got %d: %s", w.Code, w.Body.String())
		}
	})
}

// TestHarness_ServerIdempotent verifies that calling Server() twice on the
// same harness returns the same server and does not rebind URL state.
// This is a regression test for the P1 finding where repeated Server()
// calls re-created the server and invalidated earlier URL bindings.
func TestHarness_ServerIdempotent(t *testing.T) {
	h := testharness.New(t)

	srv1 := h.Server(t)
	url1 := h.BaseURL

	srv2 := h.Server(t)
	url2 := h.BaseURL

	if srv1 != srv2 {
		t.Fatal("Server() returned different *httptest.Server instances")
	}
	if url1 != url2 {
		t.Fatalf("BaseURL changed between Server() calls: %q → %q", url1, url2)
	}

	// Verify the server is still functional after the second call.
	w := h.DoREST(t, "POST", "/api/v3/user/repos", strings.NewReader(`{"name":"idempotent-repo"}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("POST repos after double Server(): expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var repo map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &repo); err != nil {
		t.Fatalf("unmarshal repo: %v", err)
	}
	apiURL, _ := repo["url"].(string)
	if !strings.HasPrefix(apiURL, srv1.URL) {
		t.Errorf("repo url %q does not start with server URL %q", apiURL, srv1.URL)
	}
}

// TestHarness_SequentialSubtestsSharedServer verifies that sibling subtests
// sharing a single Harness can each call Server(t) without getting a closed
// server. This is a regression test for the P1 finding where the first
// subtest's cleanup closed the shared server, causing later siblings to
// receive a closed (connection refused) *httptest.Server.
func TestHarness_SequentialSubtestsSharedServer(t *testing.T) {
	h := testharness.New(t)

	t.Run("first", func(t *testing.T) {
		srv := h.Server(t)

		resp, err := http.Get(srv.URL + "/api/v3")
		if err != nil {
			t.Fatalf("first subtest: GET /api/v3: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("first subtest: expected 200, got %d", resp.StatusCode)
		}
	})

	// After "first" finishes and its t.Cleanup runs, the server must still
	// be alive because cleanup is registered on the root test, not on the
	// subtest's t.
	t.Run("second", func(t *testing.T) {
		srv := h.Server(t)

		resp, err := http.Get(srv.URL + "/api/v3")
		if err != nil {
			t.Fatalf("second subtest: GET /api/v3 (server closed?): %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("second subtest: expected 200, got %d", resp.StatusCode)
		}
	})
}

// keys returns the top-level keys of a map for diagnostic messages.
func keys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
