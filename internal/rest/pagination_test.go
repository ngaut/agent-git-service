package rest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParsePagination(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		wantPage int
		wantPer  int
	}{
		{"defaults", "", 1, defaultPerPage},
		{"explicit", "page=3&per_page=10", 3, 10},
		{"page_zero", "page=0", 1, defaultPerPage},
		{"page_negative", "page=-5", 1, defaultPerPage},
		{"per_page_zero", "per_page=0", 1, defaultPerPage},
		{"per_page_negative", "per_page=-1", 1, defaultPerPage},
		{"per_page_above_max", "per_page=200", 1, maxPerPage},
		{"per_page_at_max", "per_page=100", 1, 100},
		{"non_numeric", "page=abc&per_page=xyz", 1, defaultPerPage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/?"+tt.query, nil)
			page, perPage := parsePagination(r)
			if page != tt.wantPage {
				t.Errorf("page = %d, want %d", page, tt.wantPage)
			}
			if perPage != tt.wantPer {
				t.Errorf("perPage = %d, want %d", perPage, tt.wantPer)
			}
		})
	}
}

func TestPaginate(t *testing.T) {
	items := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}

	tests := []struct {
		name    string
		page    int
		perPage int
		want    []int
	}{
		{"first_page", 1, 3, []int{0, 1, 2}},
		{"second_page", 2, 3, []int{3, 4, 5}},
		{"last_partial", 4, 3, []int{9}},
		{"beyond_data", 5, 3, []int{}},
		{"full_page", 1, 10, items},
		{"single_item", 3, 1, []int{2}},
		{"large_per_page", 1, 100, items},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := paginate(nil, nil, "", items, tt.page, tt.perPage)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for i, v := range got {
				if v != tt.want[i] {
					t.Errorf("got[%d] = %d, want %d", i, v, tt.want[i])
				}
			}
		})
	}
}

func TestPaginateEmpty(t *testing.T) {
	got := paginate(nil, nil, "", []string{}, 1, 10)
	if len(got) != 0 {
		t.Errorf("expected empty, got %d items", len(got))
	}
}

func TestSetLinkHeader(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v3/repos/a/b/issues?state=open", nil)
	w := httptest.NewRecorder()

	setLinkHeader(w, r, "http://localhost:8080", 50, 2, 20)

	link := w.Header().Get("Link")
	expected := `<http://localhost:8080/api/v3/repos/a/b/issues?page=3&per_page=20&state=open>; rel="next", <http://localhost:8080/api/v3/repos/a/b/issues?page=1&per_page=20&state=open>; rel="prev", <http://localhost:8080/api/v3/repos/a/b/issues?page=1&per_page=20&state=open>; rel="first", <http://localhost:8080/api/v3/repos/a/b/issues?page=3&per_page=20&state=open>; rel="last"`

	if link != expected {
		t.Errorf("\ngot:  %s\nwant: %s", link, expected)
	}
}

func TestSetLinkHeaderPreservesEscapedPath(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v3/repos/a/b/wiki/pages/guides%2Fsetup/history?per_page=1", nil)
	w := httptest.NewRecorder()

	setLinkHeader(w, r, "http://localhost:8080", 3, 1, 1)

	link := w.Header().Get("Link")
	if !strings.Contains(link, "/wiki/pages/guides%2Fsetup/history?") {
		t.Fatalf("expected Link header to preserve encoded wiki slug, got %q", link)
	}
	if strings.Contains(link, "/wiki/pages/guides/setup/history?") {
		t.Fatalf("Link header decoded nested wiki slug into an unusable path: %q", link)
	}
}
