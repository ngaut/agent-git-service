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

// PRSearchDeps groups dependencies for PR search.
type PRSearchDeps struct {
	DBForCtx           func(context.Context) *gorm.DB
	PreloadPR          func(*gorm.DB) *gorm.DB
	DefaultListLimit   int
	IssueSortQualifier func(sort, order string) string
	EmbedQuery         func(context.Context, string) string
	UserFromContext    func(context.Context) (db.User, bool)
}

// SearchPRs performs a hybrid search on the pull_requests table.
func SearchPRs(ctx context.Context, deps PRSearchDeps, query string) ([]db.PullRequest, error) {
	results, err := SearchPRsDetailed(ctx, deps, query, SearchOptions{})
	if err != nil {
		return nil, err
	}
	prs := make([]db.PullRequest, 0, len(results))
	for _, result := range results {
		prs = append(prs, result.PullRequest)
	}
	return prs, nil
}

// SearchPRsDetailed performs pull request search and returns ranking observability.
func SearchPRsDetailed(ctx context.Context, deps PRSearchDeps, query string, opts SearchOptions) ([]PRSearchResult, error) {
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
		q := ApplyPRQualifiers(deps.PreloadPR(baseDB), baseDB, sq)
		// Skip discoverability filter when explicit repo: qualifier is provided.
		if sq.Repo == "" {
			q = restrictPRsToDiscoverableRepos(ctx, deps, baseDB, q)
		}
		var prs []db.PullRequest
		err := q.Order(SortOrder(sortQualifier, "pull_requests")).Limit(deps.DefaultListLimit).Find(&prs).Error
		if err != nil {
			return nil, err
		}
		return prQualifierOnlyResults(prs, sq, opts), nil
	}

	freeText := strings.Join(sq.FreeText, " ")
	baseQ := ApplyPRQualifiers(deps.PreloadPR(baseDB), baseDB, sq)
	// Skip discoverability filter when explicit repo: qualifier is provided.
	if sq.Repo == "" {
		baseQ = restrictPRsToDiscoverableRepos(ctx, deps, baseDB, baseQ)
	}

	likeResults, err := searchPRLexical(ctx, baseQ, baseDB, sq, sortQualifier, explicitSort, deps.DefaultListLimit)
	if err != nil {
		return nil, err
	}

	// Step 2: Vector search.
	vec := ""
	if deps.EmbedQuery != nil {
		vec = deps.EmbedQuery(ctx, freeText)
	}
	if vec == "" {
		ranks := fuseDetailedSearchRanks(prsToRankedSearchIDs(likeResults), nil, explicitSort, deps.DefaultListLimit)
		return buildPRDetailedResults(baseDB, likeResults, ranks, sq, opts)
	}

	vecResults, err := searchPRSemantic(baseQ, baseDB, sq, vec, deps.DefaultListLimit)
	if err != nil {
		slog.WarnContext(ctx, "pull request search vector query failed; returning lexical results only", "error", err)
		ranks := fuseDetailedSearchRanks(prsToRankedSearchIDs(likeResults), nil, explicitSort, deps.DefaultListLimit)
		return buildPRDetailedResults(baseDB, likeResults, ranks, sq, opts)
	}

	ranks := fuseDetailedSearchRanks(prsToRankedSearchIDs(likeResults), prsToRankedSearchIDs(vecResults), explicitSort, deps.DefaultListLimit)
	allResults := make([]db.PullRequest, 0, len(likeResults)+len(vecResults))
	allResults = append(allResults, likeResults...)
	allResults = append(allResults, vecResults...)
	return buildPRDetailedResults(baseDB, allResults, ranks, sq, opts)
}

func buildPRDetailedResults(
	baseDB *gorm.DB,
	prs []db.PullRequest,
	ranks []detailedSearchRank,
	sq SearchQualifiers,
	opts SearchOptions,
) ([]PRSearchResult, error) {
	_, _, _, searchComments := resolvePRLexicalTargets(sq.In)
	commentBodies := map[searchCommentKey][]string(nil)
	var err error
	if searchComments {
		commentBodies, err = loadCommentBodiesForPRs(baseDB, prs)
		if err != nil {
			return nil, err
		}
	}
	return prResultsFromRanksWithComments(prs, ranks, sq, opts, commentBodies), nil
}

