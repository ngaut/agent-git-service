package wikicatalog

import "testing"

// TestTitleFromSlugGolden locks the public title contract. The
// load-bearing detail is that strings.Fields collapses every run of
// whitespace, so inputs that produce leading, trailing, or doubled
// separators after the wikiTitleReplacer step still yield clean
// single-spaced titles — a property the previous byte-walk
// implementation got wrong and an earlier review surfaced.
func TestTitleFromSlugGolden(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"home", "Home"},
		{"my-page", "My Page"},
		{"My_Page", "My Page"},
		{"deep/nested/path", "Path"},
		{"  spaces  ", "Spaces"},
		{"trailing-dash-", "Trailing Dash"},
		{"leading-dash", "Leading Dash"},
		{"multi--dash", "Multi Dash"},
		{"a_b-c", "A B C"},
		{"_sidebar", "Sidebar"},
		{"123", "123"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := TitleFromSlug(tc.in)
			if got != tc.want {
				t.Fatalf("TitleFromSlug(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
