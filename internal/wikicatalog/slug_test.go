package wikicatalog

import (
	"errors"
	"strings"
	"testing"
)

// TestCanonicalV1Golden is the lock for the slug_ci_v1 mapping. Any
// change to a row here means we are introducing a new slug version and
// must add a slug_ci_v2 column with migration, not amend v1.
func TestCanonicalV1Golden(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Simple lowercase passthrough.
		{"lowercase-simple", "home", "home"},
		{"already-canonical-nested", "guides/intro", "guides/intro"},

		// Case folding.
		{"uppercase-folded", "HOME", "home"},
		{"mixed-case-folded", "MyPage", "mypage"},
		{"mixed-case-nested", "Guides/Intro", "guides/intro"},

		// Underscore → hyphen.
		{"underscore-leaf", "my_page", "my-page"},
		{"underscore-multiple", "deep_nested_topic", "deep-nested-topic"},
		{"underscore-mixed", "Guides/My_Topic", "guides/my-topic"},

		// Whitespace collapsing.
		{"single-space", "my page", "my-page"},
		{"multi-space", "my   spaced   page", "my-spaced-page"},
		{"tab-collapsed", "my\tpage", "my-page"},

		// Combined: case, underscore, whitespace.
		{"combined", "My Mixed_Up Page", "my-mixed-up-page"},
		{"combined-nested", "Guides / My_Topic Notes", "guides/my-topic-notes"},

		// Digits and hyphens preserved.
		{"digit-suffix", "page-2", "page-2"},
		{"all-digits", "123", "123"},
		{"hyphen-in-leaf", "kebab-case-leaf", "kebab-case-leaf"},

		// Dot characters (allowed in readable form) survive lowercase.
		{"dot-in-segment", "Legacy_Page.v2", "legacy-page.v2"},

		// Reserved leaf segment passes through verbatim, including
		// case variants which must fold to the canonical literal.
		{"sidebar-reserved", "_sidebar", "_sidebar"},
		{"sidebar-uppercase", "_Sidebar", "_sidebar"},
		{"sidebar-shout", "_SIDEBAR", "_sidebar"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CanonicalV1(tc.in)
			if err != nil {
				t.Fatalf("CanonicalV1(%q) returned error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("CanonicalV1(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCanonicalV1RejectsInvalid(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"empty-segment-leading", "/home"},
		{"empty-segment-trailing", "home/"},
		{"empty-segment-middle", "foo//bar"},
		{"dot-segment", "foo/."},
		{"dotdot-segment", "foo/.."},
		{"too-deep", "a/b/c/d/e/f/g"},
		{"too-long", strings.Repeat("a", MaxSlugLength+1)},
		{"segment-too-long", strings.Repeat("a", MaxSegmentLength+1)},
		{"disallowed-character", "page!"},
		{"starts-with-hyphen", "-leading"},
		{"starts-with-dot", ".leading"},
		{"whitespace-only", "   "},
		{"whitespace-only-segment", "foo/   /bar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := CanonicalV1(tc.in)
			if err == nil {
				t.Fatalf("CanonicalV1(%q) = %q, expected ErrInvalidSlug", tc.in, out)
			}
			if !errors.Is(err, ErrInvalidSlug) {
				t.Fatalf("CanonicalV1(%q) error = %v, want ErrInvalidSlug", tc.in, err)
			}
		})
	}
}

// TestCanonicalV1Idempotent: canonicalizing an already-canonical slug
// must be a no-op. This is a property the catalog upsert relies on.
func TestCanonicalV1Idempotent(t *testing.T) {
	seeds := []string{
		"home",
		"guides/intro",
		"my-page",
		"deep/nested/path/leaf",
		"_sidebar",
		"page.v2",
	}
	for _, s := range seeds {
		t.Run(s, func(t *testing.T) {
			once, err := CanonicalV1(s)
			if err != nil {
				t.Fatalf("first pass: %v", err)
			}
			twice, err := CanonicalV1(once)
			if err != nil {
				t.Fatalf("second pass: %v", err)
			}
			if once != twice {
				t.Fatalf("not idempotent: %q -> %q -> %q", s, once, twice)
			}
		})
	}
}

func TestValidateWritable(t *testing.T) {
	ok := []string{
		"home",
		"deep/nested/path",
		"page-2",
		"_sidebar",
		"abc-def",
	}
	for _, s := range ok {
		if err := ValidateWritable(s); err != nil {
			t.Errorf("ValidateWritable(%q) unexpected error: %v", s, err)
		}
	}

	bad := []string{
		"",
		"Home",          // uppercase rejected by writable
		"my_page",       // underscore rejected by writable
		"-leading",      // can't start with -
		".leading",      // can't start with .
		"page!",         // disallowed char
		"foo/Bar",       // segment uppercase
		"foo//bar",      // empty segment
		"foo/-leading",  // segment starts with -
		"a/b/c/d/e/f/g", // too deep
		strings.Repeat("a", MaxSegmentLength+1),
	}
	for _, s := range bad {
		if err := ValidateWritable(s); err == nil {
			t.Errorf("ValidateWritable(%q) expected error, got nil", s)
		}
	}
}

func TestValidateReadableAllowsLegacy(t *testing.T) {
	// Pages that survived from old systems may have mixed case, dots,
	// and underscores. Readable validation must keep accepting them so
	// they remain reachable until renamed.
	ok := []string{
		"Legacy_Page.v2",
		"MyTopic",
		"guides/My_Topic.v3",
	}
	for _, s := range ok {
		if err := ValidateReadable(s); err != nil {
			t.Errorf("ValidateReadable(%q) unexpected error: %v", s, err)
		}
	}
}
