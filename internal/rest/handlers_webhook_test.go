package rest

import (
	"reflect"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
)

// assertNilOrEmpty checks that v is either a nil interface or an empty
// collection (map, slice). Handles Go's typed-nil-in-interface gotcha.
func assertNilOrEmpty(t *testing.T, v any, field string) {
	t.Helper()
	if v == nil {
		return
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map, reflect.Slice:
		if rv.IsNil() || rv.Len() == 0 {
			return
		}
	}
	t.Errorf("expected %s to be nil or empty, got %v", field, v)
}

// TestWebhookJSON_MalformedJSON verifies that malformed JSON fields produce
// nil fallback without panicking — the "warn + zero-value fallback" contract.
func TestWebhookJSON_MalformedJSON(t *testing.T) {
	now := time.Now()
	hook := db.Webhook{
		ID:         1,
		Name:       "web",
		Active:     true,
		EventsJSON: `[not valid`,
		ConfigJSON: `{broken json`,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	result := webhookJSON(hook)

	// Malformed JSON should produce nil or empty, not panic.
	assertNilOrEmpty(t, result["events"], "events")
	assertNilOrEmpty(t, result["config"], "config")

	// Non-JSON fields should still be populated.
	if result["id"] != uint(1) {
		t.Errorf("expected id=1, got %v", result["id"])
	}
	if result["name"] != "web" {
		t.Errorf("expected name=web, got %v", result["name"])
	}
	if result["active"] != true {
		t.Errorf("expected active=true, got %v", result["active"])
	}
}

// TestWebhookJSON_EmptyJSON verifies that empty JSON strings produce zero-value
// fallback for the optional events and config fields.
func TestWebhookJSON_EmptyJSON(t *testing.T) {
	now := time.Now()
	hook := db.Webhook{
		ID:        2,
		Name:      "web",
		Active:    true,
		CreatedAt: now,
		UpdatedAt: now,
		// EventsJSON and ConfigJSON left as empty strings.
	}

	result := webhookJSON(hook)

	assertNilOrEmpty(t, result["events"], "events")
	assertNilOrEmpty(t, result["config"], "config")
}

// TestWebhookJSON_ValidJSON verifies that valid JSON is correctly unmarshalled.
func TestWebhookJSON_ValidJSON(t *testing.T) {
	now := time.Now()
	hook := db.Webhook{
		ID:         3,
		Name:       "web",
		Active:     true,
		EventsJSON: `["push","pull_request"]`,
		ConfigJSON: `{"url":"https://example.com","content_type":"json"}`,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	result := webhookJSON(hook)

	events, ok := result["events"].([]string)
	if !ok || len(events) != 2 {
		t.Errorf("expected 2 events, got %v", result["events"])
	}
	config, ok := result["config"].(map[string]string)
	if !ok || config["url"] != "https://example.com" {
		t.Errorf("expected config with url, got %v", result["config"])
	}
}
