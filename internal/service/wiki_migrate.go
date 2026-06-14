package service

// Wiki migration tool: replay the legacy git-backed wiki repos into
// the wikicatalog so the cutover can serve all traffic from the
// catalog. Designed for a single maintenance-window pass over each
// repo with has_wiki=true; idempotent and resumable per repo, since
// each commit's original SHA is preserved as wiki_changesets.synth_
// commit_sha and migration skips commits already in the catalog.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	applog "github.com/ngaut/agent-git-service/internal/logging"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

// WikiMigrationOptions tunes a migration run.
type WikiMigrationOptions struct {
	// SkipIncompatibleSlugs, when true, drops pages whose slug cannot
	// be represented by the catalog's single slug grammar.
	SkipIncompatibleSlugs bool
}

// MigrateAllWikis replays every legacy wiki bare repo into the wiki
// catalog. Run during a maintenance window after AutoMigrate has
// created the wiki_* tables and before flipping REST traffic onto
// the catalog-backed read/write paths.
//
// Idempotent: each commit lands keyed by its original git SHA, so
// reruns skip commits already present in wiki_changesets.
//
// Returns the per-repo replay counts, plus the first error if any
// repo failed. Successful repos retain their progress regardless of
// later failures.
func (s *Service) MigrateAllWikis(ctx context.Context, opts WikiMigrationOptions) (MigrationReport, error) {
	report := MigrationReport{ByRepo: map[string]RepoMigrationStats{}}
	if s.Git == nil {
		return report, errors.New("wiki migration: git store unavailable")
	}
	if s.WikiCatalog == nil {
		return report, errors.New("wiki migration: catalog unavailable")
	}

	var repos []db.Repository
	if err := s.DBForCtx(ctx).
		Where("has_wiki = ?", true).
		Order("id ASC").
		Find(&repos).Error; err != nil {
		return report, fmt.Errorf("wiki migration: list repos: %w", err)
	}

	for _, repo := range repos {
		stats, err := s.migrateOneWiki(ctx, repo, opts)
		report.ByRepo[repo.FullName] = stats
		if err != nil && report.FirstError == nil {
			report.FirstError = fmt.Errorf("repo %q: %w", repo.FullName, err)
		}
	}
	return report, report.FirstError
}

// MigrationReport summarizes a migration run.
type MigrationReport struct {
	ByRepo     map[string]RepoMigrationStats
	FirstError error
}

// RepoMigrationStats captures what landed for one repo.
type RepoMigrationStats struct {
	GitCommits   int // total commits in the legacy wiki repo
	NewCommits   int // commits applied during this run
	SkippedExist int // commits already in the catalog (resume path)
	Pages        int // catalog pages currently visible for the repo
}

// MigrateWiki replays a single repo by full name. Useful for
// targeted reruns; MigrateAllWikis is the production entry point.
func (s *Service) MigrateWiki(ctx context.Context, repoFullName string, opts WikiMigrationOptions) (RepoMigrationStats, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return RepoMigrationStats{}, err
	}
	return s.migrateOneWiki(ctx, rep, opts)
}

