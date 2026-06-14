package search

import (
	"sort"
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
)

const (
	searchPathQualifierOnly = "qualifier_only"
	searchPathLexicalOnly   = "lexical_only"
	searchPathSemanticOnly  = "semantic_only"
	searchPathHybrid        = "hybrid"

	reciprocalRankConstant = 60.0
)

// SearchOptions controls optional search observability payloads.
type SearchOptions struct {
	IncludeTextMatches bool
}

// TextMatchSpan points to a matched term inside a TextMatch fragment.
type TextMatchSpan struct {
	Text    string
	Indices []int
}

// TextMatch mirrors GitHub's text_matches shape closely enough for search
// clients that need snippets without changing default REST responses.
type TextMatch struct {
	ObjectType string
	Property   string
	Fragment   string
	Matches    []TextMatchSpan
}

// IssueSearchResult carries the issue plus ranking data used to explain search.
type IssueSearchResult struct {
	Issue            db.Issue
	Score            float64
	LexicalRank      int
	SemanticRank     int
	LexicalScore     float64
	SemanticDistance *float64
	MatchedFields    []string
	TextMatches      []TextMatch
	SearchPath       string
}

// PRSearchResult carries the pull request plus ranking data used to explain search.
type PRSearchResult struct {
	PullRequest      db.PullRequest
	Score            float64
	LexicalRank      int
	SemanticRank     int
	LexicalScore     float64
	SemanticDistance *float64
	MatchedFields    []string
	TextMatches      []TextMatch
	SearchPath       string
}

type rankedSearchID struct {
	ID       uint
	Score    float64
	Distance *float64
}

type detailedSearchRank struct {
	ID               uint
	Score            float64
	LexicalRank      int
	SemanticRank     int
	LexicalScore     float64
	SemanticDistance *float64
	SearchPath       string
}

type searchCommentKey struct {
	RepositoryID uint
	IssueNumber  int
}

func issueIDsToRankedSearchIDs(ids []uint) []rankedSearchID {
	out := make([]rankedSearchID, 0, len(ids))
	for idx, id := range ids {
		out = append(out, rankedSearchID{
			ID:    id,
			Score: reciprocalRankScore(idx + 1),
		})
	}
	return out
}

func prsToRankedSearchIDs(prs []db.PullRequest) []rankedSearchID {
	out := make([]rankedSearchID, 0, len(prs))
	for idx, pr := range prs {
		out = append(out, rankedSearchID{
			ID:    pr.ID,
			Score: reciprocalRankScore(idx + 1),
		})
	}
	return out
}

func reciprocalRankScore(rank int) float64 {
	if rank <= 0 {
		return 0
	}
	return 1.0 / (reciprocalRankConstant + float64(rank))
}

func fuseDetailedSearchRanks(lexical, semantic []rankedSearchID, explicitSort bool, limit int) []detailedSearchRank {
	if explicitSort {
		return deduplicateDetailedSearchRanks(lexical, semantic, limit)
	}

	type aggregate struct {
		id               uint
		score            float64
		lexicalRank      int
		semanticRank     int
		lexicalScore     float64
		semanticDistance *float64
	}
	byID := make(map[uint]*aggregate, len(lexical)+len(semantic))
	for idx, entry := range lexical {
		rank := idx + 1
		item := byID[entry.ID]
		if item == nil {
			item = &aggregate{id: entry.ID}
			byID[entry.ID] = item
		}
		score := entry.Score
		if score == 0 {
			score = reciprocalRankScore(rank)
		}
		item.score += reciprocalRankScore(rank)
		item.lexicalRank = rank
		item.lexicalScore = score
	}
	for idx, entry := range semantic {
		rank := idx + 1
		item := byID[entry.ID]
		if item == nil {
			item = &aggregate{id: entry.ID}
			byID[entry.ID] = item
		}
		item.score += reciprocalRankScore(rank)
		item.semanticRank = rank
		item.semanticDistance = entry.Distance
	}

	ranked := make([]detailedSearchRank, 0, len(byID))
	for _, item := range byID {
		ranked = append(ranked, detailedSearchRank{
			ID:               item.id,
			Score:            item.score,
			LexicalRank:      item.lexicalRank,
			SemanticRank:     item.semanticRank,
			LexicalScore:     item.lexicalScore,
			SemanticDistance: item.semanticDistance,
			SearchPath:       searchPathForRanks(item.lexicalRank, item.semanticRank),
		})
	}
	sortDetailedSearchRanks(ranked)
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked
}

