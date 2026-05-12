package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"gh-server/internal/db"
	searchsvc "gh-server/internal/service/search"
)

// NextIssueNumber returns the next sequential issue number within a repo.
func (s *Service) NextIssueNumber(ctx context.Context, repoID uint) (int, error) {
	return nextIssueOrPRNumber(s, ctx, repoID)
}

// CreateIssueInput holds issue creation parameters.
type CreateIssueInput struct {
	RepoFullName string
	Title        string
	Body         string
	AuthorLogin  string
	Labels       []string // label names to apply after creation
	State        *string  // optional: "closed" to create issue in closed state
	StateReason  *string  // optional: "not_planned" or "completed"
}

// CreateIssue creates an issue in the repository.
// Retries on duplicate number to handle concurrent creation.
func (s *Service) CreateIssue(ctx context.Context, in CreateIssueInput) (db.Issue, error) {
	if err := validateBodyFitsMediumText(in.Body); err != nil {
		return db.Issue{}, fmt.Errorf("service: create issue: %w", err)
	}
	rep, err := s.getRepoBase(ctx, in.RepoFullName)
	if err != nil {
		return db.Issue{}, fmt.Errorf("service: create issue: repo: %w", err)
	}
	var (
		author    db.User
		authorErr error
	)
	if currentUser, ok := UserFromContext(ctx); ok && strings.EqualFold(currentUser.Login, in.AuthorLogin) {
		author = currentUser
	} else {
		author, authorErr = s.GetUser(ctx, in.AuthorLogin)
		if authorErr != nil {
			slog.Warn("CreateIssue: resolve author", "login", in.AuthorLogin, "error", authorErr)
		}
	}
	// Resolve repo owner — reuse auth user if they are the owner (common case).
	var repoOwner db.User
	if author.ID == rep.OwnerID {
		repoOwner = author
	} else if currentUser, ok := UserFromContext(ctx); ok && currentUser.ID == rep.OwnerID {
		repoOwner = currentUser
	} else {
		if err := s.DBForCtx(ctx).
			Select("id", "login", "name", "type", "site_admin").
			First(&repoOwner, rep.OwnerID).Error; err != nil {
			return db.Issue{}, fmt.Errorf("service: create issue: repo owner: %w", wrapErrf(err, "user %d", rep.OwnerID))
		}
	}

	// Determine initial state and state_reason
	initialState := db.StateOpen
	var initialStateReason string
	if in.State != nil && *in.State == db.StateClosed {
		initialState = db.StateClosed
		if in.StateReason != nil {
			initialStateReason = *in.StateReason
		} else {
			initialStateReason = db.StateReasonCompleted
		}
	}

	const maxRetries = 5
	for attempt := 0; attempt < maxRetries; attempt++ {
		// Pre-resolve labels before transaction to avoid holding locks during lookups.
		var resolvedLabels []db.Label
		if len(in.Labels) > 0 {
			var err error
			resolvedLabels, err = s.resolveRepoLabels(ctx, rep.ID, in.Labels)
			if err != nil {
				slog.Warn("CreateIssue: resolve labels", "error", err)
			}
		}

		// Pre-resolve mentioned users before transaction to avoid holding locks during lookups.
		mentionLogins := extractMentionLogins(in.Body)
		var mentionUsers []db.User
		for _, login := range mentionLogins {
			user, err := s.lookupUserByLoginCI(ctx, login)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					continue
				}
				return db.Issue{}, fmt.Errorf("service: create issue: lookup mention user: %w", err)
			}
			mentionUsers = append(mentionUsers, user)
		}

		var issue db.Issue
		var closedAt *time.Time
		if initialState == db.StateClosed {
			now := time.Now()
			closedAt = &now
		}
		if err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
			if err := lockRepoForNumbering(tx, rep.ID); err != nil {
				return err
			}
			num, err := nextIssueOrPRNumberTx(tx, rep.ID)
			if err != nil {
				return err
			}
			issue = db.Issue{
				Number:       num,
				RepositoryID: rep.ID,
				Title:        in.Title,
				Body:         db.LargeText(in.Body),
				State:        initialState,
				StateReason:  initialStateReason,
				ClosedAt:     closedAt,
				AuthorID:     author.ID,
			}
			if err := tx.Create(&issue).Error; err != nil {
				return err
			}

			// Record event: opened if created open, closed if created closed
			actor := in.AuthorLogin
			if initialState == db.StateClosed {
				ev := db.IssueEvent{
					IssueID:     issue.ID,
					EventType:   issueEventClosed,
					ActorLogin:  actor,
					StateReason: strPtr(initialStateReason),
				}
				if err := tx.Create(&ev).Error; err != nil {
					return fmt.Errorf("record closed event: %w", err)
				}
			} else {
				ev := db.IssueEvent{
					IssueID:    issue.ID,
					EventType:  issueEventOpened,
					ActorLogin: actor,
				}
				if err := tx.Create(&ev).Error; err != nil {
					return fmt.Errorf("record opened event: %w", err)
				}
			}

			// Apply labels if resolved successfully
			if len(resolvedLabels) > 0 {
				for _, label := range resolvedLabels {
					link := issueLabelLink{IssueID: issue.ID, LabelID: label.ID}
					res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&link)
					if res.Error != nil {
						return fmt.Errorf("add label junction: %w", res.Error)
					}
					if res.RowsAffected > 0 {
						ev := db.IssueEvent{
							IssueID:    issue.ID,
							EventType:  issueEventLabeled,
							ActorLogin: actor,
							LabelName:  strPtr(label.Name),
						}
						if err := tx.Create(&ev).Error; err != nil {
							return fmt.Errorf("record labeled event: %w", err)
						}
					}
				}
				issue.Labels = resolvedLabels
			}

			// Create mention notifications
			for _, user := range mentionUsers {
				if user.ID == 0 || (actor != "" && user.Login == actor) {
					continue
				}
				notif := db.Notification{
					UserID:           user.ID,
					ActorID:          &author.ID,
					Type:             NotificationTypeMention,
					SubjectType:      NotificationSubjectIssue,
					SubjectID:        issue.ID,
					RepositoryID:     issue.RepositoryID,
					LatestCommentURL: fmt.Sprintf("%s/api/v3/repos/%s/issues/%d", s.BaseURL, rep.FullName, issue.Number),
					Read:             false,
				}
				if err := tx.Create(&notif).Error; err != nil {
					return fmt.Errorf("create mention notification: %w", err)
				}
			}

			if err := s.syncIssueBodyReferences(ContextWithDB(ctx, tx), issue); err != nil {
				return fmt.Errorf("sync issue references: %w", err)
			}

			return nil
		}); err != nil {
			if isDuplicateErr(err) || isSQLiteLockErr(err) {
				time.Sleep(retryDelay(attempt))
				continue
			}
			return db.Issue{}, fmt.Errorf("service: create issue: %w", err)
		}
		// Populate associations in-memory to avoid a full preload round-trip.
		// The final preload (if needed) is done by the handler.
		issue.Author = author
		issue.Repository = db.Repository{
			ID:       rep.ID,
			FullName: rep.FullName,
			OwnerID:  rep.OwnerID,
			Owner:    repoOwner,
		}
		// Generate and store embedding for semantic search (fire-and-forget).
		s.EmbedIssue(ctx, issue.ID, issue.Title, string(issue.Body))
		return issue, nil
	}
	return db.Issue{}, fmt.Errorf("service: create issue: failed after %d retries", maxRetries)
}

