package graphql

import (
	"context"
	"strings"
)

func (s *Server) doCreateProject(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	title := strFrom(inp, "title")
	ownerLogin := s.resolveOwnerLogin(ctx, inp)
	proj, err := s.Svc.CreateProject(ctx, ownerLogin, title)
	if err != nil {
		return errResp(err.Error())
	}
	return wrap("createProjectV2", map[string]any{
		"projectV2": s.projectGQL(ctx, proj),
	})
}

func (s *Server) doDeleteProject(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	projectIDStr := strFrom(inp, "projectId")
	dbID := parseNodeID(projectIDStr, "Project")
	if dbID == 0 {
		return errResp("invalid project ID")
	}
	proj, err := s.Svc.GetProjectByID(ctx, dbID)
	if err != nil {
		return errResp("project not found")
	}
	if err := s.Svc.DeleteProject(ctx, dbID); err != nil {
		return errResp(err.Error())
	}
	return wrap("deleteProjectV2", map[string]any{
		"projectV2": s.projectGQL(ctx, proj),
	})
}

// doProjectItemBatch handles batched addProjectV2ItemById / deleteProjectV2Item mutations.
// The CLI sends a single mutation with aliases like add_000, add_001, delete_002
// and corresponding variables input_000, input_001, input_002.
func (s *Server) doProjectItemBatch(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
	data := make(map[string]any)

	// Check if aliases exist — if so, skip canonical names (they appear alongside aliases in AST).
	hasAddAlias, hasDeleteAlias := false, false
	for k := range ast {
		if strings.HasPrefix(k, "add_") {
			hasAddAlias = true
		}
		if strings.HasPrefix(k, "delete_") {
			hasDeleteAlias = true
		}
	}

	for alias := range ast {
		// Derive the variable name: add_000 → input_000, delete_002 → input_002
		var suffix string
		isAdd := false
		switch {
		case strings.HasPrefix(alias, "add_"):
			suffix = strings.TrimPrefix(alias, "add_")
			isAdd = true
		case strings.HasPrefix(alias, "delete_"):
			suffix = strings.TrimPrefix(alias, "delete_")
		case alias == "addProjectV2ItemById" && !hasAddAlias:
			isAdd = true
		case alias == "deleteProjectV2Item" && !hasDeleteAlias:
			// non-aliased delete
		default:
			continue
		}

		var inp map[string]any
		if suffix == "" {
			inp = inputMap(req)
		} else {
			varKey := "input_" + suffix
			if m, ok := req.Variables[varKey].(map[string]any); ok {
				inp = m
			} else {
				inp = map[string]any{}
			}
		}

		if isAdd {
			contentID := strFrom(inp, "contentId")
			projectID := strFrom(inp, "projectId")
			if contentID == "" || projectID == "" {
				return errResp("contentId and projectId are required")
			}
			projDBID := parseNodeID(projectID, "Project")
			if projDBID == 0 {
				return errResp("invalid project ID")
			}
			itemType := "ISSUE"
			if strings.HasPrefix(contentID, "PullRequest_") {
				itemType = "PULL_REQUEST"
			}
			// Idempotent add: use FindOrCreateProjectItem to prevent duplicates
			item, err := s.Svc.FindOrCreateProjectItem(ctx, projDBID, contentID, itemType)
			if err != nil {
				return errResp(err.Error())
			}
			data[alias] = map[string]any{"item": s.projectItemGQL(ctx, item)}
		} else {
			itemID := strFrom(inp, "itemId")
			if itemID == "" {
				return errResp("itemId is required")
			}
			dbID := parseNodeID(itemID, "ProjectItem")
			if dbID > 0 {
				if err := s.Svc.DeleteProjectItem(ctx, dbID); err != nil {
					return errResp(err.Error())
				}
			}
			data[alias] = map[string]any{"deletedItemId": itemID}
		}
	}

	return map[string]any{"data": data}
}

// doUpdateProject handles updateProjectV2 mutation.
func (s *Server) doUpdateProject(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	projectID := strFrom(inp, "projectId")
	dbID := parseNodeID(projectID, "Project")
	if dbID == 0 {
		return errResp("invalid project ID")
	}

	updates := make(map[string]any)
	if title := strFrom(inp, "title"); title != "" {
		updates["title"] = title
	}
	if desc := strFrom(inp, "shortDescription"); desc != "" {
		updates["short_description"] = desc
	}
	if readme := strFrom(inp, "readme"); readme != "" {
		updates["readme"] = readme
	}
	if pub, ok := inp["public"].(bool); ok {
		updates["public"] = pub
	}
	if closed, ok := inp["closed"].(bool); ok {
		updates["closed"] = closed
	}

	proj, err := s.Svc.UpdateProject(ctx, dbID, updates)
	if err != nil {
		return errResp(err.Error())
	}
	return wrap("updateProjectV2", map[string]any{"projectV2": s.projectGQL(ctx, proj)})
}

// doCloseProject handles closeProjectV2 mutation.
func (s *Server) doCloseProject(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	projectID := strFrom(inp, "projectId")
	dbID := parseNodeID(projectID, "Project")
	if dbID == 0 {
		return errResp("invalid project ID")
	}
	proj, err := s.Svc.UpdateProject(ctx, dbID, map[string]any{"closed": true})
	if err != nil {
		return errResp(err.Error())
	}
	return wrap("closeProjectV2", map[string]any{"projectV2": s.projectGQL(ctx, proj)})
}

// doCopyProject handles copyProjectV2 mutation (creates a new project with same title).
func (s *Server) doCopyProject(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	title := strFrom(inp, "title")
	ownerLogin := s.resolveOwnerLogin(ctx, inp)
	proj, err := s.Svc.CreateProject(ctx, ownerLogin, title)
	if err != nil {
		return errResp(err.Error())
	}
	return wrap("copyProjectV2", map[string]any{
		"projectV2": s.projectGQL(ctx, proj),
	})
}
