package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"gh-server/internal/db"
	"gh-server/internal/rest/respond"
	"gh-server/internal/rest/transform"
	"gh-server/internal/service"
)

type milestoneParam struct {
	Set    bool
	Number *int
}

func (m *milestoneParam) UnmarshalJSON(data []byte) error {
	m.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		m.Number = nil
		return nil
	}
	var v int
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	m.Number = &v
	return nil
}

func (d *Deps) resolveMilestoneID(ctx context.Context, repoFullName string, milestoneNumber *int) (*uint, error) {
	if milestoneNumber == nil {
		return nil, nil
	}
	ms, err := d.Svc.GetMilestoneByNumber(ctx, repoFullName, *milestoneNumber)
	if err != nil {
		return nil, err
	}
	id := ms.ID
	return &id, nil
}

func issueEditedWebhookRequested(body struct {
	Title       *string        `json:"title"`
	Body        *string        `json:"body"`
	State       *string        `json:"state"`
	StateReason *string        `json:"state_reason"`
	Locked      *bool          `json:"locked"`
	Labels      *[]string      `json:"labels"`
	Assignees   *[]string      `json:"assignees"`
	Milestone   milestoneParam `json:"milestone"`
}) bool {
	return body.Title != nil || body.Body != nil || body.Labels != nil || body.Assignees != nil || body.Milestone.Set
}

// --- Issues: Core ---

// GetIssue handles GET /api/v3/repos/{owner}/{repo}/issues/{number}
func (d *Deps) GetIssue(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	resolver := d.userResolver(r.Context())
	issue, err := d.Svc.GetIssue(r.Context(), full, num)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			pr, prErr := d.Svc.GetPR(r.Context(), full, num)
			if prErr == nil {
				cc := d.Svc.CountPRComments(r.Context(), pr.RepositoryID, pr.Number)
				assoc := d.authorAssociationChecks(r.Context(), pr.Repository)
				respond.JSON(w, 200, transform.IssueFromPR(pr, resolver, assoc, cc))
				return
			}
		}
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	cc := d.Svc.CountIssueComments(r.Context(), issue.RepositoryID, issue.Number)
	reactionCounts, err := d.Svc.CountReactions(r.Context(), issue.ID, 0)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	assoc := d.authorAssociationChecks(r.Context(), issue.Repository)
	respond.JSON(w, 200, transform.Issue(issue, resolver, assoc, transform.IssueCounts{
		Comments:  cc,
		Reactions: reactionCounts,
	}))
}