// UpdateIssueInput for partial issue update.
type UpdateIssueInput struct {
	Title            *string
	Body             *string
	State            *string
	StateReason      *string
	Locked           *bool
	ActiveLockReason *string
}

// UpdateIssue applies a partial update.
func (s *Service) UpdateIssue(ctx context.Context, repoFullName string, number int, in UpdateIssueInput) (db.Issue, error) {
	if in.Body != nil {
		if err := validateBodyFitsMediumText(*in.Body); err != nil {
			return db.Issue{}, err
		}
	}
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return db.Issue{}, err
	}
	var issue db.Issue
	if err := s.DBForCtx(ctx).First(&issue, "repository_id = ? AND number = ?", rep.ID, number).Error; err != nil {
		return db.Issue{}, wrapErrf(err, "issue #%d", number)
	}

	origTitle := issue.Title
	origBody := issue.Body
	origState := issue.State
	origLocked := issue.Locked
	origLockReason := issue.ActiveLockReason
	if in.Title != nil {
		issue.Title = *in.Title
	}
	if in.Body != nil {
		issue.Body = db.LargeText(*in.Body)
	}
	if in.State != nil {
		issue.State = *in.State
		if *in.State == db.StateClosed {
			now := time.Now()
			issue.ClosedAt = &now
			if in.StateReason != nil {
				issue.StateReason = *in.StateReason
			} else if issue.StateReason == "" || issue.StateReason == db.StateReasonReopened {
				issue.StateReason = db.StateReasonCompleted
			}
			if origState != db.StateClosed {
				if u, ok := UserFromContext(ctx); ok {
					issue.ClosedByLogin = u.Login
				}
			}
		} else {
			issue.ClosedAt = nil
			issue.StateReason = db.StateReasonReopened
			if origState == db.StateClosed {
				issue.ClosedByLogin = ""
			}
		}
	} else if in.StateReason != nil {
		issue.StateReason = *in.StateReason
	}
	if in.Locked != nil {
		issue.Locked = *in.Locked
	}
	if in.ActiveLockReason != nil {
		issue.ActiveLockReason = *in.ActiveLockReason
	}
	if err := s.DBForCtx(ctx).Save(&issue).Error; err != nil {
		return issue, err
	}
	if in.Body != nil && issue.Body != origBody {
		if err := s.syncIssueBodyReferences(ctx, issue); err != nil {
			return issue, err
		}
	}
	var events []struct {
		typ  string
		data issueEventData
	}
	if issue.Title != origTitle {
		events = append(events, struct {
			typ  string
			data issueEventData
		}{typ: issueEventRenamed, data: issueEventData{
			OldTitle: strPtr(origTitle),
			NewTitle: strPtr(issue.Title),
		}})
	}
	if issue.State != origState {
		eventType := issueEventReopened
		if issue.State == db.StateClosed {
			eventType = issueEventClosed
		}
		events = append(events, struct {
			typ  string
			data issueEventData
		}{typ: eventType, data: issueEventData{
			StateReason: strPtr(issue.StateReason),
		}})
	}
	if issue.Locked != origLocked {
		eventType := issueEventUnlocked
		data := issueEventData{}
		if issue.Locked {
			eventType = issueEventLocked
			data.LockReason = strPtr(issue.ActiveLockReason)
		} else if origLockReason != "" {
			data.LockReason = strPtr(origLockReason)
		}
		events = append(events, struct {
			typ  string
			data issueEventData
		}{typ: eventType, data: data})
	}
	for _, ev := range events {
		if err := s.recordIssueEvent(ctx, issue.ID, ev.typ, ev.data); err != nil {
			return issue, err
		}
	}
	// Re-embed if title or body actually changed.
	if issue.Title != origTitle || issue.Body != origBody {
		s.EmbedIssue(ctx, issue.ID, issue.Title, string(issue.Body))
	}
	if err := preloadIssue(s.DBForCtx(ctx)).First(&issue, issue.ID).Error; err != nil {
		return issue, wrapErr(err)
	}
	return issue, nil
}

