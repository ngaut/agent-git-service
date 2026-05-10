package graphql

import (
	"context"
	"gh-server/internal/db"
)

func (s *Server) doViewer(ctx context.Context) map[string]any {
	u, err := s.Svc.GetCurrentUser(ctx)
	if err != nil {
		return errResp(err.Error())
	}
	return wrap("viewer", s.userGQL(ctx, u))
}

func (s *Server) doViewerWithOrgs(ctx context.Context) map[string]any {
	u, err := s.Svc.GetCurrentUser(ctx)
	if err != nil {
		return errResp(err.Error())
	}
	orgs, err := s.Svc.ListOrgs(ctx)
	logErr(ctx, "doViewerWithOrgs.ListOrgs", err)
	nodes := make([]any, len(orgs))
	for i, o := range orgs {
		nodes[i] = map[string]any{"login": o.Login, "name": o.Name}
	}
	viewer := s.userGQL(ctx, u)
	viewer["organizations"] = gqlConn(nodes)
	return wrap("viewer", viewer)
}

func (s *Server) doUserWithOrgs(ctx context.Context, req gqlRequest) map[string]any {
	login := strFrom(req.Variables, "user")
	if login == "" {
		login = strFrom(req.Variables, "login")
	}
	u, err := s.Svc.GetUser(ctx, login)
	if err != nil {
		return wrap("user", nil)
	}
	orgs, err := s.Svc.ListOrgsForUser(ctx, u.ID)
	logErr(ctx, "doUserWithOrgs.ListOrgs", err)
	nodes := make([]any, len(orgs))
	for i, o := range orgs {
		nodes[i] = map[string]any{"login": o.Login, "name": o.Name}
	}
	return wrap("user", map[string]any{
		"login":         u.Login,
		"organizations": gqlConn(nodes),
	})
}

func (s *Server) doUserOrgOwner(ctx context.Context, req gqlRequest) map[string]any {
	login := strFrom(req.Variables, "login")
	u, err := s.Svc.GetUser(ctx, login)
	if err != nil {
		return map[string]any{
			"data": map[string]any{"user": nil, "organization": nil},
			"errors": []any{
				map[string]any{"type": "NOT_FOUND", "path": []any{"user"}, "message": "not found"},
				map[string]any{"type": "NOT_FOUND", "path": []any{"organization"}, "message": "not found"},
			},
		}
	}

	node := map[string]any{
		"id":         gqlID(u.Type, u.ID),
		"login":      u.Login,
		"name":       u.Name,
		"__typename": u.Type,
	}

	if u.Type == db.TypeOrganization {
		return map[string]any{
			"data": map[string]any{"user": nil, "organization": node},
			"errors": []any{
				map[string]any{"type": "NOT_FOUND", "path": []any{"user"}, "message": "user not found"},
			},
		}
	}
	return map[string]any{
		"data": map[string]any{"user": node, "organization": nil},
		"errors": []any{
			map[string]any{"type": "NOT_FOUND", "path": []any{"organization"}, "message": "organization not found"},
		},
	}
}

func (s *Server) doRepositoryOwner(ctx context.Context, req gqlRequest) map[string]any {
	login := strFrom(req.Variables, "owner")
	if login == "" {
		// viewer case
		u, err := s.Svc.GetCurrentUser(ctx)
		if err != nil {
			return map[string]any{"data": map[string]any{"repositoryOwner": nil}}
		}
		login = u.Login
	}
	u, err := s.Svc.GetUser(ctx, login)
	if err != nil {
		return map[string]any{"data": map[string]any{"repositoryOwner": nil}}
	}
	repos, err := s.Svc.ListReposByOwnerID(ctx, u.ID)
	logErr(ctx, "doRepositoryOwner.ListReposByOwnerID", err)
	userNodes := s.allUserNodes(ctx)
	nodes := make([]any, len(repos))
	for i, r := range repos {
		nodes[i] = s.repoGQL(ctx, r, userNodes)
	}
	return map[string]any{"data": map[string]any{
		"repositoryOwner": map[string]any{
			"login":        u.Login,
			"repositories": gqlConn(nodes),
		},
	}}
}

// ─── __type introspection ────────────────────────────────────────────────────

