package db

import "time"

// Release represents a GitHub release.
type Release struct {
	ID           uint       `gorm:"primaryKey;autoIncrement"`
	RepositoryID uint       `gorm:"uniqueIndex:idx_release_repo_tag"`
	Repository   Repository `gorm:"foreignKey:RepositoryID"`
	TagName      string     `gorm:"size:255;not null;uniqueIndex:idx_release_repo_tag"`
	Name         string     `gorm:"size:512"`
	Body         LargeText
	Draft        bool `gorm:"default:false"`
	PreRelease   bool `gorm:"default:false"`
	AuthorID     uint `gorm:"index"`
	Author       User `gorm:"foreignKey:AuthorID"`
	CreatedAt    time.Time
	PublishedAt  *time.Time
	Assets       []ReleaseAsset `gorm:"foreignKey:ReleaseID"`
}

// ReleaseAsset stores the binary content of a release asset uploaded via the API.
type ReleaseAsset struct {
	ID          uint   `gorm:"primaryKey;autoIncrement"`
	ReleaseID   uint   `gorm:"not null;index"`
	Name        string `gorm:"size:255;not null"`
	Label       string `gorm:"size:255"`
	ContentType string `gorm:"size:128"`
	Size        int64
	Content     []byte
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
