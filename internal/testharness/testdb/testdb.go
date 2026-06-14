package testdb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	driversql "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const defaultAdminDSN = "root:@tcp(127.0.0.1:4000)/?parseTime=true&timeout=10s"

var dbSeq atomic.Uint64

type Options struct {
	Prefix string
	Logger gormlogger.Interface
}

type SchemaPool struct {
	TemplateDB string
	Prefix     string
	Logger     gormlogger.Interface

	mu             sync.Mutex
	idle           []*pooledDB
	templateTables []string
}

type pooledDB struct {
	name     string
	gdb      *gorm.DB
	sqlDB    *sql.DB
	adminSQL *sql.DB
}

func Open(t testing.TB, opts Options) (*gorm.DB, func()) {
	t.Helper()
	dbName, dsn, adminSQL := CreateDatabase(t, opts)
	return openExisting(t, dbName, dsn, adminSQL, opts.Logger)
}

func (p *SchemaPool) Open(t testing.TB) (*gorm.DB, func()) {
	t.Helper()
	if p.TemplateDB == "" {
		t.Fatal("testdb: SchemaPool.TemplateDB is required")
	}

	pdb := p.checkout(t)
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			tables, ok, err := p.schemaTableSetMatches(context.Background(), pdb)
			if err != nil {
				t.Logf("testdb: compare pooled database %s with template %s: %v; dropping it", pdb.name, p.TemplateDB, err)
				pdb.drop()
				return
			}
			if !ok {
				t.Logf("testdb: pooled database %s schema differs from template %s; dropping it", pdb.name, p.TemplateDB)
				pdb.drop()
				return
			}
			if err := resetDataTables(context.Background(), pdb.gdb, tables); err != nil {
				t.Logf("testdb: reset pooled database %s: %v; dropping it", pdb.name, err)
				pdb.drop()
				return
			}
			configureSQLPool(pdb.sqlDB)
			p.mu.Lock()
			p.idle = append(p.idle, pdb)
			p.mu.Unlock()
		})
	}
	return pdb.gdb.Session(&gorm.Session{NewDB: true}), cleanup
}

func (p *SchemaPool) checkout(t testing.TB) *pooledDB {
	t.Helper()
	p.mu.Lock()
	if n := len(p.idle); n > 0 {
		pdb := p.idle[n-1]
		p.idle = p.idle[:n-1]
		p.mu.Unlock()
		return pdb
	}
	p.mu.Unlock()

	dbName, dsn, adminSQL := createDatabase(t, Options{Prefix: p.Prefix}, false)
	if err := CloneSchema(t.Context(), adminSQL, p.TemplateDB, dbName); err != nil {
		_, _ = adminSQL.Exec("DROP DATABASE IF EXISTS `" + dbName + "`")
		_ = adminSQL.Close()
		t.Fatalf("testdb: clone schema from %s to %s: %v", p.TemplateDB, dbName, err)
	}
	gdb, drop := openExisting(t, dbName, dsn, adminSQL, p.Logger)
	_ = drop
	sqlDB, err := gdb.DB()
	if err != nil {
		_, _ = adminSQL.Exec("DROP DATABASE IF EXISTS `" + dbName + "`")
		_ = adminSQL.Close()
		t.Fatalf("testdb: underlying sql.DB for pooled %s: %v", dbName, err)
	}
	return &pooledDB{name: dbName, gdb: gdb, sqlDB: sqlDB, adminSQL: adminSQL}
}

func (p *SchemaPool) schemaTableSetMatches(ctx context.Context, pdb *pooledDB) ([]string, bool, error) {
	templateTables, err := p.templateTableNames(ctx, pdb.adminSQL)
	if err != nil {
		return nil, false, err
	}
	currentTables, err := baseTableNames(ctx, pdb.adminSQL, pdb.name)
	if err != nil {
		return nil, false, err
	}
	if len(templateTables) != len(currentTables) {
		return nil, false, nil
	}
	for i := range templateTables {
		if templateTables[i] != currentTables[i] {
			return nil, false, nil
		}
	}
	return currentTables, true, nil
}

