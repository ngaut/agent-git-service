package search

import (
	"context"
	"log/slog"
	"sort"
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IssueSearchDeps groups dependencies for issue search.
type IssueSearchDeps struct {
	DBForCtx           func(context.Context) *gorm.DB
	PreloadIssue       func(*gorm.DB) *gorm.DB
	DefaultListLimit   int
	IssueSortQualifier func(sort, order string) string
	EmbedQuery         func(context.Context, string) string
	UserFromContext    func(context.Context) (db.User, bool)
}

// IssueListDeps groups dependencies for filtered issue listing.
type IssueListDeps struct {
	DBForCtx                  func(context.Context) *gorm.DB
	PreloadIssue              func(*gorm.DB) *gorm.DB
	DefaultListLimit          int
	IssueSortQualifier        func(sort, order string) string
	GetRepo                   func(context.Context, string) (db.Repository, error)
	ApplyIssueSinceFilter     func(*gorm.DB, string) (*gorm.DB, error)
	ApplyIssueMilestoneFilter func(*gorm.DB, *gorm.DB, uint, string) (*gorm.DB, bool)
}

// SearchIssues performs a hybrid search on the issues table.
func SearchIssues(ctx context.Context, deps IssueSearchDeps, query string) ([]db.Issue, error) {
	results, err := SearchIssuesDetailed(ctx, deps, query, SearchOptions{})
	if err != nil {
		return nil, err
	}
	issues := make([]db.Issue, 0, len(results))
	for _, result := range results {
		issues = append(issues, result.Issue)
	}
	return issues, nil
}

// SearchIssuesDetailed performs issue search and returns ranking observability.
func SearchIssuesDetailed(ctx context.Context, deps IssueSearchDeps, query string, opts SearchOptions) ([]IssueSearchResult, error) {
	if query == "" {
		return nil, nil
	}

	sq := ParseSearchQuery(query)
	sortQualifier := sq.Sort
	if sq.Order != "" && deps.IssueSortQualifier != nil {
		sortQualifier = deps.IssueSortQualifier(sq.Sort, sq.Order)
	}
	explicitSort := sq.Sort != "" || sq.Order != ""

	baseDB := deps.DBForCtx(ctx)
	if len(sq.FreeText) == 0 {
		// No free text — just apply qualifiers and return.
		q := buildIssueSearchBaseQuery(ctx, deps, baseDB, sq, true)
		var issues []db.Issue
		err := q.Order(SortOrder(sortQualifier, "issues")).Limit(deps.DefaultListLimit).Find(&issues).Error
		if err != nil {
			return nil, err
		}
		return issueQualifierOnlyResults(issues, sq, opts), nil
	}

	freeText := strings.Join(sq.FreeText, " ")
	searchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type lexicalResult struct {
		ids []uint
		err error
	}
	type semanticResult struct {
		ranks []rankedSearchID
		err   error
	}

	lexicalCh := make(chan lexicalResult, 1)
	semanticCh := make(chan semanticResult, 1)

	go func() {
		baseQ := buildIssueSearchBaseQuery(searchCtx, deps, baseDB, sq, false)
		ids, err := searchIssueLexical(searchCtx, baseQ, baseDB, sq, sortQualifier, explicitSort, deps.DefaultListLimit)
		if err != nil {
			cancel()
		}
		lexicalCh <- lexicalResult{ids: ids, err: err}
	}()

	go func() {
		vec := ""
		if deps.EmbedQuery != nil {
			vec = deps.EmbedQuery(searchCtx, freeText)
		}
		if vec == "" {
			semanticCh <- semanticResult{}
			return
		}
		baseQ := buildIssueSearchBaseQuery(searchCtx, deps, baseDB, sq, false)
		ranks, err := searchIssueSemanticDetailed(searchCtx, baseQ, baseDB, sq, vec, deps.DefaultListLimit)
		semanticCh <- semanticResult{ranks: ranks, err: err}
	}()

	lexical := <-lexicalCh
	if lexical.err != nil {
		cancel()
		<-semanticCh
		return nil, lexical.err
	}
	semantic := <-semanticCh

	if semantic.err != nil {
		slog.WarnContext(ctx, "issue search vector query failed; returning lexical results only", "error", semantic.err)
		ranks := fuseDetailedSearchRanks(issueIDsToRankedSearchIDs(lexical.ids), nil, explicitSort, deps.DefaultListLimit)
		return buildIssueDetailedResults(ctx, deps, baseDB, ranks, sq, opts)
	}

	// Merge lexical and semantic ranks for default "best match" search.
	// Preserve explicit user-requested sort semantics when sort/order qualifiers are present.
	ranks := fuseDetailedSearchRanks(issueIDsToRankedSearchIDs(lexical.ids), semantic.ranks, explicitSort, deps.DefaultListLimit)
	return buildIssueDetailedResults(ctx, deps, baseDB, ranks, sq, opts)
}

func buildIssueSearchBaseQuery(ctx context.Context, deps IssueSearchDeps, baseDB *gorm.DB, sq SearchQualifiers, preload bool) *gorm.DB {
	q := baseDB.Model(&db.Issue{})
	if preload && deps.PreloadIssue != nil {
		q = deps.PreloadIssue(q)
	}
	q = ApplyIssueQualifiers(q, baseDB, sq)
	// Skip discoverability filter when explicit repo: qualifier is provided.
	if sq.Repo == "" {
		q = restrictIssuesToDiscoverableRepos(ctx, deps, baseDB, q)
	}
	return q
}

func buildIssueDetailedResults(
	ctx context.Context,
	deps IssueSearchDeps,
	baseDB *gorm.DB,
	ranks []detailedSearchRank,
	sq SearchQualifiers,
	opts SearchOptions,
) ([]IssueSearchResult, error) {
	issues, err := preloadIssuesByID(ctx, deps, ranksToIDs(ranks))
	if err != nil {
		return nil, err
	}
	_, _, searchComments := resolveTextSearchTargets(sq.In)
	commentBodies := map[searchCommentKey][]string(nil)
	if searchComments {
		commentBodies, err = loadCommentBodiesForIssues(baseDB, issues)
		if err != nil {
			return nil, err
		}
	}
	return issueResultsFromRanksWithComments(issues, ranks, sq, opts, commentBodies), nil
}

func searchIssueLexical(ctx context.Context, baseQ *gorm.DB, baseDB *gorm.DB, sq SearchQualifiers, sortQualifier string, explicitSort bool, limit int) ([]uint, error) {
	if !supportsTiDBFullText(baseDB) {
		return searchIssueLikeFallback(baseQ, sq, sortQualifier, limit)
	}

	qLexical := baseQ.Session(&gorm.Session{NewDB: false})
	tokenGroups := make([][]lexicalTokenQuery, 0, len(sq.FreeText))
	for _, token := range sq.FreeText {
		tokenGroups = append(tokenGroups, buildIssueLexicalTokenQueries(sq.In, token, true))
	}

	issueIDs, err := collectLexicalMatchIDs(qLexical, tokenGroups, "issues", sortQualifier, explicitSort, limit)
	if err != nil {
		slog.WarnContext(ctx, "issue search TiDB full-text query failed; falling back to LIKE", "error", err)
		return searchIssueLikeFallback(baseQ, sq, sortQualifier, limit)
	}
	return issueIDs, nil
}

func searchIssueLikeFallback(baseQ *gorm.DB, sq SearchQualifiers, sortQualifier string, limit int) ([]uint, error) {
	qLike := baseQ.Session(&gorm.Session{NewDB: false})
	qLike = applyTextTokens(qLike, sq.FreeText, func(text string) (string, []any) {
		return BuildIssueTextWhere(sq.In, text)
	})
	qLike = qLike.Order(SortOrder(sortQualifier, "issues"))
	return pluckIssueIDs(qLike, limit)
}

func searchIssueSemantic(ctx context.Context, baseQ *gorm.DB, baseDB *gorm.DB, sq SearchQualifiers, vec string, limit int) ([]uint, error) {
	if !supportsVectorDistance(baseDB) {
		return nil, nil
	}
	if !supportsTiDBANN(baseDB) {
		return searchIssueSemanticFiltered(baseQ, vec, limit)
	}

	switch semanticModeForQuery(sq, issueSemanticApplicable(sq.In)) {
	case semanticModeDisabled:
		return nil, nil
	case semanticModeFilteredExact:
		return searchIssueSemanticFiltered(baseQ, vec, limit)
	default:
		return searchIssueSemanticANN(ctx, baseQ, baseDB, vec, limit)
	}
}

func searchIssueSemanticDetailed(ctx context.Context, baseQ *gorm.DB, baseDB *gorm.DB, sq SearchQualifiers, vec string, limit int) ([]rankedSearchID, error) {
	if !supportsVectorDistance(baseDB) {
		return nil, nil
	}
	if !supportsTiDBANN(baseDB) {
		return searchIssueSemanticFilteredDetailed(baseQ, vec, limit)
	}

	switch semanticModeForQuery(sq, issueSemanticApplicable(sq.In)) {
	case semanticModeDisabled:
		return nil, nil
	case semanticModeFilteredExact:
		return searchIssueSemanticFilteredDetailed(baseQ, vec, limit)
	default:
		ids, err := searchIssueSemanticANN(ctx, baseQ, baseDB, vec, limit)
		if err != nil {
			return nil, err
		}
		return issueIDsToRankedSearchIDs(ids), nil
	}
}

func buildIssueANNCandidateQuery(baseDB *gorm.DB, vec string, limit int) *gorm.DB {
	// Keep the ANN candidate query free of WHERE predicates so TiDB can use
	// the vector index for ORDER BY ... LIMIT candidate generation.
	return baseDB.Table("issues").
		Clauses(clause.OrderBy{
			Expression: clause.Expr{SQL: "VEC_COSINE_DISTANCE(issues.embedding, ?) ASC", Vars: []any{vec}},
		}).
		Limit(semanticCandidateLimit(limit))
}

func searchIssueSemanticANN(_ context.Context, baseQ *gorm.DB, baseDB *gorm.DB, vec string, limit int) ([]uint, error) {
	var candidateIDs []uint
	if err := buildIssueANNCandidateQuery(baseDB, vec, limit).Pluck("issues.id", &candidateIDs).Error; err != nil {
		return nil, err
	}
	if len(candidateIDs) == 0 {
		return nil, nil
	}

	qVec := baseQ.Session(&gorm.Session{NewDB: false}).Where("issues.id IN ?", candidateIDs)
	qVec = qVec.Clauses(clause.OrderBy{Expression: orderByIDCaseExpr("issues", candidateIDs)})

	return pluckIssueIDs(qVec, limit)
}

func buildIssueSemanticFilteredQuery(baseQ *gorm.DB, vec string) *gorm.DB {
	return baseQ.Session(&gorm.Session{NewDB: false}).
		Where("issues.embedding IS NOT NULL").
		Clauses(clause.OrderBy{
			Expression: clause.Expr{SQL: "VEC_COSINE_DISTANCE(issues.embedding, ?) ASC", Vars: []any{vec}},
		})
}

func buildIssueSemanticFilteredRankQuery(baseQ *gorm.DB, vec string) *gorm.DB {
	return buildIssueSemanticFilteredQuery(baseQ, vec).Select("issues.id")
}

func searchIssueSemanticFiltered(baseQ *gorm.DB, vec string, limit int) ([]uint, error) {
	return pluckIssueIDs(buildIssueSemanticFilteredRankQuery(baseQ, vec), limit)
}

func searchIssueSemanticFilteredDetailed(baseQ *gorm.DB, vec string, limit int) ([]rankedSearchID, error) {
	q := buildIssueSemanticFilteredQuery(baseQ, vec).
		Select("issues.id, VEC_COSINE_DISTANCE(issues.embedding, ?) AS semantic_distance", vec)
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []struct {
		ID               uint
		SemanticDistance float64 `gorm:"column:semantic_distance"`
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	results := make([]rankedSearchID, 0, len(rows))
	for _, row := range rows {
		distance := row.SemanticDistance
		results = append(results, rankedSearchID{
			ID:       row.ID,
			Score:    reciprocalRankScore(len(results) + 1),
			Distance: &distance,
		})
	}
	return results, nil
}

func pluckIssueIDs(q *gorm.DB, limit int) ([]uint, error) {
	if limit > 0 {
		q = q.Limit(limit)
	}
	var issueIDs []uint
	if err := q.Pluck("issues.id", &issueIDs).Error; err != nil {
		return nil, err
	}
	return issueIDs, nil
}

func preloadIssuesByID(ctx context.Context, deps IssueSearchDeps, issueIDs []uint) ([]db.Issue, error) {
	if len(issueIDs) == 0 {
		return nil, nil
	}
	if deps.DefaultListLimit > 0 && len(issueIDs) > deps.DefaultListLimit {
		issueIDs = issueIDs[:deps.DefaultListLimit]
	}

	q := deps.DBForCtx(ctx).Model(&db.Issue{}).
		Where("issues.id IN ?", issueIDs).
		Clauses(clause.OrderBy{Expression: orderByIDCaseExpr("issues", issueIDs)})
	if deps.PreloadIssue != nil {
		q = deps.PreloadIssue(q)
	}

	var issues []db.Issue
	if err := q.Find(&issues).Error; err != nil {
		return nil, err
	}
	return issues, nil
}

func deduplicateOrderedIssueIDs(primary, secondary []uint, limit int) []uint {
	seen := make(map[uint]struct{}, len(primary))
	for _, issueID := range primary {
		seen[issueID] = struct{}{}
	}
	result := make([]uint, len(primary), len(primary)+len(secondary))
	copy(result, primary)
	for _, issueID := range secondary {
		if _, ok := seen[issueID]; ok {
			continue
		}
		if limit > 0 && len(result) >= limit {
			break
		}
		seen[issueID] = struct{}{}
		result = append(result, issueID)
	}
	return result
}

func fuseIssueSearchResultIDs(lexical, semantic []uint, limit int) []uint {
	if len(lexical) == 0 {
		if limit > 0 && len(semantic) > limit {
			return semantic[:limit]
		}
		return semantic
	}
	if len(semantic) == 0 {
		if limit > 0 && len(lexical) > limit {
			return lexical[:limit]
		}
		return lexical
	}
	type issueRank struct {
		id          uint
		score       float64
		lexicalHit  bool
		semanticHit bool
	}
	const rankConstant = 60.0
	byID := make(map[uint]*issueRank, len(lexical)+len(semantic))
	for idx, issueID := range lexical {
		entry := byID[issueID]
		if entry == nil {
			entry = &issueRank{id: issueID}
			byID[issueID] = entry
		}
		entry.score += 1.0 / (rankConstant + float64(idx+1))
		entry.lexicalHit = true
	}
	for idx, issueID := range semantic {
		entry := byID[issueID]
		if entry == nil {
			entry = &issueRank{id: issueID}
			byID[issueID] = entry
		}
		entry.score += 1.0 / (rankConstant + float64(idx+1))
		entry.semanticHit = true
	}
	ranked := make([]issueRank, 0, len(byID))
	for _, entry := range byID {
		ranked = append(ranked, *entry)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			if ranked[i].lexicalHit != ranked[j].lexicalHit {
				return ranked[i].lexicalHit
			}
			if ranked[i].semanticHit != ranked[j].semanticHit {
				return ranked[i].semanticHit
			}
			return ranked[i].id > ranked[j].id
		}
		return ranked[i].score > ranked[j].score
	})
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]uint, 0, len(ranked))
	for _, entry := range ranked {
		out = append(out, entry.id)
	}
	return out
}

