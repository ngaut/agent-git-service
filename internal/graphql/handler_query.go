package graphql

import (
	"context"
	"regexp"
	"strings"
)

// Query dispatch precedence (highest -> lowest):
// Phase 1: Batched repo aliases (highest priority)
// Phase 2: Type introspection
// Phase 3: Node query
// Phase 4: Repository subfield dispatch map
// Phase 5: Operation name fallback
// Phase 6: Query content fallback (lowest priority)
//
// Note: Additional specialized handlers (resource/owner/user/org/viewer/search)
// run between phases 3 and 4 when their top-level fields are present.

var repoAliasRegex = regexp.MustCompile(`(?s)([a-zA-Z0-9_]+)\s*:\s*repository\s*\(\s*owner\s*:\s*"([^"]+)"\s*,\s*name\s*:\s*"([^"]+)"\s*\)`)

type repoSubfieldHandler func(*Server, context.Context, gqlRequest) (map[string]any, bool)

func wrapRepoSubfield(handler func(*Server, context.Context, gqlRequest) map[string]any) repoSubfieldHandler {
	return func(s *Server, ctx context.Context, req gqlRequest) (map[string]any, bool) {
		return handler(s, ctx, req), true
	}
}

// repoSubfieldDispatch maps repository subfields to their handlers.
var repoSubfieldDispatch = map[string]repoSubfieldHandler{
	"issueOrPullRequest":   wrapRepoSubfield((*Server).doIssueOrPR),
	"issue":                wrapRepoSubfield((*Server).doIssueSingle),
	"pullRequest":          wrapRepoSubfield((*Server).doPRSingle),
	"issues":               wrapRepoSubfield((*Server).doIssues),
	"pullRequests":         wrapRepoSubfield((*Server).doPRs),
	"releases":             wrapRepoSubfield((*Server).doReleaseList),
	"release":              wrapRepoSubfield((*Server).doReleaseSingle),
	"labels":               wrapRepoSubfield((*Server).doRepoLabels),
	"assignableUsers":      wrapRepoSubfield((*Server).doAssignableUsers),
	"milestones":           wrapRepoSubfield((*Server).doRepoMilestones),
	"projectsV2":           wrapRepoSubfield((*Server).doRepository),
	"rulesets":             wrapRepoSubfield((*Server).doRulesetList),
	"issueTemplates":       wrapRepoSubfield((*Server).doIssueTemplates),
	"pullRequestTemplates": wrapRepoSubfield((*Server).doPullRequestTemplates),
	"milestone":            wrapRepoSubfield((*Server).doRepoMilestone),
	"forks":                wrapRepoSubfield((*Server).doRepoForks),
}

// repoSubfieldDispatchOrder preserves the historical precedence when multiple
// subfields are present. Keep this list in sync with repoSubfieldDispatch.
var repoSubfieldDispatchOrder = []string{
	"issueOrPullRequest",
	"issue",
	"pullRequest",
	"issues",
	"pullRequests",
	"releases",
	"release",
	"labels",
	"assignableUsers",
	"milestones",
	"projectsV2",
	"rulesets",
	"issueTemplates",
	"pullRequestTemplates",
	"milestone",
	"forks",
}

func (s *Server) routeQuery(ctx context.Context, req gqlRequest, op string, ast map[string]any, typeIntro map[string]string) map[string]any {
	// Dispatch phases in priority order
	if result, handled := s.tryBatchedRepoAliases(ctx, req); handled {
		return result
	}
	if result, handled := s.tryTypeIntrospection(typeIntro); handled {
		return result
	}
	if result, handled := s.tryNodeQuery(ctx, req, ast); handled {
		return result
	}
	if result, handled := s.tryResourceQuery(ctx, req, ast); handled {
		return result
	}
	if result, handled := s.tryRepositoryOwnerQuery(ctx, req, ast); handled {
		return result
	}
	if result, handled := s.tryUserOrgCombined(ctx, req, ast); handled {
		return result
	}
	if result, handled := s.tryUserQuery(ctx, req, ast); handled {
		return result
	}
	if result, handled := s.tryOrgQuery(ctx, req, ast); handled {
		return result
	}
	if result, handled := s.tryViewerQuery(ctx, req, ast); handled {
		return result
	}
	if result, handled := s.trySearchQuery(ctx, req, ast, op); handled {
		return result
	}
	if result, handled := s.tryRepositoryQuery(ctx, req, ast); handled {
		return result
	}
	if result, handled := s.tryOperationNameFallback(ctx, req, op); handled {
		return result
	}
	if result, handled := s.tryQueryContentFallback(ctx, req); handled {
		return result
	}
	return map[string]any{"data": map[string]any{}}
}