func (p *SchemaPool) templateTableNames(ctx context.Context, db *sql.DB) ([]string, error) {
	p.mu.Lock()
	if len(p.templateTables) > 0 {
		tables := append([]string(nil), p.templateTables...)
		p.mu.Unlock()
		return tables, nil
	}
	p.mu.Unlock()

	tables, err := baseTableNames(ctx, db, p.TemplateDB)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	if len(p.templateTables) == 0 {
		p.templateTables = append([]string(nil), tables...)
	}
	out := append([]string(nil), p.templateTables...)
	p.mu.Unlock()
	return out, nil
}

func (pdb *pooledDB) drop() {
	_ = pdb.sqlDB.Close()
	_, _ = pdb.adminSQL.Exec("DROP DATABASE IF EXISTS `" + pdb.name + "`")
	_ = pdb.adminSQL.Close()
}

func openExisting(t testing.TB, dbName, dsn string, adminSQL *sql.DB, logger gormlogger.Interface) (*gorm.DB, func()) {
	t.Helper()
	cfg, err := driversql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("testdb: parse generated DSN: %v", err)
	}
	if logger == nil {
		logger = gormlogger.Default.LogMode(gormlogger.Warn)
	}
	gdb, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger})
	if err != nil {
		_, _ = adminSQL.Exec("DROP DATABASE IF EXISTS `" + dbName + "`")
		t.Fatalf("testdb: open database %s: %v", dbName, err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		_, _ = adminSQL.Exec("DROP DATABASE IF EXISTS `" + dbName + "`")
		t.Fatalf("testdb: underlying sql.DB for %s: %v", dbName, err)
	}
	configureSQLPool(sqlDB)

	cleanup := func() {
		_ = sqlDB.Close()
		if os.Getenv("KEEP_TEST_DB") == "1" {
			t.Logf("testdb: keeping %s because KEEP_TEST_DB=1", dbName)
			return
		}
		if _, err := adminSQL.Exec("DROP DATABASE IF EXISTS `" + dbName + "`"); err != nil {
			t.Logf("testdb: drop database %s: %v", dbName, err)
		}
	}
	return gdb, cleanup
}

func OpenRaw(t testing.TB, prefix string) (*gorm.DB, func()) {
	t.Helper()
	return Open(t, Options{Prefix: prefix})
}

func CreateDatabase(t testing.TB, opts Options) (string, string, *sql.DB) {
	t.Helper()
	return createDatabase(t, opts, true)
}

func createDatabase(t testing.TB, opts Options, cleanupAdmin bool) (string, string, *sql.DB) {
	t.Helper()

	adminDSN := strings.TrimSpace(os.Getenv("TEST_DB_DSN"))
	if adminDSN == "" {
		adminDSN = defaultAdminDSN
	}

	cfg, err := driversql.ParseDSN(adminDSN)
	if err != nil {
		t.Fatalf("testdb: parse TEST_DB_DSN: %v", err)
	}
	cfg.DBName = ""
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["parseTime"] = "true"
	cfg.Params["multiStatements"] = "true"
	if _, ok := cfg.Params["timeout"]; !ok {
		cfg.Params["timeout"] = "10s"
	}

	adminSQL, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatalf("testdb: open admin connection: %v", err)
	}
	if cleanupAdmin {
		t.Cleanup(func() { _ = adminSQL.Close() })
	}
	if err := adminSQL.Ping(); err != nil {
		t.Fatalf("testdb: ping TiDB playground using TEST_DB_DSN: %v", err)
	}
	requireTiDB(t, adminSQL)

	dbName := uniqueDBName(opts.Prefix)
	if _, err := adminSQL.Exec("CREATE DATABASE `" + dbName + "`"); err != nil {
		t.Fatalf("testdb: create database %s: %v", dbName, err)
	}

	cfg.DBName = dbName
	return dbName, cfg.FormatDSN(), adminSQL
}

