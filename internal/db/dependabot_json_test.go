package db

import "testing"

func TestDependabotAlert_Decode(t *testing.T) {
	t.Run("empty_returns_nil", func(t *testing.T) {
		a := DependabotAlert{}
		got, err := a.DecodeDependency()
		if err != nil || got != nil {
			t.Errorf("DecodeDependency on empty: got (%v, %v), want (nil, nil)", got, err)
		}
	})

	t.Run("valid_object", func(t *testing.T) {
		a := DependabotAlert{DependencyJSON: `{"package":{"name":"lodash"}}`}
		got, err := a.DecodeDependency()
		if err != nil {
			t.Fatalf("DecodeDependency: %v", err)
		}
		pkg, _ := got["package"].(map[string]any)
		if pkg["name"] != "lodash" {
			t.Errorf("got package.name=%v, want lodash", pkg["name"])
		}
	})

	t.Run("malformed_returns_error", func(t *testing.T) {
		a := DependabotAlert{SecurityAdvisoryJSON: `{not-valid`}
		got, err := a.DecodeSecurityAdvisory()
		if err == nil {
			t.Fatalf("expected error, got %v", got)
		}
		if got != nil {
			t.Errorf("expected nil map on error, got %v", got)
		}
	})

	t.Run("error_wraps_field_name", func(t *testing.T) {
		a := DependabotAlert{SecurityVulnerabilityJSON: `invalid`}
		_, err := a.DecodeSecurityVulnerability()
		if err == nil {
			t.Fatal("expected error")
		}
		// error message must name the field so alert-context logging has enough info
		if msg := err.Error(); msg == "" || !contains(msg, "SecurityVulnerabilityJSON") {
			t.Errorf("error should mention field name, got: %v", err)
		}
	})
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
