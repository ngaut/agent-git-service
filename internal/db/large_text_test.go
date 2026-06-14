package db

import (
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
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
		{name: "postgres uses text", db: &gorm.DB{Config: &gorm.Config{Dialector: postgres.Open("postgres://user:pass@localhost:5432/testdb?sslmode=disable")}}, wantType: "text"},
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
