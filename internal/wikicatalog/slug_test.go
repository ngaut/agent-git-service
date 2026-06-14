package wikicatalog

import (
	"errors"
	"strings"
	"testing"
)

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
}

func TestValidateWritableRejectsInvalid(t *testing.T) {
	bad := []string{
		"",
		"Home",
		"my_page",
		"my page",
		"page.v2",
		"-leading",
		".leading",
		"page!",
		"foo/Bar",
		"foo//bar",
		"foo/-leading",
		"a/b/c/d/e/f/g",
		strings.Repeat("a", MaxSlugLength+1),
		strings.Repeat("a", MaxSegmentLength+1),
	}
	for _, s := range bad {
		err := ValidateWritable(s)
		if err == nil {
			t.Errorf("ValidateWritable(%q) expected error, got nil", s)
			continue
		}
		if !errors.Is(err, ErrInvalidSlug) {
			t.Errorf("ValidateWritable(%q) error = %v, want ErrInvalidSlug", s, err)
		}
	}
}
