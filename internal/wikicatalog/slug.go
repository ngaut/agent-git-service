// Package wikicatalog implements the wiki storage catalog: the
// relational source of truth for wiki pages, revisions, and changesets
// described in docs/design/wiki-storage-rearchitecture.md.
//
// This file owns the slug grammar and the canonical-form function that
// every catalog primary key depends on. The behavior is frozen at v1.
// Any future change to the canonical form requires a new version
// (slug_ci_v2) with a parallel column during migration; the v1 function
// must keep its current input→output mapping forever.
package wikicatalog

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Slug grammar limits. These match the wiki API's historical limits.
const (
	MaxSlugLength    = 255
	MaxSlugDepth     = 6
	MaxSegmentLength = 64

	// SidebarSegment is a reserved leaf segment that the public API
	// allows even though it begins with an underscore.
	SidebarSegment = "_sidebar"
)

// ErrInvalidSlug is the sentinel for any slug grammar violation.
var ErrInvalidSlug = errors.New("invalid wiki slug")

// CanonicalV1 returns the canonical (lookup) form of a slug. This is
// the function whose output backs the slug_ci_v1 column.
//
// The transformation, identical to the legacy canonicalWikiLookupSlug
// in internal/service/wiki.go, is:
//
//  1. split on '/'
//  2. for each segment:
//     a. trim leading/trailing whitespace
//     b. replace '_' with '-'
//     c. collapse runs of internal whitespace into a single '-'
//     d. lowercase
//  3. rejoin with '/'
//  4. reject if the result violates the readable slug grammar
//
// Behavior is locked by TestCanonicalV1Golden.
func CanonicalV1(slug string) (string, error) {
	if slug == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidSlug)
	}
	parts := strings.Split(slug, "/")
	if len(parts) > MaxSlugDepth {
		return "", fmt.Errorf("%w: exceeds depth %d", ErrInvalidSlug, MaxSlugDepth)
	}
	out := make([]string, len(parts))
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return "", fmt.Errorf("%w: empty segment", ErrInvalidSlug)
		}
		// Reserved segments survive verbatim. Without this, "_sidebar"
		// would canonicalize to "-sidebar" via the underscore rule and
		// then fail the leading-character check — leaving GitHub's
		// reserved sidebar page unable to have a slug_ci_v1 value.
		if lower := strings.ToLower(part); isReservedSegment(lower) {
			out[i] = lower
			continue
		}
		part = strings.ReplaceAll(part, "_", "-")
		part = strings.Join(strings.Fields(part), "-")
		part = strings.ToLower(part)
		out[i] = part
	}
	canonical := strings.Join(out, "/")
	if err := ValidateReadable(canonical); err != nil {
		return "", err
	}
	return canonical, nil
}

// ValidateReadable enforces the readable-slug grammar: lower or upper
// alphanumerics plus '-', '_', '.' (the latter three may not start a
// segment), each segment ≤ MaxSegmentLength, total slug ≤ MaxSlugLength
// and ≤ MaxSlugDepth segments. The reserved leaf segment "_sidebar" is
// allowed verbatim.
//
// This mirrors the legacy validateReadableWikiSlug.
func ValidateReadable(slug string) error {
	if slug == "" {
		return fmt.Errorf("%w: empty", ErrInvalidSlug)
	}
	if len(slug) > MaxSlugLength {
		return fmt.Errorf("%w: too long", ErrInvalidSlug)
	}
	parts := strings.Split(slug, "/")
	if len(parts) > MaxSlugDepth {
		return fmt.Errorf("%w: exceeds depth %d", ErrInvalidSlug, MaxSlugDepth)
	}
	for _, part := range parts {
		if err := validateReadableSegment(part); err != nil {
			return err
		}
	}
	return nil
}

func validateReadableSegment(segment string) error {
	if segment == "" {
		return fmt.Errorf("%w: empty segment", ErrInvalidSlug)
	}
	if segment == "." || segment == ".." {
		return fmt.Errorf("%w: reserved segment %q", ErrInvalidSlug, segment)
	}
	if len(segment) > MaxSegmentLength {
		return fmt.Errorf("%w: segment too long", ErrInvalidSlug)
	}
	if segment == SidebarSegment {
		return nil
	}
	for i, r := range segment {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case (r == '-' || r == '_' || r == '.') && i > 0:
		default:
			return fmt.Errorf("%w: disallowed character %q", ErrInvalidSlug, string(r))
		}
	}
	first := segment[0]
	if first == '-' || first == '_' || first == '.' {
		return fmt.Errorf("%w: segment cannot start with punctuation", ErrInvalidSlug)
	}
	return nil
}

// ValidateWritable enforces the stricter grammar used when a client
// creates or updates a page through REST: lowercase alphanumerics plus
// '-' (which may not start a segment). "_sidebar" remains the only
// reserved exception.
//
// This mirrors the legacy validateWikiSlug.
func ValidateWritable(slug string) error {
	if slug == "" {
		return fmt.Errorf("%w: empty", ErrInvalidSlug)
	}
	if err := ValidateReadable(slug); err != nil {
		return err
	}
	if hasUpper(slug) {
		return fmt.Errorf("%w: must be lowercase", ErrInvalidSlug)
	}
	for _, part := range strings.Split(slug, "/") {
		if err := validateWritableSegment(part); err != nil {
			return err
		}
	}
	return nil
}

func validateWritableSegment(segment string) error {
	if segment == "" {
		return fmt.Errorf("%w: empty segment", ErrInvalidSlug)
	}
	if segment == "." || segment == ".." {
		return fmt.Errorf("%w: reserved segment %q", ErrInvalidSlug, segment)
	}
	if len(segment) > MaxSegmentLength {
		return fmt.Errorf("%w: segment too long", ErrInvalidSlug)
	}
	if segment == SidebarSegment {
		return nil
	}
	for i, r := range segment {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' && i > 0:
		default:
			return fmt.Errorf("%w: disallowed character %q", ErrInvalidSlug, string(r))
		}
	}
	if segment[0] == '-' {
		return fmt.Errorf("%w: segment cannot start with %q", ErrInvalidSlug, "-")
	}
	return nil
}

func hasUpper(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// isReservedSegment reports whether a (lowercased) segment is one of
// the public-API-reserved literals that must pass through slug
// canonicalization unchanged.
func isReservedSegment(lower string) bool {
	return lower == SidebarSegment
}
