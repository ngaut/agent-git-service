package graphql

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gh-server/internal/db"
)

// prGQL converts db.PullRequest to GraphQL shape. REST counterpart: rest/transform.PR()
func (s *Server) prGQL(ctx context.Context, p db.PullRequest, queries ...string) map[string]any {
	state := strings.ToUpper(p.State)
	if p.Merged {
		state = "MERGED"
	}
	closedAt := ""
	if p.ClosedAt != nil {
		closedAt = p.ClosedAt.Format(time.RFC3339)
	}
	mergedAt := ""
	if p.MergedAt != nil {
		mergedAt = p.MergedAt.Format(time.RFC3339)
	}

	q := ""
	if len(queries) > 0 {
		q = queries[0]
	}

	// Compute diff stats lazily
	additions, deletions, changedFiles := 0, 0, 0
	if queryHasAny(q, "additions", "deletions", "changedFiles") {
		additions, deletions, changedFiles = s.Svc.PRDiffStats(ctx, p.Repository.FullName, p.BaseSHA, p.HeadSHA)
	}

	var milestone any
	if p.Milestone != nil {
		milestone = s.milestoneGQL(*p.Milestone)
	}

	// Fetch all reviews lazily
	var allReviews []db.PullRequestReview
	if queryHasAny(q, "reviews", "latestReviews", "reviewDecision") {
		allReviews, _ = s.Svc.ListPRReviews(ctx, p.ID)
	}

	// assignees and assignedActors render the same underlying list; compute
	// once so each per-login GetUser call runs at most once per PR.
	assignees := s.assigneeLoginsToGQL(ctx, p.AssigneeLogins)

	return map[string]any{
		"id":                      gqlID("PullRequest", p.ID),
		"number":                  p.Number,
		"title":                   p.Title,
		"body":                    p.Body,
		"state":                   state,
		"isDraft":                 p.Draft,
		"merged":                  p.Merged,
		"url":                     fmt.Sprintf("%s/%s/pull/%d", s.Svc.HTMLBaseURL(), p.Repository.FullName, p.Number),
		"author":                  s.authorGQL(p.Author),
		"headRefName":             p.HeadRef,
		"headRefOid":              p.HeadSHA,
		"baseRefName":             p.BaseRef,
		"baseRefOid":              p.BaseSHA,
		"headRepository":          repoGQLLite(p.HeadRepository),
		"headRepositoryOwner":     map[string]any{"id": gqlID("User", p.HeadRepository.Owner.ID), "login": p.HeadRepository.Owner.Login, "name": p.HeadRepository.Owner.Name},
		"isCrossRepository":       p.RepositoryID != p.HeadRepositoryID,
		"additions":               additions,
		"deletions":               deletions,
		"changedFiles":            changedFiles,
		"mergeStateStatus":        mergeStateStatus(p),
		"mergedBy":                mergedByGQL(p),
		"mergeCommit":             mergeCommitGQL(p),
		"maintainerCanModify":     p.MaintainerCanModify,
		"closingIssuesReferences": s.closingIssuesGQL(ctx, p),
		"labels":                  s.labelsToGQL(ctx, p.Labels),
		"assignees":               assignees,
		"assignedActors":          assignees,
		"reviewRequests":          s.reviewRequestsGQL(ctx, p.ID),
		"reviews":                 s.reviewsFromList(allReviews, p.Repository.Owner.Login),
		"latestReviews":           latestReviewsFromList(allReviews, p.Repository.Owner.Login),
		"reviewDecision":          reviewDecisionFromList(allReviews),
		"comments": func() any {
			if queryHasAny(q, "comments") {
				return s.prCommentsGQL(ctx, p)
			}
			return emptyConn()
		}(),
		"commits": func() any {
			if queryHasAny(q, "commits") {
				return s.prCommitsGQL(ctx, p)
			}
			return emptyConn()
		}(),
		"files": func() any {
			if queryHasAny(q, "files") {
				return s.prFilesGQL(ctx, p)
			}
			return emptyConn()
		}(),
		"projectCards": emptyConn(),
		"projectItems": func() any {
			if queryHasAny(q, "projectItems") {
				return s.projectItemsGQL(ctx, gqlID("PullRequest", p.ID), p.Repository.Owner.Login)
			}
			return emptyConn()
		}(),
		"milestone":      milestone,
		"reactionGroups": defaultReactionGroups(),
		"statusCheckRollup": func() any {
			if queryHasAny(q, "statusCheckRollup") {
				return s.statusCheckRollupGQL(ctx, p)
			}
			return nil
		}(),
		"autoMergeRequest": autoMergeRequestGQL(p),
		"potentialMergeCommit": func() any {
			if !queryHasAny(q, "potentialMergeCommit") {
				return nil
			}
			sha, err := s.Svc.SimulatePRMerge(ctx, p.Repository.FullName, p.BaseSHA, p.HeadSHA)
			if err != nil || sha == "" {
				return nil
			}
			return map[string]any{"oid": sha}
		}(),
		"fullDatabaseId": fmt.Sprintf("%d", p.ID),
		"mergeable": func() any {
			if queryHasAny(q, "mergeable") {
				return s.prMergeable(ctx, p)
			}
			return "UNKNOWN"
		}(),
		"viewerMergeHeadlineText": fmt.Sprintf("Merge pull request #%d from %s", p.Number, p.HeadRef),
		"viewerMergeBodyText":     p.Title,
		"repository":              map[string]any{"nameWithOwner": p.Repository.FullName},
		"createdAt":               p.CreatedAt.Format(time.RFC3339),
		"updatedAt":               p.UpdatedAt.Format(time.RFC3339),
		"closedAt":                closedAt,
		"mergedAt":                mergedAt,
		"__typename":              "PullRequest",
	}
}

