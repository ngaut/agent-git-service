package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/service"
)

// SignalIssueTyping handles POST /api/v3/issues/{id}/typing.
func (d *Deps) SignalIssueTyping(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "id")
	if !ok {
		return
	}

	issue, err := d.Svc.GetIssueByID(r.Context(), uint(id))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if !d.requireRepoPermission(w, r, issue.RepositoryID, service.RepoPermissionRead) {
		return
	}

	var body struct {
		Typing *bool `json:"typing"`
	}
	if r.Body != nil {
		if err := decodeBodyStrict(r, &body); err != nil && !errors.Is(err, io.EOF) {
			respond.ValidationFailed(w, "invalid body")
			return
		}
	}

	viewer, ok := service.UserFromContext(r.Context())
	if !ok || viewer.ID == 0 {
		respond.Error(w, http.StatusUnauthorized, "Requires authentication")
		return
	}

	typing := true
	if body.Typing != nil {
		typing = *body.Typing
	}

	d.Svc.TypingHub().Signal(issue.ID, service.TypingUser{
		ID:    viewer.ID,
		Login: viewer.Login,
		Name:  viewer.Name,
	}, typing)

	respond.NoContent(w)
}

// SubscribeIssueTyping handles GET /api/v3/issues/{id}/typing as an SSE stream.
func (d *Deps) SubscribeIssueTyping(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "id")
	if !ok {
		return
	}

	issue, err := d.Svc.GetIssueByID(r.Context(), uint(id))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if !d.requireRepoPermission(w, r, issue.RepositoryID, service.RepoPermissionRead) {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		// Graceful degradation: SSE not supported by this server.
		// Return 200 OK with empty snapshot so clients don't error.
		w.Header().Set("Content-Type", "application/json")
		respond.JSON(w, http.StatusOK, map[string]any{
			"issue_id": issue.ID,
			"users":    []service.TypingUser{},
			"typing":   false,
			"warning":  "SSE not supported",
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	snapshot, events, unsubscribe := d.Svc.TypingHub().Subscribe(issue.ID)
	defer unsubscribe()

	if err := writeSSEEvent(w, "typing_snapshot", service.TypingEnvelope{
		IssueID: issue.ID,
		Users:   snapshot,
		Typing:  false,
	}); err != nil {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-events:
			if err := writeSSEEvent(w, "typing", ev); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSEEvent(w http.ResponseWriter, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	return nil
}
