package db

import (
	"strings"
	"testing"
)

func TestMigrateIssueSearch_TiDBTables(t *testing.T) {
	gdb := openTiDB(t)
	if err := gdb.Exec("CREATE TABLE issues (id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT, title TEXT, body TEXT)").Error; err != nil {
		t.Fatalf("create issues: %v", err)
	}
	if err := gdb.Exec("CREATE TABLE pull_requests (id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT, title TEXT, body TEXT, commit_messages TEXT, filenames TEXT)").Error; err != nil {
		t.Fatalf("create pull_requests: %v", err)
	}

	if err := MigrateIssueSearch(gdb); err != nil {
		t.Fatalf("MigrateIssueSearch: %v", err)
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
	if strings.Contains(fullText, "ADD_COLUMNAR_REPLICA_ON_DEMAND") {
		t.Fatalf("expected playground-compatible full-text DDL, got %q", fullText)
	}
	fullTextDDLs := fullTextIndexDDLs("issues", "idx_issues_fts_title", "title")
	if len(fullTextDDLs) != 2 {
		t.Fatalf("expected columnar and base full-text DDLs, got %#v", fullTextDDLs)
	}
	if !strings.Contains(fullTextDDLs[0], "ADD_COLUMNAR_REPLICA_ON_DEMAND") {
		t.Fatalf("expected columnar replica full-text DDL first, got %#v", fullTextDDLs)
	}
	if fullTextDDLs[1] != fullText {
		t.Fatalf("expected base full-text DDL fallback, got %#v", fullTextDDLs)
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

	gdb := openTiDB(t)
	if err := gdb.Exec("CREATE TABLE issues (id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT, title VARCHAR(255))").Error; err != nil {
		t.Fatalf("create issues: %v", err)
	}
	if err := gdb.Exec("CREATE TABLE pull_requests (id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT, title VARCHAR(255))").Error; err != nil {
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
