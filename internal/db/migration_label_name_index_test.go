package db

import "testing"

func TestLabelModelAddsNameLookupIndex(t *testing.T) {
	gdb := openTiDB(t)

	if err := gdb.AutoMigrate(&Repository{}, &Label{}); err != nil {
		t.Fatalf("auto-migrate label tables: %v", err)
	}
	if !gdb.Migrator().HasIndex(&Label{}, "idx_labels_name") {
		t.Fatal("expected labels.name lookup index to exist")
	}
}
