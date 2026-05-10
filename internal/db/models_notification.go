package db

import "time"

// Notification represents a per-user thread notification.
type Notification struct {
	ID               uint       `gorm:"primaryKey;autoIncrement"`
	UserID           uint       `gorm:"not null;uniqueIndex:idx_notification_user_subject,priority:1;index:idx_notification_user_read_updated,priority:1"`
	User             User       `gorm:"foreignKey:UserID"`
	ActorID          *uint      `gorm:"index"`
	Actor            *User      `gorm:"foreignKey:ActorID"`
	Type             string     `gorm:"size:50;not null;uniqueIndex:idx_notification_user_subject,priority:2"`
	SubjectType      string     `gorm:"size:50;not null;uniqueIndex:idx_notification_user_subject,priority:3;index"`
	SubjectID        uint       `gorm:"not null;uniqueIndex:idx_notification_user_subject,priority:4;index"`
	RepositoryID     uint       `gorm:"not null;index"`
	Repository       Repository `gorm:"foreignKey:RepositoryID"`
	LatestCommentURL string     `gorm:"size:1024"`
	Read             bool       `gorm:"not null;default:false;index:idx_notification_user_read_updated,priority:2"`
	LastReadAt       *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time `gorm:"index:idx_notification_user_read_updated,priority:3,sort:desc"`
}
