package search

import (
	"fmt"
	"strings"

	"gh-server/internal/db"
)

// CoreFilters groups primary qualifiers used to filter results.
type CoreFilters struct {
	Repo            string
	Repos           []string
	State           string
	StateConflict   bool // set when conflicting state/is qualifiers are present
	Author          string
	Assignee        string
	ReviewRequested string
	// Labels is a slice of label groups. Each group corresponds to a single
	// label: qualifier. Within a group, labels are comma-separated and OR'd.
	// Separate label: qualifiers are AND'd across groups.
	// Example: label:bug,ui label:critical → (bug OR ui) AND critical.
	Labels   [][]string
	FreeText []string
	IsPR     bool
	IsIssue  bool
	Number   *int // #<number> qualifier for searching by issue/PR number

	// Tier 1: High-impact qualifiers
	Draft      bool   // is:draft
	DraftSet   bool   // distinguishes unset from false
	Review     string // review:none, review:approved, review:changes_requested
	ReviewedBy string // reviewed-by:LOGIN
	Sort       string // sort:created-asc, sort:updated-desc, etc.
	Order      string // order:asc, order:desc

	// Tier 4: Additional filters
	Reason   string // reason:completed, reason:"not planned"
	Involves string // involves:LOGIN
	Merged   *bool  // is:merged (true) / is:unmerged (false)
}

// NegationFilters groups negated qualifiers.
type NegationFilters struct {
	NegatedLabels   [][]string // -label:bug,ui
	NegatedAuthor   string     // -author:bot
	NegatedAssignee string     // -assignee:bot
}

// MetadataFilters groups metadata flags.
type MetadataFilters struct {
	NoLabel     bool // no:label
	NoAssignee  bool // no:assignee
	NoMilestone bool // no:milestone
	NoProject   bool // no:project
	HasLabel    bool // has:label
	HasAssignee bool // has:assignee
}

// ParserFields captures parser-complete spec fields.
type ParserFields struct {
	// Parser Complete Spec - Additional handles
	Mentions            string
	Team                string
	Commenter           string
	UserReviewRequested string
	TeamReviewRequested string

	// Parser Complete Spec - Project/Milestone/Language
	Milestone  string
	Project    string
	Language   string
	Topic      []string
	User       string
	Org        string
	Fork       string
	Visibility string

	// Parser Complete Spec - Dates/Counts/Status/Links
	Status       string
	Linked       string
	Comments     string
	Interactions string
	Reactions    string
	Closed       string
	Created      string
	Pushed       string
	Updated      string
	MergedDate   string // distinct from Merged bool (is:merged)
	Head         string
	Base         string
	Size         string
	Topics       string
	Stars        string
	Forks        string
	License      string

	// Parser Complete Spec - Bools & Modifiers
	Archived    *bool
	IsLocked    *bool
	IsSponsored *bool
	In          []string
}

// SearchQualifiers holds parsed GitHub search qualifiers.
type SearchQualifiers struct {
	CoreFilters
	NegationFilters
	MetadataFilters
	ParserFields
}

// CommitSearchQuery captures commit search qualifiers.
type CommitSearchQuery struct {
	FreeText       []string
	Repo           string
	Repos          []string
	Author         string
	Committer      string
	AuthorName     string
	AuthorEmail    string
	CommitterName  string
	CommitterEmail string
	Hash           string
	Parent         string
	Tree           string
	AuthorDate     string
	CommitterDate  string
	Merge          *bool
	Org            string
	User           string
	Visibility     string
	HasQualifiers  bool
}

// HasCommitFilters reports whether commit-specific qualifiers are set,
// indicating commit filters should be constructed.
func (q CommitSearchQuery) HasCommitFilters() bool {
	return q.Author != "" || q.Committer != "" || q.AuthorName != "" ||
		q.AuthorEmail != "" || q.CommitterName != "" || q.CommitterEmail != "" ||
		q.Hash != "" || q.Parent != "" || q.Tree != "" ||
		q.AuthorDate != "" || q.CommitterDate != "" || q.Merge != nil
}

