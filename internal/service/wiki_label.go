package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ListWikiPageLabels returns labels attached to a wiki page.
func (s *Service) ListWikiPageLabels(ctx context.Context, repoFullName, slug string) ([]db.Label, error) {
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	if err := s.ensureWikiPageForLabels(ctx, repoFullName, slug); err != nil {
		return nil, err
	}
	return s.listWikiPageLabelsBySlug(ctx, rep.ID, slug)
}

// AddWikiPageLabels adds repository labels to a wiki page by name.
func (s *Service) AddWikiPageLabels(ctx context.Context, repoFullName, slug string, labelNames []string) ([]db.Label, error) {
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	if err := s.ensureWikiPageForLabels(ctx, repoFullName, slug); err != nil {
		return nil, err
	}
	labels, err := s.resolveRepoLabels(ctx, rep.ID, labelNames)
	if err != nil {
		return nil, err
	}
	if err := s.addWikiPageLabels(ctx, rep.ID, slug, labels); err != nil {
		return nil, err
	}
	s.queueWikiSearchRefresh(ctx, repoFullName, slug)
	return s.listWikiPageLabelsBySlug(ctx, rep.ID, slug)
}

// SetWikiPageLabels replaces all labels on a wiki page.
func (s *Service) SetWikiPageLabels(ctx context.Context, repoFullName, slug string, labelNames []string) ([]db.Label, error) {
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	if err := s.ensureWikiPageForLabels(ctx, repoFullName, slug); err != nil {
		return nil, err
	}
	labels, err := s.resolveRepoLabels(ctx, rep.ID, labelNames)
	if err != nil {
		return nil, err
	}
	if err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("repository_id = ? AND slug = ?", rep.ID, slug).Delete(&db.WikiPageLabel{}).Error; err != nil {
			return err
		}
		for _, label := range labels {
			link := db.WikiPageLabel{RepositoryID: rep.ID, Slug: slug, LabelID: label.ID}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&link).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	s.queueWikiSearchRefresh(ctx, repoFullName, slug)
	return s.listWikiPageLabelsBySlug(ctx, rep.ID, slug)
}

// RemoveWikiPageLabel removes one label from a wiki page by name.
func (s *Service) RemoveWikiPageLabel(ctx context.Context, repoFullName, slug, labelName string) ([]db.Label, error) {
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	if err := s.ensureWikiPageForLabels(ctx, repoFullName, slug); err != nil {
		return nil, err
	}
	label, err := s.repoLabelByName(ctx, rep.ID, labelName)
	if err != nil {
		return nil, err
	}
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND slug = ? AND label_id = ?", rep.ID, slug, label.ID).
		Delete(&db.WikiPageLabel{}).Error; err != nil {
		return nil, err
	}
	s.queueWikiSearchRefresh(ctx, repoFullName, slug)
	return s.listWikiPageLabelsBySlug(ctx, rep.ID, slug)
}

// RemoveAllWikiPageLabels removes all labels from a wiki page.
func (s *Service) RemoveAllWikiPageLabels(ctx context.Context, repoFullName, slug string) error {
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return err
	}
	if err := s.ensureWikiPageForLabels(ctx, repoFullName, slug); err != nil {
		return err
	}
	if err := s.DBForCtx(ctx).Where("repository_id = ? AND slug = ?", rep.ID, slug).Delete(&db.WikiPageLabel{}).Error; err != nil {
		return err
	}
	s.queueWikiSearchRefresh(ctx, repoFullName, slug)
	return nil
}

func (s *Service) ensureWikiPageForLabels(ctx context.Context, repoFullName, slug string) error {
	if err := validateReadableWikiSlug(slug); err != nil {
		return ErrNotFound
	}
	if s.Git == nil {
		return fmt.Errorf("git store unavailable")
	}
	full := wikiRepoFullName(repoFullName)
	if !s.Git.Exists(ctx, full) {
		return ErrNotFound
	}
	if _, err := s.Git.ReadFile(ctx, full, wikiSlugToPath(slug)); err != nil {
		return ErrNotFound
	}
	return nil
}

