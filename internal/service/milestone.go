package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gh-server/internal/db"
)

// NextMilestoneNumber returns the next sequential milestone number within a repo.
func (s *Service) NextMilestoneNumber(ctx context.Context, repoID uint) (int, error) {
	return nextNumber[db.Milestone](s, ctx, repoID)
}

// CreateMilestone creates a milestone for a repository.
// Retries on duplicate number to handle concurrent creation.
func (s *Service) CreateMilestone(ctx context.Context, repoFullName, title, description, state string) (db.Milestone, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return db.Milestone{}, err
	}
	state, err = normalizeMilestoneState(state, true)
	if err != nil {
		return db.Milestone{}, err
	}
	creatorID := rep.OwnerID
	if u, err := s.GetCurrentUser(ctx); err == nil && u.ID != 0 {
		creatorID = u.ID
	}

	const maxRetries = 5
	for attempt := 0; attempt < maxRetries; attempt++ {
		num, numErr := s.NextMilestoneNumber(ctx, rep.ID)
		if numErr != nil {
			return db.Milestone{}, fmt.Errorf("service: create milestone: %w", numErr)
		}
		m := db.Milestone{
			RepositoryID: rep.ID,
			Number:       num,
			Title:        title,
			Description:  description,
			State:        state,
			CreatorID:    creatorID,
		}
		if err := s.DBForCtx(ctx).Create(&m).Error; err != nil {
			if isDuplicateErr(err) {
				continue
			}
			return db.Milestone{}, err
		}
		if err := preloadMilestone(s.DBForCtx(ctx)).First(&m, m.ID).Error; err != nil {
			return m, wrapErr(err)
		}
		return m, nil
	}
	return db.Milestone{}, fmt.Errorf("service: create milestone: failed after %d retries", maxRetries)
}

// ListMilestones returns milestones for a repo, optionally filtered by state.
// Supports sorting and pagination; returns the total count for pagination.
func (s *Service) ListMilestones(ctx context.Context, repoFullName, state, sort, direction string, page, perPage int) ([]db.Milestone, int64, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, 0, err
	}
	return s.ListMilestonesByRepoID(ctx, rep.ID, state, sort, direction, page, perPage)
}

// ListMilestonesByRepoID returns milestones by repo ID, optionally filtered by state.
// Supports sorting and pagination; returns the total count for pagination.
func (s *Service) ListMilestonesByRepoID(ctx context.Context, repoID uint, state, sort, direction string, page, perPage int) ([]db.Milestone, int64, error) {
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

	state = strings.TrimSpace(strings.ToLower(state))
	q := preloadMilestone(s.DBForCtx(ctx)).Where("repository_id = ?", repoID)
	countQ := s.DBForCtx(ctx).Model(&db.Milestone{}).Where("repository_id = ?", repoID)
	if state != "" && state != "all" {
		q = q.Where("state = ?", state)
		countQ = countQ.Where("state = ?", state)
	}

	order, err := milestoneSortOrder(sort, direction)
	if err != nil {
		return nil, 0, err
	}

	var total int64
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var milestones []db.Milestone
	if err := q.Order(order).Offset(offset).Limit(perPage).Find(&milestones).Error; err != nil {
		return nil, 0, err
	}
	return milestones, total, nil
}

func milestoneSortOrder(sort, direction string) (string, error) {
	sort = strings.TrimSpace(strings.ToLower(sort))
	direction = strings.TrimSpace(strings.ToLower(direction))
	if sort == "" {
		sort = "number"
	}
	if direction == "" {
		direction = "asc"
	}
	if direction != "asc" && direction != "desc" {
		return "", fmt.Errorf("%w: direction must be one of: asc, desc", ErrValidation)
	}

	var col string
	switch sort {
	case "created":
		col = "created_at"
	case "updated":
		col = "updated_at"
	case "due_on":
		col = "due_on"
	case "number":
		col = "number"
	default:
		return "", fmt.Errorf("%w: sort must be one of: created, updated, due_on, number", ErrValidation)
	}
	return col + " " + direction, nil
}

func normalizeMilestoneState(state string, allowEmpty bool) (string, error) {
	state = strings.TrimSpace(strings.ToLower(state))
	if state == "" {
		if allowEmpty {
			return db.StateOpen, nil
		}
		return "", fmt.Errorf("%w: state must be one of: open, closed", ErrInvalidState)
	}
	switch state {
	case db.StateOpen, db.StateClosed:
		return state, nil
	default:
		return "", fmt.Errorf("%w: state must be one of: open, closed", ErrInvalidState)
	}
}

// MilestoneIssueCounts holds open/closed issue totals for a milestone, including PRs.
type MilestoneIssueCounts struct {
	OpenIssues   int64
	ClosedIssues int64
}

