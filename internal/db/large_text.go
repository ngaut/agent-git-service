package db

import (
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// LargeText keeps issue/PR-style body fields dialect-aware:
// MySQL/TiDB gets MEDIUMTEXT, other backends use TEXT.
type LargeText string

func (LargeText) GormDataType() string {
	return "text"
}

func (LargeText) GormDBDataType(database *gorm.DB, _ *schema.Field) string {
	if database != nil && database.Dialector != nil && database.Dialector.Name() == "mysql" {
		return "mediumtext"
	}
	return "text"
}