// ensureWikiCatalogCurrent is the read-path freshness hook for the
// wiki catalog. It treats the wikicatalog tables as a materialized
// view of the legacy git wiki repo: before serving a read, this
// function compares the wiki repo's visible content branch
// (`wikiDefaultBranch`, matching GitHub wiki semantics) against the
// last migrated commit recorded in the catalog and replays only the
// new commits if they diverge. The fast path is one Git lookup plus
// one indexed catalog query.
//
// This sits in front of catalog-backed read handlers while writes
// still flow through the legacy git path; M4 (catalog-as-SOT writes)
// will make this hook redundant.
func (s *Service) ensureWikiCatalogCurrent(ctx context.Context, repoFullName string) error {
	if s.Git == nil || s.WikiCatalog == nil {
		return nil
	}
	full := wikiRepoFullName(repoFullName)
	if !s.Git.Exists(ctx, full) {
		return nil
	}
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return err
	}
	last, err := s.loadLatestWikiChangesetState(ctx, rep.ID)
	if err != nil {
		return fmt.Errorf("read catalog head for %q: %w", repoFullName, err)
	}
	lastMigratedSHA := last.CommitSHA
	headSHA, err := s.Git.ResolveContentCommit(ctx, full, wikiDefaultBranch)
	if err != nil || strings.TrimSpace(headSHA) == "" {
		branches, branchErr := s.Git.ListBranches(ctx, full)
		if branchErr != nil {
			if err != nil {
				return fmt.Errorf("resolve wiki content commit for %q: %w", repoFullName, err)
			}
			return fmt.Errorf("list wiki branches for %q: %w", repoFullName, branchErr)
		}
		if len(branches) == 0 {
			if lastMigratedSHA != "" && last.allowGitBackfillReset() {
				if err := s.resetWikiCatalogRepo(ctx, rep.ID); err != nil {
					return fmt.Errorf("reset empty wiki catalog for %q: %w", repoFullName, err)
				}
				if err := s.pruneWikiPageLabelsForMissingPages(ctx, rep.ID); err != nil {
					return fmt.Errorf("prune wiki page labels for empty wiki %q: %w", repoFullName, err)
				}
			}
			return nil
		}
		hasVisibleBranch := false
		for _, branch := range branches {
			if branch.Name == wikiDefaultBranch {
				hasVisibleBranch = true
				break
			}
		}
		if !hasVisibleBranch {
			if lastMigratedSHA != "" && last.allowGitBackfillReset() {
				if err := s.resetWikiCatalogRepo(ctx, rep.ID); err != nil {
					return fmt.Errorf("reset catalog without %s for %q: %w", wikiDefaultBranch, repoFullName, err)
				}
				if err := s.pruneWikiPageLabelsForMissingPages(ctx, rep.ID); err != nil {
					return fmt.Errorf("prune wiki page labels without %s for %q: %w", wikiDefaultBranch, repoFullName, err)
				}
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("resolve wiki content commit for %q: %w", repoFullName, err)
		}
		return fmt.Errorf("resolve wiki content commit for %q: empty commit with %d branches", repoFullName, len(branches))
	}
	if strings.EqualFold(lastMigratedSHA, strings.ToLower(strings.TrimSpace(headSHA))) {
		return nil
	}
	if lastMigratedSHA != "" && !last.allowGitBackfillReset() {
		return nil
	}
	s.kickBackgroundWikiMigration(ctx, rep)
	return nil
}

// KickBackgroundWikiMigration schedules an asynchronous repo-scoped wiki
// migration using the caller context for repo identity lookup and the server
// lifecycle context for the background worker. Only one background migration
// per repo runs at a time.
func (s *Service) KickBackgroundWikiMigration(ctx context.Context, repoFullName string) {
	if s.Git == nil || s.WikiCatalog == nil {
		return
	}
	if ctx == nil {
		ctx = s.ServerCtx()
	}
	rep, err := s.LookupRepoIdentity(ctx, repoFullName)
	if err != nil || rep.ID == 0 {
		return
	}
	s.kickBackgroundWikiMigration(ctx, rep)
}

// IsWikiBackgroundMigrationRunning reports whether a repo currently has a
// background wiki migration in flight.
func (s *Service) IsWikiBackgroundMigrationRunning(ctx context.Context, repoFullName string) bool {
	rep, err := s.LookupRepoIdentity(ctx, repoFullName)
	if err != nil || rep.ID == 0 {
		return false
	}
	return s.isWikiBackgroundMigrationRunning(s.wikiRepoKey(ctx, rep))
}

func (s *Service) kickBackgroundWikiMigration(ctx context.Context, repo db.Repository) {
	key := s.wikiRepoKey(ctx, repo)
	if !s.claimWikiBackgroundMigration(key) {
		return
	}
	if s.testWikiBackgroundMigrationStarted != nil {
		s.testWikiBackgroundMigrationStarted(repo.FullName)
	}

	bgCtx := applog.CloneContext(s.ServerCtx(), ctx)
	if scopedDB, ok := DBFromContext(ctx); ok {
		bgCtx = ContextWithDB(bgCtx, scopedDB)
	}
	if user, ok := UserFromContext(ctx); ok {
		bgCtx = ContextWithUser(bgCtx, user)
	}
	s.Wg.Add(1)
	go func() {
		defer s.Wg.Done()
		defer s.releaseWikiBackgroundMigration(key)

		if _, err := s.migrateOneWiki(bgCtx, repo, WikiMigrationOptions{}); err != nil {
			slog.ErrorContext(bgCtx, "background wiki migration failed", "repo", repo.FullName, "error", err)
		}
	}()
}