// CreateIssue handles POST /api/v3/repos/{owner}/{repo}/issues
func (d *Deps) CreateIssue(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	var body struct {
		Title       string         `json:"title"`
		Body        string         `json:"body"`
		State       *string        `json:"state"`
		StateReason *string        `json:"state_reason"`
		Labels      []string       `json:"labels"`
		Assignees   []string       `json:"assignees"`
		Assignee    *string        `json:"assignee"`
		Milestone   milestoneParam `json:"milestone"`
	}
	// GitHub API returns 422 for creation failures — intentional compatibility.
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	if body.Title == "" {
		respond.ValidationFailed(w, "title is required")
		return
	}
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	issue, err := d.Svc.CreateIssue(r.Context(), service.CreateIssueInput{
		RepoFullName: full,
		Title:        body.Title,
		Body:         body.Body,
		AuthorLogin:  u.Login,
		Labels:       body.Labels,
		State:        body.State,
		StateReason:  body.StateReason,
	})
	if err != nil {
		if isIssueBodyTooLongError(err) {
			logIssueBodyTooLong("CreateIssue: body too long", r, full, nil, body.Title, body.Body, body.State, body.StateReason, body.Labels, body.Assignees)
		}
		if errors.Is(err, service.ErrValidation) || errors.Is(err, service.ErrInvalidState) {
			respond.ValidationFailed(w, err.Error())
			return
		}
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	// Apply optional assignees (support both assignee singular and assignees array).
	// GitHub API accepts assignee (singular, deprecated) as equivalent to assignees: [assignee].
	if body.Assignee != nil && len(body.Assignees) == 0 {
		body.Assignees = []string{*body.Assignee}
	}
	if len(body.Assignees) > 0 {
		if _, err := d.Svc.AddIssueAssignees(r.Context(), full, issue.Number, body.Assignees); err != nil {
			logErr(r.Context(), "CreateIssue: add assignees", err)
		}
	}
	// Apply optional milestone.
	if body.Milestone.Set {
		mid, err := d.resolveMilestoneID(r.Context(), full, body.Milestone.Number)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		if err := d.Svc.SetIssueMilestone(r.Context(), issue.ID, mid); err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
	}
	// Only reload from DB when assignees/milestone were modified;
	// otherwise the in-memory issue from CreateIssue is sufficient.
	if len(body.Assignees) > 0 || body.Milestone.Set {
		issue, err = d.Svc.GetIssueByID(r.Context(), issue.ID)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
	}
	action := "opened"
	if issue.State == db.StateClosed {
		action = "closed"
	}
	logErr(r.Context(), "CreateIssue: webhook", d.Svc.DispatchWebhookEvent(r.Context(), issue.RepositoryID, "issues", action, d.webhookIssuePayload(r.Context(), issue, action)))
	resolver := d.userResolver(r.Context())
	// Skip CountReactions for newly created issues — always empty.
	var reactionCounts map[string]int64
	assoc := d.authorAssociationChecks(r.Context(), issue.Repository)
	respond.JSON(w, 201, transform.Issue(issue, resolver, assoc, transform.IssueCounts{
		Reactions: reactionCounts,
	}))
}

// UpdateIssue handles PATCH /api/v3/repos/{owner}/{repo}/issues/{number}
func (d *Deps) UpdateIssue(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	resolver := d.userResolver(r.Context())
	var body struct {
		Title       *string        `json:"title"`
		Body        *string        `json:"body"`
		State       *string        `json:"state"`
		StateReason *string        `json:"state_reason"`
		Locked      *bool          `json:"locked"`
		Labels      *[]string      `json:"labels"`
		Assignees   *[]string      `json:"assignees"`
		Milestone   milestoneParam `json:"milestone"`
	}
	decodeBody(r, &body)
	issue, err := d.Svc.UpdateIssue(r.Context(), full, num, service.UpdateIssueInput{
		Title: body.Title, Body: body.Body, State: body.State, StateReason: body.StateReason, Locked: body.Locked,
	})
	if err != nil {
		if body.Body != nil && isIssueBodyTooLongError(err) {
			logIssueBodyTooLong("UpdateIssue: body too long", r, full, &num, ptrValue(body.Title), *body.Body, body.State, body.StateReason, ptrSliceValue(body.Labels), ptrSliceValue(body.Assignees))
		}
		if errors.Is(err, service.ErrNotFound) {
			pr, prErr := d.Svc.UpdatePR(r.Context(), full, num, service.UpdatePRInput{
				Title: body.Title, Body: body.Body, State: body.State,
			})
			if prErr == nil {
				if body.Labels != nil {
					if _, err := d.Svc.SetPRLabels(r.Context(), full, num, *body.Labels); err != nil {
						respond.ServiceErrorRequest(r, w, err)
						return
					}
					pr, prErr = d.Svc.GetPR(r.Context(), full, num)
					if prErr != nil {
						respond.ServiceErrorRequest(r, w, prErr)
						return
					}
				}
				if body.Assignees != nil {
					if _, err := d.Svc.SetPRAssignees(r.Context(), full, num, *body.Assignees); err != nil {
						respond.ServiceErrorRequest(r, w, err)
						return
					}
					pr, prErr = d.Svc.GetPR(r.Context(), full, num)
					if prErr != nil {
						respond.ServiceErrorRequest(r, w, prErr)
						return
					}
				}
				if body.Milestone.Set {
					mid, err := d.resolveMilestoneID(r.Context(), full, body.Milestone.Number)
					if err != nil {
						respond.ServiceErrorRequest(r, w, err)
						return
					}
					if err := d.Svc.SetPRMilestone(r.Context(), pr.ID, mid); err != nil {
						respond.ServiceErrorRequest(r, w, err)
						return
					}
					pr, prErr = d.Svc.GetPR(r.Context(), full, num)
					if prErr != nil {
						respond.ServiceErrorRequest(r, w, prErr)
						return
					}
				}
				action := ""
				if body.State != nil {
					switch *body.State {
					case db.StateClosed:
						action = "closed"
					case db.StateOpen:
						action = "reopened"
					}
				}
				if action == "" && issueEditedWebhookRequested(body) {
					action = "edited"
				}
				if action != "" {
					logErr(r.Context(), "UpdateIssue(PR fallback): webhook", d.Svc.DispatchWebhookEvent(r.Context(), pr.RepositoryID, "pull_request", action, d.webhookPRPayload(r.Context(), pr, action, d.prWithExtras(r, pr))))
				}
				cc := d.Svc.CountPRComments(r.Context(), pr.RepositoryID, pr.Number)
				assoc := d.authorAssociationChecks(r.Context(), pr.Repository)
				respond.JSON(w, 200, transform.IssueFromPR(pr, resolver, assoc, cc))
				return
			}
		}
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	needReload := false
	if body.Labels != nil {
		if _, err := d.Svc.SetIssueLabels(r.Context(), full, num, *body.Labels); err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		needReload = true
	}
	if body.Assignees != nil {
		// GitHub PATCH assignees = full replacement: set to exactly the provided list.
		if _, err := d.Svc.SetIssueAssignees(r.Context(), full, num, *body.Assignees); err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		needReload = true
	}
	if body.Milestone.Set {
		mid, err := d.resolveMilestoneID(r.Context(), full, body.Milestone.Number)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		if err := d.Svc.SetIssueMilestone(r.Context(), issue.ID, mid); err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		needReload = true
	}
	if needReload {
		issue, err = d.Svc.GetIssue(r.Context(), full, num)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
	}
	action := ""
	if body.State != nil {
		switch *body.State {
		case db.StateClosed:
			action = "closed"
		case db.StateOpen:
			action = "reopened"
		}
	}
	if action == "" && issueEditedWebhookRequested(body) {
		action = "edited"
	}
	if action != "" {
		logErr(r.Context(), "UpdateIssue: webhook", d.Svc.DispatchWebhookEvent(r.Context(), issue.RepositoryID, "issues", action, d.webhookIssuePayload(r.Context(), issue, action)))
	}
	reactionCounts, err := d.Svc.CountReactions(r.Context(), issue.ID, 0)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	assoc := d.authorAssociationChecks(r.Context(), issue.Repository)
	respond.JSON(w, 200, transform.Issue(issue, resolver, assoc, transform.IssueCounts{
		Reactions: reactionCounts,
	}))
}

// LockIssue handles PUT /api/v3/repos/{owner}/{repo}/issues/{number}/lock
func (d *Deps) LockIssue(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	var body struct {
		LockReason string `json:"lock_reason"`
	}
	decodeBody(r, &body)
	locked := true
	if _, err := d.Svc.UpdateIssue(r.Context(), full, num, service.UpdateIssueInput{
		Locked:           &locked,
		ActiveLockReason: &body.LockReason,
	}); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// UnlockIssue handles DELETE /api/v3/repos/{owner}/{repo}/issues/{number}/lock
func (d *Deps) UnlockIssue(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	locked := false
	emptyReason := ""
	if _, err := d.Svc.UpdateIssue(r.Context(), full, num, service.UpdateIssueInput{
		Locked:           &locked,
		ActiveLockReason: &emptyReason,
	}); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}
