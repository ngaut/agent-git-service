package db

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateIssueSearch_NonMySQLNoOp(t *testing.T) {
	gdb := openSQLiteDB(t, filepath.Join(t.TempDir(), "issue-search.db"))
	if err := gdb.Exec("CREATE TABLE issues (id integer primary key, title text, body text)").Error; err != nil {
		t.Fatalf("create issues: %v", err)
	}
	if err := gdb.Exec("CREATE TABLE pull_requests (id integer primary key, title text, body text, commit_messages text, filenames text)").Error; err != nil {
		t.Fatalf("create pull_requests: %v", err)
	}

	sink := captureLogs(t)
	if err := MigrateIssueSearch(gdb); err != nil {
		t.Fatalf("MigrateIssueSearch: %v", err)
	}
	entries := sink.Entries()
	if len(entries) != 0 {
		t.Fatalf("expected no logs for non-MySQL backend, got %#v", entries)
	}
}

func TestInitVector_WithExistingColumnsStaysIdempotentOnSQLite(t *testing.T) {
	gdb := openSQLiteDB(t, filepath.Join(t.TempDir(), "issue-search-vector.db"))
	createVectorTables(t, gdb)

	if err := gdb.Exec("ALTER TABLE issues ADD COLUMN embedding TEXT").Error; err != nil {
		t.Fatalf("add issues.embedding: %v", err)
	}
	if err := gdb.Exec("ALTER TABLE pull_requests ADD COLUMN embedding TEXT").Error; err != nil {
		t.Fatalf("add pull_requests.embedding: %v", err)
	}

	sink := captureLogs(t)
	InitVector(gdb, 3)
	entries := sink.Entries()

	assertLogEntry(t, entries, slog.LevelInfo, "db: InitVector: embedding column already exists", "table", "issues")
	assertLogEntry(t, entries, slog.LevelInfo, "db: InitVector: embedding column already exists", "table", "pull_requests")
	for _, entry := range entries {
		if entry.level == slog.LevelWarn {
			t.Fatalf("expected no warn logs, got %#v", entries)
		}
	}
}

func TestIssueSearchDDLBuilders(t *testing.T) {
	fullText := fullTextIndexDDL(issueSearchFullTextIndex{
		table:  "issues",
		name:   "idx_issues_fts_title",
		column: "title",
	})
	if !strings.Contains(fullText, "ADD FULLTEXT INDEX") {
		t.Fatalf("expected full-text index DDL, got %q", fullText)
	}
	if !strings.Contains(fullText, "WITH PARSER MULTILINGUAL") {
		t.Fatalf("expected multilingual parser in full-text DDL, got %q", fullText)
	}
	if !strings.Contains(fullText, "ADD_COLUMNAR_REPLICA_ON_DEMAND") {
		t.Fatalf("expected on-demand columnar replica in full-text DDL, got %q", fullText)
	}

	dropIndex := dropIndexDDL(issueSearchFullTextIndex{
		table: "issues",
		name:  "idx_issues_fts_search_document",
	})
	if !strings.Contains(dropIndex, "DROP INDEX") {
		t.Fatalf("expected drop index DDL, got %q", dropIndex)
	}

	dropColumn := dropColumnDDL(issueSearchColumn{
		table:  "issues",
		column: "search_document",
	})
	if !strings.Contains(dropColumn, "DROP COLUMN") {
		t.Fatalf("expected drop column DDL, got %q", dropColumn)
	}

	vector := vectorIndexDDL(issueSearchVectorIndex{
		table: "issues",
		name:  "idx_issues_embedding_cosine",
	})
	if !strings.Contains(vector, "ADD VECTOR INDEX") {
		t.Fatalf("expected vector index DDL, got %q", vector)
	}
	if !strings.Contains(vector, "VEC_COSINE_DISTANCE(`embedding`)") {
		t.Fatalf("expected cosine-distance vector index DDL, got %q", vector)
	}
	if !strings.Contains(vector, "USING HNSW") {
		t.Fatalf("expected HNSW vector index DDL, got %q", vector)
	}
}

func TestIssueSearchFullTextIndexesReady(t *testing.T) {
	indexes := issueSearchFullTextIndexes
	t.Cleanup(func() {
		issueSearchFullTextIndexes = indexes
	})

	if issueSearchFullTextIndexesReady(nil) {
		t.Fatal("expected nil database to report indexes not ready")
	}

	issueSearchFullTextIndexes = []issueSearchFullTextIndex{
		{table: "issues", name: "idx_issues_fts_title", column: "title"},
		{table: "pull_requests", name: "idx_pull_requests_fts_title", column: "title"},
	}

	gdb := openSQLiteDB(t, filepath.Join(t.TempDir(), "issue-search-ready.db"))
	if err := gdb.Exec("CREATE TABLE issues (id integer primary key, title text)").Error; err != nil {
		t.Fatalf("create issues: %v", err)
	}
	if err := gdb.Exec("CREATE TABLE pull_requests (id integer primary key, title text)").Error; err != nil {
		t.Fatalf("create pull_requests: %v", err)
	}
	if issueSearchFullTextIndexesReady(gdb) {
		t.Fatal("expected indexes to be reported missing before creation")
	}
	if err := gdb.Exec("CREATE INDEX idx_issues_fts_title ON issues(title)").Error; err != nil {
		t.Fatalf("create issues title index: %v", err)
	}
	if err := gdb.Exec("CREATE INDEX idx_pull_requests_fts_title ON pull_requests(title)").Error; err != nil {
		t.Fatalf("create pull_requests title index: %v", err)
	}
	if !issueSearchFullTextIndexesReady(gdb) {
		t.Fatal("expected replacement fulltext indexes to be reported ready after creation")
	}
}