// CodeSearchQuery captures code search qualifiers.
type CodeSearchQuery struct {
	FreeText          []string
	Repos             []string
	Repo              string
	Filename          string
	Extensions        []string
	Path              string
	Language          string
	NegatedQualifiers []string
	HasQualifiers     bool
}

// IssueListFilter groups filters used by ListIssuesFiltered. It mirrors the
// prior positional parameters so callers can set only what they need while
// preserving existing zero-value behavior.
type IssueListFilter struct {
	RepoFullName string
	State        string
	Assignee     string
	Mentioned    string
	CreatedBy    string
	Labels       string
	Sort         string
	Direction    string
	Milestone    string
	Since        string
}

// languageExtensions maps language names to common file extensions.
var languageExtensions = map[string][]string{
	"go":         {".go"},
	"golang":     {".go"},
	"python":     {".py", ".pyw", ".pyi"},
	"py":         {".py", ".pyw", ".pyi"},
	"javascript": {".js", ".jsx", ".mjs", ".cjs"},
	"js":         {".js", ".jsx", ".mjs", ".cjs"},
	"typescript": {".ts", ".tsx", ".mts", ".cts"},
	"ts":         {".ts", ".tsx", ".mts", ".cts"},
	"java":       {".java"},
	"c":          {".c", ".h"},
	"c++":        {".cpp", ".cc", ".cxx", ".hpp", ".hxx"},
	"cpp":        {".cpp", ".cc", ".cxx", ".hpp", ".hxx"},
	"c#":         {".cs"},
	"csharp":     {".cs"},
	"ruby":       {".rb", ".erb", ".rake"},
	"rb":         {".rb", ".erb", ".rake"},
	"rust":       {".rs"},
	"php":        {".php", ".php3", ".php4", ".php5", ".phtml"},
	"swift":      {".swift"},
	"kotlin":     {".kt", ".kts"},
	"scala":      {".scala", ".sc"},
	"html":       {".html", ".htm", ".xhtml"},
	"css":        {".css", ".scss", ".sass", ".less"},
	"shell":      {".sh", ".bash", ".zsh", ".fish"},
	"bash":       {".sh", ".bash"},
	"sql":        {".sql"},
	"yaml":       {".yaml", ".yml"},
	"yml":        {".yaml", ".yml"},
	"json":       {".json"},
	"xml":        {".xml"},
	"markdown":   {".md", ".markdown"},
	"md":         {".md", ".markdown"},
	"dockerfile": {"Dockerfile", ".dockerfile"},
	"makefile":   {"Makefile", "makefile", ".mk"},
	"text":       {".txt"},
	"plaintext":  {".txt"},
}

// parseLabelGroup splits a label value (possibly comma-separated) into a group.
func parseLabelGroup(v string) []string {
	v = strings.Trim(v, "\"")
	parts := strings.Split(v, ",")
	group := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			group = append(group, p)
		}
	}
	return group
}

// qualifierParser processes a single parsed qualifier token.
// v is the quote-trimmed value, rawValue is the original value before quote
// trimming, and negated indicates the qualifier was prefixed with '-'.
type qualifierParser func(v, rawValue string, negated bool, q *SearchQualifiers)

// stringField returns a qualifierParser that sets a string field when not negated.
func stringField(field func(q *SearchQualifiers) *string) qualifierParser {
	return func(v, _ string, negated bool, q *SearchQualifiers) {
		if !negated {
			*field(q) = v
		}
	}
}

