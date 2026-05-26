package service

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/embedding"
	applog "gh-server/internal/logging"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	wikiSearchDefaultLimit = 20
	wikiSearchMaxLimit     = 50
	wikiSnippetBudget      = 180
	wikiSemanticMinScore   = 0.2
	// When lexical search already found concrete token matches, keep
	// semantic-only additions to high-confidence neighbors so short literal
	// queries do not get flooded by weak vector nearest-neighbor noise.
	wikiSemanticOnlyMinScoreWithLexical = 0.5
	wikiSemanticMaxExact                = 1000
)

type WikiSearchResult struct {
	Slug    string
	Title   string
	Score   float64
	Snippet string
	Labels  []db.Label
}

type WikiSearchResponse struct {
	Results   []WikiSearchResult
	Query     string
	Method    string
	ElapsedMS int64
}

type WikiSearchOptions struct {
	Limit         int
	Offset        int
	Labels        []string
	ExcludeLabels []string
}

func clampWikiSearchLimit(limit int) int {
	switch {
	case limit <= 0:
		return wikiSearchDefaultLimit
	case limit > wikiSearchMaxLimit:
		return wikiSearchMaxLimit
	default:
		return limit
	}
}

func normalizeWikiSearchOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}

func (s *Service) SearchWikiPages(ctx context.Context, repoFullName, query string, limit, offset int) (WikiSearchResponse, error) {
	return s.SearchWikiPagesWithOptions(ctx, repoFullName, query, WikiSearchOptions{Limit: limit, Offset: offset})
}

func (s *Service) SearchWikiPagesWithOptions(ctx context.Context, repoFullName, query string, opts WikiSearchOptions) (WikiSearchResponse, error) {
	start := time.Now()
	query = strings.TrimSpace(query)
	if query == "" {
		return WikiSearchResponse{
			Results:   []WikiSearchResult{},
			Query:     query,
			Method:    "substring",
			ElapsedMS: time.Since(start).Milliseconds(),
		}, nil
	}

	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return WikiSearchResponse{}, err
	}
	limit := clampWikiSearchLimit(opts.Limit)
	offset := normalizeWikiSearchOffset(opts.Offset)
	labelFilters := WikiLabelFilters{Labels: opts.Labels, ExcludeLabels: opts.ExcludeLabels}

	method := "substring"
	lexical, err := s.searchWikiLexical(ctx, repo.ID, query, labelFilters)
	if err != nil {
		slog.WarnContext(ctx, "wiki search indexed path failed; falling back to git scan", "repo", repo.FullName, "error", err)
		lexical, err = s.searchWikiLexicalFromGit(ctx, repoFullName, query, labelFilters)
		if err != nil {
			return WikiSearchResponse{}, err
		}
	}
	results := paginateWikiSearchResultList(lexical, limit, offset)

	if s.Embedder != nil && !embedding.IsNop(s.Embedder) {
		if semantic, ok, semanticErr := s.searchWikiSemantic(ctx, repo.ID, query, labelFilters, limit, offset, len(lexical) == 0); semanticErr != nil {
			slog.WarnContext(ctx, "wiki search semantic path failed; falling back to substring", "repo", repo.FullName, "error", semanticErr)
		} else if ok {
			method = "vector"
			if len(lexical) == 0 {
				results = semantic
			} else {
				results = fuseWikiSearchResults(lexical, semantic, limit, offset)
			}
		}
	}

	return WikiSearchResponse{
		Results:   results,
		Query:     query,
		Method:    method,
		ElapsedMS: time.Since(start).Milliseconds(),
	}, nil
}

func (s *Service) searchWikiLexical(ctx context.Context, repoID uint, query string, filters WikiLabelFilters) ([]WikiSearchResult, error) {
	if db.SupportsTiDBSearch(s.DBForCtx(ctx)) {
		docs, err := s.wikiSearchDocumentsFullText(ctx, repoID, query, filters)
		if err == nil {
			return s.rankWikiLexicalDocuments(ctx, repoID, docs, query)
		}
		slog.WarnContext(ctx, "wiki search TiDB full-text query failed; falling back to LIKE", "repo_id", repoID, "error", err)
	}

	docs, err := s.wikiSearchDocuments(ctx, repoID, query, false, filters)
	if err != nil {
		return nil, err
	}
	return s.rankWikiLexicalDocuments(ctx, repoID, docs, query)
}

