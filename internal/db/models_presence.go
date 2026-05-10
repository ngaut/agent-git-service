package db

import "time"

// Presence represents ephemeral user presence state for an issue.
// This data is stored in-memory with TTL-based expiry (5 minutes).
// Columns: user_id, issue_id, last_heartbeat, status (active|idle|offline)
type Presence struct {
	UserID        uint      `gorm:"primaryKey;not null"`
	IssueID       uint      `gorm:"primaryKey;not null"`
	LastHeartbeat time.Time `gorm:"not null;index"`
	Status        string    `gorm:"size:20;not null;default:'offline'"` // active, idle, offline
}

// UserLastSeen represents persistent last-seen state for a user.
// Columns: user_id, last_seen_at, last_seen_issue_id
type UserLastSeen struct {
	UserID          uint      `gorm:"primaryKey;not null"`
	LastSeenAt      time.Time `gorm:"not null;index"`
	LastSeenIssueID *uint     `gorm:"index"`
	LastSeenIssue   *Issue    `gorm:"foreignKey:LastSeenIssueID"`
	UpdatedAt       time.Time `gorm:"autoUpdateTime"`
}

// TableName specifies the table name for UserLastSeen
func (UserLastSeen) TableName() string {
	return "user_last_seen"
}
