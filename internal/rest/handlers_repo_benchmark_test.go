package rest_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"gh-server/internal/testharness"
)

func BenchmarkRepoCreateResponse(b *testing.B) {
	h := testharness.New(b)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		body := fmt.Sprintf(`{"name":"bench-create-%d","auto_init":true}`, i)
		w := h.DoREST(b, http.MethodPost, "/api/v3/user/repos", strings.NewReader(body))
		if w.Code != http.StatusCreated {
			b.Fatalf("POST /api/v3/user/repos: expected 201, got %d: %s", w.Code, w.Body.String())
		}
	}
}

func BenchmarkRepoViewResponse(b *testing.B) {
	h := testharness.New(b)

	w := h.DoRESTJSON(b, http.MethodPost, "/api/v3/user/repos", map[string]any{
		"name":      "bench-view",
		"auto_init": true,
	})
	if w.Code != http.StatusCreated {
		b.Fatalf("POST /api/v3/user/repos: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := h.DoREST(b, http.MethodGet, "/api/v3/repos/testuser/bench-view", nil)
		if w.Code != http.StatusOK {
			b.Fatalf("GET /api/v3/repos/testuser/bench-view: expected 200, got %d: %s", w.Code, w.Body.String())
		}
	}
}
