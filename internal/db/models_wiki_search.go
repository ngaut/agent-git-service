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

// WikiSearchProjectionTask is a durable, coalescing outbox entry for one
// repository slug. Lexical and embedding work use separate rows so a slow
// embedding provider never prevents the latest lexical state from landing.
type WikiSearchProjectionTask struct {
	ID             uint64     `gorm:"primaryKey;autoIncrement"`
	RepositoryID   uint       `gorm:"not null;uniqueIndex:idx_wiki_search_projection_task,priority:1"`
	Repository     Repository `gorm:"foreignKey:RepositoryID"`
	Slug           string     `gorm:"size:255;not null;uniqueIndex:idx_wiki_search_projection_task,priority:2"`
	Kind           string     `gorm:"size:16;not null;uniqueIndex:idx_wiki_search_projection_task,priority:3;index:idx_wiki_search_projection_claim,priority:1"`
	Generation     uint64     `gorm:"not null;default:1"`
	RevisionSHA    string     `gorm:"size:40"`
	LabelDigest    string     `gorm:"type:text"`
	LeaseToken     string     `gorm:"size:36"`
	LeaseExpiresAt *time.Time `gorm:"index:idx_wiki_search_projection_claim,priority:2"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
