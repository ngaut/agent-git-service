package service

import (
	"fmt"
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

func TestFuseWikiSearchResultsIncludesCrossWindowWinner(t *testing.T) {
	lexical := make([]WikiSearchResult, 0, 21)
	semantic := make([]WikiSearchResult, 0, 21)
	for i := 1; i <= 20; i++ {
		lexical = append(lexical, WikiSearchResult{
			Slug:  fmt.Sprintf("lexical-only-%02d", i),
			Score: 1,
		})
		semantic = append(semantic, WikiSearchResult{
			Slug:  fmt.Sprintf("semantic-only-%02d", i),
			Score: 1,
		})
	}
	lexical = append(lexical, WikiSearchResult{Slug: "joint-21", Score: 1})
	semantic = append(semantic, WikiSearchResult{Slug: "joint-21", Score: 1})

	results := fuseWikiSearchResults(lexical, semantic, 1, 0)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Slug != "joint-21" {
		t.Fatalf("top fused slug = %q, want joint-21", results[0].Slug)
	}
}