// qualifierParsers maps qualifier keys to their parser functions.
var qualifierParsers = map[string]qualifierParser{
	"repo":                  parseRepoQualifier,
	"label":                 parseLabelQualifier,
	"state":                 parseStateQualifier,
	"is":                    parseIsQualifier,
	"author":                parseAuthorQualifier,
	"assignee":              parseAssigneeQualifier,
	"review-requested":      stringField(func(q *SearchQualifiers) *string { return &q.CoreFilters.ReviewRequested }),
	"user-review-requested": stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.UserReviewRequested }),
	"team-review-requested": stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.TeamReviewRequested }),
	"reviewed-by":           stringField(func(q *SearchQualifiers) *string { return &q.CoreFilters.ReviewedBy }),
	"review":                parseReviewQualifier,
	"team":                  stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Team }),
	"commenter":             stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Commenter }),
	"mentions":              stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Mentions }),
	"project":               stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Project }),
	"milestone":             stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Milestone }),
	"language":              stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Language }),
	"topic":                 parseTopicQualifier,
	"user":                  stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.User }),
	"org":                   stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Org }),
	"fork":                  stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Fork }),
	"visibility":            stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Visibility }),
	"status":                stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Status }),
	"linked":                stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Linked }),
	"comments":              stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Comments }),
	"interactions":          stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Interactions }),
	"reactions":             stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Reactions }),
	"head":                  stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Head }),
	"base":                  stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Base }),
	"closed":                stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Closed }),
	"created":               stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Created }),
	"pushed":                stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Pushed }),
	"updated":               stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Updated }),
	"size":                  stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Size }),
	"topics":                stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Topics }),
	"number-topics":         stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Topics }),
	"stars":                 stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Stars }),
	"forks":                 stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.Forks }),
	"license":               stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.License }),
	"merged":                stringField(func(q *SearchQualifiers) *string { return &q.ParserFields.MergedDate }),
	"in":                    parseInQualifier,
	"draft":                 parseDraftQualifier,
	"archived":              parseArchivedQualifier,
	"type":                  parseTypeQualifier,
	"sort":                  parseSortQualifier,
	"order":                 parseOrderQualifier,
	"no":                    parseNoQualifier,
	"has":                   parseHasQualifier,
	"reason":                stringField(func(q *SearchQualifiers) *string { return &q.CoreFilters.Reason }),
	"involves":              stringField(func(q *SearchQualifiers) *string { return &q.CoreFilters.Involves }),
}

var commitQualifierKeys = map[string]struct{}{
	"author":          {},
	"author-date":     {},
	"author-email":    {},
	"author-name":     {},
	"committer":       {},
	"committer-date":  {},
	"committer-email": {},
	"committer-name":  {},
	"hash":            {},
	"is":              {},
	"merge":           {},
	"org":             {},
	"parent":          {},
	"repo":            {},
	"tree":            {},
	"user":            {},
	"visibility":      {},
}

func parseLabelQualifier(_, rawValue string, negated bool, q *SearchQualifiers) {
	group := parseLabelGroup(rawValue)
	if len(group) > 0 {
		if negated {
			q.NegationFilters.NegatedLabels = append(q.NegationFilters.NegatedLabels, group)
		} else {
			q.CoreFilters.Labels = append(q.CoreFilters.Labels, group)
		}
	}
}

func parseTopicQualifier(v, _ string, negated bool, q *SearchQualifiers) {
	if negated {
		return
	}
	parts := strings.Split(v, ",")
	for _, part := range parts {
		topic := strings.TrimSpace(part)
		if topic == "" {
			continue
		}
		q.ParserFields.Topic = append(q.ParserFields.Topic, topic)
	}
}

func parseRepoQualifier(v, _ string, negated bool, q *SearchQualifiers) {
	if negated || v == "" {
		return
	}
	q.CoreFilters.Repo = v
	q.CoreFilters.Repos = append(q.CoreFilters.Repos, v)
}

func parseStateQualifier(v, _ string, negated bool, q *SearchQualifiers) {
	if !negated {
		setStateFilter(q, v)
	}
}

// setStateFilter applies a state filter while detecting conflicts across
// state:/is: qualifiers. Previously, the last qualifier silently overwrote
// the earlier one, making results order-dependent.
func setStateFilter(q *SearchQualifiers, v string) {
	if q.CoreFilters.StateConflict {
		return
	}
	state := strings.ToLower(v)
	if state == "" {
		return
	}
	if state == "all" {
		if q.CoreFilters.State == "" {
			q.CoreFilters.State = state
		}
		return
	}
	if q.CoreFilters.State == "" || q.CoreFilters.State == "all" {
		q.CoreFilters.State = state
		return
	}
	if q.CoreFilters.State != state {
		q.CoreFilters.StateConflict = true
	}
}

