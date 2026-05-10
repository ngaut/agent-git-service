package db

import "time"

// Reaction represents a user's emoji reaction on an issue or comment.
type Reaction struct {
	ID        uint   `gorm:"primaryKey;autoIncrement"`
	IssueID   *uint  `gorm:"index;uniqueIndex:idx_reaction_unique"` // set for issue-level reactions
	CommentID *uint  `gorm:"index;uniqueIndex:idx_reaction_unique"` // set for comment-level reactions
	UserID    uint   `gorm:"index;uniqueIndex:idx_reaction_unique;not null"`
	User      User   `gorm:"foreignKey:UserID"`
	Content   string `gorm:"size:20;not null;uniqueIndex:idx_reaction_unique"` // +1, -1, laugh, hooray, confused, heart, rocket, eyes
	CreatedAt time.Time
}
