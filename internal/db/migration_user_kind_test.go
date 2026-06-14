package db

import (
	"testing"

	"gorm.io/gorm"
)

func TestUserModelCreatesNonNullUserKind(t *testing.T) {
	gdb := openTiDB(t)
	if err := gdb.AutoMigrate(&User{}); err != nil {
		t.Fatalf("AutoMigrate User: %v", err)
	}
	assertMySQLColumnNotNull(t, gdb, "users", "user_kind")
}

func assertMySQLColumnNotNull(t *testing.T, gdb *gorm.DB, table, column string) {
	t.Helper()
	var rows []struct {
		ColumnName string `gorm:"column:column_name"`
		IsNullable string `gorm:"column:is_nullable"`
	}
	if err := gdb.Raw(`
		SELECT COLUMN_NAME AS column_name, IS_NULLABLE AS is_nullable
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE()
			AND TABLE_NAME = ?
			AND COLUMN_NAME = ?
	`, table, column).Scan(&rows).Error; err != nil {
		t.Fatalf("read column metadata for %s.%s: %v", table, column, err)
	}
	for _, row := range rows {
		if row.ColumnName != column {
			continue
		}
		if row.IsNullable != "NO" {
			t.Fatalf("%s.%s is_nullable = %q, want NO", table, column, row.IsNullable)
		}
		return
	}
	t.Fatalf("column %s.%s not found", table, column)
}
