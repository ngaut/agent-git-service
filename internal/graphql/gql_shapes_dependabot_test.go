package graphql_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

// TestDependabotAlertGQL_MalformedJSON seeds alerts with malformed JSON fields
// and queries them via GraphQL, verifying the "warn + zero-value fallback"
// contract: no errors, null fields in the response.
func TestDependabotAlertGQL_MalformedJSON(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: u.Login, Name: "malformed-dep"})
	require.NoError(t, err)

	// Seed an alert with malformed JSON in all three fields.
	alert := db.DependabotAlert{
		RepositoryID:              repo.ID,
		Number:                    1,
		State:                     "open",
		DependencyJSON:            `{not valid`,
		SecurityAdvisoryJSON:      `[broken`,
		SecurityVulnerabilityJSON: `"unterminated`,
		CreatedAt:                 time.Now(),
		UpdatedAt:                 time.Now(),
	}
	svc.DB.WithContext(ctx).Create(&alert)

	q := `
	query($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) {
			vulnerabilityAlerts(first: 10) {
				nodes {
					number
					state
					securityAdvisory
					securityVulnerability
					vulnerableManifestFilename
					vulnerableRequirements
				}
			}
		}
	}`

	data := doGql(t, mux, q, map[string]any{"owner": u.Login, "name": "malformed-dep"})
	rep := data["repository"].(map[string]any)
	alerts := rep["vulnerabilityAlerts"].(map[string]any)
	nodes := alerts["nodes"].([]any)

	require.Len(t, nodes, 1, "expected 1 alert")
	n := nodes[0].(map[string]any)

	// With malformed JSON, these fields should be null (zero-value fallback).
	if n["securityAdvisory"] != nil {
		t.Errorf("expected securityAdvisory=nil, got %v", n["securityAdvisory"])
	}
	if n["securityVulnerability"] != nil {
		t.Errorf("expected securityVulnerability=nil, got %v", n["securityVulnerability"])
	}
	// vulnerableManifestFilename derives from dep JSON; should be "" on parse failure.
	if n["vulnerableManifestFilename"] != "" {
		t.Errorf("expected vulnerableManifestFilename='', got %v", n["vulnerableManifestFilename"])
	}
	if n["vulnerableRequirements"] != "" {
		t.Errorf("expected vulnerableRequirements='', got %v", n["vulnerableRequirements"])
	}
	// Non-JSON fields should still work.
	if n["state"] != "OPEN" {
		t.Errorf("expected state=OPEN, got %v", n["state"])
	}
}
