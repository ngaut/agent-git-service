package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
)

// CommentResult is a minimal view of a created/updated IssueComment returned
// by AddCommentBy* functions, used by the GraphQL layer to build its response.
type CommentResult struct {
	ID        uint
	Body      string
	CreatedAt time.Time
}

const maxIssueCommentThreadDepth = 5

// ─── Issue Comments ───────────────────────────────────────────────────────────

// ListIssueComments returns all comments on an issue.
func (s *Service) ListIssueComments(ctx context.Context, repoFullName string, issueNumber int) ([]db.IssueComment, error) {
	return s.ListIssueCommentsWithFilters(ctx, repoFullName, issueNumber, "", "", "")
}

// ListIssueCommentsWithFilters returns comments for an issue with optional since/sort/direction filters.
func (s *Service) ListIssueCommentsWithFilters(ctx context.Context, repoFullName string, issueNumber int, since, sort, direction string) ([]db.IssueComment, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	return s.listIssueCommentsByRepoID(ctx, rep.ID, issueNumber, since, sort, direction, 1, defaultListLimit)
}

// ListIssueCommentsPaginated returns comments for an issue with filters and pagination.
func (s *Service) ListIssueCommentsPaginated(ctx context.Context, repoFullName string, issueNumber int, since, sort, direction string, page, perPage int) ([]db.IssueComment, int64, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.countIssueCommentsByRepoID(ctx, rep.ID, issueNumber, since)
	if err != nil {
		return nil, 0, err
	}
	comments, err := s.listIssueCommentsByRepoID(ctx, rep.ID, issueNumber, since, sort, direction, page, perPage)
	if err != nil {
		return nil, 0, err
	}
	return comments, total, nil
}

// ListRepoIssueCommentsPaginated returns all issue comments in a repository.
func (s *Service) ListRepoIssueCommentsPaginated(ctx context.Context, repoFullName string, since, sort, direction string, page, perPage int) ([]db.IssueComment, int64, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = defaultListLimit
	}
	if perPage > defaultListLimit {
		perPage = defaultListLimit
	}
	q := preloadIssueComment(s.DBForCtx(ctx)).Where("repository_id = ?", rep.ID)
	q, err = applyIssueCommentSinceFilter(q, since)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := q.Model(&db.IssueComment{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var comments []db.IssueComment
	order := sortOrder(issueCommentSortQualifier(sort, direction), "issue_comments")
	offset := (page - 1) * perPage
	if err := q.Order(order).Offset(offset).Limit(perPage).Find(&comments).Error; err != nil {
		return nil, 0, err
	}
	return comments, total, nil
}

// ListIssueCommentsByRepoID returns all comments on an issue using the internal repo ID.
// Used by the GraphQL layer and timeline which already have the repo ID from the preloaded model.
func (s *Service) ListIssueCommentsByRepoID(ctx context.Context, repoID uint, issueNumber int) ([]db.IssueComment, error) {
	return s.listIssueCommentsByRepoID(ctx, repoID, issueNumber, "", "", "", 1, defaultListLimit)
}

// ListIssueCommentsPinnedFirstByRepoID returns all comments on an issue using
// GraphQL-compatible ordering: pinned comments first, then oldest-first within
// each group.
func (s *Service) ListIssueCommentsPinnedFirstByRepoID(ctx context.Context, repoID uint, issueNumber int) ([]db.IssueComment, error) {
	var comments []db.IssueComment
	if err := preloadIssueComment(s.DBForCtx(ctx)).
		Where("repository_id = ? AND issue_number = ?", repoID, issueNumber).
		Order("issue_comments.is_pinned DESC").
		Order("issue_comments.created_at ASC").
		Order("issue_comments.id ASC").
		Limit(defaultListLimit).
		Find(&comments).Error; err != nil {
		return nil, err
	}
	return comments, nil
}

// ListIssueCommentsThreaded returns all comments for an issue with threading metadata.
// Comments are ordered by thread_root_id (nulls first), then by created_at.
// This allows efficient reconstruction of threaded conversations.
func (s *Service) ListIssueCommentsThreaded(ctx context.Context, repoID uint, issueNumber int) ([]db.IssueComment, error) {
	var comments []db.IssueComment
	if err := preloadIssueComment(s.DBForCtx(ctx)).
		Where("repository_id = ? AND issue_number = ?", repoID, issueNumber).
		Order("issue_comments.thread_root_id IS NOT NULL").
		Order("issue_comments.thread_root_id").
		Order("issue_comments.created_at ASC").
		Order("issue_comments.id ASC").
		Limit(defaultListLimit).
		Find(&comments).Error; err != nil {
		return nil, err
	}
	return comments, nil
}

func (s *Service) listIssueCommentsByRepoID(ctx context.Context, repoID uint, issueNumber int, since, sort, direction string, page, perPage int) ([]db.IssueComment, error) {
	var comments []db.IssueComment
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = defaultListLimit
	}
	if perPage > defaultListLimit {
		perPage = defaultListLimit
	}
	offset := (page - 1) * perPage
	q := preloadIssueComment(s.DBForCtx(ctx)).
		Where("repository_id = ? AND issue_number = ?", repoID, issueNumber)
	var err error
	q, err = applyIssueCommentSinceFilter(q, since)
	if err != nil {
		return nil, err
	}
	order := sortOrder(issueCommentSortQualifier(sort, direction), "issue_comments")
	if err := q.Order(order).Offset(offset).Limit(perPage).Find(&comments).Error; err != nil {
		return nil, err
	}
	return comments, nil
}

