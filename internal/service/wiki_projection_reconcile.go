package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

type wikiHeadProjectionState struct {
	db.WikiChangeset
	PendingProjectionCount int64 `gorm:"column:pending_projection_count"`
}

// wikiProjectionSnapshotMatchesGit reports whether a catalog snapshot already
// proves the invariant that reconcileWikiWriteStateLocked would establish.
// Prepared writes store their final Git SHA before the catalog transaction
// commits, so the catalog head and Git HEAD are the complete durable proof; no
// second projection-marker write is needed after ref publication.
func (s *Service) wikiProjectionSnapshotMatchesGit(
	ctx context.Context,
	git *gitstore.Store,
	repo db.Repository,
	state wikicatalog.HeadProjectionState,
) (bool, error) {
	if state.GitRepairObligationExists {
		return false, nil
	}

	full := wikiRepoFullName(repo.FullName)
	gitHead, err := git.HeadSHA(ctx, full, wikiDefaultBranch)
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return !state.Exists, nil
		}
		if state.Exists {
			return false, fmt.Errorf("read wiki Git projection head for %s: %w", repo.FullName, err)
		}
		// Preserve the no-catalog recovery behavior in
		// reconcileWikiWriteStateLocked for non-reference errors.
		return false, nil
	}
	if !state.Exists {
		return false, nil
	}

	headMatches := strings.EqualFold(
		strings.TrimSpace(state.CommitSHA),
		strings.TrimSpace(gitHead),
	)
	if state.SynthFormatVersion >= synthProjectionMaterialized && state.PendingProjectionCount == 0 {
		if state.Source == wikicatalog.SourceCompact {
			return true, nil
		}
		return headMatches, nil
	}
	return false, nil
}