func parseIsQualifier(v, _ string, negated bool, q *SearchQualifiers) {
	switch strings.ToLower(v) {
	case db.StateOpen, db.StateClosed:
		if !negated {
			setStateFilter(q, v)
		}
	case "pr":
		if !negated {
			q.CoreFilters.IsPR = true
			q.CoreFilters.IsIssue = false
		} else {
			q.CoreFilters.IsIssue = true
			q.CoreFilters.IsPR = false
		}
	case "merged":
		if !negated {
			q.CoreFilters.IsPR = true
			q.CoreFilters.IsIssue = false
			setStateFilter(q, db.StateClosed)
			merged := true
			q.CoreFilters.Merged = &merged
		}
	case "unmerged":
		if !negated {
			q.CoreFilters.IsPR = true
			q.CoreFilters.IsIssue = false
			merged := false
			q.CoreFilters.Merged = &merged
		}
	case "draft":
		q.CoreFilters.DraftSet = true
		q.CoreFilters.IsPR = true
		if negated {
			q.CoreFilters.Draft = false
		} else {
			q.CoreFilters.Draft = true
		}
	case "locked":
		if negated {
			b := false
			q.ParserFields.IsLocked = &b
		} else {
			b := true
			q.ParserFields.IsLocked = &b
		}
	case "unlocked":
		if negated {
			b := true
			q.ParserFields.IsLocked = &b
		} else {
			b := false
			q.ParserFields.IsLocked = &b
		}
	case "sponsored":
		if negated {
			b := false
			q.ParserFields.IsSponsored = &b
		} else {
			b := true
			q.ParserFields.IsSponsored = &b
		}
	case "issue":
		if !negated {
			q.CoreFilters.IsIssue = true
			q.CoreFilters.IsPR = false
		} else {
			q.CoreFilters.IsPR = true
			q.CoreFilters.IsIssue = false
		}
	}
}

func parseAuthorQualifier(v, _ string, negated bool, q *SearchQualifiers) {
	if negated {
		q.NegationFilters.NegatedAuthor = v
	} else {
		q.CoreFilters.Author = v
	}
}

func parseAssigneeQualifier(v, _ string, negated bool, q *SearchQualifiers) {
	if negated {
		q.NegationFilters.NegatedAssignee = v
	} else {
		q.CoreFilters.Assignee = v
	}
}

func parseReviewQualifier(v, _ string, negated bool, q *SearchQualifiers) {
	if !negated {
		q.CoreFilters.Review = v
		q.CoreFilters.IsPR = true
	}
}

func parseInQualifier(v, _ string, negated bool, q *SearchQualifiers) {
	if !negated {
		q.ParserFields.In = append(q.ParserFields.In, strings.Split(v, ",")...)
	}
}

func parseDraftQualifier(v, _ string, negated bool, q *SearchQualifiers) {
	q.CoreFilters.DraftSet = true
	q.CoreFilters.IsPR = true
	b := (v == "true")
	if negated {
		q.CoreFilters.Draft = !b
	} else {
		q.CoreFilters.Draft = b
	}
}

func parseArchivedQualifier(v, _ string, negated bool, q *SearchQualifiers) {
	b := (v == "true")
	if negated {
		b = !b
	}
	q.ParserFields.Archived = &b
}

func parseTypeQualifier(v, _ string, negated bool, q *SearchQualifiers) {
	switch strings.ToLower(v) {
	case "pr":
		if !negated {
			q.CoreFilters.IsPR = true
			q.CoreFilters.IsIssue = false
		} else {
			q.CoreFilters.IsIssue = true
			q.CoreFilters.IsPR = false
		}
	case "issue":
		if !negated {
			q.CoreFilters.IsIssue = true
			q.CoreFilters.IsPR = false
		} else {
			q.CoreFilters.IsPR = true
			q.CoreFilters.IsIssue = false
		}
	}
}

