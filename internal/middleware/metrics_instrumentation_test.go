package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/ngaut/agent-git-service/internal/metrics"
)

func TestMetricsInstrumentation_RecordsRequest(t *testing.T) {
	metrics.Init()

	r := chi.NewRouter()
	r.Use(MetricsInstrumentation())
	r.Get("/widgets/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/widgets/123", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}

	m := metrics.DefaultPrometheus()
	if m == nil {
		t.Fatal("expected Prometheus metrics to be initialized")
	}

	got := testutil.ToFloat64(m.HTTPRequestsTotal.WithLabelValues("GET", "/widgets/{id}", "204"))
	if got != 1 {
		t.Fatalf("expected requests_total=1, got %v", got)
	}
}

func TestMetricsInstrumentation_RecordsDerivedOperation(t *testing.T) {
	metrics.Init()

	r := chi.NewRouter()
	r.Use(MetricsInstrumentation())
	r.Get("/repos/{owner}/{repo}/issues", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	m := metrics.DefaultPrometheus()
	if m == nil {
		t.Fatal("expected Prometheus metrics to be initialized")
	}

	before := testutil.ToFloat64(m.OperationsTotal.WithLabelValues("rest", "issue_read", "success"))

	req := httptest.NewRequest(http.MethodGet, "/repos/acme/widgets/issues", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	after := testutil.ToFloat64(m.OperationsTotal.WithLabelValues("rest", "issue_read", "success"))
	if after != before+1 {
		t.Fatalf("expected operation_total to increment by 1, got before=%v after=%v", before, after)
	}
}

func TestMetricsInstrumentation_RecordsExplicitOperation(t *testing.T) {
	metrics.Init()

	r := chi.NewRouter()
	r.Use(MetricsInstrumentation())
	r.With(Operation("git", "git_read")).Post("/custom-upload", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	m := metrics.DefaultPrometheus()
	if m == nil {
		t.Fatal("expected Prometheus metrics to be initialized")
	}

	before := testutil.ToFloat64(m.OperationsTotal.WithLabelValues("git", "git_read", "success"))

	req := httptest.NewRequest(http.MethodPost, "/custom-upload", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}

	after := testutil.ToFloat64(m.OperationsTotal.WithLabelValues("git", "git_read", "success"))
	if after != before+1 {
		t.Fatalf("expected explicit operation_total to increment by 1, got before=%v after=%v", before, after)
	}
}