// GetIssue fetches a single issue by repo full name and number.
func (s *Service) GetIssue(ctx context.Context, repoFullName string, number int) (db.Issue, error) {
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return db.Issue{}, err
	}
	var issue db.Issue
	if err := preloadIssue(s.DBForCtx(ctx)).
		First(&issue, "repository_id = ? AND number = ?", rep.ID, number).Error; err != nil {
		return issue, wrapErrf(err, "issue #%d", number)
	}
	return issue, nil
}

// CountIssuesByRepoID returns the count of open issues for a repo by ID.
func (s *Service) CountIssuesByRepoID(ctx context.Context, repoID uint) int {
	var count int64
	if err := s.DBForCtx(ctx).Model(&db.Issue{}).Where("repository_id = ? AND state = ?", repoID, db.StateOpen).Count(&count).Error; err != nil {
		slog.Error("CountIssuesByRepoID", "error", err)
	}
	return int(count)
}

// ListIssues returns issues for a repo filtered by state and optionally by labels.
// labels is a comma-separated list of label names; only issues with ALL listed labels are returned.
func (s *Service) ListIssues(ctx context.Context, repoFullName, state, labels, sort, direction, milestone, since string) ([]db.Issue, error) {
	return s.listIssues(ctx, repoFullName, state, labels, sort, direction, milestone, since, false)
}

