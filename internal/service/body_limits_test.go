package service

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateBodyFitsMediumText(t *testing.T) {
	t.Parallel()

	if err := validateBodyFitsMediumText(strings.Repeat("a", mediumTextMaxBytes)); err != nil {
		t.Fatalf("expected max-sized body to pass, got %v", err)
	}

	err := validateBodyFitsMediumText(strings.Repeat("a", mediumTextMaxBytes+1))
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "MEDIUMTEXT") {
		t.Fatalf("expected MEDIUMTEXT error message, got %v", err)
	}
}
