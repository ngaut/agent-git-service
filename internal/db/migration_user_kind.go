package db

import "gorm.io/gorm"

// MigrateUserKind backfills empty user_kind values and makes the column
// non-nullable on production SQL dialects.
func MigrateUserKind(database *gorm.DB) error {
	if err := migrateLegacyAnonymousUsers(database); err != nil {
		return err
	}
	if err := backfillEmptyUserKind(database); err != nil {
		return err
	}
	if !database.Migrator().HasTable("users") {
		return nil
	}
	if !database.Migrator().HasColumn("users", "user_kind") {
		return nil
	}
	return enforceUserKindNotNull(database)
}

func backfillEmptyUserKind(database *gorm.DB) error {
	if !database.Migrator().HasTable("users") {
		return nil
	}
	if !database.Migrator().HasColumn("users", "user_kind") {
		return nil
	}

	return database.Model(&User{}).
		Where("user_kind = '' OR user_kind IS NULL").
		Update("user_kind", UserKindHuman).Error
}

func migrateLegacyAnonymousUsers(database *gorm.DB) error {
	if !database.Migrator().HasTable("users") {
		return nil
	}
	if !database.Migrator().HasColumn("users", "is_anonymous") {
		return nil
	}

	return database.Table("users").
		Where("is_anonymous = ?", true).
		Updates(map[string]any{
			"user_kind":    UserKindAgent,
			"is_anonymous": false,
		}).Error
}

func enforceUserKindNotNull(database *gorm.DB) error {
	needsAlter, err := userKindNeedsNotNullAlter(database)
	if err != nil {
		return err
	}
	if !needsAlter {
		return nil
	}
	switch database.Dialector.Name() {
	case "mysql":
		return database.Exec("ALTER TABLE `users` MODIFY COLUMN `user_kind` varchar(16) NOT NULL DEFAULT 'human'").Error
	case "postgres":
		if err := database.Exec(`ALTER TABLE "users" ALTER COLUMN "user_kind" SET DEFAULT 'human'`).Error; err != nil {
			return err
		}
		return database.Exec(`ALTER TABLE "users" ALTER COLUMN "user_kind" SET NOT NULL`).Error
	case "sqlite":
		return nil
	default:
		return database.Migrator().AlterColumn(&User{}, "UserKind")
	}
}

func userKindNeedsNotNullAlter(database *gorm.DB) (bool, error) {
	cols, err := database.Migrator().ColumnTypes("users")
	if err != nil {
		return false, err
	}
	for _, col := range cols {
		if col.Name() != "user_kind" {
			continue
		}
		nullable, ok := col.Nullable()
		if !ok {
			return true, nil
		}
		return nullable, nil
	}
	return false, nil
}
