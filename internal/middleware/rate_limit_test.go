package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/ratelimit"
)

func TestAPIRateLimitHeaders_EnforcesPerTokenBudget(t *testing.T) {
	t.Parallel()

	handler := apiRateLimitHeaders(ratelimit.NewLimiter(2, time.Hour))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	first := doRateLimitedRequest(t, handler, "/api/v3/user", "token:abc", "198.51.100.10:1234")
	requireRateLimitSnapshot(t, first, 2, 1, 1)

	second := doRateLimitedRequest(t, handler, "/api/v3/user", "token:abc", "198.51.100.10:1234")
	requireRateLimitSnapshot(t, second, 2, 2, 0)

	third := doRateLimitedRequest(t, handler, "/api/v3/user", "token:abc", "198.51.100.10:1234")
	if third.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", third.Code)
	}
	requireRateLimitSnapshot(t, third, 2, 2, 0)
	if retryAfter := third.Header().Get("Retry-After"); retryAfter == "" {
		t.Fatal("expected Retry-After header on rate-limited response")
	}
	if !strings.Contains(third.Body.String(), "API rate limit exceeded") {
		t.Fatalf("expected rate-limit error body, got %q", third.Body.String())
	}
	if resource := third.Header().Get("X-RateLimit-Resource"); resource != "core" {
		t.Fatalf("expected core resource header, got %q", resource)
	}

	fourth := doRateLimitedRequest(t, handler, "/api/v3/user", "token:def", "198.51.100.10:1234")
	requireRateLimitSnapshot(t, fourth, 2, 1, 1)
}

func TestAPIRateLimitHeaders_FallsBackToClientIPAndPreservesRateLimitEndpointBudget(t *testing.T) {
	t.Parallel()

	handler := apiRateLimitHeaders(ratelimit.NewLimiter(1, time.Hour))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	probe := doRateLimitedRequest(t, handler, "/api/v3/rate_limit", "", "203.0.113.5:1234")
	requireRateLimitSnapshot(t, probe, 1, 0, 1)

	first := doRateLimitedRequest(t, handler, "/api/v3/user", "", "203.0.113.5:1234")
	requireRateLimitSnapshot(t, first, 1, 1, 0)

	readOnly := doRateLimitedRequest(t, handler, "/api/v3/rate_limit", "", "203.0.113.5:1234")
	if readOnly.Code != http.StatusNoContent {
		t.Fatalf("expected rate_limit probe to pass after exhaustion, got %d", readOnly.Code)
	}
	requireRateLimitSnapshot(t, readOnly, 1, 1, 0)

	blocked := doRateLimitedRequest(t, handler, "/api/v3/user", "", "203.0.113.5:1234")
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", blocked.Code)
	}
	requireRateLimitSnapshot(t, blocked, 1, 1, 0)

	otherIP := doRateLimitedRequest(t, handler, "/api/v3/user", "", "203.0.113.99:1234")
	requireRateLimitSnapshot(t, otherIP, 1, 1, 0)
}

func TestAPIRateLimitHeaders_UsesGraphQLAndSearchResources(t *testing.T) {
	t.Parallel()

	handler := apiRateLimitHeaders(ratelimit.NewLimiter(3, time.Hour))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	graphql := doRateLimitedRequest(t, handler, "/api/graphql", "token:abc", "198.51.100.10:1234")
	requireRateLimitSnapshot(t, graphql, 3, 1, 2)
	if resource := graphql.Header().Get("X-RateLimit-Resource"); resource != "graphql" {
		t.Fatalf("expected graphql resource header, got %q", resource)
	}

	search := doRateLimitedRequest(t, handler, "/api/v3/search/repositories?q=test", "token:abc", "198.51.100.10:1234")
	requireRateLimitSnapshot(t, search, 3, 1, 2)
	if resource := search.Header().Get("X-RateLimit-Resource"); resource != "search" {
		t.Fatalf("expected search resource header, got %q", resource)
	}

	codeSearch := doRateLimitedRequest(t, handler, "/api/v3/search/code?q=test", "token:abc", "198.51.100.10:1234")
	requireRateLimitSnapshot(t, codeSearch, 3, 1, 2)
	if resource := codeSearch.Header().Get("X-RateLimit-Resource"); resource != "code_search" {
		t.Fatalf("expected code_search resource header, got %q", resource)
	}
}

func doRateLimitedRequest(t *testing.T, handler http.Handler, path string, actor string, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	if actor != "" {
		req = req.WithContext(ratelimit.WithActor(req.Context(), actor))
	}
	req.RemoteAddr = remoteAddr

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func requireRateLimitSnapshot(t *testing.T, w *httptest.ResponseRecorder, wantLimit int, wantUsed int, wantRemaining int) {
	t.Helper()

	if got := w.Header().Get("X-RateLimit-Limit"); got != strconv.Itoa(wantLimit) {
		t.Fatalf("limit: got %q want %d", got, wantLimit)
	}
	if got := w.Header().Get("X-RateLimit-Used"); got != strconv.Itoa(wantUsed) {
		t.Fatalf("used: got %q want %d", got, wantUsed)
	}
	if got := w.Header().Get("X-RateLimit-Remaining"); got != strconv.Itoa(wantRemaining) {
		t.Fatalf("remaining: got %q want %d", got, wantRemaining)
	}
	reset, err := strconv.ParseInt(w.Header().Get("X-RateLimit-Reset"), 10, 64)
	if err != nil {
		t.Fatalf("parse reset header: %v", err)
	}
	if reset <= time.Now().Unix() {
		t.Fatalf("expected reset in the future, got %d", reset)
	}
}