func deduplicateDetailedSearchRanks(primary, secondary []rankedSearchID, limit int) []detailedSearchRank {
	seen := make(map[uint]int, len(primary)+len(secondary))
	out := make([]detailedSearchRank, 0, len(primary)+len(secondary))
	for idx, entry := range primary {
		if _, ok := seen[entry.ID]; ok {
			continue
		}
		rank := idx + 1
		score := entry.Score
		if score == 0 {
			score = reciprocalRankScore(rank)
		}
		seen[entry.ID] = len(out)
		out = append(out, detailedSearchRank{
			ID:           entry.ID,
			Score:        score,
			LexicalRank:  rank,
			LexicalScore: score,
			SearchPath:   searchPathLexicalOnly,
		})
		if limit > 0 && len(out) >= limit {
			return out
		}
	}
	for idx, entry := range secondary {
		if seenIdx, ok := seen[entry.ID]; ok {
			out[seenIdx].SemanticRank = idx + 1
			out[seenIdx].SemanticDistance = entry.Distance
			out[seenIdx].SearchPath = searchPathForRanks(out[seenIdx].LexicalRank, out[seenIdx].SemanticRank)
			continue
		}
		rank := idx + 1
		seen[entry.ID] = len(out)
		out = append(out, detailedSearchRank{
			ID:               entry.ID,
			Score:            reciprocalRankScore(rank),
			SemanticRank:     rank,
			SemanticDistance: entry.Distance,
			SearchPath:       searchPathSemanticOnly,
		})
		if limit > 0 && len(out) >= limit {
			return out
		}
	}
	return out
}

func sortDetailedSearchRanks(ranked []detailedSearchRank) {
	for i := 1; i < len(ranked); i++ {
		current := ranked[i]
		j := i - 1
		for ; j >= 0 && detailedSearchRankLess(current, ranked[j]); j-- {
			ranked[j+1] = ranked[j]
		}
		ranked[j+1] = current
	}
}

func detailedSearchRankLess(left, right detailedSearchRank) bool {
	if left.Score == right.Score {
		leftLexical := left.LexicalRank > 0
		rightLexical := right.LexicalRank > 0
		if leftLexical != rightLexical {
			return leftLexical
		}
		leftSemantic := left.SemanticRank > 0
		rightSemantic := right.SemanticRank > 0
		if leftSemantic != rightSemantic {
			return leftSemantic
		}
		return left.ID > right.ID
	}
	return left.Score > right.Score
}

func searchPathForRanks(lexicalRank, semanticRank int) string {
	switch {
	case lexicalRank > 0 && semanticRank > 0:
		return searchPathHybrid
	case lexicalRank > 0:
		return searchPathLexicalOnly
	case semanticRank > 0:
		return searchPathSemanticOnly
	default:
		return searchPathQualifierOnly
	}
}

func ranksToIDs(ranks []detailedSearchRank) []uint {
	ids := make([]uint, 0, len(ranks))
	for _, rank := range ranks {
		ids = append(ids, rank.ID)
	}
	return ids
}

