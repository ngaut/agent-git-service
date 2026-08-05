package service

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

const (
	synthProjectionPending      int16 = 0
	synthProjectionMaterialized int16 = 1
)

// WikiCatalogPostCommit is the catalog's post-commit hook, wired during
// server bootstrap. It drives every side effect that the legacy git-backed
// handlers used to schedule synchronously: the search index plus
// materialization of the wiki bare git repo so `git clone` / `git
// pull` against the wiki still works after the catalog cutover.
//
// Errors here surface through ApplyChangeSet or the REST write's post-commit
// waiter so an operator can see what failed, but they do NOT roll back the
// catalog state — the catalog is already committed by the time this runs.
// Runtime REST writes therefore remain pending until the prepared Git commit is
// published. The catalog stores that exact commit SHA before publication, so a
// later writer can recover an interrupted publish by comparing the catalog head
// directly with Git HEAD.
func (s *Service) WikiCatalogPostCommit(ctx context.Context, repoID uint, result wikicatalog.ChangeSetResult) error {
	waiter, deferEffects := wikiPostCommitWaiterFromContext(ctx, repoID)
	if deferEffects {
		// The catalog transaction has committed, but the REST lock coordinator
		// still owns both write locks. It releases the catalog lock before
		// calling finishWikiRESTPostCommit while retaining the Git lock.
		waiter.catalogChangesetID = result.ChangesetID
		return nil
	}

	var repo db.Repository
	if err := s.DBForCtx(ctx).Select("id", "full_name").
		First(&repo, "id = ?", repoID).Error; err != nil {
		return fmt.Errorf("wiki post-commit: lookup repo %d: %w", repoID, err)
	}
	return s.finishWikiCatalogPostCommit(ctx, repo, result, nil)
}

func (s *Service) finishWikiRESTPostCommit(
	ctx context.Context,
	repo db.Repository,
	result wikicatalog.ChangeSetResult,
	waiter *wikiPostCommitWaiter,
) error {
	if waiter == nil || waiter.repo.ID != repo.ID || waiter.catalogChangesetID != result.ChangesetID {
		return fmt.Errorf("wiki post-commit: committed REST changeset handoff is invalid")
	}
	return s.finishWikiCatalogPostCommit(ctx, repo, result, waiter)
}

func (s *Service) finishWikiCatalogPostCommit(
	ctx context.Context,
	repo db.Repository,
	result wikicatalog.ChangeSetResult,
	waiter *wikiPostCommitWaiter,
) error {
	if s.Git == nil {
		return fmt.Errorf("wiki post-commit: git store unavailable")
	}
	git := s.Git
	deferEffects := waiter != nil

	var effectsDone <-chan error
	enqueueEffects := func() error {
		stageStarted := time.Now()
		done, err := s.enqueueWikiPostCommitEffects(ctx, repo, result)
		observeWikiWritePhase(ctx, wikiWritePhasePostCommitEnqueue, stageStarted)
		if err != nil {
			return fmt.Errorf("wiki post-commit: queue effects for %s: %w", repo.FullName, err)
		}
		effectsDone = done
		if deferEffects {
			waiter.add(done)
		}
		return nil
	}

	// Git ingest replays existing git commits into the catalog — the
	// git repo already holds the trees, so re-materializing would
	// duplicate history. Runtime REST writes (Source = rest/batch)
	// originate in the catalog and must land in git for clone/pull.
	if !wikiChangesetAlreadyInGit(result.Source) {
		var materializeErr error
		stageStarted := time.Now()
		if result.CommitSHAOverridden {
			prepared := gitstore.PreparedCommit{SHA: result.CommitSHA}
			if deferEffects && waiter.preparedCommit.SHA == result.CommitSHA {
				if err := waiter.waitPrepared(); err != nil {
					return fmt.Errorf("wiki post-commit: persist prepared git objects for %s: %w", repo.FullName, err)
				}
				prepared = waiter.preparedCommit
			}
			// Object persistence can overlap the catalog transaction, but the
			// publish metric continues to measure only the ref publication path.
			stageStarted = time.Now()
			if s.testWikiPreparedPublishFailure != nil {
				if err := s.testWikiPreparedPublishFailure(repo.FullName, result.CommitSHA); err != nil {
					observeWikiWritePhase(ctx, wikiWritePhaseGitPublish, stageStarted)
					return fmt.Errorf("wiki post-commit: materialize git for %s: %w", repo.FullName, err)
				}
			}
			materializeErr = git.PublishPreparedCommit(
				ctx,
				wikiRepoFullName(repo.FullName),
				wikiDefaultBranch,
				prepared,
			)
		} else {
			if s.WikiBlob == nil {
				return fmt.Errorf("wiki post-commit: blob store unavailable")
			}
			materializeErr = s.materializeChangesetToGit(ctx, git, s.WikiBlob, repo.FullName, result)
		}
		observeWikiWritePhase(ctx, wikiWritePhaseGitPublish, stageStarted)
		if materializeErr != nil {
			return fmt.Errorf("wiki post-commit: materialize git for %s: %w", repo.FullName, materializeErr)
		}
		// Once the Git ref is visible, ordered reference/search effects can leave
		// the repo write lock. Prepared REST changesets already store the published
		// Git SHA, so they need no second database write to record the same state.
		if deferEffects {
			if err := enqueueEffects(); err != nil {
				return err
			}
		}
		if result.CommitSHAOverridden {
			if !deferEffects {
				err := s.markPreparedWikiChangesetMaterialized(ctx, result.ChangesetID, result.CommitSHA)
				if err != nil {
					if effectsDone != nil {
						<-effectsDone
					}
					return fmt.Errorf("wiki post-commit: mark materialized for %s: %w", repo.FullName, err)
				}
			}
		}
	}

	if effectsDone == nil {
		if err := enqueueEffects(); err != nil {
			return err
		}
	}
	if deferEffects {
		return nil
	}
	return <-effectsDone
}

