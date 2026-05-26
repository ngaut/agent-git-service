// Package service — wiki page CRUD backed by a sibling bare git repo.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

// wikiDefaultBranch matches GitHub's wiki convention so a wiki repo cloned
// directly via git uses the same branch name clients expect.
const wikiDefaultBranch = "master"

// wikiPageExt is the on-disk extension for a wiki page. Phase C ships
// markdown only; future phases can support .rst / .textile by widening
// the extension whitelist in resolveWikiSlug.
const wikiPageExt = ".md"

const (
	wikiMaxSlugDepth      = 6
	wikiMaxSlugLength     = 255
	wikiMaxSegmentLength  = 64
	wikiMoveCodeStale     = "SOURCE_STALE"
	wikiMoveCodeDestTaken = "DESTINATION_EXISTS"
	wikiMoveCodePrefix    = "PREFIX_COLLISION"
	wikiMoveCodeMissing   = "IF_MATCH_INCOMPLETE"
	wikiMoveCodeNotFound  = "SOURCE_NOT_FOUND"
)

// WikiPage is the canonical view of a single wiki page.
type WikiPage struct {
	Slug       string
	Title      string
	Body       string
	UpdatedAt  time.Time
	SHA        string
	LastAuthor *db.User
	Labels     []db.Label
}

// WikiPageSummary is the trimmed shape used for list responses.
type WikiPageSummary struct {
	Slug       string
	Title      string
	SHA        string
	UpdatedAt  time.Time
	LastAuthor *db.User
	Labels     []db.Label
}

type WikiRewriteSkip struct {
	Slug   string
	Reason string
}

type WikiMoveResult struct {
	Moved    WikiPage
	Rewrites []WikiPageSummary
	Skipped  []WikiRewriteSkip
}

type ListWikiPagesOptions struct {
	Path          string
	Recursive     bool
	Labels        []string
	ExcludeLabels []string
}

type WikiLabelFilters struct {
	Labels        []string
	ExcludeLabels []string
}

// WikiBacklink is the read model for inbound wiki link results.
type WikiBacklink struct {
	Slug    string
	Title   string
	Snippet string
}

// WikiPageHistoryEntry is the read model for one wiki page revision.
type WikiPageHistoryEntry struct {
	SHA       string
	Message   string
	Author    *db.User
	Committer *db.User
	Date      time.Time
	BodySize  int
}

// WikiBulkMoveEntry reports one source-to-destination wiki slug move.
type WikiBulkMoveEntry struct {
	From string
	To   string
	SHA  string
}

// WikiBulkMoveResult is the API shape for one successful prefix move.
type WikiBulkMoveResult struct {
	Moved    []WikiBulkMoveEntry
	Rewrites []WikiPageSummary
	Skipped  []WikiRewriteSkip
	Commit   string
}

// WikiBulkMoveConflict reports one per-page conflict discovered during bulk move
// validation.
type WikiBulkMoveConflict struct {
	From          string
	To            string
	Code          string
	Message       string
	CurrentSHA    string
	ConflictsWith string
}

// WikiBulkMoveValidationError reports a missing if_match coverage set.
type WikiBulkMoveValidationError struct {
	From         string
	MissingSlugs []string
}

func (e *WikiBulkMoveValidationError) Error() string {
	if e == nil {
		return fmt.Sprintf("%v: if_match is required", ErrValidation)
	}
	return fmt.Sprintf("%v: if_match must include every source wiki page under %q", ErrValidation, e.From)
}

func (e *WikiBulkMoveValidationError) Unwrap() error { return ErrValidation }
func (e *WikiBulkMoveValidationError) Code() string  { return wikiMoveCodeMissing }

// WikiBulkMoveNotFoundError reports an empty source prefix.
type WikiBulkMoveNotFoundError struct {
	From string
}

func (e *WikiBulkMoveNotFoundError) Error() string {
	if e == nil {
		return ErrNotFound.Error()
	}
	return fmt.Sprintf("wiki move source prefix %q not found", e.From)
}

func (e *WikiBulkMoveNotFoundError) Unwrap() error { return ErrNotFound }
func (e *WikiBulkMoveNotFoundError) Code() string  { return wikiMoveCodeNotFound }

// WikiBulkMoveConflictError reports one or more bulk move conflicts while
// preserving the underlying 409 contract.
type WikiBulkMoveConflictError struct {
	Conflicts []WikiBulkMoveConflict
}

func (e *WikiBulkMoveConflictError) Error() string {
	if e == nil || len(e.Conflicts) == 0 {
		return ErrConflict.Error()
	}
	return fmt.Sprintf("%v: wiki bulk move has %d conflict(s)", ErrConflict, len(e.Conflicts))
}

func (e *WikiBulkMoveConflictError) Unwrap() error { return ErrConflict }

type wikiBacklinkCacheEntry struct {
	HeadSHA   string
	Backlinks []WikiBacklink
}

type wikiLinkMatch struct {
	targetSlug string
	snippet    string
	literal    bool
}

type wikiMoveConflictError struct {
	code    string
	message string
}

func (e *wikiMoveConflictError) Error() string { return e.message }
func (e *wikiMoveConflictError) Unwrap() error { return ErrConflict }
func (e *wikiMoveConflictError) Code() string  { return e.code }

var (
	wikiMarkdownLinkRE = regexp.MustCompile(`\[[^\]]+\]\(([^)]+)\)`)
	wikiBracketLinkRE  = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	wikiCommitSHARE    = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
)

// wikiRepoFullName returns the sibling repo name where wiki pages are stored.
// Mirrors GitHub's "{repo}.wiki.git" convention so direct git clones work.
func wikiRepoFullName(repoFullName string) string {
	return repoFullName + ".wiki"
}

// validateWikiSlug enforces the path-slug grammar for wiki pages.
func validateWikiSlug(slug string) error {
	if slug == "" {
		return fmt.Errorf("%w: empty wiki slug", ErrValidation)
	}
	if err := validateReadableWikiSlug(slug); err != nil {
		return err
	}
	if slug != strings.ToLower(slug) {
		return fmt.Errorf("%w: wiki slug must be lowercase", ErrValidation)
	}
	for _, part := range strings.Split(slug, "/") {
		if err := validateWikiSlugSegment(part); err != nil {
			return err
		}
	}
	return nil
}

func validateReadableWikiSlug(slug string) error {
	if slug == "" {
		return fmt.Errorf("%w: empty wiki slug", ErrValidation)
	}
	if len(slug) > wikiMaxSlugLength {
		return fmt.Errorf("%w: wiki slug too long", ErrValidation)
	}
	parts := strings.Split(slug, "/")
	if len(parts) > wikiMaxSlugDepth {
		return fmt.Errorf("%w: wiki slug exceeds max depth", ErrValidation)
	}
	for _, part := range parts {
		if err := validateReadableWikiSlugSegment(part); err != nil {
			return err
		}
	}
	return nil
}

func validateWikiSlugSegment(segment string) error {
	if segment == "" {
		return fmt.Errorf("%w: wiki slug contains an empty path segment", ErrValidation)
	}
	if segment == "." || segment == ".." {
		return fmt.Errorf("%w: wiki slug contains reserved path segment %q", ErrValidation, segment)
	}
	if len(segment) > wikiMaxSegmentLength {
		return fmt.Errorf("%w: wiki slug segment too long", ErrValidation)
	}
	if segment == "_sidebar" {
		return nil
	}
	for i, r := range segment {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' && i > 0:
		default:
			return fmt.Errorf("%w: slug contains disallowed character %q", ErrValidation, string(r))
		}
	}
	if segment[0] == '-' {
		return fmt.Errorf("%w: slug cannot start with %q", ErrValidation, "-")
	}
	return nil
}

