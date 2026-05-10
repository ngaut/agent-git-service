package db

import (
	"path/filepath"
	"reflect"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type indexListRow struct {
	Name   string `gorm:"column:name"`
	Unique int    `gorm:"column:unique"`
}

type indexInfoRow struct {
	Name string `gorm:"column:name"`
}

func openProjectItemDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "project-items.db")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, err := gdb.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := gdb.AutoMigrate(&Project{}, &ProjectItem{}); err != nil {
		t.Fatalf("migrate project items: %v", err)
	}
	return gdb
}

func TestMigrateProjectItemUniqueIndex_Idempotent(t *testing.T) {
	gdb := openProjectItemDB(t)

	if err := gdb.Exec("CREATE INDEX idx_pi_content_unique ON project_items (content_id)").Error; err != nil {
		t.Fatalf("create old index: %v", err)
	}

	if err := MigrateProjectItemUniqueIndex(gdb); err != nil {
		t.Fatalf("first migration failed: %v", err)
	}
	if err := MigrateProjectItemUniqueIndex(gdb); err != nil {
		t.Fatalf("second migration failed: %v", err)
	}

	if !gdb.Migrator().HasIndex("project_items", "idx_pi_content_unique") {
		t.Fatal("expected idx_pi_content_unique to exist after migration")
	}

	var indexes []indexListRow
	if err := gdb.Raw("PRAGMA index_list('project_items')").Scan(&indexes).Error; err != nil {
		t.Fatalf("read index_list: %v", err)
	}
	var uniqueFlag *int
	for _, idx := range indexes {
		if idx.Name == "idx_pi_content_unique" {
			uniqueFlag = &idx.Unique
			break
		}
	}
	if uniqueFlag == nil || *uniqueFlag != 1 {
		t.Fatalf("expected idx_pi_content_unique to be unique, got %#v", indexes)
	}

	var indexInfo []indexInfoRow
	if err := gdb.Raw("PRAGMA index_info('idx_pi_content_unique')").Scan(&indexInfo).Error; err != nil {
		t.Fatalf("read index_info: %v", err)
	}
	var columns []string
	for _, col := range indexInfo {
		columns = append(columns, col.Name)
	}
	want := []string{"project_id", "content_id", "type"}
	if !reflect.DeepEqual(columns, want) {
		t.Fatalf("index columns = %v, want %v", columns, want)
	}
}

func TestMigrateProjectItemUniqueIndex_DuplicateRows(t *testing.T) {
	gdb := openProjectItemDB(t)

	items := []ProjectItem{
		{ProjectID: 1, ContentID: "Issue_1", Type: "ISSUE"},
		{ProjectID: 1, ContentID: "Issue_1", Type: "ISSUE"},
	}
	if err := gdb.Create(&items).Error; err != nil {
		t.Fatalf("seed duplicates: %v", err)
	}

	if err := MigrateProjectItemUniqueIndex(gdb); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	if gdb.Migrator().HasIndex("project_items", "idx_pi_content_unique") {
		t.Fatal("expected unique index creation to fail with duplicate rows")
	}
}