func (s *Server) prCommentsGQL(ctx context.Context, p db.PullRequest) map[string]any {
	return s.issueCommentsGQL(ctx, p.RepositoryID, p.Number)
}

// prCommitsGQL returns PR commits from the actual git repo.
func (s *Server) prCommitsGQL(ctx context.Context, p db.PullRequest) map[string]any {
	if p.BaseSHA == "" || p.HeadSHA == "" || p.Repository.FullName == "" {
		return emptyConn()
	}
	commits, err := s.Svc.ListPRCommits(ctx, p.Repository.FullName, p.Number)
	if err != nil || len(commits) == 0 {
		return emptyConn()
	}
	var nodes []any
	for _, c := range commits {
		cm, _ := c["commit"].(map[string]any)
		if cm == nil {
			continue
		}
		author, _ := cm["author"].(map[string]any)
		if author == nil {
			author = map[string]any{}
		}
		msg, _ := cm["message"].(string)
		// Split message into headline and body
		headline := msg
		body := ""
		if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
			headline = msg[:idx]
			body = strings.TrimSpace(msg[idx+1:])
		}
		nodes = append(nodes, map[string]any{
			"commit": map[string]any{
				"oid":             c["sha"],
				"messageHeadline": headline,
				"messageBody":     body,
				"committedDate":   author["date"],
				"authoredDate":    author["date"],
				"authors": map[string]any{
					"nodes": []any{
						map[string]any{
							"name":  author["name"],
							"email": author["email"],
							"user": map[string]any{
								"login": author["name"],
							},
						},
					},
				},
			},
			"url": fmt.Sprintf("%s/%s/commit/%s", s.Svc.HTMLBaseURL(), p.Repository.FullName, c["sha"]),
		})
	}
	return gqlConn(nodes)
}

// prFilesGQL returns PR changed files from the actual git repo.
func (s *Server) prFilesGQL(ctx context.Context, p db.PullRequest) map[string]any {
	if p.BaseSHA == "" || p.HeadSHA == "" || p.Repository.FullName == "" {
		return emptyConn()
	}
	files, err := s.Svc.ListPRFiles(ctx, p.Repository.FullName, p.Number)
	if err != nil || len(files) == 0 {
		return emptyConn()
	}
	nodes := make([]any, len(files))
	for i, f := range files {
		nodes[i] = map[string]any{
			"path":      f["filename"],
			"additions": f["additions"],
			"deletions": f["deletions"],
		}
	}
	return gqlConn(nodes)
}

// reviewRequestsGQL returns the reviewRequests connection for a PR.
func (s *Server) reviewRequestsGQL(ctx context.Context, prID uint) map[string]any {
	reqs, _ := s.Svc.ListReviewRequests(ctx, prID)
	nodes := make([]any, len(reqs))
	for i, r := range reqs {
		if r.TeamSlug != "" {
			nodes[i] = map[string]any{
				"requestedReviewer": map[string]any{
					"__typename": "Team",
					"slug":       r.TeamSlug,
					"name":       r.TeamSlug,
				},
			}
		} else {
			nodes[i] = map[string]any{
				"requestedReviewer": map[string]any{
					"__typename": "User",
					"login":      r.Login,
					"name":       r.Login,
				},
			}
		}
	}
	return gqlConn(nodes)
}

// reviewsFromList converts a pre-fetched list of all reviews into a GraphQL connection.
// ownerLogin is used to compute authorAssociation dynamically.
func (s *Server) reviewsFromList(reviews []db.PullRequestReview, ownerLogin ...string) map[string]any {
	ownLog := ""
	if len(ownerLogin) > 0 {
		ownLog = ownerLogin[0]
	}
	nodes := make([]any, len(reviews))
	for i, r := range reviews {
		submittedAt := r.CreatedAt.Format(time.RFC3339)
		if r.SubmittedAt != nil {
			submittedAt = r.SubmittedAt.Format(time.RFC3339)
		}
		assoc := "NONE"
		if r.AuthorLogin != "" && r.AuthorLogin == ownLog {
			assoc = "OWNER"
		}
		nodes[i] = map[string]any{
			"id":                gqlID("PRReview", r.ID),
			"author":            map[string]any{"login": r.AuthorLogin},
			"authorAssociation": assoc,
			"submittedAt":       submittedAt,
			"body":              r.Body,
			"state":             r.State,
			"commit":            map[string]any{"oid": r.CommitSHA},
			"reactionGroups":    defaultReactionGroups(),
			"url":               fmt.Sprintf("%s/review/%d", s.Svc.HTMLBaseURL(), r.ID),
		}
	}
	return gqlConn(nodes)
}
