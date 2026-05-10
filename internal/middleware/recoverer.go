// Package middleware provides shared HTTP middleware.
package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recoverer converts panics into structured logs and a generic 500 response.
func Recoverer() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					slog.ErrorContext(r.Context(), "panic recovered",
						"panic", fmt.Sprint(rec),
						"stack", string(debug.Stack()),
					)
					if statusRecorder, ok := w.(interface{ Status() int }); ok && statusRecorder.Status() != 0 {
						return
					}
					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
