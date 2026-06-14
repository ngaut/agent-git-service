// Package wikicatalog implements the wiki storage catalog: the
// relational source of truth for wiki pages, revisions, and changesets
// described in docs/design/wiki-storage-rearchitecture.md.
//
// This file owns the single wiki slug grammar used by storage, lookup,
// links, and the public write API.
package wikicatalog

import (
	"errors"
	"fmt"
	"strings"
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

// ValidateWritable enforces the wiki slug grammar used for storage and
// every public wiki mutation: lowercase alphanumerics plus '-' (which
// may not start a segment). "_sidebar" remains the only reserved
// exception.
func ValidateWritable(slug string) error {
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
