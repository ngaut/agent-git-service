// Package service — label management.
package service

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ─── Labels ───────────────────────────────────────────────────────────────────

// ListLabels returns all labels for a repository, ordered by name.
func (s *Service) ListLabels(ctx context.Context, repoFullName string) ([]db.Label, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	var labels []db.Label
	if err := s.DBForCtx(ctx).Preload("Repository").Preload("Repository.Owner").
		Where("repository_id = ?", rep.ID).Find(&labels).Error; err != nil {
		return nil, err
	}
	return labels, nil
}

// SearchLabels searches labels for a single repository ID.
func (s *Service) SearchLabels(ctx context.Context, repoIDStr, query, sort, order string) ([]db.Label, error) {
	rep, err := s.GetRepoByID(ctx, repoIDStr)
	if err != nil {
		return nil, err
	}

	q := strings.TrimSpace(query)
	dbq := s.DBForCtx(ctx).Preload("Repository").Preload("Repository.Owner").
		Where("repository_id = ?", rep.ID)
	if q != "" {
		like := "%" + strings.ToLower(escapeLike(q)) + "%"
		dbq = dbq.Where("(LOWER(name) LIKE ? OR LOWER(description) LIKE ?)", like, like)
	}

	dir := "asc"
	if strings.EqualFold(order, "desc") {
		dir = "desc"
	}
	switch strings.ToLower(strings.TrimSpace(sort)) {
	case "created":
		dbq = dbq.Order("created_at " + dir).Order("name asc")
	case "updated":
		dbq = dbq.Order("updated_at " + dir).Order("name asc")
	default:
		dbq = dbq.Order("name asc")
	}

	var labels []db.Label
	if err := dbq.Find(&labels).Error; err != nil {
		return nil, err
	}
	return labels, nil
}

// CreateLabel creates a new label for a repository.
// Returns an error if a label with the same name already exists (name is unique per repo).
func (s *Service) CreateLabel(ctx context.Context, repoFullName, name, color, description string) (db.Label, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return db.Label{}, err
	}
	// Check for existing label with same name (enforce uniqueness at app layer too).
	var existing db.Label
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND name = ?", rep.ID, name).
		First(&existing).Error; err == nil {
		return db.Label{}, fmt.Errorf("label %q already exists", name)
	}
	trimmedColor := strings.TrimPrefix(color, "#")
	if len(trimmedColor) != 6 || !isHex(trimmedColor) {
		return db.Label{}, fmt.Errorf("color must be a 6-character hex code")
	}
	label := db.Label{
		RepositoryID: rep.ID,
		Name:         name,
		Color:        trimmedColor,
		Description:  description,
	}
	if err := s.DBForCtx(ctx).Create(&label).Error; err != nil {
		return db.Label{}, err
	}
	if err := s.DBForCtx(ctx).Preload("Repository").Preload("Repository.Owner").First(&label, label.ID).Error; err != nil {
		return label, err
	}
	return label, nil
}

// DeleteLabel removes a label by name from a repository.
// Cascades deletion to issue_labels, pr_labels, and wiki_page_labels join
// tables to avoid FK constraint failures.
func (s *Service) DeleteLabel(ctx context.Context, repoFullName, name string) error {
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return err
	}

	label, err := s.repoLabelByName(ctx, rep.ID, name)
	if err != nil {
		return err
	}
	affectedWikiSlugs, err := s.wikiPageSlugsForLabelIDs(ctx, rep.ID, []uint{label.ID})
	if err != nil {
		return err
	}

	// Delete references from issue_labels and pr_labels join tables using raw SQL
	if err := s.DBForCtx(ctx).Exec("DELETE FROM issue_labels WHERE label_id = ?", label.ID).Error; err != nil {
		return wrapErr(err)
	}
	if err := s.DBForCtx(ctx).Exec("DELETE FROM pr_labels WHERE label_id = ?", label.ID).Error; err != nil {
		return wrapErr(err)
	}
	if err := s.DBForCtx(ctx).Exec("DELETE FROM wiki_page_labels WHERE label_id = ?", label.ID).Error; err != nil {
		return wrapErr(err)
	}

	// Now delete the label itself
	res := s.DBForCtx(ctx).Where("id = ?", label.ID).Delete(&db.Label{})
	if err := res.Error; err != nil {
		return wrapErr(err)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	s.queueWikiSearchRefreshBySlugs(ctx, repoFullName, affectedWikiSlugs)
	return nil
}

