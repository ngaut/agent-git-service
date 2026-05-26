package graphql

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
)

// issueGQL converts db.Issue to GraphQL shape. REST counterpart: rest/transform.Issue()
func (s *Server) issueGQL(ctx context.Context, i db.Issue, queries ...string) map[string]any {
	q := ""
	if len(queries) > 0 {
		q = queries[0]
	}

	closedAt := ""
	if i.ClosedAt != nil {
		closedAt = i.ClosedAt.Format(time.RFC3339)
	}

	var milestone any
	if i.Milestone != nil {
		milestone = s.milestoneGQL(*i.Milestone)
	}

	// assignees and assignedActors both render the same underlying list;
	// compute once so each per-login GetUser call runs at most once per item.
	assignees := s.assigneeLoginsToGQL(ctx, i.AssigneeLogins)

	return map[string]any{
		"id":             gqlID("Issue", i.ID),
		"number":         i.Number,
		"title":          i.Title,
		"body":           i.Body,
		"state":          strings.ToUpper(i.State),
		"stateReason":    issueStateReason(i),
		"url":            fmt.Sprintf("%s/%s/issues/%d", s.Svc.HTMLBaseURL(), i.Repository.FullName, i.Number),
		"author":         s.authorGQL(i.Author),
		"labels":         s.labelsToGQL(ctx, i.Labels),
		"assignees":      assignees,
		"assignedActors": assignees,
		"comments": func() any {
			if queryHasAny(q, "comments") {
				return s.issueCommentsGQL(ctx, i.RepositoryID, i.Number)
			}
			return emptyConn()
		}(),
		"projectCards": emptyConn(),
		"projectItems": func() any {
			if queryHasAny(q, "projectItems") {
				return s.projectItemsGQL(ctx, gqlID("Issue", i.ID), i.Repository.Owner.Login)
			}
			return emptyConn()
		}(),
		"milestone":        milestone,
		"reactionGroups":   defaultReactionGroups(),
		"isPinned":         i.IsPinned,
		"locked":           i.Locked,
		"activeLockReason": i.ActiveLockReason,
		"closedByPullRequestsReferences": func() any {
			if queryHasAny(q, "closedByPullRequestsReferences", "closedByPullRequests") {
				return s.closedByPRsGQL(ctx, i)
			}
			return emptyConn()
		}(),
		"linkedBranches": func() any {
			if queryHasAny(q, "linkedBranches") {
				return s.linkedBranchesGQL(ctx, i)
			}
			return emptyConn()
		}(),
		"repository": map[string]any{"nameWithOwner": i.Repository.FullName},
		"createdAt":  i.CreatedAt.Format(time.RFC3339),
		"updatedAt":  i.UpdatedAt.Format(time.RFC3339),
		"closedAt":   closedAt,
		"__typename": "Issue",
	}
}

func (s *Server) issueCommentsGQL(ctx context.Context, repoID uint, issueNumber int) map[string]any {
	comments, _ := s.Svc.ListIssueCommentsPinnedFirstByRepoID(ctx, repoID, issueNumber)
	currentUser, err := s.Svc.GetCurrentUser(ctx)
	currentLogin := ""
	if err == nil {
		currentLogin = currentUser.Login
	}
	nodes := make([]any, len(comments))
	for i, c := range comments {
		nodes[i] = s.issueCommentGQL(c, currentLogin)
	}
	return map[string]any{"totalCount": len(nodes), "nodes": nodes, "pageInfo": gqlPageInfo()}
}

func (s *Server) issueCommentGQL(c db.IssueComment, currentLogin string) map[string]any {
	var pinnedAt any
	if c.PinnedAt != nil {
		pinnedAt = c.PinnedAt.Format(time.RFC3339)
	}
	return map[string]any{
		"id":                  gqlID("IssueComment", c.ID),
		"body":                c.Body,
		"author":              map[string]any{"login": c.Author.Login},
		"authorAssociation":   authorAssociation(c.Author, c.Repository),
		"isMinimized":         c.Body == "",
		"minimizedReason":     "",
		"includesCreatedEdit": !c.CreatedAt.Equal(c.UpdatedAt),
		"reactionGroups":      defaultReactionGroups(),
		"isPinned":            c.IsPinned,
		"pinnedAt":            pinnedAt,
		"createdAt":           c.CreatedAt.Format(time.RFC3339),
		"updatedAt":           c.UpdatedAt.Format(time.RFC3339),
		"viewerDidAuthor":     c.Author.Login == currentLogin,
		"url":                 fmt.Sprintf("%s/comment/%d", s.Svc.HTMLBaseURL(), c.ID),
	}
}

// issueStateReason returns the stateReason for an issue, or nil if not set.
func issueStateReason(i db.Issue) any {
	if i.StateReason != "" {
		return i.StateReason
	}
	// Derive from state if not explicitly set
	if strings.ToLower(i.State) == db.StateClosed {
		return db.StateReasonCompleted
	}
	return nil
}

// linkedBranchesGQL returns the linkedBranches connection for an issue.
func (s *Server) linkedBranchesGQL(ctx context.Context, i db.Issue) map[string]any {
	branches, _ := s.Svc.ListLinkedBranches(ctx, i.ID)
	nodes := make([]any, len(branches))
	for idx, b := range branches {
		repoURL := fmt.Sprintf("%s/%s", s.Svc.HTMLBaseURL(), b.Repository.FullName)
		nodes[idx] = map[string]any{
			"id": gqlID("LinkedBranch", b.ID),
			"ref": map[string]any{
				"name": b.BranchName,
				"repository": map[string]any{
					"url": repoURL,
				},
			},
		}
	}
	return gqlConn(nodes)
}

// closedByPRsGQL returns the closedByPullRequestsReferences connection for an issue.
func (s *Server) closedByPRsGQL(ctx context.Context, i db.Issue) map[string]any {
	prs, _ := s.Svc.FindPRsClosingIssue(ctx, i.RepositoryID, i.Number)
	nodes := make([]any, len(prs))
	for idx, pr := range prs {
		nodes[idx] = map[string]any{
			"id":     gqlID("PullRequest", pr.ID),
			"number": pr.Number,
			"url":    fmt.Sprintf("%s/%s/pull/%d", s.Svc.HTMLBaseURL(), pr.Repository.FullName, pr.Number),
			"repository": map[string]any{
				"id":            gqlID("Repository", pr.Repository.ID),
				"name":          pr.Repository.Name,
				"nameWithOwner": pr.Repository.FullName,
				"owner": map[string]any{
					"id":    gqlID("User", pr.Repository.Owner.ID),
					"login": pr.Repository.Owner.Login,
				},
			},
		}
	}
	return gqlConn(nodes)
}

// authorAssociation returns the association of the user with the repository.
func authorAssociation(u db.User, repo db.Repository) string {
	if u.ID == repo.OwnerID {
		return "OWNER"
	}
	// For gh-server, anyone who is not the owner is treated as a contributor or none.
	return "NONE"
}
