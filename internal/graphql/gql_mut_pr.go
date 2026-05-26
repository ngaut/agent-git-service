package graphql

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

// Regex patterns for extracting inline mutation arguments from GraphQL query strings.
var (
	reTitleInline   = regexp.MustCompile(`title\s*:\s*"([^"]+)"`)
	reBodyInline    = regexp.MustCompile(`body\s*:\s*"([^"]+)"`)
	reHeadRefInline = regexp.MustCompile(`headRefName\s*:\s*"([^"]+)"`)
	reBaseRefInline = regexp.MustCompile(`baseRefName\s*:\s*"([^"]+)"`)
)

func (s *Server) doCreatePR(ctx context.Context, req gqlRequest) map[string]any {

	inp := inputMap(req)

	// Fallback for `gh api` calls that inline variables or pass them at the root
	repoID := strFrom(inp, "repositoryId")
	if repoID == "" {
		repoID = strFrom(req.Variables, "repositoryId")
	}
	repoID = strings.Trim(repoID, "\"")

	headRepoID := strFrom(inp, "headRepositoryId")
	if headRepoID == "" {
		headRepoID = strFrom(req.Variables, "headRepositoryId")
	}
	headRepoID = strings.Trim(headRepoID, "\"")

	title := strFrom(inp, "title")
	if title == "" {
		if m := reTitleInline.FindStringSubmatch(req.Query); len(m) > 1 {
			title = m[1]
		}
	}
	body := strFrom(inp, "body")
	if body == "" {
		if m := reBodyInline.FindStringSubmatch(req.Query); len(m) > 1 {
			body = m[1]
		}
	}
	headRef := strFrom(inp, "headRefName")
	if headRef == "" {
		if m := reHeadRefInline.FindStringSubmatch(req.Query); len(m) > 1 {
			headRef = m[1]
		}
	}
	baseRef := strFrom(inp, "baseRefName")
	if baseRef == "" {
		if m := reBaseRefInline.FindStringSubmatch(req.Query); len(m) > 1 {
			baseRef = m[1]
		}
	}

	fullName := s.resolveRepoByNodeID(ctx, repoID)
	if _, err := s.Svc.GetRepo(ctx, fullName); err != nil {
		return errResp("repository not found")
	}
	// headRepositoryId is set for fork PRs — the head branch belongs to the fork repo
	headRepoFullName := fullName // default: same repo
	if headRepoID != "" && headRepoID != repoID {
		if hfn := s.resolveRepoByNodeID(ctx, headRepoID); hfn != "" {
			headRepoFullName = hfn
		}
	}

	// Parse cross-repo headRef format: "user:branch" or "org:branch"
	if parts := strings.SplitN(headRef, ":", 2); len(parts) == 2 && headRepoFullName == fullName {
		headRef = parts[1]
		if resolved := s.Svc.ResolveForkRepo(ctx, fullName, parts[0]); resolved != "" {
			headRepoFullName = resolved
		}
	}

	maintainerCanModify := true // default to true like GitHub
	if v, ok := inp["maintainerCanModify"].(bool); ok {
		maintainerCanModify = v
	}

	u, err := s.Svc.GetCurrentUser(ctx)
	if err != nil {
		return errResp("authentication required")
	}
	pr, err := s.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName:        fullName,
		HeadRepoFullName:    headRepoFullName,
		Title:               title,
		Body:                body,
		HeadRef:             headRef,
		BaseRef:             baseRef,
		MaintainerCanModify: maintainerCanModify,
		AuthorLogin:         u.Login,
	})
	if err != nil {
		return errResp(err.Error())
	}
	// Attach labels and assignees, then reload with associations
	if err := s.Svc.AttachLabelsAndAssignees(ctx, nil, &pr.ID, extractStringSlice(inp, "labelIds"), extractStringSlice(inp, "assigneeIds")); err != nil {
		return errResp(err.Error())
	}
	if reloaded, err := s.Svc.ReloadPR(ctx, pr.ID); err == nil {
		pr = reloaded
	}
	return wrap("createPullRequest", map[string]any{"pullRequest": s.prGQL(ctx, pr)})
}

