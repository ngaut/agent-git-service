package db

import (
	"path/filepath"
	"testing"
)

func TestMigrateWikiSlugColumns_CleansCatalogSlugCI(t *testing.T) {
	gdb := openSQLiteDB(t, filepath.Join(t.TempDir(), "wiki-slug-cleanup.db"))
	if err := gdb.Exec("CREATE TABLE wiki_pages (page_id integer primary key, repository_id integer, slug text, slug_ci_v1 text)").Error; err != nil {
		t.Fatalf("create wiki_pages: %v", err)
	}
	if err := gdb.Exec("CREATE UNIQUE INDEX idx_wiki_pages_repo_slug_ci ON wiki_pages (repository_id, slug_ci_v1)").Error; err != nil {
		t.Fatalf("create slug_ci index: %v", err)
	}

	if err := MigrateWikiSlugColumns(gdb); err != nil {
		t.Fatalf("MigrateWikiSlugColumns: %v", err)
	}
	if gdb.Migrator().HasIndex("wiki_pages", "idx_wiki_pages_repo_slug_ci") {
		t.Fatal("expected idx_wiki_pages_repo_slug_ci to be dropped")
	}
	if gdb.Migrator().HasColumn("wiki_pages", "slug_ci_v1") {
		t.Fatal("expected wiki_pages.slug_ci_v1 to be dropped")
	}
}

func TestMigrateWikiSlugColumnsBeforeAutoMigrate_RenamesLinkDstSlug(t *testing.T) {
	gdb := openSQLiteDB(t, filepath.Join(t.TempDir(), "wiki-link-slug-rename.db"))
	if err := gdb.Exec("CREATE TABLE wiki_page_links (repository_id integer, src_page_id integer, dst_slug_ci text, dst_page_id integer, primary key (src_page_id, dst_slug_ci))").Error; err != nil {
		t.Fatalf("create wiki_page_links: %v", err)
	}
	if err := gdb.Exec("INSERT INTO wiki_page_links (repository_id, src_page_id, dst_slug_ci) VALUES (1, 10, 'home')").Error; err != nil {
		t.Fatalf("seed wiki_page_links: %v", err)
	}

	if err := MigrateWikiSlugColumnsBeforeAutoMigrate(gdb); err != nil {
		t.Fatalf("MigrateWikiSlugColumnsBeforeAutoMigrate: %v", err)
	}
	if gdb.Migrator().HasColumn("wiki_page_links", "dst_slug_ci") {
		t.Fatal("expected wiki_page_links.dst_slug to be renamed")
	}
	if !gdb.Migrator().HasColumn("wiki_page_links", "dst_slug") {
		t.Fatal("expected wiki_page_links.dst_slug to exist")
	}
	var dst string
	if err := gdb.Table("wiki_page_links").Select("dst_slug").Where("src_page_id = ?", 10).Scan(&dst).Error; err != nil {
		t.Fatalf("read renamed dst_slug: %v", err)
	}
	if dst != "home" {
		t.Fatalf("dst_slug = %q, want home", dst)
	}
}
