package rest

import (
	"fmt"
	"net/http"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/rest/respond"
	"gh-server/internal/rest/transform"
)

// --- Issues: Timeline & Events ---

// GetIssueTimeline handles GET /api/v3/repos/{owner}/{repo}/issues/{number}/timeline
func (d *Deps) GetIssueTimeline(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	events, err := d.Svc.GetIssueTimeline(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	// Transform to GitHub timeline event shape
	out := make([]any, len(events))
	var assoc transform.AuthorAssociationChecks
	var assocSet bool
	for i, e := range events {
		if e.Event == "commented" && e.Comment != nil {
			// GitHub timeline comment events are basically the comment object itself with event="commented"
			reactionCounts, err := d.Svc.CountReactions(r.Context(), 0, e.Comment.ID)
			if err != nil {
				respond.ServiceErrorRequest(r, w, err)
				return
			}
			if !assocSet {
				assoc = d.authorAssociationChecks(r.Context(), e.Comment.Repository)
				assocSet = true
			}
			c := transform.IssueComment(*e.Comment, assoc, reactionCounts)
			c["event"] = "commented"
			out[i] = c
		} else if e.Event == "reviewed" && e.Review != nil {
			rv := transform.PRReview(*e.Review, full, num)
			rv["event"] = "reviewed"
			out[i] = rv
		} else {
			ev := map[string]any{
				"event":      e.Event,
				"created_at": e.CreatedAt.Format(time.RFC3339),
			}
			if e.Actor != "" {
				ev["actor"] = map[string]any{"login": e.Actor, "type": db.TypeUser}
			}
			if e.EventRec != nil {
				applyIssueEventData(ev, e.EventRec)
			}
			out[i] = ev
		}
	}
	respond.JSON(w, 200, out)
}

// ListIssueEvents handles GET /api/v3/repos/{owner}/{repo}/issues/{number}/events
func (d *Deps) ListIssueEvents(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	events, err := d.Svc.GetIssueTimeline(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	// Transform to GitHub issue event shape (excludes comments and reviews)
	var out []any
	for _, e := range events {
		if e.Event == "commented" || e.Event == "reviewed" {
			continue
		}

		var actor any
		var eventID any

		// Synthesize actor and ID based on the underlying event type if available.
		// Use the Actor field from TimelineEvent for cleaner mapping.
		if e.Actor != "" {
			actor = map[string]any{"login": e.Actor, "type": db.TypeUser}
		}
		if e.EventRec != nil {
			eventID = e.EventRec.ID
		} else if e.Comment != nil {
			eventID = fmt.Sprintf("event-comment-%d", e.Comment.ID)
		} else if e.Review != nil {
			eventID = fmt.Sprintf("event-review-%d", e.Review.ID)
		} else {
			// Fallback for generic events
			eventID = fmt.Sprintf("event-%s-%d", e.Event, e.CreatedAt.Unix())
		}

		ev := map[string]any{
			"id":         eventID,
			"node_id":    transform.NodeID("IssueEvent", eventID),
			"event":      e.Event,
			"actor":      actor,
			"created_at": e.CreatedAt.Format(time.RFC3339),
		}
		if e.EventRec != nil {
			applyIssueEventData(ev, e.EventRec)
		}
		out = append(out, ev)
	}
	if out == nil {
		out = []any{}
	}
	respond.JSON(w, 200, out)
}

func applyIssueEventData(out map[string]any, ev *db.IssueEvent) {
	if ev == nil {
		return
	}
	switch ev.EventType {
	case "labeled", "unlabeled":
		if ev.LabelName != nil {
			out["label"] = map[string]any{"name": *ev.LabelName}
		}
	case "milestoned", "demilestoned":
		if ev.MilestoneTitle != nil {
			out["milestone"] = map[string]any{"title": *ev.MilestoneTitle}
		}
	case "renamed":
		rename := map[string]any{}
		if ev.OldTitle != nil {
			rename["from"] = *ev.OldTitle
		}
		if ev.NewTitle != nil {
			rename["to"] = *ev.NewTitle
		}
		if len(rename) > 0 {
			out["rename"] = rename
		}
	case "locked":
		if ev.LockReason != nil {
			out["lock_reason"] = *ev.LockReason
		}
	case "closed", "reopened":
		if ev.StateReason != nil {
			out["state_reason"] = *ev.StateReason
		}
	case "assigned", "unassigned":
		if ev.AssigneeLogin != nil {
			out["assignee"] = map[string]any{"login": *ev.AssigneeLogin, "type": db.TypeUser}
		}
	}
}