// ListIssuesForREST returns issues for the REST list endpoint while omitting the body payload.
func (s *Service) ListIssuesForREST(ctx context.Context, repoFullName, state, labels, sort, direction, milestone, since string) ([]db.Issue, error) {
	return s.listIssues(ctx, repoFullName, state, labels, sort, direction, milestone, since, true)
}

func (s *Service) listIssues(ctx context.Context, repoFullName, state, labels, sort, direction, milestone, since string, omitBody bool) ([]db.Issue, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	q := preloadIssue(s.DBForCtx(ctx)).Where("repository_id = ?", rep.ID)
	if omitBody {
		q = q.Omit("Body")
	}
	if state != "all" && state != "" {
		q = q.Where("state = ?", state)
	}
	if labels != "" {
		var noResults bool
		q, noResults, err = searchsvc.ApplyRepoIssueLabelFilters(q, s.DBForCtx(ctx), rep.ID, labels)
		if err != nil {
			return nil, err
		}
		if noResults {
			return []db.Issue{}, nil
		}
	}
	q, err = applyIssueSinceFilter(q, since)
	if err != nil {
		return nil, err
	}
	var noResults bool
	q, noResults = applyIssueMilestoneFilter(q, s.DBForCtx(ctx), rep.ID, milestone)
	if noResults {
		return []db.Issue{}, nil
	}
	var issues []db.Issue
	if err := q.Order(sortOrder(issueSortQualifier(sort, direction), "issues")).Limit(defaultListLimit).Find(&issues).Error; err != nil {
		return nil, err
	}
	return issues, nil
}

func issueSortQualifier(sort, direction string) string {
	sort = strings.TrimSpace(strings.ToLower(sort))
	direction = strings.TrimSpace(strings.ToLower(direction))
	if direction != "asc" && direction != "desc" {
		direction = "desc"
	}
	if sort == "" {
		sort = "created"
	}
	if strings.Contains(sort, "-") {
		return sort
	}
	switch sort {
	case "created", "updated", "comments":
		return sort + "-" + direction
	default:
		return ""
	}
}

func applyIssueMilestoneFilter(q *gorm.DB, baseDB *gorm.DB, repoID uint, milestone string) (*gorm.DB, bool) {
	rawMilestone := strings.TrimSpace(milestone)
	milestone = strings.ToLower(rawMilestone)
	if milestone == "" {
		return q, false
	}
	switch milestone {
	case "*":
		return q.Where("issues.milestone_id IS NOT NULL"), false
	case "none":
		return q.Where("issues.milestone_id IS NULL"), false
	default:
		subQ := baseDB.Session(&gorm.Session{NewDB: true}).Table("milestones").Select("id").
			Where("repository_id = ?", repoID)
		if num, err := strconv.Atoi(rawMilestone); err == nil {
			subQ = subQ.Where("number = ? OR LOWER(title) = LOWER(?)", num, rawMilestone)
		} else {
			subQ = subQ.Where("LOWER(title) = LOWER(?)", rawMilestone)
		}
		return q.Where("issues.milestone_id IN (?)", subQ), false
	}
}

func applyIssueSinceFilter(q *gorm.DB, since string) (*gorm.DB, error) {
	since = strings.TrimSpace(since)
	if since == "" {
		return q, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, since)
	if err != nil {
		return q, fmt.Errorf("%w: since must be ISO 8601", ErrValidation)
	}
	return q.Where("issues.updated_at >= ?", parsed), nil
}