// DoTypeIntrospection handles all __type introspection queries used by gh CLI
// for feature detection (IssueFeatures, PullRequestFeatures, etc.).
// types is a map of alias → type name extracted during parseQuery.
func DoTypeIntrospection(types map[string]string) map[string]any {
	data := make(map[string]any, len(types))
	for alias, typeName := range types {
		data[alias] = TypeFields(typeName)
	}
	return map[string]any{"data": data}
}

// TypeFields returns the introspection response for a given GraphQL type name.
// These match what GitHub's API returns so the CLI's feature detection works.
func TypeFields(typeName string) map[string]any {
	switch typeName {
	case "Issue":
		return FieldResp("id", "title", "body", "state", "stateReason", "number",
			"author", "labels", "assignees", "comments", "createdAt", "updatedAt",
			"closedAt", "url", "milestone", "projectCards", "projectItems",
			"reactionGroups", "isPinned", "locked", "repository")
	case "PullRequest":
		return FieldResp("id", "title", "body", "state", "number",
			"author", "labels", "assignees", "comments", "createdAt", "updatedAt",
			"closedAt", "mergedAt", "url", "headRefName", "baseRefName",
			"isDraft", "merged", "mergeable", "isInMergeQueue",
			"reviewRequests", "reviews", "commits", "files",
			"headRefOid", "headRepository", "headRepositoryOwner",
			"isCrossRepository", "statusCheckRollup", "repository")
	case "StatusCheckRollupContextConnection":
		return FieldResp("checkRunCount", "checkRunCountsByState",
			"statusContextCount", "statusContextCountsByState")
	case "WorkflowRun":
		return FieldResp("id", "name", "status", "conclusion", "event",
			"createdAt", "updatedAt", "url", "workflow")
	case "Repository":
		return FieldResp("id", "name", "nameWithOwner", "description",
			"isPrivate", "isFork", "isArchived", "isEmpty",
			"defaultBranchRef", "owner", "url",
			"hasIssuesEnabled", "hasWikiEnabled", "hasProjectsEnabled",
			"pullRequestTemplates", "visibility", "autoMergeAllowed",
			"mergeCommitAllowed", "rebaseMergeAllowed", "squashMergeAllowed",
			"deleteBranchOnMerge", "viewerPermission", "vulnerabilityAlerts")
	case "RepositoryVulnerabilityAlert":
		return FieldResp("id", "number", "state", "createdAt", "dismissedAt",
			"dismissedReason", "fixedAt", "securityAdvisory", "securityVulnerability",
			"vulnerableManifestFilename", "vulnerableRequirements", "repository")
	case "ProjectV2":
		return FieldRespWithArgs(map[string][]string{
			"items": {"query", "first", "after"},
		}, "id", "title", "number", "shortDescription", "public",
			"closed", "readme", "url", "owner", "fields", "items")
	case "SearchType":
		return EnumResp("ISSUE", "ISSUE_ADVANCED", "REPOSITORY", "USER", "DISCUSSION")
	case "Release":
		return FieldResp("id", "name", "tagName", "description",
			"createdAt", "publishedAt", "url", "isDraft", "isPrerelease",
			"isLatest", "author", "immutable")
	case "LinkedBranch":
		return FieldResp("id", "ref")
	default:
		return FieldResp("id")
	}
}

// FieldResp builds a __type response with a list of field names.
func FieldResp(names ...string) map[string]any {
	fields := make([]any, len(names))
	for i, n := range names {
		fields[i] = map[string]any{"name": n}
	}
	return map[string]any{"fields": fields}
}

// FieldRespWithArgs builds a __type response where some fields have args.
func FieldRespWithArgs(argsMap map[string][]string, names ...string) map[string]any {
	fields := make([]any, len(names))
	for i, n := range names {
		f := map[string]any{"name": n}
		if argNames, ok := argsMap[n]; ok {
			args := make([]any, len(argNames))
			for j, a := range argNames {
				args[j] = map[string]any{"name": a}
			}
			f["args"] = args
		}
		fields[i] = f
	}
	return map[string]any{"fields": fields}
}

// EnumResp builds a __type response with enum values.
func EnumResp(names ...string) map[string]any {
	vals := make([]any, len(names))
	for i, n := range names {
		vals[i] = map[string]any{"name": n}
	}
	return map[string]any{"enumValues": vals}
}