func searchPRLexical(ctx context.Context, baseQ *gorm.DB, baseDB *gorm.DB, sq SearchQualifiers, sortQualifier string, explicitSort bool, limit int) ([]db.PullRequest, error) {
	if !supportsTiDBFullText(baseDB) {
		return searchPRLikeFallback(baseQ, sq, sortQualifier, limit)
	}

	qLexical := baseQ.Session(&gorm.Session{NewDB: false})
	tokenGroups := make([][]lexicalTokenQuery, 0, len(sq.FreeText))
	for _, token := range sq.FreeText {
		tokenGroups = append(tokenGroups, buildPRLexicalTokenQueries(sq.In, token, true))
	}

	prIDs, err := collectLexicalMatchIDs(qLexical, tokenGroups, "pull_requests", sortQualifier, explicitSort, limit)
	if err != nil {
		slog.WarnContext(ctx, "pull request search TiDB full-text query failed; falling back to LIKE", "error", err)
		return searchPRLikeFallback(baseQ, sq, sortQualifier, limit)
	}
	if len(prIDs) == 0 {
		return nil, nil
	}
	var prs []db.PullRequest
	if err := baseQ.Session(&gorm.Session{NewDB: false}).
		Where("pull_requests.id IN ?", prIDs).
		Clauses(clause.OrderBy{Expression: orderByIDCaseExpr("pull_requests", prIDs)}).
		Find(&prs).Error; err != nil {
		return nil, err
	}
	return prs, nil
}

func searchPRLikeFallback(baseQ *gorm.DB, sq SearchQualifiers, sortQualifier string, limit int) ([]db.PullRequest, error) {
	qLike := baseQ.Session(&gorm.Session{NewDB: false})
	qLike = applyTextTokens(qLike, sq.FreeText, func(text string) (string, []any) {
		return BuildPRTextWhere(sq.In, text)
	})
	var prs []db.PullRequest
	if err := qLike.Order(SortOrder(sortQualifier, "pull_requests")).Limit(limit).Find(&prs).Error; err != nil {
		return nil, err
	}
	return prs, nil
}

func searchPRSemantic(baseQ *gorm.DB, baseDB *gorm.DB, sq SearchQualifiers, vec string, limit int) ([]db.PullRequest, error) {
	if !supportsVectorDistance(baseDB) {
		return nil, nil
	}
	if !supportsTiDBANN(baseDB) {
		return searchPRSemanticFiltered(baseQ, vec, limit)
	}

	switch semanticModeForQuery(sq, prSemanticApplicable(sq.In)) {
	case semanticModeDisabled:
		return nil, nil
	case semanticModeFilteredExact:
		return searchPRSemanticFiltered(baseQ, vec, limit)
	default:
		return searchPRSemanticANN(baseQ, baseDB, vec, limit)
	}
}

func buildPRANNCandidateQuery(baseDB *gorm.DB, vec string, limit int) *gorm.DB {
	// Keep the ANN candidate query free of WHERE predicates so TiDB can use
	// the vector index for ORDER BY ... LIMIT candidate generation.
	return baseDB.Table("pull_requests").
		Clauses(clause.OrderBy{
			Expression: clause.Expr{SQL: "VEC_COSINE_DISTANCE(pull_requests.embedding, ?) ASC", Vars: []any{vec}},
		}).
		Limit(semanticCandidateLimit(limit))
}

func searchPRSemanticANN(baseQ *gorm.DB, baseDB *gorm.DB, vec string, limit int) ([]db.PullRequest, error) {
	var candidateIDs []uint
	if err := buildPRANNCandidateQuery(baseDB, vec, limit).Pluck("pull_requests.id", &candidateIDs).Error; err != nil {
		return nil, err
	}
	if len(candidateIDs) == 0 {
		return nil, nil
	}

	qVec := baseQ.Session(&gorm.Session{NewDB: false}).Where("pull_requests.id IN ?", candidateIDs)
	qVec = qVec.Clauses(clause.OrderBy{Expression: orderByIDCaseExpr("pull_requests", candidateIDs)})

	var prs []db.PullRequest
	if err := qVec.Limit(limit).Find(&prs).Error; err != nil {
		return nil, err
	}
	return prs, nil
}

