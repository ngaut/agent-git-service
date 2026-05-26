package rest

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

// TestDeploymentJSON_MalformedJSON verifies that malformed PayloadJSON produces
// nil fallback without panicking — the "warn + zero-value fallback" contract.
func TestDeploymentJSON_MalformedJSON(t *testing.T) {
	now := time.Now()
	d := &Deps{Svc: &service.Service{}}
	dep := db.Deployment{
		ID:          1,
		Ref:         "main",
		Task:        "deploy",
		Environment: "production",
		PayloadJSON: `{not valid json`,
		Repository:  db.Repository{FullName: "owner/repo"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	r := httptest.NewRequest("GET", "/", nil)
	result := d.deploymentJSON(r, dep)

	// Malformed JSON should produce nil or empty map, not panic.
	assertEmptyOrNilMap(t, result["payload"], "payload")

	// Non-JSON fields should still be populated.
	if result["ref"] != "main" {
		t.Errorf("expected ref=main, got %v", result["ref"])
	}
	if result["task"] != "deploy" {
		t.Errorf("expected task=deploy, got %v", result["task"])
	}
	if result["environment"] != "production" {
		t.Errorf("expected environment=production, got %v", result["environment"])
	}
}

// TestDeploymentJSON_EmptyJSON verifies that empty PayloadJSON produces an
// empty map (the else-branch fallback), not nil.
func TestDeploymentJSON_EmptyJSON(t *testing.T) {
	now := time.Now()
	d := &Deps{Svc: &service.Service{}}
	dep := db.Deployment{
		ID:          2,
		Ref:         "main",
		Task:        "deploy",
		Environment: "staging",
		PayloadJSON: "", // empty triggers the else branch → empty map
		Repository:  db.Repository{FullName: "owner/repo"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	r := httptest.NewRequest("GET", "/", nil)
	result := d.deploymentJSON(r, dep)

	payload, ok := result["payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected payload to be map[string]any, got %T", result["payload"])
	}
	if len(payload) != 0 {
		t.Errorf("expected empty payload map, got %v", payload)
	}
}

// TestDeploymentJSON_ValidJSON verifies that valid PayloadJSON is correctly
// unmarshalled.
func TestDeploymentJSON_ValidJSON(t *testing.T) {
	now := time.Now()
	d := &Deps{Svc: &service.Service{}}
	dep := db.Deployment{
		ID:          3,
		Ref:         "v1.0",
		Task:        "deploy",
		Environment: "production",
		PayloadJSON: `{"version":"1.0","rollback":false}`,
		Repository:  db.Repository{FullName: "owner/repo"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	r := httptest.NewRequest("GET", "/", nil)
	result := d.deploymentJSON(r, dep)

	payload, ok := result["payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected payload to be map[string]any, got %T", result["payload"])
	}
	if payload["version"] != "1.0" {
		t.Errorf("expected version=1.0, got %v", payload["version"])
	}
}
