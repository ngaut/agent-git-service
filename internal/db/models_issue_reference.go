package db

import "time"

// IssueReference records a GitHub-style cross-reference edge from an issue,
// pull request, issue comment, or wiki page body to a target issue/PR number.
type IssueReference struct {
	ID uint `gorm:"primaryKey;autoIncrement"`

	SourceType         string     `gorm:"size:32;not null;uniqueIndex:idx_issue_reference_edge,priority:1;index:idx_issue_reference_source,priority:1"`
	SourceRepositoryID uint       `gorm:"not null;uniqueIndex:idx_issue_reference_edge,priority:2;index:idx_issue_reference_source,priority:2"`
	SourceRepository   Repository `gorm:"foreignKey:SourceRepositoryID"`
	SourceIssueNumber  *int       `gorm:"uniqueIndex:idx_issue_reference_edge,priority:3;index:idx_issue_reference_source,priority:3"`
	SourcePRNumber     *int       `gorm:"uniqueIndex:idx_issue_reference_edge,priority:4;index:idx_issue_reference_source,priority:4"`
	SourceCommentID    *uint      `gorm:"uniqueIndex:idx_issue_reference_edge,priority:5;index:idx_issue_reference_source,priority:5"`
	SourceComment      *IssueComment
	SourceWikiSlug     *string `gorm:"type:varbinary(1024);uniqueIndex:idx_issue_reference_edge,priority:6;index:idx_issue_reference_source,priority:6"`

	TargetRepositoryID uint       `gorm:"not null;uniqueIndex:idx_issue_reference_edge,priority:7;index:idx_issue_reference_target,priority:1"`
	TargetRepository   Repository `gorm:"foreignKey:TargetRepositoryID"`
	TargetNumber       int        `gorm:"not null;uniqueIndex:idx_issue_reference_edge,priority:8;index:idx_issue_reference_target,priority:2"`
	RawReference       string     `gorm:"type:text"`

	CreatedAt time.Time `gorm:"index"`
	UpdatedAt time.Time
}
