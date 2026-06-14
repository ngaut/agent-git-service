package db

import (
	"testing"
	"time"
)

func TestWikiV2Migrate_Idempotent(t *testing.T) {
	gdb := openTiDB(t)

	if err := Migrate(gdb); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := Migrate(gdb); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	for _, table := range []string{
		"wiki_page_index",
		"wiki_index_state",
		"wiki_backlinks",
		"wiki_page_history",
	} {
		if !gdb.Migrator().HasTable(table) {
			t.Fatalf("expected table %q after Migrate", table)
		}
	}
	for _, idx := range []struct {
		table string
		name  string
	}{
		{table: "wiki_page_index", name: "idx_wiki_page_index_repo_commit"},
		{table: "wiki_page_index", name: "idx_wiki_page_index_repo_updated"},
		{table: "wiki_backlinks", name: "idx_wiki_backlinks_repo_dst"},
		{table: "wiki_backlinks", name: "idx_wiki_backlinks_repo_src"},
		{table: "wiki_page_history", name: "idx_wiki_page_history_repo_slug_committed"},
	} {
		if !gdb.Migrator().HasIndex(idx.table, idx.name) {
			t.Fatalf("expected index %q on %q", idx.name, idx.table)
		}
	}
}

func TestWikiV2RoundTrip(t *testing.T) {
	gdb := openTiDB(t)
	if err := Migrate(gdb); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	user := User{Login: "alice", Type: "User", Email: "a@example.com"}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	repo := Repository{OwnerID: user.ID, Name: "wiki", FullName: "alice/wiki", DefaultBranch: "main"}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("create repo: %v", err)
	}

	now := time.Now().UTC().Round(time.Second)
	state := WikiIndexState{
		RepositoryID:         repo.ID,
		IndexedCommitSHA:     "1111111111111111111111111111111111111111",
		BacklinksIndexedSHA:  "1111111111111111111111111111111111111111",
		IndexedAt:            &now,
		ReconcileRequestedAt: &now,
	}
	if err := gdb.Create(&state).Error; err != nil {
		t.Fatalf("create state: %v", err)
	}

	row := WikiPageIndex{
		RepositoryID:  repo.ID,
		Slug:          "guides/setup",
		HeadBlobSHA:   "2222222222222222222222222222222222222222",
		HeadCommitSHA: state.IndexedCommitSHA,
		Title:         "Setup",
		Size:          42,
		UpdatedAt:     now,
		LastAuthorID:  &user.ID,
	}
	if err := gdb.Create(&row).Error; err != nil {
		t.Fatalf("create index row: %v", err)
	}

	var gotState WikiIndexState
	if err := gdb.First(&gotState, "repository_id = ?", repo.ID).Error; err != nil {
		t.Fatalf("read state: %v", err)
	}
	if gotState.IndexedCommitSHA != state.IndexedCommitSHA || gotState.BacklinksIndexedSHA != state.BacklinksIndexedSHA || gotState.IndexedAt == nil || !gotState.IndexedAt.Equal(now) {
		t.Fatalf("state round-trip mismatch: %+v", gotState)
	}

	var gotRow WikiPageIndex
	if err := gdb.First(&gotRow, "repository_id = ? AND slug = ?", repo.ID, row.Slug).Error; err != nil {
		t.Fatalf("read index row: %v", err)
	}
	if gotRow.HeadBlobSHA != row.HeadBlobSHA || gotRow.HeadCommitSHA != row.HeadCommitSHA || gotRow.Size != row.Size {
		t.Fatalf("row round-trip mismatch: %+v", gotRow)
	}

	link := WikiBacklink{
		RepositoryID: repo.ID,
		SrcSlug:      "guides/setup",
		DstSlug:      "guides/install",
		Resolved:     true,
		UpdatedAt:    now,
	}
	if err := gdb.Create(&link).Error; err != nil {
		t.Fatalf("create backlink row: %v", err)
	}

	history := WikiPageHistory{
		RepositoryID:    repo.ID,
		Slug:            row.Slug,
		CommitSHA:       "3333333333333333333333333333333333333333",
		ParentCommitSHA: state.IndexedCommitSHA,
		PathSequence:    2,
		AuthorID:        &user.ID,
		CommitterID:     &user.ID,
		Message:         "Import wiki page snapshot",
		BodySize:        42,
		CommittedAt:     now,
	}
	if err := gdb.Create(&history).Error; err != nil {
		t.Fatalf("create history row: %v", err)
	}

	var gotLink WikiBacklink
	if err := gdb.First(&gotLink, "repository_id = ? AND src_slug = ? AND dst_slug = ?", repo.ID, link.SrcSlug, link.DstSlug).Error; err != nil {
		t.Fatalf("read backlink row: %v", err)
	}
	if gotLink.Resolved != link.Resolved {
		t.Fatalf("backlink round-trip mismatch: %+v", gotLink)
	}

	var gotHistory WikiPageHistory
	if err := gdb.First(&gotHistory, "repository_id = ? AND slug = ? AND commit_sha = ?", repo.ID, history.Slug, history.CommitSHA).Error; err != nil {
		t.Fatalf("read history row: %v", err)
	}
	if gotHistory.ParentCommitSHA != history.ParentCommitSHA || gotHistory.PathSequence != history.PathSequence || gotHistory.Message != history.Message || gotHistory.BodySize != history.BodySize || !gotHistory.CommittedAt.Equal(history.CommittedAt) {
		t.Fatalf("history round-trip mismatch: %+v", gotHistory)
	}
}
