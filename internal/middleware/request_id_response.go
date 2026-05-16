package middleware

import (
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

// RequestIDHeaderName returns the response/request header used for request IDs.
func RequestIDHeaderName() string {
	return chimiddleware.RequestIDHeader
}

// RequestIDResponseHeader mirrors the request ID from context into the response
// header so clients can correlate API calls with server logs.
func RequestIDResponseHeader() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if requestID := chimiddleware.GetReqID(r.Context()); requestID != "" {
				w.Header().Set(chimiddleware.RequestIDHeader, requestID)
			}
			next.ServeHTTP(w, r)
		})
	}
}