func validateReadableWikiSlugSegment(segment string) error {
	if segment == "" {
		return fmt.Errorf("%w: wiki slug contains an empty path segment", ErrValidation)
	}
	if segment == "." || segment == ".." {
		return fmt.Errorf("%w: wiki slug contains reserved path segment %q", ErrValidation, segment)
	}
	if len(segment) > wikiMaxSegmentLength {
		return fmt.Errorf("%w: wiki slug segment too long", ErrValidation)
	}
	if segment == "_sidebar" {
		return nil
	}
	for i, r := range segment {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case (r == '-' || r == '_' || r == '.') && i > 0:
		default:
			return fmt.Errorf("%w: slug contains disallowed character %q", ErrValidation, string(r))
		}
	}
	if segment[0] == '-' || segment[0] == '_' || segment[0] == '.' {
		return fmt.Errorf("%w: slug cannot start with reserved punctuation", ErrValidation)
	}
	return nil
}

// wikiSlugToPath maps a slug to its on-disk markdown filename inside the
// wiki repo.
func wikiSlugToPath(slug string) string {
	return slug + wikiPageExt
}

// wikiPathToSlug returns the slug for a path in the wiki repo, or "" if
// the path isn't a recognised wiki page.
func wikiPathToSlug(path string) string {
	if path == "" || strings.HasPrefix(path, ".") {
		return ""
	}
	if !strings.HasSuffix(path, wikiPageExt) {
		return ""
	}
	slug := strings.TrimSuffix(path, wikiPageExt)
	if validateReadableWikiSlug(slug) != nil {
		return ""
	}
	return slug
}

func canonicalWikiLookupSlug(slug string) string {
	parts := strings.Split(slug, "/")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return ""
		}
		part = strings.ReplaceAll(part, "_", "-")
		part = strings.Join(strings.Fields(part), "-")
		parts[i] = strings.ToLower(part)
	}
	canonical := strings.Join(parts, "/")
	if err := validateReadableWikiSlug(canonical); err != nil {
		return ""
	}
	return canonical
}

// titleFromSlug derives the stable display title returned by wiki APIs. It is
// intentionally independent from page body contents so title responses are
// deterministic and list responses do not need to read every page body.
func titleFromSlug(slug string) string {
	return wikicatalog.TitleFromSlug(slug)
}

func normalizeWikiReference(raw string) string {
	link := strings.TrimSpace(raw)
	if link == "" {
		return ""
	}
	if i := strings.Index(link, "#"); i >= 0 {
		link = link[:i]
	}
	if i := strings.Index(link, "?"); i >= 0 {
		link = link[:i]
	}
	link = strings.TrimSpace(link)
	if link == "" {
		return ""
	}
	if u, err := url.Parse(link); err == nil && u.Scheme != "" {
		return ""
	}
	link = strings.TrimPrefix(link, "./")
	link = strings.TrimPrefix(link, "/")
	if strings.Contains(link, "../") || strings.HasPrefix(link, "..") {
		return ""
	}
	link = strings.TrimSuffix(link, wikiPageExt)
	link = strings.TrimSpace(link)
	if link == "" {
		return ""
	}
	parts := strings.Split(link, "/")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return ""
		}
		part = strings.Join(strings.Fields(part), "-")
		parts[i] = part
	}
	link = strings.Join(parts, "/")
	if err := validateReadableWikiSlug(link); err != nil {
		return ""
	}
	return link
}

func extractWikiLinkMatches(body string) []wikiLinkMatch {
	var matches []wikiLinkMatch
	for _, loc := range wikiMarkdownLinkRE.FindAllStringSubmatchIndex(body, -1) {
		if len(loc) < 4 {
			continue
		}
		if loc[0] > 0 && body[loc[0]-1] == '!' {
			continue
		}
		target := normalizeWikiReference(body[loc[2]:loc[3]])
		if target == "" {
			continue
		}
		matches = append(matches, wikiLinkMatch{
			targetSlug: target,
			snippet:    snippetAround(body, loc[0], loc[1]),
			literal:    strings.Contains(target, "/"),
		})
	}
	for _, loc := range wikiBracketLinkRE.FindAllStringSubmatchIndex(body, -1) {
		if len(loc) < 4 {
			continue
		}
		target := normalizeWikiReference(body[loc[2]:loc[3]])
		if target == "" {
			continue
		}
		matches = append(matches, wikiLinkMatch{
			targetSlug: target,
			snippet:    snippetAround(body, loc[0], loc[1]),
			literal:    strings.Contains(target, "/"),
		})
	}
	return matches
}

func snippetAround(body string, start, end int) string {
	const radius = 40
	left := start - radius
	if left < 0 {
		left = 0
	}
	right := end + radius
	if right > len(body) {
		right = len(body)
	}
	snippet := strings.TrimSpace(strings.ReplaceAll(body[left:right], "\n", " "))
	return strings.Join(strings.Fields(snippet), " ")
}

func (s *Service) invalidateWikiBacklinks(repoFullName string) {
	s.wikiBacklinksMu.Lock()
	defer s.wikiBacklinksMu.Unlock()
	if s.wikiBacklinksCache == nil {
		return
	}
	delete(s.wikiBacklinksCache, repoFullName)
}

func copyWikiBacklinks(backlinks []WikiBacklink) []WikiBacklink {
	if len(backlinks) == 0 {
		return []WikiBacklink{}
	}
	out := make([]WikiBacklink, len(backlinks))
	copy(out, backlinks)
	return out
}

func (s *Service) cachedWikiBacklinks(repoFullName, slug, headSHA string) ([]WikiBacklink, bool) {
	s.wikiBacklinksMu.RLock()
	defer s.wikiBacklinksMu.RUnlock()
	if s.wikiBacklinksCache == nil {
		return nil, false
	}
	repoCache := s.wikiBacklinksCache[repoFullName]
	if repoCache == nil {
		return nil, false
	}
	entry, ok := repoCache[slug]
	if !ok || entry.HeadSHA != headSHA {
		return nil, false
	}
	return copyWikiBacklinks(entry.Backlinks), true
}

func (s *Service) storeCachedWikiBacklinks(repoFullName, slug, headSHA string, backlinks []WikiBacklink) {
	s.wikiBacklinksMu.Lock()
	defer s.wikiBacklinksMu.Unlock()
	if s.wikiBacklinksCache == nil {
		s.wikiBacklinksCache = map[string]map[string]wikiBacklinkCacheEntry{}
	}
	if s.wikiBacklinksCache[repoFullName] == nil {
		s.wikiBacklinksCache[repoFullName] = map[string]wikiBacklinkCacheEntry{}
	}
	s.wikiBacklinksCache[repoFullName][slug] = wikiBacklinkCacheEntry{
		HeadSHA:   headSHA,
		Backlinks: copyWikiBacklinks(backlinks),
	}
}

func wikiBacklinkGrepPatterns(slug string) []string {
	seen := map[string]struct{}{}
	var patterns []string
	addPattern := func(pattern string) {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			return
		}
		if _, ok := seen[pattern]; ok {
			return
		}
		seen[pattern] = struct{}{}
		patterns = append(patterns, pattern)
	}

	addPattern(slug)
	canonical := canonicalWikiLookupSlug(slug)
	if canonical != "" {
		addPattern(canonical)
	}

	variants, overflow := wikiSlugSeparatorVariants(slug, wikiBacklinkGrepPatternLimit-len(patterns))
	if overflow {
		return nil
	}
	for _, variant := range variants {
		addPattern(variant)
	}
	if canonical != "" {
		variants, overflow = wikiSlugSeparatorVariants(canonical, wikiBacklinkGrepPatternLimit-len(patterns))
		if overflow {
			return nil
		}
		for _, variant := range variants {
			addPattern(variant)
		}
	}
	return patterns
}

const wikiBacklinkGrepPatternLimit = 256

