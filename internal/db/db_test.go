package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type traceCounterLogger struct {
	gormlogger.Interface
	traceErrors int
}

func newTraceCounterLogger() *traceCounterLogger {
	return &traceCounterLogger{Interface: gormlogger.Discard}
}

func (l *traceCounterLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	l.Interface = l.Interface.LogMode(level)
	return l
}

func (l *traceCounterLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if err != nil {
		l.traceErrors++
	}
	l.Interface.Trace(ctx, begin, fc, err)
}

func TestInitVector_Idempotent(t *testing.T) {
	logger := newTraceCounterLogger()
	dbPath := filepath.Join(t.TempDir(), "vector.db")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: logger})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, err := gdb.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}

	if err := gdb.Exec("CREATE TABLE issues (id integer primary key)").Error; err != nil {
		t.Fatalf("create issues: %v", err)
	}
	if err := gdb.Exec("CREATE TABLE pull_requests (id integer primary key)").Error; err != nil {
		t.Fatalf("create pull_requests: %v", err)
	}

	InitVector(gdb, 3)

	if !gdb.Migrator().HasColumn("issues", "embedding") {
		t.Fatal("expected issues.embedding to exist after first InitVector run")
	}
	if !gdb.Migrator().HasColumn("pull_requests", "embedding") {
		t.Fatal("expected pull_requests.embedding to exist after first InitVector run")
	}

	errsAfterFirstRun := logger.traceErrors
	InitVector(gdb, 3)

	if logger.traceErrors != errsAfterFirstRun {
		t.Fatalf("expected second InitVector run to avoid SQL errors, got %d then %d", errsAfterFirstRun, logger.traceErrors)
	}
}

// TestDialectorForDSN verifies DSN parsing for different database types.
func TestDialectorForDSN(t *testing.T) {
	tests := []struct {
		name        string
		dsn         string
		wantDialect string
	}{
		{"memory sqlite", ":memory:", "sqlite"},
		{"file sqlite", "file:test.db", "sqlite"},
		{"file sqlite prefix", "sqlite://test.db", "sqlite"},
		{"sqlite prefix alt", "sqlite:test.db", "sqlite"},
		{"sqlite prefix with slashes", "sqlite://test.db", "sqlite"},
		{"postgres URL", "postgres://user:pass@localhost:5432/testdb?sslmode=disable", "postgres"},
		{"postgresql URL", "postgresql://user:pass@localhost:5432/testdb?sslmode=disable", "postgres"},
		{"postgres uppercase", "  POSTGRESQL://user:pass@localhost:5432/testdb?sslmode=disable  ", "postgres"},
		{"mysql default", "user:pass@tcp(host:3306)/db", "mysql"},
		{"mysql uppercase", "MYSQL:user:pass@tcp(host:3306)/db", "mysql"},
		{"whitespace trimmed", "  :memory:  ", "sqlite"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dialector, dialect := dialectorForDSN(tt.dsn)
			if dialect != tt.wantDialect {
				t.Errorf("dialectorForDSN(%q) dialect = %q, want %q", tt.dsn, dialect, tt.wantDialect)
			}
			if dialector == nil {
				t.Errorf("dialectorForDSN(%q) returned nil dialector", tt.dsn)
			}
		})
	}
}

// TestMigrate verifies that Migrate runs AutoMigrate for all models.
func TestMigrate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migrate.db")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, err := gdb.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}

	err = Migrate(gdb)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	// Verify key tables were created
	tables := []string{"users", "repositories", "issues", "attachments", "pull_requests", "labels", "milestones", "wiki_compaction_jobs"}
	for _, table := range tables {
		if !gdb.Migrator().HasTable(table) {
			t.Errorf("expected table %q to exist after migration", table)
		}
	}
	indexes := []struct {
		table string
		name  string
	}{
		{"issues", "idx_issue_repo_created"},
		{"issues", "idx_issue_repo_state_created"},
		{"issues", "idx_issue_milestone_state"},
		{"pull_requests", "idx_pr_milestone_state_merged"},
		{"team_members", "idx_team_members_user"},
		{"issue_labels", "idx_issue_labels_label_issue"},
		{"pr_labels", "idx_pr_labels_label_pr"},
	}
	for _, idx := range indexes {
		if !gdb.Migrator().HasIndex(idx.table, idx.name) {
			t.Errorf("expected index %q on table %q to exist after migration", idx.name, idx.table)
		}
	}
}

func TestMilestoneCountIndexes_AutoMigrate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "milestone-count-indexes.db")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, err := gdb.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}

	if err := gdb.AutoMigrate(&User{}, &Repository{}, &Milestone{}, &Issue{}, &PullRequest{}); err != nil {
		t.Fatalf("AutoMigrate failed: %v", err)
	}

	indexes := []struct {
		table string
		name  string
	}{
		{"issues", "idx_issue_milestone_state"},
		{"pull_requests", "idx_pr_milestone_state_merged"},
	}
	for _, idx := range indexes {
		if !gdb.Migrator().HasIndex(idx.table, idx.name) {
			t.Errorf("expected index %q on table %q to exist after AutoMigrate", idx.name, idx.table)
		}
	}
}

// TestMigrate_Idempotent verifies that Migrate can be called multiple times.
func TestMigrate_Idempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migrate-idempotent.db")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, err := gdb.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}

	// First migration
	if err := Migrate(gdb); err != nil {
		t.Fatalf("first Migrate failed: %v", err)
	}

	// Second migration should succeed without errors
	if err := Migrate(gdb); err != nil {
		t.Fatalf("second Migrate failed: %v", err)
	}
}

func TestMigrate_ClosedDBReturnsError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migrate-closed.db")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("get sql DB: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	if err := Migrate(gdb); err == nil {
		t.Fatal("expected Migrate to return error on closed database")
	}
}

// TestInit_SQLiteMemory verifies Init works with in-memory SQLite.
func TestInit_SQLiteMemory(t *testing.T) {
	gdb, err := Init(":memory:")
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if gdb == nil {
		t.Fatal("expected non-nil database connection")
	}
	if sqlDB, err := gdb.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}

	// Verify migrations ran
	if !gdb.Migrator().HasTable("users") {
		t.Error("expected users table to exist")
	}
}

// TestInit_SQLiteFile verifies Init works with file-based SQLite.
func TestInit_SQLiteFile(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "init.db")
	gdb, err := Init("file:" + dbPath)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if gdb == nil {
		t.Fatal("expected non-nil database connection")
	}
	if sqlDB, err := gdb.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}

	// Verify migrations ran
	if !gdb.Migrator().HasTable("users") {
		t.Error("expected users table to exist")
	}
}

// TestInit_InvalidDSN verifies Init fails with invalid DSN.
func TestInit_InvalidDSN(t *testing.T) {
	_, err := Init("invalid://dsn")
	if err == nil {
		t.Fatal("expected error for invalid DSN")
	}
}

// TestInit_Timeout verifies Init times out on slow connections.
func TestInit_Timeout(t *testing.T) {
	t.Skip("timeout test requires controlled slow connection")
}