func (s *Service) rankWikiLexicalDocuments(ctx context.Context, repoID uint, docs []db.WikiSearchDocument, query string) ([]WikiSearchResult, error) {
	if err := s.refreshStaleWikiSearchTitles(ctx, docs); err != nil {
		return nil, err
	}
	labelsBySlug, err := s.wikiSearchLabelsBySlug(ctx, repoID, docs)
	if err != nil {
		return nil, err
	}

	scored := make([]wikiScoredDocument, 0, len(docs))
	for _, doc := range docs {
		labels := labelsBySlug[doc.Slug]
		score := 0.0
		if wikiTextContainsAllTokens(doc.Title, doc.Slug, string(doc.Body), query) {
			score += lexicalScore(doc.Title, doc.Slug, string(doc.Body), query)
		}
		score += wikiLabelLexicalScore(labels, query)
		if score <= 0 {
			continue
		}
		scored = append(scored, wikiScoredDocument{doc: doc, score: score, labels: labels})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			if scored[i].doc.UpdatedAt.Equal(scored[j].doc.UpdatedAt) {
				return scored[i].doc.Slug < scored[j].doc.Slug
			}
			return scored[i].doc.UpdatedAt.After(scored[j].doc.UpdatedAt)
		}
		return scored[i].score > scored[j].score
	})
	return buildWikiSearchResults(scored, query), nil
}

func (s *Service) searchWikiLexicalFromGit(ctx context.Context, repoFullName, query string, filters WikiLabelFilters) ([]WikiSearchResult, error) {
	pages, err := s.ListWikiPages(ctx, repoFullName, ListWikiPagesOptions{
		Recursive:     true,
		Labels:        filters.Labels,
		ExcludeLabels: filters.ExcludeLabels,
	})
	if err != nil {
		return nil, err
	}

	scored := make([]wikiScoredDocument, 0, len(pages))
	for _, summary := range pages {
		page, err := s.GetWikiPage(ctx, repoFullName, summary.Slug)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		score := 0.0
		if wikiTextContainsAllTokens(page.Title, page.Slug, page.Body, query) {
			score += lexicalScore(page.Title, page.Slug, page.Body, query)
		}
		score += wikiLabelLexicalScore(page.Labels, query)
		if score <= 0 {
			continue
		}
		scored = append(scored, wikiScoredDocument{
			doc: db.WikiSearchDocument{
				Slug:      page.Slug,
				Title:     page.Title,
				Body:      db.LargeText(page.Body),
				UpdatedAt: page.UpdatedAt,
			},
			score:  score,
			labels: page.Labels,
		})
	}
	sortWikiScoredDocuments(scored)
	return buildWikiSearchResults(scored, query), nil
}

func escapeWikiSearchLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}

type wikiScoredDocument struct {
	doc    db.WikiSearchDocument
	score  float64
	labels []db.Label
}

func wikiSearchMySQLStringLiteral(s string) string {
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

func wikiSearchFullTextSubquery(database *gorm.DB, column, token string) *gorm.DB {
	field := "wiki_search_documents.body"
	if column == "title" {
		field = "wiki_search_documents.title"
	}
	return database.Session(&gorm.Session{NewDB: true}).
		Table("wiki_search_documents").
		Select("wiki_search_documents.id").
		Where("FTS_MATCH_WORD(" + wikiSearchMySQLStringLiteral(token) + ", " + field + ")")
}

func wikiSearchLabelTokenExistsSQL(likeEscape string) string {
	return "EXISTS (" +
		"SELECT 1 FROM wiki_page_labels " +
		"JOIN labels ON labels.id = wiki_page_labels.label_id " +
		"WHERE wiki_page_labels.repository_id = wiki_search_documents.repository_id " +
		"AND wiki_page_labels.slug = wiki_search_documents.slug " +
		"AND (labels.name LIKE ?" + likeEscape + " OR labels.description LIKE ?" + likeEscape + ")" +
		")"
}

func (s *Service) applyWikiSearchLabelPredicates(ctx context.Context, repoID uint, q *gorm.DB, filters WikiLabelFilters) (*gorm.DB, bool, error) {
	if !hasWikiLabelFilters(filters) {
		return q, false, nil
	}
	for _, labelName := range uniqueLabelNames(filters.Labels) {
		label, err := s.repoLabelByName(ctx, repoID, labelName)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return q.Where("1 = 0"), true, nil
			}
			return nil, false, err
		}
		q = q.Where(
			"EXISTS (SELECT 1 FROM wiki_page_labels WHERE wiki_page_labels.repository_id = wiki_search_documents.repository_id AND wiki_page_labels.slug = wiki_search_documents.slug AND wiki_page_labels.label_id = ?)",
			label.ID,
		)
	}

	excludeLabels, err := s.resolveRepoLabels(ctx, repoID, filters.ExcludeLabels)
	if err != nil {
		return nil, false, err
	}
	if len(excludeLabels) > 0 {
		labelIDs := make([]uint, 0, len(excludeLabels))
		for _, label := range excludeLabels {
			labelIDs = append(labelIDs, label.ID)
		}
		q = q.Where(
			"NOT EXISTS (SELECT 1 FROM wiki_page_labels WHERE wiki_page_labels.repository_id = wiki_search_documents.repository_id AND wiki_page_labels.slug = wiki_search_documents.slug AND wiki_page_labels.label_id IN ?)",
			labelIDs,
		)
	}
	return q, false, nil
}