// AddIssueLabelByID adds a label to an issue using their DB IDs (junction table).
// Duplicate adds are silently ignored (idempotent) to support retries and concurrency.
func (s *Service) AddIssueLabelByID(ctx context.Context, issueID, labelID uint) error {
	if _, err := s.GetIssueByID(ctx, issueID); err != nil {
		return err
	}
	var label db.Label
	if err := s.DBForCtx(ctx).First(&label, labelID).Error; err != nil {
		return wrapErr(err)
	}
	res := s.DBForCtx(ctx).Exec(
		"INSERT INTO issue_labels (issue_id, label_id) VALUES (?, ?)",
		issueID, labelID,
	)
	if isDuplicateErr(res.Error) {
		return nil
	}
	if res.Error != nil {
		return res.Error
	}
	return s.recordIssueEvent(ctx, issueID, issueEventLabeled, issueEventData{
		LabelName: strPtr(label.Name),
	})
}

// RemoveIssueLabelByID removes a label from an issue using their DB IDs (junction table).
func (s *Service) RemoveIssueLabelByID(ctx context.Context, issueID, labelID uint) error {
	if _, err := s.GetIssueByID(ctx, issueID); err != nil {
		return err
	}
	var label db.Label
	if err := s.DBForCtx(ctx).First(&label, labelID).Error; err != nil {
		return wrapErr(err)
	}
	res := s.DBForCtx(ctx).Exec("DELETE FROM issue_labels WHERE issue_id = ? AND label_id = ?", issueID, labelID)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return nil
	}
	return s.recordIssueEvent(ctx, issueID, issueEventUnlabeled, issueEventData{
		LabelName: strPtr(label.Name),
	})
}

// RemoveIssueLabel removes a label (by name) from an issue (by number) in a repo.
// Returns the remaining labels attached to the issue, matching GitHub REST API behavior.
func (s *Service) RemoveIssueLabel(ctx context.Context, repoFullName string, issueNumber int, labelName string) ([]db.Label, error) {
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	issue, err := s.issueRefByRepoAndNumber(ctx, rep.ID, issueNumber)
	if err != nil {
		return nil, err
	}
	label, err := s.repoLabelByName(ctx, rep.ID, labelName)
	if err != nil {
		return nil, err
	}
	// Delete the join-table row.
	if err := s.RemoveIssueLabelByID(ctx, issue.ID, label.ID); err != nil {
		return nil, err
	}
	// Return remaining labels on the issue (with full preloads).
	return s.ListIssueLabels(ctx, repoFullName, issueNumber)
}

// GetLabel returns a single label by name from a repository.
func (s *Service) GetLabel(ctx context.Context, repoFullName, name string) (db.Label, error) {
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return db.Label{}, err
	}
	return s.repoLabelByName(ctx, rep.ID, name)
}

// ListIssueLabels returns labels attached to a specific issue.
func (s *Service) ListIssueLabels(ctx context.Context, repoFullName string, issueNumber int) ([]db.Label, error) {
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	issue, err := s.issueRefByRepoAndNumber(ctx, rep.ID, issueNumber)
	if err != nil {
		return nil, err
	}
	return s.listIssueLabelsByIssueID(ctx, issue.ID)
}

// AddIssueLabels adds labels (by name) to an issue. Returns all labels on the issue.
func (s *Service) AddIssueLabels(ctx context.Context, repoFullName string, issueNumber int, labelNames []string) ([]db.Label, error) {
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	issue, err := s.issueRefByRepoAndNumber(ctx, rep.ID, issueNumber)
	if err != nil {
		return nil, err
	}
	labels, err := s.resolveRepoLabels(ctx, rep.ID, labelNames)
	if err != nil {
		return nil, err
	}
	if err := s.addIssueLabelsFast(ctx, issue.ID, labels); err != nil {
		return nil, err
	}
	return s.listIssueLabelsByIssueID(ctx, issue.ID)
}

