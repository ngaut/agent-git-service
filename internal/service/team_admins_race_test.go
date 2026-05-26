package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"

	"github.com/ngaut/agent-git-service/internal/db"
)

var fakeTeamAdminsMySQLDriverSeq uint64

type fakeTeamAdminsMySQLDriver struct {
	lastInsertID int64

	mu      sync.Mutex
	queries []string
}

func (d *fakeTeamAdminsMySQLDriver) Open(_ string) (driver.Conn, error) {
	return &fakeTeamAdminsMySQLConn{driver: d}, nil
}

func (d *fakeTeamAdminsMySQLDriver) record(query string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queries = append(d.queries, query)
}

func (d *fakeTeamAdminsMySQLDriver) Queries() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.queries))
	copy(out, d.queries)
	return out
}

type fakeTeamAdminsMySQLConn struct {
	driver *fakeTeamAdminsMySQLDriver
}

func (c *fakeTeamAdminsMySQLConn) Prepare(_ string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepare is not implemented")
}

func (c *fakeTeamAdminsMySQLConn) Close() error { return nil }

func (c *fakeTeamAdminsMySQLConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("transactions are not implemented")
}

func (c *fakeTeamAdminsMySQLConn) Ping(_ context.Context) error { return nil }

func (c *fakeTeamAdminsMySQLConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.driver.record(query)
	return nil, fmt.Errorf("unexpected query: %s", query)
}

func (c *fakeTeamAdminsMySQLConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.driver.record(query)
	return fakeTeamAdminsMySQLResult(c.driver.lastInsertID), nil
}

type fakeTeamAdminsMySQLResult int64

func (r fakeTeamAdminsMySQLResult) LastInsertId() (int64, error) { return int64(r), nil }
func (r fakeTeamAdminsMySQLResult) RowsAffected() (int64, error) { return 1, nil }

func openFakeTeamAdminsMySQLDB(t *testing.T, lastInsertID int64) (*gorm.DB, *fakeTeamAdminsMySQLDriver) {
	t.Helper()

	driverName := fmt.Sprintf("fake_team_admins_mysql_%d", atomic.AddUint64(&fakeTeamAdminsMySQLDriverSeq, 1))
	fakeDriver := &fakeTeamAdminsMySQLDriver{lastInsertID: lastInsertID}
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

// TestEnsureAdminsTeamTx_SurvivesLostRace guards the fix for the TOCTOU race
// in ensureAdminsTeamTx that let a second concurrent ForkRepo/CreateRepo
// return HTTP 500 on a unique-key violation.
//
// ensureAdminsTeamTx runs tx.First() → returns fast on hit, or tx.Create().
// Pre-fix, two concurrent transactions could both pass First()'s miss and
// both Create(); the loser got a 1062 / idx_org_slug error. The fix wraps
// Create with clause.OnConflict{DoNothing} and re-reads when the returned
// row has no ID.
//
// The race can't be driven end-to-end against SQLite: its single-writer file
// lock serializes concurrent writers and manifests conflicts as "database is
// locked", not as the TiDB-shape duplicate-key error the fix targets. This
// test therefore verifies the fix in two complementary pieces that together
// cover the full code path:
//
//  1. The OnConflict{DoNothing} mechanism — direct assertion that issuing
//     Create() against a pre-seeded row absorbs the conflict without error
//     and leaves ID=0, which is the signal the fix's re-read relies on.
//  2. A follow-up First() recovers the pre-seeded row — proving the re-read
//     half of the fix returns the canonical ID.
//  3. End-to-end ensureAdminsTeamTx still returns the canonical team when
//     called after the winner's commit (fast path).
//
// All three are needed: remove any one and a regression could sneak through.
func TestEnsureAdminsTeamTx_SurvivesLostRace(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "team-admins-race.db")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, _ := gdb.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Migrate(gdb); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	org := db.User{Login: "raceorg", Name: "raceorg", Type: db.TypeOrganization}
	if err := gdb.Create(&org).Error; err != nil {
		t.Fatalf("create org: %v", err)
	}

	// Seed the "winner" admins team.
	winner := db.Team{
		OrganizationID: org.ID,
		Name:           adminsTeamSlug,
		Slug:           adminsTeamSlug,
		Privacy:        db.TeamPrivacyClosed,
	}
	if err := gdb.Create(&winner).Error; err != nil {
		t.Fatalf("seed winning team: %v", err)
	}

	// Part 1: OnConflict{DoNothing} mechanism — this is the literal statement
	// the fix inserts into ensureAdminsTeamTx. Attempting to insert a
	// duplicate must absorb the conflict, return no error, and leave ID=0
	// so the re-read branch knows to run.
	loser := db.Team{
		OrganizationID: org.ID,
		Name:           adminsTeamSlug,
		Slug:           adminsTeamSlug,
		Privacy:        db.TeamPrivacyClosed,
	}
	if err := gdb.Clauses(clause.OnConflict{DoNothing: true}).Create(&loser).Error; err != nil {
		t.Fatalf("OnConflict{DoNothing} must absorb the duplicate; got: %v", err)
	}
	if loser.ID != 0 {
		t.Fatalf("OnConflict{DoNothing} populated loser.ID=%d; fix relies on zero as the re-read signal", loser.ID)
	}

	// Part 2: re-read recovers the winner's canonical row.
	var recovered db.Team
	if err := gdb.First(&recovered, "organization_id = ? AND slug = ?", org.ID, adminsTeamSlug).Error; err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if recovered.ID != winner.ID {
		t.Errorf("re-read recovered team.ID=%d, want winner.ID=%d", recovered.ID, winner.ID)
	}

	// Part 3: end-to-end call still returns the canonical team (fast path
	// here because First() hits; pre-fix this assertion held too, so its
	// job is to catch a regression that breaks the fast path).
	var got db.Team
	err = gdb.Transaction(func(tx *gorm.DB) error {
		var inner error
		got, inner = ensureAdminsTeamTx(tx, org.ID)
		return inner
	})
	if err != nil {
		t.Fatalf("ensureAdminsTeamTx: %v", err)
	}
	if got.ID != winner.ID {
		t.Errorf("ensureAdminsTeamTx returned team.ID=%d, want %d", got.ID, winner.ID)
	}

	// Invariant: exactly one admins team per org.
	var count int64
	if err := gdb.Model(&db.Team{}).
		Where("organization_id = ? AND slug = ?", org.ID, adminsTeamSlug).
		Count(&count).Error; err != nil {
		t.Fatalf("count admins teams: %v", err)
	}
	if count != 1 {
		t.Errorf("found %d admins teams, want 1", count)
	}
}

