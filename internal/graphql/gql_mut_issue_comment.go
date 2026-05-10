package graphql

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/service"
	"gorm.io/gorm"
)

// doAddComment handles the addComment mutation (works for both issues and PRs).
func (s *Server) doAddComment(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	body := strFrom(inp, "body")
	subjectID := strFrom(inp, "subjectId")
	u, err := s.Svc.GetCurrentUser(ctx)
	if err != nil {
		return errResp("authentication required")
	}

	var comment service.CommentResult
	var addErr error

	if dbID := parseNodeID(subjectID, "Issue"); dbID > 0 {
		c, err := s.Svc.AddCommentByIssueID(ctx, dbID, body, u.Login)
		comment = service.CommentResult{ID: c.ID, Body: string(c.Body), CreatedAt: c.CreatedAt}
		addErr = err
	} else if dbID := parseNodeID(subjectID, "PullRequest"); dbID > 0 {
		c, err := s.Svc.AddCommentByPRID(ctx, dbID, body, u.Login)
		comment = service.CommentResult{ID: c.ID, Body: string(c.Body), CreatedAt: c.CreatedAt}
		addErr = err
	}
	if addErr != nil {
		return errResp(addErr.Error())
	}
	return wrap("addComment", map[string]any{
		"commentEdge": map[string]any{
			"node": map[string]any{
				"id":        gqlID("IssueComment", comment.ID),
				"body":      comment.Body,
				"author":    map[string]any{"login": u.Login},
				"createdAt": comment.CreatedAt.Format(time.RFC3339),
				"url":       fmt.Sprintf("%s/comment/%d", s.Svc.HTMLBaseURL(), comment.ID),
			},
		},
	})
}

func (s *Server) doUpdateComment(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	body := strFrom(inp, "body")
	commentID := strFrom(inp, "id")
	if dbID := parseNodeID(commentID, "IssueComment"); dbID > 0 {
		if err := s.Svc.UpdateIssueComment(ctx, dbID, body); err != nil {
			return errResp(err.Error())
		}
	}
	u, err := s.Svc.GetCurrentUser(ctx)
	if err != nil {
		return errResp("authentication required")
	}
	return wrap("updateIssueComment", map[string]any{
		"issueComment": map[string]any{
			"id":     commentID,
			"body":   body,
			"url":    fmt.Sprintf("%s/comment/%s", s.Svc.HTMLBaseURL(), strings.TrimPrefix(commentID, "IssueComment_")),
			"author": map[string]any{"login": u.Login},
		},
	})
}

// doDeleteIssueComment handles the deleteIssueComment mutation.
func (s *Server) doDeleteIssueComment(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	commentID := strFrom(inp, "id")
	if dbID := parseNodeID(commentID, "IssueComment"); dbID > 0 {
		if err := s.Svc.DeleteIssueComment(ctx, dbID); err != nil {
			return errResp(err.Error())
		}
	}
	return wrap("deleteIssueComment", nil)
}

func (s *Server) doPinIssueComment(ctx context.Context, req gqlRequest, pin bool) map[string]any {
	inp := inputMap(req)
	commentID := strFrom(inp, "commentId")
	if commentID == "" {
		commentID = strFrom(inp, "issueCommentId")
	}
	if commentID == "" {
		commentID = strFrom(inp, "id")
	}

	mutationName := "pinIssueComment"
	if !pin {
		mutationName = "unpinIssueComment"
	}

	if dbID := parseNodeID(commentID, "IssueComment"); dbID > 0 {
		if err := s.Svc.PinIssueComment(ctx, dbID, pin); err != nil {
			return errResp(err.Error())
		}
		if comment, err := s.Svc.GetIssueCommentByID(ctx, dbID); err == nil {
			currentUser, err := s.Svc.GetCurrentUser(ctx)
			currentLogin := ""
			if err == nil {
				currentLogin = currentUser.Login
			}
			return wrap(mutationName, map[string]any{"issueComment": s.issueCommentGQL(comment, currentLogin)})
		}
	}

	return wrap(mutationName, map[string]any{
		"issueComment": map[string]any{
			"id":       commentID,
			"isPinned": pin,
		},
	})
}

type labelableTarget struct {
	issueID uint
	prID    uint
}

func resolveLabelableTarget(labelableID string) (labelableTarget, error) {
	switch {
	case parseNodeID(labelableID, "Issue") > 0:
		return labelableTarget{issueID: parseNodeID(labelableID, "Issue")}, nil
	case parseNodeID(labelableID, "PullRequest") > 0:
		return labelableTarget{prID: parseNodeID(labelableID, "PullRequest")}, nil
	default:
		return labelableTarget{}, fmt.Errorf("invalid labelableId")
	}
}

func (s *Server) applyLabelsToTarget(ctx context.Context, target labelableTarget, labelIDs []string, remove bool) error {
	return s.Svc.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := service.ContextWithDB(ctx, tx)
		for _, labelID := range labelIDs {
			dbID := parseNodeID(labelID, "Label")
			if dbID == 0 {
				return fmt.Errorf("invalid label ID")
			}
			if target.issueID > 0 {
				if remove {
					if err := s.Svc.RemoveIssueLabelByID(txCtx, target.issueID, dbID); err != nil {
						return err
					}
				} else if err := s.Svc.AddIssueLabelByID(txCtx, target.issueID, dbID); err != nil {
					return err
				}
				continue
			}
			if remove {
				if err := s.Svc.RemovePRLabelByID(txCtx, target.prID, dbID); err != nil {
					return err
				}
			} else if err := s.Svc.AddPRLabelByID(txCtx, target.prID, dbID); err != nil {
				return err
			}
		}
		return nil
	})
}