// UpdateIssueByID updates an issue's state using its DB ID.
func (s *Service) UpdateIssueByID(ctx context.Context, id uint, state *string, stateReason *string) error {
	if state == nil {
		return nil
	}
	issue, err := s.GetIssueByID(ctx, id)
	if err != nil {
		return err
	}
	origState := issue.State
	updates := map[string]any{"state": *state}
	newStateReason := issue.StateReason
	if *state == db.StateClosed {
		now := time.Now()
		updates["closed_at"] = &now
		if stateReason != nil {
			updates["state_reason"] = *stateReason
			newStateReason = *stateReason
		} else {
			updates["state_reason"] = db.StateReasonCompleted
			newStateReason = db.StateReasonCompleted
		}
		if origState != db.StateClosed {
			if u, ok := UserFromContext(ctx); ok {
				updates["closed_by_login"] = u.Login
			}
		}
	} else {
		updates["closed_at"] = nil
		updates["state_reason"] = db.StateReasonReopened
		newStateReason = db.StateReasonReopened
		updates["closed_by_login"] = ""
	}
	if err := s.DBForCtx(ctx).Model(&db.Issue{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return err
	}
	if *state != origState {
		eventType := issueEventReopened
		if *state == db.StateClosed {
			eventType = issueEventClosed
		}
		if err := s.recordIssueEvent(ctx, id, eventType, issueEventData{
			StateReason: strPtr(newStateReason),
		}); err != nil {
			return err
		}
	}
	return nil
}

// UpdateIssueFields updates arbitrary fields on an issue by its DB ID.
func (s *Service) UpdateIssueFields(ctx context.Context, id uint, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	issue, err := s.GetIssueByID(ctx, id)
	if err != nil {
		return err
	}
	origTitle := issue.Title
	origState := issue.State
	origStateReason := issue.StateReason
	origLocked := issue.Locked
	origLockReason := issue.ActiveLockReason
	origAssigneeLogins := issue.AssigneeLogins

	newTitle := origTitle
	if v, ok := updates["title"].(string); ok {
		newTitle = v
	}
	newState := origState
	if v, ok := updates["state"].(string); ok {
		newState = v
		if newState == db.StateClosed {
			if origState != db.StateClosed {
				if u, ok := UserFromContext(ctx); ok {
					updates["closed_by_login"] = u.Login
				}
			}
		} else {
			updates["closed_by_login"] = ""
		}
	}
	newStateReason := origStateReason
	if v, ok := updates["state_reason"].(string); ok {
		newStateReason = v
	}
	newLocked := origLocked
	if v, ok := updates["locked"].(bool); ok {
		newLocked = v
	}
	newLockReason := origLockReason
	if v, ok := updates["active_lock_reason"].(string); ok {
		newLockReason = v
	}
	newAssigneeLogins := origAssigneeLogins
	if v, ok := updates["assignee_logins"].(string); ok {
		newAssigneeLogins = v
	}
	newBody, bodyChanged := issueReferenceBodyUpdate(updates["body"], issue.Body)

	if err := s.DBForCtx(ctx).Model(&db.Issue{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return err
	}
	if bodyChanged {
		issue.Body = db.LargeText(newBody)
		issue.UpdatedAt = time.Now()
		if err := s.syncIssueBodyReferences(ctx, issue); err != nil {
			return err
		}
	}
	if newTitle != origTitle {
		if err := s.recordIssueEvent(ctx, id, issueEventRenamed, issueEventData{
			OldTitle: strPtr(origTitle),
			NewTitle: strPtr(newTitle),
		}); err != nil {
			return err
		}
	}
	if newState != origState {
		eventType := issueEventReopened
		if newState == db.StateClosed {
			eventType = issueEventClosed
		}
		if err := s.recordIssueEvent(ctx, id, eventType, issueEventData{
			StateReason: strPtr(newStateReason),
		}); err != nil {
			return err
		}
	}
	if newLocked != origLocked {
		eventType := issueEventUnlocked
		data := issueEventData{}
		if newLocked {
			eventType = issueEventLocked
			data.LockReason = strPtr(newLockReason)
		} else if origLockReason != "" {
			data.LockReason = strPtr(origLockReason)
		}
		if err := s.recordIssueEvent(ctx, id, eventType, data); err != nil {
			return err
		}
	}
	if newAssigneeLogins != origAssigneeLogins {
		desiredSet := loginSet(newAssigneeLogins)
		for _, login := range splitLogins(origAssigneeLogins) {
			if !desiredSet[login] {
				if err := s.recordIssueEvent(ctx, id, issueEventUnassigned, issueEventData{
					AssigneeLogin: strPtr(login),
				}); err != nil {
					return err
				}
			}
		}
		origSet := loginSet(origAssigneeLogins)
		var added []string
		for _, login := range splitLogins(newAssigneeLogins) {
			if !origSet[login] {
				added = append(added, login)
				if err := s.recordIssueEvent(ctx, id, issueEventAssigned, issueEventData{
					AssigneeLogin: strPtr(login),
				}); err != nil {
					return err
				}
			}
		}
		if len(added) > 0 {
			if err := s.createAssignmentNotificationsForLogins(ctx, s.notificationActorIDForContext(ctx), NotificationSubjectIssue, id, issue.RepositoryID, added); err != nil {
				return err
			}
		}
	}
	return nil
}

// DeleteIssueByID deletes an issue by its DB ID within a transaction.
func (s *Service) DeleteIssueByID(ctx context.Context, id uint) error {
	// Fetch the issue first so we can use its correct FK values for cascading deletes.
	issue, err := s.GetIssueByID(ctx, id)
	if err != nil {
		return err
	}
	var attachmentPaths []string
	if err := s.DBForCtx(ctx).Model(&db.Attachment{}).Where("issue_id = ?", id).Pluck("stored_path", &attachmentPaths).Error; err != nil {
		return err
	}
	if err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("repository_id = ? AND issue_number = ?", issue.RepositoryID, issue.Number).
			Delete(&db.IssueComment{}).Error; err != nil {
			return err
		}
		if err := tx.Where("issue_id = ?", id).Delete(&db.Attachment{}).Error; err != nil {
			return err
		}
		if err := tx.Exec("DELETE FROM issue_labels WHERE issue_id = ?", id).Error; err != nil {
			return err
		}
		if err := tx.Where("issue_id = ?", id).Delete(&db.IssueEvent{}).Error; err != nil {
			return err
		}
		if err := s.deleteIssueReferencesForIssueNumber(ContextWithDB(ctx, tx), issue.RepositoryID, issue.Number); err != nil {
			return err
		}
		// Cascade delete reactions, project_items, and linked_branches
		if err := tx.Where("issue_id = ?", id).Delete(&db.Reaction{}).Error; err != nil {
			return err
		}
		if err := tx.Where("subject_type = ? AND subject_id = ?", NotificationSubjectIssue, id).Delete(&db.Notification{}).Error; err != nil {
			return err
		}
		if err := tx.Where("type = ? AND content_id = ?", "ISSUE", fmt.Sprintf("Issue_%d", id)).Delete(&db.ProjectItem{}).Error; err != nil {
			return err
		}
		if err := tx.Where("issue_id = ?", id).Delete(&db.LinkedBranch{}).Error; err != nil {
			return err
		}
		return tx.Delete(&db.Issue{}, id).Error
	}); err != nil {
		return err
	}
	s.cleanupAttachmentPaths(attachmentPaths)
	return nil
}

