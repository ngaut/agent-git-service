package transform

import (
	"fmt"
	"gh-server/internal/db"
)

// Team converts a db.Team to a GitHub REST API team object.
func Team(t db.Team) map[string]any {
	htmlURL := fmt.Sprintf("%s/orgs/%s/teams/%s", htmlBase(), t.Organization.Login, t.Slug)
	return map[string]any{
		"id":               t.ID,
		"node_id":          NodeID("Team", t.ID),
		"url":              fmt.Sprintf("%s/api/v3/orgs/%s/teams/%s", base(), t.Organization.Login, t.Slug),
		"html_url":         htmlURL,
		"name":             t.Name,
		"slug":             t.Slug,
		"description":      t.Description,
		"privacy":          db.TeamPrivacyClosed,
		"permission":       "pull", // default
		"members_count":    t.MembersCount,
		"repos_count":      t.ReposCount,
		"members_url":      fmt.Sprintf("%s/api/v3/orgs/%s/teams/%s/members{/member}", base(), t.Organization.Login, t.Slug),
		"repositories_url": fmt.Sprintf("%s/api/v3/orgs/%s/teams/%s/repos", base(), t.Organization.Login, t.Slug),
	}
}
