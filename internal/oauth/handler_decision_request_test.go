package oauth

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/service"
)

func TestParseDeviceCodeDecisionRequest_InvalidJSONReturnsBadRequest(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v3/oauth/device/approve", strings.NewReader(`{"user_code"`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	_, err := parseDeviceCodeDecisionRequest(rec, req)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !errors.Is(err, service.ErrBadRequest) {
		t.Fatalf("expected ErrBadRequest, got %v", err)
	}
}

func TestParseDeviceCodeDecisionRequest_MissingUserCodeReturnsBadRequest(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v3/oauth/device/reject", strings.NewReader(`{"reason":"user declined"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	_, err := parseDeviceCodeDecisionRequest(rec, req)
	if err == nil {
		t.Fatal("expected error for missing user_code")
	}
	if !errors.Is(err, service.ErrBadRequest) {
		t.Fatalf("expected ErrBadRequest, got %v", err)
	}
}
