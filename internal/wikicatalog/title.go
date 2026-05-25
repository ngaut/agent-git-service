package wikicatalog

import "strings"

// wikiTitleReplacer collapses slug separators to spaces. Identical to
// the legacy implementation in internal/service/wiki.go so any caller
// switching from the legacy helper to TitleFromSlug observes no diff.
var wikiTitleReplacer = strings.NewReplacer("-", " ", "_", " ")

// TitleFromSlug derives the display title returned by the wiki REST
// API from a slug. The algorithm:
//
//  1. Take the leaf (post-last-slash) segment of the slug.
//  2. Replace '-' and '_' with spaces.
//  3. Collapse runs of whitespace via strings.Fields — this is what
//     makes "_sidebar", "trailing-", "multi--dash" all produce clean
//     single-spaced titles.
//  4. Capitalize the first letter of each word.
//
// The strings.Fields step is the load-bearing detail that the
// previous byte-walk reimplementation got wrong, producing leading,
// trailing, and double spaces for the same inputs. There is now one
// implementation in this package; the service layer wraps it.
func TitleFromSlug(slug string) string {
	parts := strings.Split(strings.Trim(slug, "/"), "/")
	leaf := slug
	if len(parts) > 0 && parts[len(parts)-1] != "" {
		leaf = parts[len(parts)-1]
	}
	leaf = wikiTitleReplacer.Replace(leaf)
	words := strings.Fields(leaf)
	if len(words) == 0 {
		return slug
	}
	for i, word := range words {
		if word == "" {
			continue
		}
		b := []byte(word)
		if b[0] >= 'a' && b[0] <= 'z' {
			b[0] -= 'a' - 'A'
		}
		words[i] = string(b)
	}
	return strings.Join(words, " ")
}