func buildPRSemanticFilteredQuery(baseQ *gorm.DB, vec string) *gorm.DB {
	return baseQ.Session(&gorm.Session{NewDB: false}).
		Where("pull_requests.embedding IS NOT NULL").
		Clauses(clause.OrderBy{
			Expression: clause.Expr{SQL: "VEC_COSINE_DISTANCE(pull_requests.embedding, ?) ASC", Vars: []any{vec}},
		})
}

func buildPRSemanticFilteredRankQuery(baseQ *gorm.DB, vec string) *gorm.DB {
	return buildPRSemanticFilteredQuery(baseQ, vec).Select("pull_requests.id")
}

func searchPRSemanticFiltered(baseQ *gorm.DB, vec string, limit int) ([]db.PullRequest, error) {
	var rankedIDs []uint
	if err := buildPRSemanticFilteredRankQuery(baseQ, vec).Limit(limit).Pluck("pull_requests.id", &rankedIDs).Error; err != nil {
		return nil, err
	}
	if len(rankedIDs) == 0 {
		return nil, nil
	}

	qVec := baseQ.Session(&gorm.Session{NewDB: false}).Where("pull_requests.id IN ?", rankedIDs)
	qVec = qVec.Clauses(clause.OrderBy{Expression: orderByIDCaseExpr("pull_requests", rankedIDs)})

	var prs []db.PullRequest
	if err := qVec.Limit(limit).Find(&prs).Error; err != nil {
		return nil, err
	}
	return prs, nil
}

func fusePRSearchResults(lexical, semantic []db.PullRequest, limit int) []db.PullRequest {
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
	type prRank struct {
		pr          db.PullRequest
		score       float64
		lexicalHit  bool
		semanticHit bool
	}
	const rankConstant = 60.0
	byID := make(map[uint]*prRank, len(lexical)+len(semantic))
	for idx, pr := range lexical {
		entry := byID[pr.ID]
		if entry == nil {
			entry = &prRank{pr: pr}
			byID[pr.ID] = entry
		}
		entry.score += 1.0 / (rankConstant + float64(idx+1))
		entry.lexicalHit = true
	}
	for idx, pr := range semantic {
		entry := byID[pr.ID]
		if entry == nil {
			entry = &prRank{pr: pr}
			byID[pr.ID] = entry
		}
		entry.score += 1.0 / (rankConstant + float64(idx+1))
		entry.semanticHit = true
	}
	ranked := make([]prRank, 0, len(byID))
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
			return ranked[i].pr.ID > ranked[j].pr.ID
		}
		return ranked[i].score > ranked[j].score
	})
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]db.PullRequest, 0, len(ranked))
	for _, entry := range ranked {
		out = append(out, entry.pr)
	}
	return out
}

func restrictPRsToDiscoverableRepos(ctx context.Context, deps PRSearchDeps, baseDB *gorm.DB, q *gorm.DB) *gorm.DB {
	return q
}

// BuildPRInFilter builds a WHERE clause to filter PR results based on in: qualifier.
func BuildPRInFilter(inValues []string) (string, []any) {
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
		}
	}

	if searchTitle && searchBody {
		return "", nil
	}
	if searchTitle {
		return "pull_requests.title IS NOT NULL AND pull_requests.title != ''", nil
	}
	if searchBody {
		return "pull_requests.body IS NOT NULL AND pull_requests.body != ''", nil
	}

	return "", nil
}

