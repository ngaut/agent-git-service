package graphql

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/service"
)

// doRequestReviews handles requestReviews and requestReviewsByLogin mutations.
func (s *Server) doRequestReviews(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	prID := strFrom(inp, "pullRequestId")

	// Determine which mutation name to use in the response
	mutationName := "requestReviews"
	if strings.Contains(req.Query, "requestReviewsByLogin") {
		mutationName = "requestReviewsByLogin"
	}

	if dbID := parseNodeID(prID, "PullRequest"); dbID > 0 {
		var logins []string
		var teams []string

		// Standard GraphQL identifiers
		for _, uid := range extractStringSlice(inp, "userIds") {
			if login := s.resolveUserLoginByNodeID(ctx, uid); login != "" {
				logins = append(logins, login)
			}
		}
		for _, tid := range extractStringSlice(inp, "teamIds") {
			if strings.HasPrefix(tid, "Team_") {
				teams = append(teams, strings.TrimPrefix(tid, "Team_"))
			}
		}

		// Legacy/non-standard identifiers (used by requestReviewsByLogin)
		logins = append(logins, extractStringSlice(inp, "userLogins")...)
		logins = append(logins, extractStringSlice(inp, "botLogins")...)
		teams = append(teams, extractStringSlice(inp, "teamSlugs")...)

		for _, login := range logins {
			if login != "" {
				if err := s.Svc.RequestReview(ctx, dbID, login); err != nil {
					return errResp(err.Error())
				}
			}
		}
		for _, slug := range teams {
			if slug != "" {
				if err := s.Svc.RequestTeamReview(ctx, dbID, slug); err != nil {
					return errResp(err.Error())
				}
			}
		}

		if pr, err := s.Svc.ReloadPR(ctx, dbID); err == nil {
			return wrap(mutationName, map[string]any{
				"clientMutationId": "",
				"pullRequest":      s.prGQL(ctx, pr),
			})
		}
	}
	return wrap(mutationName, map[string]any{
		"clientMutationId": "",
		"pullRequest":      map[string]any{"id": prID, "reviewRequests": emptyConn()},
	})
}

// doAddPRReview handles addPullRequestReview mutation.
func (s *Server) doAddPRReview(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	event := strFrom(inp, "event") // APPROVE, REQUEST_CHANGES, COMMENT
	body := strFrom(inp, "body")
	prID := strFrom(inp, "pullRequestId")
	dbID := parseNodeID(prID, "PullRequest")
	if dbID == 0 {
		return errResp("invalid pull request ID")
	}

	pr, err := s.Svc.GetPRByID(ctx, dbID)
	if err != nil {
		return errResp("pull request not found")
	}

	u, err := s.Svc.GetCurrentUser(ctx)
	if err != nil {
		return errResp("authentication required")
	}

	assoc := "NONE"
	if u.Login == pr.Repository.Owner.Login {
		assoc = "OWNER"
	}

	review, err := s.Svc.AddPRReview(ctx, dbID, u.Login, event, body, pr.HeadSHA)
	if err != nil {
		return errResp(err.Error())
	}

	submittedAt := review.CreatedAt.Format(time.RFC3339)
	return wrap("addPullRequestReview", map[string]any{
		"pullRequestReview": map[string]any{
			"id":                gqlID("PRReview", review.ID),
			"body":              body,
			"state":             event,
			"submittedAt":       submittedAt,
			"author":            map[string]any{"login": u.Login},
			"authorAssociation": assoc,
			"commit":            map[string]any{"oid": review.CommitSHA},
			"reactionGroups":    defaultReactionGroups(),
		},
	})
}

// doSetAutoMerge handles enablePullRequestAutoMerge and disablePullRequestAutoMerge mutations.
func (s *Server) doSetAutoMerge(ctx context.Context, req gqlRequest, enable bool) map[string]any {
	inp := inputMap(req)
	prID := strFrom(inp, "pullRequestId")
	mergeMethod := strFrom(inp, "mergeMethod")
	if enable && mergeMethod == "" {
		mergeMethod = "MERGE"
	}

	mutationName := "disablePullRequestAutoMerge"
	if enable {
		mutationName = "enablePullRequestAutoMerge"
	}

	dbID := parseNodeID(prID, "PullRequest")
	if dbID == 0 {
		return errResp("invalid pull request ID")
	}

	pr, err := s.Svc.GetPRByID(ctx, dbID)
	if err != nil {
		return errResp("pull request not found")
	}
	pr, err = s.Svc.SetPRAutoMerge(ctx, dbID, service.SetPRAutoMergeInput{
		Enabled:         enable,
		MergeMethod:     mergeMethod,
		CommitHeadline:  strFrom(inp, "commitHeadline"),
		CommitBody:      strFrom(inp, "commitBody"),
		AuthorEmail:     strFrom(inp, "authorEmail"),
		ExpectedHeadSHA: strFrom(inp, "expectedHeadOid"),
	})
	if err != nil {
		return errResp(err.Error())
	}

	return wrap(mutationName, map[string]any{
		"clientMutationId": "",
		"pullRequest":      s.prGQL(ctx, pr),
	})
}

