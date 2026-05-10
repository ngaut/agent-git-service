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
	results, err := s.searchWikiLexical(ctx, repo.ID, query, limit, offset, labelFilters)
	if err != nil {
		slog.WarnContext(ctx, "wiki search indexed path failed; falling back to git scan", "repo", repo.FullName, "error", err)
		results, err = s.searchWikiLexicalFromGit(ctx, repoFullName, query, limit, offset, labelFilters)
		if err != nil {
			return WikiSearchResponse{}, err
		}
	}

	if s.Embedder != nil && !embedding.IsNop(s.Embedder) {
		if semantic, ok, semanticErr := s.searchWikiSemantic(ctx, repo.ID, query, limit, offset, labelFilters); semanticErr != nil {
			slog.WarnContext(ctx, "wiki search semantic path failed; falling back to substring", "repo", repo.FullName, "error", semanticErr)
		} else if ok {
			method = "vector"
			results = semantic
		}
	}

	return WikiSearchResponse{
		Results:   results,
		Query:     query,
		Method:    method,
		ElapsedMS: time.Since(start).Milliseconds(),
	}, nil
}

func (s *Service) searchWikiLexical(ctx context.Context, repoID uint, query string, limit, offset int, filters WikiLabelFilters) ([]WikiSearchResult, error) {
	docs, err := s.wikiSearchDocuments(ctx, repoID, query, false, filters)
	if err != nil {
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
		if wikiTextContainsAllTokens(doc.Title, string(doc.Body), query) {
			score += lexicalScore(doc.Title, string(doc.Body), query)
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
	return paginateWikiSearchResults(scored, query, limit, offset), nil
}

func (s *Service) searchWikiLexicalFromGit(ctx context.Context, repoFullName, query string, limit, offset int, filters WikiLabelFilters) ([]WikiSearchResult, error) {
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
		if wikiTextContainsAllTokens(page.Title, page.Body, query) {
			score += lexicalScore(page.Title, page.Body, query)
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
	return paginateWikiSearchResults(scored, query, limit, offset), nil
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

func (s *Service) searchWikiSemantic(ctx context.Context, repoID uint, query string, limit, offset int, filters WikiLabelFilters) ([]WikiSearchResult, bool, error) {
	vec, err := s.Embedder.Embed(ctx, query)
	if err != nil {
		return nil, false, err
	}
	if len(vec) == 0 {
		return nil, false, nil
	}

	docs, err := s.wikiSearchDocuments(ctx, repoID, query, true, filters)
	if err != nil {
		return nil, false, err
	}
	if len(docs) == 0 {
		return nil, false, nil
	}
	labelsBySlug, err := s.wikiSearchLabelsBySlug(ctx, repoID, docs)
	if err != nil {
		return nil, false, err
	}

	scored := make([]wikiScoredDocument, 0, len(docs))
	for _, doc := range docs {
		docVec, ok := parseStoredVector(doc.Embedding)
		if !ok || len(docVec) != len(vec) {
			continue
		}
		score := cosineSimilarity(vec, docVec)
		if math.IsNaN(score) || math.IsInf(score, 0) {
			continue
		}
		if score < wikiSemanticMinScore {
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
	return paginateWikiSearchResults(scored, query, limit, offset), true, nil
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
	if offset >= len(scored) {
		return []WikiSearchResult{}
	}
	end := offset + limit
	if end > len(scored) {
		end = len(scored)
	}
	out := make([]WikiSearchResult, 0, end-offset)
	for _, row := range scored[offset:end] {
		out = append(out, WikiSearchResult{
			Slug:    row.doc.Slug,
			Title:   row.doc.Title,
			Score:   roundWikiScore(row.score),
			Snippet: buildWikiSnippet(string(row.doc.Body), query),
			Labels:  row.labels,
		})
	}
	return out
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
					"(wiki_search_documents.title LIKE ?"+likeEscape+" OR wiki_search_documents.body LIKE ?"+likeEscape+" OR labels.name LIKE ?"+likeEscape+" OR labels.description LIKE ?"+likeEscape+")",
					like, like, like, like,
				)
			}
			q = q.Distinct(
				"wiki_search_documents.id",
				"wiki_search_documents.repository_id",
				"wiki_search_documents.slug",
				"wiki_search_documents.title",
				"wiki_search_documents.body",
				"wiki_search_documents.revision_sha",
				"wiki_search_documents.embedding",
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

func lexicalScore(title, body, query string) float64 {
	score := 0.0
	titleLower := strings.ToLower(title)
	bodyLower := strings.ToLower(body)
	for _, token := range wikiSearchTokens(query) {
		tokenLower := strings.ToLower(token)
		score += float64(strings.Count(bodyLower, tokenLower))
		score += float64(strings.Count(titleLower, tokenLower)) * 2
	}
	return score
}

func wikiTextContainsAllTokens(title, body, query string) bool {
	titleLower := strings.ToLower(title)
	bodyLower := strings.ToLower(body)
	for _, token := range wikiSearchTokens(query) {
		token = strings.ToLower(token)
		if token == "" {
			continue
		}
		if !strings.Contains(titleLower, token) && !strings.Contains(bodyLower, token) {
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

func parseStoredVector(raw string) ([]float32, bool) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "[")
	raw = strings.TrimSuffix(raw, "]")
	if raw == "" {
		return nil, false
	}
	parts := strings.Split(raw, ",")
	vec := make([]float32, 0, len(parts))
	for _, part := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(part), 32)
		if err != nil {
			return nil, false
		}
		vec = append(vec, float32(v))
	}
	return vec, true
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

func (s *Service) queueWikiSearchUpsert(ctx context.Context, repoFullName string, page WikiPage) {
	s.Wg.Add(1)
	go func() {
		defer s.Wg.Done()
		bgCtx := applog.CloneContext(s.ServerCtx(), ctx)
		if tenantDB, ok := DBFromContext(ctx); ok {
			bgCtx = ContextWithDB(bgCtx, tenantDB)
		}
		if err := s.upsertWikiSearchDocument(bgCtx, repoFullName, page); err != nil {
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
		if err := s.deleteWikiSearchDocument(bgCtx, repoFullName, slug); err != nil {
			slog.WarnContext(bgCtx, "wiki search index delete failed", "repo", repoFullName, "slug", slug, "error", err)
		}
	}()
}

func (s *Service) upsertWikiSearchDocument(ctx context.Context, repoFullName string, page WikiPage) error {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return err
	}
	doc := db.WikiSearchDocument{
		RepositoryID: repo.ID,
		Slug:         page.Slug,
		Title:        page.Title,
		Body:         db.LargeText(page.Body),
		RevisionSHA:  page.SHA,
		Embedding:    "",
	}
	if s.Embedder != nil && !embedding.IsNop(s.Embedder) {
		text := page.Title + "\n" + wikiPageLabelsText(page.Labels) + "\n" + page.Body
		if len(text) > 32000 {
			text = text[:32000]
		}
		vec, err := s.embedWithRetry(ctx, text)
		if err != nil {
			slog.WarnContext(ctx, "wiki search embedding failed; storing lexical document only", "repo", repoFullName, "slug", page.Slug, "error", err)
		} else if len(vec) > 0 {
			doc.Embedding = embedding.FormatVector(vec)
		}
	}
	return s.DBForCtx(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "repository_id"}, {Name: "slug"}},
		DoUpdates: clause.AssignmentColumns([]string{"title", "body", "revision_sha", "embedding", "updated_at"}),
	}).Create(&doc).Error
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