func wikiSlugSeparatorVariants(slug string, limit int) ([]string, bool) {
	if limit <= 0 {
		return nil, true
	}
	parts := strings.Split(slug, "/")
	segmentVariants := make([][]string, len(parts))
	for i, part := range parts {
		segmentVariants[i] = wikiSegmentSeparatorVariants(part)
	}
	seen := map[string]struct{}{}
	var out []string
	overflow := false
	var build func(int, []string)
	build = func(idx int, acc []string) {
		if overflow {
			return
		}
		if idx == len(segmentVariants) {
			joined := strings.Join(acc, "/")
			if joined == "" {
				return
			}
			if _, ok := seen[joined]; ok {
				return
			}
			if len(out) == limit {
				overflow = true
				return
			}
			seen[joined] = struct{}{}
			out = append(out, joined)
			return
		}
		for _, variant := range segmentVariants[idx] {
			build(idx+1, append(acc, variant))
		}
	}
	build(0, nil)
	return out, overflow
}

func wikiSegmentSeparatorVariants(segment string) []string {
	seen := map[string]struct{}{}
	var variants []string
	add := func(v string) {
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		variants = append(variants, v)
	}

	add(segment)
	add(strings.ReplaceAll(segment, "-", " "))
	add(strings.ReplaceAll(segment, "-", "_"))
	add(strings.ReplaceAll(segment, "_", " "))
	add(strings.ReplaceAll(segment, "_", "-"))
	return variants
}

func resolveWikiBacklinkTarget(match wikiLinkMatch, pages, topLevelPages map[string]struct{}, canonicalPages, canonicalTopLevelPages map[string]string) (string, bool) {
	resolvedTarget := match.targetSlug
	if match.literal {
		if _, ok := pages[resolvedTarget]; ok {
			return resolvedTarget, true
		}
		canonical := canonicalWikiLookupSlug(match.targetSlug)
		if canonical == "" {
			return "", false
		}
		resolvedTarget = canonicalPages[canonical]
		return resolvedTarget, resolvedTarget != ""
	}

	if strings.Contains(resolvedTarget, "/") {
		return "", false
	}
	if _, ok := topLevelPages[resolvedTarget]; ok {
		return resolvedTarget, true
	}
	canonical := canonicalWikiLookupSlug(match.targetSlug)
	if canonical == "" {
		return "", false
	}
	resolvedTarget = canonicalTopLevelPages[canonical]
	return resolvedTarget, resolvedTarget != ""
}

