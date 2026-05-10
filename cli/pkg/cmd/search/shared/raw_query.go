package shared

import (
	"strings"

	"github.com/cli/cli/v2/pkg/search"
)

var issueQualifierKeys = map[string]struct{}{
	"archived":              {},
	"assignee":              {},
	"author":                {},
	"base":                  {},
	"closed":                {},
	"commenter":             {},
	"comments":              {},
	"created":               {},
	"draft":                 {},
	"fork":                  {},
	"forks":                 {},
	"has":                   {},
	"head":                  {},
	"in":                    {},
	"interactions":          {},
	"involves":              {},
	"is":                    {},
	"label":                 {},
	"language":              {},
	"license":               {},
	"linked":                {},
	"mentions":              {},
	"milestone":             {},
	"merged":                {},
	"no":                    {},
	"number-topics":         {},
	"org":                   {},
	"order":                 {},
	"project":               {},
	"pushed":                {},
	"reactions":             {},
	"reason":                {},
	"repo":                  {},
	"review":                {},
	"review-requested":      {},
	"reviewed-by":           {},
	"size":                  {},
	"sort":                  {},
	"stars":                 {},
	"state":                 {},
	"status":                {},
	"team":                  {},
	"team-review-requested": {},
	"topic":                 {},
	"topics":                {},
	"type":                  {},
	"updated":               {},
	"user":                  {},
	"user-review-requested": {},
}

// ApplySearchKeywords sets query keywords, preserving raw qualifier syntax when provided
// as a single space-delimited argument.
func ApplySearchKeywords(query *search.Query, args []string) {
	if raw, ok := rawQualifierQuery(args); ok {
		query.ImmutableKeywords = raw
		query.Keywords = []string{}
		return
	}
	query.Keywords = args
}

func rawQualifierQuery(args []string) (string, bool) {
	if len(args) != 1 {
		return "", false
	}

	raw := args[0]
	if !strings.Contains(raw, " ") {
		return "", false
	}

	if containsIssueQualifier(raw) {
		return raw, true
	}

	return "", false
}

func containsIssueQualifier(raw string) bool {
	for _, field := range strings.Fields(raw) {
		token := strings.TrimSpace(field)
		token = strings.Trim(token, "()")
		token = strings.TrimLeft(token, "-")
		key, _, ok := strings.Cut(token, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(key)
		if _, ok := issueQualifierKeys[key]; ok {
			return true
		}
	}
	return false
}