// BuildPRTextWhere builds the WHERE clause for PR text search based on the in: qualifier.
// Note: PR comments are stored in issue_comments table, linked by (repository_id, issue_number = PR number).
// When no in: qualifier is specified, also searches commit messages and filenames.
func BuildPRTextWhere(inValues []string, text string) (string, []any) {
	ctx := textWhereContextForTable("pull_requests")
	titleField := ctx.titleField
	bodyField := ctx.bodyField
	repoField := ctx.repoField
	numberField := ctx.numberField

	// If no in: qualifier specified, search title, body, commit messages, and filenames
	if len(inValues) == 0 {
		return "(" + titleField + " LIKE ? OR " + bodyField + " LIKE ? OR " +
				"pull_requests.commit_messages LIKE ? OR " +
				"pull_requests.filenames LIKE ?)",
			[]any{text, text, text, text}
	}

	// Check what fields to search
	searchTitle := false
	searchBody := false
	searchComments := false
	for _, v := range inValues {
		switch strings.ToLower(v) {
		case "title":
			searchTitle = true
		case "body":
			searchBody = true
		case "comments":
			searchComments = true
		}
	}

	// Helper for comments subquery
	commentSubquery := "EXISTS (SELECT 1 FROM issue_comments WHERE repository_id = " + repoField + " AND issue_number = " + numberField + " AND body LIKE ?)"

	// If only comments, search issue_comments.body via subquery
	if searchComments && !searchTitle && !searchBody {
		return commentSubquery, []any{text}
	}

	// If comments + title (but not body), search title OR comments
	if searchComments && searchTitle && !searchBody {
		return "(" + titleField + " LIKE ? OR " + commentSubquery + ")", []any{text, text}
	}

	// If comments + body (but not title), search body OR comments
	if searchComments && searchBody && !searchTitle {
		return "(" + bodyField + " LIKE ? OR " + commentSubquery + ")", []any{text, text}
	}

	// If all three (title, body, comments) or title+body, search all (but not commit messages/filenames when in: is explicit)
	if searchTitle && searchBody && searchComments {
		return "(" + titleField + " LIKE ? OR " + bodyField + " LIKE ? OR " + commentSubquery + ")", []any{text, text, text}
	}

	// Fallback to title/body only (no comments)
	if searchTitle && searchBody {
		return "(" + titleField + " LIKE ? OR " + bodyField + " LIKE ?)", []any{text, text}
	}
	if searchTitle {
		return titleField + " LIKE ?", []any{text}
	}
	if searchBody {
		return bodyField + " LIKE ?", []any{text}
	}

	// Fallback to default if no valid in: values
	return "(" + titleField + " LIKE ? OR " + bodyField + " LIKE ? OR " +
		"pull_requests.commit_messages LIKE ? OR " +
		"pull_requests.filenames LIKE ?)", []any{text, text, text, text}
}

