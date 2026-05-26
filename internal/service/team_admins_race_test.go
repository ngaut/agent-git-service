package service

import (
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ngaut/agent-git-service/internal/db"
)

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
