package db

import "time"

// IssueEvent records lifecycle events for issues (labels, state changes, etc.).
type IssueEvent struct {
	ID         uint   `gorm:"primaryKey;autoIncrement"`
	IssueID    uint   `gorm:"index"`
	Issue      Issue  `gorm:"foreignKey:IssueID"`
	EventType  string `gorm:"size:50;index"`
	ActorLogin string `gorm:"size:255;index"`

	// Event-specific data fields. Nullable because not every event uses them.
	LabelName      *string `gorm:"type:text"`
	MilestoneTitle *string `gorm:"type:text"`
	OldTitle       *string `gorm:"type:text"`
	NewTitle       *string `gorm:"type:text"`
	LockReason     *string `gorm:"type:text"`
	StateReason    *string `gorm:"type:text"`
	AssigneeLogin  *string `gorm:"type:text"`

	CreatedAt time.Time `gorm:"index"`
}
