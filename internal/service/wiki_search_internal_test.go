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

	results := paginateWikiSearchResultList(fuseWikiSearchResults(lexical, semantic), 1, 0)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Slug != "joint-21" {
		t.Fatalf("top fused slug = %q, want joint-21", results[0].Slug)
	}
}

func TestWikiSearchRankWindow(t *testing.T) {
	tests := []struct {
		name   string
		limit  int
		offset int
		want   int
	}{
		{name: "default page keeps buffered window", limit: 20, offset: 0, want: wikiSearchMinRankWindow},
		{name: "small limit still buffered", limit: 1, offset: 0, want: wikiSearchMinRankWindow},
		{name: "offset expands window", limit: 50, offset: 125, want: 175},
		{name: "deep offset expands lexical window", limit: 50, offset: 1000, want: 1050},
		{name: "invalid limit uses default", limit: -1, offset: 0, want: wikiSearchMinRankWindow},
		{name: "invalid offset normalizes", limit: 50, offset: -10, want: wikiSearchMinRankWindow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wikiSearchRankWindow(tt.limit, tt.offset); got != tt.want {
				t.Fatalf("wikiSearchRankWindow(%d, %d) = %d, want %d", tt.limit, tt.offset, got, tt.want)
			}
		})
	}
}

func TestWikiSearchSemanticRankLimit(t *testing.T) {
	if got := wikiSearchSemanticRankLimit(1050); got != wikiSearchMaxRankWindow {
		t.Fatalf("wikiSearchSemanticRankLimit(1050) = %d, want %d", got, wikiSearchMaxRankWindow)
	}
	if got := wikiSearchSemanticRankLimit(80); got != 80 {
		t.Fatalf("wikiSearchSemanticRankLimit(80) = %d, want 80", got)
	}
	if got := wikiSearchSemanticRankLimit(0); got != wikiSearchMinRankWindow {
		t.Fatalf("wikiSearchSemanticRankLimit(0) = %d, want %d", got, wikiSearchMinRankWindow)
	}
}

func TestWikiGitCandidateMatchesAllTokensAcrossTitleSlugAndBody(t *testing.T) {
	bodyMatches := map[string]map[string]struct{}{
		"byoc": {
			"calls/demo": {},
		},
		"plan": {
			"calls/demo":      {},
			"calls/byoc-demo": {},
		},
	}

	if !wikiGitCandidateMatchesAllTokens("How's migration", "calls/demo", []string{"how's", "byoc"}, bodyMatches) {
		t.Fatal("expected title token plus body token to match")
	}
	if !wikiGitCandidateMatchesAllTokens("Migration", "calls/byoc-demo", []string{"byoc", "plan"}, bodyMatches) {
		t.Fatal("expected slug token plus body token to match")
	}
	if wikiGitCandidateMatchesAllTokens("Migration", "calls/demo", []string{"how's", "missing"}, bodyMatches) {
		t.Fatal("expected missing token to fail")
	}
}

func TestTruncateWikiSearchResultListCopiesWindow(t *testing.T) {
	results := []WikiSearchResult{{Slug: "a"}, {Slug: "b"}, {Slug: "c"}}
	got := truncateWikiSearchResultList(results, 2)
	if len(got) != 2 || got[0].Slug != "a" || got[1].Slug != "b" {
		t.Fatalf("truncate result = %#v, want first two", got)
	}
	got[0].Slug = "changed"
	if results[0].Slug != "a" {
		t.Fatal("truncate should copy the returned window")
	}
}