func (s *Service) wikiSearchDocumentsFullText(ctx context.Context, repoID uint, query string, filters WikiLabelFilters) ([]db.WikiSearchDocument, error) {
	database := s.DBForCtx(ctx)
	q := database.Model(&db.WikiSearchDocument{}).Where("wiki_search_documents.repository_id = ?", repoID)
	var noResults bool
	var err error
	q, noResults, err = s.applyWikiSearchLabelPredicates(ctx, repoID, q, filters)
	if err != nil {
		return nil, err
	}
	if noResults {
		return []db.WikiSearchDocument{}, nil
	}

	likeEscape := wikiSearchLikeEscapeClause(database)
	for _, token := range wikiSearchTokens(query) {
		like := "%" + escapeWikiSearchLike(token) + "%"
		q = q.Where(
			"(wiki_search_documents.id IN (?) OR wiki_search_documents.id IN (?) OR wiki_search_documents.slug LIKE ?"+likeEscape+" OR "+wikiSearchLabelTokenExistsSQL(likeEscape)+")",
			wikiSearchFullTextSubquery(database, "title", token),
			wikiSearchFullTextSubquery(database, "body", token),
			like,
			like,
			like,
		)
	}

	var docs []db.WikiSearchDocument
	if err := q.Order("wiki_search_documents.updated_at desc").Find(&docs).Error; err != nil {
		return nil, err
	}
	return docs, nil
}

func (s *Service) searchWikiSemantic(ctx context.Context, repoID uint, query string, filters WikiLabelFilters, limit, offset int, lexicalEmpty bool) ([]WikiSearchResult, bool, error) {
	query = embedding.TruncateInput(query)
	vec, err := s.Embedder.Embed(ctx, query)
	if err != nil {
		return nil, false, err
	}
	if len(vec) == 0 {
		return nil, false, nil
	}
	if !db.SupportsVectorDistance(s.DBForCtx(ctx)) {
		return s.searchWikiSemanticInMemory(ctx, repoID, query, vec, limit, offset, filters, !lexicalEmpty)
	}
	if lexicalEmpty {
		return s.searchWikiSemanticDB(ctx, repoID, query, vec, limit, offset, filters, false)
	}
	return s.searchWikiSemanticDB(ctx, repoID, query, vec, wikiSemanticMaxExact, 0, filters, true)
}

func (s *Service) searchWikiSemanticInMemory(ctx context.Context, repoID uint, query string, vec []float32, limit, offset int, filters WikiLabelFilters, forFusion bool) ([]WikiSearchResult, bool, error) {
	docs, err := s.wikiSearchDocuments(ctx, repoID, query, true, filters)
	if err != nil {
		return nil, false, err
	}
	if len(docs) == 0 {
		return nil, false, nil
	}
	if err := s.refreshStaleWikiSearchTitles(ctx, docs); err != nil {
		return nil, false, err
	}
	labelsBySlug, err := s.wikiSearchLabelsBySlug(ctx, repoID, docs)
	if err != nil {
		return nil, false, err
	}

	scored := make([]wikiScoredDocument, 0, len(docs))
	for _, doc := range docs {
		storedVec, ok := parseStoredEmbedding(doc.Embedding)
		if !ok || len(storedVec) != len(vec) {
			continue
		}
		score := cosineSimilarity(storedVec, vec)
		if math.IsNaN(score) || math.IsInf(score, 0) || score < wikiSemanticMinScore {
			continue
		}
		labels := labelsBySlug[doc.Slug]
		score += wikiLabelLexicalScore(labels, query) * 0.05
		scored = append(scored, wikiScoredDocument{doc: doc, score: score, labels: labels})
	}
	if len(scored) == 0 {
		return nil, false, nil
	}
	sortWikiScoredDocuments(scored)
	if !forFusion {
		return paginateWikiSearchResults(scored, query, limit, offset), true, nil
	}
	return buildWikiSearchResults(scored, query), true, nil
}

