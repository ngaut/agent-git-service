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
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
	"github.com/ngaut/agent-git-service/internal/wikiv2"
)

// wikiDefaultBranch matches GitHub's wiki convention so a wiki repo cloned
// directly via git uses the same branch name clients expect.
const wikiDefaultBranch = wikiv2.DefaultBranch

// wikiPageExt is the on-disk extension for a wiki page. Phase C ships
// markdown only; future phases can support .rst / .textile by widening
// the extension whitelist in resolveWikiSlug.
const wikiPageExt = wikiv2.PageExtension

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
	Size       int64
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

// WikiTreeEntry is the read model for one wiki tree entry.
type WikiTreeEntry struct {
	Path  string
	Name  string
	Kind  string
	Slug  string
	Title string
	SHA   string
	Size  int64
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
	if len(slug) > wikiMaxSlugLength {
		return fmt.Errorf("%w: wiki slug too long", ErrValidation)
	}
	parts := strings.Split(slug, "/")
	if len(parts) > wikiMaxSlugDepth {
		return fmt.Errorf("%w: wiki slug exceeds max depth", ErrValidation)
	}
	for _, part := range parts {
		if err := validateWikiSlugSegment(part); err != nil {
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

// wikiSlugToPath maps a slug to its on-disk markdown filename inside the
// wiki repo.
func wikiSlugToPath(slug string) string {
	path, err := wikiv2.SlugToPath(slug)
	if err != nil {
		return slug + wikiPageExt
	}
	return path
}

// wikiPathToSlug returns the slug for a path in the wiki repo, or "" if
// the path isn't a recognised wiki page.
func wikiPathToSlug(path string) string {
	slug, ok := wikiv2.PathToSlug(path)
	if !ok {
		return ""
	}
	return slug
}

// titleFromSlug derives the stable display title returned by wiki APIs. It is
// intentionally independent from page body contents so title responses are
// deterministic and list responses do not need to read every page body.
func titleFromSlug(slug string) string {
	return wikicatalog.TitleFromSlug(slug)
}

func lastWikiSlugSegment(slug string) string {
	if idx := strings.LastIndex(slug, "/"); idx >= 0 {
		return slug[idx+1:]
	}
	return slug
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
	if err := validateWikiSlug(link); err != nil {
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
		target := normalizeWikiReference(wikicatalog.WikiShorthandTarget(body[loc[2]:loc[3]]))
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
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return nil
	}
	return []string{slug}
}

func resolveWikiBacklinkTarget(match wikiLinkMatch, pages, topLevelPages map[string]struct{}) (string, bool) {
	resolvedTarget := match.targetSlug
	if match.literal {
		if _, ok := pages[resolvedTarget]; ok {
			return resolvedTarget, true
		}
		return "", false
	}

	if strings.Contains(resolvedTarget, "/") {
		return "", false
	}
	if _, ok := topLevelPages[resolvedTarget]; ok {
		return resolvedTarget, true
	}
	return "", false
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
	for _, p := range paths {
		slug := wikiPathToSlug(p)
		if slug == "" {
			continue
		}
		pages[slug] = struct{}{}
		if !strings.Contains(slug, "/") {
			topLevelPages[slug] = struct{}{}
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
			resolvedTarget, ok := resolveWikiBacklinkTarget(match, pages, topLevelPages)
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

func (s *Service) loadWikiBacklinksFromCatalog(ctx context.Context, repoID uint, targetSlug string) ([]WikiBacklink, bool, error) {
	if err := validateWikiSlug(targetSlug); err != nil {
		return nil, true, ErrNotFound
	}

	var pages []db.WikiPage
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND deleted_at IS NULL", repoID).
		Find(&pages).Error; err != nil {
		if isMissingTableErr(err) {
			return nil, false, nil
		}
		return nil, true, err
	}
	if len(pages) == 0 {
		return nil, false, nil
	}

	pagesByID := make(map[uint64]db.WikiPage, len(pages))
	pageSlugs := make(map[string]struct{}, len(pages))
	topLevelPages := make(map[string]struct{}, len(pages))
	var targetPage db.WikiPage
	targetFound := false
	for _, page := range pages {
		pagesByID[page.PageID] = page
		pageSlugs[page.Slug] = struct{}{}
		if !strings.Contains(page.Slug, "/") {
			topLevelPages[page.Slug] = struct{}{}
		}
		if page.Slug == targetSlug {
			targetPage = page
			targetFound = true
		}
	}
	if !targetFound {
		return nil, false, nil
	}

	var links []db.WikiPageLink
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND (dst_page_id = ? OR dst_slug = ?)", repoID, targetPage.PageID, targetSlug).
		Find(&links).Error; err != nil {
		if isMissingTableErr(err) {
			return nil, false, nil
		}
		return nil, true, err
	}

	seenSources := make(map[string]struct{}, len(links))
	backlinks := make([]WikiBacklink, 0, len(links))
	for _, link := range links {
		sourcePage, ok := pagesByID[link.SrcPageID]
		if !ok {
			continue
		}
		if _, seen := seenSources[sourcePage.Slug]; seen {
			continue
		}
		body, err := s.wikiPageBody(ctx, sourcePage)
		if err != nil {
			return nil, true, err
		}
		for _, match := range extractWikiLinkMatches(string(body)) {
			resolvedTarget, ok := resolveWikiBacklinkTarget(match, pageSlugs, topLevelPages)
			if !ok || resolvedTarget != targetPage.Slug {
				continue
			}
			seenSources[sourcePage.Slug] = struct{}{}
			backlinks = append(backlinks, WikiBacklink{
				Slug:    sourcePage.Slug,
				Title:   titleFromSlug(sourcePage.Slug),
				Snippet: match.snippet,
			})
			break
		}
	}
	sort.Slice(backlinks, func(i, j int) bool {
		return backlinks[i].Slug < backlinks[j].Slug
	})
	return backlinks, true, nil
}

// ensureWikiRepo lazily creates the sibling wiki repo on first write.
// Idempotent: gitstore.Init early-returns when the directory already
// exists. The wiki repo is created without a seed README so empty wiki
// state is a clean tree, not a placeholder commit.
func (s *Service) ensureWikiRepo(ctx context.Context, repoFullName string) error {
	if s.Git == nil {
		return errors.New("git store unavailable")
	}
	return ensureWikiRepoWithGit(ctx, s.Git, repoFullName)
}

func ensureWikiRepoWithGit(ctx context.Context, git *gitstore.Store, repoFullName string) error {
	return git.Init(ctx, wikiRepoFullName(repoFullName), wikiDefaultBranch, false)
}

// withWikiCatalogWriteLock serializes catalog writes and git-ingest refreshes
// for one wiki repository. This keeps the read-path freshness hook from racing
// REST writes through the same catalog tables in tests and production.
func (s *Service) withWikiCatalogWriteLock(ctx context.Context, repoFullName string, fn func() error) error {
	if s.Git == nil {
		return errors.New("git store unavailable")
	}
	return s.withWikiCatalogWriteLockAndGit(ctx, s.Git, repoFullName, fn)
}

func (s *Service) withWikiCatalogWriteLockOnly(ctx context.Context, repoFullName string, fn func(db.Repository) error) error {
	stageStarted := time.Now()
	repo, err := s.LookupRepoIdentity(ctx, repoFullName)
	observeWikiWritePhase(ctx, wikiWritePhaseLockRepoLookup, stageStarted)
	if err != nil {
		return err
	}
	return s.withWikiCatalogWriteLockOnlyForRepo(ctx, repo, fn)
}

func (s *Service) withWikiCatalogWriteLockOnlyForRepo(ctx context.Context, repo db.Repository, fn func(db.Repository) error) error {
	mu := s.getWikiGitIngestSyncMu(s.wikiRepoKey(ctx, repo))
	stageStarted := time.Now()
	mu.Lock()
	observeWikiWritePhase(ctx, wikiWritePhaseCatalogLockWait, stageStarted)
	defer mu.Unlock()
	return fn(repo)
}

// WithWikiCatalogWriteLockForReceivePack serializes wiki receive-pack catalog
// ingestion with REST writes while the caller manages the wiki repo git lock.
func (s *Service) WithWikiCatalogWriteLockForReceivePack(ctx context.Context, repoFullName string, fn func() error) error {
	return s.withWikiCatalogWriteLockOnly(ctx, repoFullName, func(db.Repository) error {
		return fn()
	})
}

func (s *Service) withWikiCatalogWriteLockAndGit(ctx context.Context, git *gitstore.Store, repoFullName string, fn func() error) error {
	return s.withWikiCatalogWriteLockOnly(ctx, repoFullName, func(repo db.Repository) error {
		return s.withWikiGitLock(ctx, git, repo, fn)
	})
}

func (s *Service) withWikiCatalogWriteLockAndGitForRepo(ctx context.Context, git *gitstore.Store, repo db.Repository, fn func() error) error {
	// The caller already loaded the authenticated repository identity.
	recordWikiWriteDuration(ctx, wikiWritePhaseLockRepoLookup, 0)
	return s.withWikiCatalogWriteLockOnlyForRepo(ctx, repo, func(repo db.Repository) error {
		return s.withWikiGitLock(ctx, git, repo, fn)
	})
}

// withWikiRESTWriteLocksForRepo lets the next REST writer capture its catalog
// snapshot while the previous writer is publishing the already-committed Git
// ref. The catalog lock remains the outer serialization lock, but it is
// released after locked returns and before afterCatalogUnlock runs. The Git
// lock stays held through publication, so no receive-pack, compaction, catalog
// commit, or later ref update can pass it. The next REST writer may persist
// immutable child objects early, but validates HEAD after acquiring this lock.
func (s *Service) withWikiRESTWriteLocksForRepo(
	ctx context.Context,
	git *gitstore.Store,
	repo db.Repository,
	beforeGit func() error,
	locked func() (afterCatalogUnlock func() error, err error),
) (err error) {
	recordWikiWriteDuration(ctx, wikiWritePhaseLockRepoLookup, 0)
	mu := s.getWikiGitIngestSyncMu(s.wikiRepoKey(ctx, repo))
	waitStarted := time.Now()
	mu.Lock()
	observeWikiWritePhase(ctx, wikiWritePhaseCatalogLockWait, waitStarted)
	catalogLocked := true
	criticalSectionStarted := time.Now()
	defer func() {
		if catalogLocked {
			observeWikiWritePhase(ctx, wikiWritePhaseCriticalSection, criticalSectionStarted)
			mu.Unlock()
		}
	}()

	if beforeGit != nil {
		if err := beforeGit(); err != nil {
			return err
		}
	}

	full := wikiRepoFullName(repo.FullName)
	gitWaitStarted := time.Now()
	return git.WithRepoLock(ctx, full, func() error {
		observeWikiWritePhase(ctx, wikiWritePhaseGitLockWait, gitWaitStarted)
		afterCatalogUnlock, lockedErr := locked()
		observeWikiWritePhase(ctx, wikiWritePhaseCriticalSection, criticalSectionStarted)
		mu.Unlock()
		catalogLocked = false
		if lockedErr != nil {
			return lockedErr
		}
		if afterCatalogUnlock == nil {
			return nil
		}
		return afterCatalogUnlock()
	})
}

func (s *Service) withWikiGitLock(ctx context.Context, git *gitstore.Store, repo db.Repository, fn func() error) error {
	full := wikiRepoFullName(repo.FullName)
	waitStarted := time.Now()
	return git.WithRepoLock(ctx, full, func() error {
		observeWikiWritePhase(ctx, wikiWritePhaseGitLockWait, waitStarted)
		return fn()
	})
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
	pages, _, err := s.ListWikiPagesPaginated(ctx, repoFullName, opts, 0, 0)
	return pages, err
}

// ListWikiTreeAtRef returns the direct children for one wiki directory view.
// Live tree reads are synthesized from the same page index/catalog rows that
// serve /wiki/pages, so the sidebar cannot advertise page slugs that the page
// resolver will 404. Git tree reads remain available for explicit historical
// refs and for legacy repositories that have not produced catalog rows yet.
func (s *Service) ListWikiTreeAtRef(ctx context.Context, repoFullName, dirPath, ref string) ([]WikiTreeEntry, error) {
	if strings.TrimSpace(dirPath) != "" {
		dirPath = strings.Trim(strings.TrimSpace(dirPath), "/")
		if err := validateWikiSlug(dirPath); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(ref) == "" {
		rep, err := s.getRepoBase(ctx, repoFullName)
		if err != nil {
			return nil, err
		}
		if s.WikiCatalog != nil {
			if err := s.ensureWikiCatalogCurrent(ctx, repoFullName); err != nil {
				return nil, err
			}
			tree, _, ok, err := s.listWikiTreeFromCatalog(ctx, rep.ID, dirPath)
			if err != nil {
				return nil, err
			}
			if ok {
				return tree, nil
			}
		}
		if rows, ok, err := s.loadCurrentWikiV2Rows(ctx, repoFullName, rep.ID); err != nil {
			return nil, err
		} else if ok {
			return wikiTreeFromV2Rows(rows, dirPath), nil
		}
	}
	full := wikiRepoFullName(repoFullName)
	if !s.Git.Exists(ctx, full) || s.Git.IsEmpty(ctx, full) {
		return []WikiTreeEntry{}, nil
	}

	rawEntries, err := s.Git.ListDirAtRef(ctx, full, dirPath, ref)
	if err != nil {
		return nil, err
	}
	out := make([]WikiTreeEntry, 0, len(rawEntries))
	for _, entry := range rawEntries {
		switch entry.Type {
		case "tree":
			out = append(out, WikiTreeEntry{
				Path: entry.Path,
				Name: entry.Name,
				Kind: "directory",
				SHA:  entry.SHA,
			})
		case "blob":
			slug := wikiPathToSlug(entry.Path)
			if slug == "" {
				continue
			}
			out = append(out, WikiTreeEntry{
				Path:  slug,
				Name:  titleFromSlug(lastWikiSlugSegment(slug)),
				Kind:  "page",
				Slug:  slug,
				Title: titleFromSlug(slug),
				SHA:   entry.SHA,
				Size:  entry.Size,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind == out[j].Kind {
			return out[i].Path < out[j].Path
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}

type wikiTreePageRow struct {
	Slug        string
	HeadBlobSHA string `gorm:"column:head_blob_sha"`
	Size        int    `gorm:"column:body_size"`
}

func (s *Service) listWikiTreeFromCatalog(ctx context.Context, repoID uint, dirPath string) ([]WikiTreeEntry, int, bool, error) {
	var pages []wikiTreePageRow
	if err := buildWikiTreePageRowsQuery(s.DBForCtx(ctx), repoID, dirPath).Find(&pages).Error; err != nil {
		if isMissingTableErr(err) {
			return nil, 0, false, nil
		}
		return nil, 0, false, err
	}
	if len(pages) == 0 {
		hasCatalogState, err := s.wikiCatalogHasHead(ctx, repoID)
		if err != nil {
			return nil, 0, false, err
		}
		if hasCatalogState {
			return []WikiTreeEntry{}, 0, true, nil
		}
		return nil, 0, false, nil
	}
	return wikiTreeFromPageRows(pages, dirPath), len(pages), true, nil
}

func buildWikiTreePageRowsQuery(database *gorm.DB, repoID uint, dirPath string) *gorm.DB {
	query := database.
		Model(&db.WikiPage{}).
		Select("slug", "head_blob_sha", "body_size").
		Where("repository_id = ? AND deleted_at IS NULL", repoID)
	return applyWikiPathFilterQuery(query, "slug", dirPath, true).Order("slug ASC")
}

// ListWikiTreeWithPageCount returns the live direct-child tree and the number
// of descendant pages represented by that tree snapshot. Catalog-backed reads
// obtain both from the same indexed row scan.
func (s *Service) ListWikiTreeWithPageCount(ctx context.Context, repoFullName, dirPath string) ([]WikiTreeEntry, int, error) {
	dirPath = strings.Trim(strings.TrimSpace(dirPath), "/")
	if dirPath != "" {
		if err := validateWikiSlug(dirPath); err != nil {
			return nil, 0, err
		}
	}
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return nil, 0, err
	}
	if s.WikiCatalog != nil {
		if err := s.ensureWikiCatalogCurrent(ctx, repoFullName); err != nil {
			return nil, 0, err
		}
		tree, count, ok, err := s.listWikiTreeFromCatalog(ctx, rep.ID, dirPath)
		if err != nil {
			return nil, 0, err
		}
		if ok {
			return tree, count, nil
		}
	}
	if rows, ok, err := s.loadCurrentWikiV2Rows(ctx, repoFullName, rep.ID); err != nil {
		return nil, 0, err
	} else if ok {
		count := 0
		for _, row := range rows {
			if wikiSlugMatchesPathFilter(row.Slug, dirPath, true) {
				count++
			}
		}
		return wikiTreeFromV2Rows(rows, dirPath), count, nil
	}

	tree, err := s.ListWikiTreeAtRef(ctx, repoFullName, dirPath, "")
	if err != nil {
		return nil, 0, err
	}
	pages, err := s.ListWikiPages(ctx, repoFullName, ListWikiPagesOptions{Path: dirPath, Recursive: true})
	if err != nil {
		return nil, 0, err
	}
	return tree, len(pages), nil
}

func wikiTreeFromV2Rows(rows []db.WikiPageIndex, dirPath string) []WikiTreeEntry {
	pages := make([]wikiTreePageRow, 0, len(rows))
	for _, row := range rows {
		pages = append(pages, wikiTreePageRow{
			Slug:        row.Slug,
			HeadBlobSHA: row.HeadBlobSHA,
			Size:        row.Size,
		})
	}
	return wikiTreeFromPageRows(pages, dirPath)
}

func wikiTreeFromPageRows(pages []wikiTreePageRow, dirPath string) []WikiTreeEntry {
	out := make([]WikiTreeEntry, 0)
	seenDirs := map[string]struct{}{}
	prefix := ""
	if dirPath != "" {
		prefix = dirPath + "/"
	}
	for _, page := range pages {
		slug := strings.Trim(page.Slug, "/")
		if slug == "" {
			continue
		}
		rest := slug
		if dirPath != "" {
			if slug == dirPath || !strings.HasPrefix(slug, prefix) {
				continue
			}
			rest = strings.TrimPrefix(slug, prefix)
		}
		if rest == "" {
			continue
		}
		if idx := strings.IndexByte(rest, '/'); idx >= 0 {
			childName := rest[:idx]
			if childName == "" {
				continue
			}
			childPath := childName
			if dirPath != "" {
				childPath = dirPath + "/" + childName
			}
			if _, ok := seenDirs[childPath]; ok {
				continue
			}
			seenDirs[childPath] = struct{}{}
			out = append(out, WikiTreeEntry{
				Path: childPath,
				Name: childName,
				Kind: "directory",
			})
			continue
		}
		out = append(out, WikiTreeEntry{
			Path:  slug,
			Name:  titleFromSlug(lastWikiSlugSegment(slug)),
			Kind:  "page",
			Slug:  slug,
			Title: titleFromSlug(slug),
			SHA:   page.HeadBlobSHA,
			Size:  int64(page.Size),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind == out[j].Kind {
			return out[i].Path < out[j].Path
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// WikiTreeFromPageSummaries reuses a complete recursive list result to build
// the matching direct-child tree without a second scan of the live page table.
func WikiTreeFromPageSummaries(pages []WikiPageSummary, dirPath string) []WikiTreeEntry {
	rows := make([]wikiTreePageRow, 0, len(pages))
	for _, page := range pages {
		rows = append(rows, wikiTreePageRow{
			Slug:        page.Slug,
			HeadBlobSHA: page.SHA,
			Size:        int(page.Size),
		})
	}
	return wikiTreeFromPageRows(rows, dirPath)
}

func (s *Service) wikiCatalogHasHead(ctx context.Context, repoID uint) (bool, error) {
	var head db.WikiRepoHead
	if err := s.DBForCtx(ctx).
		Select("repository_id").
		First(&head, "repository_id = ?", repoID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || isMissingTableErr(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *Service) wikiCatalogHasLiveState(ctx context.Context, repoID uint) (bool, error) {
	var pageCount int64
	if err := s.DBForCtx(ctx).
		Model(&db.WikiPage{}).
		Where("repository_id = ? AND deleted_at IS NULL", repoID).
		Count(&pageCount).Error; err != nil {
		if isMissingTableErr(err) {
			return false, nil
		}
		return false, err
	}
	if pageCount > 0 {
		return true, nil
	}
	return s.wikiCatalogHasHead(ctx, repoID)
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
	if err := validateWikiSlug(slug); err != nil {
		return WikiPage{}, ErrNotFound
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
		if page, ok, err := s.getWikiPageFromV2(ctx, repoFullName, rep.ID, slug); err != nil {
			return WikiPage{}, err
		} else if ok {
			return page, nil
		}
	}
	if s.WikiCatalog == nil {
		return WikiPage{}, errors.New("wiki catalog unavailable")
	}
	if err := s.ensureWikiCatalogCurrent(ctx, repoFullName); err != nil {
		return WikiPage{}, err
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

	// Ref-pinned read: locate the revision by the page slug at that
	// revision plus the commit SHA pin.
	return s.getWikiPageAtRevision(ctx, rep.ID, slug, ref)
}

func (s *Service) getWikiPageFromV2(ctx context.Context, repoFullName string, repoID uint, slug string) (WikiPage, bool, error) {
	row, ok, err := s.loadCurrentWikiV2Page(ctx, repoFullName, repoID, slug)
	if err != nil || !ok {
		return WikiPage{}, ok, err
	}
	labelsBySlug, err := s.wikiLabelsForSlugs(ctx, repoID, []string{row.Slug})
	if err != nil {
		return WikiPage{}, false, err
	}

	if body, ok, err := s.wikiCurrentBodyFromCatalog(ctx, repoID, row.Slug, row.HeadBlobSHA); err != nil {
		slog.WarnContext(ctx, "wiki v2 page catalog body read failed; falling back to git", "repo", repoFullName, "slug", row.Slug, "error", err)
	} else if ok {
		return wikiPageFromV2Row(row, string(body), labelsBySlug[row.Slug]), true, nil
	}

	body, _, err := s.Git.ReadFileWithSHAAtRef(ctx, wikiRepoFullName(repoFullName), wikiSlugToPath(row.Slug), row.HeadCommitSHA)
	if err != nil {
		return WikiPage{}, false, nil
	}
	return wikiPageFromV2Row(row, string(body), labelsBySlug[row.Slug]), true, nil
}

func wikiPageFromV2Row(row db.WikiPageIndex, body string, labels []db.Label) WikiPage {
	return WikiPage{
		Slug:       row.Slug,
		Title:      row.Title,
		Body:       body,
		UpdatedAt:  row.UpdatedAt,
		SHA:        row.HeadBlobSHA,
		LastAuthor: row.LastAuthor,
		Labels:     labels,
	}
}

func (s *Service) wikiCurrentBodyFromCatalog(ctx context.Context, repoID uint, slug, expectedBlobSHA string) ([]byte, bool, error) {
	bodies, err := s.wikiCurrentBodiesFromCatalog(ctx, repoID, []string{slug}, map[string]string{slug: expectedBlobSHA})
	if err != nil {
		return nil, false, err
	}
	body, ok := bodies[slug]
	if !ok {
		return nil, false, nil
	}
	return []byte(body), true, nil
}

func (s *Service) wikiCurrentBodiesFromCatalog(ctx context.Context, repoID uint, slugs []string, expectedBlobBySlug map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(slugs))
	pagesBySlug, ok, err := s.wikiCatalogPagesBySlug(ctx, repoID, slugs)
	if err != nil || !ok {
		return out, err
	}
	for slug, page := range pagesBySlug {
		if expected := strings.TrimSpace(expectedBlobBySlug[slug]); expected != "" && !strings.EqualFold(page.HeadBlobSHA, expected) {
			continue
		}
		body, err := s.wikiPageBody(ctx, page)
		if err != nil {
			return nil, err
		}
		out[slug] = string(body)
	}
	return out, nil
}

func (s *Service) loadCurrentWikiV2Page(ctx context.Context, repoFullName string, repoID uint, slug string) (db.WikiPageIndex, bool, error) {
	headSHA, ok, err := s.loadCurrentWikiV2HeadSHA(ctx, repoFullName, repoID)
	if err != nil || !ok {
		return db.WikiPageIndex{}, false, err
	}
	var row db.WikiPageIndex
	if err := s.DBForCtx(ctx).
		Preload("LastAuthor").
		Where("repository_id = ? AND slug = ? AND LOWER(head_commit_sha) = LOWER(?)", repoID, slug, headSHA).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return db.WikiPageIndex{}, false, nil
		}
		return db.WikiPageIndex{}, false, err
	}
	return row, true, nil
}

func (s *Service) loadCurrentWikiV2Rows(ctx context.Context, repoFullName string, repoID uint) ([]db.WikiPageIndex, bool, error) {
	headSHA, ok, err := s.loadCurrentWikiV2HeadSHA(ctx, repoFullName, repoID)
	if err != nil || !ok {
		return nil, false, err
	}
	var rows []db.WikiPageIndex
	if err := s.DBForCtx(ctx).
		Preload("LastAuthor").
		Where("repository_id = ? AND LOWER(head_commit_sha) = LOWER(?)", repoID, headSHA).
		Find(&rows).Error; err != nil {
		return nil, false, err
	}
	return rows, true, nil
}

func (s *Service) listWikiPageHistoryFromV2(ctx context.Context, repoFullName string, repoID uint, slug string, page, perPage int) ([]WikiPageHistoryEntry, int, bool, error) {
	if _, ok, err := s.loadCurrentWikiV2Page(ctx, repoFullName, repoID, slug); err != nil || !ok {
		return nil, 0, ok, err
	}
	var total int64
	if err := s.DBForCtx(ctx).Model(&db.WikiPageHistory{}).
		Where("repository_id = ? AND slug = ?", repoID, slug).
		Count(&total).Error; err != nil {
		return nil, 0, false, err
	}
	if total == 0 {
		return nil, 0, false, nil
	}
	var missingSequenceCount int64
	if err := s.DBForCtx(ctx).Model(&db.WikiPageHistory{}).
		Where("repository_id = ? AND slug = ?", repoID, slug).
		Where("path_sequence <= 0").
		Count(&missingSequenceCount).Error; err != nil {
		return nil, 0, false, err
	}
	if missingSequenceCount > 0 {
		return nil, 0, false, nil
	}
	var pageRow db.WikiPage
	err := s.DBForCtx(ctx).Unscoped().
		Select("page_id").
		Where("repository_id = ? AND slug = ?", repoID, slug).
		Take(&pageRow).Error
	switch {
	case err == nil:
		var legacyTotal int64
		if err := s.DBForCtx(ctx).Model(&db.WikiPageRevision{}).
			Where("page_id = ? AND superseded_by_changeset_id IS NULL", pageRow.PageID).
			Count(&legacyTotal).Error; err != nil {
			return nil, 0, false, err
		}
		if legacyTotal > 0 && legacyTotal != total {
			return nil, 0, false, nil
		}
	case errors.Is(err, gorm.ErrRecordNotFound):
	default:
		return nil, 0, false, err
	}
	if page < 1 {
		page = 1
	}
	if perPage <= 0 {
		perPage = 30
	}
	offset := (page - 1) * perPage
	var rows []db.WikiPageHistory
	query := s.DBForCtx(ctx).
		Preload("Author").
		Preload("Committer").
		Where("repository_id = ? AND slug = ?", repoID, slug)
	if err := query.
		Order("committed_at desc, path_sequence desc, commit_sha desc").
		Offset(offset).Limit(perPage).
		Find(&rows).Error; err != nil {
		return nil, 0, false, err
	}
	if len(rows) == 0 {
		return []WikiPageHistoryEntry{}, int(total), true, nil
	}
	out := make([]WikiPageHistoryEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, WikiPageHistoryEntry{
			SHA:       row.CommitSHA,
			Message:   row.Message,
			Author:    row.Author,
			Committer: row.Committer,
			Date:      row.CommittedAt,
			BodySize:  row.BodySize,
		})
	}
	return out, int(total), true, nil
}

func (s *Service) listWikiBacklinksFromV2(ctx context.Context, repoFullName string, repoID uint, slug string) ([]WikiBacklink, bool, error) {
	headSHA, ok, err := s.loadCurrentWikiV2HeadSHA(ctx, repoFullName, repoID)
	if err != nil || !ok {
		return nil, ok, err
	}
	var state db.WikiIndexState
	if err := s.DBForCtx(ctx).Select("backlinks_indexed_sha").First(&state, "repository_id = ?", repoID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || isMissingTableErr(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !strings.EqualFold(strings.TrimSpace(state.BacklinksIndexedSHA), strings.TrimSpace(headSHA)) {
		return nil, false, nil
	}
	rows, ok, err := s.loadCurrentWikiV2Rows(ctx, repoFullName, repoID)
	if err != nil || !ok {
		return nil, ok, err
	}
	pages := make(map[string]struct{}, len(rows))
	topLevelPages := make(map[string]struct{}, len(rows))
	rowsBySlug := make(map[string]db.WikiPageIndex, len(rows))
	for _, row := range rows {
		rowsBySlug[row.Slug] = row
		pages[row.Slug] = struct{}{}
		if !strings.Contains(row.Slug, "/") {
			topLevelPages[row.Slug] = struct{}{}
		}
	}
	if _, exists := pages[slug]; !exists {
		return nil, false, nil
	}
	var links []db.WikiBacklink
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND dst_slug = ? AND resolved = ?", repoID, slug, true).
		Order("src_slug asc").
		Find(&links).Error; err != nil {
		return nil, false, err
	}
	full := wikiRepoFullName(repoFullName)
	srcSlugs := make([]string, 0, len(links))
	expectedBlobBySlug := make(map[string]string, len(links))
	for _, link := range links {
		srcSlugs = append(srcSlugs, link.SrcSlug)
		if row, ok := rowsBySlug[link.SrcSlug]; ok {
			expectedBlobBySlug[link.SrcSlug] = row.HeadBlobSHA
		}
	}
	catalogBodies, err := s.wikiCurrentBodiesFromCatalog(ctx, repoID, srcSlugs, expectedBlobBySlug)
	if err != nil {
		slog.WarnContext(ctx, "wiki backlinks catalog body read failed; falling back to git bodies", "repo", repoFullName, "error", err)
		catalogBodies = map[string]string{}
	}
	backlinks := make([]WikiBacklink, 0, len(links))
	for _, link := range links {
		body, ok := catalogBodies[link.SrcSlug]
		if !ok {
			bodyBytes, err := s.Git.ReadFileAtRef(ctx, full, wikiSlugToPath(link.SrcSlug), headSHA)
			if err != nil {
				continue
			}
			body = string(bodyBytes)
		}
		snippet := ""
		for _, match := range extractWikiLinkMatches(body) {
			resolvedTarget, ok := resolveWikiBacklinkTarget(match, pages, topLevelPages)
			if ok && resolvedTarget == slug {
				snippet = match.snippet
				break
			}
		}
		backlinks = append(backlinks, WikiBacklink{
			Slug:    link.SrcSlug,
			Title:   titleFromSlug(link.SrcSlug),
			Snippet: snippet,
		})
	}
	return backlinks, true, nil
}

func (s *Service) loadCurrentWikiV2HeadSHA(ctx context.Context, repoFullName string, repoID uint) (string, bool, error) {
	if s.Git == nil {
		return "", false, nil
	}
	full := wikiRepoFullName(repoFullName)
	if !s.Git.Exists(ctx, full) || s.Git.IsEmpty(ctx, full) {
		return "", false, nil
	}
	liveHeadSHA, err := s.Git.ResolveContentCommit(ctx, full, wikiDefaultBranch)
	if err != nil {
		return "", false, err
	}
	var state db.WikiIndexState
	if err := s.DBForCtx(ctx).First(&state, "repository_id = ?", repoID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		if isMissingTableErr(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if !strings.EqualFold(strings.TrimSpace(state.IndexedCommitSHA), strings.TrimSpace(liveHeadSHA)) {
		return "", false, nil
	}
	return liveHeadSHA, true, nil
}

// getWikiPageAtRevision returns a single page projected from the
// wiki_page_revisions row whose commit SHA matches ref and whose
// page_id maps to the requested slug. Returns ErrNotFound when the
// slug was not present at that revision.
func (s *Service) getWikiPageAtRevision(ctx context.Context, repoID uint, slug, ref string) (WikiPage, error) {
	if err := validateWikiSlug(slug); err != nil {
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
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return nil, 0, err
	}
	if history, total, ok, err := s.listWikiPageHistoryFromV2(ctx, repoFullName, rep.ID, slug, page, perPage); err != nil {
		return nil, 0, err
	} else if ok {
		return history, total, nil
	}
	if s.WikiCatalog == nil {
		return nil, 0, errors.New("wiki catalog unavailable")
	}
	if err := s.ensureWikiCatalogCurrent(ctx, repoFullName); err != nil {
		return nil, 0, err
	}
	// Locate the page id, including soft-deleted pages — history is
	// kept around even after a delete so the catalog still has a
	// truthful revision chain to project.
	if err := validateWikiSlug(slug); err != nil {
		return nil, 0, ErrNotFound
	}
	var pageRow db.WikiPage
	if err := s.DBForCtx(ctx).Unscoped().
		Where("repository_id = ? AND slug = ?", rep.ID, slug).
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
	if err := validateWikiSlug(slug); err != nil {
		return nil, ErrNotFound
	}
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	if s.WikiCatalog != nil {
		if err := s.ensureWikiCatalogCurrent(ctx, repoFullName); err != nil {
			slog.WarnContext(ctx, "wiki backlinks catalog refresh failed", "repo", repoFullName, "error", err)
		}
		hasCatalogState, stateErr := s.wikiCatalogHasLiveState(ctx, rep.ID)
		if stateErr != nil {
			return nil, stateErr
		}
		if hasCatalogState {
			backlinks, ok, err := s.loadWikiBacklinksFromCatalog(ctx, rep.ID, slug)
			if err != nil {
				return nil, err
			}
			if ok {
				return backlinks, nil
			}
			return nil, ErrNotFound
		}
	}
	if backlinks, ok, err := s.listWikiBacklinksFromV2(ctx, repoFullName, rep.ID, slug); err != nil {
		return nil, err
	} else if ok {
		return backlinks, nil
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
	ctx, timing := withWikiWriteTiming(ctx, "put")
	started := time.Now()
	recordWikiWriteValue(ctx, wikiWriteValueBodyBytes, int64(len(body)))
	defer func() {
		recordWikiWriteDuration(ctx, wikiWritePhaseTotal, time.Since(started))
		timing.flush(ctx)
	}()

	if err := validateWikiSlug(slug); err != nil {
		return WikiPage{}, err
	}
	if s.WikiCatalog == nil {
		return WikiPage{}, errors.New("wiki catalog unavailable")
	}
	if s.Git == nil {
		return WikiPage{}, errors.New("git store unavailable")
	}
	catalog := s.WikiCatalog
	git := s.Git
	stageStarted := time.Now()
	rep, err := s.getRepoBase(ctx, repoFullName)
	observeWikiWritePhase(ctx, wikiWritePhaseRepoLookup, stageStarted)
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
	authorID := s.resolveWikiAuthor(ctx)
	var result wikicatalog.ChangeSetResult
	var postCommit *wikiPostCommitWaiter
	result, postCommit, err = s.applyWikiRESTChangeSetWithLocks(
		ctx,
		git,
		catalog,
		rep,
		wikicatalog.ChangeSetRequest{
			RepositoryID: rep.ID,
			AuthorID:     authorID,
			Source:       wikicatalog.SourceREST,
			Message:      message,
			Changes:      []wikicatalog.Change{change},
		},
	)
	if err == nil {
		stageStarted = time.Now()
		err = postCommit.wait()
		observeWikiWritePhase(ctx, wikiWritePhasePostCommitWait, stageStarted)
	}
	if err != nil {
		return WikiPage{}, s.translateCatalogError(ctx, rep.ID, repoFullName, err, false)
	}
	stageStarted = time.Now()
	written := result.Changes[0]
	var writtenLabels []db.Label
	switch written.UpsertDisposition {
	case wikicatalog.UpsertDispositionCreate, wikicatalog.UpsertDispositionRestore:
		// New and restored pages cannot have labels: create has no prior row,
		// and delete clears label links in the catalog transaction.
	default:
		labels, labelErr := s.wikiLabelsForSlugs(ctx, rep.ID, []string{written.Slug})
		if labelErr != nil {
			err = labelErr
		} else {
			writtenLabels = labels[written.Slug]
		}
	}
	observeWikiWritePhase(ctx, wikiWritePhaseLabels, stageStarted)
	if err != nil {
		return WikiPage{}, err
	}

	var lastAuthor *db.User
	if authorID != nil {
		if user, ok := UserFromContext(ctx); ok && user.ID == *authorID {
			userCopy := user
			lastAuthor = &userCopy
		} else {
			var author db.User
			if err := s.DBForCtx(ctx).First(&author, "id = ?", *authorID).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return WikiPage{}, err
			} else if err == nil {
				lastAuthor = &author
			}
		}
	}
	return WikiPage{
		Slug:       written.Slug,
		Title:      wikicatalog.TitleFromSlug(written.Slug),
		Body:       body,
		UpdatedAt:  result.CommittedAt,
		SHA:        written.BlobSHA,
		LastAuthor: lastAuthor,
		Labels:     writtenLabels,
	}, nil
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
	if s.Git == nil {
		return errors.New("git store unavailable")
	}
	catalog := s.WikiCatalog
	git := s.Git
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return err
	}
	if message == "" {
		message = "Delete " + slug
	}
	_, postCommit, err := s.applyWikiRESTChangeSetWithLocks(
		ctx,
		git,
		catalog,
		rep,
		wikicatalog.ChangeSetRequest{
			RepositoryID: rep.ID,
			AuthorID:     s.resolveWikiAuthor(ctx),
			Source:       wikicatalog.SourceREST,
			Message:      message,
			Changes:      []wikicatalog.Change{{Op: wikicatalog.OpDelete, Slug: slug}},
		},
	)
	if err == nil {
		err = postCommit.wait()
	}
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
	if s.Git == nil {
		return WikiMoveResult{}, errors.New("git store unavailable")
	}
	catalog := s.WikiCatalog
	git := s.Git
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

	_, postCommit, err := s.applyWikiRESTChangeSetWithLocks(
		ctx,
		git,
		catalog,
		rep,
		wikicatalog.ChangeSetRequest{
			RepositoryID: rep.ID,
			AuthorID:     s.resolveWikiAuthor(ctx),
			Source:       wikicatalog.SourceREST,
			Message:      commitMessage,
			Changes:      changes,
		},
	)
	if err == nil {
		err = postCommit.wait()
	}
	if err != nil {
		return WikiMoveResult{}, s.translateCatalogError(ctx, rep.ID, repoFullName, err, true)
	}
	labelProjectionCount, err := s.moveWikiPageLabels(ctx, rep.ID, map[string]string{slug: newSlug})
	if err != nil {
		return WikiMoveResult{}, err
	}
	s.kickWikiSearchProjection(ctx, labelProjectionCount)

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
	if err := validateWikiSlug(oldSlug); err != nil {
		return nil, nil, err
	}
	var linkerIDs []uint64
	if err := s.DBForCtx(ctx).Model(&db.WikiPageLink{}).
		Where("repository_id = ? AND dst_slug = ?", repoID, oldSlug).
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
	slugs = uniqueStrings(slugs)
	var pages []db.WikiPage
	if err := s.DBForCtx(ctx).
		Select("page_id", "repository_id", "slug", "head_blob_sha", "body_size", "last_author_id", "updated_at").
		Preload("LastAuthor").
		Where("repository_id = ? AND slug IN ? AND deleted_at IS NULL", repoID, slugs).
		Find(&pages).Error; err != nil {
		return nil, err
	}
	out := make([]WikiPageSummary, 0, len(pages))
	for _, p := range pages {
		out = append(out, WikiPageSummary{
			Slug:       p.Slug,
			Title:      wikicatalog.TitleFromSlug(p.Slug),
			SHA:        p.HeadBlobSHA,
			Size:       int64(p.BodySize),
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
	if s.WikiCatalog == nil {
		return WikiBulkMoveResult{}, errors.New("wiki catalog unavailable")
	}
	if s.Git == nil {
		return WikiBulkMoveResult{}, errors.New("git store unavailable")
	}
	catalog := s.WikiCatalog
	git := s.Git
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
	var postCommit *wikiPostCommitWaiter
	applyResult, postCommit, err = s.applyWikiRESTChangeSetWithLocks(
		ctx,
		git,
		catalog,
		rep,
		wikicatalog.ChangeSetRequest{
			RepositoryID: rep.ID,
			AuthorID:     s.resolveWikiAuthor(ctx),
			Source:       wikicatalog.SourceREST,
			Message:      commitMessage,
			Changes:      changes,
		},
	)
	if err == nil {
		err = postCommit.wait()
	}
	if err != nil {
		return WikiBulkMoveResult{}, s.translateCatalogError(ctx, rep.ID, repoFullName, err, true)
	}
	labelRemaps := make(map[string]string, len(moved))
	for _, mv := range moved {
		labelRemaps[mv.From] = mv.To
	}
	labelProjectionCount, err := s.moveWikiPageLabels(ctx, rep.ID, labelRemaps)
	if err != nil {
		return WikiBulkMoveResult{}, err
	}

	s.invalidateWikiBacklinks(repoFullName)

	labelLookupSlugs := make([]string, 0, len(moved)+len(rewriteSlugs))
	for _, mv := range moved {
		labelLookupSlugs = append(labelLookupSlugs, mv.To)
	}
	labelLookupSlugs = append(labelLookupSlugs, rewriteSlugs...)
	s.kickWikiSearchProjection(ctx, labelProjectionCount)
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
// equals from or starts with from/.
func (s *Service) findWikiBulkMoveSources(ctx context.Context, repoID uint, from string) ([]string, map[string]db.WikiPage, error) {
	if err := validateWikiSlug(from); err != nil {
		return nil, nil, err
	}
	var pages []db.WikiPage
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND deleted_at IS NULL AND (slug = ? OR slug LIKE ?)",
			repoID, from, from+"/%").
		Find(&pages).Error; err != nil {
		return nil, nil, err
	}
	slugs := make([]string, 0, len(pages))
	bySlug := make(map[string]db.WikiPage, len(pages))
	for _, p := range pages {
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
