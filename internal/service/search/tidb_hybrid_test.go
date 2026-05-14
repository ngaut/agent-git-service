package search

import (
	"strings"
	"testing"

	modeldb "gh-server/internal/db"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func newDryRunMySQLDB(t *testing.T) *gorm.DB {
	t.Helper()

	gdb, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "gorm:gorm@tcp(localhost:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	if err != nil {
		t.Fatalf("open dry-run mysql db: %v", err)
	}
	return gdb
}

func TestSupportsTiDBSearch_DialectAloneIsNotEnough(t *testing.T) {
	gdb := newDryRunMySQLDB(t)

	if supportsTiDBFullText(gdb) {
		t.Fatal("expected dry-run mysql dialect to avoid TiDB full-text path")
	}
	if supportsTiDBANN(gdb) {
		t.Fatal("expected dry-run mysql dialect to avoid TiDB ANN path")
	}
	if supportsVectorDistance(gdb) {
		t.Fatal("expected dry-run mysql dialect to avoid vector distance path")
	}
}

func TestBuildIssueLexicalTokenQuery_UsesFullTextForMultilingualTokens(t *testing.T) {
	tests := []struct {
		name       string
		inValues   []string
		token      string
		wantFields []string
	}{
		{name: "default searches title and body indexes separately", token: "你好", wantFields: []string{"issues.title", "issues.body"}},
		{name: "title only uses title index", inValues: []string{"title"}, token: "こんにちは", wantFields: []string{"issues.title"}},
		{name: "body only uses body index", inValues: []string{"body"}, token: "안녕하세요", wantFields: []string{"issues.body"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildIssueLexicalTokenQueries(tt.inValues, tt.token, true)

			if len(got) != len(tt.wantFields) {
				t.Fatalf("expected %d token queries, got %#v", len(tt.wantFields), got)
			}
			for idx, wantField := range tt.wantFields {
				query := got[idx]
				wantExpr := "FTS_MATCH_WORD(" + mysqlStringLiteral(tt.token) + ", " + wantField + ")"
				if !strings.Contains(query.where, wantExpr) {
					t.Fatalf("expected FTS where clause on %s, got %q", wantField, query.where)
				}
				if strings.Contains(query.where, "LIKE ?") {
					t.Fatalf("expected full-text query for %q, got LIKE clause %q", tt.token, query.where)
				}
				if len(query.args) != 0 {
					t.Fatalf("expected FTS token to be emitted as a literal, got args %#v", query.args)
				}
				if !strings.Contains(query.scoreExpr, wantExpr) {
					t.Fatalf("expected FTS score expression on %s, got %q", wantField, query.scoreExpr)
				}
			}
		})
	}
}

func TestBuildFTSLexicalTokenQuery_EmitsEscapedLiteral(t *testing.T) {
	got := buildFTSLexicalTokenQuery("issues.title", "wiki's \\ draft", 1)

	want := `FTS_MATCH_WORD('wiki''s \\ draft', issues.title)`
	if got.where != want {
		t.Fatalf("expected escaped FTS expression %q, got %q", want, got.where)
	}
	if len(got.args) != 0 || len(got.scoreArgs) != 0 {
		t.Fatalf("expected escaped FTS expression to avoid bound args, got args=%#v scoreArgs=%#v", got.args, got.scoreArgs)
	}
}

func TestBuildIssueLexicalTokenQuery_CommentsStayLexicalOnly(t *testing.T) {
	got := buildIssueLexicalTokenQueries([]string{"title", "comments"}, "검색", true)

	if len(got) != 2 {
		t.Fatalf("expected title FTS and comment LIKE queries, got %#v", got)
	}
	if !strings.Contains(got[0].where, "FTS_MATCH_WORD('검색', issues.title)") {
		t.Fatalf("expected title full-text clause, got %q", got[0].where)
	}
	if !strings.Contains(got[1].where, "issue_comments.body LIKE ?") {
		t.Fatalf("expected comment LIKE clause, got %q", got[1].where)
	}
	if got[1].weight != commentLexicalScoreWeight {
		t.Fatalf("expected weighted lexical comment score, got %v", got[1].weight)
	}
}

