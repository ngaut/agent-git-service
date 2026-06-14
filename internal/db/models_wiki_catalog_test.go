package db

import (
	"path/filepath"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// TestWikiCatalogAutoMigrate is the guard rail for the catalog DDL. It
// fails fast if a model declaration is invalid on SQLite, which is the
// dialect every unit-test path uses.
func TestWikiCatalogAutoMigrate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wiki-catalog.db")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, err := gdb.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}

	if err := Migrate(gdb); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	tables := []string{
		"wiki_pages",
		"wiki_page_revisions",
		"wiki_changesets",
		"wiki_repo_heads",
		"wiki_dir_index",
		"wiki_page_links",
		"wiki_blob_refs",
		"wiki_pending_blobs",
	}
	for _, table := range tables {
		if !gdb.Migrator().HasTable(table) {
			t.Errorf("expected table %q after Migrate", table)
		}
	}

	indexes := []struct {
		table string
		name  string
	}{
		{"wiki_pages", "idx_wiki_pages_repo_slug_ci"},
		{"wiki_pages", "idx_wiki_pages_repo_updated"},
		{"wiki_pages", "idx_wiki_pages_repo_prefix"},
		{"wiki_page_revisions", "idx_wiki_revisions_changeset"},
		{"wiki_page_revisions", "idx_wiki_revisions_page_commit"},
		{"wiki_page_revisions", "idx_wiki_revisions_superseded"},
		{"wiki_changesets", "idx_wiki_changesets_repo"},
		{"wiki_changesets", "idx_wiki_changesets_parent"},
		{"wiki_changesets", "idx_wiki_changesets_superseded"},
		{"wiki_dir_index", "idx_wiki_dir_repo_parent_kind_name"},
		{"wiki_page_links", "idx_wiki_links_dst_resolved"},
		{"wiki_page_links", "idx_wiki_links_dst_string"},
	}
	for _, idx := range indexes {
		if !gdb.Migrator().HasIndex(idx.table, idx.name) {
			t.Errorf("expected index %q on %q after Migrate", idx.name, idx.table)
		}
	}
}

// TestWikiCatalogRoundTrip exercises insertion/retrieval against each
// new table. This catches column-type mismatches (e.g. forgetting that
// SQLite has no native BIGINT autoinc semantics) before the catalog
// primitive ever runs against real data.
func TestWikiCatalogRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wiki-catalog-rt.db")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := Migrate(gdb); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Seed minimal users/repos required by FKs.
	user := User{Login: "alice", Type: "User", Email: "a@example.com"}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	repo := Repository{OwnerID: user.ID, Name: "rpo", FullName: "alice/rpo", DefaultBranch: "main"}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("create repo: %v", err)
	}

	now := time.Now().UTC()
	cs := WikiChangeset{
		ChangesetID:    1,
		RepositoryID:   repo.ID,
		Message:        "first",
		AuthorID:       &user.ID,
		CommittedAt:    now,
		PageCount:      1,
		Source:         "rest",
		SynthCommitSHA: "0000000000000000000000000000000000000001",
	}
	if err := gdb.Create(&cs).Error; err != nil {
		t.Fatalf("create changeset: %v", err)
	}
	head := WikiRepoHead{RepositoryID: repo.ID, HeadChangesetID: cs.ChangesetID, UpdatedAt: now}
	if err := gdb.Create(&head).Error; err != nil {
		t.Fatalf("create head: %v", err)
	}
	page := WikiPage{
		PageID:          100,
		RepositoryID:    repo.ID,
		Slug:            "Home",
		SlugCIV1:        "home",
		Title:           "Home",
		HeadBlobSHA:     "1111111111111111111111111111111111111111",
		BodySize:        12,
		BodyInline:      []byte("hello world\n"),
		HeadRevisionID:  1,
		HeadChangesetID: cs.ChangesetID,
		LastAuthorID:    &user.ID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := gdb.Create(&page).Error; err != nil {
		t.Fatalf("create page: %v", err)
	}
	rev := WikiPageRevision{
		PageID:      100,
		RevisionID:  1,
		ChangesetID: cs.ChangesetID,
		BlobSHA:     page.HeadBlobSHA,
		BodySize:    page.BodySize,
		BodyInline:  page.BodyInline,
		SlugAtRev:   page.Slug,
		CommitSHA:   cs.SynthCommitSHA,
		Op:          "create",
		AuthorID:    &user.ID,
		CommittedAt: now,
	}
	if err := gdb.Create(&rev).Error; err != nil {
		t.Fatalf("create revision: %v", err)
	}
	dir := WikiDirIndex{
		RepositoryID: repo.ID,
		ParentDir:    "",
		ChildName:    "home",
		ChildKind:    "blob",
		PageID:       &page.PageID,
	}
	if err := gdb.Create(&dir).Error; err != nil {
		t.Fatalf("create dir entry: %v", err)
	}

	// Round-trip readback.
	var got WikiPage
	if err := gdb.First(&got, "page_id = ?", page.PageID).Error; err != nil {
		t.Fatalf("read page: %v", err)
	}
	if got.SlugCIV1 != "home" || got.HeadBlobSHA != page.HeadBlobSHA || got.BodySize != 12 {
		t.Fatalf("page round-trip mismatch: %+v", got)
	}
	if string(got.BodyInline) != "hello world\n" {
		t.Fatalf("body_inline round-trip mismatch: %q", got.BodyInline)
	}

	// Unique constraint on (repo, slug_ci_v1).
	dup := page
	dup.PageID = 101
	if err := gdb.Create(&dup).Error; err == nil {
		t.Fatalf("expected unique violation on (repo, slug_ci_v1)")
	}
}
