package db

import "testing"

func TestMigrateTeamOrgSlugIndex_TiDB(t *testing.T) {
	gdb := openTiDB(t)
	if err := gdb.AutoMigrate(&User{}, &Team{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	if err := MigrateTeamOrgSlugIndex(gdb); err != nil {
		t.Fatalf("MigrateTeamOrgSlugIndex: %v", err)
	}
	if err := MigrateTeamOrgSlugIndex(gdb); err != nil {
		t.Fatalf("MigrateTeamOrgSlugIndex second run: %v", err)
	}

	if !gdb.Migrator().HasIndex(&Team{}, "idx_org_slug") {
		t.Fatal("expected idx_org_slug to exist after migration")
	}

	hasCorrectShape, err := hasCorrectIndexShape(gdb, "teams", "idx_org_slug", []string{"organization_id", "slug"})
	if err != nil {
		t.Fatalf("hasCorrectIndexShape: %v", err)
	}
	if !hasCorrectShape {
		t.Fatal("expected idx_org_slug to have (organization_id, slug) shape")
	}
}
