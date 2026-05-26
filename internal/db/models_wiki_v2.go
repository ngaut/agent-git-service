package db

import "time"

// WikiPageIndex is the derived live-page projection for Wiki V2.
type WikiPageIndex struct {
	RepositoryID  uint       `gorm:"primaryKey;autoIncrement:false;index:idx_wiki_page_index_repo_commit,priority:1;index:idx_wiki_page_index_repo_updated,priority:1"`
	Repository    Repository `gorm:"foreignKey:RepositoryID"`
	Slug          string     `gorm:"primaryKey;type:varbinary(1024)"`
	HeadBlobSHA   string     `gorm:"type:char(40);not null"`
	HeadCommitSHA string     `gorm:"type:char(40);not null;index:idx_wiki_page_index_repo_commit,priority:2"`
	Title         string     `gorm:"type:varbinary(1024)"`
	Size          int        `gorm:"not null"`
	UpdatedAt     time.Time  `gorm:"not null;index:idx_wiki_page_index_repo_updated,priority:2,sort:desc"`
	LastAuthorID  *uint
	LastAuthor    *User `gorm:"foreignKey:LastAuthorID"`
}

func (WikiPageIndex) TableName() string { return "wiki_page_index" }

// WikiIndexState records the last fully indexed wiki commit per repository.
type WikiIndexState struct {
	RepositoryID         uint       `gorm:"primaryKey;autoIncrement:false"`
	Repository           Repository `gorm:"foreignKey:RepositoryID"`
	IndexedCommitSHA     string     `gorm:"type:char(40)"`
	IndexedAt            *time.Time
	ReconcileRequestedAt *time.Time
	ReconcilerLeaseUntil *time.Time
	UpdatedAt            time.Time
}

func (WikiIndexState) TableName() string { return "wiki_index_state" }