func (s *Service) loadWikiBacklinksForSlug(ctx context.Context, repoFullName, targetSlug string) ([]WikiBacklink, error) {
	full := wikiRepoFullName(repoFullName)
	headSHA, err := s.Git.HeadSHA(ctx, full, wikiDefaultBranch)
	if err != nil {
		return nil, err
	}

	if cached, ok := s.cachedWikiBacklinks(repoFullName, targetSlug, headSHA); ok {
		return cached, nil
	}

	paths, err := s.Git.ListTreeFilesAtRef(ctx, full, headSHA)
	if err != nil {
		return nil, err
	}
	pages := make(map[string]struct{}, len(paths))
	topLevelPages := make(map[string]struct{}, len(paths))
	canonicalPages := make(map[string]string, len(paths))
	canonicalTopLevelPages := make(map[string]string, len(paths))
	for _, p := range paths {
		slug := wikiPathToSlug(p)
		if slug == "" {
			continue
		}
		pages[slug] = struct{}{}
		if !strings.Contains(slug, "/") {
			topLevelPages[slug] = struct{}{}
		}
		if canonical := canonicalWikiLookupSlug(slug); canonical != "" {
			canonicalPages[canonical] = slug
			if !strings.Contains(slug, "/") {
				canonicalTopLevelPages[canonical] = slug
			}
		}
	}
	if _, ok := pages[targetSlug]; !ok {
		return nil, ErrNotFound
	}

	candidatePaths := paths
	if patterns := wikiBacklinkGrepPatterns(targetSlug); len(patterns) > 0 {
		candidatePaths, err = s.Git.GrepFilesAtRef(ctx, full, headSHA, patterns)
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(candidatePaths)

	backlinks := make([]WikiBacklink, 0)
	for _, path := range candidatePaths {
		sourceSlug := wikiPathToSlug(path)
		if sourceSlug == "" {
			continue
		}
		body, err := s.Git.ReadFileAtRef(ctx, full, path, headSHA)
		if err != nil {
			return nil, err
		}
		for _, match := range extractWikiLinkMatches(string(body)) {
			resolvedTarget, ok := resolveWikiBacklinkTarget(match, pages, topLevelPages, canonicalPages, canonicalTopLevelPages)
			if !ok || resolvedTarget != targetSlug {
				continue
			}
			backlinks = append(backlinks, WikiBacklink{
				Slug:    sourceSlug,
				Title:   titleFromSlug(sourceSlug),
				Snippet: match.snippet,
			})
			break
		}
	}
	sort.Slice(backlinks, func(i, j int) bool {
		return backlinks[i].Slug < backlinks[j].Slug
	})
	s.storeCachedWikiBacklinks(repoFullName, targetSlug, headSHA, backlinks)
	return copyWikiBacklinks(backlinks), nil
}

// ensureWikiRepo lazily creates the sibling wiki repo on first write.
// Idempotent: gitstore.Init early-returns when the directory already
// exists. The wiki repo is created without a seed README so empty wiki
// state is a clean tree, not a placeholder commit.
func (s *Service) ensureWikiRepo(ctx context.Context, repoFullName string) error {
	if s.Git == nil {
		return errors.New("git store unavailable")
	}
	return s.Git.Init(ctx, wikiRepoFullName(repoFullName), wikiDefaultBranch, false)
}

// withWikiCatalogWriteLock serializes catalog writes and migration-based
// refreshes for one wiki repository. This keeps the read-path freshness hook
// from racing REST writes through the same catalog tables on SQLite-backed
// test runs and in production.
func (s *Service) withWikiCatalogWriteLock(ctx context.Context, repoFullName string, fn func() error) error {
	repo, err := s.LookupRepoIdentity(ctx, repoFullName)
	if err != nil {
		return err
	}
	mu := s.getWikiMigrationSyncMu(s.wikiRepoKey(ctx, repo))
	mu.Lock()
	defer mu.Unlock()

	full := wikiRepoFullName(repoFullName)
	return s.Git.WithRepoLock(ctx, full, fn)
}

// ListWikiPages returns one summary entry per markdown page at the wiki
// repo's HEAD. Returns an empty slice (not an error) if the wiki repo
// has not been created yet.
//
// Reads come from the wikicatalog. The catalog is the system of
// record after the runtime cutover, so a single indexed query
// replaces the legacy "git ls-tree + per-page git log" walk that
// produced 55 s sidebar latencies at 3000 pages.
func (s *Service) ListWikiPages(ctx context.Context, repoFullName string, opts ListWikiPagesOptions) ([]WikiPageSummary, error) {
	if s.WikiCatalog == nil {
		return nil, errors.New("wiki catalog unavailable")
	}
	if err := s.ensureWikiCatalogCurrent(ctx, repoFullName); err != nil {
		return nil, err
	}
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	if opts.Path != "" {
		if err := validateReadableWikiSlug(opts.Path); err != nil {
			return nil, err
		}
	}

	var pages []db.WikiPage
	if err := s.DBForCtx(ctx).
		Preload("LastAuthor").
		Where("repository_id = ? AND deleted_at IS NULL", rep.ID).
		Find(&pages).Error; err != nil {
		return nil, fmt.Errorf("list wiki pages: %w", err)
	}

	pageSlugs := make([]string, 0, len(pages))
	filtered := make([]db.WikiPage, 0, len(pages))
	for _, p := range pages {
		if !wikiSlugMatchesPathFilter(p.Slug, opts.Path, opts.Recursive) {
			continue
		}
		filtered = append(filtered, p)
		pageSlugs = append(pageSlugs, p.Slug)
	}

	labelFilters := WikiLabelFilters{Labels: opts.Labels, ExcludeLabels: opts.ExcludeLabels}
	var allowedSlugs map[string]struct{}
	if hasWikiLabelFilters(labelFilters) {
		var noResults bool
		allowedSlugs, noResults, err = s.wikiSlugsMatchingLabelFilters(ctx, rep.ID, pageSlugs, labelFilters)
		if err != nil {
			return nil, err
		}
		if noResults {
			return []WikiPageSummary{}, nil
		}
	}
	labelsBySlug, err := s.wikiLabelsForSlugs(ctx, rep.ID, pageSlugs)
	if err != nil {
		return nil, err
	}

	out := make([]WikiPageSummary, 0, len(filtered))
	for _, p := range filtered {
		if allowedSlugs != nil {
			if _, ok := allowedSlugs[p.Slug]; !ok {
				continue
			}
		}
		out = append(out, WikiPageSummary{
			Slug:       p.Slug,
			Title:      titleFromSlug(p.Slug),
			SHA:        p.HeadBlobSHA,
			Labels:     labelsBySlug[p.Slug],
			UpdatedAt:  p.UpdatedAt,
			LastAuthor: p.LastAuthor,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

func wikiSlugMatchesPathFilter(slug, prefix string, recursive bool) bool {
	if prefix == "" {
		if recursive {
			return true
		}
		return !strings.Contains(slug, "/")
	}
	if slug == prefix {
		return false
	}
	prefix = prefix + "/"
	if !strings.HasPrefix(slug, prefix) {
		return false
	}
	if recursive {
		return true
	}
	rest := strings.TrimPrefix(slug, prefix)
	return rest != "" && !strings.Contains(rest, "/")
}

func (s *Service) lookupUsersByEmailCI(ctx context.Context, emails []string) map[string]db.User {
	if len(emails) == 0 {
		return nil
	}

	var rows []db.User
	if err := s.DBForCtx(ctx).Where("LOWER(email) IN ?", emails).Find(&rows).Error; err != nil {
		slog.WarnContext(ctx, "lookupUsersByEmailCI failed", "error", err, "emails", len(emails))
		return nil
	}

	grouped := make(map[string][]db.User, len(rows))
	for _, user := range rows {
		email := strings.ToLower(strings.TrimSpace(user.Email))
		if email == "" {
			continue
		}
		grouped[email] = append(grouped[email], user)
	}

	out := make(map[string]db.User, len(grouped))
	for email, users := range grouped {
		if len(users) == 1 {
			out[email] = users[0]
		}
	}
	return out
}

// GetWikiPage reads a single page from the wiki repo. Returns
// ErrNotFound if the wiki repo doesn't exist or the slug isn't present.
func (s *Service) GetWikiPage(ctx context.Context, repoFullName, slug string) (WikiPage, error) {
	return s.GetWikiPageAtRef(ctx, repoFullName, slug, "")
}

// GetWikiPageAtRef reads a single page from the wiki repo at the requested ref.
// Returns ErrNotFound if the wiki repo doesn't exist or the slug isn't present
// at that revision, or ErrValidation if the supplied ref is malformed.
//
// Reads come from the catalog. Without a ref, the page's head revision
// is returned via one indexed point lookup. With a ref, the matching
// revision in wiki_page_revisions is loaded and projected — replacing
// the legacy per-page git log + ReadFileWithSHAAtRef walk.
func (s *Service) GetWikiPageAtRef(ctx context.Context, repoFullName, slug, ref string) (WikiPage, error) {
	if err := validateReadableWikiSlug(slug); err != nil {
		return WikiPage{}, ErrNotFound
	}
	if s.WikiCatalog == nil {
		return WikiPage{}, errors.New("wiki catalog unavailable")
	}
	if err := s.ensureWikiCatalogCurrent(ctx, repoFullName); err != nil {
		return WikiPage{}, err
	}
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return WikiPage{}, err
	}
	ref = strings.TrimSpace(ref)
	if ref != "" && !wikiCommitSHARE.MatchString(ref) {
		return WikiPage{}, fmt.Errorf("%w: invalid ref", ErrValidation)
	}

	if ref == "" {
		page, err := s.loadLiveWikiPage(ctx, rep.ID, slug)
		if err != nil {
			return WikiPage{}, err
		}
		body, err := s.wikiPageBody(ctx, page)
		if err != nil {
			return WikiPage{}, err
		}
		labelsBySlug, err := s.wikiLabelsForSlugs(ctx, rep.ID, []string{slug})
		if err != nil {
			return WikiPage{}, err
		}
		return s.wikiPageFromCatalog(page, body, labelsBySlug[slug]), nil
	}

	// Ref-pinned read: locate the revision in wiki_page_revisions
	// keyed by the page row's slug_ci_v1 (which the catalog updates on
	// every rename) plus the commit SHA pin.
	return s.getWikiPageAtRevision(ctx, rep.ID, slug, ref)
}

// getWikiPageAtRevision returns a single page projected from the
// wiki_page_revisions row whose commit SHA matches ref and whose
// page_id maps to the requested slug. Returns ErrNotFound when the
// slug was not present at that revision.
func (s *Service) getWikiPageAtRevision(ctx context.Context, repoID uint, slug, ref string) (WikiPage, error) {
	// CanonicalV1 validates the slug grammar; the query below joins on
	// the raw slug_at_rev string, so the canonical form itself isn't
	// needed here, only the validation it performs.
	if _, err := wikicatalog.CanonicalV1(slug); err != nil {
		return WikiPage{}, ErrNotFound
	}
	// Find any revision for this slug at the requested commit. The
	// slug_at_rev column records the on-disk slug as of that revision
	// so a revision before a rename still resolves by its historical
	// slug; combined with the per-repo changeset filter the lookup is
	// fully indexed.
	var rev db.WikiPageRevision
	err := s.DBForCtx(ctx).
		Joins("JOIN wiki_changesets ON wiki_changesets.changeset_id = wiki_page_revisions.changeset_id").
		Where("wiki_changesets.repository_id = ? AND LOWER(wiki_page_revisions.commit_sha) = LOWER(?) AND wiki_page_revisions.slug_at_rev = ?",
			repoID, ref, slug).
		Order("wiki_page_revisions.revision_id DESC").
		Take(&rev).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return WikiPage{}, ErrNotFound
	}
	if err != nil {
		return WikiPage{}, err
	}
	if rev.Op == "delete" {
		return WikiPage{}, ErrNotFound
	}
	body, err := s.wikiRevisionBody(ctx, rev)
	if err != nil {
		return WikiPage{}, err
	}
	var page db.WikiPage
	if err := s.DBForCtx(ctx).Unscoped().Preload("LastAuthor").
		Where("page_id = ?", rev.PageID).Take(&page).Error; err != nil {
		return WikiPage{}, err
	}
	page.LastAuthor = nil
	var changeset db.WikiChangeset
	if err := s.DBForCtx(ctx).Preload("Author").
		First(&changeset, "changeset_id = ?", rev.ChangesetID).Error; err == nil {
		// Prefer the changeset's author for the ref-pinned view since
		// LastAuthor on the page row reflects HEAD, not this revision.
		if changeset.Author != nil {
			page.LastAuthor = changeset.Author
		}
		page.UpdatedAt = changeset.CommittedAt
	}
	labelsBySlug, err := s.wikiLabelsForSlugs(ctx, repoID, []string{slug})
	if err != nil {
		return WikiPage{}, err
	}
	out := s.wikiPageFromCatalog(page, body, labelsBySlug[slug])
	out.Slug = slug
	out.Title = titleFromSlug(slug)
	// At-ref reads project the revision's own blob SHA, not the
	// page row's current HEAD SHA — otherwise the SHA returned
	// would always equal HEAD regardless of the ref pin.
	out.SHA = rev.BlobSHA
	return out, nil
}

// wikiRevisionBody reads a revision's body, preferring the inline copy
// embedded on the revision row and falling back to the catalog blob
// store. Mirrors wikiPageBody but for WikiPageRevision rows.
func (s *Service) wikiRevisionBody(ctx context.Context, rev db.WikiPageRevision) ([]byte, error) {
	if len(rev.BodyInline) > 0 {
		return rev.BodyInline, nil
	}
	if rev.BlobSHA == "" {
		return nil, nil
	}
	if s.WikiBlob == nil {
		return nil, errors.New("wiki blob store unavailable")
	}
	return s.WikiBlob.Get(ctx, rev.BlobSHA)
}

// ListWikiPageHistory returns newest-first revisions for one wiki page.
func (s *Service) ListWikiPageHistory(ctx context.Context, repoFullName, slug string) ([]WikiPageHistoryEntry, error) {
	history, _, err := s.ListWikiPageHistoryPage(ctx, repoFullName, slug, 1, 0)
	return history, err
}

// ListWikiPageHistoryPage returns one page of newest-first revisions for one wiki page
// plus the total number of matching revisions.
//
// Sourced from wiki_page_revisions joined with wiki_changesets so the
// historical author, committer, and timestamp come from the catalog's
// per-revision audit record rather than a per-page git log walk.
func (s *Service) ListWikiPageHistoryPage(ctx context.Context, repoFullName, slug string, page, perPage int) ([]WikiPageHistoryEntry, int, error) {
	if err := validateWikiSlug(slug); err != nil {
		return nil, 0, err
	}
	if s.WikiCatalog == nil {
		return nil, 0, errors.New("wiki catalog unavailable")
	}
	if err := s.ensureWikiCatalogCurrent(ctx, repoFullName); err != nil {
		return nil, 0, err
	}
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return nil, 0, err
	}
	// Locate the page id, including soft-deleted pages — history is
	// kept around even after a delete so the catalog still has a
	// truthful revision chain to project.
	slugCI, err := wikicatalog.CanonicalV1(slug)
	if err != nil {
		return nil, 0, ErrNotFound
	}
	var pageRow db.WikiPage
	if err := s.DBForCtx(ctx).Unscoped().
		Where("repository_id = ? AND slug_ci_v1 = ?", rep.ID, slugCI).
		Take(&pageRow).Error; err != nil {
		return nil, 0, ErrNotFound
	}

	var total int64
	if err := s.DBForCtx(ctx).Model(&db.WikiPageRevision{}).
		Where("page_id = ? AND superseded_by_changeset_id IS NULL", pageRow.PageID).
		Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, ErrNotFound
	}

	if page < 1 {
		page = 1
	}
	if perPage <= 0 {
		perPage = 30
	}
	offset := (page - 1) * perPage

	type revWithCS struct {
		db.WikiPageRevision
		Message     string
		CommittedAt time.Time
		CSAuthorID  *uint
	}
	var rows []revWithCS
	if err := s.DBForCtx(ctx).
		Table("wiki_page_revisions").
		Select(`wiki_page_revisions.*,
			wiki_changesets.message AS message,
			wiki_changesets.committed_at AS committed_at,
			wiki_changesets.author_id AS cs_author_id`).
		Joins("JOIN wiki_changesets ON wiki_changesets.changeset_id = wiki_page_revisions.changeset_id").
		Where("wiki_page_revisions.page_id = ? AND wiki_page_revisions.superseded_by_changeset_id IS NULL", pageRow.PageID).
		Order("wiki_page_revisions.revision_id DESC").
		Offset(offset).Limit(perPage).
		Scan(&rows).Error; err != nil {
		return nil, 0, err
	}

	// Batch-load the authors for the revisions on this page.
	authorIDs := make(map[uint]struct{}, len(rows))
	for _, r := range rows {
		if r.AuthorID != nil {
			authorIDs[*r.AuthorID] = struct{}{}
		}
		if r.CSAuthorID != nil {
			authorIDs[*r.CSAuthorID] = struct{}{}
		}
	}
	users := make(map[uint]*db.User, len(authorIDs))
	if len(authorIDs) > 0 {
		ids := make([]uint, 0, len(authorIDs))
		for id := range authorIDs {
			ids = append(ids, id)
		}
		var found []db.User
		if err := s.DBForCtx(ctx).Where("id IN ?", ids).Find(&found).Error; err != nil {
			return nil, 0, err
		}
		for i := range found {
			users[found[i].ID] = &found[i]
		}
	}

	out := make([]WikiPageHistoryEntry, 0, len(rows))
	for _, r := range rows {
		entry := WikiPageHistoryEntry{
			SHA:      r.CommitSHA,
			Message:  r.Message,
			Date:     r.CommittedAt,
			BodySize: r.BodySize,
		}
		if r.AuthorID != nil {
			entry.Author = users[*r.AuthorID]
		}
		if r.CSAuthorID != nil {
			entry.Committer = users[*r.CSAuthorID]
		}
		out = append(out, entry)
	}
	return out, int(total), nil
}

// WikiConflictError reports an optimistic-concurrency failure together with
// the current server-side page representation. CurrentPage is nil when the
// page no longer exists.
type WikiConflictError struct {
	ExpectedSHA string
	CurrentPage *WikiPage
}

func (e *WikiConflictError) Error() string {
	if e == nil {
		return ErrConflict.Error()
	}
	if e.CurrentPage == nil {
		return fmt.Sprintf("%v: wiki page changed since last read (expected sha %q, current page deleted)", ErrConflict, e.ExpectedSHA)
	}
	return fmt.Sprintf("%v: wiki page changed since last read (expected sha %q, current sha %q)", ErrConflict, e.ExpectedSHA, e.CurrentPage.SHA)
}

func (e *WikiConflictError) Unwrap() error { return ErrConflict }

// ListWikiBacklinks returns all pages in the current wiki HEAD that link to
// the requested slug.
func (s *Service) ListWikiBacklinks(ctx context.Context, repoFullName, slug string) ([]WikiBacklink, error) {
	if err := validateReadableWikiSlug(slug); err != nil {
		return nil, ErrNotFound
	}
	if s.Git == nil {
		return nil, errors.New("git store unavailable")
	}
	full := wikiRepoFullName(repoFullName)
	if !s.Git.Exists(ctx, full) {
		return nil, ErrNotFound
	}
	backlinks, err := s.loadWikiBacklinksForSlug(ctx, repoFullName, slug)
	if err != nil {
		return nil, err
	}
	return backlinks, nil
}

// PutWikiPage creates or updates a page. Returns the current page view,
// including the page blob SHA used by optimistic-concurrency clients, so
// callers can render without a separate read.
//
// Writes flow through the wikicatalog ApplyChangeSet primitive: the
// catalog is the system of record. The post-commit hook materializes
// the change onto the wiki bare git repo so clone/pull continue to
// work, and feeds the search index. See WikiCatalogPostCommit.
func (s *Service) PutWikiPage(ctx context.Context, repoFullName, slug, body, message, expectedSHA string) (WikiPage, error) {
	if err := validateWikiSlug(slug); err != nil {
		return WikiPage{}, err
	}
	if s.WikiCatalog == nil {
		return WikiPage{}, errors.New("wiki catalog unavailable")
	}
	if err := s.ensureWikiRepo(ctx, repoFullName); err != nil {
		return WikiPage{}, err
	}
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return WikiPage{}, err
	}
	if message == "" {
		message = "Update " + slug
	}

	change := wikicatalog.Change{
		Op:      wikicatalog.OpUpsert,
		Slug:    slug,
		Body:    []byte(body),
		IfMatch: expectedSHA,
	}
	var result wikicatalog.ChangeSetResult
	err = s.withWikiCatalogWriteLock(ctx, repoFullName, func() error {
		var applyErr error
		result, applyErr = s.WikiCatalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
			RepositoryID: rep.ID,
			AuthorID:     s.resolveWikiAuthor(ctx),
			Source:       wikicatalog.SourceREST,
			Message:      message,
			Changes:      []wikicatalog.Change{change},
		})
		return applyErr
	})
	if err != nil {
		return WikiPage{}, s.translateCatalogError(ctx, rep.ID, repoFullName, err, false)
	}
	written := result.Changes[0]
	page, err := s.loadLiveWikiPage(ctx, rep.ID, written.Slug)
	if err != nil {
		return WikiPage{}, err
	}
	bodyBytes, err := s.wikiPageBody(ctx, page)
	if err != nil {
		return WikiPage{}, err
	}
	labels, err := s.wikiLabelsForSlugs(ctx, rep.ID, []string{written.Slug})
	if err != nil {
		return WikiPage{}, err
	}
	return s.wikiPageFromCatalog(page, bodyBytes, labels[written.Slug]), nil
}

