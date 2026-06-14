package db

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"
)

type wikiObsoleteIndex struct {
	table string
	name  string
}

type wikiObsoleteColumn struct {
	table  string
	column string
}

var obsoleteWikiSlugIndexes = []wikiObsoleteIndex{
	{table: "wiki_pages", name: "idx_wiki_pages_repo_slug_ci"},
	{table: "wiki_search_documents", name: "idx_wiki_search_repo_slug_ci"},
	{table: "wiki_page_links", name: "idx_wiki_links_dst_string"},
}

var obsoleteWikiSlugColumns = []wikiObsoleteColumn{
	{table: "wiki_pages", column: "slug_ci_v1"},
	{table: "wiki_search_documents", column: "slug_ci_v1"},
	{table: "wiki_page_links", column: "dst_slug_ci"},
}

// MigrateWikiSlugColumnsBeforeAutoMigrate renames legacy link target columns
// before AutoMigrate can create the replacement column separately.
func MigrateWikiSlugColumnsBeforeAutoMigrate(database *gorm.DB) error {
	if database == nil {
		return nil
	}
	migrator := database.Migrator()
	if !migrator.HasTable("wiki_page_links") ||
		!migrator.HasColumn("wiki_page_links", "dst_slug_ci") ||
		migrator.HasColumn("wiki_page_links", "dst_slug") {
		return nil
	}
	if err := database.Exec(renameColumnDDL(database, "wiki_page_links", "dst_slug_ci", "dst_slug")).Error; err != nil {
		if !migrator.HasColumn("wiki_page_links", "dst_slug_ci") || migrator.HasColumn("wiki_page_links", "dst_slug") {
			return nil
		}
		return fmt.Errorf("rename wiki_page_links.dst_slug_ci: %w", err)
	}
	slog.Info("db: MigrateWikiSlugColumns: renamed wiki_page_links.dst_slug_ci", "column", "dst_slug")
	return nil
}

// MigrateWikiSlugColumns removes obsolete slug_ci indexes and columns after
// the single-slug models have been migrated.
func MigrateWikiSlugColumns(database *gorm.DB) error {
	if database == nil {
		return nil
	}
	for _, idx := range obsoleteWikiSlugIndexes {
		if err := dropObsoleteWikiIndex(database, idx); err != nil {
			return err
		}
	}
	for _, col := range obsoleteWikiSlugColumns {
		if err := dropObsoleteWikiColumn(database, col); err != nil {
			return err
		}
	}
	return nil
}

func dropObsoleteWikiIndex(database *gorm.DB, idx wikiObsoleteIndex) error {
	migrator := database.Migrator()
	if !migrator.HasTable(idx.table) || !migrator.HasIndex(idx.table, idx.name) {
		return nil
	}
	var err error
	if database.Dialector != nil && database.Dialector.Name() == "mysql" {
		err = database.Exec(dropWikiIndexDDL(idx)).Error
	} else {
		err = migrator.DropIndex(idx.table, idx.name)
	}
	if err != nil {
		if !migrator.HasIndex(idx.table, idx.name) {
			return nil
		}
		return fmt.Errorf("drop obsolete wiki index %s.%s: %w", idx.table, idx.name, err)
	}
	slog.Info("db: MigrateWikiSlugColumns: dropped obsolete index", "table", idx.table, "index", idx.name)
	return nil
}

func dropObsoleteWikiColumn(database *gorm.DB, col wikiObsoleteColumn) error {
	migrator := database.Migrator()
	if !migrator.HasTable(col.table) || !migrator.HasColumn(col.table, col.column) {
		return nil
	}
	var err error
	if database.Dialector != nil {
		switch database.Dialector.Name() {
		case "mysql", "sqlite", "postgres":
			err = database.Exec(dropWikiColumnDDL(database, col)).Error
		default:
			err = migrator.DropColumn(col.table, col.column)
		}
	} else {
		err = migrator.DropColumn(col.table, col.column)
	}
	if err != nil {
		if !migrator.HasColumn(col.table, col.column) {
			return nil
		}
		return fmt.Errorf("drop obsolete wiki column %s.%s: %w", col.table, col.column, err)
	}
	slog.Info("db: MigrateWikiSlugColumns: dropped obsolete column", "table", col.table, "column", col.column)
	return nil
}

func renameColumnDDL(database *gorm.DB, table, oldName, newName string) string {
	if database != nil && database.Dialector != nil && database.Dialector.Name() == "postgres" {
		return fmt.Sprintf(`ALTER TABLE "%s" RENAME COLUMN "%s" TO "%s"`, table, oldName, newName)
	}
	return fmt.Sprintf("ALTER TABLE `%s` RENAME COLUMN `%s` TO `%s`", table, oldName, newName)
}

func dropWikiIndexDDL(idx wikiObsoleteIndex) string {
	return fmt.Sprintf("ALTER TABLE `%s` DROP INDEX `%s`", idx.table, idx.name)
}

func dropWikiColumnDDL(database *gorm.DB, col wikiObsoleteColumn) string {
	if database != nil && database.Dialector != nil && database.Dialector.Name() == "postgres" {
		return fmt.Sprintf(`ALTER TABLE "%s" DROP COLUMN "%s"`, col.table, col.column)
	}
	return fmt.Sprintf("ALTER TABLE `%s` DROP COLUMN `%s`", col.table, col.column)
}
