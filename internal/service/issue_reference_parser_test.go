package service

import "testing"

func TestParseIssueReferences(t *testing.T) {
	body := "Related to #1, octo/repo#2, and https://example.test/acme/widgets/issues/3. Also https://example.test/acme/widgets/pull/4"
	got := ParseIssueReferences(body, "local/repo")
	want := []IssueReferenceMatch{
		{RepositoryFullName: "local/repo", Number: 1, RawReference: "#1"},
		{RepositoryFullName: "octo/repo", Number: 2, RawReference: "octo/repo#2"},
		{RepositoryFullName: "acme/widgets", Number: 3, RawReference: "https://example.test/acme/widgets/issues/3"},
		{RepositoryFullName: "acme/widgets", Number: 4, RawReference: "https://example.test/acme/widgets/pull/4"},
	}
	if len(got) != len(want) {
		t.Fatalf("len(ParseIssueReferences) = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("match %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestParseIssueReferences_IgnoresCodeAndCommentsAndDedupes(t *testing.T) {
	body := "see #1 and #1\n`#2`\n```\n#3\n```\n<!-- #4 -->\nowner/repo#5"
	got := ParseIssueReferences(body, "local/repo")
	want := []IssueReferenceMatch{
		{RepositoryFullName: "local/repo", Number: 1, RawReference: "#1"},
		{RepositoryFullName: "owner/repo", Number: 5, RawReference: "owner/repo#5"},
	}
	if len(got) != len(want) {
		t.Fatalf("len(ParseIssueReferences) = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("match %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}
