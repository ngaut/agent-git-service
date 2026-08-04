package db

import (
	"strings"
	"testing"
	"time"

	driversql "github.com/go-sql-driver/mysql"
	"github.com/ngaut/agent-git-service/internal/testharness/testdb"
)

func TestInitVector_Idempotent(t *testing.T) {
	gdb := openTiDB(t)
	if !SupportsVectorDistance(gdb) {
		t.Skip("TiDB playground does not support VEC_COSINE_DISTANCE")
	}
	createVectorTables(t, gdb)

	InitVector(gdb, 3)

	if !gdb.Migrator().HasColumn("issues", "embedding") {
		t.Fatal("expected issues.embedding to exist after first InitVector run")
	}
	if !gdb.Migrator().HasColumn("pull_requests", "embedding") {
		t.Fatal("expected pull_requests.embedding to exist after first InitVector run")
	}

	InitVector(gdb, 3)
}

// TestDialectorForDSN verifies AGS uses the MySQL driver for TiDB/MySQL-compatible DSNs.
func TestDialectorForDSN(t *testing.T) {
	tests := []struct {
		name        string
		dsn         string
		wantDialect string
	}{
		{"mysql default", "user:pass@tcp(host:3306)/db", "mysql"},
		{"mysql uppercase", "MYSQL:user:pass@tcp(host:3306)/db", "mysql"},
		{"mysql whitespace trimmed", "  user:pass@tcp(host:3306)/db  ", "mysql"},
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

func TestRuntimeMySQLDSNEnablesInterpolation(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
	}{
		{name: "no parameters", dsn: "user:pass@tcp(host:4000)/ags"},
		{name: "preserves parameters", dsn: "user:pass@tcp(host:4000)/ags?charset=utf8mb4&parseTime=true&timeout=10s"},
		{name: "safe collation", dsn: "user:pass@tcp(host:4000)/ags?collation=utf8mb4_unicode_ci&loc=UTC"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			optimizedDSN := runtimeMySQLDSN(tt.dsn)
			got, err := driversql.ParseDSN(optimizedDSN)
			if err != nil {
				t.Fatalf("parse optimized DSN: %v", err)
			}
			if !got.InterpolateParams {
				t.Fatal("InterpolateParams = false, want true")
			}
			if got.User != "user" || got.Passwd != "pass" || got.Net != "tcp" || got.Addr != "host:4000" || got.DBName != "ags" {
				t.Fatalf("connection identity changed: %+v", got)
			}
			if tt.name == "preserves parameters" {
				if !got.ParseTime || got.Timeout != 10*time.Second || !strings.Contains(optimizedDSN, "charset=utf8mb4") {
					t.Fatalf("connection parameters changed: %+v", got)
				}
			}
		})
	}
}

func TestRuntimeMySQLDSNPreservesExplicitInterpolationOptOut(t *testing.T) {
	const dsn = "user:pass@tcp(host:4000)/ags?interpolateParams=false&loc=UTC"
	if got := runtimeMySQLDSN(dsn); got != dsn {
		t.Fatalf("runtimeMySQLDSN() = %q, want %q", got, dsn)
	}
}

func TestRuntimeMySQLDSNPreservesUnsafeCharacterSets(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
	}{
		{name: "unsafe charset", dsn: "user:pass@tcp(host:4000)/ags?charset=gbk"},
		{name: "unsafe quoted charset", dsn: "user:pass@tcp(host:4000)/ags?charset=%27gbk%27"},
		{name: "unsafe gb18030 charset", dsn: "user:pass@tcp(host:4000)/ags?charset=gb18030"},
		{name: "unsafe charset fallback", dsn: "user:pass@tcp(host:4000)/ags?charset=utf8mb4%2Cgbk&parseTime=true"},
		{name: "unsafe collation", dsn: "user:pass@tcp(host:4000)/ags?collation=gbk_chinese_ci"},
		{name: "unsafe gb2312 collation", dsn: "user:pass@tcp(host:4000)/ags?collation=gb2312_chinese_ci"},
		{name: "unsafe gb18030 collation", dsn: "user:pass@tcp(host:4000)/ags?collation=gb18030_unicode_520_ci"},
		{name: "unsafe client charset system variable", dsn: "user:pass@tcp(host:4000)/ags?character_set_client=%27gbk%27"},
		{name: "unsafe connection charset system variable", dsn: "user:pass@tcp(host:4000)/ags?character_set_connection=%27gb18030%27"},
		{name: "unsafe scoped client charset system variable", dsn: "user:pass@tcp(host:4000)/ags?@@session.character_set_client=%27gbk%27"},
		{name: "unsafe local connection charset system variable", dsn: "user:pass@tcp(host:4000)/ags?@@local.character_set_connection=%27gb18030%27"},
		{name: "unsafe quoted results charset system variable", dsn: "user:pass@tcp(host:4000)/ags?@@session.`character_set_results`=%27gbk%27"},
		{name: "unsafe tab scoped client charset system variable", dsn: "user:pass@tcp(host:4000)/ags?session\tcharacter_set_client=%27gbk%27"},
		{name: "unsafe escaped newline scoped results charset system variable", dsn: "user:pass@tcp(host:4000)/ags?session%0Acharacter_set_results=%27gbk%27"},
		{name: "unsafe comment-prefixed client charset system variable", dsn: "user:pass@tcp(host:4000)/ags?/*qg*/character_set_client=%27gbk%27"},
		{name: "connection system variable assignment", dsn: "user:pass@tcp(host:4000)/ags?sql_mode=%27TRADITIONAL%27"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runtimeMySQLDSN(tt.dsn); got != tt.dsn {
				t.Fatalf("runtimeMySQLDSN() = %q, want %q", got, tt.dsn)
			}
		})
	}
}

func TestRuntimeMySQLDSNPreservesInvalidInput(t *testing.T) {
	const dsn = "invalid://dsn"
	if got := runtimeMySQLDSN("  " + dsn + "  "); got != dsn {
		t.Fatalf("runtimeMySQLDSN() = %q, want %q", got, dsn)
	}
}

// TestMigrate verifies that Migrate runs AutoMigrate for all models.
func TestMigrate(t *testing.T) {
	gdb := openTiDB(t)

	if err := Migrate(gdb); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	// Verify key tables were created
	tables := []string{"users", "repositories", "issues", "pull_requests", "labels", "milestones", "wiki_compaction_jobs"}
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
	gdb := openTiDB(t)

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
	gdb := openTiDB(t)

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
	gdb := openTiDB(t)
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

func TestInit_TiDB(t *testing.T) {
	dbName, dsn, adminSQL := testdb.CreateRawDatabase(t, "init")
	t.Cleanup(func() {
		_, _ = adminSQL.Exec("DROP DATABASE IF EXISTS `" + dbName + "`")
	})
	gdb, err := Init(dsn)
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