func TestBuildIssueLexicalTokenQuery_PhraseFallsBackToLike(t *testing.T) {
	got := buildIssueLexicalTokenQueries(nil, "hello world", true)

	if len(got) != 1 {
		t.Fatalf("expected one LIKE fallback query, got %#v", got)
	}
	if strings.Contains(got[0].where, "FTS_MATCH_WORD") {
		t.Fatalf("expected phrase query to avoid FTS, got %q", got[0].where)
	}
	if !strings.Contains(got[0].where, "issues.title LIKE ? OR issues.body LIKE ?") {
		t.Fatalf("expected LIKE fallback for phrase token, got %q", got[0].where)
	}
}

func TestApplyRepoFilterWithScope_UsesRepositoryIDPredicate(t *testing.T) {
	gdb := newDryRunMySQLDB(t)

	sql := gdb.ToSQL(func(tx *gorm.DB) *gorm.DB {
		var issues []modeldb.Issue
		q := applyRepoFilterWithScope(
			tx.Model(&modeldb.Issue{}),
			tx,
			"issues",
			"octo/public",
			&repoSearchScope{ID: 42, FullName: "octo/public"},
			repoScopeResolved,
		)
		return q.Find(&issues)
	})

	if strings.Contains(sql, "JOIN repositories") {
		t.Fatalf("expected repo-scoped filter to avoid repositories join, got %q", sql)
	}
	if !strings.Contains(sql, "issues.repository_id = 42") {
		t.Fatalf("expected direct repository_id predicate, got %q", sql)
	}
}

func TestApplyResolvedLabelGroupIDs_UsesLabelIDSubqueries(t *testing.T) {
	gdb := newDryRunMySQLDB(t)

	sql := gdb.ToSQL(func(tx *gorm.DB) *gorm.DB {
		var issues []modeldb.Issue
		q := applyResolvedLabelGroupIDs(
			tx.Model(&modeldb.Issue{}),
			tx,
			[][]string{{"bug"}, {"ui", "ux"}},
			map[string][]uint{
				"bug": {7},
				"ui":  {11},
				"ux":  {13},
			},
			"issue_labels",
			"issue_labels.issue_id",
			"issues.id",
			false,
		)
		return q.Find(&issues)
	})

	if strings.Contains(sql, "JOIN labels") {
		t.Fatalf("expected resolved label groups to avoid labels join, got %q", sql)
	}
	if !strings.Contains(sql, "issue_labels.label_id IN (7)") {
		t.Fatalf("expected first label-id subquery, got %q", sql)
	}
	if !strings.Contains(sql, "issue_labels.label_id IN (11,13)") {
		t.Fatalf("expected grouped label-id subquery, got %q", sql)
	}
}

