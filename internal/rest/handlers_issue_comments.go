package rest

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/service"
)

const maxIssueCommentThreadDepth = 5

// --- Issues: Comments ---

// ListIssueComments handles GET /api/v3/repos/{owner}/{repo}/issues/{number}/comments
func (d *Deps) ListIssueComments(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	threaded := r.URL.Query().Get("threaded") == "true"
	sortParam := strings.TrimSpace(r.URL.Query().Get("sort"))
	if sortParam != "" {
		sortParam = strings.ToLower(sortParam)
		switch sortParam {
		case "created", "updated":
		default:
			respond.ValidationFailed(w, "sort must be one of: created, updated")
			return
		}
	}
	if sortParam == "" {
		sortParam = "created"
	}
	direction := strings.TrimSpace(r.URL.Query().Get("direction"))
	if direction != "" {
		direction = strings.ToLower(direction)
		switch direction {
		case "asc", "desc":
		default:
			respond.ValidationFailed(w, "direction must be one of: asc, desc")
			return
		}
	}
	if direction == "" {
		direction = "asc"
	}
	since := strings.TrimSpace(r.URL.Query().Get("since"))
	if since != "" {
		if _, err := time.Parse(time.RFC3339Nano, since); err != nil {
			respond.ValidationFailed(w, "since must be ISO 8601")
			return
		}
	}
	page, perPage := parsePagination(r)
	var comments []db.IssueComment
	var total int64
	var err error
	if threaded {
		// For threaded view, fetch all comments with threading order
		rep, repErr := d.Svc.GetRepo(r.Context(), full)
		if repErr != nil {
			respond.ServiceErrorRequest(r, w, repErr)
			return
		}
		comments, err = d.Svc.ListIssueCommentsThreaded(r.Context(), rep.ID, num)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		total = int64(len(comments))
	} else {
		comments, total, err = d.Svc.ListIssueCommentsPaginated(r.Context(), full, num, since, sortParam, direction, page, perPage)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		setLinkHeader(w, r, d.Svc.BaseURL, int(total), page, perPage)
	}
	var assoc transform.AuthorAssociationChecks
	if len(comments) > 0 {
		assoc = d.authorAssociationChecks(r.Context(), comments[0].Repository)
	}
	// Batch-fetch reaction counts for all comments in one query.
	commentIDs := make([]uint, len(comments))
	for i, c := range comments {
		commentIDs[i] = c.ID
	}
	allReactions, err := d.Svc.CountReactionsBatchForComments(r.Context(), commentIDs)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(comments))
	for i, c := range comments {
		out[i] = transform.IssueComment(c, assoc, allReactions[c.ID])
	}
	respond.JSON(w, 200, out)
}

// ListRepoIssueComments handles GET /api/v3/repos/{owner}/{repo}/issues/comments
func (d *Deps) ListRepoIssueComments(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	sortParam := strings.TrimSpace(r.URL.Query().Get("sort"))
	if sortParam != "" {
		sortParam = strings.ToLower(sortParam)
		switch sortParam {
		case "created", "updated":
		default:
			respond.ValidationFailed(w, "sort must be one of: created, updated")
			return
		}
	}
	if sortParam == "" {
		sortParam = "created"
	}
	direction := strings.TrimSpace(r.URL.Query().Get("direction"))
	if direction != "" {
		direction = strings.ToLower(direction)
		switch direction {
		case "asc", "desc":
		default:
			respond.ValidationFailed(w, "direction must be one of: asc, desc")
			return
		}
	}
	if direction == "" {
		direction = "asc"
	}
	since := strings.TrimSpace(r.URL.Query().Get("since"))
	if since != "" {
		if _, err := time.Parse(time.RFC3339Nano, since); err != nil {
			respond.ValidationFailed(w, "since must be ISO 8601")
			return
		}
	}
	page, perPage := parsePagination(r)
	comments, total, err := d.Svc.ListRepoIssueCommentsPaginated(r.Context(), full, since, sortParam, direction, page, perPage)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	setLinkHeader(w, r, d.Svc.BaseURL, int(total), page, perPage)

	var assoc transform.AuthorAssociationChecks
	if len(comments) > 0 {
		assoc = d.authorAssociationChecks(r.Context(), comments[0].Repository)
	}
	commentIDs := make([]uint, len(comments))
	for i, c := range comments {
		commentIDs[i] = c.ID
	}
	allReactions, err := d.Svc.CountReactionsBatchForComments(r.Context(), commentIDs)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(comments))
	for i, c := range comments {
		out[i] = transform.IssueComment(c, assoc, allReactions[c.ID])
	}
	respond.JSON(w, 200, out)
}

