package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var fakeMySQLDriverSeq uint64

type fakeMySQLCapabilityConfig struct {
	tidbVersionErr    error
	tidbVersion       string
	versionErr        error
	version           string
	fullTextIndexErr  error
	fullTextErr       error
	fullTextScore     float64
	vectorDistanceErr error
	vectorDistance    float64
	vectorDistanceNil bool
}

type fakeMySQLCapabilityDriver struct {
	cfg fakeMySQLCapabilityConfig

	mu      sync.Mutex
	queries []string
}

func (d *fakeMySQLCapabilityDriver) Open(_ string) (driver.Conn, error) {
	return &fakeMySQLCapabilityConn{driver: d}, nil
}

func (d *fakeMySQLCapabilityDriver) record(query string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queries = append(d.queries, query)
}

func (d *fakeMySQLCapabilityDriver) Queries() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	queries := make([]string, len(d.queries))
	copy(queries, d.queries)
	return queries
}

type fakeMySQLCapabilityConn struct {
	driver *fakeMySQLCapabilityDriver
}

func (c *fakeMySQLCapabilityConn) Prepare(_ string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not implemented")
}

func (c *fakeMySQLCapabilityConn) Close() error { return nil }

func (c *fakeMySQLCapabilityConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not implemented")
}

func (c *fakeMySQLCapabilityConn) Ping(_ context.Context) error { return nil }

func (c *fakeMySQLCapabilityConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.driver.record(query)
	normalized := strings.ToLower(strings.TrimSpace(query))
	switch normalized {
	case "select tidb_version()":
		if c.driver.cfg.tidbVersionErr != nil {
			return nil, c.driver.cfg.tidbVersionErr
		}
		return &singleValueRows{columns: []string{"tidb_version()"}, values: []driver.Value{c.driver.cfg.tidbVersion}}, nil
	case "select version()":
		if c.driver.cfg.versionErr != nil {
			return nil, c.driver.cfg.versionErr
		}
		return &singleValueRows{columns: []string{"VERSION()"}, values: []driver.Value{c.driver.cfg.version}}, nil
	case "select coalesce(vec_cosine_distance(?, ?), 0)":
		if c.driver.cfg.vectorDistanceErr != nil {
			return nil, c.driver.cfg.vectorDistanceErr
		}
		value := driver.Value(c.driver.cfg.vectorDistance)
		if c.driver.cfg.vectorDistanceNil {
			value = float64(0)
		}
		return &singleValueRows{columns: []string{"COALESCE(VEC_COSINE_DISTANCE(?, ?), 0)"}, values: []driver.Value{value}}, nil
	default:
		if strings.Contains(normalized, "fts_match_word('test', `body`)") &&
			strings.Contains(normalized, "from `_ags_fts_probe_") {
			if c.driver.cfg.fullTextErr != nil {
				return nil, c.driver.cfg.fullTextErr
			}
			return &singleValueRows{columns: []string{"id"}, values: []driver.Value{int64(1)}}, nil
		}
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
}

func (c *fakeMySQLCapabilityConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.driver.record(query)
	normalized := strings.ToLower(strings.TrimSpace(query))
	switch {
	case strings.HasPrefix(normalized, "drop table if exists `_ags_fts_probe_"),
		strings.HasPrefix(normalized, "create table `_ags_fts_probe_"),
		strings.HasPrefix(normalized, "insert into `_ags_fts_probe_"):
		return driver.RowsAffected(0), nil
	case strings.HasPrefix(normalized, "alter table `_ags_fts_probe_") &&
		strings.Contains(normalized, " add fulltext index "):
		if c.driver.cfg.fullTextIndexErr != nil {
			return nil, c.driver.cfg.fullTextIndexErr
		}
		return driver.RowsAffected(0), nil
	default:
		return nil, fmt.Errorf("unexpected exec: %s", query)
	}
}

type singleValueRows struct {
	columns []string
	values  []driver.Value
	read    bool
}

func (r *singleValueRows) Columns() []string { return r.columns }
func (r *singleValueRows) Close() error      { return nil }

func (r *singleValueRows) Next(dest []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	copy(dest, r.values)
	return nil
}

func openFakeMySQLCapabilityDB(t *testing.T, cfg fakeMySQLCapabilityConfig) (*gorm.DB, *fakeMySQLCapabilityDriver) {
	t.Helper()

	driverName := fmt.Sprintf("fake_mysql_capability_%d", atomic.AddUint64(&fakeMySQLDriverSeq, 1))
	fakeDriver := &fakeMySQLCapabilityDriver{cfg: cfg}
	sql.Register(driverName, fakeDriver)

	sqlDB, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("open fake sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	gdb, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      sqlDB,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open fake gorm db: %v", err)
	}
	return gdb, fakeDriver
}