func parseSortQualifier(v, _ string, negated bool, q *SearchQualifiers) {
	if !negated {
		q.CoreFilters.Sort = v
	}
}

func parseOrderQualifier(v, _ string, negated bool, q *SearchQualifiers) {
	if !negated {
		q.CoreFilters.Order = v
	}
}

func parseNoQualifier(v, _ string, negated bool, q *SearchQualifiers) {
	if !negated {
		switch strings.ToLower(v) {
		case "label":
			q.MetadataFilters.NoLabel = true
		case "assignee":
			q.MetadataFilters.NoAssignee = true
		case "milestone":
			q.MetadataFilters.NoMilestone = true
		case "project":
			q.MetadataFilters.NoProject = true
		}
	}
}

func parseHasQualifier(v, _ string, negated bool, q *SearchQualifiers) {
	if !negated {
		switch strings.ToLower(v) {
		case "label":
			q.MetadataFilters.HasLabel = true
		case "assignee":
			q.MetadataFilters.HasAssignee = true
		}
	}
}

// ParseSearchQuery parses a GitHub-style search query into structured qualifiers.
func ParseSearchQuery(query string) SearchQualifiers {
	var q SearchQualifiers
	tokens := tokenizeSearchQuery(query)
	for _, token := range tokens {
		// Handle negation: tokens starting with - invert the qualifier.
		negated := false
		inner := token
		if strings.HasPrefix(token, "-") && len(token) > 1 && strings.Contains(token, ":") {
			negated = true
			inner = token[1:]
		}

		// Handle #<number> qualifier for searching by issue/PR number.
		// This is a GitHub search syntax: #123 matches issue/PR number 123.
		if !negated && strings.HasPrefix(token, "#") {
			numStr := strings.TrimPrefix(token, "#")
			if num, err := parseNumber(numStr); err == nil {
				q.CoreFilters.Number = &num
				continue
			}
		}

		parts := strings.SplitN(inner, ":", 2)
		if len(parts) != 2 {
			if !negated {
				// Strip quotes from free text phrases (e.g., "exact phrase" → exact phrase)
				freeText := strings.Trim(token, "\"")
				q.CoreFilters.FreeText = append(q.CoreFilters.FreeText, freeText)
			}
			continue
		}

		key := strings.ToLower(parts[0])
		v := strings.Trim(parts[1], "\"")

		if parser, ok := qualifierParsers[key]; ok {
			parser(v, parts[1], negated, &q)
		} else if !negated {
			// Strip quotes from free text phrases for unknown qualifiers
			freeText := strings.Trim(token, "\"")
			q.CoreFilters.FreeText = append(q.CoreFilters.FreeText, freeText)
		}
	}
	return q
}

// ParseCommitSearchQuery parses a commit search query, extracting free text
// while recognizing commit-specific qualifiers so they don't pollute the
// keyword search.
func ParseCommitSearchQuery(query string) CommitSearchQuery {
	var q CommitSearchQuery
	tokens := tokenizeSearchQuery(query)
	for _, token := range tokens {
		negated := false
		inner := token
		if strings.HasPrefix(token, "-") && len(token) > 1 && strings.Contains(token, ":") {
			negated = true
			inner = token[1:]
		}

		parts := strings.SplitN(inner, ":", 2)
		if len(parts) != 2 {
			q.FreeText = append(q.FreeText, strings.Trim(token, "\""))
			continue
		}

		key := strings.ToLower(parts[0])
		value := strings.Trim(parts[1], "\"")
		if _, ok := commitQualifierKeys[key]; ok {
			q.HasQualifiers = true
			if negated {
				continue
			}
			switch key {
			case "repo":
				if value != "" {
					q.Repo = value
					q.Repos = append(q.Repos, value)
				}
			case "author":
				q.Author = value
			case "committer":
				q.Committer = value
			case "author-name":
				q.AuthorName = value
			case "author-email":
				q.AuthorEmail = value
			case "committer-name":
				q.CommitterName = value
			case "committer-email":
				q.CommitterEmail = value
			case "hash":
				q.Hash = value
			case "parent":
				q.Parent = value
			case "tree":
				q.Tree = value
			case "author-date":
				q.AuthorDate = value
			case "committer-date":
				q.CommitterDate = value
			case "is":
				if strings.ToLower(value) == "merge" {
					m := true
					q.Merge = &m
				}
			case "merge":
				// merge:true or merge:false
				b := strings.ToLower(value) == "true" || value == ""
				q.Merge = &b
			case "org":
				q.Org = value
			case "user":
				q.User = value
			case "visibility":
				q.Visibility = strings.ToLower(value)
			}
			continue
		}

		if !negated {
			q.FreeText = append(q.FreeText, strings.Trim(token, "\""))
		}
	}
	return q
}