type wikiSemanticDBRow struct {
	db.WikiSearchDocument `gorm:"embedded"`
	SemanticDistance      float64 `gorm:"column:semantic_distance"`
	LabelScore            float64 `gorm:"column:label_score"`
}

func wikiSemanticExactLimit(limit, offset int) int {
	if offset > wikiSemanticMaxExact {
		return 0
	}
	n := offset + limit
	if n <= 0 {
		n = wikiSearchDefaultLimit
	}
	if n > wikiSemanticMaxExact {
		n = wikiSemanticMaxExact
	}
	return n
}

func (s *Service) searchWikiSemanticDB(ctx context.Context, repoID uint, query string, vec []float32, limit, offset int, filters WikiLabelFilters, exactWindow bool) ([]WikiSearchResult, bool, error) {
	candidateLimit := wikiSemanticPageLimit(limit)
	dbOffset := offset
	if exactWindow {
		candidateLimit = wikiSemanticExactLimit(limit, offset)
		dbOffset = 0
		if candidateLimit == 0 {
			return nil, false, nil
		}
	}
	vecLiteral := embedding.FormatVector(vec)
	database := s.DBForCtx(ctx)
	q := database.Model(&db.WikiSearchDocument{}).
		Where("wiki_search_documents.repository_id = ?", repoID).
		Where("wiki_search_documents.embedding IS NOT NULL")
	var noResults bool
	var err error
	q, noResults, err = s.applyWikiSearchLabelPredicates(ctx, repoID, q, filters)
	if err != nil {
		return nil, false, err
	}
	if noResults {
		return nil, false, nil
	}
	var rows []wikiSemanticDBRow
	selectSQL := "wiki_search_documents.*, VEC_COSINE_DISTANCE(wiki_search_documents.embedding, ?) AS semantic_distance"
	orderSQL := "semantic_distance ASC, wiki_search_documents.updated_at DESC, wiki_search_documents.slug ASC"
	selectArgs := []any{vecLiteral}
	if !exactWindow {
		labelScoreSQL, labelScoreArgs := wikiSearchSemanticLabelScoreSQL(query)
		selectSQL += ", " + labelScoreSQL + " AS label_score"
		selectArgs = append(selectArgs, labelScoreArgs...)
		orderSQL = "(semantic_distance - (label_score * 0.05)) ASC, wiki_search_documents.updated_at DESC, wiki_search_documents.slug ASC"
	}
	queryDB := q.
		Select(selectSQL, selectArgs...).
		Clauses(clause.OrderBy{Expression: clause.Expr{SQL: orderSQL}}).
		Offset(dbOffset).
		Limit(candidateLimit)
	err = queryDB.Scan(&rows).Error
	if err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, false, nil
	}

	docs := make([]db.WikiSearchDocument, 0, len(rows))
	for _, row := range rows {
		docs = append(docs, row.WikiSearchDocument)
	}
	if err := s.refreshStaleWikiSearchTitles(ctx, docs); err != nil {
		return nil, false, err
	}
	labelsBySlug, err := s.wikiSearchLabelsBySlug(ctx, repoID, docs)
	if err != nil {
		return nil, false, err
	}

	scored := make([]wikiScoredDocument, 0, len(rows))
	for i, row := range rows {
		score := 1 - row.SemanticDistance
		if math.IsNaN(score) || math.IsInf(score, 0) || score < wikiSemanticMinScore {
			continue
		}
		doc := docs[i]
		labels := labelsBySlug[doc.Slug]
		if exactWindow {
			score += wikiLabelLexicalScore(labels, query) * 0.05
		} else {
			score += row.LabelScore * 0.05
		}
		scored = append(scored, wikiScoredDocument{doc: doc, score: score, labels: labels})
	}
	if len(scored) == 0 {
		return nil, false, nil
	}
	sortWikiScoredDocuments(scored)
	if exactWindow {
		return paginateWikiSearchResults(scored, query, limit, offset), true, nil
	}
	return buildWikiSearchResults(scored, query), true, nil
}

