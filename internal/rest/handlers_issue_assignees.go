package rest

import (
	"errors"
	"net/http"

	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/service"
)

// --- Issues: Assignees ---

// AddIssueAssignees handles POST /api/v3/repos/{owner}/{repo}/issues/{number}/assignees
func (d *Deps) AddIssueAssignees(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	resolver := d.userResolver(r.Context())
	var body struct {
		Assignees []string `json:"assignees"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	issue, err := d.Svc.AddIssueAssignees(r.Context(), full, num, body.Assignees)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			pr, prErr := d.Svc.AddPRAssignees(r.Context(), full, num, body.Assignees)
			if prErr == nil {
				cc := d.Svc.CountPRComments(r.Context(), pr.RepositoryID, pr.Number)
				assoc := d.authorAssociationChecks(r.Context(), pr.Repository)
				respond.JSON(w, 201, transform.IssueFromPR(pr, resolver, assoc, cc))
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
	respond.JSON(w, 201, transform.Issue(issue, resolver, assoc, transform.IssueCounts{
		Comments:  cc,
		Reactions: reactionCounts,
	}))
}

// RemoveIssueAssignees handles DELETE /api/v3/repos/{owner}/{repo}/issues/{number}/assignees
func (d *Deps) RemoveIssueAssignees(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	resolver := d.userResolver(r.Context())
	var body struct {
		Assignees []string `json:"assignees"`
	}
	decodeBody(r, &body)
	issue, err := d.Svc.RemoveIssueAssignees(r.Context(), full, num, body.Assignees)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			pr, prErr := d.Svc.RemovePRAssignees(r.Context(), full, num, body.Assignees)
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