// SetIssueLabels replaces all labels on an issue with the given names.
func (s *Service) SetIssueLabels(ctx context.Context, repoFullName string, issueNumber int, labelNames []string) ([]db.Label, error) {
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	issue, err := s.issueRefByRepoAndNumber(ctx, rep.ID, issueNumber)
	if err != nil {
		return nil, err
	}
	existingLabels, err := s.listIssueLabelsByIssueID(ctx, issue.ID)
	if err != nil {
		return nil, err
	}
	existingByID := make(map[uint]db.Label, len(existingLabels))
	existingIDs := make(map[uint]bool, len(existingLabels))
	for _, l := range existingLabels {
		existingByID[l.ID] = l
		existingIDs[l.ID] = true
	}
	desiredLabels, err := s.resolveRepoLabels(ctx, rep.ID, labelNames)
	if err != nil {
		return nil, err
	}
	desiredIDs := make(map[uint]db.Label, len(desiredLabels))
	orderedDesired := make([]db.Label, 0, len(desiredLabels))
	for _, label := range desiredLabels {
		if _, ok := desiredIDs[label.ID]; ok {
			continue
		}
		desiredIDs[label.ID] = label
		orderedDesired = append(orderedDesired, label)
	}
	for id := range existingByID {
		if _, ok := desiredIDs[id]; !ok {
			if err := s.RemoveIssueLabelByID(ctx, issue.ID, id); err != nil {
				return nil, err
			}
		}
	}
	for _, label := range orderedDesired {
		if !existingIDs[label.ID] {
			if err := s.AddIssueLabelByID(ctx, issue.ID, label.ID); err != nil {
				return nil, fmt.Errorf("add label %q: %w", label.Name, err)
			}
		}
	}
	return s.listIssueLabelsByIssueID(ctx, issue.ID)
}

// RemoveAllIssueLabels removes all labels from an issue.
func (s *Service) RemoveAllIssueLabels(ctx context.Context, repoFullName string, issueNumber int) error {
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return err
	}
	issue, err := s.issueRefByRepoAndNumber(ctx, rep.ID, issueNumber)
	if err != nil {
		return err
	}
	labels, err := s.listIssueLabelsByIssueID(ctx, issue.ID)
	if err != nil {
		return err
	}
	for _, label := range labels {
		if err := s.RemoveIssueLabelByID(ctx, issue.ID, label.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) issueRefByRepoAndNumber(ctx context.Context, repoID uint, issueNumber int) (db.Issue, error) {
	var issue db.Issue
	if err := s.DBForCtx(ctx).
		Select("id").
		Where("repository_id = ? AND number = ?", repoID, issueNumber).
		First(&issue).Error; err != nil {
		return db.Issue{}, wrapErrf(err, "issue #%d", issueNumber)
	}
	return issue, nil
}

func (s *Service) repoLabelByName(ctx context.Context, repoID uint, labelName string) (db.Label, error) {
	var label db.Label
	if err := s.DBForCtx(ctx).
		Preload("Repository").Preload("Repository.Owner").
		Where("repository_id = ? AND name = ?", repoID, labelName).
		First(&label).Error; err != nil {
		return db.Label{}, wrapErrf(err, "label %q", labelName)
	}
	return label, nil
}

func (s *Service) listIssueLabelsByIssueID(ctx context.Context, issueID uint) ([]db.Label, error) {
	var labels []db.Label
	if err := s.DBForCtx(ctx).
		Preload("Repository").Preload("Repository.Owner").
		Table("labels").
		Joins("JOIN issue_labels ON issue_labels.label_id = labels.id").
		Where("issue_labels.issue_id = ?", issueID).
		Find(&labels).Error; err != nil {
		return nil, err
	}
	return labels, nil
}

func (s *Service) resolveRepoLabels(ctx context.Context, repoID uint, labelNames []string) ([]db.Label, error) {
	orderedNames := uniqueLabelNames(labelNames)
	if len(orderedNames) == 0 {
		return []db.Label{}, nil
	}

	var found []db.Label
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND name IN ?", repoID, orderedNames).
		Find(&found).Error; err != nil {
		return nil, err
	}

	byName := make(map[string]db.Label, len(found))
	for _, label := range found {
		byName[label.Name] = label
	}

	out := make([]db.Label, 0, len(found))
	for _, name := range orderedNames {
		if label, ok := byName[name]; ok {
			out = append(out, label)
		}
	}
	return out, nil
}

type issueLabelLink struct {
	IssueID uint `gorm:"column:issue_id;primaryKey"`
	LabelID uint `gorm:"column:label_id;primaryKey"`
}

func (issueLabelLink) TableName() string {
	return "issue_labels"
}

func (s *Service) addIssueLabelsFast(ctx context.Context, issueID uint, labels []db.Label) error {
	if len(labels) == 0 {
		return nil
	}
	actor := ""
	if u, ok := UserFromContext(ctx); ok {
		actor = u.Login
	}

	return s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		events := make([]db.IssueEvent, 0, len(labels))
		for _, label := range labels {
			link := issueLabelLink{IssueID: issueID, LabelID: label.ID}
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&link)
			if res.Error != nil {
				return fmt.Errorf("batch add labels: %w", res.Error)
			}
			if res.RowsAffected == 0 {
				continue
			}
			events = append(events, db.IssueEvent{
				IssueID:    issueID,
				EventType:  issueEventLabeled,
				ActorLogin: actor,
				LabelName:  strPtr(label.Name),
			})
		}
		if len(events) == 0 {
			return nil
		}
		if err := tx.Create(&events).Error; err != nil {
			return fmt.Errorf("batch add label events: %w", err)
		}
		return nil
	})
}

