package db

import "time"

// WikiSearchDocument stores one searchable wiki page snapshot per repository slug.
// Embedding is stored as a serialized vector string so non-TiDB test backends can
// exercise semantic ranking logic without requiring native VECTOR support.
type WikiSearchDocument struct {
	ID           uint       `gorm:"primaryKey;autoIncrement"`
	RepositoryID uint       `gorm:"not null;index;uniqueIndex:idx_wiki_search_repo_slug"`
	Repository   Repository `gorm:"foreignKey:RepositoryID"`
	Slug         string     `gorm:"size:255;not null;uniqueIndex:idx_wiki_search_repo_slug"`
	Title        string     `gorm:"size:1024;not null"`
	Body         LargeText
	RevisionSHA  string `gorm:"size:40;not null"`
	Embedding    string `gorm:"type:text"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
