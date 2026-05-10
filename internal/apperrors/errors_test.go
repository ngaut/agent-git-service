package apperrors

import (
	"errors"
	"fmt"
	"testing"
)

func TestSentinelErrorMessages(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{ErrNotFound, "not found"},
		{ErrUnauthorized, "unauthorized"},
		{ErrForbidden, "forbidden"},
		{ErrConflict, "conflict"},
		{ErrInvalidState, "invalid state"},
		{ErrValidation, "validation failed"},
		{ErrRateLimited, "rate limit exceeded"},
		{ErrBadRequest, "bad request"},
	}
	for _, tt := range tests {
		if got := tt.err.Error(); got != tt.want {
			t.Errorf("expected %q, got %q", tt.want, got)
		}
	}
}

func TestSentinelErrorsAreDistinct(t *testing.T) {
	allErrors := []error{
		ErrNotFound,
		ErrUnauthorized,
		ErrForbidden,
		ErrConflict,
		ErrInvalidState,
		ErrValidation,
		ErrRateLimited,
		ErrBadRequest,
	}

	for i, e1 := range allErrors {
		for j, e2 := range allErrors {
			if i != j && errors.Is(e1, e2) {
				t.Errorf("error %d (%v) should not match error %d (%v)", i, e1, j, e2)
			}
		}
	}
}

func TestWrappedErrorsMatchSentinels(t *testing.T) {
	tests := []struct {
		name     string
		sentinel error
	}{
		{"ErrNotFound", ErrNotFound},
		{"ErrUnauthorized", ErrUnauthorized},
		{"ErrForbidden", ErrForbidden},
		{"ErrConflict", ErrConflict},
		{"ErrInvalidState", ErrInvalidState},
		{"ErrValidation", ErrValidation},
		{"ErrRateLimited", ErrRateLimited},
		{"ErrBadRequest", ErrBadRequest},
	}
	for _, tt := range tests {
		wrapped := fmt.Errorf("context: %w", tt.sentinel)
		if !errors.Is(wrapped, tt.sentinel) {
			t.Errorf("wrapped error should match %s via errors.Is()", tt.name)
		}
		// Wrapped error message should contain the context
		if got := wrapped.Error(); got != "context: "+tt.sentinel.Error() {
			t.Errorf("unexpected wrapped message: %q", got)
		}
	}
}