func issueResultsFromRanksWithComments(
	issues []db.Issue,
	ranks []detailedSearchRank,
	sq SearchQualifiers,
	opts SearchOptions,
	commentBodies map[searchCommentKey][]string,
) []IssueSearchResult {
	byID := make(map[uint]db.Issue, len(issues))
	for _, issue := range issues {
		byID[issue.ID] = issue
	}
	results := make([]IssueSearchResult, 0, len(ranks))
	for _, rank := range ranks {
		issue, ok := byID[rank.ID]
		if !ok {
			continue
		}
		matchedFields, textMatches := issueSearchTextMatches(
			issue,
			sq,
			opts.IncludeTextMatches,
			commentBodies[searchCommentKey{RepositoryID: issue.RepositoryID, IssueNumber: issue.Number}],
		)
		results = append(results, IssueSearchResult{
			Issue:            issue,
			Score:            rank.Score,
			LexicalRank:      rank.LexicalRank,
			SemanticRank:     rank.SemanticRank,
			LexicalScore:     rank.LexicalScore,
			SemanticDistance: rank.SemanticDistance,
			MatchedFields:    matchedFields,
			TextMatches:      textMatches,
			SearchPath:       rank.SearchPath,
		})
	}
	return results
}

func prResultsFromRanksWithComments(
	prs []db.PullRequest,
	ranks []detailedSearchRank,
	sq SearchQualifiers,
	opts SearchOptions,
	commentBodies map[searchCommentKey][]string,
) []PRSearchResult {
	byID := make(map[uint]db.PullRequest, len(prs))
	for _, pr := range prs {
		byID[pr.ID] = pr
	}
	results := make([]PRSearchResult, 0, len(ranks))
	for _, rank := range ranks {
		pr, ok := byID[rank.ID]
		if !ok {
			continue
		}
		matchedFields, textMatches := prSearchTextMatches(
			pr,
			sq,
			opts.IncludeTextMatches,
			commentBodies[searchCommentKey{RepositoryID: pr.RepositoryID, IssueNumber: pr.Number}],
		)
		results = append(results, PRSearchResult{
			PullRequest:      pr,
			Score:            rank.Score,
			LexicalRank:      rank.LexicalRank,
			SemanticRank:     rank.SemanticRank,
			LexicalScore:     rank.LexicalScore,
			SemanticDistance: rank.SemanticDistance,
			MatchedFields:    matchedFields,
			TextMatches:      textMatches,
			SearchPath:       rank.SearchPath,
		})
	}
	return results
}

func issueQualifierOnlyResults(issues []db.Issue, sq SearchQualifiers, opts SearchOptions) []IssueSearchResult {
	results := make([]IssueSearchResult, 0, len(issues))
	for _, issue := range issues {
		matchedFields, textMatches := issueSearchTextMatches(issue, sq, opts.IncludeTextMatches, nil)
		results = append(results, IssueSearchResult{
			Issue:         issue,
			Score:         1,
			MatchedFields: matchedFields,
			TextMatches:   textMatches,
			SearchPath:    searchPathQualifierOnly,
		})
	}
	return results
}

func prQualifierOnlyResults(prs []db.PullRequest, sq SearchQualifiers, opts SearchOptions) []PRSearchResult {
	results := make([]PRSearchResult, 0, len(prs))
	for _, pr := range prs {
		matchedFields, textMatches := prSearchTextMatches(pr, sq, opts.IncludeTextMatches, nil)
		results = append(results, PRSearchResult{
			PullRequest:   pr,
			Score:         1,
			MatchedFields: matchedFields,
			TextMatches:   textMatches,
			SearchPath:    searchPathQualifierOnly,
		})
	}
	return results
}

func issueSearchTextMatches(issue db.Issue, sq SearchQualifiers, includeTextMatches bool, commentBodies []string) ([]string, []TextMatch) {
	fields := searchTextFieldsForIssue(issue, sq.In, commentBodies)
	return searchTextMatches(fields, sq.FreeText, "Issue", includeTextMatches)
}

func prSearchTextMatches(pr db.PullRequest, sq SearchQualifiers, includeTextMatches bool, commentBodies []string) ([]string, []TextMatch) {
	fields := searchTextFieldsForPR(pr, sq.In, commentBodies)
	return searchTextMatches(fields, sq.FreeText, "PullRequest", includeTextMatches)
}