func (s *Service) migrateOneWiki(ctx context.Context, repo db.Repository, opts WikiMigrationOptions) (RepoMigrationStats, error) {
	stats := RepoMigrationStats{}
	full := wikiRepoFullName(repo.FullName)
	if !s.Git.Exists(ctx, full) {
		return stats, nil
	}

	mu := s.getWikiMigrationSyncMu(s.wikiRepoKey(ctx, repo))
	mu.Lock()
	defer mu.Unlock()

	commits, err := s.Git.ListAllCommits(ctx, full, nil)
	if err != nil {
		return stats, fmt.Errorf("list commits: %w", err)
	}
	stats.GitCommits = len(commits)

	last, err := s.loadLatestWikiChangesetState(ctx, repo.ID)
	if err != nil {
		return stats, fmt.Errorf("load latest migrated SHA: %w", err)
	}
	lastMigratedSHA := last.CommitSHA
	didReset := false
	if lastMigratedSHA != "" && last.allowGitBackfillReset() && !wikiCommitInHistory(commits, lastMigratedSHA) {
		if err := s.resetWikiCatalogRepo(ctx, repo.ID); err != nil {
			return stats, fmt.Errorf("reset rewritten wiki catalog: %w", err)
		}
		didReset = true
	}

	// Already-present commit SHAs short-circuit the replay so reruns
	// after a partial migration only do new work.
	existing, err := s.loadMigratedCommitSHAs(ctx, repo.ID)
	if err != nil {
		return stats, fmt.Errorf("load migrated SHAs: %w", err)
	}
	if s.testWikiMigrationAfterSnapshot != nil {
		s.testWikiMigrationAfterSnapshot(repo.FullName)
	}

	// ListAllCommits returns commits in git log's natural order, which
	// is reverse-topological — every commit appears before its parents.
	// Plain reverse gives the chronological order we need so each
	// commit's parent has already landed in the catalog when we
	// process it. A date sort is wrong: real wiki workloads commonly
	// produce multiple commits within the same second (bulk imports,
	// rapid REST writes) and a date sort cannot break those ties.
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}

	// Per-run caches so we avoid re-reading the same parent tree
	// twice (parent of commit N is current of commit N-1) and
	// re-resolving the same git author N times.
	st := &replayState{
		authors: map[string]*uint{},
	}
	for _, commit := range commits {
		if _, ok := existing[commit.SHA]; ok {
			stats.SkippedExist++
			continue
		}
		if err := s.replayCommitIntoCatalog(ctx, full, repo, commit, opts, st); err != nil {
			return stats, fmt.Errorf("commit %s: %w", commit.SHA, err)
		}
		stats.NewCommits++
		existing[commit.SHA] = struct{}{}
	}
	if didReset {
		if err := s.pruneWikiPageLabelsForMissingPages(ctx, repo.ID); err != nil {
			return stats, fmt.Errorf("prune wiki page labels after reset: %w", err)
		}
	}

	pageCount := int64(0)
	_ = s.DBForCtx(ctx).Model(&db.WikiPage{}).
		Where("repository_id = ? AND deleted_at IS NULL", repo.ID).
		Count(&pageCount)
	stats.Pages = int(pageCount)

	slog.InfoContext(ctx, "wiki migration: repo done",
		"repo", repo.FullName,
		"git_commits", stats.GitCommits,
		"new", stats.NewCommits,
		"skipped", stats.SkippedExist,
		"pages", stats.Pages,
	)
	return stats, nil
}

type wikiChangesetState struct {
	CommitSHA      string
	Source         wikicatalog.Source
	SynthFormatVer int16
}

func (s *Service) loadLatestWikiChangesetState(ctx context.Context, repoID uint) (wikiChangesetState, error) {
	var last db.WikiChangeset
	err := s.DBForCtx(ctx).
		Select("synth_commit_sha", "source", "synth_format_ver").
		Where("repository_id = ?", repoID).
		Order("changeset_id DESC").
		Limit(1).
		Take(&last).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return wikiChangesetState{}, nil
	}
	if err != nil {
		return wikiChangesetState{}, err
	}
	return wikiChangesetState{
		CommitSHA:      strings.ToLower(strings.TrimSpace(last.SynthCommitSHA)),
		Source:         wikicatalog.Source(strings.TrimSpace(last.Source)),
		SynthFormatVer: last.SynthFormatVer,
	}, nil
}

