package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gh-server/internal/db"
	"gh-server/internal/gitstore"
)

// --- Repository queries ---

func (s *Server) doRepository(ctx context.Context, req gqlRequest) map[string]any {
	owner, name, _ := resolveRepo(req.Variables)
	if owner == "" || name == "" {
		return wrap("repository", nil)
	}
	rep, err := s.Svc.GetRepo(ctx, owner+"/"+name)
	if err != nil {
		return wrap("repository", nil)
	}
	data := s.repoGQL(ctx, rep)

	// Inject target object IDs for GraphQL Git ref queries
	if defBranchData, ok := data["defaultBranchRef"].(map[string]any); ok {
		if oid, err := s.Svc.Git.HeadSHA(ctx, rep.FullName, rep.DefaultBranch); err == nil {
			defBranchData["target"] = map[string]any{"oid": oid}
		}
	}

	if refName := strFrom(req.Variables, "ref"); refName != "" {
		branch := strings.TrimPrefix(refName, gitstore.RefsHeadsPrefix)
		if oid, err := s.Svc.Git.HeadSHA(ctx, rep.FullName, branch); err == nil {
			data["ref"] = map[string]any{
				"target": map[string]any{"oid": oid},
			}
		}
	}

	// Handle qualifiedName ref queries (used by remoteTagExists: repository.ref(qualifiedName: $tagName))
	if tagName := strFrom(req.Variables, "tagName"); tagName != "" {
		refName := tagName
		if strings.HasPrefix(refName, gitstore.RefsTagsPrefix) {
			// Check if the tag exists in git
			tagRef := strings.TrimPrefix(refName, gitstore.RefsTagsPrefix)
			tags, _ := s.Svc.Git.ListTags(ctx, rep.FullName)
			var found bool
			for _, t := range tags {
				if t.Name == tagRef {
					data["ref"] = map[string]any{
						"id": gqlID("Ref", rep.ID),
					}
					found = true
					break
				}
			}
			if !found {
				data["ref"] = nil
			}
		} else if strings.HasPrefix(refName, gitstore.RefsHeadsPrefix) {
			branch := strings.TrimPrefix(refName, gitstore.RefsHeadsPrefix)
			if oid, err := s.Svc.Git.HeadSHA(ctx, rep.FullName, branch); err == nil {
				data["ref"] = map[string]any{
					"id":     gqlID("Ref", rep.ID),
					"target": map[string]any{"oid": oid},
				}
			} else {
				data["ref"] = nil
			}
		}
	}

	return wrap("repository", data)
}

// doIssueOrPR handles repository.issueOrPullRequest(number:) queries.
// The CLI uses this with __typename + ...on Issue / ...on PullRequest fragments.
func (s *Server) doIssueOrPR(ctx context.Context, req gqlRequest) map[string]any {
	owner, name, _ := resolveRepo(req.Variables)
	num := intVar(req.Variables, "number")
	if owner == "" || name == "" || num == 0 {
		return wrap("repository", map[string]any{"hasIssuesEnabled": true, "issueOrPullRequest": nil})
	}
	fullName := owner + "/" + name

	// Try issue first
	if issue, err := s.Svc.GetIssue(ctx, fullName, num); err == nil {
		return wrap("repository", map[string]any{
			"hasIssuesEnabled":   true,
			"issueOrPullRequest": s.issueGQL(ctx, issue, req.Query),
			"issue":              s.issueGQL(ctx, issue, req.Query),
		})
	}
	// Try PR
	if pr, err := s.Svc.GetPR(ctx, fullName, num); err == nil {
		return wrap("repository", map[string]any{
			"hasIssuesEnabled":   true,
			"issueOrPullRequest": s.prGQL(ctx, pr, req.Query),
			"issue":              s.prGQL(ctx, pr, req.Query),
		})
	}
	return wrap("repository", map[string]any{"hasIssuesEnabled": true, "issueOrPullRequest": nil, "issue": nil})
}

