package db

import "time"

// Attachment stores metadata for a repository-scoped attachment persisted on disk.
type Attachment struct {
	ID           uint       `gorm:"primaryKey;autoIncrement"`
	UUID         string     `gorm:"size:36;not null;uniqueIndex:idx_attachments_uuid"`
	IssueID      *uint      `gorm:"index:idx_attachments_issue_created,priority:1"`
	Issue        *Issue     `gorm:"foreignKey:IssueID"`
	RepositoryID uint       `gorm:"not null;index"`
	Repository   Repository `gorm:"foreignKey:RepositoryID"`
	UploaderID   uint       `gorm:"index"`
	Uploader     User       `gorm:"foreignKey:UploaderID"`
	OriginalName string     `gorm:"size:255;not null"`
	StoredPath   string     `gorm:"size:1024;not null"`
	ContentType  string     `gorm:"size:255;not null"`
	Extension    string     `gorm:"size:32;not null"`
	Size         int64      `gorm:"not null"`
	IsImage      bool       `gorm:"default:false"`
	CreatedAt    time.Time  `gorm:"index:idx_attachments_issue_created,priority:2"`
	UpdatedAt    time.Time
}
