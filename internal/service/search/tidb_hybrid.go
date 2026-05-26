package search

import (
	"sort"
	"strconv"
	"strings"
	"unicode"

	modeldb "github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type lexicalTokenQuery struct {
	where          string
	args           []any
	scoreExpr      string
	scoreArgs      []any
	weight         float64
	fullTextField  string
	fullTextPhrase string
}

type semanticMode int

const (
	semanticModeDisabled semanticMode = iota
	semanticModeANN
	semanticModeFilteredExact
)

const commentLexicalScoreWeight = 0.25
const auxiliaryLexicalScoreWeight = 0.5

func supportsTiDBFullText(database *gorm.DB) bool {
	return modeldb.SupportsTiDBSearch(database)
}

func supportsTiDBANN(database *gorm.DB) bool {
	return modeldb.SupportsTiDBSearch(database)
}

func supportsVectorDistance(database *gorm.DB) bool {
	return modeldb.SupportsVectorDistance(database)
}

func semanticCandidateLimit(limit int) int {
	if limit <= 0 {
		return 128
	}
	candidateLimit := limit * 4
	if candidateLimit < 64 {
		candidateLimit = 64
	}
	if candidateLimit > 512 {
		candidateLimit = 512
	}
	return candidateLimit
}

func tokenUsesLikeFallback(token string) bool {
	return strings.IndexFunc(token, unicode.IsSpace) >= 0
}

func buildLikeLexicalTokenQuery(
	inValues []string,
	token string,
	buildWhere func([]string, string) (string, []any),
) lexicalTokenQuery {
	pattern := "%" + escapeLike(token) + "%"
	whereClause, whereArgs := buildWhere(inValues, pattern)
	return lexicalTokenQuery{
		where:     whereClause,
		args:      whereArgs,
		scoreExpr: "CASE WHEN " + whereClause + " THEN 1 ELSE 0 END",
		scoreArgs: append([]any(nil), whereArgs...),
		weight:    1,
	}
}

func buildFTSLexicalTokenQuery(fieldExpr string, token string, weight float64) lexicalTokenQuery {
	// TiDB full-text search rejects prepared parameter markers here because the
	// search phrase must be a constant, so emit a safely escaped string literal.
	ftsExpr := "FTS_MATCH_WORD(" + mysqlStringLiteral(token) + ", " + fieldExpr + ")"
	return lexicalTokenQuery{
		where:          ftsExpr,
		args:           nil,
		scoreExpr:      ftsExpr,
		scoreArgs:      nil,
		weight:         weight,
		fullTextField:  fieldExpr,
		fullTextPhrase: token,
	}
}

func mysqlStringLiteral(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case 0:
			b.WriteString(`\0`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\\':
			b.WriteString(`\\`)
		case '\'':
			b.WriteString(`''`)
		case 0x1a:
			b.WriteString(`\Z`)
		default:
			b.WriteByte(s[i])
		}
	}
	b.WriteByte('\'')
	return b.String()
}

func buildWhereLexicalTokenQuery(where string, args []any, weight float64) lexicalTokenQuery {
	return lexicalTokenQuery{
		where:     where,
		args:      args,
		scoreExpr: "CASE WHEN " + where + " THEN 1 ELSE 0 END",
		scoreArgs: append([]any(nil), args...),
		weight:    weight,
	}
}

func lexicalCandidateLimit(limit int) int {
	if limit <= 0 {
		return 512
	}
	candidateLimit := limit * 8
	if candidateLimit < 128 {
		candidateLimit = 128
	}
	if candidateLimit > 1000 {
		candidateLimit = 1000
	}
	return candidateLimit
}

func lexicalWeight(query lexicalTokenQuery) float64 {
	if query.weight <= 0 {
		return 1
	}
	return query.weight
}