// doRevertPR handles revertPullRequest mutation.
// Creates a new PR that reverts the changes of a merged PR.
func (s *Server) doRevertPR(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	prID := strFrom(inp, "pullRequestId")
	title := strFrom(inp, "title")
	body := strFrom(inp, "body")
	draft, _ := inp["draft"].(bool)

	if dbID := parseNodeID(prID, "PullRequest"); dbID > 0 {
		if pr, err := s.Svc.GetPRByID(ctx, dbID); err == nil {
			if !pr.Merged || pr.MergeCommitSHA == "" {
				return errResp("pull request is not merged")
			}

			repoFullName := pr.Repository.FullName
			revertBranch := fmt.Sprintf("revert-%d-%s", pr.Number, pr.HeadRef)
			if title == "" {
				title = fmt.Sprintf("Revert \"%s\"", pr.Title)
			}
			if body == "" {
				body = fmt.Sprintf("Reverts %s#%d", repoFullName, pr.Number)
			}

			// Perform git revert
			_, gitErr := s.Svc.RevertPRMerge(ctx, repoFullName, pr.BaseRef, pr.MergeCommitSHA, revertBranch)
			if gitErr != nil {
				return errResp(fmt.Sprintf("git revert failed: %v", gitErr))
			}

			u, err := s.Svc.GetCurrentUser(ctx)
			if err != nil {
				return errResp("authentication required")
			}
			revertPR, err := s.Svc.CreatePR(ctx, service.CreatePRInput{
				RepoFullName: repoFullName,
				Title:        title,
				Body:         body,
				HeadRef:      revertBranch,
				BaseRef:      pr.BaseRef,
				Draft:        draft,
				AuthorLogin:  u.Login,
			})
			if err != nil {
				return errResp(fmt.Sprintf("failed to create revert PR: %v", err))
			}

			return wrap("revertPullRequest", map[string]any{
				"pullRequest":       s.prGQL(ctx, pr),
				"revertPullRequest": s.prGQL(ctx, revertPR),
			})
		}
	}
	return errResp("pull request not found")
}

// doUpdatePRBranch handles updatePullRequestBranch mutation.
// This merges the base branch into the PR's head branch (or rebases).
func (s *Server) doUpdatePRBranch(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	prID := strFrom(inp, "pullRequestId")
	updateMethod := strFrom(inp, "updateMethod") // MERGE or REBASE

	if dbID := parseNodeID(prID, "PullRequest"); dbID > 0 {
		if pr, err := s.Svc.GetPRByID(ctx, dbID); err == nil {
			currentUser, err := s.Svc.GetCurrentUser(ctx)
			if err != nil {
				return errResp("authentication required")
			}
			canUpdate, err := s.Svc.CanCreatePR(ctx, pr.RepositoryID, currentUser.ID)
			if err != nil {
				return errResp(err.Error())
			}
			if !canUpdate {
				return errResp("pull request not found")
			}
			repoFullName := pr.Repository.FullName
			// If cross-repo, we need to work with the head repo
			if pr.HeadRepositoryID != pr.RepositoryID {
				repoFullName = pr.HeadRepository.FullName
			}
			sha, err := s.Svc.UpdatePRBranch(ctx, gitstore.UpdatePRBranchOptions{
				FullName:     repoFullName,
				BaseBranch:   pr.BaseRef,
				HeadBranch:   pr.HeadRef,
				Committer:    currentUser.Login,
				Email:        currentUser.Email,
				UpdateMethod: updateMethod,
			})
			if err != nil {
				return errResp(err.Error())
			}

			// Update the head SHA in the DB
			if err := s.Svc.UpdatePRFields(ctx, dbID, map[string]any{"head_sha": sha}); err != nil {
				return errResp(err.Error())
			}

			return wrap("updatePullRequestBranch", map[string]any{
				"pullRequest": map[string]any{
					"id": prID,
				},
			})
		}
	}
	return errResp("pull request not found")
}

// doResolveReviewThread handles resolveReviewThread and unresolveReviewThread mutations.
func (s *Server) doResolveReviewThread(ctx context.Context, req gqlRequest, resolve bool) map[string]any {
	inp := inputMap(req)
	threadID := strFrom(inp, "threadId") // Usually a PullRequestReviewThread or PRReviewComment nodeID

	name := "resolveReviewThread"
	if !resolve {
		name = "unresolveReviewThread"
	}

	// GitHub sometimes prefixes thread IDs with "PullRequestReviewThread" or just "PRReviewComment"
	// parseNodeID strict-checks the type, so we'll just extract the ID manually if needed or try both.
	dbID := parseNodeID(threadID, "PullRequestReviewThread")
	if dbID == 0 {
		dbID = parseNodeID(threadID, "PRReviewComment")
	}

	if dbID > 0 {
		var err error
		if resolve {
			err = s.Svc.ResolvePRReviewThread(ctx, dbID)
		} else {
			err = s.Svc.UnresolvePRReviewThread(ctx, dbID)
		}
		if err != nil {
			return errResp(err.Error())
		}
		return wrap(name, map[string]any{
			"thread": map[string]any{
				"id":                 threadID,
				"isResolved":         resolve,
				"viewerCanResolve":   !resolve,
				"viewerCanUnresolve": resolve,
			},
		})
	}
	return errResp("invalid thread ID")
}
