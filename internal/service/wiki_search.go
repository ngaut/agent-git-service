package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"math"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/embedding"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	wikiSearchDefaultLimit  = 20
	wikiSearchMaxLimit      = 50
	wikiSnippetBudget       = 180
	wikiSemanticMinScore    = 0.2
	wikiSearchMinRankWindow = 100
	wikiSearchMaxRankWindow = 500
	// Preserve correctness for small repos and fresh async-index lag without
	// letting large wiki searches fall back to an O(total page body) scan.
	wikiCatalogFallbackMaxCurrentIndexedDocs = wikiSearchMinRankWindow
	// When lexical search already found concrete token matches, keep
	// semantic-only additions to high-confidence neighbors so short literal
	// queries do not get flooded by weak vector nearest-neighbor noise.
	wikiSemanticOnlyMinScoreWithLexical = 0.6
	wikiSemanticMaxExact                = wikiSearchMaxRankWindow
	wikiSemanticANNCandidateMin         = 256
	wikiSemanticANNCandidateMax         = 4096
	wikiLexicalCandidateIDMin           = 1000
	wikiLexicalCandidateIDMax           = 5000
	wikiLexicalHydrationMin             = 200
	wikiLexicalHydrationMax             = 1000
	wikiSearchTimingLogThreshold        = time.Second
	wikiReindexWorkers                  = 4
)

var errWikiCurrentSearchIndexStale = errors.New("wiki current search index stale")

type WikiSearchResult struct {
	Slug    string
	Title   string
	Score   float64
	Snippet string
	Labels  []db.Label

	liveGitHydrated         bool
	currentIndexBodyTrusted bool
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

func wikiSearchRankWindow(limit, offset int) int {
	n := clampWikiSearchLimit(limit) + normalizeWikiSearchOffset(offset)
	if n < wikiSearchMinRankWindow {
		return wikiSearchMinRankWindow
	}
	return n
}

type wikiSearchTiming struct {
	repoMS         int64
	liveHeadMS     int64
	catalogMS      int64
	lexicalMS      int64
	semanticWaitMS int64
	hydrateMS      int64

	lexicalResults  int
	semanticResults int
	fusedResults    int
	hydratedResults int
	finalResults    int
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

	timing := wikiSearchTiming{}
	stageStart := time.Now()
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return WikiSearchResponse{}, err
	}
	timing.repoMS = time.Since(stageStart).Milliseconds()
	limit := clampWikiSearchLimit(opts.Limit)
	offset := normalizeWikiSearchOffset(opts.Offset)
	rankWindow := wikiSearchRankWindow(limit, offset)
	labelFilters := WikiLabelFilters{Labels: opts.Labels, ExcludeLabels: opts.ExcludeLabels}
	searchCtx, cancelSearch := context.WithCancel(ctx)
	defer cancelSearch()
	embeddingResultC := s.startWikiSearchEmbedding(searchCtx, query)
	wikiRepoLive := false
	stageStart = time.Now()
	if _, err := s.Git.HeadSHA(searchCtx, wikiRepoFullName(repoFullName), wikiDefaultBranch); err == nil {
		wikiRepoLive = true
	}
	timing.liveHeadMS = time.Since(stageStart).Milliseconds()
	catalogSearchReady := false
	stageStart = time.Now()
	if s.WikiCatalog != nil {
		if readyErr := s.ensureWikiCatalogCurrent(searchCtx, repoFullName); readyErr != nil {
			slog.WarnContext(ctx, "wiki search catalog freshness check failed", "repo", repo.FullName, "error", readyErr)
		}
		hasCatalogState, stateErr := s.wikiCatalogHasLiveState(searchCtx, repo.ID)
		if stateErr != nil {
			return WikiSearchResponse{}, stateErr
		}
		catalogSearchReady = hasCatalogState
	}
	timing.catalogMS = time.Since(stageStart).Milliseconds()

	method := "substring"
	semanticResultC := s.startWikiSearchSemanticCandidates(searchCtx, repo.ID, query, embeddingResultC, labelFilters, rankWindow)
	stageStart = time.Now()
	lexical, err := s.searchWikiLexicalCandidates(searchCtx, repo, repoFullName, query, labelFilters, rankWindow, catalogSearchReady, wikiRepoLive)
	if err != nil {
		cancelSearch()
		return WikiSearchResponse{}, err
	}
	timing.lexicalMS = time.Since(stageStart).Milliseconds()
	timing.lexicalResults = len(lexical)
	results := lexical

	if semanticResultC != nil {
		stageStart = time.Now()
		semanticResult := <-semanticResultC
		timing.semanticWaitMS = time.Since(stageStart).Milliseconds()
		if semanticResult.err != nil {
			slog.WarnContext(ctx, "wiki search semantic path failed; falling back to substring", "repo", repo.FullName, "error", semanticResult.err)
		} else if semanticResult.ok {
			method = "vector"
			timing.semanticResults = len(semanticResult.results)
			results = fuseWikiSearchResults(lexical, semanticResult.results)
		}
	}
	timing.fusedResults = len(results)

	if wikiRepoLive {
		results = truncateWikiSearchResultList(results, rankWindow)
	}
	stageStart = time.Now()
	if catalogSearchReady {
		var ok bool
		results, ok, err = s.hydrateWikiSearchResultsFromCatalog(searchCtx, repo.ID, results, query, method == "substring")
		if err != nil {
			return WikiSearchResponse{}, err
		}
		if !ok {
			results, err = s.hydrateWikiSearchResults(searchCtx, repoFullName, results, query, wikiRepoLive)
			if err != nil {
				return WikiSearchResponse{}, err
			}
		}
	} else {
		results, err = s.hydrateWikiSearchResults(searchCtx, repoFullName, results, query, wikiRepoLive)
		if err != nil {
			return WikiSearchResponse{}, err
		}
	}
	timing.hydrateMS = time.Since(stageStart).Milliseconds()
	timing.hydratedResults = len(results)
	results = paginateWikiSearchResultList(results, limit, offset)
	timing.finalResults = len(results)
	elapsed := time.Since(start)
	logWikiSearchTiming(ctx, repo.FullName, method, query, limit, offset, rankWindow, wikiRepoLive, catalogSearchReady, elapsed, timing)

	return WikiSearchResponse{
		Results:   results,
		Query:     query,
		Method:    method,
		ElapsedMS: elapsed.Milliseconds(),
	}, nil
}

func logWikiSearchTiming(ctx context.Context, repoFullName, method, query string, limit, offset, rankWindow int, wikiRepoLive, catalogSearchReady bool, elapsed time.Duration, timing wikiSearchTiming) {
	if elapsed < wikiSearchTimingLogThreshold {
		return
	}
	slog.InfoContext(ctx, "wiki search timing",
		"repo", repoFullName,
		"search_method", method,
		"query_tokens", len(wikiSearchTokens(query)),
		"limit", limit,
		"offset", offset,
		"rank_window", rankWindow,
		"wiki_repo_live", wikiRepoLive,
		"catalog_search_ready", catalogSearchReady,
		"total_ms", elapsed.Milliseconds(),
		"repo_ms", timing.repoMS,
		"live_head_ms", timing.liveHeadMS,
		"catalog_ms", timing.catalogMS,
		"lexical_ms", timing.lexicalMS,
		"semantic_wait_ms", timing.semanticWaitMS,
		"hydrate_ms", timing.hydrateMS,
		"lexical_results", timing.lexicalResults,
		"semantic_results", timing.semanticResults,
		"fused_results", timing.fusedResults,
		"hydrated_results", timing.hydratedResults,
		"final_results", timing.finalResults,
	)
}

