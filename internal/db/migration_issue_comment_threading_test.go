package db

import (
	"testing"
)

func TestMigrateIssueCommentThreadingColumns_TiDB(t *testing.T) {
	gdb := openTiDB(t)

	if err := gdb.Exec(`
		CREATE TABLE issue_comments (
			id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
			repository_id BIGINT UNSIGNED,
			issue_number BIGINT,
			body TEXT,
			author_id BIGINT UNSIGNED,
			created_at DATETIME,
			updated_at DATETIME
		)
	`).Error; err != nil {
		t.Fatalf("create legacy issue_comments: %v", err)
	}

	// Run the migration
	if err := MigrateIssueCommentThreadingColumns(gdb); err != nil {
		t.Fatalf("MigrateIssueCommentThreadingColumns: %v", err)
	}

	// Verify columns were added
	if !gdb.Migrator().HasColumn(&IssueComment{}, "InReplyToID") {
		t.Error("InReplyToID column was not added")
	}
	if !gdb.Migrator().HasColumn(&IssueComment{}, "ThreadRootID") {
		t.Error("ThreadRootID column was not added")
	}

	// Run migration again (idempotency check)
	if err := MigrateIssueCommentThreadingColumns(gdb); err != nil {
		t.Fatalf("MigrateIssueCommentThreadingColumns second run: %v", err)
	}
}
