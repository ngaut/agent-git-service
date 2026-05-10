package db

import (
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestMigrateIssueCommentPinningColumns_SQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "issue-comment-pinning.db")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, err := gdb.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}

	if err := gdb.Exec(`
		CREATE TABLE issue_comments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			repository_id INTEGER,
			issue_number INTEGER,
			body TEXT,
			author_id INTEGER,
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
