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

func TestFuseDetailedSearchRanksExplainsSignalSource(t *testing.T) {
	lexical := issueIDsToRankedSearchIDs([]uint{2, 1})
	semantic := issueIDsToRankedSearchIDs([]uint{1, 3})

	got := fuseDetailedSearchRanks(lexical, semantic, false, 10)
	if len(got) != 3 {
		t.Fatalf("expected 3 results, got %d", len(got))
	}
	byID := make(map[uint]detailedSearchRank, len(got))
	for _, rank := range got {
		byID[rank.ID] = rank
	}
	if byID[1].SearchPath != searchPathHybrid {
		t.Fatalf("expected issue 1 to be hybrid, got %q", byID[1].SearchPath)
	}
	if byID[2].SearchPath != searchPathLexicalOnly {
		t.Fatalf("expected issue 2 to be lexical-only, got %q", byID[2].SearchPath)
	}
	if byID[3].SearchPath != searchPathSemanticOnly {
		t.Fatalf("expected issue 3 to be semantic-only, got %q", byID[3].SearchPath)
	}
	if byID[1].LexicalRank == 0 || byID[1].SemanticRank == 0 {
		t.Fatalf("expected hybrid result to include both ranks, got %#v", byID[1])
	}
}

func TestDeduplicateDetailedSearchRanksPreservesSemanticMetadataForExplicitSort(t *testing.T) {
	semanticDistance := 0.125
	lexical := []rankedSearchID{
		{ID: 7, Score: 0.9},
		{ID: 8, Score: 0.8},
	}
	semantic := []rankedSearchID{
		{ID: 7, Score: reciprocalRankScore(1), Distance: &semanticDistance},
		{ID: 9, Score: reciprocalRankScore(2)},
	}

	got := deduplicateDetailedSearchRanks(lexical, semantic, 10)
	if len(got) != 3 {
		t.Fatalf("expected 3 results, got %d", len(got))
	}
	if got[0].ID != 7 {
		t.Fatalf("expected explicit sort to preserve lexical ordering, got %#v", got)
	}
	if got[0].SemanticRank != 1 {
		t.Fatalf("expected overlapping result to preserve semantic rank, got %#v", got[0])
	}
	if got[0].SearchPath != searchPathHybrid {
		t.Fatalf("expected overlapping result to report hybrid search path, got %#v", got[0])
	}
	if got[0].SemanticDistance == nil || *got[0].SemanticDistance != semanticDistance {
		t.Fatalf("expected overlapping result to preserve semantic distance, got %#v", got[0])
	}
}

func TestSearchTextMatchesIncludesCommentHitsAndRepeatedSpans(t *testing.T) {
	fields := []searchTextField{
		{
			name:             "comments",
			values:           []string{"needle first, needle second"},
			allowTextMatches: true,
		},
	}

	matchedFields, textMatches := searchTextMatches(fields, []string{"needle"}, "Issue", true)
	if len(matchedFields) != 1 || matchedFields[0] != "comments" {
		t.Fatalf("expected comments matched field, got %#v", matchedFields)
	}
	if len(textMatches) != 1 {
		t.Fatalf("expected one text match fragment, got %#v", textMatches)
	}
	if textMatches[0].Property != "comments" {
		t.Fatalf("expected comment text match property, got %#v", textMatches[0])
	}
	if len(textMatches[0].Matches) != 2 {
		t.Fatalf("expected repeated occurrences in fragment, got %#v", textMatches[0].Matches)
	}
	if got := textMatches[0].Matches[0].Indices; got[0] != 0 || got[1] != len("needle") {
		t.Fatalf("expected first span at fragment start, got %#v", got)
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
