package service

import "gorm.io/gorm"

func sqlLikeEscapeClause(database *gorm.DB) string {
	if database != nil && database.Dialector != nil && database.Dialector.Name() == "mysql" {
		return ` ESCAPE '\\'`
	}
	return ` ESCAPE '\'`
}