// codeQualifierKeys lists qualifiers recognized by code search.
var codeQualifierKeys = map[string]struct{}{
	"repo":      {},
	"filename":  {},
	"extension": {},
	"path":      {},
	"language":  {},
}

// ParseCodeSearchQuery parses a code search query, extracting free text
// while recognizing code-specific qualifiers so they don't pollute the
// keyword search.
func ParseCodeSearchQuery(query string) CodeSearchQuery {
	var q CodeSearchQuery
	tokens := tokenizeSearchQuery(query)
	for _, token := range tokens {
		negated := false
		inner := token
		if strings.HasPrefix(token, "-") && len(token) > 1 && strings.Contains(token, ":") {
			negated = true
			inner = token[1:]
		}

		parts := strings.SplitN(inner, ":", 2)
		if len(parts) != 2 {
			q.FreeText = append(q.FreeText, strings.Trim(token, "\""))
			continue
		}

		key := strings.ToLower(parts[0])
		value := strings.Trim(parts[1], "\"")
		if _, ok := codeQualifierKeys[key]; ok {
			q.HasQualifiers = true
			if negated {
				q.NegatedQualifiers = append(q.NegatedQualifiers, token)
				continue
			}
			switch key {
			case "repo":
				if value != "" {
					q.Repo = value
					q.Repos = append(q.Repos, value)
				}
			case "filename":
				q.Filename = value
			case "extension":
				// extension: can be comma-separated
				for _, ext := range strings.Split(value, ",") {
					ext := strings.TrimSpace(ext)
					if ext != "" && !strings.HasPrefix(ext, ".") {
						ext = "." + ext
					}
					q.Extensions = append(q.Extensions, ext)
				}
			case "path":
				q.Path = value
			case "language":
				q.Language = strings.ToLower(value)
			}
			continue
		}

		if !negated {
			q.FreeText = append(q.FreeText, strings.Trim(token, "\""))
		}
	}
	return q
}

// GetExtensionsForLanguage returns file extensions for a given language.
func GetExtensionsForLanguage(language string) []string {
	return languageExtensions[strings.ToLower(language)]
}

// parseNumber parses a string as an integer, returning the number and nil error on success.
func parseNumber(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

// tokenizeSearchQuery splits a GitHub-style search query into tokens,
// keeping quoted values together (e.g. label:"bug fix" → one token).
// Unterminated quotes are treated as if closed at end-of-string to prevent
// qualifier swallowing (e.g. `label:"bug state:open` would otherwise merge
// into a single token, losing the state: qualifier).
func tokenizeSearchQuery(query string) []string {
	tokens := make([]string, 0, 8)
	var current strings.Builder
	inQuote := false

	for i := 0; i < len(query); i++ {
		ch := query[i]
		switch {
		case ch == '"':
			inQuote = !inQuote
			current.WriteByte(ch)
		case ch == ' ' && !inQuote:
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	// If a quote was never closed, re-tokenize the merged tail token by
	// splitting on spaces. This prevents qualifiers after an unterminated
	// quote from being silently swallowed into a single token.
	if inQuote && len(tokens) > 0 {
		last := tokens[len(tokens)-1]
		// Strip the orphan opening quote and re-split.
		last = strings.ReplaceAll(last, "\"", "")
		parts := strings.Fields(last)
		if len(parts) > 0 {
			tokens = append(tokens[:len(tokens)-1], parts...)
		}
	}
	return tokens
}
