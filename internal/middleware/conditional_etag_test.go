package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConditionalETag_PathCoverage(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{name: "notifications", method: http.MethodGet, path: "/api/v3/notifications", want: true},
		{name: "current user", method: http.MethodGet, path: "/api/v3/user", want: true},
		{name: "user repos", method: http.MethodGet, path: "/api/v3/user/repos", want: true},
		{name: "user orgs", method: http.MethodGet, path: "/api/v3/user/orgs", want: true},
		{name: "user agents", method: http.MethodGet, path: "/api/ext/v1/user/agents", want: true},
		{name: "user repo invitations", method: http.MethodGet, path: "/api/v3/user/repository_invitations", want: true},
		{name: "user org invitations", method: http.MethodGet, path: "/api/v3/user/organization_invitations", want: true},
		{name: "user profile", method: http.MethodGet, path: "/api/v3/users/alice", want: true},
		{name: "org root", method: http.MethodGet, path: "/api/v3/orgs/acme", want: true},
		{name: "org teams", method: http.MethodGet, path: "/api/v3/orgs/acme/teams", want: true},
		{name: "team detail", method: http.MethodGet, path: "/api/v3/orgs/acme/teams/platform", want: true},
		{name: "team members", method: http.MethodGet, path: "/api/v3/orgs/acme/teams/platform/members", want: true},
		{name: "team membership", method: http.MethodGet, path: "/api/v3/orgs/acme/teams/platform/memberships/alice", want: true},
		{name: "repo root", method: http.MethodGet, path: "/api/v3/repos/acme/widgets", want: true},
		{name: "repo collaborators", method: http.MethodGet, path: "/api/v3/repos/acme/widgets/collaborators", want: true},
		{name: "repo invitations", method: http.MethodGet, path: "/api/v3/repos/acme/widgets/invitations", want: true},
		{name: "repo issue detail", method: http.MethodGet, path: "/api/v3/repos/acme/widgets/issues/42", want: true},
		{name: "repo issue comments", method: http.MethodGet, path: "/api/v3/repos/acme/widgets/issues/42/comments", want: true},
		{name: "repo workflows", method: http.MethodGet, path: "/api/v3/repos/acme/widgets/actions/workflows", want: true},
		{name: "repo runs", method: http.MethodGet, path: "/api/v3/repos/acme/widgets/actions/runs", want: true},
		{name: "search repositories", method: http.MethodGet, path: "/api/v3/search/repositories", want: true},
		{name: "non target search", method: http.MethodGet, path: "/api/v3/search/issues", want: false},
		{name: "non target workflow detail", method: http.MethodGet, path: "/api/v3/repos/acme/widgets/actions/workflows/7", want: false},
		{name: "non target commit detail", method: http.MethodGet, path: "/api/v3/repos/acme/widgets/commits/abc123", want: false},
		{name: "non api path", method: http.MethodGet, path: "/readyz", want: false},
		{name: "write request", method: http.MethodPost, path: "/api/v3/user", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if got := shouldApplyConditionalETag(req); got != tt.want {
				t.Fatalf("shouldApplyConditionalETag(%s %s) = %v, want %v", tt.method, tt.path, got, tt.want)
			}
		})
	}
}

func TestConditionalETag_TargetedJSONResponseGetsETag(t *testing.T) {
	handler := ConditionalETag()(jsonHandler(http.StatusOK, `{"ok":true}`))

	req := httptest.NewRequest(http.MethodGet, "/api/v3/user", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got := w.Header().Get("ETag"); got == "" {
		t.Fatal("expected ETag header")
	}
	if !varyContains(w.Header(), "Authorization") {
		t.Fatalf("expected Vary to contain Authorization, got %q", strings.Join(w.Header().Values("Vary"), ", "))
	}
}

func TestConditionalETag_ReturnsNotModifiedForMatchingIfNoneMatch(t *testing.T) {
	handler := ConditionalETag()(jsonHandler(http.StatusOK, `{"ok":true}`))

	firstReq := httptest.NewRequest(http.MethodGet, "/api/v3/user", nil)
	firstResp := httptest.NewRecorder()
	handler.ServeHTTP(firstResp, firstReq)

	etag := firstResp.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected initial ETag")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v3/user", nil)
	req.Header.Set("If-None-Match", etag)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotModified {
		t.Fatalf("expected 304, got %d", w.Code)
	}
	if body := w.Body.String(); body != "" {
		t.Fatalf("expected empty body, got %q", body)
	}
	if got := w.Header().Get("ETag"); got != etag {
		t.Fatalf("expected ETag %q, got %q", etag, got)
	}
}

func TestConditionalETag_BypassesNonTargetedPath(t *testing.T) {
	handler := ConditionalETag()(jsonHandler(http.StatusOK, `{"ok":true}`))

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Header().Get("ETag"); got != "" {
		t.Fatalf("expected no ETag, got %q", got)
	}
}

func TestConditionalETag_SkipsNonJSONResponses(t *testing.T) {
	handler := ConditionalETag()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v3/user", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Header().Get("ETag"); got != "" {
		t.Fatalf("expected no ETag, got %q", got)
	}
}

func TestConditionalETag_SkipsNon200Responses(t *testing.T) {
	handler := ConditionalETag()(jsonHandler(http.StatusUnauthorized, `{"message":"nope"}`))

	req := httptest.NewRequest(http.MethodGet, "/api/v3/user", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Header().Get("ETag"); got != "" {
		t.Fatalf("expected no ETag, got %q", got)
	}
}

func TestConditionalETag_HeadRequestsReturnHeadersWithoutBody(t *testing.T) {
	handler := ConditionalETag()(jsonHandler(http.StatusOK, `{"ok":true}`))

	req := httptest.NewRequest(http.MethodHead, "/api/v3/user", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag header")
	}
	if body := w.Body.String(); body != "" {
		t.Fatalf("expected empty body, got %q", body)
	}

	req = httptest.NewRequest(http.MethodHead, "/api/v3/user", nil)
	req.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotModified {
		t.Fatalf("expected 304, got %d", w.Code)
	}
	if body := w.Body.String(); body != "" {
		t.Fatalf("expected empty body, got %q", body)
	}
}

func TestConditionalETag_PreservesOuterVaryValues(t *testing.T) {
	next := jsonHandler(http.StatusOK, `{"ok":true}`)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendVary(w.Header(), "Origin")
		ConditionalETag()(next).ServeHTTP(w, r)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v3/user", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !varyContains(w.Header(), "Origin") {
		t.Fatalf("expected Vary to contain Origin, got %q", strings.Join(w.Header().Values("Vary"), ", "))
	}
	if !varyContains(w.Header(), "Authorization") {
		t.Fatalf("expected Vary to contain Authorization, got %q", strings.Join(w.Header().Values("Vary"), ", "))
	}
}

func jsonHandler(status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

func varyContains(header http.Header, want string) bool {
	for _, value := range header.Values("Vary") {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), want) {
				return true
			}
		}
	}
	return false
}