// reconcileWikiWriteStateLocked restores the catalog/Git invariant before a
// REST write or receive-pack mutates either side. The caller owns both the
// catalog serialization mutex and the wiki repository Git lock.
func (s *Service) reconcileWikiWriteStateLocked(
	ctx context.Context,
	git *gitstore.Store,
	repo db.Repository,
) (<-chan error, error) {
	if _, err := s.consumeWikiGitRepairObligationLocked(ctx, repo); err != nil {
		return nil, err
	}

	head, exists, err := s.loadWikiHeadProjectionState(ctx, repo.ID)
	if err != nil {
		return nil, fmt.Errorf("load wiki projection state for %s: %w", repo.FullName, err)
	}

	var recoveredEffects <-chan error
	source := wikicatalog.Source(strings.TrimSpace(head.Source))
	if exists && head.SynthFormatVer < synthProjectionMaterialized {
		switch {
		case source == wikicatalog.SourceCompact:
			if err := s.resumePendingWikiCompactProjection(ctx, repo.FullName, head.WikiChangeset); err != nil {
				return nil, fmt.Errorf("resume pending wiki compaction for %s: %w", repo.FullName, err)
			}
			return nil, nil
		case wikiChangesetAlreadyInGit(source):
			// Git-originated changesets from older versions used format 0 even
			// though their commit was already present in the backing repo.
		case wikiChangesetNeedsGitProjection(source):
			if head.PendingProjectionCount != 1 {
				return nil, fmt.Errorf(
					"wiki projection for %s has %d pending changesets; refusing a new write until repaired",
					repo.FullName,
					head.PendingProjectionCount,
				)
			}
			recoveredEffects, err = s.resumePendingWikiChangesetProjectionLocked(ctx, git, repo, head.WikiChangeset)
			if err != nil {
				if errors.Is(err, plumbing.ErrObjectNotFound) {
					return s.rebuildMissingPreparedWikiChangesetLocked(ctx, git, repo, head.WikiChangeset, recoveredEffects)
				}
				return nil, err
			}
			head.SynthFormatVer = synthProjectionMaterialized
		default:
			return nil, fmt.Errorf(
				"wiki projection for %s has unsupported pending source %q",
				repo.FullName,
				head.Source,
			)
		}
	} else if exists && head.PendingProjectionCount != 0 {
		return nil, fmt.Errorf(
			"wiki projection for %s has an older pending changeset hidden by materialized head %d; refusing a new write until repaired",
			repo.FullName,
			head.ChangesetID,
		)
	}

	full := wikiRepoFullName(repo.FullName)
	gitHead, err := git.HeadSHA(ctx, full, wikiDefaultBranch)
	if err != nil {
		if exists && errors.Is(err, plumbing.ErrReferenceNotFound) {
			var missingObjectChangeset *db.WikiChangeset
			if wikiChangesetNeedsGitProjection(source) && strings.TrimSpace(head.SynthCommitSHA) != "" {
				done, resumeErr := s.resumePendingWikiChangesetProjectionLocked(ctx, git, repo, head.WikiChangeset)
				if resumeErr == nil {
					return done, nil
				}
				if !errors.Is(resumeErr, plumbing.ErrObjectNotFound) {
					return nil, resumeErr
				}
				changeset := head.WikiChangeset
				missingObjectChangeset = &changeset
			}
			if missingObjectChangeset != nil {
				var rebuildErr error
				recoveredEffects, rebuildErr = s.rebuildMissingPreparedWikiChangesetLocked(
					ctx,
					git,
					repo,
					*missingObjectChangeset,
					recoveredEffects,
				)
				if rebuildErr != nil {
					return nil, fmt.Errorf(
						"rebuild missing wiki Git projection for %s after head lookup failed (%v): %w",
						repo.FullName,
						err,
						rebuildErr,
					)
				}
			} else if _, rebuildErr := s.rebuildMissingWikiGitProjectionLocked(ctx, git, repo); rebuildErr != nil {
				return nil, fmt.Errorf(
					"rebuild missing wiki Git projection for %s after head lookup failed (%v): %w",
					repo.FullName,
					err,
					rebuildErr,
				)
			}
		} else if exists {
			return nil, fmt.Errorf("read wiki Git projection head for %s: %w", repo.FullName, err)
		}
		return recoveredEffects, nil
	}
	if exists && source == wikicatalog.SourceCompact {
		return recoveredEffects, nil
	}

	gitHead = strings.ToLower(strings.TrimSpace(gitHead))
	catalogHead := strings.ToLower(strings.TrimSpace(head.SynthCommitSHA))
	if exists && catalogHead == gitHead {
		return recoveredEffects, nil
	}
	if exists && wikiChangesetNeedsGitProjection(source) && catalogHead != "" {
		// A prepared REST commit is durable before its catalog transaction can
		// commit. If Git still points at its parent, finish the interrupted ref
		// publication. If Git already advanced beyond the catalog head through a
		// direct push, leave the normal ingest path below in charge.
		gitAhead, ancestryErr := git.IsAncestor(ctx, full, catalogHead, gitHead)
		if ancestryErr != nil {
			if errors.Is(ancestryErr, plumbing.ErrObjectNotFound) {
				return s.rebuildMissingPreparedWikiChangesetLocked(ctx, git, repo, head.WikiChangeset, recoveredEffects)
			}
			return nil, fmt.Errorf(
				"check wiki Git ancestry %s..%s for %s: %w",
				catalogHead,
				gitHead,
				repo.FullName,
				ancestryErr,
			)
		}
		if !gitAhead {
			catalogAhead, ancestryErr := git.IsAncestor(ctx, full, gitHead, catalogHead)
			if ancestryErr != nil {
				if errors.Is(ancestryErr, plumbing.ErrObjectNotFound) {
					return s.rebuildMissingPreparedWikiChangesetLocked(ctx, git, repo, head.WikiChangeset, recoveredEffects)
				}
				return nil, fmt.Errorf(
					"check wiki catalog ancestry %s..%s for %s: %w",
					gitHead,
					catalogHead,
					repo.FullName,
					ancestryErr,
				)
			}
			if !catalogAhead {
				return nil, fmt.Errorf(
					"wiki projection for %s diverged: catalog=%s git=%s",
					repo.FullName,
					catalogHead,
					gitHead,
				)
			}
			done, resumeErr := s.resumePendingWikiChangesetProjectionLocked(ctx, git, repo, head.WikiChangeset)
			if resumeErr != nil {
				if errors.Is(resumeErr, plumbing.ErrObjectNotFound) {
					return s.rebuildMissingPreparedWikiChangesetLocked(ctx, git, repo, head.WikiChangeset, recoveredEffects)
				}
				return nil, resumeErr
			}
			return done, nil
		}
	}

	if _, err := s.ingestOneWikiGitLocked(ctx, repo, WikiGitIngestOptions{}); err != nil {
		return nil, fmt.Errorf("catch up wiki Git before write for %s: %w", repo.FullName, err)
	}
	latest, err := s.loadLatestWikiChangesetState(ctx, repo.ID)
	if err != nil {
		return nil, fmt.Errorf("verify wiki catalog head for %s: %w", repo.FullName, err)
	}
	if !strings.EqualFold(latest.CommitSHA, gitHead) {
		return nil, fmt.Errorf(
			"wiki projection for %s did not converge after ingest: catalog=%s git=%s",
			repo.FullName,
			latest.CommitSHA,
			gitHead,
		)
	}
	return recoveredEffects, nil
}

