package db

import "testing"

func TestMigrateTeamMemberLookupIndexes_TiDB(t *testing.T) {
	gdb := openTiDB(t)
	if err := gdb.AutoMigrate(&Team{}, &User{}, &TeamMember{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	if err := MigrateTeamMemberLookupIndexes(gdb); err != nil {
		t.Fatalf("MigrateTeamMemberLookupIndexes: %v", err)
	}
	if err := MigrateTeamMemberLookupIndexes(gdb); err != nil {
		t.Fatalf("MigrateTeamMemberLookupIndexes second run: %v", err)
	}
	if !gdb.Migrator().HasIndex(&TeamMember{}, "idx_team_members_user") {
		t.Fatal("expected idx_team_members_user to exist")
	}
}
