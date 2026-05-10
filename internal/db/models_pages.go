package db

import "time"

// PagesConfig holds the per-repo GitHub Pages configuration.
// One row per repository. The source branch/path describe where Pages
// would build from when a real publishing pipeline is wired up; the
// initial REST surface stores configuration only and does not
// produce hosted content.
type PagesConfig struct {
	ID            uint   `gorm:"primaryKey;autoIncrement"`
	RepositoryID  uint   `gorm:"uniqueIndex;not null"`
	SourceBranch  string `gorm:"size:255"`
	SourcePath    string `gorm:"size:255"`
	BuildType     string `gorm:"size:32"` // "legacy" or "workflow"
	CNAME         string `gorm:"size:255"`
	HTTPSEnforced bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// PagesBuild records a single build trigger event. v1 stores the
// bookkeeping; nothing actually builds yet, so Status remains "queued"
// unless an external worker advances it.
type PagesBuild struct {
	ID           uint      `gorm:"primaryKey;autoIncrement"`
	RepositoryID uint      `gorm:"index:idx_pages_build_repo_created,priority:1"`
	Status       string    `gorm:"size:32;index"`
	PusherLogin  string    `gorm:"size:255"`
	CommitSHA    string    `gorm:"size:40"`
	Duration     int64     // milliseconds
	ErrorMessage string    `gorm:"type:text"`
	CreatedAt    time.Time `gorm:"index:idx_pages_build_repo_created,priority:2"`
	UpdatedAt    time.Time
}
