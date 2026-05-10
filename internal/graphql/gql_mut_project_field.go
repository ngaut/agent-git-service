package graphql

import (
	"context"
	"encoding/json"
	"log/slog"

	"gh-server/internal/db"
)

// ─── Project V2 Field / Item / Link mutations ────────────────────────────────

// doCreateProjectField handles createProjectV2Field mutation.
func (s *Server) doCreateProjectField(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	projectID := strFrom(inp, "projectId")
	name := strFrom(inp, "name")
	dataType := strFrom(inp, "dataType") // TEXT, SINGLE_SELECT, DATE, NUMBER, ITERATION

	projDBID := parseNodeID(projectID, "Project")
	if projDBID == 0 {
		return errResp("invalid project ID")
	}

	field := db.ProjectField{
		ProjectID: projDBID,
		Name:      name,
		DataType:  dataType,
	}

	// Handle SINGLE_SELECT options
	if dataType == "SINGLE_SELECT" {
		if opts, ok := inp["singleSelectOptions"].([]any); ok {
			optJSON, _ := json.Marshal(opts)
			field.Options = string(optJSON)
		}
	}

	if err := s.Svc.CreateProjectField(ctx, &field); err != nil {
		return errResp(err.Error())
	}

	return wrap("createProjectV2Field", map[string]any{
		"projectV2Field": s.projectFieldGQL(field),
	})
}

// doDeleteProjectField handles deleteProjectV2Field mutation.
func (s *Server) doDeleteProjectField(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	fieldID := strFrom(inp, "fieldId")
	dbID := parseNodeID(fieldID, "ProjectField")
	if dbID == 0 {
		return errResp("invalid field ID")
	}

	field, err := s.Svc.GetProjectField(ctx, dbID)
	if err != nil {
		return errResp("field not found")
	}
	if err := s.Svc.DeleteProjectField(ctx, dbID); err != nil {
		return errResp(err.Error())
	}

	return wrap("deleteProjectV2Field", map[string]any{
		"projectV2Field": s.projectFieldGQL(field),
	})
}

// doUpdateProjectItemFieldValue handles updateProjectV2ItemFieldValue mutation.
func (s *Server) doUpdateProjectItemFieldValue(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	itemID := strFrom(inp, "itemId")
	fieldID := strFrom(inp, "fieldId")
	value, _ := inp["value"].(map[string]any)

	dbItemID := parseNodeID(itemID, "ProjectItem")
	if dbItemID == 0 {
		return errResp("invalid item ID")
	}

	item, err := s.Svc.GetProjectItem(ctx, dbItemID)
	if err != nil {
		return errResp("item not found")
	}

	// Parse existing field values
	fieldValues := make(map[string]any)
	if item.FieldValues != "" {
		if err := json.Unmarshal([]byte(item.FieldValues), &fieldValues); err != nil {
			slog.Warn("doUpdateProjectItemFieldValue: bad field_values JSON", "itemID", dbItemID, "error", err)
		}
	}

	// Determine the value to store
	var val any
	if v, ok := value["text"]; ok {
		val = v
	} else if v, ok := value["number"]; ok {
		val = v
	} else if v, ok := value["date"]; ok {
		val = v
	} else if v, ok := value["singleSelectOptionId"]; ok {
		val = v
	} else if v, ok := value["iterationId"]; ok {
		val = v
	}

	fieldValues[fieldID] = val
	fvJSON, _ := json.Marshal(fieldValues)
	if updated, err := s.Svc.UpdateProjectItem(ctx, dbItemID, map[string]any{"field_values": string(fvJSON)}); err != nil {
		return errResp(err.Error())
	} else {
		item = updated
	}

	return wrap("updateProjectV2ItemFieldValue", map[string]any{
		"projectV2Item": s.projectItemGQL(ctx, item),
	})
}

// doClearProjectItemFieldValue handles clearProjectV2ItemFieldValue mutation.
func (s *Server) doClearProjectItemFieldValue(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	itemID := strFrom(inp, "itemId")
	fieldID := strFrom(inp, "fieldId")

	dbItemID := parseNodeID(itemID, "ProjectItem")
	if dbItemID == 0 {
		return errResp("invalid item ID")
	}

	item, err := s.Svc.GetProjectItem(ctx, dbItemID)
	if err != nil {
		return errResp("item not found")
	}

	// Remove the field value
	fieldValues := make(map[string]any)
	if item.FieldValues != "" {
		if err := json.Unmarshal([]byte(item.FieldValues), &fieldValues); err != nil {
			slog.Warn("doClearProjectItemFieldValue: bad field_values JSON", "itemID", dbItemID, "error", err)
		}
	}
	delete(fieldValues, fieldID)
	fvJSON, _ := json.Marshal(fieldValues)
	if updated, err := s.Svc.UpdateProjectItem(ctx, dbItemID, map[string]any{"field_values": string(fvJSON)}); err != nil {
		return errResp(err.Error())
	} else {
		item = updated
	}

	return wrap("clearProjectV2ItemFieldValue", map[string]any{
		"projectV2Item": s.projectItemGQL(ctx, item),
	})
}

