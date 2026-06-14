package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/embedding"

	sqlite3 "github.com/mattn/go-sqlite3"
	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newWikiSearchDryRunMySQLDB(t *testing.T) *gorm.DB {
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

type blockingWikiSearchEmbedder struct {
	started chan string
	release chan struct{}
}

func (e *blockingWikiSearchEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	e.started <- text
	select {
	case <-e.release:
		return []float32{0.1, 0.2, 0.3}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *blockingWikiSearchEmbedder) Dimensions() int { return 3 }

func TestWikiSearchLikeEscapeClauseByDialect(t *testing.T) {
	tests := []struct {
		name string
		db   *gorm.DB
		want string
	}{
		{
			name: "mysql escapes backslash string literal",
			db:   &gorm.DB{Config: &gorm.Config{Dialector: mysql.Open("user:pass@tcp(host:3306)/db")}},
			want: ` ESCAPE '\\'`,
		},
		{
			name: "sqlite keeps single backslash escape character",
			db:   &gorm.DB{Config: &gorm.Config{Dialector: sqlite.Open(":memory:")}},
			want: ` ESCAPE '\'`,
		},
		{
			name: "nil database uses portable fallback",
			db:   nil,
			want: ` ESCAPE '\'`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wikiSearchLikeEscapeClause(tt.db); got != tt.want {
				t.Fatalf("wikiSearchLikeEscapeClause() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFuseWikiSearchResultsIncludesCrossWindowWinner(t *testing.T) {
	lexical := make([]WikiSearchResult, 0, 21)
	semantic := make([]WikiSearchResult, 0, 21)
	for i := 1; i <= 20; i++ {
		lexical = append(lexical, WikiSearchResult{
			Slug:  fmt.Sprintf("lexical-only-%02d", i),
			Score: 1,
		})
		semantic = append(semantic, WikiSearchResult{
			Slug:  fmt.Sprintf("semantic-only-%02d", i),
			Score: 1,
		})
	}
	lexical = append(lexical, WikiSearchResult{Slug: "joint-21", Score: 1})
	semantic = append(semantic, WikiSearchResult{Slug: "joint-21", Score: 1})

	results := paginateWikiSearchResultList(fuseWikiSearchResults(lexical, semantic), 1, 0)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Slug != "joint-21" {
		t.Fatalf("top fused slug = %q, want joint-21", results[0].Slug)
	}
}

func TestWikiSearchRankWindow(t *testing.T) {
	tests := []struct {
		name   string
		limit  int
		offset int
		want   int
	}{
		{name: "default page keeps buffered window", limit: 20, offset: 0, want: wikiSearchMinRankWindow},
		{name: "small limit still buffered", limit: 1, offset: 0, want: wikiSearchMinRankWindow},
		{name: "offset expands window", limit: 50, offset: 125, want: 175},
		{name: "deep offset expands lexical window", limit: 50, offset: 1000, want: 1050},
		{name: "invalid limit uses default", limit: -1, offset: 0, want: wikiSearchMinRankWindow},
		{name: "invalid offset normalizes", limit: 50, offset: -10, want: wikiSearchMinRankWindow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wikiSearchRankWindow(tt.limit, tt.offset); got != tt.want {
				t.Fatalf("wikiSearchRankWindow(%d, %d) = %d, want %d", tt.limit, tt.offset, got, tt.want)
			}
		})
	}
}

func TestStartWikiSearchEmbeddingRunsAsyncAndTruncates(t *testing.T) {
	embedder := &blockingWikiSearchEmbedder{
		started: make(chan string, 1),
		release: make(chan struct{}),
	}
	svc := &Service{Embedder: embedder}
	query := "semantic query" + strings.Repeat(" token", embedding.MaxInputTokens+512)

	resultC := svc.startWikiSearchEmbedding(context.Background(), query)
	if resultC == nil {
		t.Fatal("startWikiSearchEmbedding returned nil for configured embedder")
	}

	var embeddedText string
	select {
	case embeddedText = <-embedder.started:
	case <-time.After(time.Second):
		t.Fatal("embedding did not start asynchronously")
	}
	if tokens, err := embedding.CountInputTokens(embeddedText); err != nil {
		t.Fatalf("count embedded tokens: %v", err)
	} else if tokens > embedding.MaxInputTokens {
		t.Fatalf("embedded query has %d tokens, want <= %d", tokens, embedding.MaxInputTokens)
	}

	select {
	case <-resultC:
		t.Fatal("embedding result completed before release")
	default:
	}
	close(embedder.release)

	select {
	case result := <-resultC:
		if result.err != nil {
			t.Fatalf("embedding result err = %v", result.err)
		}
		if len(result.vec) != 3 {
			t.Fatalf("embedding vector length = %d, want 3", len(result.vec))
		}
		if result.query != embeddedText {
			t.Fatalf("result query = %q, want embedded text %q", result.query, embeddedText)
		}
	case <-time.After(time.Second):
		t.Fatal("embedding result did not complete after release")
	}
}

func TestWikiSemanticANNCandidateLimit(t *testing.T) {
	tests := []struct {
		name   string
		limit  int
		offset int
		want   int
	}{
		{name: "default", limit: 0, offset: 0, want: 256},
		{name: "small page uses floor", limit: 20, offset: 0, want: 256},
		{name: "offset expands candidates", limit: 50, offset: 500, want: 4096},
		{name: "deep offset caps candidates", limit: 50, offset: 2000, want: 4096},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wikiSemanticANNCandidateLimit(tt.limit, tt.offset); got != tt.want {
				t.Fatalf("wikiSemanticANNCandidateLimit(%d, %d) = %d, want %d", tt.limit, tt.offset, got, tt.want)
			}
		})
	}
}

func TestWikiSemanticRerankLimit(t *testing.T) {
	tests := []struct {
		name   string
		limit  int
		offset int
		want   int
	}{
		{name: "default", limit: 0, offset: 0, want: wikiSearchDefaultLimit},
		{name: "rank window", limit: wikiSearchMinRankWindow, offset: 0, want: wikiSearchMinRankWindow},
		{name: "pagination window", limit: 50, offset: 500, want: 550},
		{name: "caps at ANN candidate max", limit: 50, offset: 5000, want: wikiSemanticANNCandidateMax},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wikiSemanticRerankLimit(tt.limit, tt.offset); got != tt.want {
				t.Fatalf("wikiSemanticRerankLimit(%d, %d) = %d, want %d", tt.limit, tt.offset, got, tt.want)
			}
		})
	}
}

func TestBuildWikiSemanticANNCandidateQuery_UsesIndexFriendlyShape(t *testing.T) {
	gdb := newWikiSearchDryRunMySQLDB(t)

	sql := gdb.ToSQL(func(tx *gorm.DB) *gorm.DB {
		var ids []uint
		return buildWikiSemanticANNCandidateQuery(tx, "[0.1,0.2,0.3]", 25, 0).Pluck("wiki_search_documents.id", &ids)
	})

	if !strings.Contains(sql, "FROM `wiki_search_documents`") {
		t.Fatalf("expected wiki_search_documents table in ANN query, got %q", sql)
	}
	if !strings.Contains(sql, "SELECT `wiki_search_documents`.`id`") {
		t.Fatalf("expected ANN query to select ids only, got %q", sql)
	}
	if strings.Contains(sql, "SELECT *") {
		t.Fatalf("expected ANN query to avoid SELECT *, got %q", sql)
	}
	if strings.Contains(sql, " WHERE ") {
		t.Fatalf("expected ANN candidate query without WHERE prefilters, got %q", sql)
	}
	if !strings.Contains(sql, "ORDER BY VEC_COSINE_DISTANCE(wiki_search_documents.embedding") {
		t.Fatalf("expected vector distance ordering, got %q", sql)
	}
	if !strings.Contains(sql, "LIMIT 256") {
		t.Fatalf("expected ANN candidate limit floor, got %q", sql)
	}
}

func TestBuildWikiSemanticDBRowsQuery_LimitsRowsAndOmitsEmbeddingColumn(t *testing.T) {
	gdb := newWikiSearchDryRunMySQLDB(t)

	sql := gdb.ToSQL(func(tx *gorm.DB) *gorm.DB {
		var rows []wikiSemanticDBRow
		q := tx.Model(&db.WikiSearchDocument{}).
			Where("wiki_search_documents.repository_id = ?", 42).
			Where("wiki_search_documents.embedding IS NOT NULL").
			Where("wiki_search_documents.id IN ?", []uint{7, 8, 9})
		return buildWikiSemanticDBRowsQuery(q, "wiki-perf-seed-3000", "[0.1,0.2,0.3]", false, 100, 0).Scan(&rows)
	})

	for _, want := range []string{
		"SELECT wiki_search_documents.id,wiki_search_documents.repository_id,wiki_search_documents.slug,wiki_search_documents.title,wiki_search_documents.body,wiki_search_documents.revision_sha,wiki_search_documents.label_digest,wiki_search_documents.created_at,wiki_search_documents.updated_at, VEC_COSINE_DISTANCE(wiki_search_documents.embedding",
		"FROM `wiki_search_documents`",
		"wiki_search_documents.repository_id = 42",
		"wiki_search_documents.embedding IS NOT NULL",
		"wiki_search_documents.id IN (7,8,9)",
		"ORDER BY (semantic_distance - (label_score * 0.05)) ASC, wiki_search_documents.updated_at DESC, wiki_search_documents.slug ASC",
		"LIMIT 100",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("expected semantic row SQL to contain %q, got %q", want, sql)
		}
	}
	if strings.Contains(sql, "wiki_search_documents.*") || strings.Contains(sql, "SELECT *") {
		t.Fatalf("expected semantic row SQL to avoid SELECT *, got %q", sql)
	}
	selectList := sql
	if idx := strings.Index(selectList, "VEC_COSINE_DISTANCE"); idx >= 0 {
		selectList = selectList[:idx]
	}
	if strings.Contains(selectList, "wiki_search_documents.embedding") || strings.Contains(selectList, "`embedding`") {
		t.Fatalf("expected semantic row SQL to avoid selecting embedding column, got %q", sql)
	}
}

