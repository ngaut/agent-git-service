package db

import "gorm.io/gorm"

// MigrateUserKind backfills user_kind and clears legacy anonymous flags.
func MigrateUserKind(database *gorm.DB) error {
	if !database.Migrator().HasTable("users") {
		return nil
	}
	if !database.Migrator().HasColumn("users", "user_kind") {
		return nil
	}

	// First, promote anonymous users to agent accounts and clear the flag.
	if database.Migrator().HasColumn("users", "is_anonymous") {
		if err := database.Model(&User{}).
			Where("is_anonymous = ?", true).
			Updates(map[string]any{
				"user_kind":    UserKindAgent,
				"is_anonymous": false,
			}).Error; err != nil {
			return err
		}
	}

	// Ensure any remaining empty user_kind values default to human.
	return database.Model(&User{}).
		Where("user_kind = '' OR user_kind IS NULL").
		Update("user_kind", UserKindHuman).Error
}
