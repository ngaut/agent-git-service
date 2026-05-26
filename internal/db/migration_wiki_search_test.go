package db

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateWikiSearch_NonTiDBEnsuresEmbeddingTextColumn(t *testing.T) {
	gdb := openSQLiteDB(t, filepath.Join(t.TempDir(), "wiki-search.db"))
	if err := gdb.Exec("CREATE TABLE wiki_search_documents (id integer primary key, title text, body text)").Error; err != nil {
		t.Fatalf("create wiki_search_documents: %v", err)
	}

	sink := captureLogs(t)
	if err := MigrateWikiSearch(gdb); err != nil {
		t.Fatalf("MigrateWikiSearch: %v", err)
	}
	if !gdb.Migrator().HasColumn("wiki_search_documents", "embedding") {
		t.Fatal("expected MigrateWikiSearch to add wiki_search_documents.embedding")
	}
	entries := sink.Entries()
	for _, entry := range entries {
		if entry.level == slog.LevelWarn {
			t.Fatalf("expected no warnings for non-TiDB embedding column migration, got %#v", entries)
		}
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
	if !strings.Contains(fullText, "ADD_COLUMNAR_REPLICA_ON_DEMAND") {
		t.Fatalf("expected on-demand columnar replica in wiki full-text DDL, got %q", fullText)
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

func TestInitVector_WikiSearchEmbeddingTextColumnIsLeftOnSQLite(t *testing.T) {
	gdb := openSQLiteDB(t, filepath.Join(t.TempDir(), "wiki-search-vector.db"))
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
	for _, entry := range entries {
		if entry.level == slog.LevelWarn && entry.attrs["table"] == "wiki_search_documents" {
			t.Fatalf("expected no wiki vector warning on SQLite, got %#v", entries)
		}
	}
}