func (s wikiChangesetState) allowGitBackfillReset() bool {
	if s.Source == wikicatalog.SourceCompact {
		return false
	}
	return s.SynthFormatVer >= synthProjectionMaterialized
}

func wikiCommitInHistory(commits []gitstore.SearchCommitInfo, sha string) bool {
	sha = strings.ToLower(strings.TrimSpace(sha))
	if sha == "" {
		return false
	}
	for _, commit := range commits {
		if strings.EqualFold(strings.TrimSpace(commit.SHA), sha) {
			return true
		}
	}
	return false
}

func (s *Service) resetWikiCatalogRepo(ctx context.Context, repoID uint) error {
	return s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		type blobRefCount struct {
			BlobSHA  string
			Refcount int64
		}
		var liveRefs []blobRefCount
		if err := tx.Model(&db.WikiPage{}).
			Select("head_blob_sha AS blob_sha, COUNT(*) AS refcount").
			Where("repository_id = ? AND deleted_at IS NULL", repoID).
			Group("head_blob_sha").
			Scan(&liveRefs).Error; err != nil {
			return fmt.Errorf("load live blob refs: %w", err)
		}
		for _, ref := range liveRefs {
			if strings.TrimSpace(ref.BlobSHA) == "" || ref.Refcount <= 0 {
				continue
			}
			if err := tx.Model(&db.WikiBlobRef{}).
				Where("blob_sha = ?", ref.BlobSHA).
				UpdateColumn("refcount", gorm.Expr("refcount - ?", ref.Refcount)).Error; err != nil {
				return fmt.Errorf("decrement blob ref %s: %w", ref.BlobSHA, err)
			}
		}

		pageIDs := tx.Model(&db.WikiPage{}).
			Select("page_id").
			Where("repository_id = ?", repoID)

		if err := tx.Where("repository_id = ?", repoID).Delete(&db.WikiSearchDocument{}).Error; err != nil {
			return fmt.Errorf("delete wiki search documents: %w", err)
		}
		if err := tx.Where("repository_id = ?", repoID).Delete(&db.WikiPageLink{}).Error; err != nil {
			return fmt.Errorf("delete wiki page links: %w", err)
		}
		if err := tx.Where("repository_id = ?", repoID).Delete(&db.WikiDirIndex{}).Error; err != nil {
			return fmt.Errorf("delete wiki dir index: %w", err)
		}
		if err := tx.Where("page_id IN (?)", pageIDs).Delete(&db.WikiPageRevision{}).Error; err != nil {
			return fmt.Errorf("delete wiki page revisions: %w", err)
		}
		if err := tx.Where("repository_id = ?", repoID).Delete(&db.WikiPage{}).Error; err != nil {
			return fmt.Errorf("delete wiki pages: %w", err)
		}
		if err := tx.Where("repository_id = ?", repoID).Delete(&db.WikiRepoHead{}).Error; err != nil {
			return fmt.Errorf("delete wiki repo head: %w", err)
		}
		if err := tx.Where("repository_id = ?", repoID).Delete(&db.WikiChangeset{}).Error; err != nil {
			return fmt.Errorf("delete wiki changesets: %w", err)
		}
		return nil
	})
}

func (s *Service) pruneWikiPageLabelsForMissingPages(ctx context.Context, repoID uint) error {
	return s.DBForCtx(ctx).Where(
		"repository_id = ? AND slug NOT IN (?)",
		repoID,
		s.DBForCtx(ctx).Model(&db.WikiPage{}).Select("slug").Where("repository_id = ? AND deleted_at IS NULL", repoID),
	).Delete(&db.WikiPageLabel{}).Error
}

func (s *Service) loadMigratedCommitSHAs(ctx context.Context, repoID uint) (map[string]struct{}, error) {
	var existing []db.WikiChangeset
	err := s.DBForCtx(ctx).
		Select("synth_commit_sha").
		Where("repository_id = ?", repoID).
		Find(&existing).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(existing))
	for _, cs := range existing {
		out[strings.ToLower(cs.SynthCommitSHA)] = struct{}{}
	}
	return out, nil
}