// CreateIssueComment handles POST /api/v3/repos/{owner}/{repo}/issues/{number}/comments
func (d *Deps) CreateIssueComment(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	var body struct {
		Body        string `json:"body"`
		InReplyTo   *uint  `json:"in_reply_to,omitempty"`
		InReplyToID *uint  `json:"in_reply_to_id,omitempty"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	// Support both in_reply_to and in_reply_to_id for compatibility
	inReplyTo := body.InReplyTo
	if inReplyTo == nil {
		inReplyTo = body.InReplyToID
	}
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if inReplyTo != nil {
		depth, depthErr := d.Svc.GetIssueCommentThreadDepth(r.Context(), *inReplyTo)
		if depthErr != nil {
			respond.ServiceErrorRequest(r, w, depthErr)
			return
		}
		if depth >= maxIssueCommentThreadDepth {
			respond.Error(w, http.StatusBadRequest, fmt.Sprintf("reply would exceed maximum issue comment thread depth of %d levels", maxIssueCommentThreadDepth))
			return
		}
	}
	c, err := d.Svc.CreateIssueComment(r.Context(), full, num, body.Body, u.Login, inReplyTo)
	if err != nil {
		if isIssueBodyTooLongError(err) {
			logIssueCommentBodyTooLong("CreateIssueComment: body too long", r, full, &num, nil, body.Body)
		}
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if issue, issueErr := d.Svc.GetIssue(r.Context(), full, num); issueErr == nil {
		d.Svc.TypingHub().Signal(issue.ID, service.TypingUser{
			ID:    u.ID,
			Login: u.Login,
			Name:  u.Name,
		}, false)
	}
	logErr(r.Context(), "CreateIssueComment: webhook", d.Svc.DispatchWebhookEvent(r.Context(), c.RepositoryID, "issue_comment", "created", d.webhookIssueCommentPayload(r.Context(), c, "created")))
	reactionCounts, err := d.Svc.CountReactions(r.Context(), 0, c.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	assoc := d.authorAssociationChecks(r.Context(), c.Repository)
	respond.JSON(w, 201, transform.IssueComment(c, assoc, reactionCounts))
}

// GetIssueComment handles GET /api/v3/repos/{owner}/{repo}/issues/comments/{comment_id}
func (d *Deps) GetIssueComment(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "comment_id")
	if !ok {
		return
	}
	c, err := d.Svc.GetIssueCommentByID(r.Context(), uint(id))
	if err != nil {
		respond.NotFound(w)
		return
	}
	reactionCounts, err := d.Svc.CountReactions(r.Context(), 0, c.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	assoc := d.authorAssociationChecks(r.Context(), c.Repository)
	respond.JSON(w, 200, transform.IssueComment(c, assoc, reactionCounts))
}

// GetPRComment handles GET /api/v3/repos/{owner}/{repo}/pulls/comments/{comment_id}
func (d *Deps) GetPRComment(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	id, ok := mustIntParam(w, r, "comment_id")
	if !ok {
		return
	}
	c, err := d.Svc.GetPRReviewComment(r.Context(), uint(id))
	if err != nil {
		respond.NotFound(w)
		return
	}
	pr, err := d.Svc.ReloadPR(r.Context(), c.PullRequestID)
	if err != nil || pr.Repository.FullName != full {
		respond.NotFound(w)
		return
	}
	respond.JSON(w, 200, transform.PRReviewComment(c, full, pr.Number))
}

// UpdateIssueComment handles PATCH /api/v3/repos/{owner}/{repo}/issues/comments/{comment_id}
func (d *Deps) UpdateIssueComment(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "comment_id")
	if !ok {
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	if err := d.Svc.UpdateIssueComment(r.Context(), uint(id), body.Body); err != nil {
		if isIssueBodyTooLongError(err) {
			full := repoFullName(r)
			logIssueCommentBodyTooLong("UpdateIssueComment: body too long", r, full, nil, &id, body.Body)
		}
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	c, err := d.Svc.GetIssueCommentByID(r.Context(), uint(id))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	logErr(r.Context(), "UpdateIssueComment: webhook", d.Svc.DispatchWebhookEvent(r.Context(), c.RepositoryID, "issue_comment", "edited", d.webhookIssueCommentPayload(r.Context(), c, "edited")))
	reactionCounts, err := d.Svc.CountReactions(r.Context(), 0, c.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	assoc := d.authorAssociationChecks(r.Context(), c.Repository)
	respond.JSON(w, 200, transform.IssueComment(c, assoc, reactionCounts))
}

// PinIssueComment handles PUT /api/v3/repos/{owner}/{repo}/issues/comments/{comment_id}/pin
func (d *Deps) PinIssueComment(w http.ResponseWriter, r *http.Request) {
	d.updateIssueCommentPin(w, r, true)
}

// UnpinIssueComment handles DELETE /api/v3/repos/{owner}/{repo}/issues/comments/{comment_id}/pin
func (d *Deps) UnpinIssueComment(w http.ResponseWriter, r *http.Request) {
	d.updateIssueCommentPin(w, r, false)
}

// DeleteIssueComment handles DELETE /api/v3/repos/{owner}/{repo}/issues/comments/{comment_id}
func (d *Deps) DeleteIssueComment(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "comment_id")
	if !ok {
		return
	}
	comment, err := d.Svc.GetIssueCommentByID(r.Context(), uint(id))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if err := d.Svc.DeleteIssueComment(r.Context(), uint(id)); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	logErr(r.Context(), "DeleteIssueComment: webhook", d.Svc.DispatchWebhookEvent(r.Context(), comment.RepositoryID, "issue_comment", "deleted", d.webhookIssueCommentPayload(r.Context(), comment, "deleted")))
	respond.NoContent(w)
}

func (d *Deps) updateIssueCommentPin(w http.ResponseWriter, r *http.Request, pin bool) {
	id, ok := mustIntParam(w, r, "comment_id")
	if !ok {
		return
	}
	if err := d.Svc.PinIssueComment(r.Context(), uint(id), pin); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	c, err := d.Svc.GetIssueCommentByID(r.Context(), uint(id))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	reactionCounts, err := d.Svc.CountReactions(r.Context(), 0, c.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	assoc := d.authorAssociationChecks(r.Context(), c.Repository)
	respond.JSON(w, 200, transform.IssueComment(c, assoc, reactionCounts))
}
