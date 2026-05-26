package graphql

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

// Regex patterns for extracting query-level PR filters and search aliases.
var (
	reHeadRefVar  = regexp.MustCompile(`headRefName\s*:\s*\$(\w+)`)
	reSearchAlias = regexp.MustCompile(`(\w+)\s*:\s*search\s*\(\s*query\s*:\s*\$(\w+)`)
)

// --- PR queries ---

func (s *Server) doPRSingle(ctx context.Context, req gqlRequest) map[string]any {
	owner, name, _ := resolveRepo(req.Variables)
	num := intVar(req.Variables, "number", "pr_number", "pullRequestNumber")
	if owner == "" || name == "" || num == 0 {
		return wrap("repository", map[string]any{"pullRequest": nil})
	}
	pr, err := s.Svc.GetPR(ctx, owner+"/"+name, num)
	if err != nil {
		return wrap("repository", map[string]any{"pullRequest": nil})
	}

	prShape := s.prGQL(ctx, pr, req.Query)

	// Handle baseRef.compare(headRef:) query — used by gh pr update-branch
	if headRef, ok := req.Variables["headRef"].(string); ok && headRef != "" && strings.Contains(req.Query, "compare") {
		repoFullName := owner + "/" + name
		// Resolve the head ref — it may be in "owner:branch" format for cross-repo PRs
		compareHead := headRef
		if parts := strings.SplitN(headRef, ":", 2); len(parts) == 2 {
			compareHead = parts[1]
		}
		baseSHA, _ := s.Svc.Git.HeadSHA(ctx, repoFullName, pr.BaseRef)
		headSHA, _ := s.Svc.Git.HeadSHA(ctx, repoFullName, compareHead)

		var aheadBy, behindBy int
		status := "IDENTICAL"
		if baseSHA != "" && headSHA != "" {
			result, err := s.Svc.ComparePR(ctx, repoFullName, baseSHA, headSHA)
			if err == nil {
				aheadBy = result.AheadBy
				behindBy = result.BehindBy
				switch {
				case aheadBy == 0 && behindBy == 0:
					status = "IDENTICAL"
				case aheadBy > 0 && behindBy > 0:
					status = "DIVERGED"
				case aheadBy > 0:
					status = "AHEAD"
				case behindBy > 0:
					status = "BEHIND"
				}
			}
		}

		prShape["baseRef"] = map[string]any{
			"compare": map[string]any{
				"aheadBy":  aheadBy,
				"behindBy": behindBy,
				"status":   status,
			},
		}
	}

	return wrap("repository", map[string]any{"pullRequest": prShape})
}

func (s *Server) doPRs(ctx context.Context, req gqlRequest) map[string]any {
	owner, name, _ := resolveRepo(req.Variables)

	// Parse states from GraphQL variables (e.g. ["OPEN"], ["CLOSED"], ["MERGED"])
	state := db.StateOpen
	if raw, ok := req.Variables["states"]; ok {
		if arr, ok := raw.([]any); ok && len(arr) > 0 {
			first := strings.ToLower(fmt.Sprintf("%v", arr[0]))
			switch {
			case len(arr) > 1:
				state = "all"
			case first == db.StateClosed || first == "merged":
				state = db.StateClosed
			}
		}
	}

	prs, err := s.Svc.ListPRs(ctx, owner+"/"+name, state)
	logErr(ctx, "gql.ListPRs", err)

	// Filter by headRefName if provided (used by gh pr status)
	headRefFilter := strFrom(req.Variables, "headRefName")
	if headRefFilter == "" {
		// Also try extracting from the query string itself
		if m := reHeadRefVar.FindStringSubmatch(req.Query); len(m) > 1 {
			headRefFilter = strFrom(req.Variables, m[1])
		}
	}

	var nodes []any
	for _, p := range prs {
		if headRefFilter != "" && p.HeadRef != headRefFilter {
			continue
		}
		pr := s.prGQL(ctx, p, req.Query)
		nodes = append(nodes, pr)
	}
	if nodes == nil {
		nodes = []any{}
	}
	return wrap("repository", map[string]any{"pullRequests": gqlConn(nodes)})
}

func (s *Server) doSearch(ctx context.Context, req gqlRequest) map[string]any {
	// Check for repository search (type: REPOSITORY)
	if has(req.Query, "repository") && has(req.Query, "type: repository") {
		q := strFrom(req.Variables, "query")
		return wrap("search", s.runRepoSearch(ctx, q, req.Query))
	}

	// Handle aliased search queries (e.g. "viewerCreated: search(...)" + "reviewRequested: search(...)")
	// by extracting variable names from the raw query.
	aliases := extractSearchAliases(req.Query)

	if len(aliases) == 0 {
		// Simple single-search query
		q := strFrom(req.Variables, "query")
		return wrap("search", s.runSearch(ctx, q, req.Query))
	}

	// Multiple aliased searches: build response with each alias as a key
	data := make(map[string]any)
	for alias, varName := range aliases {
		q := strFrom(req.Variables, varName)
		data[alias] = s.runSearch(ctx, q, req.Query)
	}
	return map[string]any{"data": data}
}

// runRepoSearch handles search(type: REPOSITORY) queries.
func (s *Server) runRepoSearch(ctx context.Context, q, rawQuery string) map[string]any {
	repos, err := s.Svc.SearchReposGQL(ctx, q)
	logErr(ctx, "gql.SearchReposGQL", err)
	nodes := make([]any, len(repos))
	for i, r := range repos {
		nodes[i] = s.repoGQL(ctx, r)
	}
	conn := gqlConn(nodes)
	conn["repositoryCount"] = len(nodes)
	return conn
}

// runSearch runs a single search query and returns the connection map.
func (s *Server) runSearch(ctx context.Context, q, rawQuery string) map[string]any {
	// Resolve @me to the current user's login (the CLI sends author:@me for non-enterprise hosts)
	if strings.Contains(q, "@me") {
		if u, err := s.Svc.GetCurrentUser(ctx); err == nil {
			q = strings.ReplaceAll(q, "@me", u.Login)
		}
	}
	sq := service.ParseSearchQuery(q)
	if sq.IsPR {
		prs, err := s.Svc.SearchPRs(ctx, q)
		logErr(ctx, "gql.SearchPRs", err)
		nodes := make([]any, len(prs))
		for i, p := range prs {
			nodes[i] = s.prGQL(ctx, p, rawQuery)
		}
		conn := gqlConn(nodes)
		conn["issueCount"] = len(nodes)
		return conn
	}
	issues, err := s.Svc.SearchIssues(ctx, q)
	logErr(ctx, "gql.SearchIssues", err)
	nodes := make([]any, len(issues))
	for i, is := range issues {
		nodes[i] = s.issueGQL(ctx, is, rawQuery)
	}
	conn := gqlConn(nodes)
	conn["issueCount"] = len(nodes)
	return conn
}

// extractSearchAliases parses the raw GraphQL query to find aliased search calls
// and maps each alias to its query variable name.
// E.g. "viewerCreated: search(query: $viewerQuery, ...)" → {"viewerCreated": "viewerQuery"}
func extractSearchAliases(query string) map[string]string {
	matches := reSearchAlias.FindAllStringSubmatch(query, -1)
	if len(matches) == 0 {
		return nil
	}
	result := make(map[string]string, len(matches))
	for _, m := range matches {
		result[m[1]] = m[2]
	}
	return result
}
