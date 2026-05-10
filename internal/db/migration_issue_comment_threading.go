package db

import "gorm.io/gorm"

// MigrateIssueCommentThreadingColumns ensures legacy databases have the
// threading columns (in_reply_to_id, thread_root_id) introduced on issue_comments.
func MigrateIssueCommentThreadingColumns(database *gorm.DB) error {
	if database == nil {
		return nil
	}

	migrator := database.Migrator()
	if !migrator.HasTable("issue_comments") {
		return nil
	}
	if !migrator.HasColumn("issue_comments", "in_reply_to_id") {
		if err := migrator.AddColumn(&IssueComment{}, "InReplyToID"); err != nil {
			return err
		}
	}
	if !migrator.HasColumn("issue_comments", "thread_root_id") {
		if err := migrator.AddColumn(&IssueComment{}, "ThreadRootID"); err != nil {
			return err
		}
	}
	return nil
}
