package service

import (
	"strings"
	"testing"
)

func TestBuildIssueListPageQueryUsesMySQLLikeEscapeClause(t *testing.T) {
	filter := normalizedIssueListPageFilter{
		state:       "all",
		kind:        "all",
		titlePrefix: `Run_%`,
		sort:        "created",
		direction:   "desc",
		page:        1,
		perPage:     30,
	}

	sql, args := buildIssueListPageQuery(newWikiSearchDryRunMySQLDB(t), 42, filter, nil, nil, true, false, 31)
	if !strings.Contains(sql, `LOWER(issues.title) LIKE ? ESCAPE '\\'`) {
		t.Fatalf("expected issue title LIKE to use MySQL escape clause, got %q", sql)
	}
	if !strings.Contains(sql, `LOWER(pull_requests.title) LIKE ? ESCAPE '\\'`) {
		t.Fatalf("expected pull request title LIKE to use MySQL escape clause, got %q", sql)
	}
	if !strings.Contains(sql, `ORDER BY created_at DESC, number DESC LIMIT ? OFFSET ?`) {
		t.Fatalf("expected pageable issue list SQL, got %q", sql)
	}
	if got, want := args[2], `run\_\%%`; got != want {
		t.Fatalf("escaped issue title prefix arg = %q, want %q", got, want)
	}
	if got, want := args[4], `run\_\%%`; got != want {
		t.Fatalf("escaped pull request title prefix arg = %q, want %q", got, want)
	}
}
