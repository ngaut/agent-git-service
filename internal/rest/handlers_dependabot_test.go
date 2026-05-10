package rest

import (
	"testing"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/rest/transform"
)

func init() {
	transform.Init("http://test.local")
}

// assertEmptyOrNilMap checks that v is either nil or an empty map.
// Needed because Go's interface nil semantics: a nil map[string]any stored
// in an any interface is != nil (the interface wraps a non-nil type descriptor).
func assertEmptyOrNilMap(t *testing.T, v any, field string) {
	t.Helper()
	if v == nil {
		return // truly nil interface
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Errorf("expected %s to be nil or empty map, got %T: %v", field, v, v)
		return
	}
	if len(m) != 0 {
		t.Errorf("expected %s to be nil or empty map, got %v", field, m)
	}
}

// TestDependabotAlertJSON_MalformedJSON verifies that malformed JSON fields
// produce zero-value fallback (nil/empty map) without panicking — the
// "warn + zero-value fallback" contract introduced by the error-handling fix.
func TestDependabotAlertJSON_MalformedJSON(t *testing.T) {
	now := time.Now()
	alert := db.DependabotAlert{
		Number:                    1,
		State:                     "open",
		DependencyJSON:            `{not valid json`,
		SecurityAdvisoryJSON:      `[broken`,
		SecurityVulnerabilityJSON: `"unterminated`,
		Repository:                db.Repository{FullName: "owner/repo"},
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}

	result := dependabotAlertJSON(alert)

	// Malformed JSON should produce nil or empty map, not panic or corrupt data.
	assertEmptyOrNilMap(t, result["dependency"], "dependency")
	assertEmptyOrNilMap(t, result["security_advisory"], "security_advisory")
	assertEmptyOrNilMap(t, result["security_vulnerability"], "security_vulnerability")
	// Non-JSON fields should still be populated.
	if result["number"] != 1 {
		t.Errorf("expected number=1, got %v", result["number"])
	}
	if result["state"] != "open" {
		t.Errorf("expected state=open, got %v", result["state"])
	}
}

// TestDependabotAlertJSON_EmptyJSON verifies that empty JSON strings produce
// zero-value fallback for the optional JSON fields.
func TestDependabotAlertJSON_EmptyJSON(t *testing.T) {
	now := time.Now()
	alert := db.DependabotAlert{
		Number:                    2,
		State:                     "fixed",
		DependencyJSON:            "",
		SecurityAdvisoryJSON:      "",
		SecurityVulnerabilityJSON: "",
		Repository:                db.Repository{FullName: "owner/repo"},
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}

	result := dependabotAlertJSON(alert)

	assertEmptyOrNilMap(t, result["dependency"], "dependency")
	assertEmptyOrNilMap(t, result["security_advisory"], "security_advisory")
	assertEmptyOrNilMap(t, result["security_vulnerability"], "security_vulnerability")
}
