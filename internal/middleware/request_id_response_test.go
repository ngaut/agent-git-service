package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

func TestRequestIDResponseHeaderWritesGeneratedRequestID(t *testing.T) {
	handler := chimiddleware.RequestID(RequestIDResponseHeader()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))

	req := httptest.NewRequest(http.MethodGet, "/api/v3/user", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Header().Get(chimiddleware.RequestIDHeader); got == "" {
		t.Fatalf("expected %s response header", chimiddleware.RequestIDHeader)
	}
}

func TestRequestIDResponseHeaderMirrorsIncomingRequestID(t *testing.T) {
	handler := chimiddleware.RequestID(RequestIDResponseHeader()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	req := httptest.NewRequest(http.MethodGet, "/api/v3/user", nil)
	req.Header.Set(chimiddleware.RequestIDHeader, "frontend-request-123")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Header().Get(chimiddleware.RequestIDHeader); got != "frontend-request-123" {
		t.Fatalf("expected incoming request ID to be mirrored, got %q", got)
	}
}