type searchTextField struct {
	name             string
	values           []string
	allowTextMatches bool
}

func searchTextFieldsForIssue(issue db.Issue, inValues []string, commentBodies []string) []searchTextField {
	searchTitle, searchBody, searchComments := resolveTextSearchTargets(inValues)
	fields := make([]searchTextField, 0, 3)
	if searchTitle {
		fields = append(fields, searchTextField{name: "title", values: []string{issue.Title}, allowTextMatches: true})
	}
	if searchBody {
		fields = append(fields, searchTextField{name: "body", values: []string{string(issue.Body)}, allowTextMatches: true})
	}
	if searchComments {
		fields = append(fields, searchTextField{name: "comments", values: commentBodies, allowTextMatches: true})
	}
	return fields
}

func searchTextFieldsForPR(pr db.PullRequest, inValues []string, commentBodies []string) []searchTextField {
	defaultAll, searchTitle, searchBody, searchComments := resolvePRLexicalTargets(inValues)
	fields := make([]searchTextField, 0, 5)
	if searchTitle {
		fields = append(fields, searchTextField{name: "title", values: []string{pr.Title}, allowTextMatches: true})
	}
	if searchBody {
		fields = append(fields, searchTextField{name: "body", values: []string{string(pr.Body)}, allowTextMatches: true})
	}
	if searchComments {
		fields = append(fields, searchTextField{name: "comments", values: commentBodies, allowTextMatches: true})
	}
	if defaultAll {
		fields = append(fields,
			searchTextField{name: "commit_messages", values: []string{pr.CommitMessages}},
			searchTextField{name: "filenames", values: []string{pr.Filenames}},
		)
	}
	return fields
}

func searchTextMatches(fields []searchTextField, terms []string, objectType string, includeTextMatches bool) ([]string, []TextMatch) {
	normalizedTerms := normalizeSearchTerms(terms)
	if len(normalizedTerms) == 0 {
		return nil, nil
	}
	matchedFields := make([]string, 0, len(fields))
	textMatches := make([]TextMatch, 0, len(fields))
	for _, field := range fields {
		if !fieldMatchesAnyTerm(field.values, normalizedTerms) {
			continue
		}
		matchedFields = append(matchedFields, field.name)
		if !includeTextMatches || !field.allowTextMatches {
			continue
		}
		textMatches = append(textMatches, buildTextMatchesForField(field, normalizedTerms, objectType)...)
	}
	return matchedFields, textMatches
}

func normalizeSearchTerms(terms []string) []string {
	out := make([]string, 0, len(terms))
	seen := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(strings.Trim(term, `"'`))
		if term == "" {
			continue
		}
		key := strings.ToLower(term)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, term)
	}
	return out
}

func fieldMatchesAnyTerm(values []string, terms []string) bool {
	for _, value := range values {
		value = strings.ToLower(value)
		for _, term := range terms {
			if strings.Contains(value, strings.ToLower(term)) {
				return true
			}
		}
	}
	return false
}

func buildTextMatchesForField(field searchTextField, terms []string, objectType string) []TextMatch {
	matches := make([]TextMatch, 0, len(field.values))
	for _, value := range field.values {
		match, ok := buildTextMatch(field.name, value, terms, objectType)
		if !ok {
			continue
		}
		matches = append(matches, match)
	}
	return matches
}

type textMatchLocation struct {
	start int
	end   int
}

