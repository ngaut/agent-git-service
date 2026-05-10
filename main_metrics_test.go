package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"gh-server/internal/metrics"
)

func newMetricsRouter() http.Handler {
	r := chi.NewRouter()
	h := metrics.Init()
	r.Get("/metrics", h.ServeHTTP)
	return r
}

func TestMetricsEndpoint_EnabledByDefault(t *testing.T) {
	mux := newMetricsRouter()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestMetricsEndpoint_DoesNotRequireAuth(t *testing.T) {
	mux := newMetricsRouter()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}