func collectLexicalMatchIDs(baseQ *gorm.DB, tokenGroups [][]lexicalTokenQuery, tableName string, sortQualifier string, explicitSort bool, limit int) ([]uint, error) {
	if len(tokenGroups) == 0 {
		return nil, nil
	}
	if explicitSort {
		return pluckExplicitSortLexicalMatchIDs(baseQ, tokenGroups, tableName, sortQualifier, limit)
	}

	var aggregate map[uint]float64
	for _, group := range tokenGroups {
		tokenRanks := make(map[uint]float64)
		for _, tokenQuery := range group {
			ids, err := pluckLexicalTokenMatchIDs(baseQ, tokenQuery, tableName, limit)
			if err != nil {
				return nil, err
			}
			weight := lexicalWeight(tokenQuery)
			for idx, id := range ids {
				tokenRanks[id] += weight / (60.0 + float64(idx+1))
			}
		}
		if len(tokenRanks) == 0 {
			return nil, nil
		}
		if aggregate == nil {
			aggregate = tokenRanks
			continue
		}
		for id, score := range aggregate {
			if tokenScore, ok := tokenRanks[id]; ok {
				aggregate[id] = score + tokenScore
				continue
			}
			delete(aggregate, id)
		}
		if len(aggregate) == 0 {
			return nil, nil
		}
	}

	if explicitSort {
		ids := make([]uint, 0, len(aggregate))
		for id := range aggregate {
			ids = append(ids, id)
		}
		return orderLexicalMatchIDs(baseQ, ids, tableName, sortQualifier, limit)
	}

	type rankedID struct {
		id    uint
		score float64
	}
	ranked := make([]rankedID, 0, len(aggregate))
	for id, score := range aggregate {
		ranked = append(ranked, rankedID{id: id, score: score})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].id > ranked[j].id
		}
		return ranked[i].score > ranked[j].score
	})
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	ids := make([]uint, 0, len(ranked))
	for _, entry := range ranked {
		ids = append(ids, entry.id)
	}
	return ids, nil
}

func buildExplicitSortLexicalWhere(baseQ *gorm.DB, tokenGroups [][]lexicalTokenQuery, tableName string) (string, []any) {
	groupClauses := make([]string, 0, len(tokenGroups))
	args := make([]any, 0, len(tokenGroups))
	for _, group := range tokenGroups {
		if len(group) == 0 {
			continue
		}
		tokenClauses := make([]string, 0, len(group))
		for _, tokenQuery := range group {
			if tokenQuery.isFullText() {
				subQ := baseQ.Session(&gorm.Session{NewDB: true}).
					Table(tableName).
					Select(tableName+".id AS id").
					Where(tokenQuery.where, tokenQuery.args...)
				tokenClauses = append(tokenClauses, "(EXISTS (SELECT 1 FROM (?) AS fts_matches WHERE fts_matches.id = "+tableName+".id))")
				args = append(args, subQ)
				continue
			}
			tokenClauses = append(tokenClauses, "("+tokenQuery.where+")")
			args = append(args, tokenQuery.args...)
		}
		groupClauses = append(groupClauses, "("+strings.Join(tokenClauses, " OR ")+")")
	}
	if len(groupClauses) == 0 {
		return "", nil
	}
	return strings.Join(groupClauses, " AND "), args
}

