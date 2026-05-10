package db

import (
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMigrateTeamMemberLookupIndexes_SQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "team-member-lookup.db")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, err := gdb.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}

	if err := gdb.AutoMigrate(&Team{}, &User{}, &TeamMember{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	if err := MigrateTeamMemberLookupIndexes(gdb); err != nil {
		t.Fatalf("MigrateTeamMemberLookupIndexes: %v", err)
	}
	if err := MigrateTeamMemberLookupIndexes(gdb); err != nil {
		t.Fatalf("MigrateTeamMemberLookupIndexes second run: %v", err)
	}
	if !gdb.Migrator().HasIndex(&TeamMember{}, "idx_team_members_user") {
		t.Fatal("expected idx_team_members_user to exist")
	}
}