func TestCurrentWikiSearchDocumentsQuery_UsesTiDBCompatibleJoinShape(t *testing.T) {
	gdb := newWikiSearchDryRunMySQLDB(t)
	svc := &Service{}

	sql := gdb.ToSQL(func(tx *gorm.DB) *gorm.DB {
		svc.DB = tx
		var docs []db.WikiSearchDocument
		return svc.currentWikiSearchDocumentsQuery(context.Background(), 42).
			Order("wiki_search_documents.updated_at desc").
			Find(&docs)
	})

	for _, want := range []string{
		"FROM `wiki_search_documents`",
		"JOIN wiki_pages ON wiki_pages.repository_id = wiki_search_documents.repository_id AND wiki_pages.slug = wiki_search_documents.slug",
		"wiki_search_documents.repository_id = 42",
		"wiki_pages.deleted_at IS NULL",
		"wiki_search_documents.revision_sha = wiki_pages.head_blob_sha",
		"ORDER BY wiki_search_documents.updated_at desc",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("expected current wiki search SQL to contain %q, got %q", want, sql)
		}
	}
}

func TestBuildWikiSearchCurrentCandidateIDSQL_DelegatesIntersectionAndFiltersToDB(t *testing.T) {
	gdb := newWikiSearchDryRunMySQLDB(t)

	query := buildWikiSearchCurrentCandidateIDSQL(gdb, 42, []string{"alpha", "beta"}, []wikiSearchRawSQL{
		{
			SQL:  "EXISTS (SELECT 1 FROM wiki_page_labels WHERE wiki_page_labels.repository_id = wiki_search_documents.repository_id AND wiki_page_labels.slug = wiki_search_documents.slug AND wiki_page_labels.label_id = ?)",
			Args: []any{uint(7)},
		},
	}, 200)
	sql := query.SQL

	for _, want := range []string{
		"SELECT wiki_search_documents.id FROM wiki_search_documents",
		"JOIN wiki_pages ON wiki_pages.repository_id = wiki_search_documents.repository_id AND wiki_pages.slug = wiki_search_documents.slug",
		"JOIN (SELECT wiki_search_documents.id FROM wiki_search_documents",
		"FTS_MATCH_WORD('alpha', wiki_search_documents.title)",
		"FTS_MATCH_WORD('alpha', wiki_search_documents.body)",
		"FTS_MATCH_WORD('beta', wiki_search_documents.title)",
		"FTS_MATCH_WORD('beta', wiki_search_documents.body)",
		"wiki_search_documents.slug LIKE ? ESCAPE '\\\\'",
		"JOIN wiki_page_labels ON wiki_page_labels.repository_id = wiki_search_documents.repository_id AND wiki_page_labels.slug = wiki_search_documents.slug",
		"UNION",
		"AS token_0 ON token_0.id = wiki_search_documents.id",
		"AS token_1 ON token_1.id = wiki_search_documents.id",
		"wiki_search_documents.repository_id = ? AND wiki_pages.deleted_at IS NULL AND wiki_search_documents.revision_sha = wiki_pages.head_blob_sha",
		"EXISTS (SELECT 1 FROM wiki_page_labels",
		"ORDER BY wiki_search_documents.updated_at desc, wiki_search_documents.slug asc",
		"LIMIT ?",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("expected delegated current candidate SQL to contain %q, got %q", want, sql)
		}
	}
	if strings.Contains(sql, "SELECT *") || strings.Contains(sql, "wiki_search_documents.embedding") || strings.Contains(sql, "wiki_search_documents.body,") {
		t.Fatalf("expected candidate SQL to avoid hydrating large/vector columns, got %q", sql)
	}
	if strings.Contains(sql, " id IN ") || strings.Contains(sql, " id IN(") {
		t.Fatalf("expected token intersection to use derived joins instead of IN filters, got %q", sql)
	}
	labelFilterIdx := strings.LastIndex(sql, "label_id = ?")
	orderIdx := strings.Index(sql, "ORDER BY")
	limitIdx := strings.Index(sql, "LIMIT ?")
	if labelFilterIdx == -1 || orderIdx == -1 || limitIdx == -1 || labelFilterIdx > orderIdx || orderIdx > limitIdx {
		t.Fatalf("expected label filters before ORDER BY/LIMIT, got %q", sql)
	}
	if got, want := len(query.Args), 17; got != want {
		t.Fatalf("len(args) = %d, want %d (%#v)", got, want, query.Args)
	}
	if last := query.Args[len(query.Args)-1]; last != 200 {
		t.Fatalf("last arg = %#v, want candidate limit 200", last)
	}
}