// ApplyPRQualifiers applies all structured search qualifiers to a PR query.
func ApplyPRQualifiers(q *gorm.DB, baseDB *gorm.DB, sq SearchQualifiers) *gorm.DB {
	if sq.StateConflict {
		return q.Where("1 = 0")
	}
	repoScope, repoScopeStatus := resolveRepoSearchScope(baseDB, sq.Repo)
	q = applyRepoFilterWithScope(q, baseDB, "pull_requests", sq.Repo, repoScope, repoScopeStatus)
	q = applyVisibilityQualifier(q, baseDB, "pull_requests", sq.Visibility)
	q = applyForkQualifier(q, baseDB, "pull_requests", sq.Fork)
	if sq.State != "" && sq.State != "all" {
		if sq.State == db.StateOpen {
			q = q.Where("pull_requests.state = 'open' AND pull_requests.merged = false")
		} else {
			q = q.Where("pull_requests.state = 'closed' OR pull_requests.merged = true")
		}
	}
	// Number qualifier: #<number> searches by PR number
	if sq.Number != nil {
		q = q.Where("pull_requests.number = ?", *sq.Number)
	}
	q = applyAuthorFilter(q, baseDB, "pull_requests", sq.Author)
	q = applyAssigneeFilter(q, baseDB, "pull_requests", sq.Assignee)
	if sq.ReviewRequested != "" {
		q = q.Where("pull_requests.id IN (?)",
			baseDB.Session(&gorm.Session{NewDB: true}).Table("review_requests").Select("pull_request_id").
				Where("login = ? OR team_slug = ?", sq.ReviewRequested, sq.ReviewRequested))
	}
	if sq.UserReviewRequested != "" {
		q = q.Where("pull_requests.id IN (?)",
			baseDB.Session(&gorm.Session{NewDB: true}).Table("review_requests").Select("pull_request_id").
				Where("login = ? AND login != ''", sq.UserReviewRequested))
	}
	if sq.TeamReviewRequested != "" {
		q = q.Where("pull_requests.id IN (?)",
			baseDB.Session(&gorm.Session{NewDB: true}).Table("review_requests").Select("pull_request_id").
				Where("team_slug = ? AND team_slug != ''", sq.TeamReviewRequested))
	}
	// Positive label groups
	q = applyRepoScopedLabelGroups(q, baseDB, sq.Labels, "pr_labels", "pr_labels.pull_request_id", "pull_requests.id", false, repoScope, repoScopeStatus)
	// Negated label groups
	q = applyRepoScopedLabelGroups(q, baseDB, sq.NegatedLabels, "pr_labels", "pr_labels.pull_request_id", "pull_requests.id", true, repoScope, repoScopeStatus)
	// Negated author
	q = applyNegatedAuthorFilter(q, baseDB, "pull_requests", sq.NegatedAuthor)
	// Negated assignee
	q = applyNegatedAssigneeFilter(q, baseDB, "pull_requests", sq.NegatedAssignee)
	// Metadata: no:/has:
	q = applyMetadataFilters(q, baseDB, "pull_requests", sq)
	// Milestone
	q = applyMilestoneQualifier(q, "pull_requests", sq.Milestone)
	// Project
	q = applyProjectQualifier(q, baseDB, "pull_requests", "PullRequest_", "PULL_REQUEST", sq.Project)
	// Language
	q = applyLanguageQualifier(q, baseDB, "pull_requests", sq.Language)
	// Head/Base
	if sq.Head != "" {
		q = q.Where("pull_requests.head_ref = ?", sq.Head)
	}
	if sq.Base != "" {
		q = q.Where("pull_requests.base_ref = ?", sq.Base)
	}
	// Comments / reactions / interactions
	q = applyNumericRangeQualifier(q, issueCommentCountExpr("pull_requests"), sq.Comments)
	q = applyNumericRangeQualifier(q, issueReactionCountExpr("pull_requests"), sq.Reactions)
	q = applyNumericRangeQualifier(q, issueInteractionCountExpr("pull_requests"), sq.Interactions)
	// Date ranges
	q = applyDateRangeQualifier(q, "pull_requests.created_at", sq.Created)
	q = applyDateRangeQualifier(q, "pull_requests.updated_at", sq.Updated)
	q = applyDateRangeQualifier(q, "pull_requests.closed_at", sq.Closed)
	// Mentions
	q = applyMentionsQualifier(q, "pull_requests", sq.Mentions)
	// Team involvement
	q = applyTeamQualifier(q, baseDB, "pull_requests", sq.Team)
	// Linked issues
	if strings.EqualFold(strings.TrimSpace(sq.Linked), "issue") {
		q = q.Where("EXISTS (" +
			"SELECT 1 FROM linked_branches lb " +
			"WHERE lb.repository_id = pull_requests.repository_id AND lb.branch_name = pull_requests.head_ref)")
	}
	// CI status
	q = applyPRStatusQualifier(q, sq.Status)
	// Draft filter
	if sq.DraftSet {
		q = q.Where("pull_requests.draft = ?", sq.Draft)
	}
	// Merged filter
	if sq.Merged != nil {
		q = q.Where("pull_requests.merged = ?", *sq.Merged)
	}
	// Review status filter
	switch sq.Review {
	case "none":
		q = q.Where("pull_requests.id NOT IN (?)",
			baseDB.Session(&gorm.Session{NewDB: true}).Table("pull_request_reviews").Select("DISTINCT pull_request_id"))
	case "approved":
		q = q.Where("pull_requests.id IN (?)",
			baseDB.Session(&gorm.Session{NewDB: true}).Table("pull_request_reviews").Select("DISTINCT pull_request_id").
				Where("state = 'APPROVED'"))
	case "changes_requested":
		q = q.Where("pull_requests.id IN (?)",
			baseDB.Session(&gorm.Session{NewDB: true}).Table("pull_request_reviews").Select("DISTINCT pull_request_id").
				Where("state = 'CHANGES_REQUESTED'"))
	}
	// Reviewed-by
	if sq.ReviewedBy != "" {
		q = q.Where("pull_requests.id IN (?)",
			baseDB.Session(&gorm.Session{NewDB: true}).Table("pull_request_reviews").Select("DISTINCT pull_request_id").
				Where("author_login = ?", sq.ReviewedBy))
	}
	// Commenter: filter PRs where the specified user has commented
	if sq.Commenter != "" {
		q = q.Where("pull_requests.id IN ("+
			"SELECT DISTINCT ic.id FROM pull_requests ic "+
			"JOIN issue_comments icc ON icc.repository_id = ic.repository_id AND icc.issue_number = ic.number "+
			"JOIN users u ON u.id = icc.author_id WHERE u.login = ?)",
			sq.Commenter)
	}
	return q
}