func TestBuildPRLexicalTokenQuery_TargetsExpectedFullTextColumns(t *testing.T) {
	tests := []struct {
		name       string
		inValues   []string
		token      string
		wantFields []string
	}{
		{name: "default uses title and body indexes plus auxiliary LIKE", token: "混合検索", wantFields: []string{"pull_requests.title", "pull_requests.body"}},
		{name: "title and body use separate indexes", inValues: []string{"title", "body"}, token: "レビュー", wantFields: []string{"pull_requests.title", "pull_requests.body"}},
		{name: "title only uses title index", inValues: []string{"title"}, token: "提案", wantFields: []string{"pull_requests.title"}},
		{name: "body only uses body index", inValues: []string{"body"}, token: "검토", wantFields: []string{"pull_requests.body"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildPRLexicalTokenQueries(tt.inValues, tt.token, true)

			if len(got) < len(tt.wantFields) {
				t.Fatalf("expected at least %d token queries, got %#v", len(tt.wantFields), got)
			}
			for idx, wantField := range tt.wantFields {
				query := got[idx]
				wantExpr := "FTS_MATCH_WORD(" + mysqlStringLiteral(tt.token) + ", " + wantField + ")"
				if !strings.Contains(query.where, wantExpr) {
					t.Fatalf("expected FTS where clause on %s, got %q", wantField, query.where)
				}
				if strings.Contains(query.where, "LIKE ?") {
					t.Fatalf("expected full-text query for %q, got LIKE clause %q", tt.token, query.where)
				}
			}
			for _, query := range got {
				if strings.Contains(query.where, "search_document") || strings.Contains(query.where, "search_text") {
					t.Fatalf("unexpected generated search helper column in query: %#v", got)
				}
			}
		})
	}

	commentOnly := buildPRLexicalTokenQueries([]string{"comments"}, "反馈", true)
	if len(commentOnly) != 1 {
		t.Fatalf("expected one comments-only query, got %#v", commentOnly)
	}
	if strings.Contains(commentOnly[0].where, "FTS_MATCH_WORD") {
		t.Fatalf("expected comments-only query to stay lexical-only, got %q", commentOnly[0].where)
	}
	if !strings.Contains(commentOnly[0].where, "issue_comments.body LIKE ?") {
		t.Fatalf("expected comments-only query to use comment LIKE, got %q", commentOnly[0].where)
	}
}

func TestBuildExplicitSortLexicalWhere_PreservesPerTokenDisjunctions(t *testing.T) {
	gotWhere, gotArgs := buildExplicitSortLexicalWhere([][]lexicalTokenQuery{
		{
			buildFTSLexicalTokenQuery("pull_requests.title", "alpha", 1),
			buildFTSLexicalTokenQuery("pull_requests.body", "alpha", 1),
		},
		{
			buildFTSLexicalTokenQuery("pull_requests.title", "beta", 1),
			buildWhereLexicalTokenQuery("pull_requests.commit_messages LIKE ?", []any{"%beta%"}, auxiliaryLexicalScoreWeight),
		},
	})

	wantParts := []string{
		"((FTS_MATCH_WORD('alpha', pull_requests.title)) OR (FTS_MATCH_WORD('alpha', pull_requests.body)))",
		"((FTS_MATCH_WORD('beta', pull_requests.title)) OR (pull_requests.commit_messages LIKE ?))",
	}
	for _, want := range wantParts {
		if !strings.Contains(gotWhere, want) {
			t.Fatalf("expected exact-sort where clause to contain %q, got %q", want, gotWhere)
		}
	}
	if strings.Count(gotWhere, " AND ") != 1 {
		t.Fatalf("expected token groups to be intersected once, got %q", gotWhere)
	}
	if len(gotArgs) != 1 {
		t.Fatalf("expected one bound LIKE arg, got %#v", gotArgs)
	}
}

func TestPluckExplicitSortLexicalMatchIDs_UsesSingleOrderedQuery(t *testing.T) {
	gdb := newDryRunMySQLDB(t)

	sql := gdb.ToSQL(func(tx *gorm.DB) *gorm.DB {
		baseQ := tx.Model(&modeldb.PullRequest{}).Where("pull_requests.repository_id = ?", 42)
		tokenGroups := [][]lexicalTokenQuery{
			buildPRLexicalTokenQueries(nil, "alpha", true),
			buildPRLexicalTokenQueries(nil, "beta", true),
		}
		whereClause, whereArgs := buildExplicitSortLexicalWhere(tokenGroups)
		var ids []uint
		return baseQ.Where(whereClause, whereArgs...).Order(SortOrder("updated-desc", "pull_requests")).Limit(10).Pluck("pull_requests.id", &ids)
	})

	if strings.Contains(sql, "LIMIT 128") || strings.Contains(sql, "LIMIT 1000") {
		t.Fatalf("expected explicit-sort path to avoid intermediate lexical candidate caps, got %q", sql)
	}
	if !strings.Contains(sql, "repository_id = 42") {
		t.Fatalf("expected base qualifiers to be preserved, got %q", sql)
	}
	if !strings.Contains(sql, "ORDER BY pull_requests.updated_at DESC") {
		t.Fatalf("expected requested explicit sort order, got %q", sql)
	}
	if strings.Count(sql, "FTS_MATCH_WORD") < 4 {
		t.Fatalf("expected all token predicates to stay in a single SQL query, got %q", sql)
	}
}