func TestWikiSearchCurrentHydrationQuery_OmitsEmbedding(t *testing.T) {
	gdb := newWikiSearchDryRunMySQLDB(t)
	svc := &Service{}

	sql := gdb.ToSQL(func(tx *gorm.DB) *gorm.DB {
		svc.DB = tx
		q, noResults, err := svc.currentWikiSearchDocumentsByIDQuery(context.Background(), 42, []uint{7, 8}, WikiLabelFilters{})
		if err != nil {
			t.Fatalf("currentWikiSearchDocumentsByIDQuery: %v", err)
		}
		if noResults {
			t.Fatal("currentWikiSearchDocumentsByIDQuery returned noResults")
		}
		var docs []db.WikiSearchDocument
		return q.Order("wiki_search_documents.updated_at desc, wiki_search_documents.slug asc").
			Limit(200).
			Find(&docs)
	})

	for _, want := range []string{
		"SELECT wiki_search_documents.id,wiki_search_documents.repository_id,wiki_search_documents.slug,wiki_search_documents.title,wiki_search_documents.body,wiki_search_documents.revision_sha,wiki_search_documents.label_digest,wiki_search_documents.created_at,wiki_search_documents.updated_at",
		"JOIN wiki_pages ON wiki_pages.repository_id = wiki_search_documents.repository_id AND wiki_pages.slug = wiki_search_documents.slug",
		"wiki_search_documents.id IN (7,8)",
		"ORDER BY wiki_search_documents.updated_at desc, wiki_search_documents.slug asc",
		"LIMIT 200",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("expected hydration SQL to contain %q, got %q", want, sql)
		}
	}
	if strings.Contains(sql, "`embedding`") || strings.Contains(sql, "SELECT *") {
		t.Fatalf("expected hydration query to avoid embedding and SELECT *, got %q", sql)
	}
}

