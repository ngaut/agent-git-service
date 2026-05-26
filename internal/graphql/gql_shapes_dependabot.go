package graphql

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
)

// dependabotAlertGQL converts a db.DependabotAlert to a RepositoryVulnerabilityAlert GraphQL node.
func (s *Server) dependabotAlertGQL(ctx context.Context, a db.DependabotAlert) map[string]any {
	advisory, err := a.DecodeSecurityAdvisory()
	if err != nil {
		slog.Warn("dependabot alert decode", "error", err, "alert_number", a.Number)
	}
	vuln, err := a.DecodeSecurityVulnerability()
	if err != nil {
		slog.Warn("dependabot alert decode", "error", err, "alert_number", a.Number)
	}
	dep, err := a.DecodeDependency()
	if err != nil {
		slog.Warn("dependabot alert decode", "error", err, "alert_number", a.Number)
	}

	state := strings.ToUpper(a.State) // open -> OPEN, fixed -> FIXED, dismissed -> DISMISSED

	dismissedAt := ""
	if a.DismissedAt != nil {
		dismissedAt = a.DismissedAt.Format(time.RFC3339)
	}
	fixedAt := ""
	if a.FixedAt != nil {
		fixedAt = a.FixedAt.Format(time.RFC3339)
	}

	// Extract values from dependency JSON if it's there
	vulnerableManifestFilename := ""
	vulnerableRequirements := ""
	if pkg, _ := dep["package"].(map[string]any); pkg != nil {
		if name, ok := pkg["name"].(string); ok {
			vulnerableManifestFilename = name // approximation based on standard payload
		}
	}
	if reqs, ok := dep["requirements"].(string); ok {
		vulnerableRequirements = reqs
	}

	return map[string]any{
		"id":                         gqlID("RepositoryVulnerabilityAlert", a.ID),
		"number":                     a.Number,
		"state":                      state,
		"dismissedAt":                dismissedAt,
		"dismissedReason":            a.DismissedReason,
		"fixedAt":                    fixedAt,
		"securityAdvisory":           advisory,
		"securityVulnerability":      vuln,
		"vulnerableManifestFilename": vulnerableManifestFilename,
		"vulnerableRequirements":     vulnerableRequirements,
		"repository":                 map[string]any{"nameWithOwner": a.Repository.FullName},
		"createdAt":                  a.CreatedAt.Format(time.RFC3339),
		"__typename":                 "RepositoryVulnerabilityAlert",
	}
}

// repoDependabotAlertsConn wraps all alerts for a repo into a standard GraphQL Connection.
func (s *Server) repoDependabotAlertsConn(ctx context.Context, repoID uint) any {
	alerts, err := s.Svc.ListDependabotAlerts(ctx, repoID)
	if err != nil || len(alerts) == 0 {
		return emptyConn()
	}

	nodes := make([]any, len(alerts))
	for i, a := range alerts {
		nodes[i] = s.dependabotAlertGQL(ctx, a)
	}

	return gqlConn(nodes)
}
