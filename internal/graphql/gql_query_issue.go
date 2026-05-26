package graphql

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

// Regex patterns for extracting aliased issues queries with filterBy arguments.
var (
	reIssueAlias = regexp.MustCompile(`(\w+)\s*:\s*issues\s*\(\s*filterBy\s*:\s*\{([^}]+)\}`)
	reKeyValue   = regexp.MustCompile(`(\w+)\s*:\s*(\$?\w+)`)
)

// --- Issue queries ---

func (s *Server) doIssueSingle(ctx context.Context, req gqlRequest) map[string]any {
	owner, name, _ := resolveRepo(req.Variables)
	num := intVar(req.Variables, "number")
	if owner == "" || name == "" || num == 0 {
		return wrap("repository", map[string]any{"hasIssuesEnabled": true, "issue": nil})
	}
	issue, err := s.Svc.GetIssue(ctx, owner+"/"+name, num)
	if err != nil {
		return wrap("repository", map[string]any{"hasIssuesEnabled": true, "issue": nil})
	}
	return wrap("repository", map[string]any{
		"hasIssuesEnabled": true,
		"issue":            s.issueGQL(ctx, issue, req.Query),
	})
}

func (s *Server) doIssues(ctx context.Context, req gqlRequest) map[string]any {
	owner, name, _ := resolveRepo(req.Variables)
	fullName := owner + "/" + name

	// Check for aliased issue queries with filterBy arguments (used by gh issue status).
	// These look like: assigned: issues(filterBy: {assignee: $viewer, states: OPEN})
	issueAliases := extractIssueAliases(req.Query)
	if len(issueAliases) > 0 {
		repoData := map[string]any{"hasIssuesEnabled": true}
		viewer := strFrom(req.Variables, "viewer")
		for alias, filter := range issueAliases {
			filterState := db.StateOpen
			if filter.state != "" {
				filterState = strings.ToLower(filter.state)
			}
			var issues []db.Issue
			switch {
			case filter.assignee != "":
				assignee := filter.assignee
				if assignee == "$viewer" || assignee == viewer {
					assignee = viewer
				}
				issues, _ = s.Svc.ListIssuesFiltered(ctx, service.IssueListFilter{
					RepoFullName: fullName,
					State:        filterState,
					Assignee:     assignee,
					Labels:       filter.labels,
				})
			case filter.mentioned != "":
				mentioned := filter.mentioned
				if mentioned == "$viewer" || mentioned == viewer {
					mentioned = viewer
				}
				issues, _ = s.Svc.ListIssuesFiltered(ctx, service.IssueListFilter{
					RepoFullName: fullName,
					State:        filterState,
					Mentioned:    mentioned,
					Labels:       filter.labels,
				})
			case filter.createdBy != "":
				createdBy := filter.createdBy
				if createdBy == "$viewer" || createdBy == viewer {
					createdBy = viewer
				}
				issues, _ = s.Svc.ListIssuesFiltered(ctx, service.IssueListFilter{
					RepoFullName: fullName,
					State:        filterState,
					CreatedBy:    createdBy,
					Labels:       filter.labels,
				})
			default:
				issues, _ = s.Svc.ListIssues(ctx, fullName, filterState, "", "", "", "", "")
			}
			nodes := make([]any, len(issues))
			for i, is := range issues {
				nodes[i] = s.issueGQL(ctx, is, req.Query)
			}
			repoData[alias] = gqlConn(nodes)
		}
		return wrap("repository", repoData)
	}

	// Parse states from GraphQL variables (e.g. ["OPEN"], ["CLOSED"], ["OPEN","CLOSED"])
	state := db.StateOpen
	if raw, ok := req.Variables["states"]; ok {
		if arr, ok := raw.([]any); ok && len(arr) > 0 {
			first := strings.ToLower(fmt.Sprintf("%v", arr[0]))
			if first == db.StateClosed {
				state = db.StateClosed
			}
			if len(arr) > 1 {
				state = "all"
			}
		}
	}

	// Parse label filters from variables
	var labels string
	if raw, ok := req.Variables["labels"]; ok {
		if arr, ok := raw.([]any); ok {
			parts := make([]string, len(arr))
			for i, v := range arr {
				parts[i] = fmt.Sprintf("%v", v)
			}
			labels = strings.Join(parts, ",")
		}
	}

	issues, err := s.Svc.ListIssues(ctx, fullName, state, labels, "", "", "", "")
	logErr(ctx, "doIssueList.ListIssues", err)
	nodes := make([]any, len(issues))
	for i, is := range issues {
		nodes[i] = s.issueGQL(ctx, is, req.Query)
	}
	return wrap("repository", map[string]any{
		"issues":           gqlConn(nodes),
		"hasIssuesEnabled": true,
	})
}

// issueFilterBy holds parsed filterBy arguments for an aliased issues query.
type issueFilterBy struct {
	assignee  string
	mentioned string
	createdBy string
	state     string
	labels    string // comma-separated label names
}

// extractIssueAliases parses the raw GraphQL query to find aliased issues calls
// with filterBy arguments. Returns alias → filterBy map.
// E.g. "assigned: issues(filterBy: {assignee: $viewer, states: OPEN})" → {"assigned": {assignee: "$viewer", state: "OPEN"}}
func extractIssueAliases(query string) map[string]issueFilterBy {
	matches := reIssueAlias.FindAllStringSubmatch(query, -1)
	if len(matches) == 0 {
		return nil
	}
	result := make(map[string]issueFilterBy, len(matches))
	for _, m := range matches {
		alias := m[1]
		filterStr := m[2]
		var fb issueFilterBy
		kvMatches := reKeyValue.FindAllStringSubmatch(filterStr, -1)
		for _, kv := range kvMatches {
			key, val := kv[1], kv[2]
			switch key {
			case "assignee":
				fb.assignee = val
			case "mentioned":
				fb.mentioned = val
			case "createdBy":
				fb.createdBy = val
			case "states":
				fb.state = val
			case "labels":
				fb.labels = val
			}
		}
		result[alias] = fb
	}
	return result
}