// doAddLabels handles the addLabelsToLabelable GraphQL mutation.
func (s *Server) doAddLabels(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	target, err := resolveLabelableTarget(strFrom(inp, "labelableId"))
	if err != nil {
		return errResp(err.Error())
	}
	if err := s.applyLabelsToTarget(ctx, target, extractStringSlice(inp, "labelIds"), false); err != nil {
		return errResp(err.Error())
	}
	return wrap("addLabelsToLabelable", map[string]any{"__typename": "AddLabelsToLabelablePayload"})
}

// doRemoveLabels handles the removeLabelsFromLabelable GraphQL mutation.
func (s *Server) doRemoveLabels(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	target, err := resolveLabelableTarget(strFrom(inp, "labelableId"))
	if err != nil {
		return errResp(err.Error())
	}
	if err := s.applyLabelsToTarget(ctx, target, extractStringSlice(inp, "labelIds"), true); err != nil {
		return errResp(err.Error())
	}
	return wrap("removeLabelsFromLabelable", map[string]any{"__typename": "RemoveLabelsFromLabelablePayload"})
}

// doReplaceAssignees handles replaceActorsForAssignable mutation.
// This replaces all assignees on an issue or PR with the given actor logins.
func (s *Server) doReplaceAssignees(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	assignableID := strFrom(inp, "assignableId")
	actorIDs := extractStringSlice(inp, "actorIds")

	// Resolve actor logins from node IDs
	var logins []string
	for _, aid := range actorIDs {
		if login := s.resolveUserLoginByNodeID(ctx, aid); login != "" {
			logins = append(logins, login)
		}
	}
	assigneeStr := strings.Join(logins, ",")

	if dbID := parseNodeID(assignableID, "Issue"); dbID > 0 {
		if err := s.Svc.UpdateIssueFields(ctx, dbID, map[string]any{"assignee_logins": assigneeStr}); err != nil {
			return errResp(err.Error())
		}
		if issue, err := s.Svc.ReloadIssue(ctx, dbID); err == nil {
			return wrap("replaceActorsForAssignable", map[string]any{"assignable": s.issueGQL(ctx, issue)})
		}
	}
	if dbID := parseNodeID(assignableID, "PullRequest"); dbID > 0 {
		if err := s.Svc.UpdatePRFields(ctx, dbID, map[string]any{"assignee_logins": assigneeStr}); err != nil {
			return errResp(err.Error())
		}
		if pr, err := s.Svc.ReloadPR(ctx, dbID); err == nil {
			return wrap("replaceActorsForAssignable", map[string]any{"assignable": s.prGQL(ctx, pr)})
		}
	}
	return wrap("replaceActorsForAssignable", map[string]any{"assignable": nil})
}

func (s *Server) doCreateLinkedBranch(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)

	repoID := strFrom(inp, "repositoryId")
	issueID := strFrom(inp, "issueId")
	oid := strFrom(inp, "oid")
	name := strFrom(inp, "name")

	// Fallbacks
	if repoID == "" {
		repoID = strFrom(req.Variables, "repositoryId")
	}
	if oid == "" {
		oid = strFrom(req.Variables, "oid")
	}
	if name == "" {
		name = strFrom(req.Variables, "name")
	}

	repoID = strings.Trim(repoID, "\"")
	oid = strings.Trim(oid, "\"")
	name = strings.Trim(name, "\"")

	fullName := s.resolveRepoByNodeID(ctx, repoID)
	repo, err := s.Svc.GetRepo(ctx, fullName)
	if err != nil {
		return errResp("repository not found")
	}
	currentUser, err := s.Svc.GetCurrentUser(ctx)
	if err != nil {
		return errResp("authentication required")
	}
	canCreate, err := s.Svc.CanCreatePR(ctx, repo.ID, currentUser.ID)
	if err != nil {
		return errResp(err.Error())
	}
	if !canCreate {
		return errResp("repository not found")
	}

	// Create actual linked branch in DB
	if fullName != "" && oid != "" && name != "" {
		if err := s.Svc.Git.CreateBranchFromOid(ctx, fullName, name, oid); err != nil {
			return errResp(err.Error())
		}

		// Map issueID back to DB ID if provided
		parsedIssueID := parseNodeID(issueID, "Issue")

		if err := s.Svc.CreateLinkedBranch(ctx, &db.LinkedBranch{
			RepositoryID: repo.ID,
			IssueID:      parsedIssueID,
			BranchName:   name,
		}); err != nil {
			return errResp(err.Error())
		}
	}

	return map[string]any{
		"linkedBranch": map[string]any{
			"id": fmt.Sprintf("LinkedBranch_%s_%s", repoID, name),
			"ref": map[string]any{
				"name": name,
			},
		},
	}
}