// DeleteWikiPage removes a page. Returns ErrNotFound when the wiki repo
// or the slug doesn't exist (matches GitHub's REST contract).
//
// Routed through the catalog: OpDelete on ApplyChangeSet. The catalog
// handles OCC retry internally on wiki_repo_heads, and the post-commit
// materialize hook deletes the path in the wiki git repo. Search and
// backlink cache are driven by the same hook.
func (s *Service) DeleteWikiPage(ctx context.Context, repoFullName, slug, message string) error {
	if err := validateWikiSlug(slug); err != nil {
		return err
	}
	if s.WikiCatalog == nil {
		return errors.New("wiki catalog unavailable")
	}
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return err
	}
	if message == "" {
		message = "Delete " + slug
	}
	err = s.withWikiCatalogWriteLock(ctx, repoFullName, func() error {
		_, applyErr := s.WikiCatalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
			RepositoryID: rep.ID,
			AuthorID:     s.resolveWikiAuthor(ctx),
			Source:       wikicatalog.SourceREST,
			Message:      message,
			Changes:      []wikicatalog.Change{{Op: wikicatalog.OpDelete, Slug: slug}},
		})
		return applyErr
	})
	if err != nil {
		return s.translateCatalogError(ctx, rep.ID, repoFullName, err, false)
	}
	if err := s.deleteWikiPageLabels(ctx, rep.ID, slug); err != nil {
		return err
	}
	s.invalidateWikiBacklinks(repoFullName)
	return nil
}