// TestEnsureAdminsTeamTx_NoTeamPathStillCreates covers the uncontended path —
// no admins team exists, ensureAdminsTeamTx must create it and return the new
// row with a populated ID. Guards against an over-eager DoNothing that would
// leave team.ID == 0 in the happy path.
func TestEnsureAdminsTeamTx_NoTeamPathStillCreates(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "team-admins-create.db")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, _ := gdb.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Migrate(gdb); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	org := db.User{Login: "freshorg", Name: "freshorg", Type: db.TypeOrganization}
	if err := gdb.Create(&org).Error; err != nil {
		t.Fatalf("create org: %v", err)
	}

	var got db.Team
	err = gdb.Transaction(func(tx *gorm.DB) error {
		var inner error
		got, inner = ensureAdminsTeamTx(tx, org.ID)
		return inner
	})
	if err != nil {
		t.Fatalf("ensureAdminsTeamTx: %v", err)
	}
	if got.ID == 0 {
		t.Error("new admins team returned with zero ID")
	}
	if got.Slug != adminsTeamSlug {
		t.Errorf("slug = %q, want %q", got.Slug, adminsTeamSlug)
	}
}

func TestEnsureAdminsTeamTx_MySQLUsesSingleStatementUpsert(t *testing.T) {
	gdb, fakeDriver := openFakeTeamAdminsMySQLDB(t, 42)

	team, err := ensureAdminsTeamTx(gdb.WithContext(context.Background()), 7)
	if err != nil {
		t.Fatalf("ensureAdminsTeamTx(mysql): %v", err)
	}
	if team.ID != 42 {
		t.Fatalf("team.ID = %d, want 42", team.ID)
	}
	if team.OrganizationID != 7 {
		t.Fatalf("team.OrganizationID = %d, want 7", team.OrganizationID)
	}

	queries := fakeDriver.Queries()
	if len(queries) != 1 {
		t.Fatalf("expected exactly one mysql statement, got %d (%v)", len(queries), queries)
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(queries[0]), " "))
	if !strings.Contains(normalized, "insert into teams") {
		t.Fatalf("query %q did not insert into teams", queries[0])
	}
	if !strings.Contains(normalized, "last_insert_id(id)") {
		t.Fatalf("query %q did not preserve the canonical id via LAST_INSERT_ID(id)", queries[0])
	}
	if strings.Contains(normalized, "select") {
		t.Fatalf("query %q unexpectedly performed a follow-up SELECT", queries[0])
	}
}
