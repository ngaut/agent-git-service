// Package service holds the business logic for all GitHub entities.
// This file re-exports sentinel errors from apperrors so that existing
// code using service.ErrNotFound etc. keeps compiling without changes.
package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gh-server/internal/apperrors"

	"gorm.io/gorm"
)

// Sentinel errors re-exported from apperrors.
// Handlers map these to HTTP status codes via respond.ServiceError().
var (
	ErrNotFound             = apperrors.ErrNotFound
	ErrUnauthorized         = apperrors.ErrUnauthorized
	ErrForbidden            = apperrors.ErrForbidden
	ErrConflict             = apperrors.ErrConflict
	ErrInvalidState         = apperrors.ErrInvalidState
	ErrValidation           = apperrors.ErrValidation
	ErrRateLimited          = apperrors.ErrRateLimited
	ErrBadRequest           = apperrors.ErrBadRequest
	ErrDeviceCodeExpired    = errors.New("device code expired")
	ErrDeviceCodePending    = errors.New("device code pending approval")
	ErrDeviceCodeRejected   = errors.New("device code rejected")
	ErrPKCEVerifierRequired = errors.New("pkce code_verifier required")
	ErrPKCEVerifierMismatch = errors.New("pkce code_verifier mismatch")
	// ErrDuplicate maps to ErrConflict (409) for duplicate resource errors.
	ErrDuplicate = apperrors.ErrConflict
	// ErrInvalidRequest maps to ErrBadRequest (400) for malformed requests.
	ErrInvalidRequest = apperrors.ErrBadRequest
	// ErrAlreadyCollaborator maps to ErrConflict (409) for duplicate collaborator errors.
	ErrAlreadyCollaborator = apperrors.ErrConflict
)

// defaultListLimit is a safety cap on unbounded list queries.
// Prevents full-table scans in the absence of explicit pagination.
const defaultListLimit = 1000

// wrapErr converts GORM's ErrRecordNotFound to ErrNotFound so that
// respond.ServiceError maps it to HTTP 404 instead of 500.
// All other errors pass through unchanged.
func wrapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

// wrapErrf is like wrapErr but attaches a formatted message to ErrNotFound.
func wrapErrf(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w: "+format, append([]any{ErrNotFound}, args...)...)
	}
	return err
}

// isDuplicateErr reports whether err is a duplicate-key violation.
// Works with both MySQL ("Duplicate entry") and SQLite ("UNIQUE constraint").
func isDuplicateErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Duplicate entry") || strings.Contains(msg, "UNIQUE constraint")
}

// isSQLiteLockErr reports transient SQLite lock/busy errors that are retryable.
func isSQLiteLockErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked") ||
		strings.Contains(msg, "database schema is locked") ||
		strings.Contains(msg, "sqlite_busy")
}

// retryDelay returns a small linear backoff delay for transient DB retries.
func retryDelay(attempt int) time.Duration {
	return time.Duration(attempt+1) * 10 * time.Millisecond
}

// escapeLike escapes SQL LIKE wildcards (% and _) in user-supplied input.
// This prevents users from injecting wildcards that match unintended rows.
// Usage: q.Where("col LIKE ?", "%"+escapeLike(input)+"%")
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}
