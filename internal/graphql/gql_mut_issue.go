package graphql

import (
	"context"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func (s *Server) doCreateIssue(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	u, err := s.Svc.GetCurrentUser(ctx)
	if err != nil {
		return errResp("authentication required")
	}
	repoID := strFrom(inp, "repositoryId")
	fullName := s.resolveRepoByNodeID(ctx, repoID)
	issue, err := s.Svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: fullName,
		Title:        strFrom(inp, "title"),
		Body:         strFrom(inp, "body"),
		AuthorLogin:  u.Login,
	})
	if err != nil {
		return errResp(err.Error())
	}
	// Attach labels and assignees, then reload with associations
	if err := s.Svc.AttachLabelsAndAssignees(ctx, &issue.ID, nil, extractStringSlice(inp, "labelIds"), extractStringSlice(inp, "assigneeIds")); err != nil {
		return errResp(err.Error())
	}
	// Set milestone if provided
	if msID := strFrom(inp, "milestoneId"); msID != "" {
		if dbMsID := parseNodeID(msID, "Milestone"); dbMsID > 0 {
			if err := s.Svc.SetIssueMilestone(ctx, issue.ID, &dbMsID); err != nil {
				return errResp(err.Error())
			}
		}
	}
	issue, err = s.Svc.ReloadIssue(ctx, issue.ID)
	if err != nil {
		return errResp(err.Error())
	}
	return wrap("createIssue", map[string]any{"issue": s.issueGQL(ctx, issue)})
}

// doSetIssueState handles closeIssue and reopenIssue mutations.
func (s *Server) doSetIssueState(ctx context.Context, req gqlRequest, state, mutName string) map[string]any {
	inp := inputMap(req)
	issueID := strFrom(inp, "issueId")
	if dbID := parseNodeID(issueID, "Issue"); dbID > 0 {
		st := state
		var reason *string
		if state == db.StateClosed {
			r := db.StateReasonCompleted
			if customR, ok := inp["stateReason"].(string); ok && customR != "" {
				r = customR
			}
			reason = &r
		}
		if err := s.Svc.UpdateIssueByID(ctx, dbID, &st, reason); err != nil {
			return errResp(err.Error())
		}
		if issue, err := s.Svc.ReloadIssue(ctx, dbID); err == nil {
			return wrap(mutName, map[string]any{"issue": s.issueGQL(ctx, issue)})
		}
	}
	return wrap(mutName, map[string]any{"issue": map[string]any{"state": strings.ToUpper(state)}})
}

func (s *Server) doUpdateIssue(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	issueID := strFrom(inp, "id")
	if dbID := parseNodeID(issueID, "Issue"); dbID > 0 {
		// Update title, body, state via service
		updates := make(map[string]any)
		if title := strFrom(inp, "title"); title != "" {
			updates["title"] = title
		}
		if body, ok := inp["body"].(string); ok {
			updates["body"] = body
		}
		if state := strFrom(inp, "state"); state != "" {
			updates["state"] = strings.ToLower(state)
			if strings.ToLower(state) == db.StateClosed {
				now := time.Now()
				updates["closed_at"] = &now
				updates["state_reason"] = db.StateReasonCompleted
			} else {
				updates["closed_at"] = nil
				updates["state_reason"] = db.StateReasonReopened
			}
		}
		if len(updates) > 0 {
			if err := s.Svc.UpdateIssueFields(ctx, dbID, updates); err != nil {
				return errResp(err.Error())
			}
		}
		// Attach labels and assignees
		if err := s.Svc.AttachLabelsAndAssignees(ctx, &dbID, nil, extractStringSlice(inp, "labelIds"), extractStringSlice(inp, "assigneeIds")); err != nil {
			return errResp(err.Error())
		}
		// Set milestone if provided
		if msID := strFrom(inp, "milestoneId"); msID != "" {
			if dbMsID := parseNodeID(msID, "Milestone"); dbMsID > 0 {
				if err := s.Svc.SetIssueMilestone(ctx, dbID, &dbMsID); err != nil {
					return errResp(err.Error())
				}
			}
		}
		// Reload and return full issue
		if issue, err := s.Svc.ReloadIssue(ctx, dbID); err == nil {
			return wrap("updateIssue", map[string]any{"issue": s.issueGQL(ctx, issue)})
		}
	}
	return wrap("updateIssue", map[string]any{"issue": map[string]any{"id": issueID}})
}

