package rest

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/service"
)

// PresenceHandlers wraps PresenceHub for HTTP handlers
type PresenceHandlers struct {
	Svc *service.Service
	Hub *service.PresenceHub
}

func (h *PresenceHandlers) ready(w http.ResponseWriter) bool {
	if h == nil || h.Svc == nil || h.Hub == nil {
		respond.Error(w, http.StatusInternalServerError, "Internal Server Error")
		return false
	}
	return true
}

func (h *PresenceHandlers) requireViewer(w http.ResponseWriter, r *http.Request) (db.User, bool) {
	viewer, ok := service.UserFromContext(r.Context())
	if !ok || viewer.ID == 0 {
		respond.Unauthorized(w, "Authentication required")
		return db.User{}, false
	}
	return viewer, true
}

func (h *PresenceHandlers) requireIssueReadAccess(w http.ResponseWriter, r *http.Request, viewerID, issueID uint) (db.Issue, bool) {
	issue, visible, err := h.issueVisibleToViewer(r.Context(), viewerID, issueID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return db.Issue{}, false
	}
	if !visible {
		respond.ServiceErrorRequest(r, w, service.ErrNotFound)
		return db.Issue{}, false
	}

	return issue, true
}

func (h *PresenceHandlers) issueVisibleToViewer(ctx context.Context, viewerID, issueID uint) (db.Issue, bool, error) {
	issue, err := h.Svc.GetIssueByID(ctx, issueID)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			return db.Issue{}, false, nil
		}
		return db.Issue{}, false, err
	}

	perm, err := h.Svc.HasRepoAccess(ctx, issue.RepositoryID, viewerID)
	if err != nil {
		return db.Issue{}, false, err
	}
	return issue, perm.AtLeast(service.RepoPermissionRead), nil
}

func formatPresenceTime(ts time.Time) string {
	return ts.UTC().Format(time.RFC3339)
}

func writeHiddenLastSeen(w http.ResponseWriter, userID uint) {
	respond.JSON(w, http.StatusOK, map[string]any{
		"user_id":            userID,
		"last_seen_at":       nil,
		"last_seen_issue_id": nil,
	})
}

// PostPresenceHeartbeat handles POST /api/v3/presence/heartbeat
// Updates presence (called every 30s by clients)
func (h *PresenceHandlers) PostPresenceHeartbeat(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	viewer, ok := h.requireViewer(w, r)
	if !ok {
		return
	}

	var req struct {
		IssueID uint `json:"issue_id"`
	}
	if err := decodeBodyStrict(r, &req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.IssueID == 0 {
		respond.Error(w, http.StatusBadRequest, "issue_id is required")
		return
	}

	issue, ok := h.requireIssueReadAccess(w, r, viewer.ID, req.IssueID)
	if !ok {
		return
	}

	if err := h.Hub.UpdateHeartbeat(r.Context(), viewer.ID, issue.ID); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	respond.JSON(w, http.StatusOK, map[string]string{
		"status": "ok",
	})
}

// GetIssuePresence handles GET /api/v3/issues/{issue_id}/presence
// Gets presence state for all users in an issue
func (h *PresenceHandlers) GetIssuePresence(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	viewer, ok := h.requireViewer(w, r)
	if !ok {
		return
	}

	issueIDStr := pathParam(r, "issue_id")
	if issueIDStr == "" {
		respond.Error(w, http.StatusBadRequest, "issue_id is required")
		return
	}

	issueID, err := strconv.ParseUint(issueIDStr, 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "issue_id must be a number")
		return
	}

	issue, ok := h.requireIssueReadAccess(w, r, viewer.ID, uint(issueID))
	if !ok {
		return
	}

	entries, err := h.Hub.GetIssuePresenceState(r.Context(), issue.ID, viewer.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	presence := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		presence = append(presence, map[string]any{
			"user_id":        entry.UserID,
			"status":         string(entry.Status),
			"last_heartbeat": formatPresenceTime(entry.LastHeartbeat),
		})
	}

	respond.JSON(w, http.StatusOK, map[string]any{
		"presence": presence,
	})
}

// PutPresencePrivacy handles PUT /api/v3/user/presence/privacy
// Toggles the user's hide_presence preference
func (h *PresenceHandlers) PutPresencePrivacy(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	viewer, ok := h.requireViewer(w, r)
	if !ok {
		return
	}

	var req struct {
		Hide bool `json:"hide"`
	}
	if err := decodeBodyStrict(r, &req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.Hub.SetHidePresence(r.Context(), viewer.ID, req.Hide); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	respond.JSON(w, http.StatusOK, map[string]any{
		"hide_presence": req.Hide,
	})
}

// GetPresencePrivacy handles GET /api/v3/user/presence/privacy
// Gets the user's current hide_presence preference
func (h *PresenceHandlers) GetPresencePrivacy(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	viewer, ok := h.requireViewer(w, r)
	if !ok {
		return
	}

	hidden, err := h.Hub.IsPresenceHidden(r.Context(), viewer.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	respond.JSON(w, http.StatusOK, map[string]any{
		"hide_presence": hidden,
	})
}

// GetUserLastSeen handles GET /api/v3/users/{user_id}/last-seen
// Gets last-seen timestamp for a user
func (h *PresenceHandlers) GetUserLastSeen(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	viewer, ok := h.requireViewer(w, r)
	if !ok {
		return
	}

	userIDStr := pathParam(r, "user_id")
	if userIDStr == "" {
		respond.Error(w, http.StatusBadRequest, "user_id is required")
		return
	}

	userID, err := strconv.ParseUint(userIDStr, 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "user_id must be a number")
		return
	}

	targetID := uint(userID)
	if targetID != viewer.ID {
		hidden, err := h.Hub.IsPresenceHidden(r.Context(), targetID)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		if hidden {
			writeHiddenLastSeen(w, targetID)
			return
		}
	}

	lastSeen, err := h.Hub.GetUserLastSeen(r.Context(), targetID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	if lastSeen == nil {
		writeHiddenLastSeen(w, targetID)
		return
	}

	if targetID != viewer.ID && lastSeen.LastSeenIssueID != nil {
		if _, visible, err := h.issueVisibleToViewer(r.Context(), viewer.ID, *lastSeen.LastSeenIssueID); err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		} else if !visible {
			writeHiddenLastSeen(w, targetID)
			return
		}
	}

	respond.JSON(w, http.StatusOK, map[string]any{
		"user_id":            lastSeen.UserID,
		"last_seen_at":       formatPresenceTime(lastSeen.LastSeenAt),
		"last_seen_issue_id": lastSeen.LastSeenIssueID,
	})
}