func (s *Service) countIssueCommentsByRepoID(ctx context.Context, repoID uint, issueNumber int, since string) (int64, error) {
	var count int64
	q := s.DBForCtx(ctx).Model(&db.IssueComment{}).
		Where("repository_id = ? AND issue_number = ?", repoID, issueNumber)
	var err error
	q, err = applyIssueCommentSinceFilter(q, since)
	if err != nil {
		return 0, err
	}
	if err := q.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func issueCommentSortQualifier(sort, direction string) string {
	sort = strings.TrimSpace(strings.ToLower(sort))
	direction = strings.TrimSpace(strings.ToLower(direction))
	if direction != "asc" && direction != "desc" {
		direction = "asc"
	}
	if sort == "" {
		sort = "created"
	}
	if strings.Contains(sort, "-") {
		return sort
	}
	switch sort {
	case "created", "updated":
		return sort + "-" + direction
	default:
		return "created-asc"
	}
}

func applyIssueCommentSinceFilter(q *gorm.DB, since string) (*gorm.DB, error) {
	since = strings.TrimSpace(since)
	if since == "" {
		return q, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, since)
	if err != nil {
		return q, fmt.Errorf("%w: since must be ISO 8601", ErrValidation)
	}
	return q.Where("issue_comments.updated_at >= ?", parsed), nil
}

// GetIssueCommentByID returns a single issue comment by its DB ID.
func (s *Service) GetIssueCommentByID(ctx context.Context, id uint) (db.IssueComment, error) {
	var c db.IssueComment
	err := preloadIssueComment(s.DBForCtx(ctx)).First(&c, id).Error
	if err != nil {
		return c, wrapErr(err)
	}
	return c, nil
}

// GetIssueCommentThreadDepth returns a comment's nesting depth within its thread.
// Top-level comments have depth 1, direct replies depth 2, and so on.
func (s *Service) GetIssueCommentThreadDepth(ctx context.Context, id uint) (int, error) {
	depth := 0
	currentID := id
	visited := make(map[uint]struct{})

	for {
		if _, seen := visited[currentID]; seen {
			return 0, fmt.Errorf("issue comment thread cycle detected at comment #%d", currentID)
		}
		visited[currentID] = struct{}{}

		var comment db.IssueComment
		if err := s.DBForCtx(ctx).Select("id", "in_reply_to_id").First(&comment, currentID).Error; err != nil {
			return 0, wrapErrf(err, "issue comment #%d", currentID)
		}

		depth++
		if comment.InReplyToID == nil {
			return depth, nil
		}
		currentID = *comment.InReplyToID
	}
}

func (s *Service) validateIssueCommentReplyDepth(ctx context.Context, parentID uint) error {
	depth, err := s.GetIssueCommentThreadDepth(ctx, parentID)
	if err != nil {
		return err
	}
	if depth >= maxIssueCommentThreadDepth {
		return fmt.Errorf("%w: reply would exceed maximum issue comment thread depth of %d levels", ErrValidation, maxIssueCommentThreadDepth)
	}
	return nil
}

// PinIssueComment toggles the pinned state of an issue comment.
func (s *Service) PinIssueComment(ctx context.Context, id uint, pin bool) error {
	comment, err := s.GetIssueCommentByID(ctx, id)
	if err != nil {
		return err
	}
	if err := s.requireRepoPermission(ctx, comment.RepositoryID, RepoPermissionWrite); err != nil {
		return err
	}
	if viewer, ok := UserFromContext(ctx); ok && viewer.ID != 0 && viewer.ID != comment.AuthorID {
		return ErrForbidden
	}
	if comment.IsPinned == pin {
		return nil
	}

	updates := map[string]any{"is_pinned": pin}
	if pin {
		now := time.Now()
		updates["pinned_at"] = &now
	} else {
		updates["pinned_at"] = nil
	}

	return s.DBForCtx(ctx).Model(&db.IssueComment{}).Where("id = ?", id).Updates(updates).Error
}

// GetPinnedComments returns pinned comments for an issue.
func (s *Service) GetPinnedComments(ctx context.Context, repoID uint, issueNumber int) ([]db.IssueComment, error) {
	if err := s.requireRepoPermission(ctx, repoID, RepoPermissionRead); err != nil {
		return nil, err
	}

	var comments []db.IssueComment
	if err := preloadIssueComment(s.DBForCtx(ctx)).
		Where("repository_id = ? AND issue_number = ? AND is_pinned = ?", repoID, issueNumber, true).
		Order("pinned_at desc").
		Order("created_at asc").
		Find(&comments).Error; err != nil {
		return nil, err
	}
	return comments, nil
}

// UpdateIssueComment updates the body of an issue comment identified by its DB ID.
// Returns ErrNotFound if the comment does not exist.
func (s *Service) UpdateIssueComment(ctx context.Context, id uint, body string) error {
	if err := validateBodyFitsMediumText(body); err != nil {
		return err
	}
	comment, err := s.GetIssueCommentByID(ctx, id)
	if err != nil {
		return err
	}
	if err := checkAffected(s.DBForCtx(ctx).Model(&db.IssueComment{}).Where("id = ?", id).Update("body", body)); err != nil {
		return err
	}
	comment.Body = db.LargeText(body)
	comment.UpdatedAt = time.Now()
	return s.syncIssueCommentReferences(ctx, comment)
}

// DeleteIssueComment deletes an issue comment by its DB ID.
func (s *Service) DeleteIssueComment(ctx context.Context, id uint) error {
	if _, err := s.GetIssueCommentByID(ctx, id); err != nil {
		return err
	}
	return s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("source_type = ? AND source_comment_id = ?", issueReferenceSourceIssueComment, id).
			Delete(&db.IssueReference{}).Error; err != nil {
			return err
		}
		return checkAffected(tx.Delete(&db.IssueComment{}, id))
	})
}

