package respond_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gh-server/internal/apperrors"
	"gh-server/internal/rest/respond"
	"gh-server/internal/service"
)

func TestJSON(t *testing.T) {
	w := httptest.NewRecorder()
	respond.JSON(w, http.StatusOK, map[string]string{"hello": "world"})

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("unexpected Content-Type: %q", ct)
	}
	if mt := w.Header().Get("X-GitHub-Media-Type"); mt != "github.v3; format=json" {
		t.Errorf("unexpected X-GitHub-Media-Type: %q", mt)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body["hello"] != "world" {
		t.Errorf("expected hello=world, got %v", body["hello"])
	}
}

func TestJSONWithNon200Status(t *testing.T) {
	w := httptest.NewRecorder()
	respond.JSON(w, http.StatusCreated, map[string]string{"id": "42"})

	if w.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", w.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body["id"] != "42" {
		t.Errorf("expected id=42, got %v", body["id"])
	}
}

func TestNotFound(t *testing.T) {
	w := httptest.NewRecorder()
	respond.NotFound(w)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body["message"] != "Not Found" {
		t.Errorf("expected message 'Not Found', got %v", body["message"])
	}
	if body["documentation_url"] != "https://docs.github.com/rest" {
		t.Errorf("unexpected documentation_url: %v", body["documentation_url"])
	}
}

func TestError(t *testing.T) {
	w := httptest.NewRecorder()
	respond.Error(w, http.StatusForbidden, "Forbidden")

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body["message"] != "Forbidden" {
		t.Errorf("expected message 'Forbidden', got %v", body["message"])
	}
}

func TestServiceError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "ErrNotFound",
			err:        apperrors.ErrNotFound,
			wantStatus: http.StatusNotFound,
			wantMsg:    "Not Found",
		},
		{
			name:       "wrapped ErrNotFound",
			err:        fmt.Errorf("repo xyz: %w", apperrors.ErrNotFound),
			wantStatus: http.StatusNotFound,
			wantMsg:    "Not Found",
		},
		{
			name:       "ErrUnauthorized",
			err:        apperrors.ErrUnauthorized,
			wantStatus: http.StatusUnauthorized,
			wantMsg:    "Bad credentials",
		},
		{
			name:       "wrapped ErrUnauthorized",
			err:        fmt.Errorf("token expired: %w", apperrors.ErrUnauthorized),
			wantStatus: http.StatusUnauthorized,
			wantMsg:    "Bad credentials",
		},
		{
			name:       "ErrForbidden",
			err:        apperrors.ErrForbidden,
			wantStatus: http.StatusForbidden,
			wantMsg:    "Resource not accessible by integration",
		},
		{
			name:       "wrapped ErrForbidden",
			err:        fmt.Errorf("no write access: %w", apperrors.ErrForbidden),
			wantStatus: http.StatusForbidden,
			wantMsg:    "Resource not accessible by integration",
		},
		{
			name:       "ErrConflict",
			err:        fmt.Errorf("already exists: %w", apperrors.ErrConflict),
			wantStatus: http.StatusConflict,
			wantMsg:    "already exists: conflict",
		},
		{
			name:       "ErrInvalidState",
			err:        apperrors.ErrInvalidState,
			wantStatus: http.StatusUnprocessableEntity,
			wantMsg:    "invalid state",
		},
		{
			name:       "ErrValidation",
			err:        apperrors.ErrValidation,
			wantStatus: http.StatusUnprocessableEntity,
			wantMsg:    "validation failed",
		},
		{
			name:       "ErrRateLimited",
			err:        apperrors.ErrRateLimited,
			wantStatus: http.StatusTooManyRequests,
			wantMsg:    "You have exceeded a secondary rate limit",
		},
		{
			name:       "ErrBadRequest",
			err:        apperrors.ErrBadRequest,
			wantStatus: http.StatusBadRequest,
			wantMsg:    "bad request",
		},
		{
			name:       "wrapped ErrBadRequest",
			err:        fmt.Errorf("invalid JSON: %w", apperrors.ErrBadRequest),
			wantStatus: http.StatusBadRequest,
			wantMsg:    "invalid JSON: bad request",
		},
		{
			name:       "service ErrNotFound",
			err:        fmt.Errorf("repo xyz: %w", service.ErrNotFound),
			wantStatus: http.StatusNotFound,
			wantMsg:    "Not Found",
		},
		{
			name:       "service ErrUnauthorized",
			err:        fmt.Errorf("bad token: %w", service.ErrUnauthorized),
			wantStatus: http.StatusUnauthorized,
			wantMsg:    "Bad credentials",
		},
		{
			name:       "service ErrForbidden",
			err:        fmt.Errorf("no access: %w", service.ErrForbidden),
			wantStatus: http.StatusForbidden,
			wantMsg:    "Resource not accessible by integration",
		},
		{
			name:       "service ErrConflict",
			err:        fmt.Errorf("already exists: %w", service.ErrConflict),
			wantStatus: http.StatusConflict,
			wantMsg:    "already exists: conflict",
		},
		{
			name:       "service ErrInvalidState",
			err:        fmt.Errorf("cannot merge: %w", service.ErrInvalidState),
			wantStatus: http.StatusUnprocessableEntity,
			wantMsg:    "cannot merge: invalid state",
		},
		{
			name:       "service ErrValidation",
			err:        fmt.Errorf("bad input: %w", service.ErrValidation),
			wantStatus: http.StatusUnprocessableEntity,
			wantMsg:    "bad input: validation failed",
		},
		{
			name:       "service ErrRateLimited",
			err:        fmt.Errorf("too many requests: %w", service.ErrRateLimited),
			wantStatus: http.StatusTooManyRequests,
			wantMsg:    "You have exceeded a secondary rate limit",
		},
		{
			name:       "service ErrBadRequest",
			err:        fmt.Errorf("malformed request: %w", service.ErrBadRequest),
			wantStatus: http.StatusBadRequest,
			wantMsg:    "malformed request: bad request",
		},
		{
			name:       "service ErrDuplicate maps to Conflict",
			err:        fmt.Errorf("already exists: %w", service.ErrDuplicate),
			wantStatus: http.StatusConflict,
			wantMsg:    "already exists: conflict",
		},
		{
			name:       "service ErrInvalidRequest maps to BadRequest",
			err:        fmt.Errorf("invalid: %w", service.ErrInvalidRequest),
			wantStatus: http.StatusBadRequest,
			wantMsg:    "invalid: bad request",
		},
		{
			name:       "service ErrAlreadyCollaborator maps to Conflict",
			err:        fmt.Errorf("duplicate: %w", service.ErrAlreadyCollaborator),
			wantStatus: http.StatusConflict,
			wantMsg:    "duplicate: conflict",
		},
		{
			name:       "unknown error",
			err:        errors.New("something broke"),
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "Internal Server Error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			respond.ServiceError(w, tt.err)

			if w.Code != tt.wantStatus {
				t.Errorf("expected status %d, got %d", tt.wantStatus, w.Code)
			}
			var body map[string]any
			if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
				t.Fatalf("failed to decode body: %v", err)
			}
			if body["message"] != tt.wantMsg {
				t.Errorf("expected message %q, got %v", tt.wantMsg, body["message"])
			}
		})
	}
}

