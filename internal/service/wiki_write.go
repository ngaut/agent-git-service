package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

type wikiRESTSpeculativePrepare struct {
	preparedChangeSet *wikicatalog.PreparedChangeSet
	prepared          gitstore.PreparedCommit
	persistDone       <-chan error
	err               error
}

// applyWikiRESTChangeSetWithLocks captures the catalog snapshot before waiting
// for the Git lock. A preceding writer may still be publishing its ref while
// this read runs, but it has already committed the matching catalog head. No
// A healthy snapshot can prepare and persist immutable child objects while
// that publication finishes. The second catalog commit still waits for the Git
// lock and a fresh HEAD validation.
func (s *Service) applyWikiRESTChangeSetWithLocks(
	ctx context.Context,
	git *gitstore.Store,
	catalog *wikicatalog.Catalog,
	repo db.Repository,
	req wikicatalog.ChangeSetRequest,
) (wikicatalog.ChangeSetResult, *wikiPostCommitWaiter, error) {
	committedAt := time.Now().UTC().Truncate(time.Second)
	if catalog.Now != nil {
		committedAt = catalog.Now()
	}
	req.OverrideCommittedAt = &committedAt

	var (
		snapshot    *wikicatalog.ChangeSetSnapshot
		snapshotErr error
		result      wikicatalog.ChangeSetResult
		waiter      *wikiPostCommitWaiter
		speculative *wikiRESTSpeculativePrepare
	)
	err := s.withWikiRESTWriteLocksForRepo(
		ctx,
		git,
		repo,
		func() error {
			stageStarted := time.Now()
			snapshot, snapshotErr = catalog.SnapshotChangeSet(ctx, req)
			observeWikiWritePhase(ctx, wikiWritePhasePreflight, stageStarted)
			if s.testWikiRESTSnapshot != nil {
				s.testWikiRESTSnapshot(repo.FullName)
			}
			if snapshotErr == nil && wikiSnapshotCanPrepareBeforeGitLock(snapshot.HeadProjection()) {
				speculative = s.prepareWikiRESTCommitBeforeGitLock(
					ctx,
					git,
					catalog,
					repo,
					req,
					snapshot,
					committedAt,
				)
			}
			return nil
		},
		func() (func() error, error) {
			var applyErr error
			result, waiter, applyErr = s.applyWikiRESTChangeSet(
				ctx,
				git,
				catalog,
				repo,
				req,
				snapshot,
				snapshotErr,
				speculative,
			)
			if applyErr != nil {
				return nil, applyErr
			}
			return func() error {
				return s.finishWikiRESTPostCommit(ctx, repo, result, waiter)
			}, nil
		},
	)
	return result, waiter, err
}

func wikiSnapshotCanPrepareBeforeGitLock(state wikicatalog.HeadProjectionState) bool {
	return state.Exists &&
		state.CommitSHA != "" &&
		state.Source != wikicatalog.SourceCompact &&
		state.SynthFormatVersion >= synthProjectionMaterialized &&
		state.PendingProjectionCount == 0 &&
		!state.GitRepairObligationExists
}