func pluckExplicitSortLexicalMatchIDs(baseQ *gorm.DB, tokenGroups [][]lexicalTokenQuery, tableName string, sortQualifier string, limit int) ([]uint, error) {
	whereClause, whereArgs := buildExplicitSortLexicalWhere(baseQ, tokenGroups, tableName)
	if whereClause == "" {
		return nil, nil
	}
	q := baseQ.Session(&gorm.Session{NewDB: false}).
		Where(whereClause, whereArgs...).
		Order(SortOrder(sortQualifier, tableName))
	if limit > 0 {
		q = q.Limit(limit)
	}

	var ids []uint
	if err := q.Pluck(tableName+".id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func pluckLexicalTokenMatchIDs(baseQ *gorm.DB, tokenQuery lexicalTokenQuery, tableName string, limit int) ([]uint, error) {
	if tokenQuery.isFullText() {
		return pluckFullTextLexicalTokenMatchIDs(baseQ, tokenQuery, tableName, limit)
	}
	q := baseQ.Session(&gorm.Session{NewDB: false}).Where(tokenQuery.where, tokenQuery.args...)
	if tokenQuery.scoreExpr != "" {
		q = q.Clauses(clause.OrderBy{
			Expression: clause.Expr{
				SQL:  tokenQuery.scoreExpr + " DESC, " + tableName + ".id DESC",
				Vars: tokenQuery.scoreArgs,
			},
		})
	} else {
		q = q.Order(tableName + ".id DESC")
	}
	q = q.Limit(lexicalCandidateLimit(limit))

	var ids []uint
	if err := q.Pluck(tableName+".id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func (q lexicalTokenQuery) isFullText() bool {
	return q.fullTextField != "" && q.fullTextPhrase != ""
}

func hasFullTextLexicalToken(tokenGroups [][]lexicalTokenQuery) bool {
	for _, group := range tokenGroups {
		for _, tokenQuery := range group {
			if tokenQuery.isFullText() {
				return true
			}
		}
	}
	return false
}

// TiDB requires FTS_MATCH_WORD to be the only predicate in its WHERE clause.
// Keep FTS in a derived table, then apply repo/state/label qualifiers outside.
func buildFullTextLexicalSubquery(baseQ *gorm.DB, tokenQuery lexicalTokenQuery, tableName string) *gorm.DB {
	return baseQ.Session(&gorm.Session{NewDB: true}).
		Table(tableName).
		Select(tableName+".id AS id, "+tokenQuery.scoreExpr+" AS fts_score", tokenQuery.scoreArgs...).
		Where(tokenQuery.where, tokenQuery.args...)
}

func buildFullTextLexicalMatchQuery(baseQ *gorm.DB, tokenQuery lexicalTokenQuery, tableName string, limit int) *gorm.DB {
	subQ := buildFullTextLexicalSubquery(baseQ, tokenQuery, tableName)
	q := baseQ.Session(&gorm.Session{NewDB: false}).
		Joins("JOIN (?) AS fts_matches ON fts_matches.id = "+tableName+".id", subQ).
		Clauses(clause.OrderBy{
			Expression: clause.Expr{
				SQL: "fts_matches.fts_score DESC, " + tableName + ".id DESC",
			},
		})
	if limit > 0 {
		q = q.Limit(lexicalCandidateLimit(limit))
	}
	return q
}

func pluckFullTextLexicalTokenMatchIDs(baseQ *gorm.DB, tokenQuery lexicalTokenQuery, tableName string, limit int) ([]uint, error) {
	var ids []uint
	if err := buildFullTextLexicalMatchQuery(baseQ, tokenQuery, tableName, limit).
		Pluck(tableName+".id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func orderLexicalMatchIDs(baseQ *gorm.DB, ids []uint, tableName string, sortQualifier string, limit int) ([]uint, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q := baseQ.Session(&gorm.Session{NewDB: false}).
		Where(tableName+".id IN ?", ids).
		Order(SortOrder(sortQualifier, tableName))
	if limit > 0 {
		q = q.Limit(limit)
	}

	var orderedIDs []uint
	if err := q.Pluck(tableName+".id", &orderedIDs).Error; err != nil {
		return nil, err
	}
	return orderedIDs, nil
}

func issueCommentExistsExpr(pattern string) (string, []any) {
	return "EXISTS (" +
			"SELECT 1 FROM issue_comments " +
			"WHERE issue_comments.repository_id = issues.repository_id " +
			"AND issue_comments.issue_number = issues.number " +
			"AND issue_comments.body LIKE ?)",
		[]any{pattern}
}

func buildIssueLexicalTokenQueries(inValues []string, token string, useFullText bool) []lexicalTokenQuery {
	if !useFullText || tokenUsesLikeFallback(token) {
		return []lexicalTokenQuery{buildLikeLexicalTokenQuery(inValues, token, BuildIssueTextWhere)}
	}

	searchTitle, searchBody, searchComments := resolveTextSearchTargets(inValues)
	pattern := "%" + escapeLike(token) + "%"
	commentWhere, commentArgs := issueCommentExistsExpr(pattern)

	queries := make([]lexicalTokenQuery, 0, 3)
	if searchTitle {
		queries = append(queries, buildFTSLexicalTokenQuery("issues.title", token, 1))
	}
	if searchBody {
		queries = append(queries, buildFTSLexicalTokenQuery("issues.body", token, 1))
	}
	if searchComments {
		queries = append(queries, buildWhereLexicalTokenQuery(commentWhere, commentArgs, commentLexicalScoreWeight))
	}
	if len(queries) == 0 {
		return []lexicalTokenQuery{buildLikeLexicalTokenQuery(inValues, token, BuildIssueTextWhere)}
	}
	return queries
}

func prCommentExistsExpr(pattern string) (string, []any) {
	return "EXISTS (" +
			"SELECT 1 FROM issue_comments " +
			"WHERE issue_comments.repository_id = pull_requests.repository_id " +
			"AND issue_comments.issue_number = pull_requests.number " +
			"AND issue_comments.body LIKE ?)",
		[]any{pattern}
}

func resolvePRLexicalTargets(inValues []string) (defaultAll bool, searchTitle bool, searchBody bool, searchComments bool) {
	if len(inValues) == 0 {
		return true, true, true, false
	}
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
	if !searchTitle && !searchBody && !searchComments {
		return true, true, true, false
	}
	return false, searchTitle, searchBody, searchComments
}

func buildPRLexicalTokenQueries(inValues []string, token string, useFullText bool) []lexicalTokenQuery {
	if !useFullText || tokenUsesLikeFallback(token) {
		return []lexicalTokenQuery{buildLikeLexicalTokenQuery(inValues, token, BuildPRTextWhere)}
	}

	defaultAll, searchTitle, searchBody, searchComments := resolvePRLexicalTargets(inValues)
	pattern := "%" + escapeLike(token) + "%"
	commentWhere, _ := prCommentExistsExpr(pattern)

	queries := make([]lexicalTokenQuery, 0, 5)
	if searchTitle {
		queries = append(queries, buildFTSLexicalTokenQuery("pull_requests.title", token, 1))
	}
	if searchBody {
		queries = append(queries, buildFTSLexicalTokenQuery("pull_requests.body", token, 1))
	}
	if defaultAll {
		queries = append(queries,
			buildWhereLexicalTokenQuery("pull_requests.commit_messages LIKE ?", []any{pattern}, auxiliaryLexicalScoreWeight),
			buildWhereLexicalTokenQuery("pull_requests.filenames LIKE ?", []any{pattern}, auxiliaryLexicalScoreWeight),
		)
	}
	if searchComments {
		queries = append(queries, buildWhereLexicalTokenQuery(commentWhere, []any{pattern}, commentLexicalScoreWeight))
	}
	if len(queries) == 0 {
		return []lexicalTokenQuery{buildLikeLexicalTokenQuery(inValues, token, BuildPRTextWhere)}
	}
	return queries
}

func issueSemanticApplicable(inValues []string) bool {
	if len(inValues) == 0 {
		return true
	}
	searchTitle, searchBody, searchComments := resolveTextSearchTargets(inValues)
	return searchTitle && searchBody && !searchComments
}

func prSemanticApplicable(inValues []string) bool {
	if len(inValues) == 0 {
		return true
	}
	_, searchTitle, searchBody, searchComments := resolvePRLexicalTargets(inValues)
	return searchTitle && searchBody && !searchComments
}

func semanticModeForQuery(sq SearchQualifiers, applicable bool) semanticMode {
	if !applicable {
		return semanticModeDisabled
	}
	if hasRestrictiveSemanticQualifiers(sq) {
		return semanticModeFilteredExact
	}
	return semanticModeANN
}

func hasRestrictiveSemanticQualifiers(sq SearchQualifiers) bool {
	if sq.Repo != "" || len(sq.Repos) > 0 {
		return true
	}
	if sq.Number != nil {
		return true
	}
	if sq.Author != "" || sq.Assignee != "" || sq.Commenter != "" || sq.Mentions != "" || sq.Team != "" || sq.Involves != "" {
		return true
	}
	if sq.NegatedAuthor != "" || sq.NegatedAssignee != "" {
		return true
	}
	if len(sq.Labels) > 0 || len(sq.NegatedLabels) > 0 || sq.NoLabel || sq.HasLabel || sq.NoAssignee || sq.HasAssignee || sq.NoMilestone || sq.NoProject {
		return true
	}
	if sq.Milestone != "" || sq.Project != "" || sq.Language != "" || sq.Visibility != "" || sq.Fork != "" {
		return true
	}
	if sq.Status != "" || sq.Linked != "" || sq.Reason != "" {
		return true
	}
	if sq.Comments != "" || sq.Interactions != "" || sq.Reactions != "" {
		return true
	}
	if sq.Created != "" || sq.Updated != "" || sq.Closed != "" || sq.MergedDate != "" {
		return true
	}
	if sq.Head != "" || sq.Base != "" {
		return true
	}
	if sq.Review != "" || sq.ReviewedBy != "" || sq.ReviewRequested != "" || sq.UserReviewRequested != "" || sq.TeamReviewRequested != "" {
		return true
	}
	if sq.DraftSet || sq.Merged != nil || sq.IsLocked != nil {
		return true
	}
	return false
}

func orderByIDCaseExpr(tableName string, ids []uint) clause.Expr {
	if len(ids) == 0 {
		return clause.Expr{SQL: tableName + ".id DESC"}
	}
	var sql strings.Builder
	vars := make([]any, 0, len(ids))
	sql.WriteString("CASE ")
	sql.WriteString(tableName)
	sql.WriteString(".id")
	for idx, id := range ids {
		sql.WriteString(" WHEN ? THEN ")
		sql.WriteString(strconv.Itoa(idx))
		vars = append(vars, id)
	}
	sql.WriteString(" ELSE ")
	sql.WriteString(strconv.Itoa(len(ids)))
	sql.WriteString(" END ASC")
	return clause.Expr{SQL: sql.String(), Vars: vars}
}
