package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/mentions"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	NotificationTypeMention       = "mention"
	NotificationTypeAssignment    = "assignment"
	NotificationTypeReply         = "reply"
	NotificationTypeWorkflowEvent = "workflow_event"

	NotificationSubjectIssue       = "issue"
	NotificationSubjectPullRequest = "pull_request"
	NotificationSubjectWorkflowRun = "workflow_run"
)

// ListNotifications returns notifications for a user ordered by most recent update.
func (s *Service) ListNotifications(ctx context.Context, userID uint, unreadOnly bool, limit int) ([]db.Notification, error) {
	if userID == 0 {
		return []db.Notification{}, nil
	}
	if limit <= 0 || limit > defaultListLimit {
		limit = defaultListLimit
	}
	q := s.DBForCtx(ctx).
		Preload("Repository").
		Preload("Repository.Owner").
		Where("user_id = ?", userID).
		Order("updated_at DESC").
		Limit(limit)
	if unreadOnly {
		q = q.Where("`read` = ?", false)
	}
	var notifications []db.Notification
	if err := q.Find(&notifications).Error; err != nil {
		return nil, wrapErr(err)
	}
	return notifications, nil
}

// MarkAllNotificationsRead marks all notifications for a user as read.
func (s *Service) MarkAllNotificationsRead(ctx context.Context, userID uint) error {
	if userID == 0 {
		return nil
	}
	now := time.Now().UTC()
	return s.DBForCtx(ctx).
		Model(&db.Notification{}).
		Where("user_id = ? AND `read` = ?", userID, false).
		UpdateColumns(map[string]any{
			"read":         true,
			"last_read_at": &now,
		}).Error
}

// CreateMentionNotification creates a mention notification unless it is self-directed.
func (s *Service) CreateMentionNotification(ctx context.Context, userID, actorID uint, subjectType string, subjectID, repoID uint, latestCommentURL string) (db.Notification, error) {
	if userID == 0 || (actorID != 0 && actorID == userID) {
		return db.Notification{}, nil
	}
	return s.createNotification(ctx, userID, actorID, NotificationTypeMention, subjectType, subjectID, repoID, latestCommentURL)
}

// CreateAssignmentNotification creates an assignment notification unless it is self-directed.
func (s *Service) CreateAssignmentNotification(ctx context.Context, userID, actorID uint, subjectType string, subjectID, repoID uint) (db.Notification, error) {
	if userID == 0 || (actorID != 0 && actorID == userID) {
		return db.Notification{}, nil
	}
	return s.createNotification(ctx, userID, actorID, NotificationTypeAssignment, subjectType, subjectID, repoID, "")
}

// CreateReplyNotification creates a reply notification unless it is self-directed.
func (s *Service) CreateReplyNotification(ctx context.Context, userID, actorID uint, subjectType string, subjectID, repoID uint, latestCommentURL string) (db.Notification, error) {
	if userID == 0 || (actorID != 0 && actorID == userID) {
		return db.Notification{}, nil
	}
	return s.createNotification(ctx, userID, actorID, NotificationTypeReply, subjectType, subjectID, repoID, latestCommentURL)
}

// CreateWorkflowEventNotification creates a workflow completion notification.
func (s *Service) CreateWorkflowEventNotification(ctx context.Context, userID, actorID uint, workflowRunID, repoID uint) (db.Notification, error) {
	if userID == 0 {
		return db.Notification{}, nil
	}
	return s.createNotification(ctx, userID, actorID, NotificationTypeWorkflowEvent, NotificationSubjectWorkflowRun, workflowRunID, repoID, "")
}

func (s *Service) createNotification(ctx context.Context, userID, actorID uint, typ, subjectType string, subjectID, repoID uint, latestCommentURL string) (db.Notification, error) {
	if userID == 0 || subjectID == 0 || repoID == 0 || typ == "" || subjectType == "" {
		return db.Notification{}, ErrValidation
	}
	now := time.Now().UTC()
	values := db.Notification{
		UserID:           userID,
		Type:             typ,
		SubjectType:      subjectType,
		SubjectID:        subjectID,
		RepositoryID:     repoID,
		LatestCommentURL: latestCommentURL,
		Read:             false,
		LastReadAt:       nil,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if actorID != 0 {
		values.ActorID = &actorID
	}
	if err := s.DBForCtx(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "user_id"},
			{Name: "type"},
			{Name: "subject_type"},
			{Name: "subject_id"},
		},
		DoUpdates: clause.Assignments(map[string]any{
			"actor_id":           values.ActorID,
			"repository_id":      repoID,
			"latest_comment_url": latestCommentURL,
			"read":               false,
			"last_read_at":       nil,
			"updated_at":         now,
		}),
	}).Create(&values).Error; err != nil {
		return db.Notification{}, wrapErr(err)
	}

	var notification db.Notification
	if err := s.DBForCtx(ctx).
		Preload("Repository").
		Preload("Repository.Owner").
		Where("user_id = ? AND type = ? AND subject_type = ? AND subject_id = ?", userID, typ, subjectType, subjectID).
		First(&notification).Error; err != nil {
		return db.Notification{}, wrapErr(err)
	}
	return notification, nil
}

func (s *Service) createMentionNotificationsForBody(ctx context.Context, actorID uint, subjectType string, subjectID, repoID uint, latestCommentURL, body string) error {
	for _, login := range extractMentionLogins(body) {
		user, err := s.lookupUserByLoginCI(ctx, login)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return err
		}
		if _, err := s.CreateMentionNotification(ctx, user.ID, actorID, subjectType, subjectID, repoID, latestCommentURL); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) createAssignmentNotificationsForLogins(ctx context.Context, actorID uint, subjectType string, subjectID, repoID uint, logins []string) error {
	for _, login := range logins {
		user, err := s.lookupUserByLoginCI(ctx, login)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return err
		}
		if _, err := s.CreateAssignmentNotification(ctx, user.ID, actorID, subjectType, subjectID, repoID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) notificationSubjectForRepoNumber(ctx context.Context, repoID uint, issueNumber int) (string, uint, error) {
	var pr db.PullRequest
	if err := s.DBForCtx(ctx).Select("id").First(&pr, "repository_id = ? AND number = ?", repoID, issueNumber).Error; err == nil {
		return NotificationSubjectPullRequest, pr.ID, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", 0, wrapErr(err)
	}

	var issue db.Issue
	if err := s.DBForCtx(ctx).Select("id").First(&issue, "repository_id = ? AND number = ?", repoID, issueNumber).Error; err != nil {
		return "", 0, wrapErr(err)
	}
	return NotificationSubjectIssue, issue.ID, nil
}

func (s *Service) notificationActorIDForContext(ctx context.Context) uint {
	if user, ok := UserFromContext(ctx); ok {
		return user.ID
	}
	return 0
}

func (s *Service) lookupUserByLoginCI(ctx context.Context, login string) (db.User, error) {
	var user db.User
	if err := s.DBForCtx(ctx).Where("LOWER(login) = ?", strings.ToLower(strings.TrimSpace(login))).First(&user).Error; err != nil {
		return user, wrapErr(err)
	}
	return user, nil
}

func extractMentionLogins(body string) []string {
	return mentions.ExtractLogins(body)
}

func issueCommentURL(baseURL, repoFullName string, commentID uint) string {
	return fmt.Sprintf("%s/api/v3/repos/%s/issues/comments/%d", baseURL, repoFullName, commentID)
}

func prReviewCommentURL(baseURL, repoFullName string, commentID uint) string {
	return fmt.Sprintf("%s/api/v3/repos/%s/pulls/comments/%d", baseURL, repoFullName, commentID)
}