func (s *Service) searchWikiLexicalCandidates(ctx context.Context, repo db.Repository, repoFullName, query string, filters WikiLabelFilters, rankWindow int, catalogSearchReady, wikiRepoLive bool) ([]WikiSearchResult, error) {
	if catalogSearchReady {
		indexedLexical, indexedOK, indexedErr := s.searchWikiLexicalFromCurrentIndex(ctx, repo.ID, query, filters, rankWindow)
		if indexedErr != nil && !errors.Is(indexedErr, errWikiCurrentSearchIndexStale) {
			slog.WarnContext(ctx, "wiki search current index path failed; falling back to catalog lexical path", "repo", repo.FullName, "error", indexedErr)
		}
		useCatalogFallback := !indexedOK || errors.Is(indexedErr, errWikiCurrentSearchIndexStale)
		if indexedOK && indexedErr == nil && len(indexedLexical) == 0 {
			currentIndexedCount, countOK, countErr := s.countCurrentWikiSearchDocuments(ctx, repo.ID)
			if countErr != nil {
				return nil, countErr
			}
			useCatalogFallback = !countOK || currentIndexedCount <= wikiCatalogFallbackMaxCurrentIndexedDocs
		}
		if useCatalogFallback {
			catalogLexical, catalogErr := s.searchWikiLexicalFromCatalog(ctx, repo.ID, query, filters, rankWindow)
			if catalogErr != nil {
				slog.WarnContext(ctx, "wiki search catalog lexical path failed", "repo", repo.FullName, "error", catalogErr)
				if indexedErr != nil && !errors.Is(indexedErr, errWikiCurrentSearchIndexStale) {
					return nil, indexedErr
				}
				indexedLexical, indexedErr := s.searchWikiLexical(ctx, repo.ID, query, filters, rankWindow)
				if indexedErr != nil {
					return nil, catalogErr
				}
				return indexedLexical, nil
			}
			return catalogLexical, nil
		}
		if indexedErr == nil {
			return indexedLexical, nil
		}
		return []WikiSearchResult{}, nil
	}
	if wikiRepoLive {
		gitLexical, gitErr := s.searchWikiLexicalFromGit(ctx, repoFullName, query, filters, rankWindow)
		if gitErr != nil {
			indexedLexical, indexedErr := s.searchWikiLexical(ctx, repo.ID, query, filters, rankWindow)
			if indexedErr != nil {
				return nil, gitErr
			}
			slog.WarnContext(ctx, "wiki search git lexical path failed; falling back to indexed lexical path", "repo", repo.FullName, "error", gitErr)
			return indexedLexical, nil
		}
		return gitLexical, nil
	}
	lexical, err := s.searchWikiLexical(ctx, repo.ID, query, filters, 0)
	if err != nil {
		return nil, err
	}
	return lexical, nil
}

func (s *Service) hydrateWikiSearchResults(ctx context.Context, repoFullName string, results []WikiSearchResult, query string, wikiRepoLive bool) ([]WikiSearchResult, error) {
	if len(results) == 0 {
		return []WikiSearchResult{}, nil
	}
	if !wikiRepoLive {
		return results, nil
	}
	hydrated := make([]WikiSearchResult, 0, len(results))
	for _, result := range results {
		page, err := s.GetWikiPage(ctx, repoFullName, result.Slug)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				// Live git lexical search can surface a page before the
				// background catalog catch-up has materialized it. Preserve
				// the already-hydrated git result only when the slug still
				// exists at the live wiki HEAD; stale semantic/index rows
				// must continue to drop out here.
				if _, liveErr := s.Git.ReadFileAtRef(ctx, wikiRepoFullName(repoFullName), wikiSlugToPath(result.Slug), wikiDefaultBranch); liveErr == nil &&
					(result.Title != "" || result.Snippet != "" || len(result.Labels) > 0) {
					hydrated = append(hydrated, result)
				}
				continue
			}
			return nil, err
		}
		if result.liveGitHydrated {
			result.Labels = page.Labels
			hydrated = append(hydrated, result)
			continue
		}
		result.Title = page.Title
		result.Snippet = buildWikiSnippet(page.Body, query)
		result.Labels = page.Labels
		hydrated = append(hydrated, result)
	}
	return hydrated, nil
}

func (s *Service) hydrateWikiSearchResultsFromCatalog(ctx context.Context, repoID uint, results []WikiSearchResult, query string, requireLexicalMatch bool) ([]WikiSearchResult, bool, error) {
	if len(results) == 0 {
		return []WikiSearchResult{}, true, nil
	}
	slugs := make([]string, 0, len(results))
	for _, result := range results {
		slugs = append(slugs, result.Slug)
	}
	pagesBySlug, ok, err := s.wikiCatalogPagesBySlug(ctx, repoID, slugs)
	if err != nil || !ok {
		return nil, ok, err
	}
	labelsBySlug, err := s.wikiLabelsForSlugs(ctx, repoID, slugs)
	if err != nil {
		return nil, true, err
	}

	hydrated := make([]WikiSearchResult, 0, len(results))
	for _, result := range results {
		page, ok := pagesBySlug[result.Slug]
		if !ok {
			continue
		}
		labels := labelsBySlug[page.Slug]
		title := titleFromSlug(page.Slug)
		if result.currentIndexBodyTrusted {
			result.Slug = page.Slug
			result.Title = title
			result.Labels = labels
			hydrated = append(hydrated, result)
			continue
		}
		body, err := s.wikiPageBody(ctx, page)
		if err != nil {
			return nil, true, err
		}
		if requireLexicalMatch && !wikiTextContainsAllTokens(title, page.Slug, string(body), query) && wikiLabelLexicalScore(labels, query) <= 0 {
			continue
		}
		result.Slug = page.Slug
		result.Title = title
		result.Snippet = buildWikiSnippet(string(body), query)
		result.Labels = labels
		result.liveGitHydrated = false
		hydrated = append(hydrated, result)
	}
	return hydrated, true, nil
}

func (s *Service) wikiCatalogPagesBySlug(ctx context.Context, repoID uint, slugs []string) (map[string]db.WikiPage, bool, error) {
	slugs = uniqueStrings(slugs)
	out := make(map[string]db.WikiPage, len(slugs))
	if len(slugs) == 0 {
		return out, true, nil
	}
	var pages []db.WikiPage
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND deleted_at IS NULL AND slug IN ?", repoID, slugs).
		Find(&pages).Error; err != nil {
		if isMissingTableErr(err) {
			return nil, false, nil
		}
		return nil, true, err
	}
	for _, page := range pages {
		out[page.Slug] = page
	}
	return out, true, nil
}

func (s *Service) wikiCatalogMatchesLiveHead(ctx context.Context, repoFullName string, repoID uint) (bool, error) {
	if s.Git == nil || s.WikiCatalog == nil {
		return false, nil
	}
	full := wikiRepoFullName(repoFullName)
	if !s.Git.Exists(ctx, full) {
		return false, nil
	}
	headSHA, err := s.Git.ResolveContentCommit(ctx, full, wikiDefaultBranch)
	if err != nil || strings.TrimSpace(headSHA) == "" {
		return false, err
	}
	last, err := s.loadLatestWikiChangesetState(ctx, repoID)
	if err != nil {
		if isMissingTableErr(err) {
			return false, nil
		}
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(last.CommitSHA), strings.TrimSpace(headSHA)), nil
}