func TestSemanticModeForQuery(t *testing.T) {
	tests := []struct {
		name string
		sq   SearchQualifiers
		want semanticMode
	}{
		{name: "broad query uses ann", sq: SearchQualifiers{}, want: semanticModeANN},
		{name: "repo qualifier forces filtered exact", sq: SearchQualifiers{CoreFilters: CoreFilters{Repo: "owner/repo"}}, want: semanticModeFilteredExact},
		{name: "label qualifier forces filtered exact", sq: SearchQualifiers{CoreFilters: CoreFilters{Labels: [][]string{{"bug"}}}}, want: semanticModeFilteredExact},
		{name: "title only disables semantic", sq: SearchQualifiers{ParserFields: ParserFields{In: []string{"title"}}}, want: semanticModeDisabled},
		{name: "comments disable semantic", sq: SearchQualifiers{ParserFields: ParserFields{In: []string{"title", "comments"}}}, want: semanticModeDisabled},
		{name: "title and body keeps ann", sq: SearchQualifiers{ParserFields: ParserFields{In: []string{"title", "body"}}}, want: semanticModeANN},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := semanticModeForQuery(tt.sq, issueSemanticApplicable(tt.sq.In))
			if got != tt.want {
				t.Fatalf("expected semantic mode %v, got %v", tt.want, got)
			}
		})
	}
}

func TestSemanticCandidateLimit(t *testing.T) {
	tests := []struct {
		limit int
		want  int
	}{
		{limit: 0, want: 128},
		{limit: 1, want: 64},
		{limit: 20, want: 80},
		{limit: 200, want: 512},
	}

	for _, tt := range tests {
		if got := semanticCandidateLimit(tt.limit); got != tt.want {
			t.Fatalf("semanticCandidateLimit(%d) = %d, want %d", tt.limit, got, tt.want)
		}
	}
}

func TestBuildIssueANNCandidateQuery_UsesIndexFriendlyShape(t *testing.T) {
	gdb := newDryRunMySQLDB(t)

	sql := gdb.ToSQL(func(tx *gorm.DB) *gorm.DB {
		var ids []uint
		return buildIssueANNCandidateQuery(tx, "[0.1,0.2,0.3]", 25).Pluck("issues.id", &ids)
	})

	if !strings.Contains(sql, "FROM `issues`") {
		t.Fatalf("expected issues table in ANN query, got %q", sql)
	}
	if !strings.Contains(sql, "ORDER BY VEC_COSINE_DISTANCE(issues.embedding") {
		t.Fatalf("expected vector distance ordering, got %q", sql)
	}
	if strings.Contains(sql, " WHERE ") {
		t.Fatalf("expected ANN candidate query without WHERE prefilters, got %q", sql)
	}
	if !strings.Contains(sql, "LIMIT 100") {
		t.Fatalf("expected expanded ANN candidate limit, got %q", sql)
	}
}

func TestBuildIssueSemanticFilteredQuery_PreservesQualifiers(t *testing.T) {
	gdb := newDryRunMySQLDB(t)

	sql := gdb.ToSQL(func(tx *gorm.DB) *gorm.DB {
		baseQ := tx.Model(&modeldb.Issue{}).Where("issues.repository_id = ?", 42)
		var issues []modeldb.Issue
		return buildIssueSemanticFilteredQuery(baseQ, "[0.1,0.2,0.3]").Limit(10).Find(&issues)
	})

	if !strings.Contains(sql, "issues.repository_id = 42") {
		t.Fatalf("expected qualifier filter to be preserved, got %q", sql)
	}
	if !strings.Contains(sql, "issues.embedding IS NOT NULL") {
		t.Fatalf("expected non-null embedding filter, got %q", sql)
	}
	if !strings.Contains(sql, "ORDER BY VEC_COSINE_DISTANCE(issues.embedding") {
		t.Fatalf("expected exact distance ordering, got %q", sql)
	}
}