// MoveWikiPage renames a page and rewrites inbound references to it
// in one atomic catalog changeset. The materialize hook lands the
// equivalent git commit so clone/pull stay coherent.
func (s *Service) MoveWikiPage(ctx context.Context, repoFullName, slug, newSlug, ifMatch, message string) (WikiMoveResult, error) {
	if err := validateWikiSlug(slug); err != nil {
		return WikiMoveResult{}, err
	}
	if err := validateWikiSlug(newSlug); err != nil {
		return WikiMoveResult{}, err
	}
	if ifMatch == "" {
		return WikiMoveResult{}, fmt.Errorf("%w: if_match is required", ErrValidation)
	}
	if s.WikiCatalog == nil {
		return WikiMoveResult{}, errors.New("wiki catalog unavailable")
	}
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return WikiMoveResult{}, err
	}

	// Plan the inbound rewrites against the catalog so we can pack the
	// rename and the body updates into a single atomic changeset. The
	// catalog enforces the IfMatch, destination-occupied, and
	// prefix-collision checks; we only have to compute the rewrites.
	rewrittenBodies, skipped, err := s.planWikiMoveRewrites(ctx, rep.ID, slug, newSlug)
	if err != nil {
		return WikiMoveResult{}, err
	}

	commitMessage := message
	if commitMessage == "" {
		commitMessage = "Move " + slug + " to " + newSlug
		if len(rewrittenBodies) > 0 {
			suffix := "pages"
			if len(rewrittenBodies) == 1 {
				suffix = "page"
			}
			commitMessage += fmt.Sprintf(" (rewrote refs in %d %s)", len(rewrittenBodies), suffix)
		}
	}

	changes := make([]wikicatalog.Change, 0, len(rewrittenBodies)+1)
	changes = append(changes, wikicatalog.Change{
		Op:      wikicatalog.OpRename,
		Slug:    slug,
		NewSlug: newSlug,
		IfMatch: ifMatch,
	})
	rewriteSlugs := make([]string, 0, len(rewrittenBodies))
	for s := range rewrittenBodies {
		rewriteSlugs = append(rewriteSlugs, s)
	}
	sort.Strings(rewriteSlugs)
	for _, rs := range rewriteSlugs {
		changes = append(changes, wikicatalog.Change{
			Op:   wikicatalog.OpUpsert,
			Slug: rs,
			Body: []byte(rewrittenBodies[rs]),
		})
	}

	err = s.withWikiCatalogWriteLock(ctx, repoFullName, func() error {
		_, applyErr := s.WikiCatalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
			RepositoryID: rep.ID,
			AuthorID:     s.resolveWikiAuthor(ctx),
			Source:       wikicatalog.SourceREST,
			Message:      commitMessage,
			Changes:      changes,
		})
		return applyErr
	})
	if err != nil {
		return WikiMoveResult{}, s.translateCatalogError(ctx, rep.ID, repoFullName, err, true)
	}
	if err := s.moveWikiPageLabels(ctx, rep.ID, map[string]string{slug: newSlug}); err != nil {
		return WikiMoveResult{}, err
	}
	s.queueWikiSearchRefreshBySlugs(ctx, repoFullName, append([]string{newSlug}, rewriteSlugs...))

	s.invalidateWikiBacklinks(repoFullName)

	movedRow, err := s.loadLiveWikiPage(ctx, rep.ID, newSlug)
	if err != nil {
		return WikiMoveResult{}, err
	}
	movedBody, err := s.wikiPageBody(ctx, movedRow)
	if err != nil {
		return WikiMoveResult{}, err
	}
	labelsBySlug, err := s.wikiLabelsForSlugs(ctx, rep.ID, append([]string{newSlug}, rewriteSlugs...))
	if err != nil {
		return WikiMoveResult{}, err
	}
	moved := s.wikiPageFromCatalog(movedRow, movedBody, labelsBySlug[newSlug])

	rewrites, err := s.wikiSummariesFromCatalog(ctx, rep.ID, rewriteSlugs, labelsBySlug)
	if err != nil {
		return WikiMoveResult{}, err
	}
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].Slug < skipped[j].Slug })
	return WikiMoveResult{
		Moved:    moved,
		Rewrites: rewrites,
		Skipped:  skipped,
	}, nil
}