func (s *Service) listWikiPageLabelsBySlug(ctx context.Context, repoID uint, slug string) ([]db.Label, error) {
	labelsBySlug, err := s.wikiLabelsForSlugs(ctx, repoID, []string{slug})
	if err != nil {
		return nil, err
	}
	labels := labelsBySlug[slug]
	if labels == nil {
		return []db.Label{}, nil
	}
	return labels, nil
}

func (s *Service) addWikiPageLabels(ctx context.Context, repoID uint, slug string, labels []db.Label) error {
	for _, label := range labels {
		link := db.WikiPageLabel{RepositoryID: repoID, Slug: slug, LabelID: label.ID}
		if err := s.DBForCtx(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&link).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) deleteWikiPageLabels(ctx context.Context, repoID uint, slug string) error {
	return s.DBForCtx(ctx).Where("repository_id = ? AND slug = ?", repoID, slug).Delete(&db.WikiPageLabel{}).Error
}

func (s *Service) moveWikiPageLabels(ctx context.Context, repoID uint, remaps map[string]string) error {
	if len(remaps) == 0 {
		return nil
	}
	return s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		for from, to := range remaps {
			var links []db.WikiPageLabel
			if err := tx.Where("repository_id = ? AND slug = ?", repoID, from).Find(&links).Error; err != nil {
				return err
			}
			if len(links) == 0 {
				continue
			}
			if err := tx.Where("repository_id = ? AND slug = ?", repoID, from).Delete(&db.WikiPageLabel{}).Error; err != nil {
				return err
			}
			for _, link := range links {
				link.Slug = to
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&link).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *Service) wikiLabelsForSlugs(ctx context.Context, repoID uint, slugs []string) (map[string][]db.Label, error) {
	out := make(map[string][]db.Label, len(slugs))
	slugs = uniqueStrings(slugs)
	if len(slugs) == 0 {
		return out, nil
	}
	for _, slug := range slugs {
		out[slug] = []db.Label{}
	}

	var rows []struct {
		Slug  string
		Label db.Label `gorm:"embedded"`
	}
	if err := s.DBForCtx(ctx).
		Table("wiki_page_labels").
		Select("wiki_page_labels.slug, labels.*").
		Joins("JOIN labels ON labels.id = wiki_page_labels.label_id").
		Where("wiki_page_labels.repository_id = ? AND wiki_page_labels.slug IN ?", repoID, slugs).
		Order("labels.name asc").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	labelIDs := make([]uint, 0, len(rows))
	for _, row := range rows {
		if row.Label.ID != 0 {
			labelIDs = append(labelIDs, row.Label.ID)
		}
	}
	labelsByID, err := s.preloadedLabelsByID(ctx, labelIDs)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		label, ok := labelsByID[row.Label.ID]
		if !ok {
			continue
		}
		out[row.Slug] = append(out[row.Slug], label)
	}
	return out, nil
}

func (s *Service) preloadedLabelsByID(ctx context.Context, ids []uint) (map[uint]db.Label, error) {
	ids = uniqueUintIDs(ids)
	if len(ids) == 0 {
		return map[uint]db.Label{}, nil
	}
	var labels []db.Label
	if err := s.DBForCtx(ctx).Preload("Repository").Preload("Repository.Owner").Where("id IN ?", ids).Order("name asc").Find(&labels).Error; err != nil {
		return nil, err
	}
	out := make(map[uint]db.Label, len(labels))
	for _, label := range labels {
		out[label.ID] = label
	}
	return out, nil
}

func (s *Service) wikiSlugsMatchingLabelFilters(ctx context.Context, repoID uint, slugs []string, filters WikiLabelFilters) (map[string]struct{}, bool, error) {
	slugs = uniqueStrings(slugs)
	allowed := make(map[string]struct{}, len(slugs))
	for _, slug := range slugs {
		allowed[slug] = struct{}{}
	}
	if len(slugs) == 0 {
		return allowed, false, nil
	}

	for _, labelName := range uniqueLabelNames(filters.Labels) {
		label, err := s.repoLabelByName(ctx, repoID, labelName)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return map[string]struct{}{}, true, nil
			}
			return nil, false, err
		}
		var rows []db.WikiPageLabel
		if err := s.DBForCtx(ctx).
			Where("repository_id = ? AND label_id = ? AND slug IN ?", repoID, label.ID, slugs).
			Find(&rows).Error; err != nil {
			return nil, false, err
		}
		withLabel := make(map[string]struct{}, len(rows))
		for _, row := range rows {
			withLabel[row.Slug] = struct{}{}
		}
		for slug := range allowed {
			if _, ok := withLabel[slug]; !ok {
				delete(allowed, slug)
			}
		}
		if len(allowed) == 0 {
			return allowed, true, nil
		}
	}

	excludeLabels, err := s.resolveRepoLabels(ctx, repoID, filters.ExcludeLabels)
	if err != nil {
		return nil, false, err
	}
	if len(excludeLabels) > 0 {
		labelIDs := make([]uint, 0, len(excludeLabels))
		for _, label := range excludeLabels {
			labelIDs = append(labelIDs, label.ID)
		}
		var rows []db.WikiPageLabel
		if err := s.DBForCtx(ctx).
			Where("repository_id = ? AND label_id IN ? AND slug IN ?", repoID, labelIDs, slugs).
			Find(&rows).Error; err != nil {
			return nil, false, err
		}
		for _, row := range rows {
			delete(allowed, row.Slug)
		}
	}
	return allowed, len(allowed) == 0, nil
}

