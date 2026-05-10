package service

import (
	"strings"
	"testing"
)

func TestExtractWikiLinkMatches_IgnoresMarkdownImageEmbeds(t *testing.T) {
	body := "# Setup\n\n![Diagram](home.md)\n\nSee [Home](home.md) and [[Home]].\n"

	matches := extractWikiLinkMatches(body)
	if len(matches) != 2 {
		t.Fatalf("expected 2 wiki link matches, got %d", len(matches))
	}
	for _, match := range matches {
		if match.targetSlug != "home" && match.targetSlug != "Home" {
			t.Fatalf("targetSlug = %q, want home or Home", match.targetSlug)
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

func TestRewriteWikiReferences_RewritesLiteralTargetsOnly(t *testing.T) {
	body := strings.Join([]string{
		"# Home",
		"",
		"See [[guides/setup]] and [Setup](guides/setup.md?view=1#intro).",
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
		"See [[tutorials/setup]] and [Setup](tutorials/setup.md?view=1#intro).",
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
