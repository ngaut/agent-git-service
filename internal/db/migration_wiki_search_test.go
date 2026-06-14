package db

import (
	"log/slog"
	"strings"
	"testing"
)

func TestMigrateWikiSearch_TiDBTables(t *testing.T) {
	gdb := openTiDB(t)
	if err := gdb.Exec("CREATE TABLE wiki_search_documents (id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT, title TEXT, body TEXT)").Error; err != nil {
		t.Fatalf("create wiki_search_documents: %v", err)
	}

	if err := MigrateWikiSearch(gdb); err != nil {
		t.Fatalf("MigrateWikiSearch: %v", err)
	}
	if gdb.Migrator().HasColumn("wiki_search_documents", "embedding") {
		t.Fatal("expected TiDB MigrateWikiSearch to leave embedding for InitVector")
	}
}

func TestWikiSearchDDLBuilders(t *testing.T) {
	fullText := wikiFullTextIndexDDL(wikiSearchFullTextIndexes[0])
	if !strings.Contains(fullText, "ALTER TABLE `wiki_search_documents` ADD FULLTEXT INDEX `idx_wiki_search_fts_title`") {
		t.Fatalf("expected wiki full-text index DDL, got %q", fullText)
	}
	if !strings.Contains(fullText, "WITH PARSER MULTILINGUAL") {
		t.Fatalf("expected multilingual parser in wiki full-text DDL, got %q", fullText)
	}
	if strings.Contains(fullText, "ADD_COLUMNAR_REPLICA_ON_DEMAND") {
		t.Fatalf("expected playground-compatible wiki full-text DDL, got %q", fullText)
	}

	textColumn := wikiSearchAddTextEmbeddingDDL(nil)
	if !strings.Contains(textColumn, "ALTER TABLE `wiki_search_documents` ADD COLUMN `embedding` TEXT") {
		t.Fatalf("expected wiki embedding text column DDL, got %q", textColumn)
	}

	vector := wikiVectorIndexDDL(wikiSearchVectorIndex)
	if !strings.Contains(vector, "ALTER TABLE `wiki_search_documents` ADD VECTOR INDEX `idx_wiki_search_embedding_cosine`") {
		t.Fatalf("expected wiki vector index DDL, got %q", vector)
	}
	if !strings.Contains(vector, "VEC_COSINE_DISTANCE(`embedding`)") {
		t.Fatalf("expected cosine-distance vector index DDL, got %q", vector)
	}
	if !strings.Contains(vector, "USING HNSW") {
		t.Fatalf("expected HNSW vector index DDL, got %q", vector)
	}
}

func TestInitVector_WikiSearchEmbeddingColumnUsesTiDBVector(t *testing.T) {
	gdb := openTiDB(t)
	if !SupportsVectorDistance(gdb) {
		t.Skip("TiDB playground does not support VEC_COSINE_DISTANCE")
	}
	if err := gdb.AutoMigrate(&WikiSearchDocument{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	if gdb.Migrator().HasColumn("wiki_search_documents", "embedding") {
		t.Fatal("expected AutoMigrate to leave wiki_search_documents.embedding to explicit migrations")
	}
	if err := MigrateWikiSearch(gdb); err != nil {
		t.Fatalf("MigrateWikiSearch: %v", err)
	}

	sink := captureLogs(t)
	InitVector(gdb, 3)
	entries := sink.Entries()

	if !gdb.Migrator().HasColumn("wiki_search_documents", "embedding") {
		t.Fatal("expected wiki_search_documents.embedding to remain present")
	}
	if !wikiSearchEmbeddingColumnIsVector(gdb) {
		t.Fatal("expected wiki_search_documents.embedding to use TiDB VECTOR")
	}
	for _, entry := range entries {
		if entry.level == slog.LevelWarn && entry.attrs["table"] == "wiki_search_documents" {
			t.Fatalf("expected no wiki vector warning on TiDB, got %#v", entries)
		}
	}
}

func TestMigrateWikiSlugColumns_DropsSearchSlugCIColumnAndIndex(t *testing.T) {
	gdb := openTiDB(t)
	if err := gdb.Exec("CREATE TABLE wiki_search_documents (id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT, repository_id BIGINT UNSIGNED, slug VARCHAR(255), slug_ci_v1 VARCHAR(255), title TEXT, body TEXT, revision_sha VARCHAR(64), created_at DATETIME, updated_at DATETIME)").Error; err != nil {
		t.Fatalf("create wiki_search_documents: %v", err)
	}
	if err := gdb.Exec("CREATE INDEX idx_wiki_search_repo_slug_ci ON wiki_search_documents (repository_id, slug_ci_v1)").Error; err != nil {
		t.Fatalf("create slug_ci index: %v", err)
	}

	if err := MigrateWikiSlugColumns(gdb); err != nil {
		t.Fatalf("MigrateWikiSlugColumns: %v", err)
	}
	if gdb.Migrator().HasIndex("wiki_search_documents", "idx_wiki_search_repo_slug_ci") {
		t.Fatal("expected idx_wiki_search_repo_slug_ci to be dropped")
	}
	if gdb.Migrator().HasColumn("wiki_search_documents", "slug_ci_v1") {
		t.Fatal("expected wiki_search_documents.slug_ci_v1 to be dropped")
	}
}