// rebuildMissingWikiGitProjectionLocked restores a deleted or empty backing
// repository from the catalog's complete live-page snapshot. The recovery
// commit is recorded as a zero-change Git-originated changeset so the catalog
// head and Git head converge before the caller applies its requested mutation.
func (s *Service) rebuildMissingWikiGitProjectionLocked(
	ctx context.Context,
	git *gitstore.Store,
	repo db.Repository,
) (bool, error) {
	var pages []db.WikiPage
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND deleted_at IS NULL", repo.ID).
		Order("slug ASC").
		Find(&pages).Error; err != nil {
		return false, fmt.Errorf("load live pages: %w", err)
	}
	wikiFullName := wikiRepoFullName(repo.FullName)
	livePaths := make(map[string]struct{}, len(pages))
	for _, page := range pages {
		livePaths[wikiSlugToPath(page.Slug)] = struct{}{}
	}

	var existingPaths []string
	if paths, err := git.ListTreeFilesAtRef(ctx, wikiFullName, wikiDefaultBranch); err == nil {
		existingPaths = paths
	}

	// The branch can be absent in missing-projection recovery. When it is
	// present, delete paths outside the catalog snapshot so delete and rename
	// recovery converge to catalog state instead of preserving stale page files.
	// Receive-pack can also accept non-catalog files such as assets; preserve
	// those because Git ingest intentionally ignores them.
	mutations := make([]gitstore.FileMutation, 0, len(existingPaths)+len(pages))
	for _, path := range existingPaths {
		if _, ok := livePaths[path]; ok {
			continue
		}
		if !wikiPathManagedByCatalog(path) {
			continue
		}
		mutations = append(mutations, gitstore.FileMutation{
			Path:   path,
			Delete: true,
		})
	}
	for _, page := range pages {
		body, err := s.wikiPageBody(ctx, page)
		if err != nil {
			return false, fmt.Errorf("load body for %q: %w", page.Slug, err)
		}
		mutations = append(mutations, gitstore.FileMutation{
			Path:    wikiSlugToPath(page.Slug),
			Content: body,
		})
	}
	if len(mutations) == 0 {
		if len(pages) == 0 && len(existingPaths) == 0 {
			committedAt := time.Now().UTC().Truncate(time.Second)
			commitSHA, err := git.CommitRootEmptyTreeAt(
				ctx,
				wikiFullName,
				wikiDefaultBranch,
				"Rebuild wiki Git projection",
				committedAt,
			)
			if err != nil {
				return false, fmt.Errorf("commit empty catalog snapshot: %w", err)
			}
			if _, err := s.WikiCatalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
				RepositoryID:        repo.ID,
				Message:             "Rebuild wiki Git projection",
				Source:              wikicatalog.SourceGit,
				OverrideCommitSHA:   commitSHA,
				OverrideCommittedAt: &committedAt,
			}); err != nil {
				return false, fmt.Errorf("record rebuilt projection commit %s: %w", commitSHA, err)
			}
			return true, nil
		}
		return false, nil
	}

	committedAt := time.Now().UTC().Truncate(time.Second)
	commitSHA, err := git.CommitFilesAt(
		ctx,
		wikiFullName,
		wikiDefaultBranch,
		"Rebuild wiki Git projection",
		mutations,
		committedAt,
	)
	if err != nil {
		return false, fmt.Errorf("commit catalog snapshot: %w", err)
	}
	if _, err := s.WikiCatalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
		RepositoryID:        repo.ID,
		Message:             "Rebuild wiki Git projection",
		Source:              wikicatalog.SourceGit,
		OverrideCommitSHA:   commitSHA,
		OverrideCommittedAt: &committedAt,
	}); err != nil {
		return false, fmt.Errorf("record rebuilt projection commit %s: %w", commitSHA, err)
	}
	return true, nil
}

func wikiPathManagedByCatalog(path string) bool {
	slug, ok := wikiGitIngestPathToSlug(path)
	if !ok {
		return false
	}
	return wikicatalog.ValidateWritable(slug) == nil
}