// GetMilestoneByTitle looks up a milestone by title (case-insensitive) within a repo.
func (s *Service) GetMilestoneByTitle(ctx context.Context, repoFullName, title string) (db.Milestone, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return db.Milestone{}, err
	}
	var m db.Milestone
	if err := preloadMilestone(s.DBForCtx(ctx)).
		Where("repository_id = ? AND LOWER(title) = ?", rep.ID, strings.ToLower(title)).First(&m).Error; err != nil {
		return m, wrapErrf(err, "milestone %q", title)
	}
	return m, nil
}

// GetMilestoneByID looks up a milestone by its DB ID.
func (s *Service) GetMilestoneByID(ctx context.Context, id uint) (db.Milestone, error) {
	var m db.Milestone
	if err := preloadMilestone(s.DBForCtx(ctx)).First(&m, id).Error; err != nil {
		return m, wrapErrf(err, "milestone #%d", id)
	}
	return m, nil
}

// GetMilestoneByNumber looks up a milestone by its repository-scoped number.
func (s *Service) GetMilestoneByNumber(ctx context.Context, repoFullName string, number int) (db.Milestone, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return db.Milestone{}, err
	}
	var m db.Milestone
	if err := preloadMilestone(s.DBForCtx(ctx)).
		Where("repository_id = ? AND number = ?", rep.ID, number).First(&m).Error; err != nil {
		return m, wrapErrf(err, "milestone #%d", number)
	}
	return m, nil
}

// UpdateMilestoneInput holds the optional fields for updating a milestone.
type UpdateMilestoneInput struct {
	Title       *string    `json:"title"`
	Description *string    `json:"description"`
	State       *string    `json:"state"`
	DueOn       *time.Time `json:"due_on"`
}

// UpdateMilestone patches a milestone identified by repo + number.
func (s *Service) UpdateMilestone(ctx context.Context, repoFullName string, number int, in UpdateMilestoneInput) (db.Milestone, error) {
	m, err := s.GetMilestoneByNumber(ctx, repoFullName, number)
	if err != nil {
		return db.Milestone{}, err
	}
	updates := make(map[string]any)
	if in.Title != nil {
		updates["title"] = *in.Title
	}
	if in.Description != nil {
		updates["description"] = *in.Description
	}
	if in.State != nil {
		state, err := normalizeMilestoneState(*in.State, false)
		if err != nil {
			return db.Milestone{}, err
		}
		updates["state"] = state
		if state == db.StateClosed {
			now := time.Now()
			updates["closed_at"] = &now
		} else {
			updates["closed_at"] = nil
		}
	}
	if in.DueOn != nil {
		updates["due_on"] = in.DueOn
	}
	if len(updates) > 0 {
		if err := s.DBForCtx(ctx).Model(&m).Updates(updates).Error; err != nil {
			return m, err
		}
	}
	// Reload to return the updated state.
	if err := preloadMilestone(s.DBForCtx(ctx)).First(&m, m.ID).Error; err != nil {
		return m, wrapErr(err)
	}
	return m, nil
}

// DeleteMilestone deletes a milestone by repo + number.
// Clears milestone_id on issues and PRs that reference it before deletion.
func (s *Service) DeleteMilestone(ctx context.Context, repoFullName string, number int) error {
	m, err := s.GetMilestoneByNumber(ctx, repoFullName, number)
	if err != nil {
		return err
	}
	// Clear FK references to avoid orphaned milestone_id values.
	s.DBForCtx(ctx).Model(&db.Issue{}).Where("milestone_id = ?", m.ID).Update("milestone_id", nil)
	s.DBForCtx(ctx).Model(&db.PullRequest{}).Where("milestone_id = ?", m.ID).Update("milestone_id", nil)
	return s.DBForCtx(ctx).Delete(&m).Error
}

// SetIssueMilestone sets the milestone on an issue by milestone DB ID (0 to clear).
func (s *Service) SetIssueMilestone(ctx context.Context, issueID uint, milestoneID *uint) error {
	issue, err := s.GetIssueByID(ctx, issueID)
	if err != nil {
		return err
	}
	oldMilestoneID := issue.MilestoneID
	var oldTitle *string
	if oldMilestoneID != nil {
		if m, err := s.GetMilestoneByID(ctx, *oldMilestoneID); err == nil {
			oldTitle = strPtr(m.Title)
		}
	}
	var newTitle *string
	if milestoneID != nil {
		m, err := s.GetMilestoneByID(ctx, *milestoneID)
		if err != nil {
			return err
		}
		if m.RepositoryID != issue.RepositoryID {
			return fmt.Errorf("%w: milestone does not belong to issue repository", ErrValidation)
		}
		newTitle = strPtr(m.Title)
	}
	if err := s.DBForCtx(ctx).Model(&db.Issue{}).Where("id = ?", issueID).Update("milestone_id", milestoneID).Error; err != nil {
		return err
	}
	switch {
	case oldMilestoneID == nil && milestoneID != nil:
		return s.recordIssueEvent(ctx, issueID, issueEventMilestoned, issueEventData{MilestoneTitle: newTitle})
	case oldMilestoneID != nil && milestoneID == nil:
		return s.recordIssueEvent(ctx, issueID, issueEventDemilestoned, issueEventData{MilestoneTitle: oldTitle})
	case oldMilestoneID != nil && milestoneID != nil && *oldMilestoneID != *milestoneID:
		if err := s.recordIssueEvent(ctx, issueID, issueEventDemilestoned, issueEventData{MilestoneTitle: oldTitle}); err != nil {
			return err
		}
		return s.recordIssueEvent(ctx, issueID, issueEventMilestoned, issueEventData{MilestoneTitle: newTitle})
	default:
		return nil
	}
}

