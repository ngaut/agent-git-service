package db

import "time"

// IssueReadState tracks the read state of an issue for a specific user.
// This enables read receipts and unread message tracking.
type IssueReadState struct {
	ID                uint      `gorm:"primaryKey;autoIncrement"`
	IssueID           uint      `gorm:"not null;uniqueIndex:idx_issue_user,priority:1"`
	Issue             Issue     `gorm:"foreignKey:IssueID"`
	UserID            uint      `gorm:"not null;uniqueIndex:idx_issue_user,priority:2;index"`
	User              User      `gorm:"foreignKey:UserID"`
	LastReadCommentID uint      `gorm:"not null;default:0"` // ID of the last comment the user has read (0 = no comments read)
	UpdatedAt         time.Time `gorm:"autoUpdateTime"`
	CreatedAt         time.Time
}