func (s *Service) applyWikiPostCommitEffects(
	ctx context.Context,
	repo db.Repository,
	result wikicatalog.ChangeSetResult,
) error {
	started := time.Now()
	defer observeWikiWritePhase(ctx, wikiWritePhaseReferenceEffectsTotal, started)

	if s.testWikiPostCommitEffects != nil {
		s.testWikiPostCommitEffects(repo.FullName, result)
	}
	if s.WikiBlob == nil {
		return fmt.Errorf("wiki post-commit: blob store unavailable")
	}
	blobStore := s.WikiBlob
	synchronousSearch := wikiChangesetAlreadyInGit(result.Source)

	for _, ch := range result.Changes {
		switch ch.Op {
		case wikicatalog.OpUpsert, wikicatalog.OpRename:
			// On rename, the old slug's search document must be removed before
			// indexing the new one or wiki/search will surface both.
			if ch.Op == wikicatalog.OpRename && ch.PrevSlug != "" && ch.PrevSlug != ch.Slug {
				if err := s.runWikiReferenceAndSearchEffects(
					ctx,
					repo.ID,
					ch.PrevSlug,
					synchronousSearch,
					func() error {
						if err := s.deleteIssueReferencesForWikiPage(ctx, repo.ID, ch.PrevSlug); err != nil {
							return fmt.Errorf("delete previous issue refs for %s: %w", ch.PrevSlug, err)
						}
						return nil
					},
					func() error {
						if err := s.syncWikiSearchDelete(ctx, repo.FullName, ch.PrevSlug); err != nil {
							return fmt.Errorf("delete previous search document for %s: %w", ch.PrevSlug, err)
						}
						return nil
					},
				); err != nil {
					return err
				}
			}

			body := string(ch.Body)
			bodyAvailable := ch.BodyAvailable
			if !bodyAvailable {
				body, bodyAvailable = s.wikiBodyForReindex(ctx, blobStore, ch.PageID, ch.BlobSHA)
			}
			page := WikiPage{
				Slug:  ch.Slug,
				Title: wikicatalog.TitleFromSlug(ch.Slug),
				SHA:   ch.BlobSHA,
				Body:  body,
			}
			if bodyAvailable {
				if err := s.runWikiReferenceAndSearchEffects(
					ctx,
					repo.ID,
					ch.Slug,
					synchronousSearch,
					func() error {
						if !wikiChangeNeedsReferenceSync(ch, result.ReferenceEffectsPending) {
							return nil
						}
						if err := s.syncWikiPageReferences(ctx, repo, ch.Slug, body, result.CommittedAt); err != nil {
							return fmt.Errorf("sync issue refs for %s: %w", ch.Slug, err)
						}
						return nil
					},
					func() error {
						if err := s.syncWikiSearchUpsert(ctx, repo.FullName, page); err != nil {
							return fmt.Errorf("upsert search document for %s: %w", ch.Slug, err)
						}
						return nil
					},
				); err != nil {
					return err
				}
			} else if synchronousSearch {
				if err := s.syncWikiSearchUpsert(ctx, repo.FullName, page); err != nil {
					return fmt.Errorf("upsert search document for %s: %w", ch.Slug, err)
				}
			} else {
				stageStarted := time.Now()
				s.queueWikiSearchProjectionForRepo(ctx, repo.ID, ch.Slug)
				observeWikiWritePhase(ctx, wikiWritePhaseSearchEnqueue, stageStarted)
			}

		case wikicatalog.OpDelete:
			if err := s.runWikiReferenceAndSearchEffects(
				ctx,
				repo.ID,
				ch.Slug,
				synchronousSearch,
				func() error {
					if err := s.deleteIssueReferencesForWikiPage(ctx, repo.ID, ch.Slug); err != nil {
						return fmt.Errorf("delete issue refs for %s: %w", ch.Slug, err)
					}
					return nil
				},
				func() error {
					if err := s.syncWikiSearchDelete(ctx, repo.FullName, ch.Slug); err != nil {
						return fmt.Errorf("delete search document for %s: %w", ch.Slug, err)
					}
					return nil
				},
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) runWikiReferenceAndSearchEffects(
	ctx context.Context,
	repoID uint,
	slug string,
	synchronousSearch bool,
	referenceEffect func() error,
	synchronousSearchEffect func() error,
) error {
	runReference := func() error {
		stageStarted := time.Now()
		err := referenceEffect()
		observeWikiWritePhase(ctx, wikiWritePhaseReferenceSync, stageStarted)
		return err
	}
	if synchronousSearch {
		if err := runReference(); err != nil {
			return err
		}
		return synchronousSearchEffect()
	}

	runSearchEnqueue := func() error {
		stageStarted := time.Now()
		s.queueWikiSearchProjectionForRepo(ctx, repoID, slug)
		observeWikiWritePhase(ctx, wikiWritePhaseSearchEnqueue, stageStarted)
		return nil
	}
	return runWikiEffectsConcurrently(runReference, runSearchEnqueue)
}

func runWikiEffectsConcurrently(referenceEffect, searchEffect func() error) error {
	referenceDone := make(chan error, 1)
	go func() {
		referenceDone <- referenceEffect()
	}()

	searchErr := searchEffect()
	referenceErr := <-referenceDone
	if referenceErr != nil {
		return referenceErr
	}
	return searchErr
}

func wikiChangeNeedsReferenceSync(ch wikicatalog.ChangeResult, recoveryPending bool) bool {
	if ch.Op != wikicatalog.OpUpsert {
		return true
	}
	switch ch.UpsertDisposition {
	case wikicatalog.UpsertDispositionCreate:
		return recoveryPending
	default:
		// Updates and restores must clear references removed from the previous body.
		return true
	}
}

func wikiChangesetAlreadyInGit(source wikicatalog.Source) bool {
	return source == wikicatalog.SourceGit || source == wikicatalog.SourcePush
}

// markPreparedWikiChangesetMaterialized supports non-REST callers that publish
// an overridden commit outside the prepared REST write path. Prepared REST
// changesets store format 1 in their original transaction and do not call this.
func (s *Service) markPreparedWikiChangesetMaterialized(ctx context.Context, changesetID uint64, commitSHA string) error {
	result := s.DBForCtx(ctx).Model(&db.WikiChangeset{}).
		Where("changeset_id = ? AND synth_commit_sha = ?", changesetID, commitSHA).
		UpdateColumn("synth_format_ver", synthProjectionMaterialized)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("prepared changeset %d with commit %s not found", changesetID, commitSHA)
	}
	return nil
}

// markWikiChangesetMaterialized also backfills revision SHAs for legacy or
// recovered changesets that were not created through the prepared REST path.
func (s *Service) markWikiChangesetMaterialized(ctx context.Context, changesetID uint64, commitSHA string) error {
	return s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&db.WikiChangeset{}).
			Where("changeset_id = ?", changesetID).
			Updates(map[string]any{
				"synth_commit_sha": commitSHA,
				"synth_format_ver": synthProjectionMaterialized,
			}).Error; err != nil {
			return err
		}
		return tx.Model(&db.WikiPageRevision{}).
			Where("changeset_id = ?", changesetID).
			UpdateColumn("commit_sha", commitSHA).Error
	})
}

