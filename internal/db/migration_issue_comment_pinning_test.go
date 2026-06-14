package db

import "testing"

func TestMigrateIssueCommentPinningColumns_TiDB(t *testing.T) {
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

	if err := MigrateIssueCommentPinningColumns(gdb); err != nil {
		t.Fatalf("MigrateIssueCommentPinningColumns: %v", err)
	}
	if err := MigrateIssueCommentPinningColumns(gdb); err != nil {
		t.Fatalf("MigrateIssueCommentPinningColumns second run: %v", err)
	}

	if !gdb.Migrator().HasColumn("issue_comments", "is_pinned") {
		t.Fatal("expected is_pinned to exist after migration")
	}
	if !gdb.Migrator().HasColumn("issue_comments", "pinned_at") {
		t.Fatal("expected pinned_at to exist after migration")
	}
}