func TestServiceErrorContext_LogsStructuredFailure(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	slog.SetDefault(logger)
	t.Cleanup(func() {
		slog.SetDefault(prev)
	})

	w := httptest.NewRecorder()
	respond.ServiceErrorContext(context.Background(), w, fmt.Errorf("token expired: %w", apperrors.ErrUnauthorized))

	logLine := buf.String()
	if !strings.Contains(logLine, "msg=\"service request failed\"") {
		t.Fatalf("expected service failure log, got %q", logLine)
	}
	if !strings.Contains(logLine, "status=401") {
		t.Fatalf("expected status=401 in log, got %q", logLine)
	}
	if !strings.Contains(logLine, "error_kind=unauthorized") {
		t.Fatalf("expected error_kind=unauthorized in log, got %q", logLine)
	}
	if !strings.Contains(logLine, "token expired: unauthorized") {
		t.Fatalf("expected wrapped error details in log, got %q", logLine)
	}
}

func TestServiceErrorContext_LogsInternalErrorsAtErrorLevel(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	slog.SetDefault(logger)
	t.Cleanup(func() {
		slog.SetDefault(prev)
	})

	w := httptest.NewRecorder()
	respond.ServiceErrorContext(context.Background(), w, errors.New("boom"))

	logLine := buf.String()
	if !strings.Contains(logLine, "level=ERROR") {
		t.Fatalf("expected ERROR level log, got %q", logLine)
	}
	if !strings.Contains(logLine, "status=500") {
		t.Fatalf("expected status=500 in log, got %q", logLine)
	}
	if !strings.Contains(logLine, "error_kind=internal") {
		t.Fatalf("expected error_kind=internal in log, got %q", logLine)
	}
}

func TestValidationFailed(t *testing.T) {
	w := httptest.NewRecorder()
	respond.ValidationFailed(w, "title is required")

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected status 422, got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body["message"] != "title is required" {
		t.Errorf("expected message 'title is required', got %v", body["message"])
	}
}

func TestUnauthorized(t *testing.T) {
	w := httptest.NewRecorder()
	respond.Unauthorized(w, "Authentication required")

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body["message"] != "Authentication required" {
		t.Errorf("expected message 'Authentication required', got %v", body["message"])
	}
}

func TestForbidden(t *testing.T) {
	w := httptest.NewRecorder()
	respond.Forbidden(w, "Admin access required")

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body["message"] != "Admin access required" {
		t.Errorf("expected message 'Admin access required', got %v", body["message"])
	}
}

func TestRateLimited(t *testing.T) {
	w := httptest.NewRecorder()
	respond.RateLimited(w)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected status 429, got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body["message"] != "You have exceeded a secondary rate limit" {
		t.Errorf("expected message 'You have exceeded a secondary rate limit', got %v", body["message"])
	}
}

func TestNoContent(t *testing.T) {
	w := httptest.NewRecorder()
	respond.NoContent(w)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected status 204, got %d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("expected empty body, got %d bytes", w.Body.Len())
	}
}