func TestBuildIssueSemanticFilteredRankQuery_SelectsIDsOnly(t *testing.T) {
	gdb := newDryRunMySQLDB(t)

	sql := gdb.ToSQL(func(tx *gorm.DB) *gorm.DB {
		baseQ := tx.Model(&modeldb.Issue{}).Where("issues.repository_id = ?", 42)
		var ids []uint
		return buildIssueSemanticFilteredRankQuery(baseQ, "[0.1,0.2,0.3]").Limit(10).Pluck("issues.id", &ids)
	})

	if strings.Contains(sql, "SELECT *") {
		t.Fatalf("expected rank query to avoid SELECT *, got %q", sql)
	}
	if !strings.Contains(sql, "SELECT issues.id") {
		t.Fatalf("expected rank query to select ids only, got %q", sql)
	}
	if !strings.Contains(sql, "issues.repository_id = 42") {
		t.Fatalf("expected qualifier filter to be preserved, got %q", sql)
	}
	if !strings.Contains(sql, "ORDER BY VEC_COSINE_DISTANCE(issues.embedding") {
		t.Fatalf("expected exact distance ordering, got %q", sql)
	}
}

func TestBuildPRANNCandidateQuery_UsesIndexFriendlyShape(t *testing.T) {
	gdb := newDryRunMySQLDB(t)

	sql := gdb.ToSQL(func(tx *gorm.DB) *gorm.DB {
		var ids []uint
		return buildPRANNCandidateQuery(tx, "[0.1,0.2,0.3]", 16).Pluck("pull_requests.id", &ids)
	})

	if !strings.Contains(sql, "FROM `pull_requests`") {
		t.Fatalf("expected pull_requests table in ANN query, got %q", sql)
	}
	if !strings.Contains(sql, "ORDER BY VEC_COSINE_DISTANCE(pull_requests.embedding") {
		t.Fatalf("expected vector distance ordering, got %q", sql)
	}
	if strings.Contains(sql, " WHERE ") {
		t.Fatalf("expected ANN candidate query without WHERE prefilters, got %q", sql)
	}
	if !strings.Contains(sql, "LIMIT 64") {
		t.Fatalf("expected expanded ANN candidate limit floor, got %q", sql)
	}
}

func TestBuildPRSemanticFilteredRankQuery_SelectsIDsOnly(t *testing.T) {
	gdb := newDryRunMySQLDB(t)

	sql := gdb.ToSQL(func(tx *gorm.DB) *gorm.DB {
		baseQ := tx.Model(&modeldb.PullRequest{}).Where("pull_requests.repository_id = ?", 42)
		var ids []uint
		return buildPRSemanticFilteredRankQuery(baseQ, "[0.1,0.2,0.3]").Limit(10).Pluck("pull_requests.id", &ids)
	})

	if strings.Contains(sql, "SELECT *") {
		t.Fatalf("expected rank query to avoid SELECT *, got %q", sql)
	}
	if !strings.Contains(sql, "SELECT pull_requests.id") {
		t.Fatalf("expected rank query to select ids only, got %q", sql)
	}
	if !strings.Contains(sql, "pull_requests.repository_id = 42") {
		t.Fatalf("expected qualifier filter to be preserved, got %q", sql)
	}
	if !strings.Contains(sql, "ORDER BY VEC_COSINE_DISTANCE(pull_requests.embedding") {
		t.Fatalf("expected exact distance ordering, got %q", sql)
	}
}
