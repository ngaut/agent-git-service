// Package apperrors defines application-wide sentinel errors.
// These live in a shared package so that both the service layer (which wraps them)
// and the presentation layer (which maps them to HTTP status codes) can reference
// them without creating a dependency from presentation → service.
package apperrors

import "errors"

// Sentinel errors used throughout the application.
// Service methods wrap these; handlers map them to HTTP status codes via respond.ServiceError().
var (
	// ErrNotFound indicates the requested resource does not exist (HTTP 404).
	ErrNotFound = errors.New("not found")

	// ErrUnauthorized indicates authentication is required or credentials are invalid (HTTP 401).
	ErrUnauthorized = errors.New("unauthorized")

	// ErrForbidden indicates the authenticated user lacks permission for this action (HTTP 403).
	ErrForbidden = errors.New("forbidden")

	// ErrConflict indicates a duplicate or conflicting resource (HTTP 409).
	ErrConflict = errors.New("conflict")

	// ErrInvalidState indicates an operation is invalid for the current resource state (HTTP 422).
	ErrInvalidState = errors.New("invalid state")

	// ErrValidation indicates an input validation error (HTTP 422).
	ErrValidation = errors.New("validation failed")

	// ErrRateLimited indicates the request exceeded rate limits (HTTP 429).
	ErrRateLimited = errors.New("rate limit exceeded")

	// ErrBadRequest indicates a malformed or invalid request (HTTP 400).
	ErrBadRequest = errors.New("bad request")
)