func (s *Service) prepareWikiRESTCommitBeforeGitLock(
	ctx context.Context,
	git *gitstore.Store,
	catalog *wikicatalog.Catalog,
	repo db.Repository,
	req wikicatalog.ChangeSetRequest,
	snapshot *wikicatalog.ChangeSetSnapshot,
	committedAt time.Time,
) *wikiRESTSpeculativePrepare {
	speculative := &wikiRESTSpeculativePrepare{}

	stageStarted := time.Now()
	mutations, err := s.wikiGitMutationsForChanges(ctx, req.RepositoryID, req.Changes)
	observeWikiWritePhase(ctx, wikiWritePhaseMutations, stageStarted)
	if err != nil {
		speculative.err = err
		return speculative
	}

	stageStarted = time.Now()
	speculative.preparedChangeSet, err = catalog.ValidateChangeSetSnapshot(ctx, snapshot)
	observeWikiWritePhase(ctx, wikiWritePhasePreflight, stageStarted)
	if err != nil {
		speculative.err = err
		return speculative
	}

	gitPrepareStarted := time.Now()
	stageStarted = time.Now()
	speculative.prepared, err = git.BuildCommitFilesAtParent(
		ctx,
		wikiRepoFullName(repo.FullName),
		wikiDefaultBranch,
		snapshot.HeadProjection().CommitSHA,
		req.Message,
		mutations,
		committedAt,
	)
	observeWikiWritePhase(ctx, wikiWritePhaseGitPrepareBuild, stageStarted)
	if err != nil {
		speculative.err = err
		return speculative
	}
	speculative.persistDone = s.startWikiPreparedCommitPersistence(
		ctx,
		git,
		repo.FullName,
		speculative.prepared,
		gitPrepareStarted,
	)
	return speculative
}

// applyWikiRESTChangeSet prepares the exact Git commit before committing the
// catalog transaction. The caller holds both wiki write locks and supplies the
// snapshot captured under the catalog lock before it acquired the Git lock.
// A preceding writer may still be publishing the ref while the snapshot is
// captured, but the Git lock ensures publication completes before this method
// validates the snapshot against Git HEAD.
func (s *Service) applyWikiRESTChangeSet(
	ctx context.Context,
	git *gitstore.Store,
	catalog *wikicatalog.Catalog,
	repo db.Repository,
	req wikicatalog.ChangeSetRequest,
	snapshot *wikicatalog.ChangeSetSnapshot,
	snapshotErr error,
	speculative *wikiRESTSpeculativePrepare,
) (wikicatalog.ChangeSetResult, *wikiPostCommitWaiter, error) {
	stageStarted := time.Now()
	err := ensureWikiRepoWithGit(ctx, git, repo.FullName)
	observeWikiWritePhase(ctx, wikiWritePhaseRepoInit, stageStarted)
	if err != nil {
		return wikicatalog.ChangeSetResult{}, nil, err
	}

	committedAt := *req.OverrideCommittedAt

	var recoveredEffects <-chan error
	snapshotHealthy := false
	stageStarted = time.Now()
	if snapshotErr == nil {
		snapshotHealthy, err = s.wikiProjectionSnapshotMatchesGit(
			ctx,
			git,
			repo,
			snapshot.HeadProjection(),
		)
	}
	if err != nil && speculative != nil && speculative.persistDone != nil {
		<-speculative.persistDone
		speculative = nil
	}
	if err == nil && !snapshotHealthy {
		if speculative != nil && speculative.persistDone != nil {
			<-speculative.persistDone
		}
		speculative = nil
		recoveredEffects, err = s.reconcileWikiWriteStateLocked(ctx, git, repo)
	}
	observeWikiWritePhase(ctx, wikiWritePhaseReconcile, stageStarted)
	if err != nil {
		return wikicatalog.ChangeSetResult{}, nil, err
	}
	if !snapshotHealthy {
		// Recovery can take materially longer than the healthy head check.
		// Refresh the requested commit time before taking the replacement
		// snapshot so repaired writes keep the original timestamp behavior.
		committedAt = time.Now().UTC().Truncate(time.Second)
		if catalog.Now != nil {
			committedAt = catalog.Now()
		}
		req.OverrideCommittedAt = &committedAt
	}

	var (
		preparedChangeSet *wikicatalog.PreparedChangeSet
		prepared          gitstore.PreparedCommit
		persistDone       <-chan error
	)
	if snapshotHealthy && speculative != nil {
		if speculative.err != nil {
			return wikicatalog.ChangeSetResult{}, nil, speculative.err
		}
		preparedChangeSet = speculative.preparedChangeSet
		prepared = speculative.prepared
		persistDone = speculative.persistDone
	} else {
		stageStarted = time.Now()
		mutations, mutationErr := s.wikiGitMutationsForChanges(ctx, req.RepositoryID, req.Changes)
		observeWikiWritePhase(ctx, wikiWritePhaseMutations, stageStarted)
		if mutationErr != nil {
			return wikicatalog.ChangeSetResult{}, nil, mutationErr
		}

		stageStarted = time.Now()
		if snapshotHealthy {
			preparedChangeSet, err = catalog.ValidateChangeSetSnapshot(ctx, snapshot)
		} else {
			preparedChangeSet, err = catalog.PrepareChangeSet(ctx, req)
		}
		observeWikiWritePhase(ctx, wikiWritePhasePreflight, stageStarted)
		if err != nil {
			return wikicatalog.ChangeSetResult{}, nil, err
		}

		gitPrepareStarted := time.Now()
		stageStarted = time.Now()
		prepared, err = git.BuildCommitFilesAt(
			ctx,
			wikiRepoFullName(repo.FullName),
			wikiDefaultBranch,
			req.Message,
			mutations,
			committedAt,
		)
		observeWikiWritePhase(ctx, wikiWritePhaseGitPrepareBuild, stageStarted)
		if err != nil {
			return wikicatalog.ChangeSetResult{}, nil, err
		}
		persistDone = s.startWikiPreparedCommitPersistence(
			ctx,
			git,
			repo.FullName,
			prepared,
			gitPrepareStarted,
		)
	}

	postCommitCtx, waiter := withWikiPostCommitWaiter(ctx, repo, prepared, persistDone)
	postCommitCtx = wikicatalog.WithApplyPhaseObserver(
		postCommitCtx,
		func(phase wikicatalog.ApplyPhase, duration time.Duration) {
			recordWikiWriteDuration(ctx, wikiCatalogWritePhase(phase), duration)
		},
	)
	waiter.add(recoveredEffects)

	waitForPersistBeforeCommit := func() error {
		barrierStarted := time.Now()
		barrierErr := waiter.waitPrepared()
		observeWikiWritePhase(ctx, wikiWritePhaseGitPersistBarrierWait, barrierStarted)
		return barrierErr
	}
	stageStarted = time.Now()
	result, err := catalog.ApplyPreparedChangeSetWithCommitBarrier(
		postCommitCtx,
		preparedChangeSet,
		prepared.SHA,
		waitForPersistBeforeCommit,
	)
	observeWikiWritePhase(ctx, wikiWritePhaseCatalogApplyTotal, stageStarted)
	// The commit barrier lets catalog mutations overlap current object
	// persistence, but persistence may not still be pending when the transaction
	// commits.
	persistErr := waiter.waitPrepared()
	if err == nil && persistErr != nil {
		err = persistErr
	}
	return result, waiter, err
}