// planWikiMoveRewrites finds every live page that links to oldSlug and
// computes the rewritten body for each. Failed rewrites are reported
// via the skipped slice (same shape the legacy git-walking code
// produced). The page being renamed is excluded from rewriting — its
// content moves through OpRename unchanged, and a self-reference
// rewrite would collide with OpRename's target slug.
func (s *Service) planWikiMoveRewrites(ctx context.Context, repoID uint, oldSlug, newSlug string) (map[string]string, []WikiRewriteSkip, error) {
	oldCI, err := wikicatalog.CanonicalV1(oldSlug)
	if err != nil {
		return nil, nil, err
	}
	var linkerIDs []uint64
	if err := s.DBForCtx(ctx).Model(&db.WikiPageLink{}).
		Where("repository_id = ? AND dst_slug_ci = ?", repoID, oldCI).
		Distinct("src_page_id").
		Pluck("src_page_id", &linkerIDs).Error; err != nil {
		return nil, nil, fmt.Errorf("look up inbound linkers: %w", err)
	}
	if len(linkerIDs) == 0 {
		return map[string]string{}, []WikiRewriteSkip{}, nil
	}
	var linkers []db.WikiPage
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND page_id IN ? AND deleted_at IS NULL", repoID, linkerIDs).
		Find(&linkers).Error; err != nil {
		return nil, nil, fmt.Errorf("load linker pages: %w", err)
	}
	rewritten := make(map[string]string, len(linkers))
	skipped := make([]WikiRewriteSkip, 0)
	for _, p := range linkers {
		if p.Slug == oldSlug {
			continue
		}
		body, err := s.wikiPageBody(ctx, p)
		if err != nil {
			return nil, nil, fmt.Errorf("read linker body for %q: %w", p.Slug, err)
		}
		out, changed, err := rewriteWikiReferences(string(body), oldSlug, newSlug)
		if err != nil {
			slog.WarnContext(ctx, "wiki move skipped inbound rewrite", "slug", p.Slug, "reason", err.Error())
			skipped = append(skipped, WikiRewriteSkip{Slug: p.Slug, Reason: err.Error()})
			continue
		}
		if changed {
			rewritten[p.Slug] = out
		}
	}
	return rewritten, skipped, nil
}

// wikiSummariesFromCatalog builds WikiPageSummary entries for a set
// of slugs by reading their current catalog rows. Replaces the legacy
// wikiSummariesForBodies that walked git for per-page metadata.
func (s *Service) wikiSummariesFromCatalog(ctx context.Context, repoID uint, slugs []string, labelsBySlug map[string][]db.Label) ([]WikiPageSummary, error) {
	if len(slugs) == 0 {
		return []WikiPageSummary{}, nil
	}
	cis := make([]string, 0, len(slugs))
	for _, sl := range slugs {
		ci, err := wikicatalog.CanonicalV1(sl)
		if err != nil {
			continue
		}
		cis = append(cis, ci)
	}
	var pages []db.WikiPage
	if err := s.DBForCtx(ctx).
		Preload("LastAuthor").
		Where("repository_id = ? AND slug_ci_v1 IN ? AND deleted_at IS NULL", repoID, cis).
		Find(&pages).Error; err != nil {
		return nil, err
	}
	out := make([]WikiPageSummary, 0, len(pages))
	for _, p := range pages {
		out = append(out, WikiPageSummary{
			Slug:       p.Slug,
			Title:      wikicatalog.TitleFromSlug(p.Slug),
			SHA:        p.HeadBlobSHA,
			UpdatedAt:  p.UpdatedAt,
			LastAuthor: p.LastAuthor,
			Labels:     labelsBySlug[p.Slug],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

// MoveWikiPagePrefix atomically moves every wiki page whose slug equals from or
// starts with from/.
func (s *Service) MoveWikiPagePrefix(ctx context.Context, repoFullName, from, to string, ifMatch map[string]string, message string) (WikiBulkMoveResult, error) {
	if err := validateWikiSlug(from); err != nil {
		return WikiBulkMoveResult{}, err
	}
	if err := validateWikiSlug(to); err != nil {
		return WikiBulkMoveResult{}, err
	}
	if from == to {
		return WikiBulkMoveResult{}, fmt.Errorf("%w: from and to must differ", ErrValidation)
	}
	if s.Git == nil {
		return WikiBulkMoveResult{}, errors.New("git store unavailable")
	}
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return WikiBulkMoveResult{}, err
	}
	if message == "" {
		message = "Move wiki prefix " + from + " to " + to
	}

	// Enumerate sources from the catalog (indexed prefix scan) instead
	// of walking the git tree.
	sources, sourcePages, err := s.findWikiBulkMoveSources(ctx, rep.ID, from)
	if err != nil {
		return WikiBulkMoveResult{}, err
	}
	if len(sources) == 0 {
		return WikiBulkMoveResult{}, &WikiBulkMoveNotFoundError{From: from}
	}

	missing := make([]string, 0)
	for _, slug := range sources {
		if strings.TrimSpace(ifMatch[slug]) == "" {
			missing = append(missing, slug)
		}
	}
	if len(missing) > 0 {
		return WikiBulkMoveResult{}, &WikiBulkMoveValidationError{From: from, MissingSlugs: missing}
	}

	// Build the rename plan + per-source destination map. The catalog
	// enforces destination-occupied and prefix-collision at apply
	// time, but we still need to detect them up front because the
	// legacy REST contract returns them as a single batched
	// WikiBulkMoveConflictError instead of bailing on the first.
	sourceSet := make(map[string]struct{}, len(sources))
	for _, slug := range sources {
		sourceSet[slug] = struct{}{}
	}
	unaffectedPages, err := s.loadUnaffectedWikiPages(ctx, rep.ID, sourceSet)
	if err != nil {
		return WikiBulkMoveResult{}, err
	}
	unaffectedSlugs := make([]string, 0, len(unaffectedPages))
	for slug := range unaffectedPages {
		unaffectedSlugs = append(unaffectedSlugs, slug)
	}

	moved := make([]WikiBulkMoveEntry, 0, len(sources))
	remaps := make([]WikiBulkMoveEntry, 0, len(sources))
	movedBodies := make(map[string]string, len(sources))
	movedTargets := make(map[string]struct{}, len(sources))
	conflicts := make([]WikiBulkMoveConflict, 0)
	for _, slug := range sources {
		destSlug := remapWikiMoveSlug(slug, from, to)
		if err := validateWikiSlug(destSlug); err != nil {
			return WikiBulkMoveResult{}, err
		}
		page := sourcePages[slug]
		expectedSHA := strings.TrimSpace(ifMatch[slug])
		if !strings.EqualFold(page.HeadBlobSHA, expectedSHA) {
			conflicts = append(conflicts, WikiBulkMoveConflict{
				From:       slug,
				To:         destSlug,
				Code:       wikiMoveCodeStale,
				Message:    fmt.Sprintf("%s: source page %q is stale", wikiMoveCodeStale, slug),
				CurrentSHA: page.HeadBlobSHA,
			})
			continue
		}
		if _, taken := unaffectedPages[destSlug]; taken {
			conflicts = append(conflicts, WikiBulkMoveConflict{
				From:    slug,
				To:      destSlug,
				Code:    wikiMoveCodeDestTaken,
				Message: fmt.Sprintf("%s: destination page %q already exists", wikiMoveCodeDestTaken, destSlug),
			})
			continue
		}
		if collision := findWikiPrefixCollision(destSlug, unaffectedSlugs, nil); collision != "" {
			conflicts = append(conflicts, WikiBulkMoveConflict{
				From:          slug,
				To:            destSlug,
				Code:          wikiMoveCodePrefix,
				Message:       fmt.Sprintf("%s: destination page %q conflicts with existing page %q", wikiMoveCodePrefix, destSlug, collision),
				ConflictsWith: collision,
			})
			continue
		}
		body, err := s.wikiPageBody(ctx, page)
		if err != nil {
			return WikiBulkMoveResult{}, err
		}
		moved = append(moved, WikiBulkMoveEntry{From: slug, To: destSlug, SHA: page.HeadBlobSHA})
		remaps = append(remaps, WikiBulkMoveEntry{From: slug, To: destSlug, SHA: page.HeadBlobSHA})
		movedBodies[destSlug] = string(body)
		movedTargets[destSlug] = struct{}{}
	}
	if len(conflicts) > 0 {
		return WikiBulkMoveResult{}, &WikiBulkMoveConflictError{Conflicts: conflicts}
	}

	// Rewrite inbound references in every body (unaffected pages plus
	// the moved pages themselves — a moved page may reference another
	// moved page and its body needs the new slug). Pages whose
	// rewriter trips are recorded as skipped, matching the legacy
	// behaviour for malformed content.
	skipped := make([]WikiRewriteSkip, 0)
	rewriteAllBodies := func(slug, body string) (string, bool, bool) {
		// returns (newBody, changed, shouldSkip)
		rewritten := body
		changed := false
		for _, remap := range remaps {
			next, bodyChanged, err := rewriteWikiReferences(rewritten, remap.From, remap.To)
			if err != nil {
				slog.WarnContext(ctx, "wiki bulk move skipped inbound rewrite", "slug", slug, "reason", err.Error())
				skipped = append(skipped, WikiRewriteSkip{Slug: slug, Reason: err.Error()})
				return body, false, true
			}
			if bodyChanged {
				rewritten = next
				changed = true
			}
		}
		return rewritten, changed, false
	}
	rewrittenBodies := map[string]string{}
	for slug, page := range unaffectedPages {
		body, err := s.wikiPageBody(ctx, page)
		if err != nil {
			return WikiBulkMoveResult{}, err
		}
		newBody, changed, skip := rewriteAllBodies(slug, string(body))
		if skip || !changed {
			continue
		}
		rewrittenBodies[slug] = newBody
	}
	// Apply the same rewrite pass to the bodies that move (keyed by
	// the destination slug). These end up on OpUpsert at the new
	// slug, not on OpRename, so the new revision can carry the
	// rewritten content.
	movedRewrittenBodies := make(map[string]string, len(moved))
	for _, mv := range moved {
		orig := movedBodies[mv.To]
		newBody, _, skip := rewriteAllBodies(mv.From, orig)
		if skip {
			movedRewrittenBodies[mv.To] = orig
			continue
		}
		movedRewrittenBodies[mv.To] = newBody
	}

	commitMessage := message
	if len(rewrittenBodies) > 0 && !strings.Contains(commitMessage, "rewrote refs in") {
		suffix := "pages"
		if len(rewrittenBodies) == 1 {
			suffix = "page"
		}
		commitMessage += fmt.Sprintf(" (rewrote refs in %d %s)", len(rewrittenBodies), suffix)
	}

	// Build the changeset: one OpRename per moved page, carrying the
	// (possibly rewritten) body so the page identity stays continuous
	// across the move. One OpUpsert per rewritten unaffected linker.
	changes := make([]wikicatalog.Change, 0, len(moved)+len(rewrittenBodies))
	for _, mv := range moved {
		changes = append(changes, wikicatalog.Change{
			Op:      wikicatalog.OpRename,
			Slug:    mv.From,
			NewSlug: mv.To,
			Body:    []byte(movedRewrittenBodies[mv.To]),
			IfMatch: mv.SHA,
		})
	}
	rewriteSlugs := make([]string, 0, len(rewrittenBodies))
	for slug := range rewrittenBodies {
		rewriteSlugs = append(rewriteSlugs, slug)
	}
	sort.Strings(rewriteSlugs)
	for _, slug := range rewriteSlugs {
		changes = append(changes, wikicatalog.Change{
			Op:   wikicatalog.OpUpsert,
			Slug: slug,
			Body: []byte(rewrittenBodies[slug]),
		})
	}

	var applyResult wikicatalog.ChangeSetResult
	err = s.withWikiCatalogWriteLock(ctx, repoFullName, func() error {
		var applyErr error
		applyResult, applyErr = s.WikiCatalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
			RepositoryID: rep.ID,
			AuthorID:     s.resolveWikiAuthor(ctx),
			Source:       wikicatalog.SourceREST,
			Message:      commitMessage,
			Changes:      changes,
		})
		return applyErr
	})
	if err != nil {
		return WikiBulkMoveResult{}, s.translateCatalogError(ctx, rep.ID, repoFullName, err, true)
	}
	labelRemaps := make(map[string]string, len(moved))
	for _, mv := range moved {
		labelRemaps[mv.From] = mv.To
	}
	if err := s.moveWikiPageLabels(ctx, rep.ID, labelRemaps); err != nil {
		return WikiBulkMoveResult{}, err
	}

	s.invalidateWikiBacklinks(repoFullName)

	labelLookupSlugs := make([]string, 0, len(moved)+len(rewriteSlugs))
	for _, mv := range moved {
		labelLookupSlugs = append(labelLookupSlugs, mv.To)
	}
	labelLookupSlugs = append(labelLookupSlugs, rewriteSlugs...)
	s.queueWikiSearchRefreshBySlugs(ctx, repoFullName, labelLookupSlugs)
	labelsBySlug, err := s.wikiLabelsForSlugs(ctx, rep.ID, labelLookupSlugs)
	if err != nil {
		return WikiBulkMoveResult{}, err
	}

	rewrites, err := s.wikiSummariesFromCatalog(ctx, rep.ID, rewriteSlugs, labelsBySlug)
	if err != nil {
		return WikiBulkMoveResult{}, err
	}
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].Slug < skipped[j].Slug })

	return WikiBulkMoveResult{
		Moved:    moved,
		Commit:   applyResult.CommitSHA,
		Rewrites: rewrites,
		Skipped:  skipped,
	}, nil
}

