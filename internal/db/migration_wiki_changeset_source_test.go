package db

import "testing"

func TestMigrateWikiChangesetSourceGit(t *testing.T) {
	gdb := openTiDB(t)
	if err := gdb.Exec(`
		CREATE TABLE wiki_changesets (
			changeset_id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
			source CHAR(16) NOT NULL
		)
	`).Error; err != nil {
		t.Fatalf("create wiki_changesets: %v", err)
	}
	if err := gdb.Exec("INSERT INTO wiki_changesets (source) VALUES ('migration'), ('rest')").Error; err != nil {
		t.Fatalf("seed wiki_changesets: %v", err)
	}

	if err := MigrateWikiChangesetSourceGit(gdb); err != nil {
		t.Fatalf("MigrateWikiChangesetSourceGit: %v", err)
	}

	var sources []string
	if err := gdb.Table("wiki_changesets").Order("changeset_id").Pluck("source", &sources).Error; err != nil {
		t.Fatalf("read wiki_changesets.source: %v", err)
	}
	want := []string{"git", "rest"}
	if len(sources) != len(want) {
		t.Fatalf("sources = %v, want %v", sources, want)
	}
	for i := range sources {
		if sources[i] != want[i] {
			t.Fatalf("sources = %v, want %v", sources, want)
		}
	}
}
