package graphql

import (
	"fmt"
	"github.com/ngaut/agent-git-service/internal/db"
)

// teamGQL converts a db.Team to a GraphQL Team node.
func (s *Server) teamGQL(t db.Team) map[string]any {
	htmlURL := fmt.Sprintf("%s/orgs/%s/teams/%s", s.Svc.HTMLBaseURL(), t.Organization.Login, t.Slug)

	// Assuming members are eagerly loaded, or we fetch them if needed
	// Currently, Team nodes in our system aren't fully resolved for members connection
	// deeply inside simple queries, so we leave it emptyConn by default or populate if we have members.
	var memberNodes []any
	if len(t.Members) > 0 {
		memberNodes = make([]any, len(t.Members))
		for i, m := range t.Members {
			// userGQL requires context, we'll just map a basic user if we don't have ctx
			memberNodes[i] = map[string]any{
				"id":    gqlID("User", m.ID),
				"login": m.Login,
				"name":  m.Name,
				"url":   fmt.Sprintf("%s/%s", s.Svc.HTMLBaseURL(), m.Login),
			}
		}
	}

	return map[string]any{
		"id":          gqlID("Team", t.ID),
		"name":        t.Name,
		"slug":        t.Slug,
		"description": t.Description,
		"url":         htmlURL,
		"members":     gqlConn(memberNodes), // Use actual members if preloaded, else empty
		"organization": map[string]any{
			"login": t.Organization.Login,
		},
	}
}
