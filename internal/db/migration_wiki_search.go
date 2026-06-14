package db

import (
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"
)

type wikiSearchFullTextIndex struct {
	table  string
	name   string
	column string
}

type wikiSearchVectorIndexSpec struct {
	table string
	name  string
}

var wikiSearchFullTextIndexes = []wikiSearchFullTextIndex{
	{table: "wiki_search_documents", name: "idx_wiki_search_fts_title", column: "title"},
	{table: "wiki_search_documents", name: "idx_wiki_search_fts_body", column: "body"},
}

var wikiSearchVectorIndex = wikiSearchVectorIndexSpec{
	table: "wiki_search_documents",
	name:  "idx_wiki_search_embedding_cosine",
}

// MigrateWikiSearch provisions TiDB-native search structures for wiki search.
//
// This is intentionally best-effort, matching MigrateIssueSearch: non-TiDB
// backends continue to use fallback search paths, and TiDB variants only run
// FTS/vector DDL after the matching capability probe succeeds.
func MigrateWikiSearch(database *gorm.DB) error {
	if !IsTiDB(database) {
		ensureWikiSearchEmbeddingTextColumn(database)
		return nil
	}
	if !SupportsVectorDistance(database) {
		ensureWikiSearchEmbeddingTextColumn(database)
	}
	if !SupportsTiDBFullText(database) {
		return nil
	}
	for _, idx := range wikiSearchFullTextIndexes {
		ensureWikiFullTextIndex(database, idx)
	}
	return nil
}

func ensureWikiSearchEmbeddingTextColumn(database *gorm.DB) {
	if database == nil {
		return
	}
	migrator := database.Migrator()
	if !migrator.HasTable("wiki_search_documents") || migrator.HasColumn("wiki_search_documents", "embedding") {
		return
	}

	sql := wikiSearchAddTextEmbeddingDDL(database)
	if err := database.Exec(sql).Error; err != nil {
		if migrator.HasColumn("wiki_search_documents", "embedding") || isAlreadyExistsErr(err) {
			return
		}
		slog.Warn("db: MigrateWikiSearch: add embedding column", "table", "wiki_search_documents", "err", err)
		return
	}
	slog.Debug("db: MigrateWikiSearch: added embedding column", "table", "wiki_search_documents")
}

func ensureWikiFullTextIndex(database *gorm.DB, idx wikiSearchFullTextIndex) {
	if database == nil {
		return
	}
	migrator := database.Migrator()
	if migrator.HasIndex(idx.table, idx.name) {
		return
	}

	var lastErr error
	for _, sql := range fullTextIndexDDLs(idx.table, idx.name, idx.column) {
		if err := quietCapabilityDB(database).Exec(sql).Error; err != nil {
			if migrator.HasIndex(idx.table, idx.name) || isAlreadyExistsErr(err) {
				return
			}
			lastErr = err
			continue
		}
		if migrator.HasIndex(idx.table, idx.name) {
			slog.Debug("db: MigrateWikiSearch: added fulltext index", "table", idx.table, "index", idx.name, "column", idx.column)
			return
		}
	}
	if lastErr != nil {
		slog.Debug("db: MigrateWikiSearch: fulltext index unavailable", "table", idx.table, "index", idx.name, "column", idx.column, "err", lastErr)
	} else {
		slog.Debug("db: MigrateWikiSearch: fulltext index unavailable", "table", idx.table, "index", idx.name, "column", idx.column)
	}
}

func ensureWikiSearchVector(database *gorm.DB, dims int) {
	if dims <= 0 || !SupportsVectorDistance(database) {
		return
	}
	migrator := database.Migrator()
	if !migrator.HasTable("wiki_search_documents") {
		return
	}

	if !migrator.HasColumn("wiki_search_documents", "embedding") {
		if addWikiSearchVectorColumn(database, dims) {
			ensureWikiSearchVectorIndex(database)
		}
		return
	}

	if wikiSearchEmbeddingColumnIsVector(database) {
		slog.Debug("db: InitVector: embedding column already exists", "table", "wiki_search_documents")
		ensureWikiSearchVectorIndex(database)
		return
	}

	if !recreateWikiSearchVectorColumn(database, dims) {
		return
	}
	ensureWikiSearchVectorIndex(database)
}