// materializeChangesetToGit projects a catalog changeset onto the
// legacy wiki bare repo as a single git commit. This keeps `git
// clone` and `git pull` consistent with the catalog after the
// REST-write cutover; the catalog is SOT, git is a materialized
// projection.
//
// Precondition: the caller already holds Git.WithRepoLock for the
// wiki repo. The service-layer write entry points (PutWikiPage,
// DeleteWikiPage, MoveWikiPage, MoveWikiPagePrefix) all take the
// lock around their ApplyChangeSet call so concurrent post-commit
// hooks land git commits in the same order they landed in the
// catalog; re-locking here would deadlock because the gitstore
// mutex is not reentrant.
func (s *Service) materializeChangesetToGit(
	ctx context.Context,
	git *gitstore.Store,
	blobStore *wikicatalog.BlobStore,
	repoFullName string,
	result wikicatalog.ChangeSetResult,
) error {
	full := wikiRepoFullName(repoFullName)
	if err := ensureWikiRepoWithGit(ctx, git, repoFullName); err != nil {
		return err
	}
	mutations := make([]gitstore.FileMutation, 0, len(result.Changes)*2)
	for _, ch := range result.Changes {
		switch ch.Op {
		case wikicatalog.OpUpsert:
			body := string(ch.Body)
			ok := ch.BodyAvailable
			if !ok {
				body, ok = s.wikiBodyForReindex(ctx, blobStore, ch.PageID, ch.BlobSHA)
			}
			if !ok {
				return fmt.Errorf("body unavailable for upsert of %q", ch.Slug)
			}
			mutations = append(mutations, gitstore.FileMutation{
				Path:    wikiSlugToPath(ch.Slug),
				Content: []byte(body),
			})
		case wikicatalog.OpDelete:
			mutations = append(mutations, gitstore.FileMutation{
				Path:   wikiSlugToPath(ch.PrevSlug),
				Delete: true,
			})
		case wikicatalog.OpRename:
			body := string(ch.Body)
			ok := ch.BodyAvailable
			if !ok {
				body, ok = s.wikiBodyForReindex(ctx, blobStore, ch.PageID, ch.BlobSHA)
			}
			if !ok {
				return fmt.Errorf("body unavailable for rename of %q", ch.Slug)
			}
			mutations = append(mutations,
				gitstore.FileMutation{Path: wikiSlugToPath(ch.PrevSlug), Delete: true},
				gitstore.FileMutation{Path: wikiSlugToPath(ch.Slug), Content: []byte(body)},
			)
		}
	}
	if len(mutations) == 0 {
		return nil
	}
	gitSHA, err := git.CommitFilesAt(ctx, full, wikiDefaultBranch, result.Message, mutations, result.CommittedAt)
	if err != nil {
		return err
	}
	// Reconcile the catalog's synth_commit_sha (and the per-revision
	// commit_sha) with the materialized git commit SHA. Two effects:
	//   1. A subsequent IngestWikiGit run sees this changeset as
	//      already-ingested and skips it; without this IngestWikiGit
	//      would double-apply every runtime write.
	//   2. wiki_page_revisions.commit_sha matches the real git SHA so
	//      ref-pinned reads (GetWikiPageAtRef) and the history
	//      endpoint resolve correctly.
	//
	// Both UPDATEs run in one transaction so a back-to-back write burst
	// applies the materialization state atomically per ApplyChangeSet.
	return s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&db.WikiChangeset{}).
			Where("changeset_id = ?", result.ChangesetID).
			Updates(map[string]any{
				"synth_commit_sha": gitSHA,
				"synth_format_ver": synthProjectionMaterialized,
			}).Error; err != nil {
			return err
		}
		return tx.Model(&db.WikiPageRevision{}).
			Where("changeset_id = ?", result.ChangesetID).
			UpdateColumn("commit_sha", gitSHA).Error
	})
}

// wikiBodyForReindex retrieves the latest body for a page, preferring
// the inline copy on the page row. Used only by the post-commit
// search hook; production reads go through the catalog API.
func (s *Service) wikiBodyForReindex(ctx context.Context, blobStore *wikicatalog.BlobStore, pageID uint64, blobSHA string) (string, bool) {
	var p db.WikiPage
	if err := s.DBForCtx(ctx).Select("body_inline").
		First(&p, "page_id = ?", pageID).Error; err != nil {
		return "", false
	}
	if p.BodyInline != nil {
		return string(p.BodyInline), true
	}
	if blobSHA == "" {
		return "", false
	}
	body, err := blobStore.Get(ctx, blobSHA)
	if err != nil {
		return "", false
	}
	return string(body), true
}