// FindPRsClosingIssue returns PRs whose body references "fixes #N", "closes #N", etc.
// Supports three formats: local (#123), cross-repo (owner/repo#123), and URL (https://.../issues/123).
func (s *Service) FindPRsClosingIssue(ctx context.Context, repoID uint, issueNumber int) ([]db.PullRequest, error) {
	var prs []db.PullRequest
	// LIKE filter catches both #N and /issues/N patterns for initial filtering
	pattern := fmt.Sprintf("%%#%d%%", issueNumber)
	urlPattern := fmt.Sprintf("%%/issues/%d%%", issueNumber)
	if err := preloadPRFull(s.DBForCtx(ctx)).
		Where("repository_id = ? AND (body LIKE ? OR body LIKE ?)", repoID, pattern, urlPattern).
		Find(&prs).Error; err != nil {
		return nil, err
	}

	// Filter to only those that actually match closing keywords
	var result []db.PullRequest
	// Match: Fixes #123, Fixes owner/repo#123, or Fixes https://.../issues/123
	closingPattern := fmt.Sprintf(
		`(?i)(?:fix(?:es)?|close[sd]?|resolve[sd]?)\s+(?:#%d\b|[\w-]+/[\w-]+#%d\b|https?://[^\s]+/issues/%d\b)`,
		issueNumber, issueNumber, issueNumber,
	)
	re := regexp.MustCompile(closingPattern)
	for _, pr := range prs {
		if re.MatchString(string(pr.Body)) {
			result = append(result, pr)
		}
	}
	return result, nil
}

