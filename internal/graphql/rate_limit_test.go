package graphql_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"gh-server/internal/testharness"
)

func TestGraphQLResponses_IncludeGraphQLRateLimitHeaders(t *testing.T) {
	h := testharness.New(t)

	body, err := json.Marshal(map[string]any{
		"query": `query { viewer { login } }`,
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/graphql", bytes.NewReader(body))
	req.Header.Set("Authorization", "token "+h.Token)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if got := w.Header().Get("X-RateLimit-Resource"); got != "graphql" {
		t.Fatalf("resource: got %q want graphql", got)
	}
	if got := w.Header().Get("X-RateLimit-Limit"); got != "5000" {
		t.Fatalf("limit: got %q want 5000", got)
	}
	used, err := strconv.Atoi(w.Header().Get("X-RateLimit-Used"))
	if err != nil {
		t.Fatalf("parse used: %v", err)
	}
	if used != 1 {
		t.Fatalf("used: got %d want 1", used)
	}
	remaining, err := strconv.Atoi(w.Header().Get("X-RateLimit-Remaining"))
	if err != nil {
		t.Fatalf("parse remaining: %v", err)
	}
	if remaining != 4999 {
		t.Fatalf("remaining: got %d want 4999", remaining)
	}
}