func (s *Service) startWikiPreparedCommitPersistence(
	ctx context.Context,
	git *gitstore.Store,
	repoFullName string,
	prepared gitstore.PreparedCommit,
	gitPrepareStarted time.Time,
) <-chan error {
	persistDone := make(chan error, 1)
	go func() {
		persistStarted := time.Now()
		var persistErr error
		if s.testWikiPreparedPersist != nil {
			persistErr = s.testWikiPreparedPersist(repoFullName, prepared.SHA)
		}
		if persistErr == nil {
			persistErr = git.PersistPreparedCommit(ctx, prepared)
		}
		observeWikiWritePhase(ctx, wikiWritePhaseGitPreparePersist, persistStarted)
		observeWikiWritePhase(ctx, wikiWritePhaseGitPrepare, gitPrepareStarted)
		persistDone <- persistErr
		close(persistDone)
	}()
	return persistDone
}

func wikiCatalogWritePhase(phase wikicatalog.ApplyPhase) string {
	switch phase {
	case wikicatalog.ApplyPhaseBlobUpload:
		return wikiWritePhaseCatalogBlobUpload
	case wikicatalog.ApplyPhaseTransaction:
		return wikiWritePhaseCatalogTransaction
	case wikicatalog.ApplyPhaseTransactionBody:
		return wikiWritePhaseCatalogTxnBody
	case wikicatalog.ApplyPhaseTransactionBoundary:
		return wikiWritePhaseCatalogTxnBoundary
	case wikicatalog.ApplyPhaseChangesetInsert:
		return wikiWritePhaseCatalogChangeset
	case wikicatalog.ApplyPhaseHeadCAS:
		return wikiWritePhaseCatalogHeadCAS
	case wikicatalog.ApplyPhaseChanges:
		return wikiWritePhaseCatalogChanges
	case wikicatalog.ApplyPhasePageWrite:
		return wikiWritePhaseCatalogPageWrite
	case wikicatalog.ApplyPhaseRevisionInsert:
		return wikiWritePhaseCatalogRevision
	case wikicatalog.ApplyPhaseBlobRefs:
		return wikiWritePhaseCatalogBlobRefs
	case wikicatalog.ApplyPhaseOutlinks:
		return wikiWritePhaseCatalogOutlinks
	case wikicatalog.ApplyPhaseInboundLinks:
		return wikiWritePhaseCatalogInboundLinks
	case wikicatalog.ApplyPhaseLabelMutation:
		return wikiWritePhaseCatalogLabels
	case wikicatalog.ApplyPhasePendingBlobCleanup:
		return wikiWritePhaseCatalogPendingCleanup
	case wikicatalog.ApplyPhaseCommitBarrier:
		return wikiWritePhaseCatalogCommitBarrier
	case wikicatalog.ApplyPhasePostCommit:
		return wikiWritePhaseCatalogPostCommit
	default:
		return "catalog_" + string(phase)
	}
}