func recreateWikiSearchVectorColumn(database *gorm.DB, dims int) bool {
	if err := database.Exec("ALTER TABLE `wiki_search_documents` DROP COLUMN `embedding`").Error; err != nil {
		if !database.Migrator().HasColumn("wiki_search_documents", "embedding") {
			return addWikiSearchVectorColumn(database, dims)
		}
		slog.Warn("db: InitVector: drop legacy wiki_search_documents.embedding", "error", err)
		return false
	}
	return addWikiSearchVectorColumn(database, dims)
}

func addWikiSearchVectorColumn(database *gorm.DB, dims int) bool {
	sql := fmt.Sprintf("ALTER TABLE `wiki_search_documents` ADD COLUMN `embedding` VECTOR(%d)", dims)
	if err := database.Exec(sql).Error; err != nil {
		if wikiSearchEmbeddingColumnIsVector(database) {
			slog.Debug("db: InitVector: embedding column already exists", "table", "wiki_search_documents")
			return true
		}
		if isAlreadyExistsErr(err) {
			slog.Warn("db: InitVector: wiki_search_documents.embedding exists but is not VECTOR", "error", err)
			return false
		}
		slog.Warn("db: InitVector: wiki_search_documents", "error", err)
		return false
	}
	slog.Debug("db: InitVector: added embedding column", "table", "wiki_search_documents", "dims", dims)
	return true
}

func wikiSearchEmbeddingColumnIsVector(database *gorm.DB) bool {
	if database == nil {
		return false
	}
	cols, err := database.Migrator().ColumnTypes("wiki_search_documents")
	if err != nil {
		return false
	}
	for _, col := range cols {
		if !strings.EqualFold(col.Name(), "embedding") {
			continue
		}
		return strings.Contains(strings.ToLower(col.DatabaseTypeName()), "vector")
	}
	return false
}

func ensureWikiSearchVectorIndex(database *gorm.DB) {
	if !SupportsVectorDistance(database) {
		return
	}
	migrator := database.Migrator()
	if !migrator.HasColumn(wikiSearchVectorIndex.table, "embedding") || migrator.HasIndex(wikiSearchVectorIndex.table, wikiSearchVectorIndex.name) {
		return
	}
	sql := wikiVectorIndexDDL(wikiSearchVectorIndex)
	if err := quietCapabilityDB(database).Exec(sql).Error; err != nil {
		if migrator.HasIndex(wikiSearchVectorIndex.table, wikiSearchVectorIndex.name) || isAlreadyExistsErr(err) {
			return
		}
		if isUnsupportedVectorIndexErr(err) {
			slog.Debug("db: InitVector: skip unavailable vector index", "table", wikiSearchVectorIndex.table, "index", wikiSearchVectorIndex.name, "err", err)
			return
		}
		slog.Warn("db: InitVector: add vector index", "table", wikiSearchVectorIndex.table, "index", wikiSearchVectorIndex.name, "err", err)
		return
	}
	slog.Debug("db: InitVector: added vector index", "table", wikiSearchVectorIndex.table, "index", wikiSearchVectorIndex.name)
}

func wikiFullTextIndexDDL(idx wikiSearchFullTextIndex) string {
	return fullTextIndexDDLFor(idx.table, idx.name, idx.column)
}

func wikiSearchAddTextEmbeddingDDL(database *gorm.DB) string {
	return "ALTER TABLE `wiki_search_documents` ADD COLUMN `embedding` TEXT"
}

func wikiVectorIndexDDL(idx wikiSearchVectorIndexSpec) string {
	return fmt.Sprintf(
		"ALTER TABLE `%s` ADD VECTOR INDEX `%s` ((VEC_COSINE_DISTANCE(`embedding`))) USING HNSW",
		idx.table,
		idx.name,
	)
}
