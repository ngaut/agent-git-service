package db

import "time"

type WikiCompactionJob struct {
	ID              string     `gorm:"primaryKey;size:36"`
	RepositoryID    uint       `gorm:"not null;index:idx_wiki_compaction_jobs_repo_status_created,priority:1"`
	Repository      Repository `gorm:"foreignKey:RepositoryID"`
	RequestedByID   *uint
	RequestedBy     *User  `gorm:"foreignKey:RequestedByID"`
	Status          string `gorm:"type:char(16);not null;index:idx_wiki_compaction_jobs_repo_status_created,priority:2"`
	PreviousHead    string `gorm:"type:char(40)"`
	NewHead         string `gorm:"type:char(40)"`
	CompactedBefore *time.Time
	Pages           int
	CommitsRemoved  int
	ErrorMessage    string `gorm:"type:text"`
	StartedAt       *time.Time
	FinishedAt      *time.Time
	CreatedAt       time.Time `gorm:"index:idx_wiki_compaction_jobs_repo_status_created,priority:3,sort:desc"`
	UpdatedAt       time.Time
}

func (WikiCompactionJob) TableName() string { return "wiki_compaction_jobs" }