// doUpdateProjectDraftIssue handles updateProjectV2DraftIssue mutation.
func (s *Server) doUpdateProjectDraftIssue(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	draftIssueID := strFrom(inp, "draftIssueId")

	// Draft issue IDs map to ProjectItem IDs (since drafts are stored as items)
	dbID := parseNodeID(draftIssueID, "DraftIssue")
	if dbID == 0 {
		dbID = parseNodeID(draftIssueID, "ProjectItem")
	}
	if dbID == 0 {
		return errResp("invalid draft issue ID")
	}

	updates := make(map[string]any)
	if title := strFrom(inp, "title"); title != "" {
		updates["draft_title"] = title
	}
	if body, ok := inp["body"].(string); ok {
		updates["draft_body"] = body
	}

	item, err := s.Svc.UpdateProjectItem(ctx, dbID, updates)
	if err != nil {
		return errResp(err.Error())
	}

	return wrap("updateProjectV2DraftIssue", map[string]any{
		"draftIssue": map[string]any{
			"id":    gqlID("DraftIssue", item.ID),
			"title": item.DraftTitle,
			"body":  item.DraftBody,
		},
	})
}

// doAddProjectDraftIssue handles addProjectV2DraftIssue mutation.
func (s *Server) doAddProjectDraftIssue(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	projectID := strFrom(inp, "projectId")
	title := strFrom(inp, "title")
	body := strFrom(inp, "body")

	projDBID := parseNodeID(projectID, "Project")
	if projDBID == 0 {
		return errResp("invalid project ID")
	}

	item := db.ProjectItem{
		ProjectID:  projDBID,
		Type:       "DRAFT_ISSUE",
		DraftTitle: title,
		DraftBody:  body,
	}
	if err := s.Svc.CreateProjectItem(ctx, &item); err != nil {
		return errResp(err.Error())
	}

	return wrap("addProjectV2DraftIssue", map[string]any{
		"projectItem": s.projectItemGQL(ctx, item),
	})
}

// doArchiveProjectItem handles archiveProjectV2Item / unarchiveProjectV2Item mutations.
func (s *Server) doArchiveProjectItem(ctx context.Context, req gqlRequest, archive bool) map[string]any {
	inp := inputMap(req)
	itemID := strFrom(inp, "itemId")

	dbID := parseNodeID(itemID, "ProjectItem")
	if dbID == 0 {
		return errResp("invalid item ID")
	}

	item, err := s.Svc.UpdateProjectItem(ctx, dbID, map[string]any{"archived": archive})
	if err != nil {
		return errResp(err.Error())
	}

	mutationName := "archiveProjectV2Item"
	if !archive {
		mutationName = "unarchiveProjectV2Item"
	}
	return wrap(mutationName, map[string]any{
		"item": s.projectItemGQL(ctx, item),
	})
}

// doLinkProjectToRepo handles linkProjectV2ToRepository mutation.
func (s *Server) doLinkProjectToRepo(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	projectID := parseNodeID(strFrom(inp, "projectId"), "Project")
	repoID := parseNodeID(strFrom(inp, "repositoryId"), "Repository")
	if projectID == 0 || repoID == 0 {
		return errResp("invalid project or repository ID")
	}
	if err := s.Svc.LinkProjectToRepo(ctx, projectID, repoID); err != nil {
		return errResp(err.Error())
	}
	return wrap("linkProjectV2ToRepository", map[string]any{})
}

// doUnlinkProjectFromRepo handles unlinkProjectV2FromRepository mutation.
func (s *Server) doUnlinkProjectFromRepo(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	projectID := parseNodeID(strFrom(inp, "projectId"), "Project")
	repoID := parseNodeID(strFrom(inp, "repositoryId"), "Repository")
	if projectID == 0 || repoID == 0 {
		return errResp("invalid project or repository ID")
	}
	if err := s.Svc.UnlinkProjectFromRepo(ctx, projectID, repoID); err != nil {
		return errResp(err.Error())
	}
	return wrap("unlinkProjectV2FromRepository", map[string]any{})
}

// doLinkProjectToTeam handles linkProjectV2ToTeam mutation.
// Teams are not modeled; return an error so callers know the operation is unsupported.
func (s *Server) doLinkProjectToTeam(_ context.Context, _ gqlRequest) map[string]any {
	return errResp("teams are not supported")
}

// doUnlinkProjectFromTeam handles unlinkProjectV2FromTeam mutation.
// Teams are not modeled; return an error so callers know the operation is unsupported.
func (s *Server) doUnlinkProjectFromTeam(_ context.Context, _ gqlRequest) map[string]any {
	return errResp("teams are not supported")
}

// doMarkProjectAsTemplate handles markProjectV2AsTemplate / unmarkProjectV2AsTemplate.
func (s *Server) doMarkProjectAsTemplate(ctx context.Context, req gqlRequest, mark bool) map[string]any {
	inp := inputMap(req)
	projectID := strFrom(inp, "projectId")
	dbID := parseNodeID(projectID, "Project")
	if dbID == 0 {
		return errResp("invalid project ID")
	}

	proj, err := s.Svc.UpdateProject(ctx, dbID, map[string]any{"is_template": mark})
	if err != nil {
		return errResp(err.Error())
	}

	mutationName := "markProjectV2AsTemplate"
	if !mark {
		mutationName = "unmarkProjectV2AsTemplate"
	}
	return wrap(mutationName, map[string]any{
		"projectV2": s.projectGQL(ctx, proj),
	})
}