func (s *Service) searchWikiLexicalFromCurrentIndex(ctx context.Context, repoID uint, query string, filters WikiLabelFilters, rankLimit int) ([]WikiSearchResult, bool, error) {
	if db.SupportsTiDBFullText(s.DBForCtx(ctx)) {
		docs, err := s.wikiSearchCurrentDocumentsFullText(ctx, repoID, query, filters, rankLimit)
		if err == nil {
			results, err := s.rankWikiCurrentLexicalDocuments(ctx, repoID, docs, query)
			if err != nil {
				return nil, true, err
			}
			if len(results) == 0 {
				likeDocs, likeErr := s.wikiSearchCurrentDocuments(ctx, repoID, query, false, filters)
				if likeErr != nil {
					return nil, true, likeErr
				}
				results, err = s.rankWikiCurrentLexicalDocuments(ctx, repoID, likeDocs, query)
				if err != nil {
					return nil, true, err
				}
			}
			return markWikiSearchResultsCurrentIndex(truncateWikiSearchResultList(results, rankLimit)), true, nil
		}
		if isMissingTableErr(err) || wikiSearchDocumentTableMissing(err) {
			return nil, false, nil
		}
		slog.WarnContext(ctx, "wiki search current TiDB full-text query failed; falling back to LIKE", "repo_id", repoID, "error", err)
	}

	docs, err := s.wikiSearchCurrentDocuments(ctx, repoID, query, false, filters)
	if err != nil {
		if isMissingTableErr(err) || wikiSearchDocumentTableMissing(err) {
			return nil, false, nil
		}
		return nil, true, err
	}
	results, err := s.rankWikiCurrentLexicalDocuments(ctx, repoID, docs, query)
	if err != nil {
		return nil, true, err
	}
	return markWikiSearchResultsCurrentIndex(truncateWikiSearchResultList(results, rankLimit)), true, nil
}

func (s *Service) countCurrentWikiSearchDocuments(ctx context.Context, repoID uint) (int64, bool, error) {
	var count int64
	err := s.currentWikiSearchDocumentsQuery(ctx, repoID).Count(&count).Error
	if err != nil {
		if isMissingTableErr(err) || wikiSearchDocumentTableMissing(err) {
			return 0, false, nil
		}
		return 0, true, err
	}
	return count, true, nil
}

func (s *Service) currentWikiSearchDocumentsQuery(ctx context.Context, repoID uint) *gorm.DB {
	return s.DBForCtx(ctx).
		Model(&db.WikiSearchDocument{}).
		Joins("JOIN wiki_pages ON wiki_pages.repository_id = wiki_search_documents.repository_id AND wiki_pages.slug = wiki_search_documents.slug").
		Where("wiki_search_documents.repository_id = ? AND wiki_pages.deleted_at IS NULL AND wiki_search_documents.revision_sha = wiki_pages.head_blob_sha", repoID)
}

func (s *Service) rankWikiCurrentLexicalDocuments(ctx context.Context, repoID uint, docs []db.WikiSearchDocument, query string) ([]WikiSearchResult, error) {
	currentDocs, dropped := filterCurrentWikiSearchDocuments(docs)
	if dropped > 0 && len(currentDocs) == 0 {
		return nil, errWikiCurrentSearchIndexStale
	}
	return s.rankWikiLexicalDocuments(ctx, repoID, currentDocs, query)
}

func filterCurrentWikiSearchDocuments(docs []db.WikiSearchDocument) ([]db.WikiSearchDocument, int) {
	out := make([]db.WikiSearchDocument, 0, len(docs))
	dropped := 0
	for _, doc := range docs {
		if !wikiSearchDocumentBodyMatchesRevision(doc) {
			dropped++
			continue
		}
		out = append(out, doc)
	}
	return out, dropped
}

func wikiSearchDocumentBodyMatchesRevision(doc db.WikiSearchDocument) bool {
	revision := strings.TrimSpace(doc.RevisionSHA)
	if revision == "" {
		return false
	}
	return strings.EqualFold(wikicatalog.HashContent([]byte(doc.Body)), revision)
}

func (s *Service) searchWikiLexical(ctx context.Context, repoID uint, query string, filters WikiLabelFilters, rankLimit int) ([]WikiSearchResult, error) {
	if db.SupportsTiDBFullText(s.DBForCtx(ctx)) {
		docs, err := s.wikiSearchDocumentsFullText(ctx, repoID, query, filters)
		if err == nil {
			results, err := s.rankWikiLexicalDocuments(ctx, repoID, docs, query)
			if err != nil {
				return nil, err
			}
			return truncateWikiSearchResultList(results, rankLimit), nil
		}
		slog.WarnContext(ctx, "wiki search TiDB full-text query failed; falling back to LIKE", "repo_id", repoID, "error", err)
	}

	docs, err := s.wikiSearchDocuments(ctx, repoID, query, false, filters)
	if err != nil {
		return nil, err
	}
	results, err := s.rankWikiLexicalDocuments(ctx, repoID, docs, query)
	if err != nil {
		return nil, err
	}
	return truncateWikiSearchResultList(results, rankLimit), nil
}

func (s *Service) rankWikiLexicalDocuments(ctx context.Context, repoID uint, docs []db.WikiSearchDocument, query string) ([]WikiSearchResult, error) {
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

func (s *Service) searchWikiLexicalFromCatalog(ctx context.Context, repoID uint, query string, filters WikiLabelFilters, rankLimit int) ([]WikiSearchResult, error) {
	var pages []db.WikiPage
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND deleted_at IS NULL", repoID).
		Order("updated_at desc").
		Find(&pages).Error; err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		return []WikiSearchResult{}, nil
	}

	slugs := make([]string, 0, len(pages))
	for _, page := range pages {
		slugs = append(slugs, page.Slug)
	}
	allowedSlugs := map[string]struct{}{}
	if hasWikiLabelFilters(filters) {
		var noResults bool
		var err error
		allowedSlugs, noResults, err = s.wikiSlugsMatchingLabelFilters(ctx, repoID, slugs, filters)
		if err != nil {
			return nil, err
		}
		if noResults {
			return []WikiSearchResult{}, nil
		}
	}
	labelScores, err := s.wikiLabelLexicalScoresBySlug(ctx, repoID, query)
	if err != nil {
		return nil, err
	}

	scored := make([]wikiScoredDocument, 0, len(pages))
	for _, page := range pages {
		if len(allowedSlugs) > 0 {
			if _, ok := allowedSlugs[page.Slug]; !ok {
				continue
			}
		}
		title := titleFromSlug(page.Slug)
		body, err := s.wikiPageBody(ctx, page)
		if err != nil {
			return nil, err
		}
		score := 0.0
		if wikiTextContainsAllTokens(title, page.Slug, string(body), query) {
			score += lexicalScore(title, page.Slug, string(body), query)
		}
		score += labelScores[page.Slug]
		if score <= 0 {
			continue
		}
		scored = append(scored, wikiScoredDocument{
			doc: db.WikiSearchDocument{
				Slug:      page.Slug,
				Title:     title,
				Body:      db.LargeText(body),
				UpdatedAt: page.UpdatedAt,
			},
			score: score,
		})
	}
	if len(scored) == 0 {
		return []WikiSearchResult{}, nil
	}
	sortWikiScoredDocuments(scored)
	if rankLimit > 0 && len(scored) > rankLimit {
		scored = scored[:rankLimit]
	}
	docs := make([]db.WikiSearchDocument, 0, len(scored))
	for _, row := range scored {
		docs = append(docs, row.doc)
	}
	labelsBySlug, err := s.wikiSearchLabelsBySlug(ctx, repoID, docs)
	if err != nil {
		return nil, err
	}
	for i := range scored {
		scored[i].labels = labelsBySlug[scored[i].doc.Slug]
	}
	return buildWikiSearchResults(scored, query), nil
}