func fuseIssueSearchResults(lexical, semantic []db.Issue, limit int) []db.Issue {
	if len(lexical) == 0 {
		if limit > 0 && len(semantic) > limit {
			return semantic[:limit]
		}
		return semantic
	}
	if len(semantic) == 0 {
		if limit > 0 && len(lexical) > limit {
			return lexical[:limit]
		}
		return lexical
	}
	type issueRank struct {
		issue       db.Issue
		score       float64
		lexicalHit  bool
		semanticHit bool
	}
	const rankConstant = 60.0
	byID := make(map[uint]*issueRank, len(lexical)+len(semantic))
	for idx, issue := range lexical {
		entry := byID[issue.ID]
		if entry == nil {
			entry = &issueRank{issue: issue}
			byID[issue.ID] = entry
		}
		entry.score += 1.0 / (rankConstant + float64(idx+1))
		entry.lexicalHit = true
	}
	for idx, issue := range semantic {
		entry := byID[issue.ID]
		if entry == nil {
			entry = &issueRank{issue: issue}
			byID[issue.ID] = entry
		}
		entry.score += 1.0 / (rankConstant + float64(idx+1))
		entry.semanticHit = true
	}
	ranked := make([]issueRank, 0, len(byID))
	for _, entry := range byID {
		ranked = append(ranked, *entry)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			if ranked[i].lexicalHit != ranked[j].lexicalHit {
				return ranked[i].lexicalHit
			}
			if ranked[i].semanticHit != ranked[j].semanticHit {
				return ranked[i].semanticHit
			}
			return ranked[i].issue.ID > ranked[j].issue.ID
		}
		return ranked[i].score > ranked[j].score
	})
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]db.Issue, 0, len(ranked))
	for _, entry := range ranked {
		out = append(out, entry.issue)
	}
	return out
}