func (s *Service) wikiGitMutationsForChanges(
	ctx context.Context,
	repoID uint,
	changes []wikicatalog.Change,
) ([]gitstore.FileMutation, error) {
	mutations := make([]gitstore.FileMutation, 0, len(changes)*2)
	for _, change := range changes {
		switch change.Op {
		case wikicatalog.OpUpsert:
			mutations = append(mutations, gitstore.FileMutation{
				Path:    wikiSlugToPath(change.Slug),
				Content: change.Body,
			})
		case wikicatalog.OpDelete:
			mutations = append(mutations, gitstore.FileMutation{
				Path:   wikiSlugToPath(change.Slug),
				Delete: true,
			})
		case wikicatalog.OpRename:
			body := change.Body
			if len(body) == 0 {
				current, err := s.currentWikiBodyForGitMutation(ctx, repoID, change.Slug)
				if err != nil {
					return nil, err
				}
				body = current
			}
			mutations = append(mutations,
				gitstore.FileMutation{Path: wikiSlugToPath(change.Slug), Delete: true},
				gitstore.FileMutation{Path: wikiSlugToPath(change.NewSlug), Content: body},
			)
		default:
			return nil, fmt.Errorf("unsupported wiki change op %v", change.Op)
		}
	}
	return mutations, nil
}

func (s *Service) currentWikiBodyForGitMutation(ctx context.Context, repoID uint, slug string) ([]byte, error) {
	var page db.WikiPage
	err := s.DBForCtx(ctx).
		Select("page_id", "head_blob_sha", "body_size", "body_inline").
		Where("repository_id = ? AND slug = ? AND deleted_at IS NULL", repoID, slug).
		Take(&page).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, wikicatalog.ErrPageNotFound
	}
	if err != nil {
		return nil, err
	}
	if page.BodySize <= wikicatalog.MaxBodyInlineBytes {
		return append([]byte(nil), page.BodyInline...), nil
	}
	if s.WikiBlob == nil {
		return nil, errors.New("wiki blob store unavailable")
	}
	return s.WikiBlob.Get(ctx, page.HeadBlobSHA)
}
