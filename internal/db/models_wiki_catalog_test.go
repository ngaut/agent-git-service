package db

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestWikiCatalogAutoMigrate is the guard rail for the catalog DDL. It
// fails fast if a model declaration is invalid on TiDB, matching the
// production database dialect.
func TestWikiCatalogAutoMigrate(t *testing.T) {
	gdb := openTiDB(t)

	if err := Migrate(gdb); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	tables := []string{
		"wiki_pages",
		"wiki_page_revisions",
		"wiki_changesets",
		"wiki_repo_heads",
		"wiki_git_repair_obligations",
		"wiki_page_links",
		"wiki_blob_refs",
		"wiki_pending_blobs",
		"wiki_search_projection_tasks",
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
		{"wiki_pages", "idx_wiki_pages_repo_slug"},
		{"wiki_pages", "idx_wiki_pages_repo_updated"},
		{"wiki_pages", "idx_wiki_pages_repo_prefix"},
		{"wiki_page_revisions", "idx_wiki_revisions_changeset"},
		{"wiki_page_revisions", "idx_wiki_revisions_page_commit"},
		{"wiki_page_revisions", "idx_wiki_revisions_superseded"},
		{"wiki_changesets", "idx_wiki_changesets_repo"},
		{"wiki_changesets", "idx_wiki_changesets_parent"},
		{"wiki_changesets", "idx_wiki_changesets_superseded"},
		{"wiki_repo_heads", "idx_wiki_repo_heads_reference_pending"},
		{"wiki_page_links", "idx_wiki_links_dst_resolved"},
		{"wiki_page_links", "idx_wiki_links_repo_dst_slug"},
		{"wiki_search_projection_tasks", "idx_wiki_search_projection_task"},
		{"wiki_search_projection_tasks", "idx_wiki_search_projection_claim"},
	}
	for _, idx := range indexes {
		if !gdb.Migrator().HasIndex(idx.table, idx.name) {
			t.Errorf("expected index %q on %q after Migrate", idx.name, idx.table)
		}
	}
	if !gdb.Migrator().HasConstraint(&WikiSearchProjectionTask{}, "Repository") {
		t.Error("expected repository foreign key on wiki_search_projection_tasks after Migrate")
	}
}

func TestWikiSlugModelTypesAvoidIndexedCollationRewrite(t *testing.T) {
	wikiSearchSlugTag := gormTag(t, WikiSearchDocument{}, "Slug")
	if strings.Contains(wikiSearchSlugTag, "varbinary") {
		t.Fatalf("WikiSearchDocument.Slug gorm tag = %q, want varchar/size tag to avoid TiDB indexed collation rewrite", wikiSearchSlugTag)
	}
	if !strings.Contains(wikiSearchSlugTag, "size:255") {
		t.Fatalf("WikiSearchDocument.Slug gorm tag = %q, want size:255", wikiSearchSlugTag)
	}
	projectionSlugTag := gormTag(t, WikiSearchProjectionTask{}, "Slug")
	if !strings.Contains(projectionSlugTag, "size:255") {
		t.Fatalf("WikiSearchProjectionTask.Slug gorm tag = %q, want size:255", projectionSlugTag)
	}

	wikiPageSlugTag := gormTag(t, WikiPage{}, "Slug")
	if !strings.Contains(wikiPageSlugTag, "type:varbinary(1024)") {
		t.Fatalf("WikiPage.Slug gorm tag = %q, want existing varbinary(1024) width preserved", wikiPageSlugTag)
	}

	wikiLinkDstSlugTag := gormTag(t, WikiPageLink{}, "DstSlug")
	if !strings.Contains(wikiLinkDstSlugTag, "type:varbinary(384)") {
		t.Fatalf("WikiPageLink.DstSlug gorm tag = %q, want renamed dst_slug_ci width preserved", wikiLinkDstSlugTag)
	}
}

func gormTag(t *testing.T, model any, fieldName string) string {
	t.Helper()
	field, ok := reflect.TypeOf(model).FieldByName(fieldName)
	if !ok {
		t.Fatalf("%T missing field %s", model, fieldName)
	}
	return field.Tag.Get("gorm")
}

// TestWikiCatalogRoundTrip exercises insertion/retrieval against each
// new table. This catches TiDB column-type and constraint mismatches before
// the catalog primitive ever runs against real data.
func TestWikiCatalogRoundTrip(t *testing.T) {
	gdb := openTiDB(t)
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
		Slug:            "home",
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
	// Round-trip readback.
	var got WikiPage
	if err := gdb.First(&got, "page_id = ?", page.PageID).Error; err != nil {
		t.Fatalf("read page: %v", err)
	}
	if got.Slug != "home" || got.HeadBlobSHA != page.HeadBlobSHA || got.BodySize != 12 {
		t.Fatalf("page round-trip mismatch: %+v", got)
	}
	if string(got.BodyInline) != "hello world\n" {
		t.Fatalf("body_inline round-trip mismatch: %q", got.BodyInline)
	}

	// Unique constraint on (repo, slug).
	dup := page
	dup.PageID = 101
	if err := gdb.Create(&dup).Error; err == nil {
		t.Fatalf("expected unique violation on (repo, slug)")
	}
}
