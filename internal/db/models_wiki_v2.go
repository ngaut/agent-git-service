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

// WikiBacklink stores the current derived wiki link graph for one repository.
type WikiBacklink struct {
	RepositoryID uint       `gorm:"primaryKey;autoIncrement:false;index:idx_wiki_backlinks_repo_dst,priority:1;index:idx_wiki_backlinks_repo_src,priority:1"`
	Repository   Repository `gorm:"foreignKey:RepositoryID"`
	SrcSlug      string     `gorm:"primaryKey;type:varbinary(1024);index:idx_wiki_backlinks_repo_src,priority:2"`
	DstSlug      string     `gorm:"primaryKey;type:varbinary(1024);index:idx_wiki_backlinks_repo_dst,priority:2"`
	Resolved     bool       `gorm:"not null;index:idx_wiki_backlinks_repo_dst,priority:3"`
	UpdatedAt    time.Time  `gorm:"not null"`
}

func (WikiBacklink) TableName() string { return "wiki_backlinks" }

// WikiPageHistory is the optional derived history accelerator for one page.
type WikiPageHistory struct {
	RepositoryID    uint       `gorm:"primaryKey;autoIncrement:false;index:idx_wiki_page_history_repo_slug_committed,priority:1"`
	Repository      Repository `gorm:"foreignKey:RepositoryID"`
	Slug            string     `gorm:"primaryKey;type:varbinary(1024);index:idx_wiki_page_history_repo_slug_committed,priority:2"`
	CommitSHA       string     `gorm:"primaryKey;type:char(40)"`
	ParentCommitSHA string     `gorm:"type:char(40)"`
	AuthorID        *uint
	Author          *User     `gorm:"foreignKey:AuthorID"`
	Message         string    `gorm:"type:text;not null"`
	CommittedAt     time.Time `gorm:"not null;index:idx_wiki_page_history_repo_slug_committed,priority:3,sort:desc"`
}

func (WikiPageHistory) TableName() string { return "wiki_page_history" }
