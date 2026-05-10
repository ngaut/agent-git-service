package db

import (
	"path/filepath"
	"testing"
)

func TestLabelModelAddsNameLookupIndex(t *testing.T) {
	gdb := openSQLiteDB(t, filepath.Join(t.TempDir(), "label-name-index.db"))

	if err := gdb.AutoMigrate(&Repository{}, &Label{}); err != nil {
		t.Fatalf("auto-migrate label tables: %v", err)
	}
	if !gdb.Migrator().HasIndex(&Label{}, "idx_labels_name") {
		t.Fatal("expected labels.name lookup index to exist")
	}
}