func TestIsTiDB(t *testing.T) {
	tests := []struct {
		name string
		cfg  fakeMySQLCapabilityConfig
		want bool
	}{
		{
			name: "tidb_version function exists",
			cfg: fakeMySQLCapabilityConfig{
				tidbVersion: "Release Version: v8.5.0",
			},
			want: true,
		},
		{
			name: "version fallback reports tidb",
			cfg: fakeMySQLCapabilityConfig{
				tidbVersionErr: errors.New("function tidb_version does not exist"),
				version:        "8.0.11-TiDB-v8.5.0",
			},
			want: true,
		},
		{
			name: "plain mysql",
			cfg: fakeMySQLCapabilityConfig{
				tidbVersionErr: errors.New("function tidb_version does not exist"),
				version:        "8.0.36 MySQL Community Server",
			},
			want: false,
		},
		{
			name: "version query fails",
			cfg: fakeMySQLCapabilityConfig{
				tidbVersionErr: errors.New("function tidb_version does not exist"),
				versionErr:     errors.New("connection failed"),
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gdb, _ := openFakeMySQLCapabilityDB(t, tt.cfg)
			if got := IsTiDB(gdb); got != tt.want {
				t.Fatalf("IsTiDB() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTiDB_NilAndDryRunFalse(t *testing.T) {
	if IsTiDB(nil) {
		t.Fatal("expected nil database not to be detected as TiDB")
	}
	gdb, _ := openFakeMySQLCapabilityDB(t, fakeMySQLCapabilityConfig{
		tidbVersion: "Release Version: v8.5.0",
	})
	gdb.Config.DryRun = true
	if IsTiDB(gdb) {
		t.Fatal("expected dry-run database not to be detected as TiDB")
	}
}

func TestDetectCapabilities_MySQL(t *testing.T) {
	gdb, _ := openFakeMySQLCapabilityDB(t, fakeMySQLCapabilityConfig{
		tidbVersion:    "Release Version: v8.5.0",
		fullTextScore:  1,
		vectorDistance: 0,
	})

	got := detectCapabilities(gdb)
	if got.GORMDialect != "mysql" {
		t.Fatalf("GORMDialect = %q, want mysql", got.GORMDialect)
	}
	if !got.TiDBDetected {
		t.Fatal("expected TiDBDetected capability")
	}
	if !got.TiDBFullText {
		t.Fatal("expected TiDBFullText capability")
	}
	if !got.VectorDistance {
		t.Fatal("expected VectorDistance capability")
	}
}

func TestSupportsTiDBFullText_MySQLRequiresTiDBAndIndexedColumnProbe(t *testing.T) {
	tests := []struct {
		name              string
		cfg               fakeMySQLCapabilityConfig
		want              bool
		wantFullTextDDL   bool
		wantFullTextQuery bool
	}{
		{
			name: "tidb with fulltext index and fts_match_word",
			cfg: fakeMySQLCapabilityConfig{
				tidbVersion:   "Release Version: v8.5.0",
				fullTextScore: 1,
			},
			want:              true,
			wantFullTextDDL:   true,
			wantFullTextQuery: true,
		},
		{
			name: "tidb without fts_match_word",
			cfg: fakeMySQLCapabilityConfig{
				tidbVersion: "Release Version: v8.1.0",
				fullTextErr: errors.New("function FTS_MATCH_WORD does not exist"),
			},
			want:              false,
			wantFullTextDDL:   true,
			wantFullTextQuery: true,
		},
		{
			name: "tidb without fulltext index support",
			cfg: fakeMySQLCapabilityConfig{
				tidbVersion:      "Release Version: v8.5.0",
				fullTextIndexErr: errors.New("fulltext index unsupported"),
			},
			want:            false,
			wantFullTextDDL: true,
		},
		{
			name: "plain mysql skips fts probe",
			cfg: fakeMySQLCapabilityConfig{
				tidbVersionErr: errors.New("function tidb_version does not exist"),
				version:        "8.0.36 MySQL Community Server",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gdb, fakeDriver := openFakeMySQLCapabilityDB(t, tt.cfg)
			if got := SupportsTiDBFullText(gdb); got != tt.want {
				t.Fatalf("SupportsTiDBFullText() = %v, want %v", got, tt.want)
			}

			sawFullTextDDL := false
			sawFullTextQuery := false
			sawFullTextRankQuery := false
			for _, query := range fakeDriver.Queries() {
				upper := strings.ToUpper(query)
				if strings.Contains(upper, "FTS_MATCH_WORD(?, ?)") {
					t.Fatalf("full-text probe must use an indexed column, saw legacy query %q", query)
				}
				if strings.HasPrefix(upper, "SELECT FTS_MATCH_WORD") && strings.Contains(upper, "LIMIT") {
					t.Fatalf("full-text probe must not limit a top-level FTS_MATCH_WORD projection, saw unsupported query %q", query)
				}
				if strings.Contains(upper, "ADD FULLTEXT INDEX") {
					sawFullTextDDL = true
				}
				if strings.Contains(upper, "FTS_MATCH_WORD") {
					sawFullTextQuery = true
					if strings.Contains(upper, " AS FTS_SCORE") &&
						strings.Contains(upper, "JOIN (SELECT") &&
						strings.Contains(upper, "ORDER BY FTS_MATCHES.FTS_SCORE DESC") &&
						strings.Contains(upper, "LIMIT 1") {
						sawFullTextRankQuery = true
					}
				}
			}
			if sawFullTextDDL != tt.wantFullTextDDL {
				t.Fatalf("full-text DDL probe presence = %v, want %v; queries=%#v", sawFullTextDDL, tt.wantFullTextDDL, fakeDriver.Queries())
			}
			if sawFullTextQuery != tt.wantFullTextQuery {
				t.Fatalf("full-text query probe presence = %v, want %v; queries=%#v", sawFullTextQuery, tt.wantFullTextQuery, fakeDriver.Queries())
			}
			if sawFullTextRankQuery != tt.wantFullTextQuery {
				t.Fatalf("full-text ranked query probe presence = %v, want %v; queries=%#v", sawFullTextRankQuery, tt.wantFullTextQuery, fakeDriver.Queries())
			}
		})
	}
}

func TestSupportsTiDBFullText_TiDBProbe(t *testing.T) {
	gdb := openFullTextProbeDB(t)
	got := SupportsTiDBFullText(gdb)
	if os.Getenv("TIDB_EXPECT_FULLTEXT") == "1" && !got {
		t.Fatal("expected TiDB full-text support")
	}
	if !got {
		t.Log("TiDB full-text support is unavailable in this environment")
	}
}

func openFullTextProbeDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := strings.TrimSpace(os.Getenv("TIDB_FULLTEXT_TEST_DSN"))
	if dsn == "" {
		return openTiDB(t)
	}

	gdb, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open TiDB full-text test DSN: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	return gdb
}

func TestSupportsVectorDistance_MySQLRequiresTiDBAndFunction(t *testing.T) {
	tests := []struct {
		name            string
		cfg             fakeMySQLCapabilityConfig
		want            bool
		wantVectorProbe bool
	}{
		{
			name: "tidb with vector function",
			cfg: fakeMySQLCapabilityConfig{
				tidbVersion:    "Release Version: v8.5.0",
				vectorDistance: 0,
			},
			want:            true,
			wantVectorProbe: true,
		},
		{
			name: "tidb with null vector distance result",
			cfg: fakeMySQLCapabilityConfig{
				tidbVersion:       "Release Version: v8.5.5",
				vectorDistanceNil: true,
			},
			want:            true,
			wantVectorProbe: true,
		},
		{
			name: "tidb without vector function",
			cfg: fakeMySQLCapabilityConfig{
				tidbVersion:       "Release Version: v7.1.0",
				vectorDistanceErr: errors.New("function VEC_COSINE_DISTANCE does not exist"),
			},
			want:            false,
			wantVectorProbe: true,
		},
		{
			name: "plain mysql skips vector probe",
			cfg: fakeMySQLCapabilityConfig{
				tidbVersionErr: errors.New("function tidb_version does not exist"),
				version:        "8.0.36 MySQL Community Server",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gdb, fakeDriver := openFakeMySQLCapabilityDB(t, tt.cfg)
			if got := SupportsVectorDistance(gdb); got != tt.want {
				t.Fatalf("SupportsVectorDistance() = %v, want %v", got, tt.want)
			}

			sawVectorProbe := false
			for _, query := range fakeDriver.Queries() {
				if strings.Contains(strings.ToUpper(query), "VEC_COSINE_DISTANCE") {
					sawVectorProbe = true
				}
			}
			if sawVectorProbe != tt.wantVectorProbe {
				t.Fatalf("vector probe presence = %v, want %v; queries=%#v", sawVectorProbe, tt.wantVectorProbe, fakeDriver.Queries())
			}
		})
	}
}

func TestPlainMySQLSkipsTiDBOnlySearchDDL(t *testing.T) {
	gdb, fakeDriver := openFakeMySQLCapabilityDB(t, fakeMySQLCapabilityConfig{
		tidbVersionErr: errors.New("function tidb_version does not exist"),
		version:        "8.0.36 MySQL Community Server",
	})

	if err := MigrateIssueSearch(gdb); err != nil {
		t.Fatalf("MigrateIssueSearch: %v", err)
	}
	InitVector(gdb, 3)

	for _, query := range fakeDriver.Queries() {
		upper := strings.ToUpper(query)
		if strings.Contains(upper, "ALTER TABLE") ||
			strings.Contains(upper, "FTS_MATCH_WORD") ||
			strings.Contains(upper, "VEC_COSINE_DISTANCE") {
			t.Fatalf("plain MySQL should not execute TiDB-only SQL, saw %q", query)
		}
	}
}

func TestTiDBSkipsUnavailableSearchFeatureAppDDL(t *testing.T) {
	gdb, fakeDriver := openFakeMySQLCapabilityDB(t, fakeMySQLCapabilityConfig{
		tidbVersion:       "Release Version: v7.1.0",
		fullTextErr:       errors.New("function FTS_MATCH_WORD does not exist"),
		vectorDistanceErr: errors.New("function VEC_COSINE_DISTANCE does not exist"),
	})

	if err := MigrateIssueSearch(gdb); err != nil {
		t.Fatalf("MigrateIssueSearch: %v", err)
	}
	InitVector(gdb, 3)

	var sawFullTextProbe bool
	var sawVectorProbe bool
	for _, query := range fakeDriver.Queries() {
		upper := strings.ToUpper(query)
		if strings.Contains(upper, "FTS_MATCH_WORD") {
			sawFullTextProbe = true
		}
		if strings.Contains(upper, "VEC_COSINE_DISTANCE") {
			sawVectorProbe = true
		}
		if strings.Contains(upper, "ALTER TABLE `ISSUES`") ||
			strings.Contains(upper, "ALTER TABLE `PULL_REQUESTS`") ||
			strings.Contains(upper, "ADD VECTOR INDEX") ||
			strings.Contains(upper, "ADD COLUMN `EMBEDDING`") {
			t.Fatalf("TiDB without feature probes should not execute app feature DDL, saw %q", query)
		}
	}
	if !sawFullTextProbe {
		t.Fatal("expected TiDB full-text capability probe")
	}
	if !sawVectorProbe {
		t.Fatal("expected TiDB vector-distance capability probe")
	}
}
