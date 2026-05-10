package db

import (
	"encoding/json"
	"fmt"
)

// Empty column yields (nil, nil); malformed yields a wrapped error whose
// message names the field so callers can log alert context alongside the raw
// json error.

func (a *DependabotAlert) DecodeDependency() (map[string]any, error) {
	return decodeDependabotJSON(a.DependencyJSON, "DependencyJSON")
}

func (a *DependabotAlert) DecodeSecurityAdvisory() (map[string]any, error) {
	return decodeDependabotJSON(a.SecurityAdvisoryJSON, "SecurityAdvisoryJSON")
}

func (a *DependabotAlert) DecodeSecurityVulnerability() (map[string]any, error) {
	return decodeDependabotJSON(a.SecurityVulnerabilityJSON, "SecurityVulnerabilityJSON")
}

func decodeDependabotJSON(raw, field string) (map[string]any, error) {
	if raw == "" {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("decode %s: %w", field, err)
	}
	return out, nil
}
