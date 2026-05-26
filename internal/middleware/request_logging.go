// Package middleware provides shared HTTP middleware.
package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	applog "github.com/ngaut/agent-git-service/internal/logging"
)

const (
	maxCapturedErrorBodyBytes  = 4 << 10
	maxLoggedErrorMessageRunes = 512
	statusClientClosedRequest  = 499
)

// RequestLogging attaches request-scoped structured fields and emits a single
// completion log for every HTTP request, including a small error summary when
// the response is non-successful.
func RequestLogging() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := applog.WithRequestContext(r.Context(),
				slog.String("request_id", chimiddleware.GetReqID(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("host", r.Host),
				slog.String("remote_ip", clientIP(r)),
				slog.String("user_agent", r.UserAgent()),
			)
			r = r.WithContext(ctx)

			start := time.Now()
			ww := &captureResponseWriter{
				WrapResponseWriter: chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor),
			}
			next.ServeHTTP(ww, r)

			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}

			route := "unmatched"
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				if pattern := rctx.RoutePattern(); pattern != "" {
					route = pattern
				}
			}
			applog.AddAttrs(ctx, slog.String("route", route))

			args := []any{
				"status", status,
				"route", route,
				"duration_ms", time.Since(start).Milliseconds(),
				"response_bytes", ww.BytesWritten(),
			}
			if msg := ww.errorSummary(); msg != "" {
				args = append(args, "error_message", msg)
			}

			switch {
			case status >= 500:
				slog.ErrorContext(ctx, "http request completed", args...)
			case status == statusClientClosedRequest:
				slog.InfoContext(ctx, "http request completed", args...)
			case status >= 400:
				slog.WarnContext(ctx, "http request completed", args...)
			default:
				slog.InfoContext(ctx, "http request completed", args...)
			}
		})
	}
}

type captureResponseWriter struct {
	chimiddleware.WrapResponseWriter
	body bytes.Buffer
}

func (w *captureResponseWriter) Write(p []byte) (int, error) {
	if w.body.Len() < maxCapturedErrorBodyBytes {
		remaining := maxCapturedErrorBodyBytes - w.body.Len()
		if remaining > len(p) {
			remaining = len(p)
		}
		_, _ = w.body.Write(p[:remaining])
	}
	return w.WrapResponseWriter.Write(p)
}

func (w *captureResponseWriter) errorSummary() string {
	status := w.Status()
	if status < http.StatusBadRequest {
		return ""
	}

	raw := strings.TrimSpace(w.body.String())
	if raw == "" {
		return ""
	}

	contentType := strings.ToLower(strings.TrimSpace(w.Header().Get("Content-Type")))
	if strings.Contains(contentType, "application/json") {
		var payload map[string]any
		if err := json.Unmarshal(w.body.Bytes(), &payload); err == nil {
			if message := firstString(payload["message"], payload["error"]); message != "" {
				return truncateMessage(message)
			}
		}
	}

	return truncateMessage(raw)
}

func truncateMessage(message string) string {
	runes := []rune(message)
	if len(runes) <= maxLoggedErrorMessageRunes {
		return message
	}
	return string(runes[:maxLoggedErrorMessageRunes]) + "..."
}

func firstString(values ...any) string {
	for _, value := range values {
		if s, ok := value.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func clientIP(r *http.Request) string {
	host := strings.TrimSpace(r.RemoteAddr)
	if host == "" {
		return ""
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		return parsedHost
	}
	return host
}
