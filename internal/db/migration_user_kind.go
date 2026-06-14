package db

import "gorm.io/gorm"

// MigrateUserKind makes the user_kind column non-nullable on production SQL dialects.
func MigrateUserKind(database *gorm.DB) error {
	if !database.Migrator().HasTable("users") {
		return nil
	}
	if !database.Migrator().HasColumn("users", "user_kind") {
		return nil
	}
	return enforceUserKindNotNull(database)
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