func buildTextMatch(property string, value string, terms []string, objectType string) (TextMatch, bool) {
	if value == "" {
		return TextMatch{}, false
	}
	valueLower := strings.ToLower(value)
	locations := make([]textMatchLocation, 0, len(terms))
	for _, term := range terms {
		termLower := strings.ToLower(term)
		searchFrom := 0
		for {
			idx := strings.Index(valueLower[searchFrom:], termLower)
			if idx < 0 {
				break
			}
			start := searchFrom + idx
			end := start + len(termLower)
			locations = append(locations, textMatchLocation{start: start, end: end})
			searchFrom = end
		}
	}
	if len(locations) == 0 {
		return TextMatch{}, false
	}
	sort.Slice(locations, func(i, j int) bool {
		if locations[i].start == locations[j].start {
			return locations[i].end < locations[j].end
		}
		return locations[i].start < locations[j].start
	})

	const before = 48
	const after = 96
	firstStart := locations[0].start
	lastEnd := locations[len(locations)-1].end
	fragmentStart := firstStart - before
	if fragmentStart < 0 {
		fragmentStart = 0
	}
	fragmentEnd := lastEnd + after
	if fragmentEnd > len(value) {
		fragmentEnd = len(value)
	}
	fragment := value[fragmentStart:fragmentEnd]

	spans := make([]TextMatchSpan, 0, len(locations))
	for _, location := range locations {
		if location.start < fragmentStart || location.end > fragmentEnd {
			continue
		}
		start := location.start - fragmentStart
		end := location.end - fragmentStart
		spans = append(spans, TextMatchSpan{
			Text:    fragment[start:end],
			Indices: []int{start, end},
		})
	}
	if len(spans) == 0 {
		return TextMatch{}, false
	}
	return TextMatch{
		ObjectType: objectType,
		Property:   property,
		Fragment:   fragment,
		Matches:    spans,
	}, true
}

func loadCommentBodiesForIssues(database *gorm.DB, issues []db.Issue) (map[searchCommentKey][]string, error) {
	keys := make([]searchCommentKey, 0, len(issues))
	for _, issue := range issues {
		keys = append(keys, searchCommentKey{RepositoryID: issue.RepositoryID, IssueNumber: issue.Number})
	}
	return loadCommentBodies(database, keys)
}

func loadCommentBodiesForPRs(database *gorm.DB, prs []db.PullRequest) (map[searchCommentKey][]string, error) {
	keys := make([]searchCommentKey, 0, len(prs))
	for _, pr := range prs {
		keys = append(keys, searchCommentKey{RepositoryID: pr.RepositoryID, IssueNumber: pr.Number})
	}
	return loadCommentBodies(database, keys)
}

func loadCommentBodies(database *gorm.DB, keys []searchCommentKey) (map[searchCommentKey][]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	issueNumbersByRepo := make(map[uint]map[int]struct{}, len(keys))
	for _, key := range keys {
		if _, ok := issueNumbersByRepo[key.RepositoryID]; !ok {
			issueNumbersByRepo[key.RepositoryID] = make(map[int]struct{})
		}
		issueNumbersByRepo[key.RepositoryID][key.IssueNumber] = struct{}{}
	}

	q := database.Session(&gorm.Session{NewDB: true}).Model(&db.IssueComment{})
	firstClause := true
	for repoID, numberSet := range issueNumbersByRepo {
		issueNumbers := make([]int, 0, len(numberSet))
		for number := range numberSet {
			issueNumbers = append(issueNumbers, number)
		}
		if firstClause {
			q = q.Where("(repository_id = ? AND issue_number IN ?)", repoID, issueNumbers)
			firstClause = false
			continue
		}
		q = q.Or("(repository_id = ? AND issue_number IN ?)", repoID, issueNumbers)
	}

	var rows []struct {
		RepositoryID uint
		IssueNumber  int
		Body         string
	}
	if err := q.Select("repository_id", "issue_number", "body").Find(&rows).Error; err != nil {
		return nil, err
	}

	out := make(map[searchCommentKey][]string, len(rows))
	for _, row := range rows {
		key := searchCommentKey{RepositoryID: row.RepositoryID, IssueNumber: row.IssueNumber}
		out[key] = append(out[key], row.Body)
	}
	return out, nil
}