func (s *Service) searchWikiLexicalFromGit(ctx context.Context, repoFullName, query string, filters WikiLabelFilters, rankLimit int) ([]WikiSearchResult, error) {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	full := wikiRepoFullName(repoFullName)
	headSHA, err := s.Git.HeadSHA(ctx, full, wikiDefaultBranch)
	if err != nil {
		return nil, err
	}
	paths, err := s.Git.ListTreeFilesAtRef(ctx, full, headSHA)
	if err != nil {
		return nil, err
	}

	slugs := make([]string, 0, len(paths))
	pathBySlug := make(map[string]string, len(paths))
	for _, path := range paths {
		slug := wikiPathToSlug(path)
		if slug == "" {
			continue
		}
		slugs = append(slugs, slug)
		pathBySlug[slug] = path
	}
	if len(slugs) == 0 {
		return []WikiSearchResult{}, nil
	}

	allowedSlugs := map[string]struct{}{}
	if hasWikiLabelFilters(filters) {
		var noResults bool
		allowedSlugs, noResults, err = s.wikiSlugsMatchingLabelFilters(ctx, repo.ID, slugs, filters)
		if err != nil {
			return nil, err
		}
		if noResults {
			return []WikiSearchResult{}, nil
		}
	}

	tokens := wikiSearchTokens(query)
	bodyTokenSlugs := make(map[string]map[string]struct{}, len(tokens))
	bodyMatchCounts := make(map[string]int)
	for _, token := range tokens {
		counts, err := s.Git.GrepFileMatchCountsAtRef(ctx, full, headSHA, []string{token})
		if err != nil {
			return nil, err
		}
		tokenKey := strings.ToLower(token)
		matches := make(map[string]struct{}, len(counts))
		for path, count := range counts {
			slug := wikiPathToSlug(path)
			if slug == "" {
				continue
			}
			matches[slug] = struct{}{}
			bodyMatchCounts[slug] += count
		}
		bodyTokenSlugs[tokenKey] = matches
	}

	labelScores, err := s.wikiLabelLexicalScoresBySlug(ctx, repo.ID, query)
	if err != nil {
		return nil, err
	}

	candidates := make([]wikiGitLexicalCandidate, 0, len(slugs))
	for _, slug := range slugs {
		if len(allowedSlugs) > 0 {
			if _, ok := allowedSlugs[slug]; !ok {
				continue
			}
		}
		title := titleFromSlug(slug)
		labelScore := labelScores[slug]
		matchedContent := wikiGitCandidateMatchesAllTokens(title, slug, tokens, bodyTokenSlugs)
		if !matchedContent && labelScore <= 0 {
			continue
		}
		approxScore := lexicalScore(title, slug, "", query) + float64(bodyMatchCounts[slug]) + labelScore
		candidates = append(candidates, wikiGitLexicalCandidate{
			slug:        slug,
			path:        pathBySlug[slug],
			title:       title,
			approxScore: approxScore,
		})
	}
	if len(candidates) == 0 {
		return []WikiSearchResult{}, nil
	}

	sortWikiGitLexicalCandidates(candidates)
	preRankLimit := rankLimit * 3
	if preRankLimit < rankLimit {
		preRankLimit = rankLimit
	}
	if preRankLimit <= 0 {
		preRankLimit = wikiSearchMinRankWindow
	}
	if len(candidates) > preRankLimit {
		candidates = candidates[:preRankLimit]
	}

	candidateSlugs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidateSlugs = append(candidateSlugs, candidate.slug)
	}
	updatedAtBySlug, err := s.wikiPageUpdatedAtBySlug(ctx, repo.ID, candidateSlugs)
	if err != nil {
		return nil, err
	}
	for i := range candidates {
		candidates[i].updatedAt = updatedAtBySlug[candidates[i].slug]
	}
	sortWikiGitLexicalCandidates(candidates)
	if rankLimit <= 0 {
		rankLimit = wikiSearchMinRankWindow
	}
	if len(candidates) > rankLimit {
		candidates = candidates[:rankLimit]
	}

	candidateSlugs = candidateSlugs[:0]
	for _, candidate := range candidates {
		candidateSlugs = append(candidateSlugs, candidate.slug)
	}
	labelsBySlug, err := s.wikiLabelsForSlugs(ctx, repo.ID, candidateSlugs)
	if err != nil {
		return nil, err
	}

	scored := make([]wikiScoredDocument, 0, len(candidates))
	for _, candidate := range candidates {
		body, err := s.Git.ReadFileAtRef(ctx, full, candidate.path, headSHA)
		if err != nil {
			return nil, err
		}
		labels := labelsBySlug[candidate.slug]
		score := 0.0
		if wikiTextContainsAllTokens(candidate.title, candidate.slug, string(body), query) {
			score += lexicalScore(candidate.title, candidate.slug, string(body), query)
		}
		score += wikiLabelLexicalScore(labels, query)
		if score <= 0 {
			continue
		}
		scored = append(scored, wikiScoredDocument{
			doc: db.WikiSearchDocument{
				Slug:      candidate.slug,
				Title:     candidate.title,
				Body:      db.LargeText(body),
				UpdatedAt: candidate.updatedAt,
			},
			score:  score,
			labels: labels,
		})
	}
	sortWikiScoredDocuments(scored)
	return markWikiSearchResultsLiveGitHydrated(buildWikiSearchResults(scored, query)), nil
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

type wikiGitLexicalCandidate struct {
	slug        string
	path        string
	title       string
	approxScore float64
	updatedAt   time.Time
}

func sortWikiGitLexicalCandidates(candidates []wikiGitLexicalCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].approxScore == candidates[j].approxScore {
			if candidates[i].updatedAt.Equal(candidates[j].updatedAt) {
				return candidates[i].slug < candidates[j].slug
			}
			return candidates[i].updatedAt.After(candidates[j].updatedAt)
		}
		return candidates[i].approxScore > candidates[j].approxScore
	})
}

func wikiGitCandidateMatchesAllTokens(title, slug string, tokens []string, bodyTokenSlugs map[string]map[string]struct{}) bool {
	titleLower := strings.ToLower(title)
	slugLower := strings.ToLower(slug)
	for _, token := range tokens {
		token = strings.ToLower(token)
		if token == "" {
			continue
		}
		if strings.Contains(titleLower, token) || strings.Contains(slugLower, token) {
			continue
		}
		if matches := bodyTokenSlugs[token]; matches != nil {
			if _, ok := matches[slug]; ok {
				continue
			}
		}
		return false
	}
	return true
}

func (s *Service) wikiLabelLexicalScoresBySlug(ctx context.Context, repoID uint, query string) (map[string]float64, error) {
	scores := map[string]float64{}
	tokens := wikiSearchTokens(query)
	if len(tokens) == 0 {
		return scores, nil
	}

	database := s.DBForCtx(ctx)
	likeEscape := wikiSearchLikeEscapeClause(database)
	clauses := make([]string, 0, len(tokens)*2)
	args := make([]any, 0, len(tokens)*2)
	for _, token := range tokens {
		like := "%" + strings.ToLower(escapeWikiSearchLike(token)) + "%"
		clauses = append(clauses, "LOWER(labels.name) LIKE ?"+likeEscape)
		args = append(args, like)
		clauses = append(clauses, "LOWER(labels.description) LIKE ?"+likeEscape)
		args = append(args, like)
	}

	var rows []struct {
		Slug        string
		Name        string
		Description string
	}
	if err := database.
		Table("wiki_page_labels").
		Select("wiki_page_labels.slug, labels.name, labels.description").
		Joins("JOIN labels ON labels.id = wiki_page_labels.label_id").
		Where("wiki_page_labels.repository_id = ?", repoID).
		Where("("+strings.Join(clauses, " OR ")+")", args...).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		scores[row.Slug] += wikiLabelLexicalScore([]db.Label{{
			Name:        row.Name,
			Description: row.Description,
		}}, query)
	}
	return scores, nil
}

