package search

import (
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
)

func TestFuseIssueSearchResultsPrefersCombinedSignals(t *testing.T) {
	lexical := []db.Issue{
		{ID: 2, Number: 2, Title: "lexical-only newer"},
		{ID: 1, Number: 1, Title: "lexical-and-semantic older"},
	}
	semantic := []db.Issue{
		{ID: 1, Number: 1, Title: "lexical-and-semantic older"},
	}

	got := fuseIssueSearchResults(lexical, semantic, 10)
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
	if got[0].ID != 1 {
		t.Fatalf("expected issue 1 to rank first, got issue %d", got[0].ID)
	}
}

func TestFuseIssueSearchResultIDsPrefersCombinedSignals(t *testing.T) {
	lexical := []uint{2, 1}
	semantic := []uint{1}

	got := fuseIssueSearchResultIDs(lexical, semantic, 10)
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
	if got[0] != 1 {
		t.Fatalf("expected issue 1 to rank first, got issue %d", got[0])
	}
}

func TestDeduplicateOrderedIssueIDsSkipsDuplicates(t *testing.T) {
	got := deduplicateOrderedIssueIDs([]uint{5, 3}, []uint{3, 2, 1}, 10)
	want := []uint{5, 3, 2, 1}
	if len(got) != len(want) {
		t.Fatalf("expected %d IDs, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected ID %d at index %d, got %d", want[i], i, got[i])
		}
	}
}

func TestFusePRSearchResultsPrefersCombinedSignals(t *testing.T) {
	lexical := []db.PullRequest{
		{ID: 2, Number: 2, Title: "lexical-only newer"},
		{ID: 1, Number: 1, Title: "lexical-and-semantic older"},
	}
	semantic := []db.PullRequest{
		{ID: 1, Number: 1, Title: "lexical-and-semantic older"},
	}

	got := fusePRSearchResults(lexical, semantic, 10)
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
	if got[0].ID != 1 {
		t.Fatalf("expected pull request 1 to rank first, got pull request %d", got[0].ID)
	}
}