func CreateRawDatabase(t testing.TB, prefix string) (string, string, *sql.DB) {
	t.Helper()
	return CreateDatabase(t, Options{Prefix: prefix})
}

func CloneSchema(ctx context.Context, adminSQL *sql.DB, templateDB, targetDB string) error {
	templateDB = strings.TrimSpace(templateDB)
	targetDB = strings.TrimSpace(targetDB)
	if templateDB == "" || targetDB == "" {
		return fmt.Errorf("template and target database names are required")
	}

	conn, err := adminSQL.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	tables, err := baseTableNames(ctx, adminSQL, templateDB)
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return fmt.Errorf("template database %s has no tables", templateDB)
	}

	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=0"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "SET FOREIGN_KEY_CHECKS=1")
	if _, err := conn.ExecContext(ctx, "USE "+quoteIdentifier(targetDB)); err != nil {
		return err
	}

	for _, table := range tables {
		var tableName, createSQL string
		if err := conn.QueryRowContext(ctx, "SHOW CREATE TABLE "+quoteIdentifier(templateDB)+"."+quoteIdentifier(table)).Scan(&tableName, &createSQL); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, createSQL); err != nil {
			return fmt.Errorf("create table %s: %w", table, err)
		}
	}
	return nil
}

func baseTableNames(ctx context.Context, db *sql.DB, schema string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT TABLE_NAME
		FROM information_schema.tables
		WHERE table_schema = ? AND table_type = 'BASE TABLE'
		ORDER BY TABLE_NAME`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

func resetDataTables(ctx context.Context, database *gorm.DB, tables []string) error {
	sqlDB, err := database.DB()
	if err != nil {
		return err
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return resetDataTablesWithConn(ctx, conn, tables)
}

func resetDataTablesWithConn(ctx context.Context, conn *sql.Conn, tables []string) error {
	var sql strings.Builder
	sql.WriteString("SET FOREIGN_KEY_CHECKS=0;")
	for _, table := range tables {
		sql.WriteString("DELETE FROM ")
		sql.WriteString(quoteIdentifier(table))
		sql.WriteString(";")
	}
	sql.WriteString("SET FOREIGN_KEY_CHECKS=1;")
	if _, err := conn.ExecContext(ctx, sql.String()); err != nil {
		return err
	}
	return nil
}

func requireTiDB(t testing.TB, db *sql.DB) {
	t.Helper()
	var tidbVersion sql.NullString
	if err := db.QueryRow("SELECT tidb_version()").Scan(&tidbVersion); err == nil {
		return
	}
	var version string
	if err := db.QueryRow("SELECT VERSION()").Scan(&version); err != nil {
		t.Fatalf("testdb: verify TiDB version: %v", err)
	}
	if !strings.Contains(strings.ToLower(version), "tidb") {
		t.Fatalf("testdb: TEST_DB_DSN must point to TiDB, got VERSION()=%q", version)
	}
}

func quoteIdentifier(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

func configureSQLPool(sqlDB *sql.DB) {
	sqlDB.SetMaxIdleConns(4)
	sqlDB.SetMaxOpenConns(16)
	sqlDB.SetConnMaxIdleTime(30 * time.Second)
	sqlDB.SetConnMaxLifetime(2 * time.Minute)
}

func uniqueDBName(prefix string) string {
	prefix = sanitizeIdentifierPart(prefix)
	if prefix == "" {
		prefix = "test"
	}
	return fmt.Sprintf("ags_%s_%d_%d_%d", prefix, os.Getpid(), time.Now().UnixNano(), dbSeq.Add(1))
}

func sanitizeIdentifierPart(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_':
			b.WriteRune(r)
		case r == '-' || r == '.' || r == '/':
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}