func (s *Service) rebuildMissingPreparedWikiChangesetLocked(
	ctx context.Context,
	git *gitstore.Store,
	repo db.Repository,
	changeset db.WikiChangeset,
	recoveredEffects <-chan error,
) (<-chan error, error) {
	rebuilt, err := s.rebuildMissingWikiGitProjectionLocked(ctx, git, repo)
	if err != nil {
		return nil, err
	}
	if !rebuilt {
		return nil, fmt.Errorf("rebuild missing wiki Git projection for %s produced no recovery commit", repo.FullName)
	}
	done, err := s.enqueueRecoveredWikiChangesetEffects(ctx, repo, changeset)
	if err != nil {
		return nil, err
	}
	return combineWikiPostCommitWaiters(recoveredEffects, done), nil
}

// ReconcileWikiBeforeReceivePackLocked catches up work left by an earlier
// failed request before another receive-pack is allowed to update the ref.
// The caller already owns the catalog and Git repository locks.
func (s *Service) ReconcileWikiBeforeReceivePackLocked(ctx context.Context, repoFullName string) error {
	repo, err := s.LookupRepoIdentity(ctx, repoFullName)
	if err != nil {
		return err
	}
	done, err := s.reconcileWikiWriteStateLocked(ctx, s.Git, repo)
	if err != nil {
		return err
	}
	if done != nil {
		return <-done
	}
	return nil
}

