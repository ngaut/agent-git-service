package db

import "time"

// Project represents a GitHub project (ProjectV2).
type Project struct {
	ID               uint   `gorm:"primaryKey;autoIncrement"`
	Number           int32  `gorm:"not null;uniqueIndex:idx_owner_number"` // unique per owner
	OwnerLogin       string `gorm:"size:255;not null;uniqueIndex:idx_owner_number"`
	Title            string `gorm:"size:255;not null"`
	Description      string `gorm:"type:text"`
	ShortDescription string `gorm:"size:1024"`
	Public           bool   `gorm:"default:false"`
	Closed           bool   `gorm:"default:false"`
	Readme           string `gorm:"type:text"`
	IsTemplate       bool   `gorm:"default:false"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ProjectField represents a custom field on a ProjectV2.
type ProjectField struct {
	ID        uint    `gorm:"primaryKey;autoIncrement"`
	ProjectID uint    `gorm:"not null;index:idx_pf_project"`
	Project   Project `gorm:"foreignKey:ProjectID"`
	Name      string  `gorm:"size:255;not null"`
	DataType  string  `gorm:"size:30;not null"` // TEXT, SINGLE_SELECT, DATE, NUMBER, ITERATION
	// JSON-encoded options for SINGLE_SELECT fields: [{"id":"...","name":"..."}]
	Options   string `gorm:"type:text"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ProjectRepoLink represents a link between a ProjectV2 and a Repository.
type ProjectRepoLink struct {
	ID           uint `gorm:"primaryKey;autoIncrement"`
	ProjectID    uint `gorm:"not null;uniqueIndex:idx_proj_repo"`
	RepositoryID uint `gorm:"not null;uniqueIndex:idx_proj_repo"`
}

// ProjectItem represents an item in a ProjectV2 (linked issue/PR or draft issue).
// Uniqueness is enforced on (project_id, content_id, type) to prevent duplicate
// items for the same content within a project. DRAFT_ISSUE items have empty content_id
// and are allowed to have multiple entries per project.
type ProjectItem struct {
	ID        uint    `gorm:"primaryKey;autoIncrement"`
	ProjectID uint    `gorm:"not null;index:idx_pi_project"`
	Project   Project `gorm:"foreignKey:ProjectID"`
	ContentID string  `gorm:"size:255"` // e.g. "Issue_123" or "PullRequest_456", empty for drafts
	Type      string  `gorm:"size:30"`  // ISSUE, PULL_REQUEST, DRAFT_ISSUE
	// For DRAFT_ISSUE items
	DraftTitle string `gorm:"size:1024"`
	DraftBody  string `gorm:"type:text"`
	Archived   bool   `gorm:"default:false"`
	// JSON-encoded field values: {"FieldID": value, ...}
	FieldValues string `gorm:"type:text"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TableName for ProjectItem
func (ProjectItem) TableName() string {
	return "project_items"
}
