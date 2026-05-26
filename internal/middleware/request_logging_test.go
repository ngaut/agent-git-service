package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestLogging_ClientClosedStatusLogsAtInfo(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	slog.SetDefault(logger)
	t.Cleanup(func() {
		slog.SetDefault(prev)
	})

	handler := RequestLogging()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(statusClientClosedRequest)
		_, _ = w.Write([]byte(`{"message":"Client Closed Request"}`))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v3/repos/acme/demo/issues", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	logLine := buf.String()
	if !strings.Contains(logLine, "level=INFO") {
		t.Fatalf("expected INFO level log, got %q", logLine)
	}
	if strings.Contains(logLine, "level=WARN") || strings.Contains(logLine, "level=ERROR") {
		t.Fatalf("did not expect warning/error log, got %q", logLine)
	}
	if !strings.Contains(logLine, "status=499") {
		t.Fatalf("expected status=499 in log, got %q", logLine)
	}
}
