package graphql

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gh-server/internal/db"
)

// latestByAuthor returns the latest review per author from a list of reviews.
// Since reviews are ordered by creation time, the last entry per author wins.
func latestByAuthor(allReviews []db.PullRequestReview) map[string]db.PullRequestReview {
	m := make(map[string]db.PullRequestReview)
	for _, r := range allReviews {
		m[r.AuthorLogin] = r
	}
	return m
}

// latestReviewsFromList derives the latest review per author from a pre-fetched list.
// ownerLogin is used to compute authorAssociation.
func latestReviewsFromList(allReviews []db.PullRequestReview, ownerLogin ...string) map[string]any {
	ownLog := ""
	if len(ownerLogin) > 0 {
		ownLog = ownerLogin[0]
	}
	byAuthor := latestByAuthor(allReviews)
	nodes := make([]any, 0, len(byAuthor))
	for _, r := range byAuthor {
		submittedAt := r.CreatedAt.Format(time.RFC3339)
		if r.SubmittedAt != nil {
			submittedAt = r.SubmittedAt.Format(time.RFC3339)
		}
		assoc := "NONE"
		if r.AuthorLogin != "" && r.AuthorLogin == ownLog {
			assoc = "OWNER"
		}
		nodes = append(nodes, map[string]any{
			"author":            map[string]any{"login": r.AuthorLogin},
			"authorAssociation": assoc,
			"submittedAt":       submittedAt,
			"body":              r.Body,
			"state":             r.State,
		})
	}
	return gqlConn(nodes)
}

// reviewDecisionFromList computes the review decision from a pre-fetched list of all reviews.
func reviewDecisionFromList(allReviews []db.PullRequestReview) string {
	byAuthor := latestByAuthor(allReviews)
	if len(byAuthor) == 0 {
		return ""
	}
	hasApproval := false
	hasChangesRequested := false
	for _, r := range byAuthor {
		switch r.State {
		case db.ReviewApproved:
			hasApproval = true
		case db.ReviewChangesRequested:
			hasChangesRequested = true
		}
	}
	if hasChangesRequested {
		return db.ReviewChangesRequested
	}
	if hasApproval {
		return db.ReviewApproved
	}
	return "REVIEW_REQUIRED"
}

// autoMergeRequestGQL returns the autoMergeRequest field for a PR.
func autoMergeRequestGQL(p db.PullRequest) any {
	if !p.AutoMerge {
		return nil
	}
	var authorEmail any
	if p.AutoMergeAuthorEmail != "" {
		authorEmail = p.AutoMergeAuthorEmail
	}
	var commitBody any
	if body := strings.TrimSpace(string(p.AutoMergeCommitBody)); body != "" {
		commitBody = body
	}
	var commitHeadline any
	if headline := strings.TrimSpace(p.AutoMergeCommitHeadline); headline != "" {
		commitHeadline = headline
	}
	enabledBy := p.AutoMergeEnabledByLogin
	if enabledBy == "" {
		enabledBy = p.Author.Login
	}
	return map[string]any{
		"enabledAt":      p.UpdatedAt.Format(time.RFC3339),
		"mergeMethod":    p.AutoMergeMethod,
		"enabledBy":      map[string]any{"login": enabledBy},
		"authorEmail":    authorEmail,
		"commitBody":     commitBody,
		"commitHeadline": commitHeadline,
	}
}

// mergeStateStatus returns the mergeStateStatus for a PR.
func mergeStateStatus(p db.PullRequest) string {
	if p.Merged {
		return "UNKNOWN"
	}
	if p.State == db.StateClosed {
		return "UNKNOWN"
	}
	if p.Draft {
		return "DRAFT"
	}
	return "CLEAN"
}

// mergedByGQL returns the mergedBy field for a PR.
func mergedByGQL(p db.PullRequest) any {
	if !p.Merged || p.MergedByLogin == "" {
		return nil
	}
	return map[string]any{
		"login": p.MergedByLogin,
	}
}

// mergeCommitGQL returns the mergeCommit field for a PR.
func mergeCommitGQL(p db.PullRequest) any {
	if p.MergeCommitSHA == "" {
		return nil
	}
	return map[string]any{
		"oid": p.MergeCommitSHA,
	}
}

// jobToCheckNode converts a workflow run job to a GraphQL CheckRun node.
func jobToCheckNode(job db.WorkflowRunJob, wf db.Workflow, run db.WorkflowRun, htmlBaseURL, repoFullName string) map[string]any {
	status := strings.ToUpper(job.Status)
	conclusion := strings.ToUpper(job.Conclusion)
	if status == "" {
		status = "COMPLETED"
	}
	if conclusion == "" {
		conclusion = "SUCCESS"
	}
	return map[string]any{
		"__typename":  "CheckRun",
		"name":        job.Name,
		"status":      status,
		"conclusion":  conclusion,
		"startedAt":   job.StartedAt.Format(time.RFC3339),
		"completedAt": job.CompletedAt.Format(time.RFC3339),
		"detailsUrl":  fmt.Sprintf("%s/%s/actions/runs/%d", htmlBaseURL, repoFullName, run.ID),
		"isRequired":  false,
		"checkSuite": map[string]any{
			"workflowRun": map[string]any{
				"workflow": map[string]any{
					"name": wf.Name,
				},
			},
		},
	}
}