func wikiSemanticPageLimit(limit int) int {
	if limit <= 0 {
		return wikiSearchDefaultLimit
	}
	return limit
}

func wikiSearchSemanticLabelScoreSQL(query string) (string, []any) {
	tokens := wikiSearchTokens(query)
	if len(tokens) == 0 {
		return "0", nil
	}

	scoreTerms := make([]string, 0, len(tokens)*2)
	args := make([]any, 0, len(tokens)*2)
	for _, token := range tokens {
		like := "%" + strings.ToLower(escapeWikiSearchLike(token)) + "%"
		scoreTerms = append(scoreTerms, "CASE WHEN LOWER(labels.name) LIKE ? THEN 3 ELSE 0 END")
		args = append(args, like)
		scoreTerms = append(scoreTerms, "CASE WHEN LOWER(labels.description) LIKE ? THEN 1.5 ELSE 0 END")
		args = append(args, like)
	}
	scoreExpr := strings.Join(scoreTerms, " + ")
	sql := "COALESCE((" +
		"SELECT SUM(" + scoreExpr + ") " +
		"FROM wiki_page_labels " +
		"JOIN labels ON labels.id = wiki_page_labels.label_id " +
		"WHERE wiki_page_labels.repository_id = wiki_search_documents.repository_id " +
		"AND wiki_page_labels.slug = wiki_search_documents.slug" +
		"), 0)"
	return sql, args
}

func sortWikiScoredDocuments(scored []wikiScoredDocument) {
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			if scored[i].doc.UpdatedAt.Equal(scored[j].doc.UpdatedAt) {
				return scored[i].doc.Slug < scored[j].doc.Slug
			}
			return scored[i].doc.UpdatedAt.After(scored[j].doc.UpdatedAt)
		}
		return scored[i].score > scored[j].score
	})
}

func paginateWikiSearchResults(scored []wikiScoredDocument, query string, limit, offset int) []WikiSearchResult {
	return paginateWikiSearchResultList(buildWikiSearchResults(scored, query), limit, offset)
}

func buildWikiSearchResults(scored []wikiScoredDocument, query string) []WikiSearchResult {
	if len(scored) == 0 {
		return []WikiSearchResult{}
	}
	out := make([]WikiSearchResult, 0, len(scored))
	for _, row := range scored {
		out = append(out, WikiSearchResult{
			Slug:    row.doc.Slug,
			Title:   titleFromSlug(row.doc.Slug),
			Score:   roundWikiScore(row.score),
			Snippet: buildWikiSnippet(string(row.doc.Body), query),
			Labels:  row.labels,
		})
	}
	return out
}

func paginateWikiSearchResultList(results []WikiSearchResult, limit, offset int) []WikiSearchResult {
	if offset >= len(results) {
		return []WikiSearchResult{}
	}
	end := offset + limit
	if end > len(results) {
		end = len(results)
	}
	out := make([]WikiSearchResult, end-offset)
	copy(out, results[offset:end])
	return out
}

type wikiFusedSearchResult struct {
	result       WikiSearchResult
	score        float64
	lexicalRank  int
	semanticRank int
}

func wikiReciprocalRankScore(rank int) float64 {
	if rank <= 0 {
		return 0
	}
	return 1.0 / (60.0 + float64(rank))
}

func fuseWikiSearchResults(lexical, semantic []WikiSearchResult, limit, offset int) []WikiSearchResult {
	bySlug := make(map[string]*wikiFusedSearchResult, len(lexical)+len(semantic))
	for idx, result := range lexical {
		rank := idx + 1
		entry := &wikiFusedSearchResult{
			result:      result,
			score:       wikiReciprocalRankScore(rank),
			lexicalRank: rank,
		}
		bySlug[result.Slug] = entry
	}
	for idx, result := range semantic {
		rank := idx + 1
		entry := bySlug[result.Slug]
		if entry == nil {
			if len(lexical) > 0 && result.Score < wikiSemanticOnlyMinScoreWithLexical {
				continue
			}
			entry = &wikiFusedSearchResult{result: result}
			bySlug[result.Slug] = entry
		} else if result.Score > entry.result.Score {
			entry.result.Score = result.Score
		}
		entry.score += wikiReciprocalRankScore(rank)
		entry.semanticRank = rank
	}

	ranked := make([]wikiFusedSearchResult, 0, len(bySlug))
	for _, entry := range bySlug {
		ranked = append(ranked, *entry)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			if (ranked[i].lexicalRank > 0) != (ranked[j].lexicalRank > 0) {
				return ranked[i].lexicalRank > 0
			}
			if (ranked[i].semanticRank > 0) != (ranked[j].semanticRank > 0) {
				return ranked[i].semanticRank > 0
			}
			return ranked[i].result.Slug < ranked[j].result.Slug
		}
		return ranked[i].score > ranked[j].score
	})

	results := make([]WikiSearchResult, 0, len(ranked))
	for _, entry := range ranked {
		results = append(results, entry.result)
	}
	return paginateWikiSearchResultList(results, limit, offset)
}

