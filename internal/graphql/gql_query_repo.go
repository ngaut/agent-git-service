package graphql

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (s *Server) doNode(ctx context.Context, req gqlRequest) map[string]any {
	id := strFrom(req.Variables, "id")
	q := strings.ToLower(req.Query)

	// Resolve Issue node
	if dbID := parseNodeID(id, "Issue"); dbID > 0 {
		if issue, err := s.Svc.GetIssueByID(ctx, dbID); err == nil {
			node := map[string]any{
				"id":         id,
				"__typename": "Issue",
			}
			if has(q, "comments") {
				node["comments"] = s.issueCommentsGQL(ctx, issue.RepositoryID, issue.Number)
			}
			return map[string]any{"data": map[string]any{"node": node}}
		}
	}

	// Resolve PullRequest node
	if dbID := parseNodeID(id, "PullRequest"); dbID > 0 {
		if pr, err := s.Svc.GetPRByID(ctx, dbID); err == nil {
			// Return the full PR shape so statusCheckRollup and other fields are available
			node := s.prGQL(ctx, pr, req.Query)
			node["id"] = id
			node["__typename"] = "PullRequest"
			// Override with node-specific fields if requested
			if has(q, "comments") {
				node["comments"] = s.issueCommentsGQL(ctx, pr.RepositoryID, pr.Number)
			}
			if has(q, "reviews") {
				reviews, err := s.Svc.ListPRReviews(ctx, pr.ID)
				logErr(ctx, "gql.ListPRReviews", err)
				node["reviews"] = s.reviewsFromList(reviews, pr.Repository.Owner.Login)
			}
			return map[string]any{"data": map[string]any{"node": node}}
		}
	}

	return map[string]any{"data": map[string]any{"node": nil}}
}

// doResource handles resource(url:) queries — resolves a URL to an Issue or PullRequest.
// Used by `gh project item-add` to resolve issue/PR URLs to node IDs.
func (s *Server) doResource(ctx context.Context, req gqlRequest) map[string]any {
	rawURL := strFrom(req.Variables, "url")
	if rawURL == "" {
		return wrap("resource", nil)
	}

	// Parse URL to extract owner/repo/number and type
	// Expected formats:
	//   https://host/OWNER/REPO/issues/NUMBER
	//   https://host/OWNER/REPO/pull/NUMBER
	parts := strings.Split(strings.Trim(rawURL, "/"), "/")
	if len(parts) < 4 {
		return wrap("resource", nil)
	}

	// Find the "issues" or "pull" segment
	var owner, repo, numStr, resourceType string
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "issues" || parts[i] == "pull" {
			if i >= 2 {
				owner = parts[i-2]
				repo = parts[i-1]
				numStr = parts[i+1]
				resourceType = parts[i]
			}
			break
		}
	}

	if owner == "" || numStr == "" {
		return wrap("resource", nil)
	}

	num, err := strconv.Atoi(numStr)
	if err != nil {
		return wrap("resource", nil)
	}

	fullName := owner + "/" + repo
	if resourceType == "issues" {
		issue, err := s.Svc.GetIssue(ctx, fullName, num)
		if err != nil {
			return wrap("resource", nil)
		}
		return wrap("resource", map[string]any{
			"__typename": "Issue",
			"id":         gqlID("Issue", issue.ID),
		})
	}

	if resourceType == "pull" {
		pr, err := s.Svc.GetPR(ctx, fullName, num)
		if err != nil {
			return wrap("resource", nil)
		}
		return wrap("resource", map[string]any{
			"__typename": "PullRequest",
			"id":         gqlID("PullRequest", pr.ID),
		})
	}

	return wrap("resource", nil)
}

func (s *Server) doReleaseList(ctx context.Context, req gqlRequest) map[string]any {
	owner, name, _ := resolveRepo(req.Variables)
	fullName := owner + "/" + name
	releases, err := s.Svc.ListReleases(ctx, fullName)
	if err != nil {
		return wrap("repository", map[string]any{"releases": emptyConn()})
	}
	nodes := make([]any, len(releases))
	for i, r := range releases {
		publishedAt := ""
		if r.PublishedAt != nil {
			publishedAt = r.PublishedAt.Format(time.RFC3339)
		}
		tagName := r.TagName
		nodes[i] = map[string]any{
			"id":           gqlID("Release", r.ID),
			"databaseId":   r.ID,
			"name":         r.Name,
			"tagName":      tagName,
			"tag":          map[string]any{"name": tagName},
			"isDraft":      r.Draft,
			"isPrerelease": r.PreRelease,
			"isLatest":     i == 0,
			"createdAt":    r.CreatedAt.Format(time.RFC3339),
			"publishedAt":  publishedAt,
			"url":          fmt.Sprintf("%s/%s/releases/tag/%s", s.Svc.HTMLBaseURL(), fullName, tagName),
			"author":       map[string]any{"login": r.Author.Login},
		}
	}
	return wrap("repository", map[string]any{"releases": gqlConn(nodes)})
}

