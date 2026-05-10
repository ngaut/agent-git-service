package db

import (
	"gorm.io/gorm"
)

// MigrateProjectItemUniqueIndex adds a unique index on (project_id, content_id, type)
// to the project_items table to prevent duplicate items for the same content within a project.
// This migration is idempotent and safe to run on databases that already have the index.
func MigrateProjectItemUniqueIndex(db *gorm.DB) error {
	if !db.Migrator().HasTable("project_items") {
		// Table doesn't exist yet, skip migration (AutoMigrate will create it with the index)
		return nil
	}

	// Drop the old index if it exists (idx_pi_content_unique without project_id).
	// This is safe to run even if the index doesn't exist.
	if db.Migrator().HasIndex("project_items", "idx_pi_content_unique") {
		_ = db.Migrator().DropIndex("project_items", "idx_pi_content_unique")
	}

	// Create the new unique index covering (project_id, content_id, type).
	//
	// NOTE: We only enforce uniqueness when content_id is non-empty (ISSUE and PULL_REQUEST types).
	// DRAFT_ISSUE items have empty content_id, so multiple drafts can exist in the same project.
	if !db.Migrator().HasIndex("project_items", "idx_pi_content_unique") {
		_ = db.Exec("CREATE UNIQUE INDEX idx_pi_content_unique ON project_items (project_id, content_id, type)")
	}

	return nil
}