// tryBatchedRepoAliases handles batched aliased repository queries
// (used by gh repo set-default / ResolveRemotesToRepos)
func (s *Server) tryBatchedRepoAliases(ctx context.Context, req gqlRequest) (map[string]any, bool) {
	matches := repoAliasRegex.FindAllStringSubmatch(req.Query, -1)
	if len(matches) == 0 {
		return nil, false
	}
	out := make(map[string]any)
	userNodes := s.allUserNodes(ctx)
	for _, m := range matches {
		alias := m[1]
		owner := m[2]
		name := m[3]
		rep, err := s.Svc.GetRepo(ctx, owner+"/"+name)
		if err != nil {
			out[alias] = nil
		} else {
			out[alias] = s.repoGQL(ctx, rep, userNodes)
		}
	}
	// Include viewer if the query requests it (used by RepositoryNetwork)
	if has(req.Query, "viewer") {
		if u, err := s.Svc.GetCurrentUser(ctx); err == nil {
			out["viewer"] = map[string]any{"login": u.Login}
		}
	}
	return map[string]any{"data": out}, true
}

// tryTypeIntrospection handles __type introspection queries (feature detection)
func (s *Server) tryTypeIntrospection(typeIntro map[string]string) (map[string]any, bool) {
	if len(typeIntro) == 0 {
		return nil, false
	}
	return DoTypeIntrospection(typeIntro), true
}

// tryNodeQuery handles node(id:) queries with optional repository/organization merge
func (s *Server) tryNodeQuery(ctx context.Context, req gqlRequest, ast map[string]any) (map[string]any, bool) {
	if !astHas(ast, "node") {
		return nil, false
	}
	result := s.doNode(ctx, req)
	if astHas(ast, "repository") || astHas(ast, "organization") {
		// Merge in repository data
		if astHas(ast, "repository") {
			repoResult := s.doRepository(ctx, req)
			MergeInto(result, repoResult)
		}
		// Merge in organization data (teams, etc.)
		if astHas(ast, "organization") {
			orgChild := astChild(ast, "organization")
			var orgResult map[string]any
			if orgChild != nil && astHas(orgChild, "teams") {
				orgResult = s.doOrgTeams(ctx, req)
			} else if orgChild != nil && astHas(orgChild, "team") {
				orgResult = s.doOrgTeam(ctx, req)
			} else {
				orgResult = s.doOrgTeams(ctx, req)
			}
			MergeInto(result, orgResult)
		}
	}
	return result, true
}

// tryResourceQuery handles resource(url:) queries
func (s *Server) tryResourceQuery(ctx context.Context, req gqlRequest, ast map[string]any) (map[string]any, bool) {
	if !astHas(ast, "resource") {
		return nil, false
	}
	return s.doResource(ctx, req), true
}

// tryRepositoryOwnerQuery handles repositoryOwner queries
func (s *Server) tryRepositoryOwnerQuery(ctx context.Context, req gqlRequest, ast map[string]any) (map[string]any, bool) {
	if !astHas(ast, "repositoryOwner") {
		return nil, false
	}
	return s.doRepositoryOwner(ctx, req), true
}

// tryUserOrgCombined handles user + organization combined queries
func (s *Server) tryUserOrgCombined(ctx context.Context, req gqlRequest, ast map[string]any) (map[string]any, bool) {
	if !astHas(ast, "user") || !astHas(ast, "organization") {
		return nil, false
	}
	userChild := astChild(ast, "user")
	orgChild := astChild(ast, "organization")
	// If either has projectV2, it's a project query
	if (userChild != nil && astHas(userChild, "projectV2")) || (orgChild != nil && astHas(orgChild, "projectV2")) {
		return s.doUserOrgProject(ctx, req), true
	}
	return s.doUserOrgOwner(ctx, req), true
}

// tryUserQuery handles single user queries
func (s *Server) tryUserQuery(ctx context.Context, req gqlRequest, ast map[string]any) (map[string]any, bool) {
	if !astHas(ast, "user") {
		return nil, false
	}
	userChild := astChild(ast, "user")
	if userChild != nil && astHas(userChild, "projectV2") {
		return s.doUserOrgProject(ctx, req), true
	}
	if userChild != nil && astHas(userChild, "organizations") {
		return s.doUserWithOrgs(ctx, req), true
	}
	return nil, false
}

// tryOrgQuery handles single organization queries
func (s *Server) tryOrgQuery(ctx context.Context, req gqlRequest, ast map[string]any) (map[string]any, bool) {
	if !astHas(ast, "organization") {
		return nil, false
	}
	orgChild := astChild(ast, "organization")
	if orgChild == nil {
		return nil, false
	}
	if astHas(orgChild, "projectsV2") {
		return s.doProjectV2List(ctx, req), true
	}
	if astHas(orgChild, "projectV2") {
		return s.doUserOrgProject(ctx, req), true
	}
	if astHas(orgChild, "team") {
		return s.doOrgTeam(ctx, req), true
	}
	if astHas(orgChild, "teams") {
		return s.doOrgTeams(ctx, req), true
	}
	return nil, false
}