func (s *Service) wikiSearchDocuments(ctx context.Context, repoID uint, query string, requireEmbedding bool, filters WikiLabelFilters) ([]db.WikiSearchDocument, error) {
	var docs []db.WikiSearchDocument
	database := s.DBForCtx(ctx)
	q := database.Model(&db.WikiSearchDocument{}).Where("wiki_search_documents.repository_id = ?", repoID)
	if requireEmbedding {
		q = q.Where("wiki_search_documents.embedding <> ''")
	}
	if !requireEmbedding {
		if tokens := wikiSearchTokens(query); len(tokens) > 0 {
			likeEscape := wikiSearchLikeEscapeClause(database)
			q = q.
				Joins("LEFT JOIN wiki_page_labels ON wiki_page_labels.repository_id = wiki_search_documents.repository_id AND wiki_page_labels.slug = wiki_search_documents.slug").
				Joins("LEFT JOIN labels ON labels.id = wiki_page_labels.label_id")
			for _, token := range tokens {
				like := "%" + escapeWikiSearchLike(token) + "%"
				q = q.Where(
					"(wiki_search_documents.title LIKE ?"+likeEscape+" OR wiki_search_documents.slug LIKE ?"+likeEscape+" OR wiki_search_documents.body LIKE ?"+likeEscape+" OR labels.name LIKE ?"+likeEscape+" OR labels.description LIKE ?"+likeEscape+")",
					like, like, like, like, like,
				)
			}
			q = q.Distinct(
				"wiki_search_documents.id",
				"wiki_search_documents.repository_id",
				"wiki_search_documents.slug",
				"wiki_search_documents.title",
				"wiki_search_documents.body",
				"wiki_search_documents.revision_sha",
				"wiki_search_documents.created_at",
				"wiki_search_documents.updated_at",
			)
		}
	}
	if err := q.Order("wiki_search_documents.updated_at desc").Find(&docs).Error; err != nil {
		return nil, err
	}
	if !hasWikiLabelFilters(filters) || len(docs) == 0 {
		return docs, nil
	}
	slugs := make([]string, 0, len(docs))
	for _, doc := range docs {
		slugs = append(slugs, doc.Slug)
	}
	allowed, noResults, err := s.wikiSlugsMatchingLabelFilters(ctx, repoID, slugs, filters)
	if err != nil {
		return nil, err
	}
	if noResults {
		return []db.WikiSearchDocument{}, nil
	}
	filtered := make([]db.WikiSearchDocument, 0, len(docs))
	for _, doc := range docs {
		if _, ok := allowed[doc.Slug]; ok {
			filtered = append(filtered, doc)
		}
	}
	return filtered, nil
}

func (s *Service) refreshStaleWikiSearchTitles(ctx context.Context, docs []db.WikiSearchDocument) error {
	for i := range docs {
		title := titleFromSlug(docs[i].Slug)
		if docs[i].Title == title {
			continue
		}
		if err := s.DBForCtx(ctx).
			Model(&db.WikiSearchDocument{}).
			Where("id = ?", docs[i].ID).
			Update("title", title).
			Error; err != nil {
			return err
		}
		docs[i].Title = title
	}
	return nil
}

func wikiSearchLikeEscapeClause(database *gorm.DB) string {
	if database != nil && database.Dialector != nil && database.Dialector.Name() == "mysql" {
		return ` ESCAPE '\\'`
	}
	return ` ESCAPE '\'`
}

func (s *Service) wikiSearchLabelsBySlug(ctx context.Context, repoID uint, docs []db.WikiSearchDocument) (map[string][]db.Label, error) {
	slugs := make([]string, 0, len(docs))
	for _, doc := range docs {
		slugs = append(slugs, doc.Slug)
	}
	return s.wikiLabelsForSlugs(ctx, repoID, slugs)
}

