package db

import (
	"fmt"

	"gorm.io/gorm"
)

const retiredWikiDirIndexTable = "wiki_dir_index"

// DropRetiredWikiDirIndex removes the materialized directory view retired in
// favor of prefix queries over wiki_pages.
func DropRetiredWikiDirIndex(database *gorm.DB) error {
	if database == nil {
		return nil
	}
	if err := database.Migrator().DropTable(retiredWikiDirIndexTable); err != nil {
		return fmt.Errorf("drop retired wiki directory index: %w", err)
	}
	return nil
}
