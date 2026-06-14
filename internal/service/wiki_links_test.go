package service

import (
	"strings"
	"testing"
)

func TestExtractWikiLinkMatches_IgnoresMarkdownImageEmbeds(t *testing.T) {
	body := "# Setup\n\n![Diagram](home.md)\n\nSee [Home](home.md) and [[home]].\n"

	matches := extractWikiLinkMatches(body)
	if len(matches) != 2 {
		t.Fatalf("expected 2 wiki link matches, got %d", len(matches))
	}
	for _, match := range matches {
		if match.targetSlug != "home" {
			t.Fatalf("targetSlug = %q, want home", match.targetSlug)
		}
		if match.snippet == "![Diagram](home.md)" {
			t.Fatalf("image embed should not produce a backlink snippet")
		}
		if match.literal {
			t.Fatalf("top-level links should not be marked literal")
		}
	}
}

func TestExtractWikiLinkMatches_PreservesNestedLiteralPaths(t *testing.T) {
	body := "See [[guides/setup]] and [Guide](guides/setup.md).\n"

	matches := extractWikiLinkMatches(body)
	if len(matches) != 2 {
		t.Fatalf("expected 2 wiki link matches, got %d", len(matches))
	}
	for _, match := range matches {
		if match.targetSlug != "guides/setup" {
			t.Fatalf("targetSlug = %q, want guides/setup", match.targetSlug)
		}
		if !match.literal {
			t.Fatalf("nested links must be treated as literal path matches")
		}
	}
}

func TestExtractWikiLinkMatches_UsesGitHubShorthandTarget(t *testing.T) {
	body := "See [[guides/getting-started|Getting started]] and [[guides/setup|other-page]].\n"

	matches := extractWikiLinkMatches(body)
	if len(matches) != 2 {
		t.Fatalf("expected 2 wiki link matches, got %d", len(matches))
	}
	if matches[0].targetSlug != "guides/getting-started" {
		t.Fatalf("first targetSlug = %q, want guides/getting-started", matches[0].targetSlug)
	}
	if matches[1].targetSlug != "guides/setup" {
		t.Fatalf("second targetSlug = %q, want guides/setup", matches[1].targetSlug)
	}
	for _, match := range matches {
		if !match.literal {
			t.Fatalf("nested shorthand target %q must be treated as literal", match.targetSlug)
		}
	}
}

func TestWikiBacklinkGrepPatterns_UsesExactSlug(t *testing.T) {
	slug := "one-two/three-four/five-six/seven-eight/nine-ten/eleven-twelve"

	patterns := wikiBacklinkGrepPatterns(slug)
	if len(patterns) != 1 || patterns[0] != slug {
		t.Fatalf("patterns = %#v, want exact slug only", patterns)
	}
}

func TestRewriteWikiReferences_RewritesLiteralTargetsOnly(t *testing.T) {
	body := strings.Join([]string{
		"# Home",
		"",
		"See [[guides/setup|Setup guide]] and [Setup](guides/setup.md?view=1#intro).",
		"Label-only text should stay [[home|guides/setup]].",
		"",
		"[setup-ref]: guides/setup.md#deep \"Guide\"",
		"",
		"`[[guides/setup]]`",
		"",
		"```md",
		"[[guides/setup]]",
		"[Setup](guides/setup.md)",
		"```",
		"",
		"<pre>",
		"[[guides/setup]]",
		"</pre>",
		"",
		"[[Setup]]",
	}, "\n")

	rewritten, changed, err := rewriteWikiReferences(body, "guides/setup", "tutorials/setup")
	if err != nil {
		t.Fatalf("rewriteWikiReferences: %v", err)
	}
	if !changed {
		t.Fatal("expected rewriteWikiReferences to report a change")
	}

	want := strings.Join([]string{
		"# Home",
		"",
		"See [[tutorials/setup|Setup guide]] and [Setup](tutorials/setup.md?view=1#intro).",
		"Label-only text should stay [[home|guides/setup]].",
		"",
		"[setup-ref]: tutorials/setup.md#deep \"Guide\"",
		"",
		"`[[guides/setup]]`",
		"",
		"```md",
		"[[guides/setup]]",
		"[Setup](guides/setup.md)",
		"```",
		"",
		"<pre>",
		"[[guides/setup]]",
		"</pre>",
		"",
		"[[Setup]]",
	}, "\n")
	if rewritten != want {
		t.Fatalf("rewritten body mismatch:\n--- got ---\n%s\n--- want ---\n%s", rewritten, want)
	}
}

func TestRewriteWikiReferences_RewritesIndentedReferenceDefinitions(t *testing.T) {
	body := strings.Join([]string{
		"List:",
		"  [setup-ref]: guides/setup.md",
		"   [deep-ref]: ./guides/setup.md#anchor",
		"    [too-deep]: guides/setup.md",
	}, "\n")

	rewritten, changed, err := rewriteWikiReferences(body, "guides/setup", "tutorials/setup")
	if err != nil {
		t.Fatalf("rewriteWikiReferences: %v", err)
	}
	if !changed {
		t.Fatal("expected indented reference definitions to be rewritten")
	}

	want := strings.Join([]string{
		"List:",
		"  [setup-ref]: tutorials/setup.md",
		"   [deep-ref]: ./tutorials/setup.md#anchor",
		"    [too-deep]: guides/setup.md",
	}, "\n")
	if rewritten != want {
		t.Fatalf("rewritten body mismatch:\n--- got ---\n%s\n--- want ---\n%s", rewritten, want)
	}
}

func TestRewriteWikiReferences_RejectsInvalidUTF8(t *testing.T) {
	body := string([]byte{'#', ' ', 'B', 'a', 'd', '\n', '\n', '[', '[', 'h', 'o', 'm', 'e', ']', ']', '\xff'})

	_, _, err := rewriteWikiReferences(body, "home", "start")
	if err == nil {
		t.Fatal("expected invalid UTF-8 body to fail rewrite")
	}
}
