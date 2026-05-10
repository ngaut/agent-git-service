package search

import (
	"strings"

	"gh-server/internal/db"

	"gorm.io/gorm"
)

type repoLabelRef struct {
	ID   uint
	Name string
}

// ApplyRepoIssueLabelFilters resolves repo-local label names once, then applies
// issue_labels subqueries keyed by label_id instead of joining labels per issue.
func ApplyRepoIssueLabelFilters(q *gorm.DB, baseDB *gorm.DB, repoID uint, rawLabels string) (*gorm.DB, bool, error) {
	names := splitCSVLabels(rawLabels)
	if len(names) == 0 {
		return q, false, nil
	}

	labelIDsByName, err := resolveRepoLabelIDs(baseDB, repoID, names)
	if err != nil {
		return q, false, err
	}

	for _, name := range names {
		ids := labelIDsByName[name]
		if len(ids) == 0 {
			return q, true, nil
		}
		subQ := baseDB.Session(&gorm.Session{NewDB: true}).
			Table("issue_labels").
			Select("issue_labels.issue_id").
			Where("issue_labels.label_id IN ?", ids)
		q = q.Where("issues.id IN (?)", subQ)
	}

	return q, false, nil
}

func splitCSVLabels(raw string) []string {
	parts := strings.Split(raw, ",")
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.ToLower(strings.TrimSpace(part))
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func resolveRepoLabelIDs(baseDB *gorm.DB, repoID uint, names []string) (map[string][]uint, error) {
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		wanted[name] = struct{}{}
	}

	var labels []repoLabelRef
	if err := baseDB.Model(&db.Label{}).
		Select("id", "name").
		Where("repository_id = ?", repoID).
		Find(&labels).Error; err != nil {
		return nil, err
	}

	labelIDsByName := make(map[string][]uint, len(wanted))
	for _, label := range labels {
		key := strings.ToLower(label.Name)
		if _, ok := wanted[key]; ok {
			labelIDsByName[key] = append(labelIDsByName[key], label.ID)
		}
	}

	return labelIDsByName, nil
}
