package db

import (
	"testing"

	"gorm.io/gorm"
)

type indexListRow struct {
	NonUnique int `gorm:"column:Non_unique"`
}

func openProjectItemDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb := openTiDB(t)
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

	var indexRows []indexListRow
	if err := gdb.Raw("SHOW INDEX FROM `project_items` WHERE Key_name = ?", "idx_pi_content_unique").Scan(&indexRows).Error; err != nil {
		t.Fatalf("read project_items index: %v", err)
	}
	if len(indexRows) == 0 {
		t.Fatal("expected idx_pi_content_unique rows from SHOW INDEX")
	}
	for _, idx := range indexRows {
		if idx.NonUnique != 0 {
			t.Fatalf("expected idx_pi_content_unique to be unique, got %#v", indexRows)
		}
	}

	hasShape, err := hasCorrectIndexShape(gdb, "project_items", "idx_pi_content_unique", []string{"project_id", "content_id", "type"})
	if err != nil {
		t.Fatalf("hasCorrectIndexShape: %v", err)
	}
	want := []string{"project_id", "content_id", "type"}
	if !hasShape {
		columns, err := mysqlIndexColumns(gdb, "project_items", "idx_pi_content_unique")
		if err != nil {
			t.Fatalf("read index columns: %v", err)
		}
		t.Fatalf("index columns = %v, want %v", columns, want)
	}
}

func TestMigrateProjectItemUniqueIndex_DuplicateRows(t *testing.T) {
	gdb := openProjectItemDB(t)

	project := Project{OwnerLogin: "octo-org", Number: 1, Title: "Roadmap"}
	if err := gdb.Create(&project).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	items := []ProjectItem{
		{ProjectID: project.ID, ContentID: "Issue_1", Type: "ISSUE"},
		{ProjectID: project.ID, ContentID: "Issue_1", Type: "ISSUE"},
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
