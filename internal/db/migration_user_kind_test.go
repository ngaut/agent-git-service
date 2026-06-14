package db

import (
	"path/filepath"
	"testing"

	"gorm.io/gorm"
)

func TestUserModelCreatesNonNullUserKind(t *testing.T) {
	gdb := openSQLiteDB(t, filepath.Join(t.TempDir(), "user-kind-not-null.db"))
	if err := gdb.AutoMigrate(&User{}); err != nil {
		t.Fatalf("AutoMigrate User: %v", err)
	}
	assertSQLiteColumnNotNull(t, gdb, "users", "user_kind")
}

func assertSQLiteColumnNotNull(t *testing.T, gdb *gorm.DB, table, column string) {
	t.Helper()
	var rows []struct {
		Name    string `gorm:"column:name"`
		NotNull int    `gorm:"column:notnull"`
	}
	if err := gdb.Raw("PRAGMA table_info(" + table + ")").Scan(&rows).Error; err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	for _, row := range rows {
		if row.Name == column {
			if row.NotNull != 1 {
				t.Fatalf("%s.%s notnull = %d, want 1", table, column, row.NotNull)
			}
			return
		}
	}
	t.Fatalf("column %s.%s not found", table, column)
}