func roundWikiScore(score float64) float64 {
	return math.Round(score*1000) / 1000
}

func lexicalScore(title, slug, body, query string) float64 {
	score := 0.0
	titleLower := strings.ToLower(title)
	slugLower := strings.ToLower(slug)
	bodyLower := strings.ToLower(body)
	for _, token := range wikiSearchTokens(query) {
		tokenLower := strings.ToLower(token)
		score += float64(strings.Count(bodyLower, tokenLower))
		score += float64(strings.Count(slugLower, tokenLower)) * 1.5
		score += float64(strings.Count(titleLower, tokenLower)) * 2
	}
	return score
}

func wikiTextContainsAllTokens(title, slug, body, query string) bool {
	titleLower := strings.ToLower(title)
	slugLower := strings.ToLower(slug)
	bodyLower := strings.ToLower(body)
	for _, token := range wikiSearchTokens(query) {
		token = strings.ToLower(token)
		if token == "" {
			continue
		}
		if !strings.Contains(titleLower, token) && !strings.Contains(slugLower, token) && !strings.Contains(bodyLower, token) {
			return false
		}
	}
	return true
}

var whitespaceRE = regexp.MustCompile(`\s+`)

func wikiSearchTokens(query string) []string {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return nil
	}
	out := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, token := range fields {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		lower := strings.ToLower(token)
		if _, ok := seen[lower]; ok {
			continue
		}
		seen[lower] = struct{}{}
		out = append(out, token)
	}
	return out
}

func buildWikiSnippet(body, query string) string {
	plain := strings.TrimSpace(whitespaceRE.ReplaceAllString(body, " "))
	if plain == "" {
		return ""
	}
	start := 0
	lowerPlain := strings.ToLower(plain)
	for _, token := range wikiSearchTokens(query) {
		if idx := strings.Index(lowerPlain, strings.ToLower(token)); idx >= 0 {
			start = idx
			break
		}
	}
	if start > wikiSnippetBudget/2 {
		start -= wikiSnippetBudget / 2
	}
	if start < 0 {
		start = 0
	}
	end := start + wikiSnippetBudget
	if end > len(plain) {
		end = len(plain)
	}
	snippet := strings.TrimSpace(plain[start:end])
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(plain) {
		snippet += "..."
	}
	return highlightSnippet(snippet, query)
}

func highlightSnippet(snippet, query string) string {
	out := html.EscapeString(snippet)
	for _, token := range wikiSearchTokens(query) {
		if token == "" {
			continue
		}
		re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(html.EscapeString(token)))
		out = re.ReplaceAllStringFunc(out, func(match string) string {
			return "<mark>" + match + "</mark>"
		})
	}
	return out
}

func (s *Service) queueWikiSearchUpsert(ctx context.Context, repoFullName string, page WikiPage) {
	s.Wg.Add(1)
	go func() {
		defer s.Wg.Done()
		bgCtx := applog.CloneContext(s.ServerCtx(), ctx)
		if tenantDB, ok := DBFromContext(ctx); ok {
			bgCtx = ContextWithDB(bgCtx, tenantDB)
		}
		repo, err := s.LookupRepoIdentity(bgCtx, repoFullName)
		if err != nil {
			slog.WarnContext(bgCtx, "wiki search index update skipped", "repo", repoFullName, "slug", page.Slug, "error", err)
			return
		}
		mu := s.getWikiMigrationSyncMu(s.wikiRepoKey(bgCtx, repo))
		mu.Lock()
		err = s.upsertWikiSearchDocument(bgCtx, repoFullName, page)
		mu.Unlock()
		if err != nil {
			slog.WarnContext(bgCtx, "wiki search index update failed", "repo", repoFullName, "slug", page.Slug, "error", err)
		}
	}()
}

func (s *Service) queueWikiSearchDelete(ctx context.Context, repoFullName, slug string) {
	s.Wg.Add(1)
	go func() {
		defer s.Wg.Done()
		bgCtx := applog.CloneContext(s.ServerCtx(), ctx)
		if tenantDB, ok := DBFromContext(ctx); ok {
			bgCtx = ContextWithDB(bgCtx, tenantDB)
		}
		repo, err := s.LookupRepoIdentity(bgCtx, repoFullName)
		if err != nil {
			slog.WarnContext(bgCtx, "wiki search index delete skipped", "repo", repoFullName, "slug", slug, "error", err)
			return
		}
		mu := s.getWikiMigrationSyncMu(s.wikiRepoKey(bgCtx, repo))
		mu.Lock()
		err = s.deleteWikiSearchDocument(bgCtx, repoFullName, slug)
		mu.Unlock()
		if err != nil {
			slog.WarnContext(bgCtx, "wiki search index delete failed", "repo", repoFullName, "slug", slug, "error", err)
		}
	}()
}