func (s *Server) doRulesetList(ctx context.Context, req gqlRequest) map[string]any {
	owner := strFrom(req.Variables, "owner")
	name := strFrom(req.Variables, "repo")
	fullName := owner + "/" + name

	rep, err := s.Svc.GetRepo(ctx, fullName)
	if err != nil {
		return map[string]any{
			"data": map[string]any{
				"level": nil,
			},
		}
	}

	rulesets, err := s.Svc.ListRulesets(ctx, fullName)
	if err != nil {
		rulesets = []db.Ruleset{}
	}

	nodes := make([]any, len(rulesets))
	for i, rs := range rulesets {
		// Parse rules array to get count
		var rules []any
		if rs.RulesJSON != "" {
			if err := json.Unmarshal([]byte(rs.RulesJSON), &rules); err != nil {
				rules = []any{}
			}
		}
		nodes[i] = map[string]any{
			"databaseId":  rs.ID,
			"name":        rs.Name,
			"target":      strings.ToUpper(rs.Target),
			"enforcement": strings.ToUpper(rs.Enforcement),
			"source": map[string]any{
				"__typename": "Repository",
				"owner":      rep.FullName,
			},
			"rules": map[string]any{
				"totalCount": len(rules),
			},
		}
	}

	return map[string]any{
		"data": map[string]any{
			"level": map[string]any{
				"rulesets": map[string]any{
					"totalCount": len(nodes),
					"nodes":      nodes,
					"pageInfo":   gqlPageInfo(),
				},
			},
		},
	}
}

// doIssueTemplates returns an empty list of issue templates.
func (s *Server) doIssueTemplates(ctx context.Context, req gqlRequest) map[string]any {
	return wrap("repository", map[string]any{"issueTemplates": []any{}})
}

// doPullRequestTemplates returns an empty list of pull request templates.
func (s *Server) doPullRequestTemplates(ctx context.Context, req gqlRequest) map[string]any {
	return wrap("repository", map[string]any{"pullRequestTemplates": []any{}})
}

// doRepoMilestone returns a single milestone by number.
func (s *Server) doRepoMilestone(ctx context.Context, req gqlRequest) map[string]any {
	owner, name, _ := resolveRepo(req.Variables)
	num := intVar(req.Variables, "number")
	milestones, _, err := s.Svc.ListMilestones(ctx, owner+"/"+name, "all", "", "", 1, 0)
	logErr(ctx, "gql.ListMilestones", err)
	for _, m := range milestones {
		if m.Number == num {
			return wrap("repository", map[string]any{"milestone": s.milestoneGQL(m)})
		}
	}
	return wrap("repository", map[string]any{"milestone": nil})
}

// doRepoForks returns the forks of a repository.
func (s *Server) doRepoForks(ctx context.Context, req gqlRequest) map[string]any {
	owner, name, _ := resolveRepo(req.Variables)
	repo, err := s.Svc.GetRepo(ctx, owner+"/"+name)
	if err != nil {
		return wrap("repository", map[string]any{"forks": emptyConn()})
	}
	forks, err := s.Svc.ListForks(ctx, repo.ID)
	logErr(ctx, "gql.ListForks", err)
	nodes := make([]any, len(forks))
	for i, f := range forks {
		nodes[i] = map[string]any{
			"id":               gqlID("Repository", f.ID),
			"name":             f.Name,
			"owner":            map[string]any{"login": f.Owner.Login},
			"url":              fmt.Sprintf("%s/%s", s.Svc.HTMLBaseURL(), f.FullName),
			"viewerPermission": s.viewerPermission(ctx, f),
		}
	}
	return wrap("repository", map[string]any{"forks": gqlConn(nodes)})
}

// doOrgTeam returns a single team by slug within an organization.
func (s *Server) doOrgTeam(ctx context.Context, req gqlRequest) map[string]any {
	login := strFrom(req.Variables, "login")
	slug := strFrom(req.Variables, "slug")

	org, err := s.Svc.GetUser(ctx, login)
	if err != nil {
		return wrap("organization", map[string]any{"team": nil})
	}

	team, err := s.Svc.GetTeam(ctx, org.ID, slug)
	if err != nil {
		return wrap("organization", map[string]any{"team": nil})
	}
	team.Organization = org

	return wrap("organization", map[string]any{
		"team": s.teamGQL(team),
	})
}

// doOrgTeams returns the teams within an organization.
func (s *Server) doOrgTeams(ctx context.Context, req gqlRequest) map[string]any {
	login := strFrom(req.Variables, "login")
	org, err := s.Svc.GetUser(ctx, login)
	if err != nil {
		return wrap("organization", map[string]any{"teams": emptyConn()})
	}

	teams, err := s.Svc.ListOrgTeams(ctx, org.ID)
	logErr(ctx, "gql.ListOrgTeams", err)

	nodes := make([]any, len(teams))
	for i, t := range teams {
		t.Organization = org
		nodes[i] = s.teamGQL(t)
	}

	return wrap("organization", map[string]any{"teams": gqlConn(nodes)})
}
