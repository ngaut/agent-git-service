package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func TestNotifications_CreateListAndMarkRead(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	actor := db.User{Login: "notif-actor", Name: "notif-actor", Type: db.TypeUser}
	mentioned := db.User{Login: "notif-mentioned", Name: "notif-mentioned", Type: db.TypeUser}
	assignee := db.User{Login: "notif-assignee", Name: "notif-assignee", Type: db.TypeUser}
	parentAuthor := db.User{Login: "notif-parent", Name: "notif-parent", Type: db.TypeUser}
	replier := db.User{Login: "notif-replier", Name: "notif-replier", Type: db.TypeUser}
	for _, user := range []*db.User{&actor, &mentioned, &assignee, &parentAuthor, &replier} {
		if err := svc.DB.Create(user).Error; err != nil {
			t.Fatalf("create user %s: %v", user.Login, err)
		}
	}

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: actor.Login,
		Name:       "notifications",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: repo.FullName,
		Title:        "Mentioned issue",
		Body:         "hello @notif-mentioned",
		AuthorLogin:  actor.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	mentionedNotifications, err := svc.ListNotifications(ctx, mentioned.ID, true, 100)
	if err != nil {
		t.Fatalf("ListNotifications(mentioned): %v", err)
	}
	if len(mentionedNotifications) != 1 {
		t.Fatalf("expected 1 mention notification, got %d", len(mentionedNotifications))
	}
	if mentionedNotifications[0].Type != service.NotificationTypeMention {
		t.Fatalf("expected mention notification, got %q", mentionedNotifications[0].Type)
	}
	if mentionedNotifications[0].SubjectType != service.NotificationSubjectIssue {
		t.Fatalf("expected issue subject, got %q", mentionedNotifications[0].SubjectType)
	}
	if mentionedNotifications[0].SubjectID != issue.ID {
		t.Fatalf("expected subject ID %d, got %d", issue.ID, mentionedNotifications[0].SubjectID)
	}

	issueCtx := service.ContextWithUser(ctx, actor)
	if _, err := svc.AddIssueAssignees(issueCtx, repo.FullName, issue.Number, []string{assignee.Login}); err != nil {
		t.Fatalf("AddIssueAssignees: %v", err)
	}

	assigneeNotifications, err := svc.ListNotifications(ctx, assignee.ID, true, 100)
	if err != nil {
		t.Fatalf("ListNotifications(assignee): %v", err)
	}
	if len(assigneeNotifications) != 1 {
		t.Fatalf("expected 1 assignment notification, got %d", len(assigneeNotifications))
	}
	if assigneeNotifications[0].Type != service.NotificationTypeAssignment {
		t.Fatalf("expected assignment notification, got %q", assigneeNotifications[0].Type)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Reply target",
		AuthorLogin:  actor.Login,
		HeadRef:      "feature/reply-target",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	parent := &db.PRReviewComment{
		AuthorLogin: parentAuthor.Login,
		Body:        "Parent review comment",
		CommitID:    "abc123",
		Path:        "file.go",
		Line:        12,
	}
	if err := svc.CreatePRReviewComment(ctx, pr.ID, parent); err != nil {
		t.Fatalf("CreatePRReviewComment: %v", err)
	}

	reply, err := svc.ReplyToPRReviewComment(ctx, pr.ID, parent.ID, "reply body", replier.Login)
	if err != nil {
		t.Fatalf("ReplyToPRReviewComment: %v", err)
	}
	if reply.ID == 0 {
		t.Fatal("expected reply ID to be set")
	}

	replyNotifications, err := svc.ListNotifications(ctx, parentAuthor.ID, true, 100)
	if err != nil {
		t.Fatalf("ListNotifications(parentAuthor): %v", err)
	}
	if len(replyNotifications) != 1 {
		t.Fatalf("expected 1 reply notification, got %d", len(replyNotifications))
	}
	if replyNotifications[0].Type != service.NotificationTypeReply {
		t.Fatalf("expected reply notification, got %q", replyNotifications[0].Type)
	}
	if replyNotifications[0].SubjectType != service.NotificationSubjectPullRequest {
		t.Fatalf("expected pull request subject, got %q", replyNotifications[0].SubjectType)
	}
	if replyNotifications[0].SubjectID != pr.ID {
		t.Fatalf("expected PR subject ID %d, got %d", pr.ID, replyNotifications[0].SubjectID)
	}

	if err := svc.MarkAllNotificationsRead(ctx, parentAuthor.ID); err != nil {
		t.Fatalf("MarkAllNotificationsRead: %v", err)
	}

	unreadAfterMark, err := svc.ListNotifications(ctx, parentAuthor.ID, true, 100)
	if err != nil {
		t.Fatalf("ListNotifications(unread after mark): %v", err)
	}
	if len(unreadAfterMark) != 0 {
		t.Fatalf("expected 0 unread notifications, got %d", len(unreadAfterMark))
	}

	allAfterMark, err := svc.ListNotifications(ctx, parentAuthor.ID, false, 100)
	if err != nil {
		t.Fatalf("ListNotifications(all after mark): %v", err)
	}
	if len(allAfterMark) != 1 {
		t.Fatalf("expected 1 notification after mark-read, got %d", len(allAfterMark))
	}
	if !allAfterMark[0].Read {
		t.Fatal("expected notification to be marked read")
	}
	if allAfterMark[0].LastReadAt == nil {
		t.Fatal("expected last_read_at to be populated")
	}
}

func TestNotifications_DetectMentionsInIssueAndCommentBodies(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	author := db.User{Login: "mention-author", Name: "mention-author", Type: db.TypeUser}
	mentioned := db.User{Login: "mention-target", Name: "mention-target", Type: db.TypeUser}
	for _, user := range []*db.User{&author, &mentioned} {
		if err := svc.DB.Create(user).Error; err != nil {
			t.Fatalf("create user %s: %v", user.Login, err)
		}
	}

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: author.Login,
		Name:       "mention-bodies",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: repo.FullName,
		Title:        "Mention issue",
		Body:         "ping @mention-target and @mention-target again",
		AuthorLogin:  author.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	notifications, err := svc.ListNotifications(ctx, mentioned.ID, false, 100)
	if err != nil {
		t.Fatalf("ListNotifications(issue mention): %v", err)
	}
	if len(notifications) != 1 {
		t.Fatalf("expected 1 issue-body mention notification, got %d", len(notifications))
	}
	if notifications[0].LatestCommentURL == "" {
		t.Fatal("expected issue-body mention to retain a latest_comment_url")
	}

	if _, err := svc.CreateIssueComment(ctx, repo.FullName, issue.Number, "comment mention for @mention-target", author.Login, nil); err != nil {
		t.Fatalf("CreateIssueComment: %v", err)
	}

	notifications, err = svc.ListNotifications(ctx, mentioned.ID, false, 100)
	if err != nil {
		t.Fatalf("ListNotifications(comment mention): %v", err)
	}
	if len(notifications) != 1 {
		t.Fatalf("expected upserted mention notification count 1, got %d", len(notifications))
	}
	if notifications[0].LatestCommentURL == "" || !strings.Contains(notifications[0].LatestCommentURL, "/issues/comments/") {
		t.Fatalf("expected latest_comment_url to point at the mention comment, got %q", notifications[0].LatestCommentURL)
	}
}
