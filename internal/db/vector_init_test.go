package db

import (
	"log/slog"
	"testing"

	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/testharness/testdb"
)

func openTiDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, cleanup := testdb.OpenRaw(t, "db")
	t.Cleanup(cleanup)
	return gdb
}

func createVectorTables(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	if err := gdb.Exec("CREATE TABLE issues (id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT)").Error; err != nil {
		t.Fatalf("create issues: %v", err)
	}
	if err := gdb.Exec("CREATE TABLE pull_requests (id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT)").Error; err != nil {
		t.Fatalf("create pull_requests: %v", err)
	}
}

func mysqlIndexColumns(gdb *gorm.DB, tableName, indexName string) ([]string, error) {
	var rows []struct {
		ColumnName string `gorm:"column:column_name"`
	}
	if err := gdb.Raw(`
		SELECT COLUMN_NAME AS column_name
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE()
			AND TABLE_NAME = ?
			AND INDEX_NAME = ?
		ORDER BY SEQ_IN_INDEX
	`, tableName, indexName).Scan(&rows).Error; err != nil {
		return nil, err
	}
	cols := make([]string, 0, len(rows))
	for _, row := range rows {
		cols = append(cols, row.ColumnName)
	}
	return cols, nil
}

func assertLogEntry(t *testing.T, entries []logEntry, level slog.Level, message string, attrKey string, attrValue any) {
	t.Helper()
	for _, entry := range entries {
		if entry.level != level || entry.message != message {
			continue
		}
		if attrKey == "" {
			return
		}
		if value, ok := entry.attrs[attrKey]; ok && value == attrValue {
			return
		}
	}
	if attrKey == "" {
		t.Fatalf("expected log level %s message %q, got %#v", level, message, entries)
	}
	t.Fatalf("expected log level %s message %q with %s=%v, got %#v", level, message, attrKey, attrValue, entries)
}

func TestInitVector_DuplicateColumnLogsDebug(t *testing.T) {
	gdb := openTiDB(t)
	createVectorTables(t, gdb)

	if err := gdb.Exec("ALTER TABLE issues ADD COLUMN embedding TEXT").Error; err != nil {
		t.Fatalf("add issues.embedding: %v", err)
	}
	if err := gdb.Exec("ALTER TABLE pull_requests ADD COLUMN embedding TEXT").Error; err != nil {
		t.Fatalf("add pull_requests.embedding: %v", err)
	}

	sink := captureLogs(t)
	InitVector(gdb, 3)
	entries := sink.Entries()

	assertLogEntry(t, entries, slog.LevelDebug, "db: InitVector: embedding column already exists", "table", "issues")
	assertLogEntry(t, entries, slog.LevelDebug, "db: InitVector: embedding column already exists", "table", "pull_requests")
	for _, entry := range entries {
		if entry.level == slog.LevelWarn {
			t.Fatalf("expected no warn logs, got %#v", entries)
		}
	}
}

func TestInitVector_InvalidDimensionsNoOp(t *testing.T) {
	gdb := openTiDB(t)
	createVectorTables(t, gdb)

	sink := captureLogs(t)
	InitVector(gdb, 0)
	entries := sink.Entries()

	if len(entries) != 0 {
		t.Fatalf("expected no logs for invalid dimensions, got %#v", entries)
	}
	if gdb.Migrator().HasColumn("issues", "embedding") {
		t.Fatal("expected issues.embedding to remain absent for invalid dimensions")
	}
	if gdb.Migrator().HasColumn("pull_requests", "embedding") {
		t.Fatal("expected pull_requests.embedding to remain absent for invalid dimensions")
	}
}