func (s *Server) doMergePR(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	prID := strFrom(inp, "pullRequestId")
	if prID == "" {
		return errResp("invalid pull request ID")
	}
	mergeMethod := strings.ToLower(strFrom(inp, "mergeMethod")) // MERGE, SQUASH, REBASE
	commitHeadline := strFrom(inp, "commitHeadline")
	commitBody := strFrom(inp, "commitBody")

	dbID := parseNodeID(prID, "PullRequest")
	if dbID == 0 {
		return errResp("invalid pull request ID")
	}

	var mergeErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("doMergePR: panicked", "panic", r)
				debug.PrintStack()
				mergeErr = fmt.Errorf("merge panicked: %v", r)
			}
		}()
		// Build commit message from headline and body if provided
		commitMsg := commitHeadline
		if commitBody != "" {
			if commitMsg != "" {
				commitMsg += "\n\n" + commitBody
			} else {
				commitMsg = commitBody
			}
		}
		mergeErr = s.Svc.MergePRByID(ctx, dbID, mergeMethod, commitMsg)
	}()
	if mergeErr != nil {
		return errResp(mergeErr.Error())
	}
	return wrap("mergePullRequest", map[string]any{
		"clientMutationId": "",
		"pullRequest":      map[string]any{"merged": true, "state": db.StateMerged},
	})
}

// doSetPRState handles closePullRequest and reopenPullRequest mutations.
func (s *Server) doSetPRState(ctx context.Context, req gqlRequest, state, mutName string) map[string]any {
	inp := inputMap(req)
	prID := strFrom(inp, "pullRequestId")
	if dbID := parseNodeID(prID, "PullRequest"); dbID > 0 {
		st := state
		if err := s.Svc.UpdatePRByID(ctx, dbID, &st, nil); err != nil {
			return errResp(err.Error())
		}
		if pr, err := s.Svc.ReloadPR(ctx, dbID); err == nil {
			return wrap(mutName, map[string]any{"pullRequest": s.prGQL(ctx, pr)})
		}
	}
	return wrap(mutName, map[string]any{"pullRequest": map[string]any{"state": strings.ToUpper(state)}})
}

func (s *Server) doUpdatePR(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	prID := strFrom(inp, "pullRequestId")
	if prID == "" {
		prID = strFrom(inp, "id")
	}
	if dbID := parseNodeID(prID, "PullRequest"); dbID > 0 {
		// Update title, body, baseRefName via service
		updates := make(map[string]any)
		if title := strFrom(inp, "title"); title != "" {
			updates["title"] = title
		}
		if body, ok := inp["body"].(string); ok {
			updates["body"] = body
		}
		if baseRef := strFrom(inp, "baseRefName"); baseRef != "" {
			updates["base_ref"] = baseRef
		}
		if len(updates) > 0 {
			if err := s.Svc.UpdatePRFields(ctx, dbID, updates); err != nil {
				return errResp(err.Error())
			}
		}
		// Attach labels and assignees
		if err := s.Svc.AttachLabelsAndAssignees(ctx, nil, &dbID, extractStringSlice(inp, "labelIds"), extractStringSlice(inp, "assigneeIds")); err != nil {
			return errResp(err.Error())
		}
		// Update milestone only when explicitly provided (supports clearing with null).
		if raw, ok := inp["milestoneId"]; ok {
			switch v := raw.(type) {
			case nil:
				if err := s.Svc.SetPRMilestone(ctx, dbID, nil); err != nil {
					return errResp(err.Error())
				}
			case string:
				if v == "" {
					return errResp("invalid milestone ID")
				}
				if dbMsID := parseNodeID(v, "Milestone"); dbMsID > 0 {
					if err := s.Svc.SetPRMilestone(ctx, dbID, &dbMsID); err != nil {
						return errResp(err.Error())
					}
				} else {
					return errResp("invalid milestone ID")
				}
			default:
				return errResp("invalid milestone ID")
			}
		}
		// Reload and return full PR
		if pr, err := s.Svc.ReloadPR(ctx, dbID); err == nil {
			return wrap("updatePullRequest", map[string]any{"pullRequest": s.prGQL(ctx, pr)})
		}
	}
	return wrap("updatePullRequest", map[string]any{"pullRequest": map[string]any{"id": prID}})
}

// doSetPRDraft handles markPullRequestReadyForReview and convertPullRequestToDraft mutations.
func (s *Server) doSetPRDraft(ctx context.Context, req gqlRequest, draft bool, mutName string) map[string]any {
	inp := inputMap(req)
	prID := strFrom(inp, "pullRequestId")
	if dbID := parseNodeID(prID, "PullRequest"); dbID > 0 {
		d := draft
		if err := s.Svc.UpdatePRByID(ctx, dbID, nil, &d); err != nil {
			return errResp(err.Error())
		}
	}
	return wrap(mutName, map[string]any{
		"pullRequest": map[string]any{"isDraft": draft},
	})
}
