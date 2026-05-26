package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"
)

func (s *Server) projectGQL(ctx context.Context, proj db.Project) map[string]any {
	// Load fields from DB
	fields, _ := s.Svc.ListProjectFields(ctx, proj.ID)
	fieldNodes := make([]any, 0, len(fields))
	for _, f := range fields {
		fieldNodes = append(fieldNodes, s.projectFieldGQL(f))
	}

	// Load items from DB
	items, _ := s.Svc.ListProjectItems(ctx, proj.ID)
	itemNodes := make([]any, 0, len(items))
	for _, it := range items {
		itemNodes = append(itemNodes, s.projectItemGQL(ctx, it))
	}

	return map[string]any{
		"__typename":       "ProjectV2",
		"id":               gqlID("Project", proj.ID),
		"title":            proj.Title,
		"number":           proj.Number,
		"shortDescription": proj.ShortDescription,
		"public":           proj.Public,
		"closed":           proj.Closed,
		"readme":           proj.Readme,
		"template":         proj.IsTemplate,
		"url":              fmt.Sprintf("%s/orgs/%s/projects/%d", s.Svc.HTMLBaseURL(), proj.OwnerLogin, proj.Number),
		"owner": map[string]any{
			"__typename": "Organization",
			"login":      proj.OwnerLogin,
		},
		"fields": map[string]any{
			"totalCount": len(fieldNodes),
			"nodes":      fieldNodes,
			// Intentional simplified no-op: Pagination is not fully modeled for projects yet
			"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
		},
		"items": map[string]any{
			"totalCount": len(itemNodes),
			"nodes":      itemNodes,
			// Intentional simplified no-op: Pagination is not fully modeled for projects yet
			"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
		},
	}
}

func (s *Server) projectFieldGQL(f db.ProjectField) map[string]any {
	typeName := "ProjectV2Field"
	if f.DataType == "SINGLE_SELECT" {
		typeName = "ProjectV2SingleSelectField"
	} else if f.DataType == "ITERATION" {
		typeName = "ProjectV2IterationField"
	}
	m := map[string]any{
		"__typename": typeName,
		"id":         gqlID("ProjectField", f.ID),
		"name":       f.Name,
		"dataType":   f.DataType,
	}
	if f.DataType == "SINGLE_SELECT" && f.Options != "" {
		// Options stored as JSON array
		var opts []any
		if err := json.Unmarshal([]byte(f.Options), &opts); err == nil {
			m["options"] = opts
		} else {
			m["options"] = []any{}
		}
	}
	return m
}

func (s *Server) projectItemGQL(ctx context.Context, it db.ProjectItem) map[string]any {
	// Parse field values from JSON
	fieldValues := make(map[string]any)
	if it.FieldValues != "" {
		if err := json.Unmarshal([]byte(it.FieldValues), &fieldValues); err != nil {
			slog.Warn("projectItemGQL: bad field_values JSON", "itemID", it.ID, "error", err)
		}
	}

	// Build field value nodes with proper GraphQL shape
	fieldValueNodes := make([]any, 0, len(fieldValues))
	for fieldIDStr, value := range fieldValues {
		// Parse field ID to uint (fieldIDs in JSON are stored as strings)
		var fieldID uint
		if parsed, err := strconv.ParseUint(fieldIDStr, 10, 64); err == nil {
			fieldID = uint(parsed)
		}
		fieldValueNodes = append(fieldValueNodes, map[string]any{
			"field": map[string]any{
				"id": gqlID("ProjectField", fieldID),
			},
			"value": value,
		})
	}

	m := map[string]any{
		"__typename": "ProjectV2Item",
		"id":         gqlID("ProjectItem", it.ID),
		"type":       it.Type,
		"isArchived": it.Archived,
		"fieldValues": map[string]any{
			"nodes": fieldValueNodes,
		},
	}
	if it.Type == "DRAFT_ISSUE" {
		m["content"] = map[string]any{
			"__typename": "DraftIssue",
			"id":         gqlID("DraftIssue", it.ID),
			"title":      it.DraftTitle,
			"body":       it.DraftBody,
		}
	} else if dbID := parseNodeID(it.ContentID, "Issue"); dbID > 0 {
		if issue, err := s.Svc.GetIssueByID(ctx, dbID); err == nil {
			m["content"] = s.issueContentSummary(issue, it.ContentID)
		}
	} else if dbID := parseNodeID(it.ContentID, "PullRequest"); dbID > 0 {
		if pr, err := s.Svc.GetPRByID(ctx, dbID); err == nil {
			m["content"] = s.prContentSummary(pr, it.ContentID)
		}
	}
	if _, ok := m["content"]; !ok {
		m["content"] = map[string]any{"id": it.ContentID}
	}
	return m
}

func (s *Server) issueContentSummary(issue db.Issue, nodeID string) map[string]any {
	return map[string]any{
		"__typename": "Issue",
		"id":         nodeID,
		"title":      issue.Title,
		"number":     issue.Number,
		"body":       issue.Body,
		"url":        fmt.Sprintf("%s/%s/issues/%d", s.Svc.HTMLBaseURL(), issue.Repository.FullName, issue.Number),
	}
}

func (s *Server) prContentSummary(pr db.PullRequest, nodeID string) map[string]any {
	return map[string]any{
		"__typename": "PullRequest",
		"id":         nodeID,
		"title":      pr.Title,
		"number":     pr.Number,
		"body":       pr.Body,
		"url":        fmt.Sprintf("%s/%s/pull/%d", s.Svc.HTMLBaseURL(), pr.Repository.FullName, pr.Number),
	}
}

func (s *Server) projectItemsGQL(ctx context.Context, contentNodeID string, ownerLogin string) map[string]any {
	// Query ProjectItem records linked to this specific content node ID
	items, _ := s.Svc.FindProjectItemsByContentIDs(ctx, []string{contentNodeID})

	// Pre-load fields for each project to find status field
	// Cache: projectID -> statusFieldID (uint)
	statusFieldCache := make(map[uint]uint)

	nodes := make([]any, 0, len(items))
	for _, item := range items {
		// Find status field for this project (cache lookup)
		var statusFieldID uint
		if fid, ok := statusFieldCache[item.ProjectID]; ok {
			statusFieldID = fid
		} else {
			fields, _ := s.Svc.ListProjectFields(ctx, item.ProjectID)
			for _, f := range fields {
				if f.DataType == "SINGLE_SELECT" && strings.Contains(strings.ToLower(f.Name), "status") {
					statusFieldID = f.ID
					statusFieldCache[item.ProjectID] = statusFieldID
					break
				}
			}
		}

		// Derive status value from field values
		var statusValue map[string]any
		if statusFieldID != 0 && item.FieldValues != "" {
			fieldValues := make(map[string]any)
			if json.Unmarshal([]byte(item.FieldValues), &fieldValues) == nil {
				// FieldValues JSON uses string keys
				statusFieldKey := fmt.Sprintf("%d", statusFieldID)
				if val, ok := fieldValues[statusFieldKey]; ok {
					statusValue = map[string]any{
						"optionId": val,
						"name":     "", // Would need option lookup for human-readable name
					}
				}
			}
		}

		nodes = append(nodes, map[string]any{
			"id":      gqlID("ProjectItem", item.ID),
			"project": s.projectGQL(ctx, item.Project),
			"status":  statusValue,
		})
	}
	return gqlConn(nodes)
}