func hasWikiLabelFilters(filters WikiLabelFilters) bool {
	return len(uniqueLabelNames(filters.Labels)) > 0 || len(uniqueLabelNames(filters.ExcludeLabels)) > 0
}

func (s *Service) queueWikiSearchRefresh(ctx context.Context, repoFullName, slug string) {
	page, err := s.GetWikiPage(ctx, repoFullName, slug)
	if err != nil {
		return
	}
	s.queueWikiSearchUpsert(ctx, repoFullName, page)
}

func (s *Service) queueWikiSearchRefreshBySlugs(ctx context.Context, repoFullName string, slugs []string) {
	for _, slug := range uniqueStrings(slugs) {
		s.queueWikiSearchRefresh(ctx, repoFullName, slug)
	}
}

func (s *Service) wikiPageSlugsForLabelIDs(ctx context.Context, repoID uint, labelIDs []uint) ([]string, error) {
	labelIDs = uniqueUintIDs(labelIDs)
	if len(labelIDs) == 0 {
		return nil, nil
	}
	var slugs []string
	if err := s.DBForCtx(ctx).
		Table("wiki_page_labels").
		Distinct("slug").
		Where("repository_id = ? AND label_id IN ?", repoID, labelIDs).
		Order("slug asc").
		Pluck("slug", &slugs).Error; err != nil {
		return nil, err
	}
	return slugs, nil
}

func wikiPageLabelsText(labels []db.Label) string {
	if len(labels) == 0 {
		return ""
	}
	parts := make([]string, 0, len(labels)*2)
	for _, label := range labels {
		parts = append(parts, "label:"+label.Name)
		if strings.TrimSpace(label.Description) != "" {
			parts = append(parts, label.Description)
		}
	}
	return strings.Join(parts, "\n")
}

func wikiLabelLexicalScore(labels []db.Label, query string) float64 {
	score := 0.0
	tokens := wikiSearchTokens(query)
	if len(tokens) == 0 || len(labels) == 0 {
		return score
	}
	for _, label := range labels {
		name := strings.ToLower(label.Name)
		description := strings.ToLower(label.Description)
		for _, token := range tokens {
			token = strings.ToLower(token)
			if token == "" {
				continue
			}
			if strings.Contains(name, token) {
				score += 3
			}
			if strings.Contains(description, token) {
				score += 1.5
			}
		}
	}
	return score
}

func uniqueStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func uniqueUintIDs(values []uint) []uint {
	out := make([]uint, 0, len(values))
	seen := make(map[uint]struct{}, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
