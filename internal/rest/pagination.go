package rest

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const (
	defaultPerPage = 30
	maxPerPage     = 100
)

// parsePagination reads page and per_page query parameters from the request.
// Returns (page, perPage) with sensible defaults: page ≥ 1, 1 ≤ perPage ≤ 100.
func parsePagination(r *http.Request) (int, int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if perPage < 1 {
		perPage = defaultPerPage
	}
	if perPage > maxPerPage {
		perPage = maxPerPage
	}
	return page, perPage
}

// buildLinkHeader returns the RFC 5988 Link header value for pagination.
func buildLinkHeader(r *http.Request, baseURL string, totalItems, page, perPage int) string {
	if totalItems == 0 {
		return ""
	}

	lastPage := (totalItems + perPage - 1) / perPage
	if lastPage <= 1 {
		return ""
	}

	query := r.URL.Query()
	makeLink := func(p int, rel string) string {
		query.Set("page", strconv.Itoa(p))
		query.Set("per_page", strconv.Itoa(perPage))
		u := baseURL + r.URL.EscapedPath() + "?" + query.Encode()
		return fmt.Sprintf("<%s>; rel=\"%s\"", u, rel)
	}

	var links []string
	if page < lastPage {
		links = append(links, makeLink(page+1, "next"))
	}
	if page > 1 {
		links = append(links, makeLink(page-1, "prev"))
	}
	links = append(links, makeLink(1, "first"))
	links = append(links, makeLink(lastPage, "last"))

	return strings.Join(links, ", ")
}

// setLinkHeader writes the RFC 5988 Link header for GitHub API pagination.
func setLinkHeader(w http.ResponseWriter, r *http.Request, baseURL string, totalItems, page, perPage int) {
	link := buildLinkHeader(r, baseURL, totalItems, page, perPage)
	if link == "" {
		return
	}
	w.Header().Set("Link", link)
}

// paginate applies page/per_page slicing to an already-fetched slice.
// It automatically constructs and writes the RFC 5988 Link header to w.
// Returns the paginated sub-slice. If page is beyond the data, returns empty.
// Safely ignores w and r if they are nil (for testing).
func paginate[T any](w http.ResponseWriter, r *http.Request, baseURL string, items []T, page, perPage int) []T {
	if w != nil && r != nil {
		setLinkHeader(w, r, baseURL, len(items), page, perPage)
	}

	start := (page - 1) * perPage
	if start >= len(items) {
		return []T{}
	}
	end := start + perPage
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}
