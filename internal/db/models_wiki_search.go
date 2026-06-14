package db

import "time"

// WikiSearchDocument stores one searchable wiki page snapshot per repository slug.
// Embedding is managed by wiki search migrations: non-TiDB backends keep a text
// column, while TiDB deployments can convert it to VECTOR(dims) during InitVector.
type WikiSearchDocument struct {
	ID           uint       `gorm:"primaryKey;autoIncrement"`
	RepositoryID uint       `gorm:"not null;index;uniqueIndex:idx_wiki_search_repo_slug"`
	Repository   Repository `gorm:"foreignKey:RepositoryID"`
	Slug         string     `gorm:"size:255;not null;uniqueIndex:idx_wiki_search_repo_slug"`
	Title        string     `gorm:"size:1024;not null"`
	Body         LargeText
	RevisionSHA  string `gorm:"size:40;not null"`
	LabelDigest  string `gorm:"type:text"`
	Embedding    string `gorm:"column:embedding;-:migration"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
