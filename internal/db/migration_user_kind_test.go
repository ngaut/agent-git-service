package db

import (
	"path/filepath"
	"testing"

	"gorm.io/gorm"
)

func TestMigrateUserKind_BackfillsEmptyUserKind(t *testing.T) {
	gdb := openSQLiteDB(t, filepath.Join(t.TempDir(), "user-kind-backfill.db"))
	if err := gdb.Exec(`
		CREATE TABLE users (
			id integer primary key,
			login text not null,
			name text,
			email text,
			bio text,
			type text not null,
			status text not null default 'active',
			user_kind text,
			is_anonymous boolean default false,
			site_admin boolean default false,
			default_repository_permission text not null default 'none',
			hide_presence boolean default false,
			created_at datetime,
			updated_at datetime
		)
	`).Error; err != nil {
		t.Fatalf("create users table: %v", err)
	}
	if err := gdb.Exec(`
		INSERT INTO users (id, login, type, user_kind, is_anonymous)
		VALUES
			(1, 'blank-kind', 'User', '', false),
			(2, 'null-kind', 'User', NULL, false),
			(3, 'agent-kind', 'User', 'agent', false),
			(4, 'legacy-anon', 'User', NULL, true)
	`).Error; err != nil {
		t.Fatalf("seed users: %v", err)
	}

	if err := MigrateUserKind(gdb); err != nil {
		t.Fatalf("MigrateUserKind: %v", err)
	}

	var rows []struct {
		Login       string
		UserKind    string
		IsAnonymous bool
	}
	if err := gdb.Table("users").
		Select("login", "user_kind", "is_anonymous").
		Order("id").
		Scan(&rows).Error; err != nil {
		t.Fatalf("load migrated users: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("migrated row count = %d, want 4", len(rows))
	}
	if rows[0].Login != "blank-kind" || rows[0].UserKind != UserKindHuman || rows[0].IsAnonymous {
		t.Fatalf("blank user_kind row = %#v, want human", rows[0])
	}
	if rows[1].Login != "null-kind" || rows[1].UserKind != UserKindHuman || rows[1].IsAnonymous {
		t.Fatalf("null user_kind row = %#v, want human", rows[1])
	}
	if rows[2].Login != "agent-kind" || rows[2].UserKind != UserKindAgent || rows[2].IsAnonymous {
		t.Fatalf("agent user_kind row = %#v, want unchanged agent", rows[2])
	}
	if rows[3].Login != "legacy-anon" || rows[3].UserKind != UserKindAgent || rows[3].IsAnonymous {
		t.Fatalf("legacy anonymous row = %#v, want migrated agent account", rows[3])
	}
}

func TestUserModelCreatesNonNullUserKind(t *testing.T) {
	gdb := openSQLiteDB(t, filepath.Join(t.TempDir(), "user-kind-not-null.db"))
	if err := gdb.AutoMigrate(&User{}); err != nil {
		t.Fatalf("AutoMigrate User: %v", err)
	}
	assertSQLiteColumnNotNull(t, gdb, "users", "user_kind")
}

func assertSQLiteColumnNotNull(t *testing.T, gdb *gorm.DB, table, column string) {
	t.Helper()
	var rows []struct {
		Name    string `gorm:"column:name"`
		NotNull int    `gorm:"column:notnull"`
	}
	if err := gdb.Raw("PRAGMA table_info(" + table + ")").Scan(&rows).Error; err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	for _, row := range rows {
		if row.Name == column {
			if row.NotNull != 1 {
				t.Fatalf("%s.%s notnull = %d, want 1", table, column, row.NotNull)
			}
			return
		}
	}
	t.Fatalf("column %s.%s not found", table, column)
}
