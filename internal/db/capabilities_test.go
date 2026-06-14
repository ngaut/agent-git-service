package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var fakeMySQLDriverSeq uint64

type fakeMySQLCapabilityConfig struct {
	tidbVersionErr    error
	tidbVersion       string
	versionErr        error
	version           string
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
	case "select fts_match_word(?, ?)":
		if c.driver.cfg.fullTextErr != nil {
			return nil, c.driver.cfg.fullTextErr
		}
		return &singleValueRows{columns: []string{"FTS_MATCH_WORD(?, ?)"}, values: []driver.Value{c.driver.cfg.fullTextScore}}, nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
}

func (c *fakeMySQLCapabilityConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.driver.record(query)
	return nil, fmt.Errorf("unexpected exec: %s", query)
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

func TestSupportsTiDBSearch(t *testing.T) {
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
			if got := SupportsTiDBSearch(gdb); got != tt.want {
				t.Fatalf("SupportsTiDBSearch() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSupportsTiDBSearch_NonMySQLFalse(t *testing.T) {
	gdb := &gorm.DB{Config: &gorm.Config{Dialector: postgres.Open("postgres://user:pass@localhost:5432/testdb?sslmode=disable")}}
	if SupportsTiDBSearch(gdb) {
		t.Fatal("expected postgres to report no TiDB search support")
	}
}

func TestSupportsTiDBFullText_MySQLRequiresTiDBAndFunction(t *testing.T) {
	tests := []struct {
		name              string
		cfg               fakeMySQLCapabilityConfig
		want              bool
		wantFullTextProbe bool
	}{
		{
			name: "tidb with fts_match_word",
			cfg: fakeMySQLCapabilityConfig{
				tidbVersion:   "Release Version: v8.5.0",
				fullTextScore: 1,
			},
			want:              true,
			wantFullTextProbe: true,
		},
		{
			name: "tidb without fts_match_word",
			cfg: fakeMySQLCapabilityConfig{
				tidbVersion: "Release Version: v8.1.0",
				fullTextErr: errors.New("function FTS_MATCH_WORD does not exist"),
			},
			want:              false,
			wantFullTextProbe: true,
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

			sawFullTextProbe := false
			for _, query := range fakeDriver.Queries() {
				if strings.Contains(strings.ToUpper(query), "FTS_MATCH_WORD") {
					sawFullTextProbe = true
				}
			}
			if sawFullTextProbe != tt.wantFullTextProbe {
				t.Fatalf("full-text probe presence = %v, want %v; queries=%#v", sawFullTextProbe, tt.wantFullTextProbe, fakeDriver.Queries())
			}
		})
	}
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
