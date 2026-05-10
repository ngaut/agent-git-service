package service

import (
	"context"

	"gh-server/internal/db"
)

const (
	issueEventOpened       = "opened"
	issueEventClosed       = "closed"
	issueEventReopened     = "reopened"
	issueEventLabeled      = "labeled"
	issueEventUnlabeled    = "unlabeled"
	issueEventMilestoned   = "milestoned"
	issueEventDemilestoned = "demilestoned"
	issueEventRenamed      = "renamed"
	issueEventLocked       = "locked"
	issueEventUnlocked     = "unlocked"
	issueEventAssigned     = "assigned"
	issueEventUnassigned   = "unassigned"
)

type issueEventData struct {
	LabelName      *string
	MilestoneTitle *string
	OldTitle       *string
	NewTitle       *string
	LockReason     *string
	StateReason    *string
	AssigneeLogin  *string
}

func (s *Service) recordIssueEvent(ctx context.Context, issueID uint, eventType string, data issueEventData, actorOverride ...string) error {
	actor := ""
	if len(actorOverride) > 0 && actorOverride[0] != "" {
		actor = actorOverride[0]
	} else if u, ok := UserFromContext(ctx); ok {
		actor = u.Login
	}
	ev := db.IssueEvent{
		IssueID:        issueID,
		EventType:      eventType,
		ActorLogin:     actor,
		LabelName:      data.LabelName,
		MilestoneTitle: data.MilestoneTitle,
		OldTitle:       data.OldTitle,
		NewTitle:       data.NewTitle,
		LockReason:     data.LockReason,
		StateReason:    data.StateReason,
		AssigneeLogin:  data.AssigneeLogin,
	}
	return s.DBForCtx(ctx).Create(&ev).Error
}

func strPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
