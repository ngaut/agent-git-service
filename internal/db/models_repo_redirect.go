package db

import "time"

// RepoRedirect maps an old repository full name (owner/repo) to a repository ID.
// It enables stable access via historical URLs after rename/transfer/claim/merge.
type RepoRedirect struct {
	ID          uint       `gorm:"primaryKey;autoIncrement"`
	OldFullName string     `gorm:"uniqueIndex;size:512;not null"`
	RepoID      uint       `gorm:"not null;index"`
	Repo        Repository `gorm:"foreignKey:RepoID"`
	CreatedAt   time.Time
}
