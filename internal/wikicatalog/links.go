package wikicatalog

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// PageExt is the on-disk extension for a wiki page body. Mirrors the
// legacy wikiPageExt; kept in this package so the catalog does not
// import the service layer.
const PageExt = ".md"

var (
	// markdownLinkRE matches the URL portion of a markdown
	// `[label](target)` link. Images (`![alt](target)`) are excluded
	// at the call site by checking for a preceding '!'.
	markdownLinkRE = regexp.MustCompile(`\[[^\]]+\]\(([^)]+)\)`)

	// bracketLinkRE matches the GitHub-flavored `[[target]]` and
	// `[[target|label]]` wiki-style links.
	bracketLinkRE = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
)

// WikiShorthandTarget returns the page target from GitHub wiki shorthand.
// GitHub treats `[[target|label]]` as target first, label second; the
// single-argument form `[[target]]` uses the same value for both.
func WikiShorthandTarget(raw string) string {
	if pipe := strings.IndexByte(raw, '|'); pipe >= 0 {
		return raw[:pipe]
	}
	return raw
}

// ExtractOutlinks returns the unique outbound link targets present in
// body. The returned slice is sorted so the resulting wiki_page_links
// rows are stable across writes.
//
// References that:
//   - have a non-empty URL scheme (i.e. external links)
//   - escape the wiki root via `..`
//   - are images
//   - fail slug validation
//
// are dropped silently — they are not catalog links. Anchor (`#…`)
// and query (`?…`) fragments are stripped before validation.
func ExtractOutlinks(body string) []string {
	seen := make(map[string]struct{})
	for _, loc := range markdownLinkRE.FindAllStringSubmatchIndex(body, -1) {
		if len(loc) < 4 {
			continue
		}
		// Exclude images: `![alt](target)`.
		if loc[0] > 0 && body[loc[0]-1] == '!' {
			continue
		}
		if slug := wikiLinkTarget(body[loc[2]:loc[3]]); slug != "" {
			seen[slug] = struct{}{}
		}
	}
	for _, loc := range bracketLinkRE.FindAllStringSubmatchIndex(body, -1) {
		if len(loc) < 4 {
			continue
		}
		if slug := wikiLinkTarget(WikiShorthandTarget(body[loc[2]:loc[3]])); slug != "" {
			seen[slug] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for slug := range seen {
		out = append(out, slug)
	}
	sort.Strings(out)
	return out
}

// wikiLinkTarget normalizes a raw markdown link target into the single
// wiki slug form, or returns "" if the target is not a valid intra-wiki
// reference.
func wikiLinkTarget(raw string) string {
	link := strings.TrimSpace(raw)
	if link == "" {
		return ""
	}
	if i := strings.Index(link, "#"); i >= 0 {
		link = link[:i]
	}
	if i := strings.Index(link, "?"); i >= 0 {
		link = link[:i]
	}
	link = strings.TrimSpace(link)
	if link == "" {
		return ""
	}
	if u, err := url.Parse(link); err == nil && u.Scheme != "" {
		return ""
	}
	link = strings.TrimPrefix(link, "./")
	link = strings.TrimPrefix(link, "/")
	if strings.Contains(link, "../") || strings.HasPrefix(link, "..") {
		return ""
	}
	link = strings.TrimSuffix(link, PageExt)
	link = strings.TrimSpace(link)
	if link == "" {
		return ""
	}
	if err := ValidateWritable(link); err != nil {
		return ""
	}
	return link
}
