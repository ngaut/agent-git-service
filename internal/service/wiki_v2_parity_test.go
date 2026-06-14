package service

import (
	"testing"

	"github.com/ngaut/agent-git-service/internal/wikiv2"
)

func TestWikiV2SlugPathParityMatchesServiceHelpers(t *testing.T) {
	validSlugs := []string{
		"home",
		"guides/setup",
		"guides/nested/deep",
		"_sidebar",
	}
	for _, slug := range validSlugs {
		t.Run(slug, func(t *testing.T) {
			path, err := wikiv2.SlugToPath(slug)
			if err != nil {
				t.Fatalf("SlugToPath(%q): %v", slug, err)
			}
			if got := wikiSlugToPath(slug); got != path {
				t.Fatalf("wikiSlugToPath(%q) = %q, want %q", slug, got, path)
			}
			gotSlug, ok := wikiv2.PathToSlug(path)
			if !ok {
				t.Fatalf("PathToSlug(%q) rejected canonical path", path)
			}
			if serviceSlug := wikiPathToSlug(path); serviceSlug != gotSlug {
				t.Fatalf("wikiPathToSlug(%q) = %q, want %q", path, serviceSlug, gotSlug)
			}
		})
	}

	for _, path := range []string{"", ".hidden.md", "guides/setup", "guides/setup.txt", "guides//setup.md"} {
		if got, ok := wikiv2.PathToSlug(path); ok || got != "" {
			t.Fatalf("PathToSlug(%q) = (%q, %v), want rejection", path, got, ok)
		}
		if serviceSlug := wikiPathToSlug(path); serviceSlug != "" {
			t.Fatalf("wikiPathToSlug(%q) = %q, want empty", path, serviceSlug)
		}
	}
}