func (s *Service) wikiPageUpdatedAtBySlug(ctx context.Context, repoID uint, slugs []string) (map[string]time.Time, error) {
	slugs = uniqueStrings(slugs)
	out := make(map[string]time.Time, len(slugs))
	if len(slugs) == 0 {
		return out, nil
	}
	var rows []struct {
		Slug      string
		UpdatedAt time.Time
	}
	if err := s.DBForCtx(ctx).
		Model(&db.WikiPage{}).
		Select("slug", "updated_at").
		Where("repository_id = ? AND deleted_at IS NULL AND slug IN ?", repoID, slugs).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.Slug] = row.UpdatedAt
	}
	return out, nil
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

func (s *Service) wikiSearchCurrentDocumentsFullText(ctx context.Context, repoID uint, query string, filters WikiLabelFilters, rankLimit int) ([]db.WikiSearchDocument, error) {
	ids, err := s.wikiSearchCurrentCandidateIDsFullText(ctx, repoID, query, filters, rankLimit)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []db.WikiSearchDocument{}, nil
	}
	return s.wikiSearchCurrentDocumentsByID(ctx, repoID, ids, filters, wikiSearchLexicalHydrationLimit(rankLimit))
}

func (s *Service) wikiSearchCurrentCandidateIDsFullText(ctx context.Context, repoID uint, query string, filters WikiLabelFilters, rankLimit int) ([]uint, error) {
	tokens := wikiSearchTokens(query)
	if len(tokens) == 0 {
		return nil, nil
	}

	querySQL, noResults, err := s.wikiSearchCurrentCandidateIDSQL(ctx, repoID, tokens, filters, wikiSearchLexicalCandidateIDLimit(rankLimit))
	if err != nil {
		return nil, err
	}
	if noResults {
		return []uint{}, nil
	}

	var rows []struct {
		ID uint `gorm:"column:id"`
	}
	if err := s.DBForCtx(ctx).Raw(querySQL.SQL, querySQL.Args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids, nil
}

type wikiSearchRawSQL struct {
	SQL  string
	Args []any
}

func (s *Service) wikiSearchCurrentCandidateIDSQL(ctx context.Context, repoID uint, tokens []string, filters WikiLabelFilters, limit int) (wikiSearchRawSQL, bool, error) {
	labelPredicates, noResults, err := s.wikiSearchLabelFilterPredicates(ctx, repoID, filters)
	if err != nil || noResults {
		return wikiSearchRawSQL{}, noResults, err
	}
	return buildWikiSearchCurrentCandidateIDSQL(s.DBForCtx(ctx), repoID, tokens, labelPredicates, limit), false, nil
}

func buildWikiSearchCurrentCandidateIDSQL(database *gorm.DB, repoID uint, tokens []string, labelPredicates []wikiSearchRawSQL, limit int) wikiSearchRawSQL {
	tokens = uniqueStrings(tokens)
	likeEscape := wikiSearchLikeEscapeClause(database)
	var b strings.Builder
	args := make([]any, 0, 1+len(tokens)*7+len(labelPredicates)+1)

	b.WriteString("SELECT wiki_search_documents.id FROM wiki_search_documents ")
	b.WriteString(wikiSearchCurrentPageJoinSQL())
	for i, token := range tokens {
		tokenSQL := buildWikiSearchCurrentTokenCandidateSQL(repoID, token, likeEscape)
		b.WriteString(" JOIN (")
		b.WriteString(tokenSQL.SQL)
		b.WriteString(") AS token_")
		b.WriteString(strconv.Itoa(i))
		b.WriteString(" ON token_")
		b.WriteString(strconv.Itoa(i))
		b.WriteString(".id = wiki_search_documents.id")
		args = append(args, tokenSQL.Args...)
	}
	b.WriteString(" WHERE ")
	b.WriteString(wikiSearchCurrentPredicateSQL())
	args = append(args, repoID)
	for _, predicate := range labelPredicates {
		if strings.TrimSpace(predicate.SQL) == "" {
			continue
		}
		b.WriteString(" AND ")
		b.WriteString(predicate.SQL)
		args = append(args, predicate.Args...)
	}
	b.WriteString(" ORDER BY wiki_search_documents.updated_at desc, wiki_search_documents.slug asc")
	if limit > 0 {
		b.WriteString(" LIMIT ?")
		args = append(args, limit)
	}
	return wikiSearchRawSQL{SQL: b.String(), Args: args}
}

func buildWikiSearchCurrentTokenCandidateSQL(repoID uint, token, likeEscape string) wikiSearchRawSQL {
	like := "%" + escapeWikiSearchLike(token) + "%"
	queries := []wikiSearchRawSQL{
		wikiSearchCurrentFTSTokenCandidateSQL(repoID, "title", token),
		wikiSearchCurrentFTSTokenCandidateSQL(repoID, "body", token),
		{
			SQL:  "SELECT wiki_search_documents.id FROM wiki_search_documents WHERE wiki_search_documents.repository_id = ? AND wiki_search_documents.slug LIKE ?" + likeEscape,
			Args: []any{repoID, like},
		},
		{
			SQL: "SELECT wiki_search_documents.id FROM wiki_search_documents " +
				" JOIN wiki_page_labels ON wiki_page_labels.repository_id = wiki_search_documents.repository_id AND wiki_page_labels.slug = wiki_search_documents.slug" +
				" JOIN labels ON labels.id = wiki_page_labels.label_id" +
				" WHERE wiki_search_documents.repository_id = ?" +
				" AND (labels.name LIKE ?" + likeEscape + " OR labels.description LIKE ?" + likeEscape + ")",
			Args: []any{repoID, like, like},
		},
	}
	var b strings.Builder
	args := make([]any, 0, 7)
	for i, query := range queries {
		if i > 0 {
			b.WriteString(" UNION ")
		}
		b.WriteString(query.SQL)
		args = append(args, query.Args...)
	}
	return wikiSearchRawSQL{SQL: b.String(), Args: args}
}

func wikiSearchCurrentFTSTokenCandidateSQL(repoID uint, column, token string) wikiSearchRawSQL {
	field := "wiki_search_documents.body"
	if column == "title" {
		field = "wiki_search_documents.title"
	}
	sql := "SELECT wiki_search_documents.id FROM wiki_search_documents " +
		" JOIN (SELECT wiki_search_documents.id FROM wiki_search_documents WHERE FTS_MATCH_WORD(" +
		wikiSearchMySQLStringLiteral(token) + ", " + field + ")) AS fts_matches ON fts_matches.id = wiki_search_documents.id" +
		" WHERE wiki_search_documents.repository_id = ?"
	return wikiSearchRawSQL{SQL: sql, Args: []any{repoID}}
}

func wikiSearchCurrentPageJoinSQL() string {
	return "JOIN wiki_pages ON wiki_pages.repository_id = wiki_search_documents.repository_id AND wiki_pages.slug = wiki_search_documents.slug"
}

func wikiSearchCurrentPredicateSQL() string {
	return "wiki_search_documents.repository_id = ? AND wiki_pages.deleted_at IS NULL AND wiki_search_documents.revision_sha = wiki_pages.head_blob_sha"
}

func (s *Service) wikiSearchLabelFilterPredicates(ctx context.Context, repoID uint, filters WikiLabelFilters) ([]wikiSearchRawSQL, bool, error) {
	if !hasWikiLabelFilters(filters) {
		return nil, false, nil
	}

	predicates := make([]wikiSearchRawSQL, 0, len(filters.Labels)+1)
	for _, labelName := range uniqueLabelNames(filters.Labels) {
		label, err := s.repoLabelByName(ctx, repoID, labelName)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil, true, nil
			}
			return nil, false, err
		}
		predicates = append(predicates, wikiSearchRawSQL{
			SQL:  "EXISTS (SELECT 1 FROM wiki_page_labels WHERE wiki_page_labels.repository_id = wiki_search_documents.repository_id AND wiki_page_labels.slug = wiki_search_documents.slug AND wiki_page_labels.label_id = ?)",
			Args: []any{label.ID},
		})
	}

	excludeLabels, err := s.resolveRepoLabels(ctx, repoID, filters.ExcludeLabels)
	if err != nil {
		return nil, false, err
	}
	if len(excludeLabels) > 0 {
		labelIDs := make([]any, 0, len(excludeLabels))
		for _, label := range excludeLabels {
			labelIDs = append(labelIDs, label.ID)
		}
		predicates = append(predicates, wikiSearchRawSQL{
			SQL: "NOT EXISTS (SELECT 1 FROM wiki_page_labels WHERE wiki_page_labels.repository_id = wiki_search_documents.repository_id " +
				"AND wiki_page_labels.slug = wiki_search_documents.slug AND wiki_page_labels.label_id IN (" + wikiSearchSQLPlaceholders(len(labelIDs)) + "))",
			Args: labelIDs,
		})
	}
	return predicates, false, nil
}

func wikiSearchSQLPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

func (s *Service) wikiSearchCurrentDocumentsByID(ctx context.Context, repoID uint, ids []uint, filters WikiLabelFilters, limit int) ([]db.WikiSearchDocument, error) {
	if len(ids) == 0 {
		return []db.WikiSearchDocument{}, nil
	}
	q, noResults, err := s.currentWikiSearchDocumentsByIDQuery(ctx, repoID, ids, filters)
	if err != nil {
		return nil, err
	}
	if noResults {
		return []db.WikiSearchDocument{}, nil
	}
	if limit > 0 {
		q = q.Limit(limit)
	}

	var docs []db.WikiSearchDocument
	if err := q.Order("wiki_search_documents.updated_at desc, wiki_search_documents.slug asc").Find(&docs).Error; err != nil {
		return nil, err
	}
	return docs, nil
}

func (s *Service) currentWikiSearchDocumentsByIDQuery(ctx context.Context, repoID uint, ids []uint, filters WikiLabelFilters) (*gorm.DB, bool, error) {
	q := s.currentWikiSearchDocumentsQuery(ctx, repoID).
		Select(wikiSearchDocumentHydrationColumns()).
		Where("wiki_search_documents.id IN ?", ids)
	return s.applyWikiSearchLabelPredicates(ctx, repoID, q, filters)
}

func wikiSearchDocumentHydrationColumns() []string {
	return []string{
		"wiki_search_documents.id",
		"wiki_search_documents.repository_id",
		"wiki_search_documents.slug",
		"wiki_search_documents.title",
		"wiki_search_documents.body",
		"wiki_search_documents.revision_sha",
		"wiki_search_documents.label_digest",
		"wiki_search_documents.created_at",
		"wiki_search_documents.updated_at",
	}
}

func wikiSearchLexicalCandidateIDLimit(rankLimit int) int {
	if rankLimit <= 0 {
		return wikiLexicalCandidateIDMin
	}
	limit := rankLimit * 20
	if limit < wikiLexicalCandidateIDMin {
		return wikiLexicalCandidateIDMin
	}
	if limit > wikiLexicalCandidateIDMax {
		return wikiLexicalCandidateIDMax
	}
	return limit
}

func wikiSearchLexicalHydrationLimit(rankLimit int) int {
	if rankLimit <= 0 {
		return wikiLexicalHydrationMin
	}
	limit := rankLimit * 3
	if limit < wikiLexicalHydrationMin {
		return wikiLexicalHydrationMin
	}
	if limit > wikiLexicalHydrationMax {
		return wikiLexicalHydrationMax
	}
	return limit
}

type wikiSearchEmbeddingResult struct {
	query string
	vec   []float32
	err   error
}

type wikiSearchSemanticResult struct {
	results []WikiSearchResult
	ok      bool
	err     error
}

func (s *Service) startWikiSearchEmbedding(ctx context.Context, query string) <-chan wikiSearchEmbeddingResult {
	if s.Embedder == nil || embedding.IsNop(s.Embedder) {
		return nil
	}
	resultC := make(chan wikiSearchEmbeddingResult, 1)
	query = embedding.TruncateInput(query)
	go func() {
		vec, err := s.Embedder.Embed(ctx, query)
		resultC <- wikiSearchEmbeddingResult{query: query, vec: vec, err: err}
	}()
	return resultC
}

func (s *Service) startWikiSearchSemanticCandidates(ctx context.Context, repoID uint, query string, embeddingResultC <-chan wikiSearchEmbeddingResult, filters WikiLabelFilters, rankWindow int) <-chan wikiSearchSemanticResult {
	if embeddingResultC == nil {
		return nil
	}
	resultC := make(chan wikiSearchSemanticResult, 1)
	go func() {
		embeddingResult := <-embeddingResultC
		if embeddingResult.err != nil {
			resultC <- wikiSearchSemanticResult{err: embeddingResult.err}
			return
		}
		semanticQuery := embeddingResult.query
		if semanticQuery == "" {
			semanticQuery = query
		}
		results, ok, err := s.searchWikiSemanticCandidatesWithVector(ctx, repoID, semanticQuery, embeddingResult.vec, filters, rankWindow)
		resultC <- wikiSearchSemanticResult{results: results, ok: ok, err: err}
	}()
	return resultC
}

func (s *Service) searchWikiSemanticCandidatesWithVector(ctx context.Context, repoID uint, query string, vec []float32, filters WikiLabelFilters, rankWindow int) ([]WikiSearchResult, bool, error) {
	if len(vec) == 0 {
		return nil, false, nil
	}
	if rankWindow <= 0 {
		rankWindow = wikiSearchMinRankWindow
	}
	if !db.SupportsVectorDistance(s.DBForCtx(ctx)) {
		results, ok, err := s.searchWikiSemanticInMemory(ctx, repoID, query, vec, rankWindow, 0, filters, false)
		if err != nil || !ok {
			return results, ok, err
		}
		return truncateWikiSearchResultList(results, rankWindow), true, nil
	}
	if db.IsTiDB(s.DBForCtx(ctx)) {
		return s.searchWikiSemanticANN(ctx, repoID, query, vec, rankWindow, 0, filters, false, false)
	}
	return s.searchWikiSemanticDB(ctx, repoID, query, vec, rankWindow, 0, filters, false, false)
}

