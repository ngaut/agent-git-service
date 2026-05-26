package rest

import (
	"errors"
	"net/http"

	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/service"
)

// MarkIssueReadRequest represents the request body for marking an issue as read.
type MarkIssueReadRequest struct {
	LastReadCommentID uint `json:"last_read_comment_id,omitempty"`
}

// IssueReadStateResponse represents the read state response.
type IssueReadStateResponse struct {
	IssueID           uint   `json:"issue_id"`
	UserID            uint   `json:"user_id"`
	LastReadCommentID uint   `json:"last_read_comment_id"`
	UpdatedAt         string `json:"updated_at"`
}

// ParticipantReadStateResponse represents read state for a participant.
type ParticipantReadStateResponse struct {
	UserID            uint   `json:"user_id"`
	UserLogin         string `json:"user_login"`
	LastReadCommentID uint   `json:"last_read_comment_id"`
	UpdatedAt         string `json:"updated_at"`
}

// MarkIssueRead handles POST /api/v3/repos/{owner}/{repo}/issues/{number}/read
// Marks the issue as read up to the specified comment for the current user.
func (d *Deps) MarkIssueRead(w http.ResponseWriter, r *http.Request) {
	// Get repo to ensure it exists
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}

	// Get issue number
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}

	// Get the issue to get its ID
	issue, err := d.Svc.GetIssue(r.Context(), repo.FullName, num)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			respond.NotFound(w)
			return
		}
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	// Parse optional request body
	var body MarkIssueReadRequest
	if err := decodeBodyStrictOptional(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}

	// Mark issue as read
	state, err := d.Svc.MarkIssueAsRead(r.Context(), issue.ID, body.LastReadCommentID)
	if err != nil {
		if errors.Is(err, service.ErrUnauthorized) {
			respond.Unauthorized(w, "Authentication required")
			return
		}
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	respond.JSON(w, 200, IssueReadStateResponse{
		IssueID:           state.IssueID,
		UserID:            state.UserID,
		LastReadCommentID: state.LastReadCommentID,
		UpdatedAt:         state.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

// GetIssueReadState handles GET /api/v3/repos/{owner}/{repo}/issues/{number}/read-state
// Gets the read state for the current user on the specified issue.
func (d *Deps) GetIssueReadState(w http.ResponseWriter, r *http.Request) {
	// Get repo to ensure it exists
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}

	// Get issue number
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}

	// Get the issue to get its ID
	issue, err := d.Svc.GetIssue(r.Context(), repo.FullName, num)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			respond.NotFound(w)
			return
		}
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	// Get read state for current user
	state, err := d.Svc.GetCurrentUserIssueReadState(r.Context(), issue.ID)
	if err != nil {
		if errors.Is(err, service.ErrUnauthorized) {
			respond.Unauthorized(w, "Authentication required")
			return
		}
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	respond.JSON(w, 200, IssueReadStateResponse{
		IssueID:           state.IssueID,
		UserID:            state.UserID,
		LastReadCommentID: state.LastReadCommentID,
		UpdatedAt:         state.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

// GetIssueParticipantsReadState handles GET /api/v3/repos/{owner}/{repo}/issues/{number}/participants/read-state
// Gets the read state for all participants of the issue.
func (d *Deps) GetIssueParticipantsReadState(w http.ResponseWriter, r *http.Request) {
	// Get repo to ensure it exists
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}

	// Get issue number
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}

	// Get the issue to get its ID
	issue, err := d.Svc.GetIssue(r.Context(), repo.FullName, num)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			respond.NotFound(w)
			return
		}
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	// Get read states for all participants
	states, err := d.Svc.GetIssueParticipantsReadState(r.Context(), issue.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	// Transform to response format
	response := make([]ParticipantReadStateResponse, 0, len(states))
	for _, state := range states {
		response = append(response, ParticipantReadStateResponse{
			UserID:            state.UserID,
			UserLogin:         state.User.Login,
			LastReadCommentID: state.LastReadCommentID,
			UpdatedAt:         state.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}

	respond.JSON(w, 200, response)
}

// GetIssueUnreadCount handles GET /api/v3/repos/{owner}/{repo}/issues/{number}/unread-count
// Gets the count of unread comments for the current user.
func (d *Deps) GetIssueUnreadCount(w http.ResponseWriter, r *http.Request) {
	// Get repo to ensure it exists
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}

	// Get issue number
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}

	// Get the issue to get its ID
	issue, err := d.Svc.GetIssue(r.Context(), repo.FullName, num)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			respond.NotFound(w)
			return
		}
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	// Get current user
	viewer, ok := service.UserFromContext(r.Context())
	if !ok {
		respond.Unauthorized(w, "Authentication required")
		return
	}

	// Get unread count
	count, err := d.Svc.GetIssueUnreadCount(r.Context(), issue.ID, viewer.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	respond.JSON(w, 200, map[string]any{
		"unread_count": count,
		"issue_id":     issue.ID,
		"user_id":      viewer.ID,
	})
}