// replayCommitIntoCatalog computes the diff between commit and its
// first parent (or the empty tree for a root commit), constructs the
// corresponding wikicatalog.ChangeSetRequest, and applies it.
//
// Rename detection is intentionally not performed: the legacy code
// recorded renames as delete-of-old + add-of-new in a single commit,
// which the catalog replays as two changes. Page identity (page_id)
// is allocated fresh at the moment of the create, so historical
// rename chains lose page-id continuity — but since page_id is an
// internal identifier and never exposed by the legacy REST API,
// nothing observable changes.
// replayState carries forward per-run caches so migration does
// O(N) git tree reads instead of O(N) duplicated reads (parent of
// commit i equals current of commit i-1) and O(unique-authors)
// user lookups instead of O(N).
type replayState struct {
	prevSHA   string
	prevPaths []string
	prevBlobs map[string]string
	authors   map[string]*uint
}

func (s *Service) replayCommitIntoCatalog(ctx context.Context, full string, repo db.Repository, commit gitstore.SearchCommitInfo, opts WikiMigrationOptions, st *replayState) error {
	// Wiki repos should hold linear history — they're written by REST
	// handlers committing one file change at a time. A merge commit
	// here means either a manual git push of an unusual workflow or a
	// gitstore bug. The diff-against-first-parent strategy used below
	// would silently drop a second-parent branch's content, so refuse
	// instead. Operators can resolve by squashing in source git
	// before migration.
	if len(commit.ParentSHAs) > 1 {
		return fmt.Errorf("commit %s has %d parents; wiki history must be linear before migration", commit.SHA, len(commit.ParentSHAs))
	}
	parent := ""
	if len(commit.ParentSHAs) == 1 {
		parent = commit.ParentSHAs[0]
	}

	// Reuse the previous commit's tree if the chain is linear (the
	// common case) — its current state equals this commit's parent.
	// Otherwise read fresh.
	var (
		parPaths []string
		parBlobs map[string]string
		err      error
	)
	if parent != "" {
		if st.prevSHA == parent {
			parPaths = st.prevPaths
			parBlobs = st.prevBlobs
		} else {
			parPaths, err = s.Git.ListTreeFilesAtRef(ctx, full, parent)
			if err != nil {
				return fmt.Errorf("list tree at parent %s: %w", parent, err)
			}
			parBlobs, err = s.Git.BlobSHAs(ctx, full, parent, parPaths)
			if err != nil {
				return fmt.Errorf("blob SHAs at parent %s: %w", parent, err)
			}
		}
	}

	curPaths, err := s.Git.ListTreeFilesAtRef(ctx, full, commit.SHA)
	if err != nil {
		return fmt.Errorf("list tree at %s: %w", commit.SHA, err)
	}
	curBlobs, err := s.Git.BlobSHAs(ctx, full, commit.SHA, curPaths)
	if err != nil {
		return fmt.Errorf("blob SHAs at %s: %w", commit.SHA, err)
	}

	changes, err := s.diffToChanges(ctx, full, commit.SHA, curPaths, curBlobs, parBlobs, opts)
	if err != nil {
		return err
	}
	committedAt, err := parseCommitTime(commit.CommitterDate, commit.Date)
	if err != nil {
		return fmt.Errorf("parse commit time for %s: %w", commit.SHA, err)
	}
	authorID := s.resolveAuthorForMigrationCached(ctx, commit, st)

	req := wikicatalog.ChangeSetRequest{
		RepositoryID:        repo.ID,
		AuthorID:            authorID,
		Source:              wikicatalog.SourceMigration,
		Message:             commit.Message,
		Changes:             changes,
		OverrideCommitSHA:   strings.ToLower(strings.TrimSpace(commit.SHA)),
		OverrideCommittedAt: &committedAt,
	}
	if _, err := s.WikiCatalog.ApplyChangeSet(ctx, req); err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	st.prevSHA = commit.SHA
	st.prevPaths = curPaths
	st.prevBlobs = curBlobs
	return nil
}

func (s *Service) resolveAuthorForMigrationCached(ctx context.Context, commit gitstore.SearchCommitInfo, st *replayState) *uint {
	key := strings.ToLower(strings.TrimSpace(commit.Email)) + "|" + strings.TrimSpace(commit.Author)
	if cached, ok := st.authors[key]; ok {
		return cached
	}
	out := s.resolveAuthorForMigration(ctx, commit)
	st.authors[key] = out
	return out
}