// findWikiBulkMoveSources returns every live wiki page whose slug
// equals from or starts with from/. Bypasses the slow git tree walk
// by querying the catalog's slug_ci_v1 prefix index.
func (s *Service) findWikiBulkMoveSources(ctx context.Context, repoID uint, from string) ([]string, map[string]db.WikiPage, error) {
	fromCI, err := wikicatalog.CanonicalV1(from)
	if err != nil {
		return nil, nil, err
	}
	var pages []db.WikiPage
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND deleted_at IS NULL AND (slug_ci_v1 = ? OR slug_ci_v1 LIKE ?)",
			repoID, fromCI, fromCI+"/%").
		Find(&pages).Error; err != nil {
		return nil, nil, err
	}
	slugs := make([]string, 0, len(pages))
	bySlug := make(map[string]db.WikiPage, len(pages))
	for _, p := range pages {
		if p.Slug != from && !strings.HasPrefix(p.Slug, from+"/") {
			// The slug_ci_v1 prefix match can over-include when the
			// canonicalisation folds case or unusual characters into
			// the same key; filter on the raw slug to match legacy
			// REST semantics exactly.
			continue
		}
		slugs = append(slugs, p.Slug)
		bySlug[p.Slug] = p
	}
	sort.Strings(slugs)
	return slugs, bySlug, nil
}

// loadUnaffectedWikiPages returns every live wiki page in the repo
// whose slug is NOT in the provided source set, keyed by raw slug.
// Used by MoveWikiPagePrefix to find inbound rewrite candidates and
// to detect destination collisions.
func (s *Service) loadUnaffectedWikiPages(ctx context.Context, repoID uint, exclude map[string]struct{}) (map[string]db.WikiPage, error) {
	var pages []db.WikiPage
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND deleted_at IS NULL", repoID).
		Find(&pages).Error; err != nil {
		return nil, err
	}
	out := make(map[string]db.WikiPage, len(pages))
	for _, p := range pages {
		if _, skip := exclude[p.Slug]; skip {
			continue
		}
		out[p.Slug] = p
	}
	return out, nil
}

func remapWikiMoveSlug(slug, from, to string) string {
	if slug == from {
		return to
	}
	return to + strings.TrimPrefix(slug, from)
}

func findWikiPrefixCollision(slug string, existing []string, ignore map[string]struct{}) string {
	for _, candidate := range existing {
		if candidate == "" || candidate == slug {
			continue
		}
		if ignore != nil {
			if _, ok := ignore[candidate]; ok {
				continue
			}
		}
		if strings.HasPrefix(candidate, slug+"/") || strings.HasPrefix(slug, candidate+"/") {
			return candidate
		}
	}
	return ""
}

func sliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
