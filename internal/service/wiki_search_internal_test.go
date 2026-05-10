package service

import (
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestWikiSearchLikeEscapeClauseByDialect(t *testing.T) {
	tests := []struct {
		name string
		db   *gorm.DB
		want string
	}{
		{
			name: "mysql escapes backslash string literal",
			db:   &gorm.DB{Config: &gorm.Config{Dialector: mysql.Open("user:pass@tcp(host:3306)/db")}},
			want: ` ESCAPE '\\'`,
		},
		{
			name: "sqlite keeps single backslash escape character",
			db:   &gorm.DB{Config: &gorm.Config{Dialector: sqlite.Open(":memory:")}},
			want: ` ESCAPE '\'`,
		},
		{
			name: "nil database uses portable fallback",
			db:   nil,
			want: ` ESCAPE '\'`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wikiSearchLikeEscapeClause(tt.db); got != tt.want {
				t.Fatalf("wikiSearchLikeEscapeClause() = %q, want %q", got, tt.want)
			}
		})
	}
}