// countChecksByState builds count-by-conclusion summaries from check nodes.
func countChecksByState(checkNodes []any) []any {
	counts := map[string]int{}
	for _, node := range checkNodes {
		n, ok := node.(map[string]any)
		if !ok {
			continue
		}
		conclusion, _ := n["conclusion"].(string)
		if conclusion == "" {
			conclusion = "UNKNOWN"
		}
		counts[conclusion]++
	}
	var result []any
	for state, count := range counts {
		result = append(result, map[string]any{"state": state, "count": count})
	}
	return result
}

// statusCheckRollupGQL builds the statusCheckRollup from real workflow runs.
func (s *Server) statusCheckRollupGQL(ctx context.Context, p db.PullRequest) any {
	if p.HeadSHA == "" {
		return nil
	}
	runs, _ := s.Svc.ListWorkflowRunsBySHA(ctx, p.RepositoryID, p.HeadSHA)
	if len(runs) == 0 {
		return nil
	}

	var checkNodes []any
	for _, run := range runs {
		wf, _ := s.Svc.GetWorkflowByID(ctx, run.WorkflowID)
		jobs, _ := s.Svc.ListWorkflowRunJobsByRun(ctx, run.ID)
		for _, job := range jobs {
			checkNodes = append(checkNodes, jobToCheckNode(job, wf, run, s.Svc.HTMLBaseURL(), p.Repository.FullName))
		}
	}

	contexts := map[string]any{
		"checkRunCount":              len(checkNodes),
		"checkRunCountsByState":      countChecksByState(checkNodes),
		"statusContextCount":         0,
		"statusContextCountsByState": []any{},
		"nodes":                      checkNodes,
		"pageInfo":                   gqlPageInfo(),
	}

	return map[string]any{
		"nodes": []any{
			map[string]any{
				"commit": map[string]any{
					"oid": p.HeadSHA,
					"statusCheckRollup": map[string]any{
						"contexts": contexts,
					},
				},
			},
		},
	}
}

// closingIssuesRegex matches "fixes #N", "closes #N", "resolves #N" patterns.
var closingIssuesRegex = regexp.MustCompile(`(?i)(?:fix(?:es)?|close[sd]?|resolve[sd]?)\s+#(\d+)`)

// closingIssuesGQL scans the PR body for closing issue references and returns them as a connection.
func (s *Server) closingIssuesGQL(ctx context.Context, p db.PullRequest) map[string]any {
	if p.Body == "" {
		return emptyConn()
	}
	matches := closingIssuesRegex.FindAllStringSubmatch(string(p.Body), -1)
	if len(matches) == 0 {
		return emptyConn()
	}
	seen := map[int]bool{}
	var nodes []any
	for _, m := range matches {
		num, err := strconv.Atoi(m[1])
		if err != nil || seen[num] {
			continue
		}
		seen[num] = true
		issue, err := s.Svc.GetIssue(ctx, p.Repository.FullName, num)
		if err != nil {
			continue
		}
		// Use slim shape with proper repository fields that CLI expects:
		// {id, number, url, repository: {id, name, owner: {id, login}}}
		nodes = append(nodes, map[string]any{
			"id":     gqlID("Issue", issue.ID),
			"number": issue.Number,
			"url":    fmt.Sprintf("%s/%s/issues/%d", s.Svc.HTMLBaseURL(), issue.Repository.FullName, issue.Number),
			"repository": map[string]any{
				"id":   gqlID("Repository", issue.Repository.ID),
				"name": issue.Repository.Name,
				"owner": map[string]any{
					"id":    gqlID("User", issue.Repository.Owner.ID),
					"login": issue.Repository.Owner.Login,
				},
			},
		})
	}
	return gqlConn(nodes)
}

// prMergeable computes the mergeable status of a PR from actual git state.
func (s *Server) prMergeable(ctx context.Context, p db.PullRequest) string {
	if p.Merged || p.State != db.StateOpen {
		return "UNKNOWN"
	}
	if p.Repository.FullName == "" {
		return "UNKNOWN"
	}
	return s.Svc.CanMergePR(ctx, p.Repository.FullName, p.BaseRef, p.HeadRef)
}

func repoGQLLite(rep db.Repository) map[string]any {
	return map[string]any{
		"id":            gqlID("Repository", rep.ID),
		"name":          rep.Name,
		"nameWithOwner": rep.FullName,
		"owner":         map[string]any{"login": rep.Owner.Login},
	}
}