// ListMilestoneLabels returns all distinct labels assigned to issues associated with a milestone.
func (s *Service) ListMilestoneLabels(ctx context.Context, repoFullName string, number int) ([]db.Label, error) {
	m, err := s.GetMilestoneByNumber(ctx, repoFullName, number)
	if err != nil {
		return nil, err
	}
	var labels []db.Label
	err = s.DBForCtx(ctx).
		Joins("JOIN issue_labels ON issue_labels.label_id = labels.id").
		Joins("JOIN issues ON issues.id = issue_labels.issue_id").
		Where("issues.milestone_id = ?", m.ID).
		Group("labels.id").
		Find(&labels).Error
	return labels, wrapErr(err)
}

// CountMilestoneIssues returns open/closed counts for issues and PRs on a milestone.
func (s *Service) CountMilestoneIssues(ctx context.Context, milestoneID uint) (int64, int64, error) {
	countsByMilestone, err := s.CountMilestoneIssuesBatch(ctx, []uint{milestoneID})
	if err != nil {
		return 0, 0, err
	}
	counts := countsByMilestone[milestoneID]
	return counts.OpenIssues, counts.ClosedIssues, nil
}

// CountMilestoneIssuesBatch returns open/closed counts for issues and PRs on each milestone.
func (s *Service) CountMilestoneIssuesBatch(ctx context.Context, milestoneIDs []uint) (map[uint]MilestoneIssueCounts, error) {
	countsByMilestone := make(map[uint]MilestoneIssueCounts, len(milestoneIDs))
	if len(milestoneIDs) == 0 {
		return countsByMilestone, nil
	}

	type milestoneCountRow struct {
		MilestoneID  uint
		OpenIssues   int64
		ClosedIssues int64
	}

	var issueRows []milestoneCountRow
	if err := s.DBForCtx(ctx).
		Model(&db.Issue{}).
		Select(
			"milestone_id, "+
				"SUM(CASE WHEN state = ? THEN 1 ELSE 0 END) AS open_issues, "+
				"SUM(CASE WHEN state = ? THEN 1 ELSE 0 END) AS closed_issues",
			db.StateOpen,
			db.StateClosed,
		).
		Where("milestone_id IN ?", milestoneIDs).
		Group("milestone_id").
		Scan(&issueRows).Error; err != nil {
		return nil, err
	}
	for _, row := range issueRows {
		countsByMilestone[row.MilestoneID] = MilestoneIssueCounts{
			OpenIssues:   row.OpenIssues,
			ClosedIssues: row.ClosedIssues,
		}
	}

	var prRows []milestoneCountRow
	if err := s.DBForCtx(ctx).
		Model(&db.PullRequest{}).
		Select(
			"milestone_id, "+
				"SUM(CASE WHEN state = ? AND merged = ? THEN 1 ELSE 0 END) AS open_issues, "+
				"SUM(CASE WHEN state = ? OR merged = ? THEN 1 ELSE 0 END) AS closed_issues",
			db.StateOpen,
			false,
			db.StateClosed,
			true,
		).
		Where("milestone_id IN ?", milestoneIDs).
		Group("milestone_id").
		Scan(&prRows).Error; err != nil {
		return nil, err
	}
	for _, row := range prRows {
		counts := countsByMilestone[row.MilestoneID]
		counts.OpenIssues += row.OpenIssues
		counts.ClosedIssues += row.ClosedIssues
		countsByMilestone[row.MilestoneID] = counts
	}

	return countsByMilestone, nil
}

// SetPRMilestone sets the milestone on a PR by milestone DB ID (0 to clear).
func (s *Service) SetPRMilestone(ctx context.Context, prID uint, milestoneID *uint) error {
	pr, err := s.GetPRByID(ctx, prID)
	if err != nil {
		return err
	}
	if milestoneID != nil {
		m, err := s.GetMilestoneByID(ctx, *milestoneID)
		if err != nil {
			return err
		}
		if m.RepositoryID != pr.RepositoryID {
			return fmt.Errorf("%w: milestone does not belong to pull request repository", ErrValidation)
		}
	}
	return s.DBForCtx(ctx).Model(&db.PullRequest{}).Where("id = ?", prID).Update("milestone_id", milestoneID).Error
}