// tryViewerQuery handles viewer queries
func (s *Server) tryViewerQuery(ctx context.Context, req gqlRequest, ast map[string]any) (map[string]any, bool) {
	if !astHas(ast, "viewer") {
		return nil, false
	}
	viewerChild := astChild(ast, "viewer")
	if viewerChild != nil {
		if astHas(viewerChild, "organizations") {
			return s.doViewerWithOrgs(ctx), true
		}
		if astHas(viewerChild, "projectsV2") {
			return s.doProjectV2List(ctx, req), true
		}
	}
	return s.doViewer(ctx), true
}

// trySearchQuery handles search queries with optional repository merge
func (s *Server) trySearchQuery(ctx context.Context, req gqlRequest, ast map[string]any, op string) (map[string]any, bool) {
	if !astHas(ast, "search") && !has(op, "search") {
		return nil, false
	}
	result := s.doSearch(ctx, req)
	// Merge repository data if the query also requests it
	if astHas(ast, "repository") {
		repoChild := astChild(ast, "repository")
		repoResult := s.doRepository(ctx, req)
		// Overlay pullRequests if requested (doRepository doesn't include them)
		if repoChild != nil && astHas(repoChild, "pullRequests") {
			MergeNestedData(repoResult, s.doPRs(ctx, req), "repository")
		}
		MergeInto(result, repoResult)
	}
	return result, true
}

// tryRepositoryQuery handles repository queries with subfield dispatch.
func (s *Server) tryRepositoryQuery(ctx context.Context, req gqlRequest, ast map[string]any) (map[string]any, bool) {
	if !astHas(ast, "repository") {
		return nil, false
	}
	repoChild := astChild(ast, "repository")
	if repoChild != nil {
		for _, field := range repoSubfieldDispatchOrder {
			if !astHas(repoChild, field) {
				continue
			}
			if handler, ok := repoSubfieldDispatch[field]; ok {
				return handler(s, ctx, req)
			}
		}
	}
	return s.doRepository(ctx, req), true
}

// tryOperationNameFallback handles low-priority operation name fallbacks for CLI queries.
// Precedence: runs only after repository subfield dispatch and all structured handlers.
func (s *Server) tryOperationNameFallback(ctx context.Context, req gqlRequest, op string) (map[string]any, bool) {
	switch {
	case has(op, "orgproject"), has(op, "userproject"):
		return s.doUserOrgProject(ctx, req), true
	case has(op, "userorgowner"):
		return s.doUserOrgOwner(ctx, req), true
	case has(op, "issuebynumber"):
		return s.doIssueSingle(ctx, req), true
	case has(op, "issuelist"), has(op, "repoissues"):
		return s.doIssues(ctx, req), true
	case has(op, "pullrequestlist"), has(op, "prs"):
		return s.doPRs(ctx, req), true
	case has(op, "repositoryinfo"), has(op, "repoinfo"), has(op, "reponetwork"), has(op, "repodefaultbranch"):
		return s.doRepository(ctx, req), true
	case has(op, "reporulesetlist"):
		return s.doRulesetList(ctx, req), true
	case has(op, "viewer"):
		return s.doViewer(ctx), true
	}
	return nil, false
}

// tryQueryContentFallback handles the lowest-priority query content heuristics.
// Precedence: runs only after operation-name fallbacks fail.
func (s *Server) tryQueryContentFallback(ctx context.Context, req gqlRequest) (map[string]any, bool) {
	if strings.Contains(req.Query, "rulesets") && strings.Contains(req.Query, "repository") {
		return s.doRulesetList(ctx, req), true
	}
	return nil, false
}

func has(s, sub string) bool { return strings.Contains(s, sub) }

// MergeInto copies top-level keys from src's "data" map into dst's "data" map.
// Used to combine multiple query results (e.g. search + repository).
func MergeInto(dst, src map[string]any) {
	dstData, ok1 := dst["data"].(map[string]any)
	srcData, ok2 := src["data"].(map[string]any)
	if ok1 && ok2 {
		for k, v := range srcData {
			dstData[k] = v
		}
	}
}

// MergeNestedData merges nested data from src's "data.key" map into dst's "data.key" map.
func MergeNestedData(dst, src map[string]any, key string) {
	dstData, _ := dst["data"].(map[string]any)
	srcData, _ := src["data"].(map[string]any)
	if dstData == nil || srcData == nil {
		return
	}
	dstNested, _ := dstData[key].(map[string]any)
	srcNested, _ := srcData[key].(map[string]any)
	if dstNested != nil && srcNested != nil {
		for k, v := range srcNested {
			dstNested[k] = v
		}
	}
}

// wrap creates {"data": {key: value}}

func wrap(key string, value any) map[string]any {
	return map[string]any{"data": map[string]any{key: value}}
}

func errResp(msg string) map[string]any {
	return map[string]any{"errors": []any{map[string]any{"message": msg}}}
}

// --- Viewers ---