func restrictIssuesToDiscoverableRepos(ctx context.Context, deps IssueSearchDeps, baseDB *gorm.DB, q *gorm.DB) *gorm.DB {
	return q
}

// BuildIssueTextWhere builds the WHERE clause for issue text search based on the in: qualifier.
// inValues contains values like "title", "body", "comments".
// Default (empty) searches both title and body.
// Note: issue_comments table links to issues via (repository_id, issue_number), not issue_id.
func BuildIssueTextWhere(inValues []string, text string) (string, []any) {
	return buildTextWhere(textWhereContextForTable("issues"), inValues, text)
}

// BuildIssueInFilter builds a WHERE clause to filter results based on in: qualifier.
// This is used for vector search to ensure field constraints are enforced.
// Returns empty string and nil if no filtering is needed (default search).
func BuildIssueInFilter(inValues []string) (string, []any) {
	if len(inValues) == 0 {
		return "", nil
	}

	searchTitle := false
	searchBody := false
	for _, v := range inValues {
		switch strings.ToLower(v) {
		case "title":
			searchTitle = true
		case "body":
			searchBody = true
		case "comments":
			searchBody = true
		}
	}

	if searchTitle && searchBody {
		return "", nil // No filter needed, both fields allowed
	}
	if searchTitle {
		return "issues.title IS NOT NULL AND issues.title != ''", nil
	}
	if searchBody {
		return "issues.body IS NOT NULL AND issues.body != ''", nil
	}

	return "", nil
}

