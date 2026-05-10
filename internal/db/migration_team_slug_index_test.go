package db

import (
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestMigrateTeamOrgSlugIndex_SQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "team-slug-index.db")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, err := gdb.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}

	if err := gdb.AutoMigrate(&User{}, &Team{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	if err := MigrateTeamOrgSlugIndex(gdb); err != nil {
		t.Fatalf("MigrateTeamOrgSlugIndex: %v", err)
	}
	if err := MigrateTeamOrgSlugIndex(gdb); err != nil {
		t.Fatalf("MigrateTeamOrgSlugIndex second run: %v", err)
	}

	if !gdb.Migrator().HasIndex(&Team{}, "idx_org_slug") {
		t.Fatal("expected idx_org_slug to exist after migration")
	}

	hasCorrectShape, err := hasCorrectIndexShape(gdb, "teams", "idx_org_slug", []string{"organization_id", "slug"})
	if err != nil {
		t.Fatalf("hasCorrectIndexShape: %v", err)
	}
	if !hasCorrectShape {
		t.Fatal("expected idx_org_slug to have (organization_id, slug) shape")
	}
}