func TestSearchWikiSemanticANN_PostFiltersWithoutExactRepoFallback(t *testing.T) {
	driverName := fmt.Sprintf("sqlite3_wiki_ann_fallback_%d", time.Now().UnixNano())
	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return conn.RegisterFunc("VEC_COSINE_DISTANCE", func(embedding, query string) float64 {
				switch embedding {
				case "[1,0,0]":
					return 0
				case "[0.8,0.2,0]":
					return 0.1
				default:
					return 1
				}
			}, true)
		},
	})

	gdb, err := gorm.Open(sqlite.Dialector{DriverName: driverName, DSN: "file::memory:?cache=shared"}, &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	if err := gdb.AutoMigrate(&db.Repository{}, &db.Label{}, &db.WikiSearchDocument{}, &db.WikiPageLabel{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	if err := gdb.Exec("ALTER TABLE wiki_search_documents ADD COLUMN embedding TEXT").Error; err != nil {
		t.Fatalf("add embedding column: %v", err)
	}
	svc := &Service{DB: gdb}

	ctx := context.Background()
	repos := []db.Repository{
		{ID: 1, Name: "wiki-target", FullName: "target/wiki-target"},
		{ID: 2, Name: "wiki-noise", FullName: "noise/wiki-noise"},
	}
	if err := svc.DB.Create(&repos).Error; err != nil {
		t.Fatalf("seed repos: %v", err)
	}

	baseTime := time.Date(2026, time.January, 7, 0, 0, 0, 0, time.UTC)
	noiseDocs := make([]db.WikiSearchDocument, 0, wikiSemanticANNCandidateMin)
	for i := 0; i < wikiSemanticANNCandidateMin; i++ {
		noiseDocs = append(noiseDocs, db.WikiSearchDocument{
			RepositoryID: repos[1].ID,
			Slug:         fmt.Sprintf("noise-%03d", i),
			Title:        fmt.Sprintf("Noise %03d", i),
			Body:         db.LargeText("noise body"),
			RevisionSHA:  fmt.Sprintf("%040d", i+1),
			Embedding:    "[1,0,0]",
			CreatedAt:    baseTime.Add(time.Duration(i) * time.Second),
			UpdatedAt:    baseTime.Add(time.Duration(i) * time.Second),
		})
	}
	if err := svc.DB.CreateInBatches(noiseDocs, 64).Error; err != nil {
		t.Fatalf("seed noise docs: %v", err)
	}
	if err := svc.DB.Create(&db.WikiSearchDocument{
		RepositoryID: repos[0].ID,
		Slug:         "target-hit",
		Title:        "Target Hit",
		Body:         db.LargeText("target body"),
		RevisionSHA:  fmt.Sprintf("%040d", wikiSemanticANNCandidateMin+1),
		Embedding:    "[0.8,0.2,0]",
		CreatedAt:    baseTime.Add(300 * time.Second),
		UpdatedAt:    baseTime.Add(300 * time.Second),
	}).Error; err != nil {
		t.Fatalf("seed target doc: %v", err)
	}

	results, ok, err := svc.searchWikiSemanticANN(ctx, repos[0].ID, "semantic target", []float32{1, 0, 0}, 20, 0, WikiLabelFilters{}, false, true)
	if err != nil {
		t.Fatalf("searchWikiSemanticANN: %v", err)
	}
	if ok {
		t.Fatalf("expected ANN path to avoid exact fallback when candidate window misses repo docs, got results %#v", results)
	}
	if len(results) != 0 {
		t.Fatalf("results = %#v, want none without exact fallback", results)
	}
}

func TestBuildWikiTreePageRowsQuery_UsesCurrentPagesWithoutLargeFields(t *testing.T) {
	gdb := newWikiSearchDryRunMySQLDB(t)

	sql := gdb.ToSQL(func(tx *gorm.DB) *gorm.DB {
		var rows []wikiTreePageRow
		return buildWikiTreePageRowsQuery(tx, 42).Find(&rows)
	})

	if !strings.Contains(sql, "wiki_pages") {
		t.Fatalf("expected wiki_pages in live tree query, got %q", sql)
	}
	if strings.Contains(sql, "wiki_dir_index") {
		t.Fatalf("expected live tree query to avoid stale directory index rows, got %q", sql)
	}
	if strings.Contains(sql, "SELECT *") || strings.Contains(sql, "body_inline") {
		t.Fatalf("expected live tree query to select only sidebar fields, got %q", sql)
	}
	if strings.Contains(sql, " IN ") {
		t.Fatalf("expected live tree query to avoid page_id IN lists, got %q", sql)
	}
	if !strings.Contains(sql, "repository_id = 42") || !strings.Contains(sql, "deleted_at IS NULL") {
		t.Fatalf("expected repo and live-page predicates, got %q", sql)
	}
}

func TestWikiGitCandidateMatchesAllTokensAcrossTitleSlugAndBody(t *testing.T) {
	bodyMatches := map[string]map[string]struct{}{
		"byoc": {
			"calls/demo": {},
		},
		"plan": {
			"calls/demo":      {},
			"calls/byoc-demo": {},
		},
	}

	if !wikiGitCandidateMatchesAllTokens("How's migration", "calls/demo", []string{"how's", "byoc"}, bodyMatches) {
		t.Fatal("expected title token plus body token to match")
	}
	if !wikiGitCandidateMatchesAllTokens("Migration", "calls/byoc-demo", []string{"byoc", "plan"}, bodyMatches) {
		t.Fatal("expected slug token plus body token to match")
	}
	if wikiGitCandidateMatchesAllTokens("Migration", "calls/demo", []string{"how's", "missing"}, bodyMatches) {
		t.Fatal("expected missing token to fail")
	}
}

func TestTruncateWikiSearchResultListCopiesWindow(t *testing.T) {
	results := []WikiSearchResult{{Slug: "a"}, {Slug: "b"}, {Slug: "c"}}
	got := truncateWikiSearchResultList(results, 2)
	if len(got) != 2 || got[0].Slug != "a" || got[1].Slug != "b" {
		t.Fatalf("truncate result = %#v, want first two", got)
	}
	got[0].Slug = "changed"
	if results[0].Slug != "a" {
		t.Fatal("truncate should copy the returned window")
	}
}