// CreateIssueComment adds a comment to an issue.
// If inReplyToID is provided, the comment will be a reply to that comment.
func (s *Service) CreateIssueComment(ctx context.Context, repoFullName string, issueNumber int, body, authorLogin string, inReplyToID *uint) (db.IssueComment, error) {
	if err := validateBodyFitsMediumText(body); err != nil {
		return db.IssueComment{}, err
	}
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return db.IssueComment{}, err
	}
	author, authorErr2 := s.GetUser(ctx, authorLogin)
	if authorErr2 != nil {
		slog.Warn("CreateIssueComment: resolve author", "login", authorLogin, "error", authorErr2)
	}
	c := db.IssueComment{
		RepositoryID: rep.ID,
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
	if err := preloadIssueComment(s.DBForCtx(ctx)).First(&c, c.ID).Error; err != nil {
		return c, wrapErr(err)
	}
	if err := s.syncIssueCommentReferences(ctx, c); err != nil {
		return c, err
	}
	var subjectType string
	var subjectID uint
	if subjectType, subjectID, err = s.notificationSubjectForRepoNumber(ctx, rep.ID, issueNumber); err == nil {
		if err := s.createMentionNotificationsForBody(
			ctx,
			author.ID,
			subjectType,
			subjectID,
			rep.ID,
			issueCommentURL(s.BaseURL, rep.FullName, c.ID),
			string(c.Body),
		); err != nil {
			return c, err
		}
	}
	// If this is a reply, notify the parent author
	if inReplyToID != nil {
		var parent db.IssueComment
		if err := preloadIssueComment(s.DBForCtx(ctx)).First(&parent, *inReplyToID).Error; err == nil {
			if parent.AuthorID != author.ID {
				if _, err := s.CreateReplyNotification(ctx, parent.AuthorID, author.ID, NotificationSubjectIssue, subjectID, rep.ID, issueCommentURL(s.BaseURL, rep.FullName, c.ID)); err != nil {
					return c, err
				}
			}
		}
	}
	return c, nil
}

// SetIssueAssignees replaces all assignees on an issue with the given logins.
func (s *Service) SetIssueAssignees(ctx context.Context, repoFullName string, number int, logins []string) (db.Issue, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return db.Issue{}, err
	}
	var issue db.Issue
	if err := s.DBForCtx(ctx).First(&issue, "repository_id = ? AND number = ?", rep.ID, number).Error; err != nil {
		return db.Issue{}, wrapErrf(err, "issue #%d", number)
	}
	existingSet := loginSet(issue.AssigneeLogins)
	desiredSet := sliceLoginSet(logins)
	var added []string
	for _, login := range logins {
		if !existingSet[login] {
			added = append(added, login)
		}
	}
	var removed []string
	for _, login := range splitLogins(issue.AssigneeLogins) {
		if !desiredSet[login] {
			removed = append(removed, login)
		}
	}
	newVal := strings.Join(logins, ",")
	if err := s.DBForCtx(ctx).Model(&issue).Update("assignee_logins", newVal).Error; err != nil {
		return db.Issue{}, err
	}
	for _, login := range removed {
		if err := s.recordIssueEvent(ctx, issue.ID, issueEventUnassigned, issueEventData{
			AssigneeLogin: strPtr(login),
		}); err != nil {
			return db.Issue{}, err
		}
	}
	for _, login := range added {
		if err := s.recordIssueEvent(ctx, issue.ID, issueEventAssigned, issueEventData{
			AssigneeLogin: strPtr(login),
		}); err != nil {
			return db.Issue{}, err
		}
	}
	if len(added) > 0 {
		if err := s.createAssignmentNotificationsForLogins(ctx, s.notificationActorIDForContext(ctx), NotificationSubjectIssue, issue.ID, issue.RepositoryID, added); err != nil {
			return db.Issue{}, err
		}
	}
	if err := preloadIssue(s.DBForCtx(ctx)).First(&issue, issue.ID).Error; err != nil {
		return issue, wrapErr(err)
	}
	return issue, nil
}