func uniqueLabelNames(labelNames []string) []string {
	out := make([]string, 0, len(labelNames))
	for _, name := range labelNames {
		name = strings.TrimSpace(name)
		if name == "" || slices.Contains(out, name) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// ─── PR Labels Fallbacks ─────────────────────────────────────────────────────────

// AddPRLabelByID adds a label to a PR using their DB IDs (junction table).
func (s *Service) AddPRLabelByID(ctx context.Context, prID, labelID uint) error {
	if err := s.DBForCtx(ctx).Exec(
		"INSERT INTO pr_labels (pull_request_id, label_id) SELECT ?, ? WHERE NOT EXISTS (SELECT 1 FROM pr_labels WHERE pull_request_id = ? AND label_id = ?)",
		prID, labelID, prID, labelID,
	).Error; err != nil && !isDuplicateErr(err) {
		return err
	}
	return nil
}

// RemovePRLabelByID removes a label from a PR using their DB IDs (junction table).
func (s *Service) RemovePRLabelByID(ctx context.Context, prID, labelID uint) error {
	return s.DBForCtx(ctx).Exec("DELETE FROM pr_labels WHERE pull_request_id = ? AND label_id = ?", prID, labelID).Error
}

// ListPRLabels returns labels attached to a specific PR.
func (s *Service) ListPRLabels(ctx context.Context, repoFullName string, number int) ([]db.Label, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	var pr db.PullRequest
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND number = ?", rep.ID, number).
		First(&pr).Error; err != nil {
		return nil, wrapErrf(err, "pull request #%d", number)
	}
	var labels []db.Label
	if err := s.DBForCtx(ctx).
		Preload("Repository").Preload("Repository.Owner").
		Table("labels").
		Joins("JOIN pr_labels ON pr_labels.label_id = labels.id").
		Where("pr_labels.pull_request_id = ?", pr.ID).
		Find(&labels).Error; err != nil {
		return nil, err
	}
	return labels, nil
}

// AddPRLabels adds labels (by name) to a PR. Returns all labels on the PR.
func (s *Service) AddPRLabels(ctx context.Context, repoFullName string, number int, labelNames []string) ([]db.Label, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	var pr db.PullRequest
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND number = ?", rep.ID, number).
		First(&pr).Error; err != nil {
		return nil, wrapErrf(err, "pull request #%d", number)
	}
	for _, name := range labelNames {
		var label db.Label
		if err := s.DBForCtx(ctx).
			Where("repository_id = ? AND name = ?", rep.ID, name).
			First(&label).Error; err != nil {
			continue
		}
		if err := s.AddPRLabelByID(ctx, pr.ID, label.ID); err != nil {
			return nil, fmt.Errorf("add label %q: %w", name, err)
		}
	}
	return s.ListPRLabels(ctx, repoFullName, number)
}

// SetPRLabels replaces all labels on a PR with the given names.
func (s *Service) SetPRLabels(ctx context.Context, repoFullName string, number int, labelNames []string) ([]db.Label, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	var pr db.PullRequest
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND number = ?", rep.ID, number).
		First(&pr).Error; err != nil {
		return nil, wrapErrf(err, "pull request #%d", number)
	}
	if err := s.DBForCtx(ctx).Exec("DELETE FROM pr_labels WHERE pull_request_id = ?", pr.ID).Error; err != nil {
		return nil, err
	}
	for _, name := range labelNames {
		var label db.Label
		if err := s.DBForCtx(ctx).
			Where("repository_id = ? AND name = ?", rep.ID, name).
			First(&label).Error; err != nil {
			continue
		}
		if err := s.AddPRLabelByID(ctx, pr.ID, label.ID); err != nil {
			return nil, fmt.Errorf("add label %q: %w", name, err)
		}
	}
	return s.ListPRLabels(ctx, repoFullName, number)
}

// RemovePRLabel removes a label (by name) from a PR (by number). Returns remaining labels.
func (s *Service) RemovePRLabel(ctx context.Context, repoFullName string, number int, labelName string) ([]db.Label, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	var pr db.PullRequest
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND number = ?", rep.ID, number).
		First(&pr).Error; err != nil {
		return nil, wrapErrf(err, "pull request #%d", number)
	}
	var label db.Label
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND name = ?", rep.ID, labelName).
		First(&label).Error; err != nil {
		return nil, wrapErrf(err, "label %q", labelName)
	}
	if err := s.RemovePRLabelByID(ctx, pr.ID, label.ID); err != nil {
		return nil, err
	}
	return s.ListPRLabels(ctx, repoFullName, number)
}

// RemoveAllPRLabels removes all labels from a PR.
func (s *Service) RemoveAllPRLabels(ctx context.Context, repoFullName string, number int) error {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return err
	}
	var pr db.PullRequest
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND number = ?", rep.ID, number).
		First(&pr).Error; err != nil {
		return wrapErrf(err, "pull request #%d", number)
	}
	return s.DBForCtx(ctx).Exec("DELETE FROM pr_labels WHERE pull_request_id = ?", pr.ID).Error
}

func (s *Service) CreateLinkedBranch(ctx context.Context, lb *db.LinkedBranch) error {
	return s.DBForCtx(ctx).Create(lb).Error
}

// ListLinkedBranches returns all linked branches for an issue.
func (s *Service) ListLinkedBranches(ctx context.Context, issueID uint) ([]db.LinkedBranch, error) {
	var branches []db.LinkedBranch
	err := s.DBForCtx(ctx).Preload("Repository").Preload("Repository.Owner").Where("issue_id = ?", issueID).Find(&branches).Error
	return branches, err
}

// EditLabelInput holds the fields that can be updated on a label.
type EditLabelInput struct {
	NewName     *string
	Color       *string
	Description *string
}

// EditLabel applies a partial update to a label identified by oldName.
func (s *Service) EditLabel(ctx context.Context, repoFullName, oldName string, in EditLabelInput) (db.Label, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return db.Label{}, err
	}
	var label db.Label
	if err := s.DBForCtx(ctx).Where("repository_id = ? AND name = ?", rep.ID, oldName).First(&label).Error; err != nil {
		return db.Label{}, wrapErrf(err, "label %q", oldName)
	}
	affectedWikiSlugs, err := s.wikiPageSlugsForLabelIDs(ctx, rep.ID, []uint{label.ID})
	if err != nil {
		return db.Label{}, err
	}
	if in.NewName != nil {
		label.Name = *in.NewName
	}
	if in.Color != nil {
		trimmedColor := strings.TrimPrefix(*in.Color, "#")
		if len(trimmedColor) != 6 || !isHex(trimmedColor) {
			return db.Label{}, fmt.Errorf("%w: invalid color format: %s", ErrValidation, *in.Color)
		}
		label.Color = trimmedColor
	}
	if in.Description != nil {
		label.Description = *in.Description
	}
	if err := s.DBForCtx(ctx).Save(&label).Error; err != nil {
		return label, err
	}
	if err := s.DBForCtx(ctx).Preload("Repository").Preload("Repository.Owner").First(&label, label.ID).Error; err != nil {
		return label, err
	}
	s.queueWikiSearchRefreshBySlugs(ctx, repoFullName, affectedWikiSlugs)
	return label, nil
}

func isHex(color string) bool {
	for i := 0; i < len(color); i++ {
		ch := color[i]
		if (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F') {
			continue
		}
		return false
	}
	return true
}