func (s *Service) upsertWikiSearchDocument(ctx context.Context, repoFullName string, page WikiPage) error {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return err
	}
	targetDB := s.DBForCtx(ctx)
	title := titleFromSlug(page.Slug)
	now := time.Now()
	values := map[string]any{
		"repository_id": repo.ID,
		"slug":          page.Slug,
		"title":         title,
		"body":          db.LargeText(page.Body),
		"revision_sha":  page.SHA,
		"created_at":    now,
		"updated_at":    now,
	}
	updateColumns := []string{"title", "body", "revision_sha", "updated_at"}
	if s.Embedder != nil && !embedding.IsNop(s.Embedder) {
		text := title + "\n" + wikiPageLabelsText(page.Labels) + "\n" + page.Body
		hasEmbeddingColumn := targetDB.Migrator().HasColumn("wiki_search_documents", "embedding")
		vec, err := s.embedWithRetry(ctx, text)
		if err != nil {
			slog.WarnContext(ctx, "wiki search embedding failed; storing lexical document only", "repo", repoFullName, "slug", page.Slug, "error", err)
			if hasEmbeddingColumn {
				values["embedding"] = nil
				updateColumns = append(updateColumns, "embedding")
			}
		} else if len(vec) > 0 {
			s.ensureVectorInit(targetDB, len(vec))
			hasEmbeddingColumn = targetDB.Migrator().HasColumn("wiki_search_documents", "embedding")
			if hasEmbeddingColumn {
				values["embedding"] = embedding.FormatVector(vec)
				updateColumns = append(updateColumns, "embedding")
			}
		}
	}
	return targetDB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "repository_id"}, {Name: "slug"}},
		DoUpdates: clause.AssignmentColumns(updateColumns),
	}).Model(&db.WikiSearchDocument{}).Create(values).Error
}

func (s *Service) deleteWikiSearchDocument(ctx context.Context, repoFullName, slug string) error {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return err
	}
	return s.DBForCtx(ctx).Where("repository_id = ? AND slug = ?", repo.ID, slug).Delete(&db.WikiSearchDocument{}).Error
}

func (s *Service) ReindexWikiSearch(ctx context.Context, repoFullName string) (int, error) {
	pages, err := s.ListWikiPages(ctx, repoFullName, ListWikiPagesOptions{Recursive: true})
	if err != nil {
		return 0, err
	}
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return 0, err
	}
	if err := s.DBForCtx(ctx).Where("repository_id = ?", repo.ID).Delete(&db.WikiSearchDocument{}).Error; err != nil {
		return 0, err
	}
	count := 0
	for _, summary := range pages {
		page, err := s.GetWikiPage(ctx, repoFullName, summary.Slug)
		if err != nil {
			return count, err
		}
		if err := s.upsertWikiSearchDocument(ctx, repoFullName, page); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *Service) ReindexAllWikiSearch(ctx context.Context) (int, error) {
	var repos []db.Repository
	if err := s.DBForCtx(ctx).Where("has_wiki = ?", true).Find(&repos).Error; err != nil {
		return 0, err
	}
	total := 0
	for _, repo := range repos {
		n, err := s.ReindexWikiSearch(ctx, repo.FullName)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return total, fmt.Errorf("reindex %s: %w", repo.FullName, err)
		}
		total += n
	}
	return total, nil
}

func parseStoredEmbedding(raw string) ([]float32, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	raw = strings.TrimPrefix(raw, "[")
	raw = strings.TrimSuffix(raw, "]")
	if strings.TrimSpace(raw) == "" {
		return nil, false
	}
	parts := strings.Split(raw, ",")
	vec := make([]float32, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 32)
		if err != nil {
			return nil, false
		}
		vec = append(vec, float32(value))
	}
	return vec, true
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, magA, magB float64
	for i := range a {
		af := float64(a[i])
		bf := float64(b[i])
		dot += af * bf
		magA += af * af
		magB += bf * bf
	}
	if magA == 0 || magB == 0 {
		return 0
	}
	return dot / (math.Sqrt(magA) * math.Sqrt(magB))
}
