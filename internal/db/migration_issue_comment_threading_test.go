package db

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMigrateIssueCommentThreadingColumns_SQLite(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}

	// Create the base table without threading columns
	if err := gdb.Migrator().CreateTable(&IssueComment{}); err != nil {
		t.Fatalf("failed to create table: %v", err)
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
