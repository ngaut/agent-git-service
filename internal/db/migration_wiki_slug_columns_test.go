package db

import "testing"

func TestMigrateWikiSlugColumns_CleansCatalogSlugCI(t *testing.T) {
	gdb := openTiDB(t)
	if err := gdb.Exec("CREATE TABLE wiki_pages (page_id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT, repository_id BIGINT UNSIGNED, slug VARBINARY(1024), slug_ci_v1 VARBINARY(1024))").Error; err != nil {
		t.Fatalf("create wiki_pages: %v", err)
	}
	if err := gdb.Exec("CREATE UNIQUE INDEX idx_wiki_pages_repo_slug_ci ON wiki_pages (repository_id, slug_ci_v1)").Error; err != nil {
		t.Fatalf("create slug_ci index: %v", err)
	}
	if err := gdb.Exec("CREATE INDEX idx_wiki_pages_repo_prefix ON wiki_pages (repository_id, slug_ci_v1)").Error; err != nil {
		t.Fatalf("create prefix index: %v", err)
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
	if !gdb.Migrator().HasIndex("wiki_pages", "idx_wiki_pages_repo_prefix") {
		t.Fatal("expected idx_wiki_pages_repo_prefix to be rebuilt")
	}
	if cols, err := mysqlIndexColumns(gdb, "wiki_pages", "idx_wiki_pages_repo_prefix"); err != nil {
		t.Fatalf("read idx_wiki_pages_repo_prefix columns: %v", err)
	} else if len(cols) != 2 || cols[0] != "repository_id" || cols[1] != "slug" {
		t.Fatalf("idx_wiki_pages_repo_prefix columns = %v, want [repository_id slug]", cols)
	}
}

func TestMigrateWikiSlugColumnsBeforeAutoMigrate_RenamesLinkDstSlug(t *testing.T) {
	gdb := openTiDB(t)
	if err := gdb.Exec("CREATE TABLE wiki_page_links (repository_id BIGINT UNSIGNED, src_page_id BIGINT UNSIGNED, dst_slug_ci VARBINARY(384), dst_page_id BIGINT UNSIGNED, primary key (src_page_id, dst_slug_ci))").Error; err != nil {
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
