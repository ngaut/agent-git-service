package transform

import (
	"encoding/json"
	"fmt"

	"gh-server/internal/db"
)

// Environment converts a persisted environment into the GitHub REST shape.
func Environment(env db.Environment, repoFullName string) map[string]any {
	protectionRules := []any{}
	if env.ProtectionRulesJSON != "" {
		_ = json.Unmarshal([]byte(env.ProtectionRulesJSON), &protectionRules)
	}

	deploymentBranchPolicy := map[string]any{
		"protected_branches":     false,
		"custom_branch_policies": false,
	}
	if env.DeploymentPolicyJSON != "" {
		_ = json.Unmarshal([]byte(env.DeploymentPolicyJSON), &deploymentBranchPolicy)
	}

	return map[string]any{
		"id":                       env.ID,
		"node_id":                  NodeID("Environment", env.ID),
		"name":                     env.Name,
		"url":                      fmt.Sprintf("%s/api/v3/repos/%s/environments/%s", base(), repoFullName, env.Name),
		"html_url":                 fmt.Sprintf("%s/%s/deployments/activity_log?environments_filter=%s", htmlBase(), repoFullName, env.Name),
		"created_at":               env.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"updated_at":               env.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"protection_rules":         protectionRules,
		"deployment_branch_policy": deploymentBranchPolicy,
	}
}