func (s *Service) searchWikiSemanticInMemory(ctx context.Context, repoID uint, query string, vec []float32, limit, offset int, filters WikiLabelFilters, paginateBeforeHydration bool) ([]WikiSearchResult, bool, error) {
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
	if paginateBeforeHydration {
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

func wikiSemanticANNCandidateLimit(limit, offset int) int {
	n := limit + offset
	if n <= 0 {
		n = wikiSearchDefaultLimit
	}
	candidateLimit := n * 8
	if candidateLimit < wikiSemanticANNCandidateMin {
		candidateLimit = wikiSemanticANNCandidateMin
	}
	if candidateLimit > wikiSemanticANNCandidateMax {
		candidateLimit = wikiSemanticANNCandidateMax
	}
	return candidateLimit
}

func wikiSemanticRerankLimit(limit, offset int) int {
	n := limit + offset
	if n <= 0 {
		return wikiSearchDefaultLimit
	}
	if n > wikiSemanticANNCandidateMax {
		return wikiSemanticANNCandidateMax
	}
	return n
}

func buildWikiSemanticANNCandidateQuery(database *gorm.DB, vecLiteral string, limit, offset int) *gorm.DB {
	return database.Table("wiki_search_documents").
		Clauses(clause.OrderBy{
			Expression: clause.Expr{
				SQL:  "VEC_COSINE_DISTANCE(wiki_search_documents.embedding, ?) ASC",
				Vars: []any{vecLiteral},
			},
		}).
		Limit(wikiSemanticANNCandidateLimit(limit, offset))
}

func (s *Service) searchWikiSemanticANN(ctx context.Context, repoID uint, query string, vec []float32, limit, offset int, filters WikiLabelFilters, exactWindow, paginateBeforeHydration bool) ([]WikiSearchResult, bool, error) {
	vecLiteral := embedding.FormatVector(vec)
	database := s.DBForCtx(ctx)
	var candidateIDs []uint
	if err := buildWikiSemanticANNCandidateQuery(database, vecLiteral, limit, offset).Pluck("wiki_search_documents.id", &candidateIDs).Error; err != nil {
		return nil, false, err
	}
	if len(candidateIDs) == 0 {
		return nil, false, nil
	}

	filteredQ := database.Model(&db.WikiSearchDocument{}).
		Where("wiki_search_documents.repository_id = ?", repoID).
		Where("wiki_search_documents.embedding IS NOT NULL")
	var noResults bool
	var err error
	filteredQ, noResults, err = s.applyWikiSearchLabelPredicates(ctx, repoID, filteredQ, filters)
	if err != nil {
		return nil, false, err
	}
	if noResults {
		return nil, false, nil
	}

	q := filteredQ.Where("wiki_search_documents.id IN ?", candidateIDs)
	rows, err := s.scanWikiSemanticDBRows(q, query, vecLiteral, exactWindow, wikiSemanticRerankLimit(limit, offset), 0)
	if err != nil {
		return nil, false, err
	}
	return s.buildWikiSemanticResultsFromDBRows(ctx, repoID, rows, query, limit, offset, exactWindow, paginateBeforeHydration, false)
}

func (s *Service) searchWikiSemanticDB(ctx context.Context, repoID uint, query string, vec []float32, limit, offset int, filters WikiLabelFilters, exactWindow, paginateBeforeHydration bool) ([]WikiSearchResult, bool, error) {
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

	rows, err := s.scanWikiSemanticDBRows(q, query, vecLiteral, exactWindow, candidateLimit, dbOffset)
	if err != nil {
		return nil, false, err
	}
	return s.buildWikiSemanticResultsFromDBRows(ctx, repoID, rows, query, limit, offset, exactWindow, paginateBeforeHydration, true)
}

func buildWikiSemanticDBRowsQuery(q *gorm.DB, query, vecLiteral string, exactWindow bool, limit, offset int) *gorm.DB {
	selectSQL := strings.Join(wikiSearchDocumentHydrationColumns(), ",") + ", VEC_COSINE_DISTANCE(wiki_search_documents.embedding, ?) AS semantic_distance"
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
		Clauses(clause.OrderBy{Expression: clause.Expr{SQL: orderSQL}})
	if offset > 0 {
		queryDB = queryDB.Offset(offset)
	}
	if limit > 0 {
		queryDB = queryDB.Limit(limit)
	}
	return queryDB
}

func (s *Service) scanWikiSemanticDBRows(q *gorm.DB, query, vecLiteral string, exactWindow bool, limit, offset int) ([]wikiSemanticDBRow, error) {
	queryDB := buildWikiSemanticDBRowsQuery(q, query, vecLiteral, exactWindow, limit, offset)
	var rows []wikiSemanticDBRow
	if err := queryDB.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *Service) buildWikiSemanticResultsFromDBRows(ctx context.Context, repoID uint, rows []wikiSemanticDBRow, query string, limit, offset int, exactWindow, paginateBeforeHydration, rowsAlreadyPaginated bool) ([]WikiSearchResult, bool, error) {
	if len(rows) == 0 {
		return nil, false, nil
	}

	docs := make([]db.WikiSearchDocument, 0, len(rows))
	for _, row := range rows {
		docs = append(docs, row.WikiSearchDocument)
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
	if paginateBeforeHydration {
		if rowsAlreadyPaginated {
			return buildWikiSearchResults(scored, query), true, nil
		}
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
			Slug:            row.doc.Slug,
			Title:           titleFromSlug(row.doc.Slug),
			Score:           roundWikiScore(row.score),
			Snippet:         buildWikiSnippet(string(row.doc.Body), query),
			Labels:          row.labels,
			liveGitHydrated: false,
		})
	}
	return out
}

func markWikiSearchResultsLiveGitHydrated(results []WikiSearchResult) []WikiSearchResult {
	for i := range results {
		results[i].liveGitHydrated = true
	}
	return results
}

func markWikiSearchResultsCurrentIndex(results []WikiSearchResult) []WikiSearchResult {
	for i := range results {
		results[i].currentIndexBodyTrusted = true
	}
	return results
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

func truncateWikiSearchResultList(results []WikiSearchResult, limit int) []WikiSearchResult {
	if limit <= 0 || len(results) <= limit {
		return results
	}
	out := make([]WikiSearchResult, limit)
	copy(out, results[:limit])
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

func fuseWikiSearchResults(lexical, semantic []WikiSearchResult) []WikiSearchResult {
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
	return results
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

func (s *Service) wikiSearchCurrentDocuments(ctx context.Context, repoID uint, query string, requireEmbedding bool, filters WikiLabelFilters) ([]db.WikiSearchDocument, error) {
	var docs []db.WikiSearchDocument
	database := s.DBForCtx(ctx)
	q := s.currentWikiSearchDocumentsQuery(ctx, repoID)
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
	var noResults bool
	var err error
	q, noResults, err = s.applyWikiSearchLabelPredicates(ctx, repoID, q, filters)
	if err != nil {
		return nil, err
	}
	if noResults {
		return []db.WikiSearchDocument{}, nil
	}
	if err := q.Order("wiki_search_documents.updated_at desc").Find(&docs).Error; err != nil {
		return nil, err
	}
	return docs, nil
}

func wikiSearchDocumentTableMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such table: wiki_search_documents") ||
		strings.Contains(msg, "table `wiki_search_documents` doesn't exist") ||
		(strings.Contains(msg, "wiki_search_documents") && strings.Contains(msg, "doesn't exist"))
}

func wikiSearchLikeEscapeClause(database *gorm.DB) string {
	return sqlLikeEscapeClause(database)
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

func (s *Service) syncWikiSearchUpsert(ctx context.Context, repoFullName string, page WikiPage) error {
	return s.upsertWikiSearchDocument(ctx, repoFullName, page)
}

func (s *Service) syncWikiSearchDelete(ctx context.Context, repoFullName, slug string) error {
	return s.deleteWikiSearchDocument(ctx, repoFullName, slug)
}

func (s *Service) upsertWikiSearchDocument(ctx context.Context, repoFullName string, page WikiPage) error {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return err
	}
	if err := s.upsertWikiSearchLexicalDocument(ctx, repo.ID, page); err != nil {
		return err
	}
	if s.Embedder == nil || embedding.IsNop(s.Embedder) {
		return nil
	}
	if err := s.embedWikiSearchDocument(ctx, repo.ID, page); err != nil {
		slog.WarnContext(ctx, "wiki search embedding failed; lexical document remains available", "repo", repoFullName, "slug", page.Slug, "error", err)
	}
	return nil
}

func (s *Service) upsertWikiSearchLexicalDocument(ctx context.Context, repoID uint, page WikiPage) error {
	targetDB := s.DBForCtx(ctx)
	title := titleFromSlug(page.Slug)
	labelDigest := wikiPageLabelsText(page.Labels)
	now := time.Now()
	values := map[string]any{
		"repository_id": repoID,
		"slug":          page.Slug,
		"title":         title,
		"body":          db.LargeText(page.Body),
		"revision_sha":  page.SHA,
		"label_digest":  labelDigest,
		"created_at":    now,
		"updated_at":    now,
	}
	updateColumns := []string{"title", "body", "revision_sha", "label_digest", "updated_at"}
	if s.Embedder != nil &&
		!embedding.IsNop(s.Embedder) &&
		s.wikiSearchEmbeddingColumnAvailable(targetDB) {
		values["embedding"] = nil
		updateColumns = append(updateColumns, "embedding")
	}
	return targetDB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "repository_id"}, {Name: "slug"}},
		DoUpdates: clause.AssignmentColumns(updateColumns),
	}).Model(&db.WikiSearchDocument{}).Create(values).Error
}

