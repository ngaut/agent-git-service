package db

import (
	"log/slog"
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func openSQLiteDB(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, err := gdb.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	return gdb
}

func createVectorTables(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	if err := gdb.Exec("CREATE TABLE issues (id integer primary key)").Error; err != nil {
		t.Fatalf("create issues: %v", err)
	}
	if err := gdb.Exec("CREATE TABLE pull_requests (id integer primary key)").Error; err != nil {
		t.Fatalf("create pull_requests: %v", err)
	}
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

func TestInitVector_UnsupportedBackendLogsWarning(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vector-readonly.db")
	writerDB := openSQLiteDB(t, dbPath)
	createVectorTables(t, writerDB)
	if sqlDB, err := writerDB.DB(); err == nil {
		if err := sqlDB.Close(); err != nil {
			t.Fatalf("close sqlite: %v", err)
		}
	}

	readOnlyDB := openSQLiteDB(t, "file:"+dbPath+"?mode=ro")

	sink := captureLogs(t)
	InitVector(readOnlyDB, 3)
	entries := sink.Entries()

	assertLogEntry(t, entries, slog.LevelWarn, "db: InitVector: issues", "", nil)
	assertLogEntry(t, entries, slog.LevelWarn, "db: InitVector: pull_requests", "", nil)

	if readOnlyDB.Migrator().HasColumn("issues", "embedding") {
		t.Fatal("expected issues.embedding to remain absent on read-only database")
	}
	if readOnlyDB.Migrator().HasColumn("pull_requests", "embedding") {
		t.Fatal("expected pull_requests.embedding to remain absent on read-only database")
	}
}

func TestInitVector_DuplicateColumnLogsInfo(t *testing.T) {
	gdb := openSQLiteDB(t, filepath.Join(t.TempDir(), "vector-dup.db"))
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

	assertLogEntry(t, entries, slog.LevelInfo, "db: InitVector: embedding column already exists", "table", "issues")
	assertLogEntry(t, entries, slog.LevelInfo, "db: InitVector: embedding column already exists", "table", "pull_requests")
	for _, entry := range entries {
		if entry.level == slog.LevelWarn {
			t.Fatalf("expected no warn logs, got %#v", entries)
		}
	}
}

func TestInitVector_InvalidDimensionsNoOp(t *testing.T) {
	gdb := openSQLiteDB(t, filepath.Join(t.TempDir(), "vector-invalid.db"))
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
