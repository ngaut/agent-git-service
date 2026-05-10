package db

import "time"

// WikiPageLabel attaches a repository label to a wiki page slug.
// Wiki pages live in git rather than a relational table, so the slug is stored
// directly and scoped by repository_id.
type WikiPageLabel struct {
	RepositoryID uint       `gorm:"primaryKey;index:idx_wiki_page_labels_repo_slug,priority:1;index:idx_wiki_page_labels_repo_label,priority:1"`
	Repository   Repository `gorm:"foreignKey:RepositoryID"`
	Slug         string     `gorm:"size:255;primaryKey;index:idx_wiki_page_labels_repo_slug,priority:2"`
	LabelID      uint       `gorm:"primaryKey;index:idx_wiki_page_labels_repo_label,priority:2"`
	Label        Label      `gorm:"foreignKey:LabelID"`
	CreatedAt    time.Time
}
