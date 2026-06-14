package db

import (
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"
)

type issueSearchFullTextIndex struct {
	table  string
	name   string
	column string
}

type issueSearchColumn struct {
	table  string
	column string
}

type issueSearchVectorIndex struct {
	table string
	name  string
}

var obsoleteIssueSearchFullTextIndexes = []issueSearchFullTextIndex{
	{table: "issues", name: "idx_issues_fts_search_document", column: "search_document"},
	{table: "pull_requests", name: "idx_pull_requests_fts_search_text", column: "search_text"},
	{table: "pull_requests", name: "idx_pull_requests_fts_search_document", column: "search_document"},
}

var obsoleteIssueSearchColumns = []issueSearchColumn{
	{table: "issues", column: "search_document"},
	{table: "pull_requests", column: "search_text"},
	{table: "pull_requests", column: "search_document"},
}

var issueSearchFullTextIndexes = []issueSearchFullTextIndex{
	{table: "issues", name: "idx_issues_fts_title", column: "title"},
	{table: "issues", name: "idx_issues_fts_body", column: "body"},
	{table: "pull_requests", name: "idx_pull_requests_fts_title", column: "title"},
	{table: "pull_requests", name: "idx_pull_requests_fts_body", column: "body"},
}

var issueSearchVectorIndexes = []issueSearchVectorIndex{
	{table: "issues", name: "idx_issues_embedding_cosine"},
	{table: "pull_requests", name: "idx_pull_requests_embedding_cosine"},
}

// MigrateIssueSearch provisions TiDB-native search structures for issue/PR search.
//
// This is intentionally best-effort: on non-TiDB backends, the function
// returns nil so normal startup can continue with fallback search paths.
// On TiDB variants without the required full-text/vector capabilities, each
// individual DDL attempt logs a warning and startup continues.
func MigrateIssueSearch(database *gorm.DB) error {
	if !SupportsTiDBSearch(database) {
		return nil
	}

	for _, idx := range issueSearchFullTextIndexes {
		ensureFullTextIndex(database, idx)
	}
	if !issueSearchFullTextIndexesReady(database) {
		slog.Debug("db: MigrateIssueSearch: skip obsolete search cleanup until replacement fulltext indexes are ready")
		ensureVectorIndexes(database)
		return nil
	}
	for _, idx := range obsoleteIssueSearchFullTextIndexes {
		dropObsoleteFullTextIndex(database, idx)
	}
	for _, col := range obsoleteIssueSearchColumns {
		dropObsoleteSearchColumn(database, col)
	}
	ensureVectorIndexes(database)
	return nil
}

func issueSearchFullTextIndexesReady(database *gorm.DB) bool {
	if database == nil {
		return false
	}
	migrator := database.Migrator()
	for _, idx := range issueSearchFullTextIndexes {
		if !migrator.HasIndex(idx.table, idx.name) {
			return false
		}
	}
	return true
}

func dropObsoleteFullTextIndex(database *gorm.DB, idx issueSearchFullTextIndex) {
	if database == nil {
		return
	}
	migrator := database.Migrator()
	if !migrator.HasIndex(idx.table, idx.name) {
		return
	}

	sql := dropIndexDDL(idx)
	if err := database.Exec(sql).Error; err != nil {
		if !migrator.HasIndex(idx.table, idx.name) {
			return
		}
		slog.Warn("db: MigrateIssueSearch: drop obsolete fulltext index", "table", idx.table, "index", idx.name, "err", err)
		return
	}
	slog.Debug("db: MigrateIssueSearch: dropped obsolete fulltext index", "table", idx.table, "index", idx.name)
}

func dropObsoleteSearchColumn(database *gorm.DB, col issueSearchColumn) {
	if database == nil {
		return
	}
	migrator := database.Migrator()
	if !migrator.HasColumn(col.table, col.column) {
		return
	}

	sql := dropColumnDDL(col)
	if err := database.Exec(sql).Error; err != nil {
		if !migrator.HasColumn(col.table, col.column) {
			return
		}
		slog.Warn("db: MigrateIssueSearch: drop obsolete search column", "table", col.table, "column", col.column, "err", err)
		return
	}
	slog.Debug("db: MigrateIssueSearch: dropped obsolete search column", "table", col.table, "column", col.column)
}

func ensureFullTextIndex(database *gorm.DB, idx issueSearchFullTextIndex) {
	if database == nil {
		return
	}
	migrator := database.Migrator()
	if migrator.HasIndex(idx.table, idx.name) {
		return
	}

	sql := fullTextIndexDDL(idx)
	if err := database.Exec(sql).Error; err != nil {
		if migrator.HasIndex(idx.table, idx.name) || isAlreadyExistsErr(err) {
			return
		}
		slog.Warn("db: MigrateIssueSearch: add fulltext index", "table", idx.table, "index", idx.name, "column", idx.column, "err", err)
		return
	}
	slog.Debug("db: MigrateIssueSearch: added fulltext index", "table", idx.table, "index", idx.name, "column", idx.column)
}

func ensureVectorIndexes(database *gorm.DB) {
	if !SupportsTiDBSearch(database) {
		return
	}
	migrator := database.Migrator()
	for _, idx := range issueSearchVectorIndexes {
		if !migrator.HasColumn(idx.table, "embedding") {
			continue
		}
		if migrator.HasIndex(idx.table, idx.name) {
			continue
		}

		sql := vectorIndexDDL(idx)
		if err := quietCapabilityDB(database).Exec(sql).Error; err != nil {
			if migrator.HasIndex(idx.table, idx.name) || isAlreadyExistsErr(err) {
				continue
			}
			if isUnsupportedVectorIndexErr(err) {
				slog.Debug("db: InitVector: skip unavailable vector index", "table", idx.table, "index", idx.name, "err", err)
				continue
			}
			slog.Warn("db: InitVector: add vector index", "table", idx.table, "index", idx.name, "err", err)
			continue
		}
		slog.Debug("db: InitVector: added vector index", "table", idx.table, "index", idx.name)
	}
}

func fullTextIndexDDL(idx issueSearchFullTextIndex) string {
	return fmt.Sprintf(
		"ALTER TABLE `%s` ADD FULLTEXT INDEX `%s` (`%s`) WITH PARSER MULTILINGUAL",
		idx.table,
		idx.name,
		idx.column,
	)
}

func dropIndexDDL(idx issueSearchFullTextIndex) string {
	return fmt.Sprintf("ALTER TABLE `%s` DROP INDEX `%s`", idx.table, idx.name)
}

func dropColumnDDL(col issueSearchColumn) string {
	return fmt.Sprintf("ALTER TABLE `%s` DROP COLUMN `%s`", col.table, col.column)
}

func vectorIndexDDL(idx issueSearchVectorIndex) string {
	return fmt.Sprintf(
		"ALTER TABLE `%s` ADD VECTOR INDEX `%s` ((VEC_COSINE_DISTANCE(`embedding`))) USING HNSW",
		idx.table,
		idx.name,
	)
}

func isAlreadyExistsErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "exists") ||
		strings.Contains(msg, "duplicate key name")
}

func isUnsupportedVectorIndexErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unsupported add vector index") ||
		strings.Contains(msg, "unsupported empty tiflash replica")
}