func (s *Service) wikiSearchEmbeddingColumnAvailable(targetDB *gorm.DB) bool {
	if targetDB == nil {
		return false
	}
	sqlDB, err := s.sqlDBHandle(targetDB)
	if err != nil {
		return targetDB.Migrator().HasColumn("wiki_search_documents", "embedding")
	}

	s.wikiSearchEmbeddingColumnMu.Lock()
	defer s.wikiSearchEmbeddingColumnMu.Unlock()
	if available, checked := s.wikiSearchEmbeddingColumns[sqlDB]; checked {
		return available
	}
	if s.wikiSearchEmbeddingColumns == nil {
		s.wikiSearchEmbeddingColumns = make(map[*sql.DB]bool)
	}
	available := targetDB.Migrator().HasColumn("wiki_search_documents", "embedding")
	s.wikiSearchEmbeddingColumns[sqlDB] = available
	return available
}

func (s *Service) refreshWikiSearchEmbeddingColumn(targetDB *gorm.DB) {
	if targetDB == nil {
		return
	}
	sqlDB, err := s.sqlDBHandle(targetDB)
	if err != nil {
		return
	}
	available := targetDB.Migrator().HasColumn("wiki_search_documents", "embedding")

	s.wikiSearchEmbeddingColumnMu.Lock()
	defer s.wikiSearchEmbeddingColumnMu.Unlock()
	if s.wikiSearchEmbeddingColumns == nil {
		s.wikiSearchEmbeddingColumns = make(map[*sql.DB]bool)
	}
	s.wikiSearchEmbeddingColumns[sqlDB] = available
}

func (s *Service) embedWikiSearchDocument(ctx context.Context, repoID uint, page WikiPage) error {
	title := titleFromSlug(page.Slug)
	labelDigest := wikiPageLabelsText(page.Labels)
	vec, err := s.embedWithRetry(ctx, title+"\n"+labelDigest+"\n"+page.Body)
	if err != nil {
		return err
	}
	if len(vec) == 0 {
		return nil
	}

	targetDB := s.DBForCtx(ctx)
	s.ensureVectorInit(targetDB, len(vec))
	if !s.wikiSearchEmbeddingColumnAvailable(targetDB) {
		return nil
	}
	return targetDB.Model(&db.WikiSearchDocument{}).
		Where(
			"repository_id = ? AND slug = ? AND revision_sha = ? AND label_digest = ?",
			repoID,
			page.Slug,
			page.SHA,
			labelDigest,
		).
		UpdateColumn("embedding", embedding.FormatVector(vec)).Error
}

func (s *Service) deleteWikiSearchDocument(ctx context.Context, repoFullName, slug string) error {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return err
	}
	return s.DBForCtx(ctx).Where("repository_id = ? AND slug = ?", repo.ID, slug).Delete(&db.WikiSearchDocument{}).Error
}

func (s *Service) ReindexWikiSearch(ctx context.Context, repoFullName string) (int, error) {
	if err := s.ensureWikiCatalogCurrent(ctx, repoFullName); err != nil {
		return 0, err
	}
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return 0, err
	}

	var pages []db.WikiPage
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND deleted_at IS NULL", repo.ID).
		Order("page_id ASC").
		Find(&pages).Error; err != nil {
		return 0, err
	}

	var existing []db.WikiSearchDocument
	if err := s.DBForCtx(ctx).
		Where("repository_id = ?", repo.ID).
		Find(&existing).Error; err != nil {
		return 0, err
	}

	liveBySlug := make(map[string]db.WikiPage, len(pages))
	slugs := make([]string, 0, len(pages))
	for _, page := range pages {
		liveBySlug[page.Slug] = page
		slugs = append(slugs, page.Slug)
	}

	staleSlugs := make([]string, 0)
	existingBySlug := make(map[string]db.WikiSearchDocument, len(existing))
	for _, doc := range existing {
		existingBySlug[doc.Slug] = doc
		if _, ok := liveBySlug[doc.Slug]; !ok {
			staleSlugs = append(staleSlugs, doc.Slug)
		}
	}
	if len(staleSlugs) > 0 {
		if err := s.DBForCtx(ctx).
			Where("repository_id = ? AND slug IN ?", repo.ID, staleSlugs).
			Delete(&db.WikiSearchDocument{}).Error; err != nil {
			return 0, err
		}
	}

	labelsBySlug, err := s.wikiLabelsForSlugs(ctx, repo.ID, slugs)
	if err != nil {
		return 0, err
	}

	toRefresh := make([]WikiPage, 0, len(pages))
	for _, page := range pages {
		labelDigest := wikiPageLabelsText(labelsBySlug[page.Slug])
		title := titleFromSlug(page.Slug)
		if doc, ok := existingBySlug[page.Slug]; ok && doc.RevisionSHA == page.HeadBlobSHA && doc.LabelDigest == labelDigest && doc.Title == title {
			continue
		}
		body, err := s.wikiPageBody(ctx, page)
		if err != nil {
			return 0, err
		}
		toRefresh = append(toRefresh, WikiPage{
			Slug:       page.Slug,
			Title:      title,
			Body:       string(body),
			UpdatedAt:  page.UpdatedAt,
			SHA:        page.HeadBlobSHA,
			LastAuthor: page.LastAuthor,
			Labels:     labelsBySlug[page.Slug],
		})
	}

	if err := s.reindexWikiSearchDocuments(ctx, repoFullName, toRefresh); err != nil {
		return 0, err
	}
	return len(pages), nil
}

func (s *Service) reindexWikiSearchDocuments(ctx context.Context, repoFullName string, pages []WikiPage) error {
	if len(pages) == 0 {
		return nil
	}

	workers := wikiReindexWorkers
	if workers > len(pages) {
		workers = len(pages)
	}
	if maxProcs := runtime.GOMAXPROCS(0); maxProcs > 0 && workers > maxProcs {
		workers = maxProcs
	}
	if workers < 1 {
		workers = 1
	}

	workCh := make(chan WikiPage)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for page := range workCh {
			if ctx.Err() != nil {
				return
			}
			if err := s.upsertWikiSearchDocument(ctx, repoFullName, page); err != nil {
				select {
				case errCh <- err:
				default:
				}
				return
			}
		}
	}

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go worker()
	}

	for _, page := range pages {
		select {
		case err := <-errCh:
			close(workCh)
			wg.Wait()
			return err
		case <-ctx.Done():
			close(workCh)
			wg.Wait()
			return ctx.Err()
		case workCh <- page:
		}
	}
	close(workCh)
	wg.Wait()

	select {
	case err := <-errCh:
		return err
	default:
	}
	return nil
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
