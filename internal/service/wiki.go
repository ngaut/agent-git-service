// Package service — wiki page CRUD backed by a sibling bare git repo.
package service

import (
	"context"
	"errors"
	"fmt"
	"gh-server/internal/db"
	"gh-server/internal/gitstore"
	"gh-server/internal/wikicatalog"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
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

// ListWikiPages returns one summary entry per markdown page at the wiki
// repo's HEAD. Returns an empty slice (not an error) if the wiki repo
// has not been created yet.
func (s *Service) ListWikiPages(ctx context.Context, repoFullName string, opts ListWikiPagesOptions) ([]WikiPageSummary, error) {
	if s.Git == nil {
		return nil, errors.New("git store unavailable")
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

	full := wikiRepoFullName(repoFullName)
	if !s.Git.Exists(ctx, full) {
		return []WikiPageSummary{}, nil
	}
	snapshot, err := s.Git.ResolveContentCommit(ctx, full, "")
	if err != nil {
		return []WikiPageSummary{}, nil
	}
	paths, err := s.Git.ListTreeFilesAtRef(ctx, full, snapshot)
	if err != nil {
		return []WikiPageSummary{}, nil
	}
	pagePaths := make([]string, 0, len(paths))
	for _, p := range paths {
		slug := wikiPathToSlug(p)
		if slug != "" && wikiSlugMatchesPathFilter(slug, opts.Path, opts.Recursive) {
			pagePaths = append(pagePaths, p)
		}
	}
	pageSlugs := make([]string, 0, len(pagePaths))
	for _, p := range pagePaths {
		if slug := wikiPathToSlug(p); slug != "" {
			pageSlugs = append(pageSlugs, slug)
		}
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

	metadata, err := s.wikiPageMetadataAtRef(ctx, full, snapshot, pagePaths)
	if err != nil {
		return nil, err
	}
	blobSHAs, err := s.wikiBlobSHAsAtRef(ctx, full, snapshot, pagePaths)
	if err != nil {
		return nil, err
	}

	var out []WikiPageSummary
	for _, p := range pagePaths {
		slug := wikiPathToSlug(p)
		if slug == "" {
			continue
		}
		if allowedSlugs != nil {
			if _, ok := allowedSlugs[slug]; !ok {
				continue
			}
		}
		summary := WikiPageSummary{
			Slug:   slug,
			Title:  titleFromSlug(slug),
			SHA:    blobSHAs[p],
			Labels: labelsBySlug[slug],
		}
		if meta, ok := metadata[p]; ok {
			summary.UpdatedAt = meta.UpdatedAt
			summary.LastAuthor = meta.LastAuthor
		}
		out = append(out, summary)
	}
	if out == nil {
		out = []WikiPageSummary{}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

func (s *Service) wikiBlobSHAsAtRef(ctx context.Context, repoFullName, ref string, paths []string) (map[string]string, error) {
	if len(paths) == 0 {
		return map[string]string{}, nil
	}
	return s.Git.BlobSHAs(ctx, repoFullName, ref, paths)
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

type wikiPageMetadata struct {
	UpdatedAt  time.Time
	LastAuthor *db.User
	CommitSHA  string
}

func (s *Service) wikiPageMetadata(ctx context.Context, repoFullName string, paths []string) (map[string]wikiPageMetadata, error) {
	return s.wikiPageMetadataAtRef(ctx, repoFullName, "", paths)
}

func (s *Service) wikiPageMetadataAtRef(ctx context.Context, repoFullName, ref string, paths []string) (map[string]wikiPageMetadata, error) {
	commits, err := s.Git.LatestCommitsForPathsAtRef(ctx, repoFullName, ref, paths)
	if err != nil {
		return nil, err
	}
	authors := s.resolveWikiCommitAuthors(ctx, commits)
	out := make(map[string]wikiPageMetadata, len(commits))
	for path, commit := range commits {
		meta := wikiPageMetadata{
			LastAuthor: authors[path],
			CommitSHA:  commit.SHA,
		}
		if commit.Date != "" {
			if updatedAt, err := time.Parse(time.RFC3339, commit.Date); err == nil {
				meta.UpdatedAt = updatedAt
			}
		}
		out[path] = meta
	}
	return out, nil
}

func (s *Service) resolveWikiCommitAuthors(ctx context.Context, commits map[string]gitstore.SearchCommitInfo) map[string]*db.User {
	if len(commits) == 0 {
		return nil
	}

	logins := make([]string, 0, len(commits))
	emailSet := make(map[string]struct{}, len(commits))
	emails := make([]string, 0, len(commits))
	for _, commit := range commits {
		login := strings.TrimSpace(commit.Author)
		if login != "" {
			logins = append(logins, login)
		}
		email := strings.ToLower(strings.TrimSpace(commit.Email))
		if email == "" {
			continue
		}
		if _, seen := emailSet[email]; seen {
			continue
		}
		emailSet[email] = struct{}{}
		emails = append(emails, email)
	}

	usersByLogin := s.GetUsersByLogins(ctx, logins)
	usersByEmail := s.lookupUsersByEmailCI(ctx, emails)
	out := make(map[string]*db.User, len(commits))
	for path, commit := range commits {
		email := strings.ToLower(strings.TrimSpace(commit.Email))
		if user, ok := usersByEmail[email]; ok {
			u := user
			out[path] = &u
			continue
		}
		login := strings.TrimSpace(commit.Author)
		if user, ok := usersByLogin[login]; ok {
			u := user
			out[path] = &u
		}
	}
	return out
}

func (s *Service) resolveWikiCommitUserMap(ctx context.Context, commits []gitstore.SearchCommitInfo, picker func(gitstore.SearchCommitInfo) (string, string)) map[string]*db.User {
	if len(commits) == 0 {
		return nil
	}

	logins := make([]string, 0, len(commits))
	emailSet := make(map[string]struct{}, len(commits))
	emails := make([]string, 0, len(commits))
	for _, commit := range commits {
		login, email := picker(commit)
		login = strings.TrimSpace(login)
		if login != "" {
			logins = append(logins, login)
		}
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" {
			continue
		}
		if _, seen := emailSet[email]; seen {
			continue
		}
		emailSet[email] = struct{}{}
		emails = append(emails, email)
	}

	usersByLogin := s.GetUsersByLogins(ctx, logins)
	usersByEmail := s.lookupUsersByEmailCI(ctx, emails)
	out := make(map[string]*db.User, len(commits))
	for _, commit := range commits {
		login, email := picker(commit)
		if user, ok := usersByLogin[strings.TrimSpace(login)]; ok {
			u := user
			out[commit.SHA] = &u
			continue
		}
		email = strings.ToLower(strings.TrimSpace(email))
		if user, ok := usersByEmail[email]; ok {
			u := user
			out[commit.SHA] = &u
		}
	}
	return out
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
func (s *Service) GetWikiPageAtRef(ctx context.Context, repoFullName, slug, ref string) (WikiPage, error) {
	if err := validateReadableWikiSlug(slug); err != nil {
		return WikiPage{}, ErrNotFound
	}
	if s.Git == nil {
		return WikiPage{}, errors.New("git store unavailable")
	}
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return WikiPage{}, err
	}
	ref = strings.TrimSpace(ref)
	if ref != "" {
		if !wikiCommitSHARE.MatchString(ref) {
			return WikiPage{}, fmt.Errorf("%w: invalid ref", ErrValidation)
		}
	}
	full := wikiRepoFullName(repoFullName)
	if !s.Git.Exists(ctx, full) {
		return WikiPage{}, ErrNotFound
	}
	path := wikiSlugToPath(slug)
	if ref != "" {
		history, err := s.Git.ListAllCommits(ctx, full, &gitstore.ListCommitsOptions{Path: path})
		if err != nil {
			return WikiPage{}, err
		}
		found := false
		for _, commit := range history {
			if strings.EqualFold(commit.SHA, ref) {
				found = true
				break
			}
		}
		if !found {
			return WikiPage{}, ErrNotFound
		}
	}
	body, blobSHA, err := s.Git.ReadFileWithSHAAtRef(ctx, full, path, ref)
	if err != nil {
		return WikiPage{}, ErrNotFound
	}
	bodyStr := string(body)
	metadata, err := s.wikiPageMetadataAtRef(ctx, full, ref, []string{path})
	if err != nil {
		return WikiPage{}, err
	}
	labelsBySlug, err := s.wikiLabelsForSlugs(ctx, rep.ID, []string{slug})
	if err != nil {
		return WikiPage{}, err
	}
	meta := metadata[path]
	return WikiPage{
		Slug:       slug,
		Title:      titleFromSlug(slug),
		Body:       bodyStr,
		UpdatedAt:  meta.UpdatedAt,
		SHA:        blobSHA,
		LastAuthor: meta.LastAuthor,
		Labels:     labelsBySlug[slug],
	}, nil
}

// ListWikiPageHistory returns newest-first revisions for one wiki page.
func (s *Service) ListWikiPageHistory(ctx context.Context, repoFullName, slug string) ([]WikiPageHistoryEntry, error) {
	history, _, err := s.ListWikiPageHistoryPage(ctx, repoFullName, slug, 1, 0)
	return history, err
}

// ListWikiPageHistoryPage returns one page of newest-first revisions for one wiki page
// plus the total number of matching revisions.
func (s *Service) ListWikiPageHistoryPage(ctx context.Context, repoFullName, slug string, page, perPage int) ([]WikiPageHistoryEntry, int, error) {
	if err := validateWikiSlug(slug); err != nil {
		return nil, 0, err
	}
	if s.Git == nil {
		return nil, 0, errors.New("git store unavailable")
	}
	full := wikiRepoFullName(repoFullName)
	if !s.Git.Exists(ctx, full) {
		return nil, 0, ErrNotFound
	}
	path := wikiSlugToPath(slug)
	if _, err := s.Git.ReadFile(ctx, full, path); err != nil {
		return nil, 0, ErrNotFound
	}

	total, err := s.Git.CountCommits(ctx, full, &gitstore.ListCommitsOptions{Path: path})
	if err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, ErrNotFound
	}

	commits, err := s.Git.ListCommitsPage(ctx, full, page, perPage, &gitstore.ListCommitsOptions{Path: path})
	if err != nil {
		return nil, 0, err
	}

	authors := s.resolveWikiCommitUserMap(ctx, commits, func(commit gitstore.SearchCommitInfo) (string, string) {
		return commit.Author, commit.Email
	})
	committers := s.resolveWikiCommitUserMap(ctx, commits, func(commit gitstore.SearchCommitInfo) (string, string) {
		return commit.Committer, commit.CommitterEmail
	})

	out := make([]WikiPageHistoryEntry, 0, len(commits))
	for _, commit := range commits {
		entry := WikiPageHistoryEntry{
			SHA:       commit.SHA,
			Message:   commit.Message,
			Author:    authors[commit.SHA],
			Committer: committers[commit.SHA],
		}
		exists, err := s.Git.FileExistsAtRef(ctx, full, path, commit.SHA)
		if err != nil {
			return nil, 0, err
		}
		if exists {
			body, err := s.Git.ReadFileAtRef(ctx, full, path, commit.SHA)
			if err != nil {
				return nil, 0, err
			}
			entry.BodySize = len(body)
		}
		dateValue := commit.CommitterDate
		if dateValue == "" {
			dateValue = commit.Date
		}
		if dateValue != "" {
			if parsed, err := time.Parse(time.RFC3339, dateValue); err == nil {
				entry.Date = parsed
			}
		}
		out = append(out, entry)
	}
	return out, total, nil
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
func (s *Service) PutWikiPage(ctx context.Context, repoFullName, slug, body, message, expectedSHA string) (WikiPage, error) {
	if err := validateWikiSlug(slug); err != nil {
		return WikiPage{}, err
	}
	if s.Git == nil {
		return WikiPage{}, errors.New("git store unavailable")
	}
	if err := s.ensureWikiRepo(ctx, repoFullName); err != nil {
		return WikiPage{}, err
	}
	if message == "" {
		message = "Update " + slug
	}
	full := wikiRepoFullName(repoFullName)
	err := s.Git.WithRepoLock(ctx, full, func() error {
		if err := s.ensureNoWikiPrefixCollision(ctx, full, slug, ""); err != nil {
			return err
		}
		if expectedSHA == "" {
			_, err := s.Git.WriteFile(
				ctx,
				full,
				wikiDefaultBranch,
				wikiSlugToPath(slug),
				message,
				[]byte(body),
			)
			return err
		}
		currentPage, headSHA, err := s.getCurrentWikiPageAtHEAD(ctx, repoFullName, slug)
		switch {
		case err == nil:
			if !strings.EqualFold(expectedSHA, currentPage.SHA) {
				return &WikiConflictError{ExpectedSHA: expectedSHA, CurrentPage: &currentPage}
			}
		case errors.Is(err, ErrNotFound):
			return &WikiConflictError{ExpectedSHA: expectedSHA, CurrentPage: nil}
		default:
			return err
		}
		_, err = s.Git.WriteFileIfBranchHead(
			ctx,
			full,
			wikiDefaultBranch,
			wikiSlugToPath(slug),
			message,
			[]byte(body),
			headSHA,
		)
		if errors.Is(err, gitstore.ErrRefChanged) {
			currentPage, currentErr := s.getCurrentWikiPage(ctx, repoFullName, slug)
			if currentErr == nil {
				return &WikiConflictError{ExpectedSHA: expectedSHA, CurrentPage: &currentPage}
			}
			if errors.Is(currentErr, ErrNotFound) {
				return &WikiConflictError{ExpectedSHA: expectedSHA, CurrentPage: nil}
			}
			return currentErr
		}
		return err
	})
	if err != nil {
		return WikiPage{}, err
	}
	s.invalidateWikiBacklinks(repoFullName)
	page, err := s.getCurrentWikiPage(ctx, repoFullName, slug)
	if err != nil {
		return WikiPage{}, err
	}
	s.queueWikiSearchUpsert(ctx, repoFullName, page)
	return page, nil
}

// DeleteWikiPage removes a page. Returns ErrNotFound when the wiki repo
// or the slug doesn't exist (matches GitHub's REST contract).
func (s *Service) DeleteWikiPage(ctx context.Context, repoFullName, slug, message string) error {
	if err := validateWikiSlug(slug); err != nil {
		return err
	}
	if s.Git == nil {
		return errors.New("git store unavailable")
	}
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return err
	}
	full := wikiRepoFullName(repoFullName)
	if !s.Git.Exists(ctx, full) {
		return ErrNotFound
	}
	if message == "" {
		message = "Delete " + slug
	}
	path := wikiSlugToPath(slug)
	const maxDeleteAttempts = 5
	for attempt := 0; attempt < maxDeleteAttempts; attempt++ {
		err = s.Git.WithRepoLock(ctx, full, func() error {
			if _, err := s.Git.ReadFile(ctx, full, path); err != nil {
				return ErrNotFound
			}
			_, err := s.Git.DeleteFileFromRepo(ctx, full, wikiDefaultBranch, path, message)
			return err
		})
		if errors.Is(err, gitstore.ErrRefChanged) {
			continue
		}
		if err != nil {
			return err
		}
		break
	}
	if errors.Is(err, gitstore.ErrRefChanged) {
		return fmt.Errorf("delete wiki page %q: %w", slug, err)
	}
	if err := s.deleteWikiPageLabels(ctx, rep.ID, slug); err != nil {
		return err
	}
	s.invalidateWikiBacklinks(repoFullName)
	s.queueWikiSearchDelete(ctx, repoFullName, slug)
	return nil
}

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
	if s.Git == nil {
		return WikiMoveResult{}, errors.New("git store unavailable")
	}
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return WikiMoveResult{}, err
	}

	full := wikiRepoFullName(repoFullName)
	if !s.Git.Exists(ctx, full) {
		return WikiMoveResult{}, ErrNotFound
	}

	rewrittenBodies := map[string]string{}
	skipped := make([]WikiRewriteSkip, 0)
	err = s.Git.WithRepoLock(ctx, full, func() error {
		sourcePath := wikiSlugToPath(slug)
		destPath := wikiSlugToPath(newSlug)

		currentPage, _, err := s.getCurrentWikiPageAtHEAD(ctx, repoFullName, slug)
		switch {
		case errors.Is(err, ErrNotFound):
			return ErrNotFound
		case err != nil:
			return err
		}
		if !strings.EqualFold(currentPage.SHA, ifMatch) {
			return &wikiMoveConflictError{
				code:    wikiMoveCodeStale,
				message: fmt.Sprintf("%s: source page %q is stale", wikiMoveCodeStale, slug),
			}
		}
		if _, err := s.Git.ReadFile(ctx, full, destPath); err == nil {
			return &wikiMoveConflictError{
				code:    wikiMoveCodeDestTaken,
				message: fmt.Sprintf("%s: destination page %q already exists", wikiMoveCodeDestTaken, newSlug),
			}
		}
		if err := s.ensureNoWikiPrefixCollision(ctx, full, newSlug, slug); err != nil {
			return err
		}

		paths, err := s.Git.ListTreeFiles(ctx, full)
		if err != nil {
			return err
		}
		for _, path := range paths {
			candidateSlug := wikiPathToSlug(path)
			if candidateSlug == "" || candidateSlug == slug {
				continue
			}
			body, err := s.Git.ReadFile(ctx, full, path)
			if err != nil {
				return err
			}
			rewritten, changed, err := rewriteWikiReferences(string(body), slug, newSlug)
			if err != nil {
				slog.WarnContext(ctx, "wiki move skipped inbound rewrite", "slug", candidateSlug, "reason", err.Error())
				skipped = append(skipped, WikiRewriteSkip{
					Slug:   candidateSlug,
					Reason: err.Error(),
				})
				continue
			}
			if changed {
				rewrittenBodies[candidateSlug] = rewritten
			}
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

		mutations := make([]gitstore.FileMutation, 0, len(rewrittenBodies)+2)
		mutations = append(mutations,
			gitstore.FileMutation{Path: sourcePath, Delete: true},
			gitstore.FileMutation{Path: destPath, Content: []byte(currentPage.Body)},
		)
		rewrittenSlugs := make([]string, 0, len(rewrittenBodies))
		for candidateSlug := range rewrittenBodies {
			rewrittenSlugs = append(rewrittenSlugs, candidateSlug)
		}
		sort.Strings(rewrittenSlugs)
		for _, candidateSlug := range rewrittenSlugs {
			mutations = append(mutations, gitstore.FileMutation{
				Path:    wikiSlugToPath(candidateSlug),
				Content: []byte(rewrittenBodies[candidateSlug]),
			})
		}

		_, err = s.Git.CommitFiles(ctx, full, wikiDefaultBranch, commitMessage, mutations)
		return err
	})
	if err != nil {
		return WikiMoveResult{}, err
	}

	s.invalidateWikiBacklinks(repoFullName)
	if err := s.moveWikiPageLabels(ctx, rep.ID, map[string]string{slug: newSlug}); err != nil {
		return WikiMoveResult{}, err
	}
	moved, err := s.getCurrentWikiPage(ctx, repoFullName, newSlug)
	if err != nil {
		return WikiMoveResult{}, err
	}
	s.queueWikiSearchDelete(ctx, repoFullName, slug)
	s.queueWikiSearchUpsert(ctx, repoFullName, moved)
	rewrites, err := s.wikiSummariesForBodies(ctx, full, rewrittenBodies)
	if err != nil {
		return WikiMoveResult{}, err
	}
	sort.Slice(skipped, func(i, j int) bool {
		return skipped[i].Slug < skipped[j].Slug
	})
	return WikiMoveResult{
		Moved:    moved,
		Rewrites: rewrites,
		Skipped:  skipped,
	}, nil
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

	full := wikiRepoFullName(repoFullName)
	if !s.Git.Exists(ctx, full) {
		return WikiBulkMoveResult{}, &WikiBulkMoveNotFoundError{From: from}
	}
	if message == "" {
		message = "Move wiki prefix " + from + " to " + to
	}

	var (
		result          WikiBulkMoveResult
		rewrittenBodies = map[string]string{}
		skipped         = make([]WikiRewriteSkip, 0)
	)
	err = s.Git.WithRepoLock(ctx, full, func() error {
		headSHA, err := s.Git.HeadSHA(ctx, full, wikiDefaultBranch)
		if err != nil {
			return &WikiBulkMoveNotFoundError{From: from}
		}

		paths, err := s.Git.ListTreeFiles(ctx, full)
		if err != nil {
			return err
		}
		currentSlugs := wikiSlugsFromPaths(paths)
		sources := wikiBulkMoveSources(currentSlugs, from)
		if len(sources) == 0 {
			return &WikiBulkMoveNotFoundError{From: from}
		}

		missing := make([]string, 0)
		for _, slug := range sources {
			if strings.TrimSpace(ifMatch[slug]) == "" {
				missing = append(missing, slug)
			}
		}
		if len(missing) > 0 {
			return &WikiBulkMoveValidationError{From: from, MissingSlugs: missing}
		}

		sourceSet := make(map[string]struct{}, len(sources))
		for _, slug := range sources {
			sourceSet[slug] = struct{}{}
		}
		unaffected := make([]string, 0, len(currentSlugs)-len(sources))
		for _, slug := range currentSlugs {
			if _, ok := sourceSet[slug]; !ok {
				unaffected = append(unaffected, slug)
			}
		}

		moves := make([]gitstore.FileMove, 0, len(sources))
		moved := make([]WikiBulkMoveEntry, 0, len(sources))
		remaps := make([]WikiBulkMoveEntry, 0, len(sources))
		movedBodies := make(map[string]string, len(sources))
		movedTargets := make(map[string]struct{}, len(sources))
		conflicts := make([]WikiBulkMoveConflict, 0)
		for _, slug := range sources {
			destSlug := remapWikiMoveSlug(slug, from, to)
			if err := validateWikiSlug(destSlug); err != nil {
				return err
			}

			page, err := s.getWikiPageAtRef(ctx, repoFullName, slug, headSHA)
			if err != nil {
				return &WikiBulkMoveNotFoundError{From: from}
			}

			expectedSHA := strings.TrimSpace(ifMatch[slug])
			if !strings.EqualFold(page.SHA, expectedSHA) {
				conflicts = append(conflicts, WikiBulkMoveConflict{
					From:       slug,
					To:         destSlug,
					Code:       wikiMoveCodeStale,
					Message:    fmt.Sprintf("%s: source page %q is stale", wikiMoveCodeStale, slug),
					CurrentSHA: page.SHA,
				})
				continue
			}

			if sliceContains(unaffected, destSlug) {
				conflicts = append(conflicts, WikiBulkMoveConflict{
					From:    slug,
					To:      destSlug,
					Code:    wikiMoveCodeDestTaken,
					Message: fmt.Sprintf("%s: destination page %q already exists", wikiMoveCodeDestTaken, destSlug),
				})
				continue
			}

			if collision := findWikiPrefixCollision(destSlug, unaffected, nil); collision != "" {
				conflicts = append(conflicts, WikiBulkMoveConflict{
					From:          slug,
					To:            destSlug,
					Code:          wikiMoveCodePrefix,
					Message:       fmt.Sprintf("%s: destination page %q conflicts with existing page %q", wikiMoveCodePrefix, destSlug, collision),
					ConflictsWith: collision,
				})
				continue
			}

			moves = append(moves, gitstore.FileMove{
				OldPath: wikiSlugToPath(slug),
				NewPath: wikiSlugToPath(destSlug),
			})
			moved = append(moved, WikiBulkMoveEntry{
				From: slug,
				To:   destSlug,
				SHA:  page.SHA,
			})
			remaps = append(remaps, WikiBulkMoveEntry{
				From: slug,
				To:   destSlug,
				SHA:  page.SHA,
			})
			movedBodies[destSlug] = page.Body
			movedTargets[destSlug] = struct{}{}
		}

		if len(conflicts) > 0 {
			return &WikiBulkMoveConflictError{Conflicts: conflicts}
		}

		mutatedBodies := make(map[string]string, len(movedBodies))
		for slug, body := range movedBodies {
			mutatedBodies[slug] = body
		}
		for _, candidateSlug := range unaffected {
			body, err := s.Git.ReadFile(ctx, full, wikiSlugToPath(candidateSlug))
			if err != nil {
				return err
			}
			mutatedBodies[candidateSlug] = string(body)
		}

		for candidateSlug, originalBody := range mutatedBodies {
			rewritten := originalBody
			changed := false
			skipPage := false
			for _, remap := range remaps {
				nextBody, bodyChanged, err := rewriteWikiReferences(rewritten, remap.From, remap.To)
				if err != nil {
					slog.WarnContext(ctx, "wiki bulk move skipped inbound rewrite", "slug", candidateSlug, "reason", err.Error())
					skipped = append(skipped, WikiRewriteSkip{
						Slug:   candidateSlug,
						Reason: err.Error(),
					})
					skipPage = true
					break
				}
				if bodyChanged {
					rewritten = nextBody
					changed = true
				}
			}
			if skipPage {
				continue
			}
			if changed {
				mutatedBodies[candidateSlug] = rewritten
				if _, isMovedTarget := movedTargets[candidateSlug]; !isMovedTarget {
					rewrittenBodies[candidateSlug] = rewritten
				}
			}
		}

		commitMessage := message
		if len(rewrittenBodies) > 0 && !strings.Contains(commitMessage, "rewrote refs in") {
			suffix := "pages"
			if len(rewrittenBodies) == 1 {
				suffix = "page"
			}
			commitMessage += fmt.Sprintf(" (rewrote refs in %d %s)", len(rewrittenBodies), suffix)
		}

		mutations := make([]gitstore.FileMutation, 0, len(moves)*2+len(mutatedBodies))
		for _, move := range moves {
			mutations = append(mutations,
				gitstore.FileMutation{Path: move.OldPath, Delete: true},
				gitstore.FileMutation{Path: move.NewPath, Content: []byte(mutatedBodies[wikiPathToSlug(move.NewPath)])},
			)
		}
		rewrittenSlugs := make([]string, 0, len(rewrittenBodies))
		for slug := range rewrittenBodies {
			rewrittenSlugs = append(rewrittenSlugs, slug)
		}
		sort.Strings(rewrittenSlugs)
		for _, slug := range rewrittenSlugs {
			mutations = append(mutations, gitstore.FileMutation{
				Path:    wikiSlugToPath(slug),
				Content: []byte(rewrittenBodies[slug]),
			})
		}

		commitSHA, err := s.Git.CommitFiles(ctx, full, wikiDefaultBranch, commitMessage, mutations)
		if err != nil {
			return err
		}
		result = WikiBulkMoveResult{
			Moved:  moved,
			Commit: commitSHA,
		}
		return nil
	})
	if err != nil {
		return WikiBulkMoveResult{}, err
	}

	s.invalidateWikiBacklinks(repoFullName)
	remaps := make(map[string]string, len(result.Moved))
	for _, item := range result.Moved {
		remaps[item.From] = item.To
	}
	if err := s.moveWikiPageLabels(ctx, rep.ID, remaps); err != nil {
		return WikiBulkMoveResult{}, err
	}
	for _, item := range result.Moved {
		s.queueWikiSearchDelete(ctx, repoFullName, item.From)
		if page, err := s.GetWikiPage(ctx, repoFullName, item.To); err == nil {
			s.queueWikiSearchUpsert(ctx, repoFullName, page)
		}
	}
	rewrites, err := s.wikiSummariesForBodies(ctx, full, rewrittenBodies)
	if err != nil {
		return WikiBulkMoveResult{}, err
	}
	sort.Slice(skipped, func(i, j int) bool {
		return skipped[i].Slug < skipped[j].Slug
	})
	result.Rewrites = rewrites
	result.Skipped = skipped
	return result, nil
}

func (s *Service) ensureNoWikiPrefixCollision(ctx context.Context, repoFullName, slug, ignore string) error {
	if _, err := s.Git.HeadSHA(ctx, repoFullName, wikiDefaultBranch); err != nil {
		return nil
	}
	paths, err := s.Git.ListTreeFiles(ctx, repoFullName)
	if err != nil {
		return err
	}
	ignoreSet := map[string]struct{}{}
	if ignore != "" {
		ignoreSet[ignore] = struct{}{}
	}
	if collision := findWikiPrefixCollision(slug, wikiSlugsFromPaths(paths), ignoreSet); collision != "" {
		return fmt.Errorf("%w: wiki slug %q conflicts with existing page %q", ErrConflict, slug, collision)
	}
	return nil
}

func (s *Service) getCurrentWikiPage(ctx context.Context, repoFullName, slug string) (WikiPage, error) {
	page, err := s.GetWikiPage(ctx, repoFullName, slug)
	if err != nil {
		return WikiPage{}, err
	}
	return page, nil
}

func (s *Service) getCurrentWikiPageAtHEAD(ctx context.Context, repoFullName, slug string) (WikiPage, string, error) {
	full := wikiRepoFullName(repoFullName)
	headSHA, err := s.Git.HeadSHA(ctx, full, wikiDefaultBranch)
	if err != nil {
		return WikiPage{}, "", ErrNotFound
	}
	page, err := s.getWikiPageAtRef(ctx, repoFullName, slug, headSHA)
	if err != nil {
		return WikiPage{}, "", err
	}
	return page, headSHA, nil
}

func (s *Service) getWikiPageAtRef(ctx context.Context, repoFullName, slug, ref string) (WikiPage, error) {
	full := wikiRepoFullName(repoFullName)
	body, blobSHA, err := s.Git.ReadFileWithSHAAtRef(ctx, full, wikiSlugToPath(slug), ref)
	if err != nil {
		return WikiPage{}, ErrNotFound
	}
	bodyStr := string(body)
	return WikiPage{
		Slug:       slug,
		Title:      titleFromSlug(slug),
		Body:       bodyStr,
		SHA:        blobSHA,
		LastAuthor: nil,
	}, nil
}

func wikiBulkMoveSources(slugs []string, from string) []string {
	out := make([]string, 0)
	for _, slug := range slugs {
		if slug == from || strings.HasPrefix(slug, from+"/") {
			out = append(out, slug)
		}
	}
	return out
}

func wikiSlugsFromPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		slug := wikiPathToSlug(path)
		if slug != "" {
			out = append(out, slug)
		}
	}
	sort.Strings(out)
	return out
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

func (s *Service) wikiSummariesForBodies(ctx context.Context, wikiRepoFullName string, bodies map[string]string) ([]WikiPageSummary, error) {
	if len(bodies) == 0 {
		return []WikiPageSummary{}, nil
	}
	paths := make([]string, 0, len(bodies))
	for slug := range bodies {
		paths = append(paths, wikiSlugToPath(slug))
	}
	sort.Strings(paths)

	snapshot, err := s.Git.ResolveContentCommit(ctx, wikiRepoFullName, "")
	if err != nil {
		return nil, err
	}

	metadata, err := s.wikiPageMetadataAtRef(ctx, wikiRepoFullName, snapshot, paths)
	if err != nil {
		return nil, err
	}
	blobSHAs, err := s.wikiBlobSHAsAtRef(ctx, wikiRepoFullName, snapshot, paths)
	if err != nil {
		return nil, err
	}

	summaries := make([]WikiPageSummary, 0, len(paths))
	for _, path := range paths {
		slug := wikiPathToSlug(path)
		if slug == "" {
			continue
		}
		summary := WikiPageSummary{
			Slug:  slug,
			Title: titleFromSlug(slug),
			SHA:   blobSHAs[path],
		}
		if meta, ok := metadata[path]; ok {
			summary.UpdatedAt = meta.UpdatedAt
			summary.LastAuthor = meta.LastAuthor
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}
