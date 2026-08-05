package db

import (
	"fmt"

	"gorm.io/gorm"
)

const (
	legacyWikiChangesetSourceMigration = "migration"
	wikiChangesetSourceGit             = "git"
)

// MigrateWikiChangesetSourceGit renames historical git-originated wiki
// changesets from the legacy "migration" source value to "git".
func MigrateWikiChangesetSourceGit(database *gorm.DB) error {
	if database == nil {
		return nil
	}
	migrator := database.Migrator()
	if !migrator.HasTable("wiki_changesets") || !migrator.HasColumn("wiki_changesets", "source") {
		return nil
	}
	if err := database.Table("wiki_changesets").
		Where("source = ?", legacyWikiChangesetSourceMigration).
		Update("source", wikiChangesetSourceGit).Error; err != nil {
		return fmt.Errorf("migrate wiki_changesets.source from migration to git: %w", err)
	}
	return nil
}