func (s *Service) loadWikiHeadProjectionState(ctx context.Context, repoID uint) (wikiHeadProjectionState, bool, error) {
	var state wikiHeadProjectionState
	result := s.DBForCtx(ctx).
		Table("wiki_changesets AS current").
		Select(
			"current.*, ("+
				"SELECT COUNT(*) FROM wiki_changesets AS pending "+
				"WHERE pending.repository_id = current.repository_id "+
				"AND pending.synth_format_ver < ? "+
				"AND pending.source IN ?"+
				") AS pending_projection_count",
			synthProjectionMaterialized,
			[]string{
				string(wikicatalog.SourceREST),
				string(wikicatalog.SourceAdmin),
				string(wikicatalog.SourceBatch),
			},
		).
		Joins("JOIN wiki_repo_heads AS head ON head.head_changeset_id = current.changeset_id").
		Where("head.repository_id = ? AND current.repository_id = ?", repoID, repoID).
		Limit(1).
		Find(&state)
	if result.Error != nil {
		return wikiHeadProjectionState{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		return wikiHeadProjectionState{}, false, nil
	}
	return state, true, nil
}

func (s *Service) resumePendingWikiChangesetProjectionLocked(
	ctx context.Context,
	git *gitstore.Store,
	repo db.Repository,
	changeset db.WikiChangeset,
) (<-chan error, error) {
	full := wikiRepoFullName(repo.FullName)
	target := strings.ToLower(strings.TrimSpace(changeset.SynthCommitSHA))
	if target == "" {
		return nil, fmt.Errorf("pending wiki changeset %d has no prepared commit", changeset.ChangesetID)
	}

	current, _ := git.HeadSHA(ctx, full, wikiDefaultBranch)
	if !strings.EqualFold(strings.TrimSpace(current), target) {
		if err := git.PublishPreparedCommit(
			ctx,
			full,
			wikiDefaultBranch,
			gitstore.PreparedCommit{SHA: target},
		); err != nil {
			return nil, fmt.Errorf(
				"resume pending wiki commit %s for %s: %w",
				target,
				repo.FullName,
				err,
			)
		}
	}
	if changeset.SynthFormatVer < synthProjectionMaterialized {
		if err := s.markWikiChangesetMaterialized(ctx, changeset.ChangesetID, target); err != nil {
			return nil, fmt.Errorf("mark recovered wiki commit %s for %s: %w", target, repo.FullName, err)
		}
	}
	return s.enqueueRecoveredWikiChangesetEffects(ctx, repo, changeset)
}

func (s *Service) enqueueRecoveredWikiChangesetEffects(
	ctx context.Context,
	repo db.Repository,
	changeset db.WikiChangeset,
) (<-chan error, error) {
	result, err := s.loadWikiChangesetResult(ctx, changeset)
	if err != nil {
		return nil, fmt.Errorf("load recovered wiki changeset %d for %s: %w", changeset.ChangesetID, repo.FullName, err)
	}
	done, err := s.enqueueWikiPostCommitEffects(ctx, repo, result)
	if err != nil {
		return nil, fmt.Errorf("queue recovered wiki effects for %s: %w", repo.FullName, err)
	}
	return done, nil
}

func combineWikiPostCommitWaiters(first, second <-chan error) <-chan error {
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	done := make(chan error, 1)
	go func() {
		firstErr := <-first
		secondErr := <-second
		if firstErr != nil {
			done <- firstErr
		} else {
			done <- secondErr
		}
		close(done)
	}()
	return done
}

func (s *Service) loadWikiChangesetResult(
	ctx context.Context,
	changeset db.WikiChangeset,
) (wikicatalog.ChangeSetResult, error) {
	var revisions []db.WikiPageRevision
	if err := s.DBForCtx(ctx).
		Where("changeset_id = ?", changeset.ChangesetID).
		Order("page_id ASC, revision_id ASC").
		Find(&revisions).Error; err != nil {
		return wikicatalog.ChangeSetResult{}, err
	}
	if len(revisions) == 0 && changeset.PageCount != 0 {
		return wikicatalog.ChangeSetResult{}, fmt.Errorf("changeset has no revisions")
	}
	var head db.WikiRepoHead
	if err := s.DBForCtx(ctx).
		Select("reference_effects_through_changeset_id").
		Where("repository_id = ?", changeset.RepositoryID).
		Take(&head).Error; err != nil {
		return wikicatalog.ChangeSetResult{}, fmt.Errorf("load reference effects cursor: %w", err)
	}

	// Recovery replays every post-commit effect, including create-time
	// references from legacy changesets that predate the durable cursor.
	// If any cursor is still pending, keep it for the full rebuild worker
	// instead of letting this single recovered changeset clear older work.
	result := wikicatalog.ChangeSetResult{
		ChangesetID:               changeset.ChangesetID,
		ParentID:                  changeset.ParentID,
		CommitSHA:                 strings.ToLower(strings.TrimSpace(changeset.SynthCommitSHA)),
		CommitSHAOverridden:       true,
		Message:                   string(changeset.Message),
		CommittedAt:               changeset.CommittedAt,
		Source:                    wikicatalog.Source(strings.TrimSpace(changeset.Source)),
		Changes:                   make([]wikicatalog.ChangeResult, 0, len(revisions)),
		ReferenceEffectsPending:   true,
		ReferenceEffectsCoalesced: head.ReferenceEffectsThroughChangesetID != nil,
	}
	for _, revision := range revisions {
		change := wikicatalog.ChangeResult{
			Slug:       revision.SlugAtRev,
			PageID:     revision.PageID,
			RevisionID: revision.RevisionID,
			BlobSHA:    revision.BlobSHA,
			BodySize:   revision.BodySize,
		}
		switch revision.Op {
		case "create":
			change.Op = wikicatalog.OpUpsert
			change.UpsertDisposition = wikicatalog.UpsertDispositionCreate
		case "update":
			change.Op = wikicatalog.OpUpsert
			change.UpsertDisposition = wikicatalog.UpsertDispositionUpdate
		case "restore":
			change.Op = wikicatalog.OpUpsert
			change.UpsertDisposition = wikicatalog.UpsertDispositionRestore
		case "delete":
			change.Op = wikicatalog.OpDelete
			change.PrevSlug = revision.SlugAtRev
		case "rename":
			change.Op = wikicatalog.OpRename
			if revision.RevisionID <= 1 {
				return wikicatalog.ChangeSetResult{}, fmt.Errorf(
					"rename revision for page %d has no predecessor",
					revision.PageID,
				)
			}
			var previous db.WikiPageRevision
			if err := s.DBForCtx(ctx).
				Where("page_id = ? AND revision_id = ?", revision.PageID, revision.RevisionID-1).
				Take(&previous).Error; err != nil {
				return wikicatalog.ChangeSetResult{}, fmt.Errorf(
					"load predecessor for page %d revision %d: %w",
					revision.PageID,
					revision.RevisionID,
					err,
				)
			}
			change.PrevSlug = previous.SlugAtRev
		default:
			return wikicatalog.ChangeSetResult{}, fmt.Errorf(
				"unsupported revision operation %q",
				revision.Op,
			)
		}
		result.Changes = append(result.Changes, change)
	}
	return result, nil
}

func wikiChangesetNeedsGitProjection(source wikicatalog.Source) bool {
	switch source {
	case wikicatalog.SourceREST, wikicatalog.SourceAdmin, wikicatalog.SourceBatch:
		return true
	default:
		return false
	}
}
