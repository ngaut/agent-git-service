package service

// This test guards the contract between the legacy wiki slug
// functions (canonicalWikiLookupSlug, validateWikiSlug,
// validateReadableWikiSlug) and the v1 canonical-form package that
// the catalog primary key depends on. Any drift breaks the migration
// guarantee that catalog rows match the slugs the legacy code created.

import (
	"errors"
	"testing"

	"gh-server/internal/wikicatalog"
)

func TestWikiSlugCanonicalV1MatchesLegacy(t *testing.T) {
	inputs := []string{
		"home",
		"guides/intro",
		"Guides/Intro",
		"HOME",
		"MyPage",
		"my_page",
		"My_Page",
		"My_Mixed Page",
		"Guides / My_Topic Notes",
		"  whitespace-leading",
		"trailing-whitespace  ",
		"page-2",
		"123",
		"kebab-case-leaf",
		"deep/nested/path/leaf",
		"Legacy_Page.v2",
		"page.v2",
		"foo/bar baz/qux",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			gotV1, errV1 := wikicatalog.CanonicalV1(in)
			gotLegacy := canonicalWikiLookupSlug(in)

			// Legacy returns "" for any input it cannot canonicalize,
			// including inputs the v1 function rejects with an error.
			if gotLegacy == "" {
				if errV1 == nil {
					// The one approved divergence is "_sidebar" and
					// its case variants: v1 preserves the reserved
					// literal so a catalog row can have a slug_ci_v1
					// value; legacy could not.
					if !isApprovedSidebarVariant(in) {
						t.Fatalf("legacy rejected %q but v1 accepted as %q",
							in, gotV1)
					}
				}
				return
			}
			if errV1 != nil {
				t.Fatalf("v1 rejected %q (%v) but legacy returned %q",
					in, errV1, gotLegacy)
			}
			if gotV1 != gotLegacy {
				t.Fatalf("v1=%q, legacy=%q for input %q", gotV1, gotLegacy, in)
			}
		})
	}
}

func TestWikiSlugValidateWritableMatchesLegacy(t *testing.T) {
	inputs := []string{
		"home",
		"Home",
		"HOME",
		"my-page",
		"my_page",
		"my page",
		"-leading",
		"_leading",
		".leading",
		"deep/nested/path",
		"deep/Nested/path",
		"page-2",
		"_sidebar",
		"_Sidebar",
		"foo//bar",
		"",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			errV1 := wikicatalog.ValidateWritable(in)
			errLegacy := validateWikiSlug(in)
			if (errV1 == nil) != (errLegacy == nil) {
				t.Fatalf("disagreement on %q: v1=%v legacy=%v",
					in, errV1, errLegacy)
			}
		})
	}
}

func TestWikiSlugValidateReadableMatchesLegacy(t *testing.T) {
	inputs := []string{
		"home",
		"Home",
		"My_Page.v2",
		"guides/My_Topic",
		"-leading",
		"_leading",
		".leading",
		"foo//bar",
		"foo/.",
		"_sidebar",
		"_Sidebar",
		"",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			errV1 := wikicatalog.ValidateReadable(in)
			errLegacy := validateReadableWikiSlug(in)
			if (errV1 == nil) != (errLegacy == nil) {
				t.Fatalf("disagreement on %q: v1=%v legacy=%v",
					in, errV1, errLegacy)
			}
		})
	}
}

// isApprovedSidebarVariant returns true for inputs that v1 deliberately
// accepts even though the legacy lookup function rejected them. This is
// the one approved divergence: legacy could not canonicalize "_sidebar"
// and its case variants, which left the reserved sidebar page unable
// to participate in any case-insensitive lookup. v1 preserves it.
func isApprovedSidebarVariant(in string) bool {
	canonical, err := wikicatalog.CanonicalV1(in)
	return err == nil && canonical == wikicatalog.SidebarSegment
}

// Sanity: errors.Is on ErrInvalidSlug remains the contract.
func TestWikiSlugCanonicalV1InvalidIsErrInvalidSlug(t *testing.T) {
	_, err := wikicatalog.CanonicalV1("")
	if !errors.Is(err, wikicatalog.ErrInvalidSlug) {
		t.Fatalf("expected ErrInvalidSlug, got %v", err)
	}
}