// ApplyIssueQualifiers applies all structured search qualifiers to an issue query.
// baseDB is a separate *gorm.DB handle used for subqueries (avoids clause contamination).
func ApplyIssueQualifiers(q *gorm.DB, baseDB *gorm.DB, sq SearchQualifiers) *gorm.DB {
	if sq.StateConflict {
		return q.Where("1 = 0")
	}
	if sq.IsPR && !sq.IsIssue {
		q = q.Where("1 = 0") // PR search on issue table returns nothing
	}
	repoScope, repoScopeStatus := resolveRepoSearchScope(baseDB, sq.Repo)
	q = applyRepoFilterWithScope(q, baseDB, "issues", sq.Repo, repoScope, repoScopeStatus)
	q = applyVisibilityQualifier(q, baseDB, "issues", sq.Visibility)
	q = applyForkQualifier(q, baseDB, "issues", sq.Fork)
	if sq.State != "" && sq.State != "all" {
		q = q.Where("issues.state = ?", sq.State)
	}
	// Number qualifier: #<number> searches by issue number
	if sq.Number != nil {
		q = q.Where("issues.number = ?", *sq.Number)
	}
	q = applyAuthorFilter(q, baseDB, "issues", sq.Author)
	q = applyAssigneeFilter(q, baseDB, "issues", sq.Assignee)
	// Positive label groups: each group is AND'd, within each group labels are OR'd.
	q = applyRepoScopedLabelGroups(q, baseDB, sq.Labels, "issue_labels", "issue_labels.issue_id", "issues.id", false, repoScope, repoScopeStatus)
	// Negated label groups
	q = applyRepoScopedLabelGroups(q, baseDB, sq.NegatedLabels, "issue_labels", "issue_labels.issue_id", "issues.id", true, repoScope, repoScopeStatus)
	// Negated author
	q = applyNegatedAuthorFilter(q, baseDB, "issues", sq.NegatedAuthor)
	// Negated assignee
	q = applyNegatedAssigneeFilter(q, baseDB, "issues", sq.NegatedAssignee)
	// Metadata: no:/has:
	q = applyMetadataFilters(q, baseDB, "issues", sq)
	// Milestone
	q = applyMilestoneQualifier(q, "issues", sq.Milestone)
	// Project
	q = applyProjectQualifier(q, baseDB, "issues", "Issue_", "ISSUE", sq.Project)
	// Language
	q = applyLanguageQualifier(q, baseDB, "issues", sq.Language)
	// Comments / reactions / interactions
	q = applyNumericRangeQualifier(q, issueCommentCountExpr("issues"), sq.Comments)
	q = applyNumericRangeQualifier(q, issueReactionCountExpr("issues"), sq.Reactions)
	q = applyNumericRangeQualifier(q, issueInteractionCountExpr("issues"), sq.Interactions)
	// Date ranges
	q = applyDateRangeQualifier(q, "issues.created_at", sq.Created)
	q = applyDateRangeQualifier(q, "issues.updated_at", sq.Updated)
	q = applyDateRangeQualifier(q, "issues.closed_at", sq.Closed)
	// Mentions
	q = applyMentionsQualifier(q, "issues", sq.Mentions)
	// Team involvement
	q = applyTeamQualifier(q, baseDB, "issues", sq.Team)
	// Linked PRs
	if strings.EqualFold(strings.TrimSpace(sq.Linked), "pr") {
		q = q.Where("EXISTS (" +
			"SELECT 1 FROM linked_branches lb " +
			"JOIN pull_requests pr ON pr.repository_id = lb.repository_id AND pr.head_ref = lb.branch_name " +
			"WHERE lb.issue_id = issues.id)")
	}
	// Locked / unlocked
	if sq.IsLocked != nil {
		q = q.Where("issues.locked = ?", *sq.IsLocked)
	}
	// CI status
	q = applyIssueStatusQualifier(q, sq.Status)
	// Reason
	if sq.Reason != "" {
		q = q.Where("LOWER(issues.state_reason) = LOWER(?)", sq.Reason)
	}
	// Involves: author OR assignee OR commenter
	if sq.Involves != "" {
		userIDSub := baseDB.Session(&gorm.Session{NewDB: true}).Table("users").Select("id").Where("login = ?", sq.Involves)
		assigneeCond, assigneeArgs := assigneeMatchCondition(sq.Involves)
		// Build assignee condition with proper table prefix
		assigneeWhere := strings.ReplaceAll(assigneeCond, "assignee_logins", "issues.assignee_logins")
		q = q.Where(
			"issues.author_id IN (?) OR "+assigneeWhere+" OR issues.id IN ("+
				"SELECT DISTINCT ic.id FROM issues ic "+
				"JOIN issue_comments icc ON icc.repository_id = ic.repository_id AND icc.issue_number = ic.number "+
				"JOIN users u ON u.id = icc.author_id WHERE u.login = ?)",
			append(append([]any{userIDSub}, assigneeArgs...), sq.Involves)...,
		)
	}
	// Commenter: filter issues where the specified user has commented
	if sq.Commenter != "" {
		userIDSub := baseDB.Session(&gorm.Session{NewDB: true}).Table("users").Select("id").Where("login = ?", sq.Commenter)
		q = q.Where("issues.id IN ("+
			"SELECT DISTINCT ic.id FROM issues ic "+
			"JOIN issue_comments icc ON icc.repository_id = ic.repository_id AND icc.issue_number = ic.number "+
			"JOIN users u ON u.id = icc.author_id WHERE u.login = ?)",
			sq.Commenter)
		_ = userIDSub
	}
	return q
}

