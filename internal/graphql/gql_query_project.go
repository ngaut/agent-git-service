package graphql

import (
	"context"
)

func (s *Server) doUserOrgProject(ctx context.Context, req gqlRequest) map[string]any {
	login := strFrom(req.Variables, "login")

	// GraphQL variables uses float64 for numbers parsing via json
	var number int32
	if n, ok := req.Variables["number"].(float64); ok {
		number = int32(n)
	}

	proj, err := s.Svc.GetProjectByOwnerNumber(ctx, login, number)
	if err != nil {
		return map[string]any{
			"data": map[string]any{"organization": nil, "user": nil},
			"errors": []any{
				map[string]any{"type": "NOT_FOUND", "path": []any{"organization", "projectV2"}, "message": "project not found"},
				map[string]any{"type": "NOT_FOUND", "path": []any{"user", "projectV2"}, "message": "project not found"},
			},
		}
	}

	projData := s.projectGQL(ctx, proj)
	return map[string]any{
		"data": map[string]any{
			"organization": map[string]any{
				"projectV2":  projData,
				"login":      login,
				"__typename": "Organization",
			},
			"user": map[string]any{
				"projectV2":  projData,
				"login":      login,
				"__typename": "User",
			},
		},
	}
}

func (s *Server) doProjectV2List(ctx context.Context, req gqlRequest) map[string]any {
	owner := strFrom(req.Variables, "owner")
	if owner == "" {
		owner = strFrom(req.Variables, "login")
	}
	if owner == "" {
		owner = "testorg"
	}
	queryStr := strFrom(req.Variables, "query")

	projs, err := s.Svc.SearchProjects(ctx, owner, queryStr)
	nodes := []any{}
	if err == nil {
		for _, p := range projs {
			nodes = append(nodes, s.projectGQL(ctx, p))
		}
	}

	projectsData := map[string]any{
		"projectsV2": gqlConn(nodes),
		"login":      owner,
		"__typename": "Organization", // or User, depending on the query
	}

	return map[string]any{
		"data": map[string]any{
			"organization": projectsData,
			"user":         projectsData,
			"viewer":       projectsData,
			"repository":   projectsData,
		},
	}
}