func (s *Server) doReleaseSingle(ctx context.Context, req gqlRequest) map[string]any {
	owner, name, _ := resolveRepo(req.Variables)
	tagName := strFrom(req.Variables, "tagName")
	fullName := owner + "/" + name
	release, err := s.Svc.GetReleaseByTag(ctx, fullName, tagName)
	if err != nil {
		return wrap("repository", map[string]any{"release": nil})
	}
	publishedAt := ""
	if release.PublishedAt != nil {
		publishedAt = release.PublishedAt.Format(time.RFC3339)
	}
	return wrap("repository", map[string]any{"release": map[string]any{
		"id":             gqlID("Release", release.ID),
		"databaseId":     release.ID,
		"name":           release.Name,
		"tagName":        release.TagName,
		"tag":            map[string]any{"name": release.TagName},
		"isDraft":        release.Draft,
		"isPrerelease":   release.PreRelease,
		"isLatest":       true,
		"createdAt":      release.CreatedAt.Format(time.RFC3339),
		"publishedAt":    publishedAt,
		"url":            fmt.Sprintf("%s/%s/releases/tag/%s", s.Svc.HTMLBaseURL(), fullName, release.TagName),
		"author":         map[string]any{"login": release.Author.Login},
		"body":           release.Body,
		"releaseAssets":  s.releaseAssetsGQL(ctx, release),
		"reactionGroups": defaultReactionGroups(),
		"__typename":     "Release",
	}})
}

func (s *Server) doRepoLabels(ctx context.Context, req gqlRequest) map[string]any {
	owner, name, _ := resolveRepo(req.Variables)
	labels, err := s.Svc.ListLabels(ctx, owner+"/"+name)
	if err != nil {
		return wrap("repository", map[string]any{"labels": emptyConn()})
	}
	nodes := make([]any, len(labels))
	for i, l := range labels {
		nodes[i] = map[string]any{
			"id":          gqlID("Label", l.ID),
			"name":        l.Name,
			"description": l.Description,
			"color":       l.Color,
			"__typename":  "Label",
		}
	}
	return wrap("repository", map[string]any{"labels": gqlConn(nodes)})
}

func (s *Server) doAssignableUsers(ctx context.Context, req gqlRequest) map[string]any {
	owner, name, _ := resolveRepo(req.Variables)
	// Validate repo exists, return empty users if not
	if _, err := s.Svc.GetRepo(ctx, owner+"/"+name); err != nil {
		return wrap("repository", map[string]any{"assignableUsers": emptyConn()})
	}
	users, err := s.Svc.ListAllUsers(ctx)
	logErr(ctx, "gql.ListAllUsers", err)
	nodes := make([]any, len(users))
	for i, u := range users {
		nodes[i] = map[string]any{
			"id":         gqlID("User", u.ID),
			"login":      u.Login,
			"name":       u.Name,
			"__typename": "User",
		}
	}
	return wrap("repository", map[string]any{"assignableUsers": gqlConn(nodes)})
}

func (s *Server) doRepoMilestones(ctx context.Context, req gqlRequest) map[string]any {
	owner, name, _ := resolveRepo(req.Variables)
	fullName := owner + "/" + name
	if _, err := s.Svc.GetRepo(ctx, fullName); err != nil {
		return wrap("repository", map[string]any{"milestones": emptyConn()})
	}

	// Parse states filter (OPEN, CLOSED, or all)
	stateFilter := "all"
	if raw, ok := req.Variables["states"]; ok {
		if arr, ok := raw.([]any); ok && len(arr) > 0 {
			stateFilter = strings.ToLower(fmt.Sprintf("%v", arr[0]))
		} else if s, ok := raw.(string); ok {
			stateFilter = strings.ToLower(s)
		}
	}
	milestones, _, _ := s.Svc.ListMilestones(ctx, fullName, stateFilter, "", "", 1, 0)
	nodes := make([]any, len(milestones))
	for i, m := range milestones {
		nodes[i] = s.milestoneGQL(m)
	}
	return wrap("repository", map[string]any{"milestones": gqlConn(nodes)})
}