// ReplyToIssueComment creates a reply to an existing issue comment.
// It sets InReplyToID to the parent comment and ThreadRootID to the root of the thread.
func (s *Service) ReplyToIssueComment(ctx context.Context, repoID uint, issueNumber int, parentID uint, body, authorLogin string) (db.IssueComment, error) {
	var parent db.IssueComment
	if err := preloadIssueComment(s.DBForCtx(ctx)).First(&parent, parentID).Error; err != nil {
		return db.IssueComment{}, wrapErrf(err, "parent comment #%d", parentID)
	}
	if err := s.validateIssueCommentReplyDepth(ctx, parentID); err != nil {
		return db.IssueComment{}, err
	}
	// Determine the thread root: if parent is a reply, use its thread_root_id; otherwise use parent's ID
	threadRootID := parent.ID
	if parent.ThreadRootID != nil {
		threadRootID = *parent.ThreadRootID
	}
	reply := db.IssueComment{
		RepositoryID: repoID,
		IssueNumber:  issueNumber,
		Body:         db.LargeText(body),
		AuthorID:     parent.AuthorID, // will be overwritten below
		InReplyToID:  &parentID,
		ThreadRootID: &threadRootID,
	}
	// Resolve author
	author, authorErr := s.GetUser(ctx, authorLogin)
	if authorErr != nil {
		slog.Warn("ReplyToIssueComment: resolve author", "login", authorLogin, "error", authorErr)
	}
	reply.AuthorID = author.ID
	if err := s.DBForCtx(ctx).Create(&reply).Error; err != nil {
		return db.IssueComment{}, err
	}
	if err := s.DBForCtx(ctx).Preload("Author").First(&reply, reply.ID).Error; err != nil {
		return reply, wrapErr(err)
	}
	if err := s.syncIssueCommentReferences(ctx, reply); err != nil {
		return reply, err
	}
	// Create notifications
	var subjectType string
	var subjectID uint
	var repo db.Repository
	if err := s.DBForCtx(ctx).Select("full_name").First(&repo, repoID).Error; err != nil {
		return reply, wrapErr(err)
	}
	if st, sid, err := s.notificationSubjectForRepoNumber(ctx, repoID, issueNumber); err == nil {
		subjectType = st
		subjectID = sid
		if err := s.createMentionNotificationsForBody(
			ctx,
			author.ID,
			subjectType,
			subjectID,
			repoID,
			issueCommentURL(s.BaseURL, repo.FullName, reply.ID),
			string(reply.Body),
		); err != nil {
			return reply, err
		}
	}
	// Notify the parent comment author about the reply
	parentAuthor, parentErr := s.GetUser(ctx, parent.Author.Login)
	if parentErr == nil && parentAuthor.ID != author.ID {
		if _, err := s.CreateReplyNotification(ctx, parentAuthor.ID, author.ID, NotificationSubjectIssue, subjectID, repoID, issueCommentURL(s.BaseURL, repo.FullName, reply.ID)); err != nil {
			return reply, err
		}
	}
	return reply, nil
}

