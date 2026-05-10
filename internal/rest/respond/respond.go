// Package respond provides helpers for writing GitHub-spec JSON responses.
package respond

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"gh-server/internal/apperrors"
)

// JSON writes v as JSON with the given HTTP status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-GitHub-Media-Type", "github.v3; format=json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("failed to encode JSON response", "error", err)
	}
}

// NotFound writes a 404 GitHub-style error.
func NotFound(w http.ResponseWriter) {
	Error(w, http.StatusNotFound, "Not Found")
}

// Error writes a GitHub-style error JSON body.
func Error(w http.ResponseWriter, status int, message string) {
	JSON(w, status, map[string]any{
		"error":             message,
		"message":           message,
		"documentation_url": "https://docs.github.com/rest",
	})
}

// ServiceError maps service-layer sentinel errors to appropriate HTTP status codes.
// Handlers call this instead of manually inspecting error strings.
//
// Error mapping follows GitHub-compatible semantics:
//   - ErrNotFound      → 404 Not Found
//   - ErrUnauthorized  → 401 Unauthorized
//   - ErrForbidden     → 403 Forbidden
//   - ErrBadRequest    → 400 Bad Request
//   - ErrConflict      → 409 Conflict
//   - ErrInvalidState  → 422 Unprocessable Entity
//   - ErrValidation    → 422 Unprocessable Entity
//   - ErrRateLimited   → 429 Too Many Requests
//   - Unknown errors   → 500 Internal Server Error
func ServiceError(w http.ResponseWriter, err error) {
	ServiceErrorContext(context.Background(), w, err)
}

// ServiceErrorRequest logs and writes a service-layer error using the request
// context so request-scoped attributes are preserved.
func ServiceErrorRequest(r *http.Request, w http.ResponseWriter, err error) {
	if r == nil {
		ServiceErrorContext(context.Background(), w, err)
		return
	}
	ServiceErrorContext(r.Context(), w, err)
}

// ServiceErrorContext logs and writes a service-layer error using the provided
// context for request correlation.
func ServiceErrorContext(ctx context.Context, w http.ResponseWriter, err error) {
	status, publicMessage, errorKind := classifyServiceError(err)
	logServiceError(ctx, status, errorKind, publicMessage, err)

	switch {
	case status == http.StatusNotFound:
		NotFound(w)
	case status == http.StatusUnauthorized:
		Error(w, http.StatusUnauthorized, "Bad credentials")
	case status == http.StatusForbidden:
		Error(w, http.StatusForbidden, "Resource not accessible by integration")
	case status == http.StatusTooManyRequests:
		Error(w, http.StatusTooManyRequests, "You have exceeded a secondary rate limit")
	default:
		Error(w, status, publicMessage)
	}
}

func classifyServiceError(err error) (status int, publicMessage string, errorKind string) {
	switch {
	case errors.Is(err, apperrors.ErrNotFound):
		return http.StatusNotFound, "Not Found", "not_found"
	case errors.Is(err, apperrors.ErrUnauthorized):
		return http.StatusUnauthorized, "Bad credentials", "unauthorized"
	case errors.Is(err, apperrors.ErrForbidden):
		return http.StatusForbidden, "Resource not accessible by integration", "forbidden"
	case errors.Is(err, apperrors.ErrBadRequest):
		return http.StatusBadRequest, err.Error(), "bad_request"
	case errors.Is(err, apperrors.ErrConflict):
		return http.StatusConflict, err.Error(), "conflict"
	case errors.Is(err, apperrors.ErrInvalidState):
		return http.StatusUnprocessableEntity, err.Error(), "invalid_state"
	case errors.Is(err, apperrors.ErrValidation):
		return http.StatusUnprocessableEntity, err.Error(), "validation"
	case errors.Is(err, apperrors.ErrRateLimited):
		return http.StatusTooManyRequests, "You have exceeded a secondary rate limit", "rate_limited"
	default:
		return http.StatusInternalServerError, "Internal Server Error", "internal"
	}
}

func logServiceError(ctx context.Context, status int, errorKind string, publicMessage string, err error) {
	if err == nil {
		return
	}

	args := []any{
		"status", status,
		"error_kind", errorKind,
		"public_message", publicMessage,
		"error", err,
	}

	switch {
	case status >= http.StatusInternalServerError:
		slog.ErrorContext(ctx, "service request failed", args...)
	case status >= http.StatusBadRequest:
		slog.WarnContext(ctx, "service request failed", args...)
	default:
		slog.InfoContext(ctx, "service request completed", args...)
	}
}

// ValidationFailed writes a 422 validation error.
func ValidationFailed(w http.ResponseWriter, msg string) {
	Error(w, http.StatusUnprocessableEntity, msg)
}

// Unauthorized writes a 401 authentication error.
func Unauthorized(w http.ResponseWriter, msg string) {
	Error(w, http.StatusUnauthorized, msg)
}

// Forbidden writes a 403 authorization error.
func Forbidden(w http.ResponseWriter, msg string) {
	Error(w, http.StatusForbidden, msg)
}

// RateLimited writes a 429 rate limit error.
func RateLimited(w http.ResponseWriter) {
	Error(w, http.StatusTooManyRequests, "You have exceeded a secondary rate limit")
}

// NoContent writes 204.
func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}
