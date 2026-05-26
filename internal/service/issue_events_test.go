package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func TestIssueEventsRecorded(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	setupRepoForTest(t, svc, "evtuser", "evtrepo")
	var user db.User
	if err := svc.DB.First(&user, "login = ?", "evtuser").Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	ctx := service.ContextWithUser(context.Background(), user)
	full := "evtuser/evtrepo"

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: full,
		Title:        "Initial title",
		Body:         "body",
		AuthorLogin:  user.Login,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	const labelName = "evt-bug"
	if _, err := svc.CreateLabel(ctx, full, labelName, "ff0000", ""); err != nil {
		t.Fatalf("create label: %v", err)
	}
	if _, err := svc.AddIssueLabels(ctx, full, issue.Number, []string{labelName}); err != nil {
		t.Fatalf("add label: %v", err)
	}

	ms, err := svc.CreateMilestone(ctx, full, "v1", "milestone", db.StateOpen)
	if err != nil {
		t.Fatalf("create milestone: %v", err)
	}
	if err := svc.SetIssueMilestone(ctx, issue.ID, &ms.ID); err != nil {
		t.Fatalf("set milestone: %v", err)
	}

	newTitle := "Updated title"
	if _, err := svc.UpdateIssue(ctx, full, issue.Number, service.UpdateIssueInput{Title: &newTitle}); err != nil {
		t.Fatalf("update title: %v", err)
	}

	closed := db.StateClosed
	reason := db.StateReasonNotPlanned
	if _, err := svc.UpdateIssue(ctx, full, issue.Number, service.UpdateIssueInput{State: &closed, StateReason: &reason}); err != nil {
		t.Fatalf("close issue: %v", err)
	}

	locked := true
	lockReason := "SPAM"
	if _, err := svc.UpdateIssue(ctx, full, issue.Number, service.UpdateIssueInput{Locked: &locked, ActiveLockReason: &lockReason}); err != nil {
		t.Fatalf("lock issue: %v", err)
	}

	if _, err := svc.RemoveIssueLabel(ctx, full, issue.Number, labelName); err != nil {
		t.Fatalf("remove label: %v", err)
	}
	if err := svc.SetIssueMilestone(ctx, issue.ID, nil); err != nil {
		t.Fatalf("clear milestone: %v", err)
	}

	var events []db.IssueEvent
	if err := svc.DB.Where("issue_id = ?", issue.ID).Find(&events).Error; err != nil {
		t.Fatalf("load events: %v", err)
	}

	findEvent := func(typ string) *db.IssueEvent {
		t.Helper()
		for i := range events {
			if events[i].EventType == typ {
				return &events[i]
			}
		}
		return nil
	}

	if ev := findEvent("opened"); ev == nil {
		t.Fatalf("expected opened event")
	} else if ev.ActorLogin != user.Login {
		t.Fatalf("opened actor: got %q want %q", ev.ActorLogin, user.Login)
	}
	if ev := findEvent("labeled"); ev == nil || ev.LabelName == nil || *ev.LabelName != labelName {
		t.Fatalf("expected labeled event with label %s, got %+v", labelName, ev)
	}
	if ev := findEvent("unlabeled"); ev == nil || ev.LabelName == nil || *ev.LabelName != labelName {
		t.Fatalf("expected unlabeled event with label %s, got %+v", labelName, ev)
	}
	if ev := findEvent("milestoned"); ev == nil || ev.MilestoneTitle == nil || *ev.MilestoneTitle != "v1" {
		t.Fatalf("expected milestoned event with milestone v1, got %+v", ev)
	}
	if ev := findEvent("demilestoned"); ev == nil || ev.MilestoneTitle == nil || *ev.MilestoneTitle != "v1" {
		t.Fatalf("expected demilestoned event with milestone v1, got %+v", ev)
	}
	if ev := findEvent("renamed"); ev == nil || ev.OldTitle == nil || ev.NewTitle == nil || *ev.OldTitle != "Initial title" || *ev.NewTitle != "Updated title" {
		t.Fatalf("expected renamed event with titles, got %+v", ev)
	}
	if ev := findEvent("closed"); ev == nil || ev.StateReason == nil || *ev.StateReason != db.StateReasonNotPlanned {
		t.Fatalf("expected closed event with state reason, got %+v", ev)
	}
	if ev := findEvent("locked"); ev == nil || ev.LockReason == nil || *ev.LockReason != "SPAM" {
		t.Fatalf("expected locked event with reason, got %+v", ev)
	}
}

func TestGetIssueTimeline_IncludesIssueEvents(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	setupRepoForTest(t, svc, "timelineuser", "timelinerepo")
	var user db.User
	if err := svc.DB.First(&user, "login = ?", "timelineuser").Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	ctx := service.ContextWithUser(context.Background(), user)
	full := "timelineuser/timelinerepo"

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: full,
		Title:        "Timeline issue",
		AuthorLogin:  user.Login,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	if _, err := svc.CreateLabel(ctx, full, "timeline-label", "00ff00", ""); err != nil {
		t.Fatalf("create label: %v", err)
	}
	if _, err := svc.AddIssueLabels(ctx, full, issue.Number, []string{"timeline-label"}); err != nil {
		t.Fatalf("add label: %v", err)
	}

	comment, err := svc.CreateIssueComment(ctx, full, issue.Number, "first comment", user.Login, nil)
	if err != nil {
		t.Fatalf("create comment: %v", err)
	}

	var events []db.IssueEvent
	if err := svc.DB.Where("issue_id = ?", issue.ID).Find(&events).Error; err != nil {
		t.Fatalf("load events: %v", err)
	}

	base := time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC)
	t1 := base.Add(1 * time.Hour)
	t2 := base.Add(2 * time.Hour)
	t3 := base.Add(3 * time.Hour)

	for _, ev := range events {
		switch ev.EventType {
		case "opened":
			if err := svc.DB.Model(&db.IssueEvent{}).Where("id = ?", ev.ID).Update("created_at", t1).Error; err != nil {
				t.Fatalf("update opened event time: %v", err)
			}
		case "labeled":
			if err := svc.DB.Model(&db.IssueEvent{}).Where("id = ?", ev.ID).Update("created_at", t2).Error; err != nil {
				t.Fatalf("update labeled event time: %v", err)
			}
		}
	}
	if err := svc.DB.Model(&db.IssueComment{}).Where("id = ?", comment.ID).
		Updates(map[string]any{"created_at": t3, "updated_at": t3}).Error; err != nil {
		t.Fatalf("update comment time: %v", err)
	}

	timeline, err := svc.GetIssueTimeline(ctx, full, issue.Number)
	if err != nil {
		t.Fatalf("GetIssueTimeline failed: %v", err)
	}
	if len(timeline) != 3 {
		t.Fatalf("expected 3 timeline events, got %d", len(timeline))
	}
	if timeline[0].Event != "opened" || timeline[0].EventRec == nil {
		t.Fatalf("expected first event to be opened, got %+v", timeline[0])
	}
	if timeline[1].Event != "labeled" || timeline[1].EventRec == nil {
		t.Fatalf("expected second event to be labeled, got %+v", timeline[1])
	}
	if timeline[2].Event != "commented" || timeline[2].Comment == nil {
		t.Fatalf("expected third event to be comment, got %+v", timeline[2])
	}
	if !timeline[0].CreatedAt.Equal(t1) || !timeline[1].CreatedAt.Equal(t2) || !timeline[2].CreatedAt.Equal(t3) {
		t.Fatalf("timeline ordering mismatch: %+v", timeline)
	}
}