// addComment is the shared implementation for adding a comment to an issue or PR.
// If inReplyToID is provided, the comment will be a reply to that comment.
func (s *Service) addComment(ctx context.Context, repoID uint, issueNumber int, body, authorLogin string, inReplyToID *uint) (db.IssueComment, error) {
	if err := validateBodyFitsMediumText(body); err != nil {
		return db.IssueComment{}, err
	}
	author, authorErr := s.GetUser(ctx, authorLogin)
	if authorErr != nil {
		slog.Warn("addComment: resolve author", "login", authorLogin, "error", authorErr)
	}
	c := db.IssueComment{
		RepositoryID: repoID,
		IssueNumber:  issueNumber,
		Body:         db.LargeText(body),
		AuthorID:     author.ID,
	}
	// Handle reply threading
	if inReplyToID != nil {
		var parent db.IssueComment
		if err := s.DBForCtx(ctx).First(&parent, *inReplyToID).Error; err != nil {
			return db.IssueComment{}, wrapErrf(err, "parent comment #%d", *inReplyToID)
		}
		c.InReplyToID = inReplyToID
		// Determine thread root: if parent is already a reply, use its thread_root_id
		if parent.ThreadRootID != nil {
			c.ThreadRootID = parent.ThreadRootID
		} else {
			c.ThreadRootID = &parent.ID
		}
	}
	if err := s.DBForCtx(ctx).Create(&c).Error; err != nil {
		return db.IssueComment{}, err
	}
	if err := s.DBForCtx(ctx).Preload("Author").First(&c, c.ID).Error; err != nil {
		return c, wrapErr(err)
	}
	if err := s.syncIssueCommentReferences(ctx, c); err != nil {
		return c, err
	}
	var subjectType string
	var subjectID uint
	var repo db.Repository
	if err := s.DBForCtx(ctx).Select("full_name").First(&repo, repoID).Error; err == nil {
		if st, sid, err := s.notificationSubjectForRepoNumber(ctx, repoID, issueNumber); err == nil {
			subjectType = st
			subjectID = sid
			if err := s.createMentionNotificationsForBody(
				ctx,
				author.ID,
				subjectType,
				subjectID,
				repoID,
				issueCommentURL(s.BaseURL, repo.FullName, c.ID),
				string(c.Body),
			); err != nil {
				return c, err
			}
		}
	}
	// If this is a reply, notify the parent author
	if inReplyToID != nil {
		var parent db.IssueComment
		if err := s.DBForCtx(ctx).Preload("Author").First(&parent, *inReplyToID).Error; err == nil {
			if parent.Author.ID != author.ID {
				if _, err := s.CreateReplyNotification(ctx, parent.Author.ID, author.ID, NotificationSubjectIssue, subjectID, repoID, issueCommentURL(s.BaseURL, repo.FullName, c.ID)); err != nil {
					return c, err
				}
			}
		}
	}
	return c, nil
}

