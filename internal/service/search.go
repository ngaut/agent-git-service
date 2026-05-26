package service

import (
	"context"
	"log/slog"

	"gh-server/internal/db"
	"gh-server/internal/embedding"
	searchsvc "gh-server/internal/service/search"

	"gorm.io/gorm"
)

// Type aliases preserve the public API while delegating to the search package.
type CoreFilters = searchsvc.CoreFilters

type NegationFilters = searchsvc.NegationFilters

type MetadataFilters = searchsvc.MetadataFilters

type ParserFields = searchsvc.ParserFields

type SearchQualifiers = searchsvc.SearchQualifiers

type CommitSearchQuery = searchsvc.CommitSearchQuery

type CodeSearchQuery = searchsvc.CodeSearchQuery

type IssueListFilter = searchsvc.IssueListFilter

// ParseSearchQuery parses a GitHub-style search query into structured qualifiers.
func ParseSearchQuery(query string) SearchQualifiers {
	return searchsvc.ParseSearchQuery(query)
}

// ParseCommitSearchQuery parses a commit search query, extracting free text.
func ParseCommitSearchQuery(query string) CommitSearchQuery {
	return searchsvc.ParseCommitSearchQuery(query)
}

// ParseCodeSearchQuery parses a code search query, extracting free text.
func ParseCodeSearchQuery(query string) CodeSearchQuery {
	return searchsvc.ParseCodeSearchQuery(query)
}

// GetExtensionsForLanguage returns file extensions for a given language.
func GetExtensionsForLanguage(language string) []string {
	return searchsvc.GetExtensionsForLanguage(language)
}

// HasRepoSearchFilters reports whether repo-specific qualifiers were provided.
func HasRepoSearchFilters(sq SearchQualifiers) bool {
	return searchsvc.HasRepoSearchFilters(sq)
}

// SearchIssues performs a hybrid search on the issues table.
func (s *Service) SearchIssues(ctx context.Context, query string) ([]db.Issue, error) {
	return searchsvc.SearchIssues(ctx, searchsvc.IssueSearchDeps{
		DBForCtx:           s.DBForCtx,
		PreloadIssue:       preloadIssue,
		DefaultListLimit:   defaultListLimit,
		IssueSortQualifier: issueSortQualifier,
		EmbedQuery:         s.embedQuery,
		UserFromContext:    UserFromContext,
	}, query)
}

// SearchPRs performs a hybrid search on the pull_requests table.
func (s *Service) SearchPRs(ctx context.Context, query string) ([]db.PullRequest, error) {
	return searchsvc.SearchPRs(ctx, searchsvc.PRSearchDeps{
		DBForCtx:           s.DBForCtx,
		PreloadPR:          preloadPRFull,
		DefaultListLimit:   defaultListLimit,
		IssueSortQualifier: issueSortQualifier,
		EmbedQuery:         s.embedQuery,
		UserFromContext:    UserFromContext,
	}, query)
}

// ListIssuesFiltered returns issues for a repo with filterBy support.
func (s *Service) ListIssuesFiltered(ctx context.Context, filter IssueListFilter) ([]db.Issue, error) {
	return searchsvc.ListIssuesFiltered(ctx, searchsvc.IssueListDeps{
		DBForCtx:                  s.DBForCtx,
		PreloadIssue:              preloadIssue,
		DefaultListLimit:          defaultListLimit,
		IssueSortQualifier:        issueSortQualifier,
		GetRepo:                   s.GetRepo,
		ApplyIssueSinceFilter:     applyIssueSinceFilter,
		ApplyIssueMilestoneFilter: applyIssueMilestoneFilter,
	}, filter)
}

// applyIssueQualifiers applies all structured search qualifiers to an issue query.
func applyIssueQualifiers(q *gorm.DB, baseDB *gorm.DB, sq SearchQualifiers) *gorm.DB {
	return searchsvc.ApplyIssueQualifiers(q, baseDB, sq)
}

// applyPRQualifiers applies all structured search qualifiers to a PR query.
func applyPRQualifiers(q *gorm.DB, baseDB *gorm.DB, sq SearchQualifiers) *gorm.DB {
	return searchsvc.ApplyPRQualifiers(q, baseDB, sq)
}

// buildIssueTextWhere builds the WHERE clause for issue text search based on the in: qualifier.
func buildIssueTextWhere(inValues []string, text string) (string, []any) {
	return searchsvc.BuildIssueTextWhere(inValues, text)
}

// buildPRTextWhere builds the WHERE clause for PR text search based on the in: qualifier.
func buildPRTextWhere(inValues []string, text string) (string, []any) {
	return searchsvc.BuildPRTextWhere(inValues, text)
}

// buildIssueInFilter builds a WHERE clause to filter results based on in: qualifier.
func buildIssueInFilter(inValues []string) (string, []any) {
	return searchsvc.BuildIssueInFilter(inValues)
}

// buildPRInFilter builds a WHERE clause to filter PR results based on in: qualifier.
func buildPRInFilter(inValues []string) (string, []any) {
	return searchsvc.BuildPRInFilter(inValues)
}

// sortOrder returns the SQL ORDER BY clause for the given sort qualifier.
func sortOrder(sort, prefix string) string {
	return searchsvc.SortOrder(sort, prefix)
}

// deduplicateIssues merges primary and secondary slices, skipping any duplicates by ID.
func deduplicateIssues(primary, secondary []db.Issue) []db.Issue {
	return searchsvc.DeduplicateIssues(primary, secondary, defaultListLimit)
}

// deduplicatePRs merges primary and secondary slices, skipping any duplicates by ID.
func deduplicatePRs(primary, secondary []db.PullRequest) []db.PullRequest {
	return searchsvc.DeduplicatePRs(primary, secondary, defaultListLimit)
}

// embedQuery attempts to embed a search query string. Returns the formatted
// vector literal if successful, or empty string if embedding is unavailable.
func (s *Service) embedQuery(ctx context.Context, text string) string {
	if s.Embedder == nil || embedding.IsNop(s.Embedder) {
		return ""
	}
	text = embedding.TruncateInput(text)
	vec, err := s.Embedder.Embed(ctx, text)
	if err != nil {
		slog.WarnContext(ctx, "search embed query failed; falling back to lexical search", "error", err)
		return ""
	}
	if len(vec) == 0 {
		return ""
	}
	targetDB := s.DB
	if tenantDB, ok := DBFromContext(ctx); ok {
		targetDB = tenantDB
	}
	s.ensureVectorInit(targetDB, len(vec))
	return embedding.FormatVector(vec)
}