// AddIssueAssignees adds assignees (by login) to an issue. Returns the updated issue.
func (s *Service) AddIssueAssignees(ctx context.Context, repoFullName string, number int, logins []string) (db.Issue, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return db.Issue{}, err
	}
	var issue db.Issue
	if err := s.DBForCtx(ctx).First(&issue, "repository_id = ? AND number = ?", rep.ID, number).Error; err != nil {
		return db.Issue{}, wrapErrf(err, "issue #%d", number)
	}
	existingSet := loginSet(issue.AssigneeLogins)
	var added []string
	for _, login := range logins {
		if !existingSet[login] {
			added = append(added, login)
		}
	}
	merged := mergeLogins(issue.AssigneeLogins, logins)
	if err := s.DBForCtx(ctx).Model(&issue).Update("assignee_logins", merged).Error; err != nil {
		return db.Issue{}, err
	}
	for _, login := range added {
		if err := s.recordIssueEvent(ctx, issue.ID, issueEventAssigned, issueEventData{
			AssigneeLogin: strPtr(login),
		}); err != nil {
			return db.Issue{}, err
		}
	}
	if len(added) > 0 {
		if err := s.createAssignmentNotificationsForLogins(ctx, s.notificationActorIDForContext(ctx), NotificationSubjectIssue, issue.ID, issue.RepositoryID, added); err != nil {
			return db.Issue{}, err
		}
	}
	if err := preloadIssue(s.DBForCtx(ctx)).First(&issue, issue.ID).Error; err != nil {
		return issue, wrapErr(err)
	}
	return issue, nil
}

// RemoveIssueAssignees removes assignees (by login) from an issue. Returns the updated issue.
func (s *Service) RemoveIssueAssignees(ctx context.Context, repoFullName string, number int, logins []string) (db.Issue, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return db.Issue{}, err
	}
	var issue db.Issue
	if err := s.DBForCtx(ctx).First(&issue, "repository_id = ? AND number = ?", rep.ID, number).Error; err != nil {
		return db.Issue{}, wrapErrf(err, "issue #%d", number)
	}
	existingSet := loginSet(issue.AssigneeLogins)
	var removed []string
	for _, login := range logins {
		if existingSet[login] {
			removed = append(removed, login)
		}
	}
	remaining := removeLogins(issue.AssigneeLogins, logins)
	if err := s.DBForCtx(ctx).Model(&issue).Update("assignee_logins", remaining).Error; err != nil {
		return db.Issue{}, err
	}
	for _, login := range removed {
		if err := s.recordIssueEvent(ctx, issue.ID, issueEventUnassigned, issueEventData{
			AssigneeLogin: strPtr(login),
		}); err != nil {
			return db.Issue{}, err
		}
	}
	if err := preloadIssue(s.DBForCtx(ctx)).First(&issue, issue.ID).Error; err != nil {
		return issue, wrapErr(err)
	}
	return issue, nil
}

// GetIssueByID loads an issue by its internal DB ID with standard associations.
func (s *Service) GetIssueByID(ctx context.Context, id uint) (db.Issue, error) {
	var issue db.Issue
	err := preloadIssue(s.DBForCtx(ctx)).First(&issue, id).Error
	if err != nil {
		return issue, wrapErr(err)
	}
	return issue, nil
}