// AddCommentByIssueID adds a comment to an issue using the issue's internal DB ID.
// This is the path taken by the GraphQL addComment mutation.
func (s *Service) AddCommentByIssueID(ctx context.Context, issueID uint, body, authorLogin string) (db.IssueComment, error) {
	issue, err := s.GetIssueByID(ctx, issueID)
	if err != nil {
		return db.IssueComment{}, fmt.Errorf("issue not found: %w", err)
	}
	return s.addComment(ctx, issue.RepositoryID, issue.Number, body, authorLogin, nil)
}

// AddCommentByPRID adds a comment to a pull request using the PR's internal DB ID.
func (s *Service) AddCommentByPRID(ctx context.Context, prID uint, body, authorLogin string) (db.IssueComment, error) {
	pr, err := s.GetPRByID(ctx, prID)
	if err != nil {
		return db.IssueComment{}, fmt.Errorf("pull request not found: %w", err)
	}
	return s.addComment(ctx, pr.RepositoryID, pr.Number, body, authorLogin, nil)
}

// ─── Reload helpers (used by GQL mutations after label/assignee attach) ───────

// ReloadIssue re-fetches an issue by ID with all associations.
func (s *Service) ReloadIssue(ctx context.Context, id uint) (db.Issue, error) {
	return s.GetIssueByID(ctx, id)
}

// ReloadPR re-fetches a pull request by ID with all associations.
func (s *Service) ReloadPR(ctx context.Context, id uint) (db.PullRequest, error) {
	return s.GetPRByID(ctx, id)
}

// ─── Label & Assignee attachment ──────────────────────────────────────────────

// AttachLabelsAndAssignees attaches label/assignee GQL node IDs to an issue or PR.
// issueID and prID must not both be non-nil simultaneously.
func (s *Service) AttachLabelsAndAssignees(ctx context.Context, issueID *uint, prID *uint, labelIDs []string, assigneeIDs []string) error {
	if issueID != nil {
		if _, err := s.GetIssueByID(ctx, *issueID); err != nil {
			return err
		}
	}
	if prID != nil {
		if _, err := s.GetPRByID(ctx, *prID); err != nil {
			return err
		}
	}
	if err := s.attachLabels(ctx, issueID, prID, labelIDs); err != nil {
		return err
	}
	return s.attachAssignees(ctx, issueID, prID, assigneeIDs)
}

// attachLabels resolves GQL label node IDs ("Label_<dbID>") and inserts
// join-table rows for the target issue or PR.
// Uses INSERT … WHERE NOT EXISTS for idempotent TiDB/MySQL-compatible writes.
func (s *Service) attachLabels(ctx context.Context, issueID *uint, prID *uint, labelIDs []string) error {
	for _, idStr := range labelIDs {
		if !strings.HasPrefix(idStr, "Label_") {
			continue
		}
		dbID := strings.TrimPrefix(idStr, "Label_")
		var label db.Label
		if s.DBForCtx(ctx).First(&label, "id = ?", dbID).Error != nil {
			continue
		}
		if issueID != nil {
			if err := s.AddIssueLabelByID(ctx, *issueID, label.ID); err != nil {
				return fmt.Errorf("attachLabels: issue_labels: %w", err)
			}
		}
		if prID != nil {
			if err := s.AddPRLabelByID(ctx, *prID, label.ID); err != nil {
				return fmt.Errorf("attachLabels: pr_labels: %w", err)
			}
		}
	}
	return nil
}