// ListIssuesFiltered returns issues for a repo with filterBy support.
// Supports filtering by assignee login, mentioned user, and createdBy (author).
func ListIssuesFiltered(ctx context.Context, deps IssueListDeps, filter IssueListFilter) ([]db.Issue, error) {
	rep, err := deps.GetRepo(ctx, filter.RepoFullName)
	if err != nil {
		return nil, err
	}
	baseDB := deps.DBForCtx(ctx)
	q := deps.PreloadIssue(baseDB).Where("repository_id = ?", rep.ID)
	if filter.State != "all" && filter.State != "" {
		q = q.Where("state = ?", filter.State)
	}
	if filter.Assignee != "" {
		cond, args := assigneeMatchCondition(filter.Assignee)
		q = q.Where(cond, args...)
	}
	if filter.Mentioned != "" {
		q = applyMentionsQualifier(q, "issues", filter.Mentioned)
	}
	if filter.CreatedBy != "" {
		q = q.Where("issues.author_id IN (?)",
			baseDB.Session(&gorm.Session{NewDB: true}).Table("users").Select("id").Where("login = ?", filter.CreatedBy))
	}
	if filter.Labels != "" {
		var noResults bool
		q, noResults, err = ApplyRepoIssueLabelFilters(q, baseDB, rep.ID, filter.Labels)
		if err != nil {
			return nil, err
		}
		if noResults {
			return []db.Issue{}, nil
		}
	}
	q, err = deps.ApplyIssueSinceFilter(q, filter.Since)
	if err != nil {
		return nil, err
	}
	var noResults bool
	q, noResults = deps.ApplyIssueMilestoneFilter(q, baseDB, rep.ID, filter.Milestone)
	if noResults {
		return []db.Issue{}, nil
	}
	orderExpr := SortOrder(deps.IssueSortQualifier(filter.Sort, filter.Direction), "issues")
	if filter.Mentioned == "" {
		var issues []db.Issue
		if err := q.Order(orderExpr).Limit(deps.DefaultListLimit).Find(&issues).Error; err != nil {
			return nil, err
		}
		return issues, nil
	}
	batchSize := deps.DefaultListLimit
	if batchSize < 25 {
		batchSize = 25
	}
	var (
		offset  int
		matches []db.Issue
	)
	for len(matches) < deps.DefaultListLimit {
		var batch []db.Issue
		if err := q.Order(orderExpr).Limit(batchSize).Offset(offset).Find(&batch).Error; err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, issue := range batch {
			ok, err := issueMatchesMention(ctx, baseDB, issue, filter.Mentioned)
			if err != nil {
				return nil, err
			}
			if ok {
				matches = append(matches, issue)
				if len(matches) >= deps.DefaultListLimit {
					break
				}
			}
		}
		if len(batch) < batchSize {
			break
		}
		offset += len(batch)
	}
	return matches, nil
}
