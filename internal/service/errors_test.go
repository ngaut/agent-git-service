package service

import (
	"errors"
	"fmt"
	"testing"

	"gh-server/internal/apperrors"

	"gorm.io/gorm"
)

func TestHTTPMappedSentinelsMatchAppErrors(t *testing.T) {
	tests := []struct {
		name    string
		service error
		app     error
	}{
		{name: "not found", service: ErrNotFound, app: apperrors.ErrNotFound},
		{name: "unauthorized", service: ErrUnauthorized, app: apperrors.ErrUnauthorized},
		{name: "forbidden", service: ErrForbidden, app: apperrors.ErrForbidden},
		{name: "conflict", service: ErrConflict, app: apperrors.ErrConflict},
		{name: "invalid state", service: ErrInvalidState, app: apperrors.ErrInvalidState},
		{name: "validation", service: ErrValidation, app: apperrors.ErrValidation},
		{name: "rate limited", service: ErrRateLimited, app: apperrors.ErrRateLimited},
		{name: "bad request", service: ErrBadRequest, app: apperrors.ErrBadRequest},
		{name: "ErrDuplicate maps to conflict", service: ErrDuplicate, app: apperrors.ErrConflict},
		{name: "ErrInvalidRequest maps to bad request", service: ErrInvalidRequest, app: apperrors.ErrBadRequest},
		{name: "ErrAlreadyCollaborator maps to conflict", service: ErrAlreadyCollaborator, app: apperrors.ErrConflict},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !errors.Is(tt.service, tt.app) {
				t.Fatalf("service sentinel should match apperrors sentinel")
			}
			if !errors.Is(fmt.Errorf("wrapped: %w", tt.service), tt.app) {
				t.Fatalf("wrapped service sentinel should match apperrors sentinel")
			}
		})
	}
}

func TestWrapErrfNotFoundMatchesAppError(t *testing.T) {
	err := wrapErrf(gorm.ErrRecordNotFound, "repository %s", "alice/proj")
	if err == nil {
		t.Fatal("expected wrapped error")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatal("wrapped error should match service ErrNotFound")
	}
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatal("wrapped error should match apperrors ErrNotFound")
	}
}