// attachAssignees resolves GQL user node IDs ("User_<dbID>") to logins and
// merges them with any existing assignees on the target issue or PR.
// Existing assignees are preserved (append, not overwrite).
func (s *Service) attachAssignees(ctx context.Context, issueID *uint, prID *uint, assigneeIDs []string) error {
	var newLogins []string
	for _, idStr := range assigneeIDs {
		if !strings.HasPrefix(idStr, "User_") {
			continue
		}
		dbID := strings.TrimPrefix(idStr, "User_")
		var user db.User
		if s.DBForCtx(ctx).First(&user, "id = ?", dbID).Error == nil {
			newLogins = append(newLogins, user.Login)
		}
	}
	if len(newLogins) == 0 {
		return nil
	}
	if issueID != nil {
		var issue db.Issue
		if err := s.DBForCtx(ctx).Select("assignee_logins").First(&issue, *issueID).Error; err != nil {
			return fmt.Errorf("attachAssignees: fetch issue: %w", err)
		}
		// Emit assigned events only for truly new logins.
		existing := loginSet(issue.AssigneeLogins)
		var added []string
		for _, login := range newLogins {
			if !existing[login] {
				added = append(added, login)
			}
		}
		merged := mergeLogins(issue.AssigneeLogins, newLogins)
		if err := s.DBForCtx(ctx).Model(&db.Issue{}).Where("id = ?", *issueID).Update("assignee_logins", merged).Error; err != nil {
			return fmt.Errorf("attachAssignees: issue: %w", err)
		}
		for _, login := range added {
			if err := s.recordIssueEvent(ctx, *issueID, issueEventAssigned, issueEventData{
				AssigneeLogin: strPtr(login),
			}); err != nil {
				return err
			}
		}
		if len(added) > 0 {
			var repoID uint
			if err := s.DBForCtx(ctx).Model(&db.Issue{}).Where("id = ?", *issueID).Pluck("repository_id", &repoID).Error; err != nil {
				return fmt.Errorf("attachAssignees: issue repo: %w", err)
			}
			if err := s.createAssignmentNotificationsForLogins(ctx, s.notificationActorIDForContext(ctx), NotificationSubjectIssue, *issueID, repoID, added); err != nil {
				return err
			}
		}
	}
	if prID != nil {
		var pr db.PullRequest
		if err := s.DBForCtx(ctx).Select("assignee_logins").First(&pr, *prID).Error; err != nil {
			return fmt.Errorf("attachAssignees: fetch pr: %w", err)
		}
		existing := loginSet(pr.AssigneeLogins)
		var added []string
		for _, login := range newLogins {
			if !existing[login] {
				added = append(added, login)
			}
		}
		merged := mergeLogins(pr.AssigneeLogins, newLogins)
		if err := s.DBForCtx(ctx).Model(&db.PullRequest{}).Where("id = ?", *prID).Update("assignee_logins", merged).Error; err != nil {
			return fmt.Errorf("attachAssignees: pr: %w", err)
		}
		if len(added) > 0 {
			var repoID uint
			if err := s.DBForCtx(ctx).Model(&db.PullRequest{}).Where("id = ?", *prID).Pluck("repository_id", &repoID).Error; err != nil {
				return fmt.Errorf("attachAssignees: pr repo: %w", err)
			}
			if err := s.createAssignmentNotificationsForLogins(ctx, s.notificationActorIDForContext(ctx), NotificationSubjectPullRequest, *prID, repoID, added); err != nil {
				return err
			}
		}
	}
	return nil
}

// mergeLogins appends newLogins to existing (comma-separated) without duplicates.
func mergeLogins(existing string, newLogins []string) string {
	seen := make(map[string]bool)
	var all []string
	if existing != "" {
		for _, l := range strings.Split(existing, ",") {
			l = strings.TrimSpace(l)
			if l != "" && !seen[l] {
				seen[l] = true
				all = append(all, l)
			}
		}
	}
	for _, l := range newLogins {
		if !seen[l] {
			seen[l] = true
			all = append(all, l)
		}
	}
	return strings.Join(all, ",")
}

// removeLogins removes specified logins from a comma-separated string.
func removeLogins(existing string, toRemove []string) string {
	remove := make(map[string]bool)
	for _, l := range toRemove {
		remove[strings.TrimSpace(l)] = true
	}
	var kept []string
	if existing != "" {
		for _, l := range strings.Split(existing, ",") {
			l = strings.TrimSpace(l)
			if l != "" && !remove[l] {
				kept = append(kept, l)
			}
		}
	}
	return strings.Join(kept, ",")
}

func loginSet(logins string) map[string]bool {
	out := make(map[string]bool)
	if logins == "" {
		return out
	}
	for _, l := range strings.Split(logins, ",") {
		l = strings.TrimSpace(l)
		if l != "" {
			out[l] = true
		}
	}
	return out
}

func sliceLoginSet(logins []string) map[string]bool {
	out := make(map[string]bool)
	for _, l := range logins {
		l = strings.TrimSpace(l)
		if l != "" {
			out[l] = true
		}
	}
	return out
}

func splitLogins(logins string) []string {
	if logins == "" {
		return nil
	}
	parts := strings.Split(logins, ",")
	out := make([]string, 0, len(parts))
	for _, l := range parts {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