// doDeleteIssue handles the deleteIssue GraphQL mutation.
func (s *Server) doDeleteIssue(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	issueID := strFrom(inp, "issueId")
	if dbID := parseNodeID(issueID, "Issue"); dbID > 0 {
		if err := s.Svc.DeleteIssueByID(ctx, dbID); err != nil {
			return errResp(err.Error())
		}
	}
	return wrap("deleteIssue", nil)
}

// doLockLockable handles lockLockable and unlockLockable mutations.
func (s *Server) doLockLockable(ctx context.Context, req gqlRequest, lock bool) map[string]any {
	inp := inputMap(req)
	lockableID := strFrom(inp, "lockableId")
	lockReason := strFrom(inp, "lockReason") // OFF_TOPIC, RESOLVED, SPAM, TOO_HEATED

	updates := map[string]any{"locked": lock}
	if lock {
		if lockReason != "" {
			updates["active_lock_reason"] = lockReason
		}
	} else {
		updates["active_lock_reason"] = ""
	}

	if dbID := parseNodeID(lockableID, "Issue"); dbID > 0 {
		if err := s.Svc.UpdateIssueFields(ctx, dbID, updates); err != nil {
			return errResp(err.Error())
		}
	} else if dbID := parseNodeID(lockableID, "PullRequest"); dbID > 0 {
		if err := s.Svc.UpdatePRFields(ctx, dbID, updates); err != nil {
			return errResp(err.Error())
		}
	}

	mutationName := "lockLockable"
	if !lock {
		mutationName = "unlockLockable"
	}
	return wrap(mutationName, map[string]any{
		"lockedRecord": map[string]any{"locked": lock, "activeLockReason": lockReason},
	})
}

// doPinIssue handles pinIssue and unpinIssue mutations.
func (s *Server) doPinIssue(ctx context.Context, req gqlRequest, pin bool) map[string]any {
	inp := inputMap(req)
	issueID := strFrom(inp, "issueId")
	if dbID := parseNodeID(issueID, "Issue"); dbID > 0 {
		if err := s.Svc.UpdateIssueFields(ctx, dbID, map[string]any{"is_pinned": pin}); err != nil {
			return errResp(err.Error())
		}
		if issue, err := s.Svc.ReloadIssue(ctx, dbID); err == nil {
			mutationName := "pinIssue"
			if !pin {
				mutationName = "unpinIssue"
			}
			return wrap(mutationName, map[string]any{"issue": s.issueGQL(ctx, issue)})
		}
	}
	mutationName := "pinIssue"
	if !pin {
		mutationName = "unpinIssue"
	}
	return wrap(mutationName, map[string]any{"issue": map[string]any{"isPinned": pin}})
}

// doTransferIssue handles transferIssue mutation — moves an issue to a different repo.
func (s *Server) doTransferIssue(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	issueID := strFrom(inp, "issueId")
	repoID := strFrom(inp, "repositoryId")

	if dbIssueID := parseNodeID(issueID, "Issue"); dbIssueID > 0 {
		destFullName := s.resolveRepoByNodeID(ctx, repoID)
		if destFullName != "" {
			destRepo, err := s.Svc.GetRepo(ctx, destFullName)
			if err == nil {
				// Update the issue's repository and assign a new number in the destination repo
				newNumber, numErr := s.Svc.NextIssueNumber(ctx, destRepo.ID)
				if numErr != nil {
					return errResp(numErr.Error())
				}
				if err := s.Svc.UpdateIssueFields(ctx, dbIssueID, map[string]any{
					"repository_id": destRepo.ID,
					"number":        newNumber,
				}); err != nil {
					return errResp(err.Error())
				}
				if issue, err := s.Svc.ReloadIssue(ctx, dbIssueID); err == nil {
					return wrap("transferIssue", map[string]any{
						"issue": s.issueGQL(ctx, issue),
					})
				}
			}
		}
	}
	return wrap("transferIssue", map[string]any{"issue": map[string]any{"id": issueID}})
}
