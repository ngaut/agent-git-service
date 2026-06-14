package db

import (
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestLargeTextDataTypeByDialect(t *testing.T) {
	tests := []struct {
		name     string
		db       *gorm.DB
		wantType string
	}{
		{name: "mysql uses mediumtext", db: &gorm.DB{Config: &gorm.Config{Dialector: mysql.Open("user:pass@tcp(host:3306)/db")}}, wantType: "mediumtext"},
		{name: "nil database uses text", db: nil, wantType: "text"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LargeText("").GormDBDataType(tt.db, &schema.Field{})
			if got != tt.wantType {
				t.Fatalf("GormDBDataType = %q, want %q", got, tt.wantType)
			}
		})
	}
}