// diffToChanges turns the file-set delta between parent and commit into
// wikicatalog.Change rows. Paths that are not wiki markdown paths (dotfiles,
// non-.md files) are skipped without error. A markdown path whose slug cannot
// be represented by the catalog's single slug grammar produces an error by
// default.
func (s *Service) diffToChanges(ctx context.Context, full, commitSHA string, curPaths []string, curBlobs, parBlobs map[string]string, opts WikiMigrationOptions) ([]wikicatalog.Change, error) {
	current := make(map[string]struct{}, len(curPaths))
	for _, p := range curPaths {
		current[p] = struct{}{}
	}
	var changes []wikicatalog.Change

	checkCompatible := func(slug, path string) (skip bool, err error) {
		if err := wikicatalog.ValidateWritable(slug); err == nil {
			return false, nil
		}
		if opts.SkipIncompatibleSlugs {
			slog.WarnContext(ctx, "wiki migration: skipping slug incompatible with catalog slug grammar",
				"sha", commitSHA, "path", path, "slug", slug)
			return true, nil
		}
		return false, fmt.Errorf("commit %s touches path %q whose slug %q cannot be represented by the catalog; rename the page in source git before migrating, or set SkipIncompatibleSlugs=true to drop these pages with a warning",
			commitSHA, path, slug)
	}

	// Upserts: added or modified pages.
	for _, p := range curPaths {
		slug, ok := wikiMigrationPathToSlug(p)
		if !ok {
			continue
		}
		if skip, err := checkCompatible(slug, p); err != nil {
			return nil, err
		} else if skip {
			continue
		}
		if parBlobs[p] == curBlobs[p] {
			continue
		}
		body, err := s.Git.ReadFileAtRef(ctx, full, p, commitSHA)
		if err != nil {
			return nil, fmt.Errorf("read %s@%s: %w", p, commitSHA, err)
		}
		changes = append(changes, wikicatalog.Change{
			Op:   wikicatalog.OpUpsert,
			Slug: slug,
			Body: body,
		})
	}

	// Deletes: in parent, not in current.
	for p := range parBlobs {
		if _, ok := current[p]; ok {
			continue
		}
		slug, ok := wikiMigrationPathToSlug(p)
		if !ok {
			continue
		}
		if skip, err := checkCompatible(slug, p); err != nil {
			return nil, err
		} else if skip {
			continue
		}
		changes = append(changes, wikicatalog.Change{
			Op:   wikicatalog.OpDelete,
			Slug: slug,
		})
	}

	// Stable ordering — the catalog rejects duplicate slug slots within
	// a changeset, and stable order makes failures reproducible.
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Slug != changes[j].Slug {
			return changes[i].Slug < changes[j].Slug
		}
		return changes[i].Op < changes[j].Op
	})
	return changes, nil
}

func wikiMigrationPathToSlug(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" || strings.HasPrefix(path, ".") || !strings.HasSuffix(path, wikiPageExt) {
		return "", false
	}
	return strings.TrimSuffix(path, wikiPageExt), true
}

func (s *Service) resolveAuthorForMigration(ctx context.Context, commit gitstore.SearchCommitInfo) *uint {
	email := strings.ToLower(strings.TrimSpace(commit.Email))
	if email != "" {
		usersByEmail := s.lookupUsersByEmailCI(ctx, []string{email})
		if u, ok := usersByEmail[email]; ok {
			id := u.ID
			return &id
		}
	}
	login := strings.TrimSpace(commit.Author)
	if login != "" {
		usersByLogin := s.GetUsersByLogins(ctx, []string{login})
		if u, ok := usersByLogin[login]; ok {
			id := u.ID
			return &id
		}
	}
	return nil
}

// parseCommitTime accepts the ISO-8601 strings emitted by git log
// %cI / %aI and returns the UTC instant. The first non-empty field
// that parses wins. If neither parses, parseCommitTime returns an
// error: silently fabricating time.Now() here would publish historical
// changesets with a present-day committed_at, which migration cannot
// recover from once the cutover runs.
func parseCommitTime(primary, fallback string) (time.Time, error) {
	var lastErr error
	for _, raw := range []string{primary, fallback} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err == nil {
			return t.UTC(), nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return time.Time{}, fmt.Errorf("wiki migration: unparseable commit timestamps %q / %q: %w", primary, fallback, lastErr)
	}
	return time.Time{}, fmt.Errorf("wiki migration: no commit timestamp present")
}
