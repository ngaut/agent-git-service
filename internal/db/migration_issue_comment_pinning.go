package db

import "gorm.io/gorm"

// MigrateIssueCommentPinningColumns ensures legacy databases have the comment
// pinning columns introduced on issue_comments.
func MigrateIssueCommentPinningColumns(database *gorm.DB) error {
	if database == nil {
		return nil
	}

	migrator := database.Migrator()
	if !migrator.HasTable("issue_comments") {
		return nil
	}
	if !migrator.HasColumn("issue_comments", "is_pinned") {
		if err := migrator.AddColumn(&IssueComment{}, "IsPinned"); err != nil {
			return err
		}
	}
	if !migrator.HasColumn("issue_comments", "pinned_at") {
		if err := migrator.AddColumn(&IssueComment{}, "PinnedAt"); err != nil {
			return err
		}
	}
	return nil
}
