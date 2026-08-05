package wikicatalog

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"

	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// uploadBlobConcurrency bounds the number of parallel CAS writes per
// ApplyChangeSet. Small enough to avoid spawning thousands of
// goroutines on a 10k-page batch import, large enough to soak filesystem
// write latency.
const uploadBlobConcurrency = 8

// ApplyChangeSet is the direct write entry point for the wiki catalog. Git
// replay and other callers without an external prepare phase use it directly;
// REST mutations use PrepareChangeSet followed by ApplyPreparedChangeSet. The
// shared contract:
//
//   - Validates inputs and rejects malformed slugs / intra-changeset
//     duplicates before any state touches the database.
//   - Resolves conflicts against a pre-read snapshot: stale IfMatch,
//     rename-destination occupied, prefix collision, delete-of-missing.
//     The first conflict is returned as a typed *ConflictError or
//     ErrPageNotFound; no partial application occurs.
//   - Uploads blobs to the content-addressed store before opening the
//     SQL transaction. A failed upload aborts the whole changeset and
//     leaves the catalog unchanged. Uploaded blobs are tracked in
//     wiki_pending_blobs so the GC can reclaim them if the SQL
//     transaction fails after them.
//   - Commits all catalog mutations in one SQL transaction. Inside
//     that transaction the per-repo head row is updated under CAS
//     against either the caller's ExpectedParent or the parent row
//     observed at read time. On CAS failure ApplyChangeSet retries up
//     to MaxCASRetries times unless the caller pinned ExpectedParent,
//     in which case the first loss surfaces as ErrCASLost.
//
// All other invariants (refcount maintenance, link rewrites, slug aliases for
// renames, label remapping) are kept inside the same SQL transaction. There is
// no dual-write.
func (c *Catalog) ApplyChangeSet(ctx context.Context, req ChangeSetRequest) (ChangeSetResult, error) {
	plan, err := c.planChangeSet(req)
	if err != nil {
		return ChangeSetResult{}, err
	}
	return c.applyPlannedChangeSet(ctx, plan, nil, nil)
}

// ApplyPreparedChangeSet applies a validated snapshot returned by
// PrepareChangeSet or ValidateChangeSetSnapshot. overrideCommitSHA may be empty
// to retain the request's original behavior, or may carry the Git commit
// prepared between validation and apply.
//
// The snapshot is never refreshed internally. If another writer advances the
// repository head after preflight, this method returns ErrCASLost so the caller
// can restart both Git preparation and catalog validation from one new parent.
func (c *Catalog) ApplyPreparedChangeSet(
	ctx context.Context,
	prepared *PreparedChangeSet,
	overrideCommitSHA string,
) (ChangeSetResult, error) {
	return c.applyPreparedChangeSet(ctx, prepared, overrideCommitSHA, nil)
}

// ApplyPreparedChangeSetWithCommitBarrier is the prepared-write variant used
// when an external durable operation can run alongside the catalog
// transaction. beforeCommit runs after all catalog mutations but before the
// SQL transaction commits. A barrier error rolls the transaction back.
func (c *Catalog) ApplyPreparedChangeSetWithCommitBarrier(
	ctx context.Context,
	prepared *PreparedChangeSet,
	overrideCommitSHA string,
	beforeCommit func() error,
) (ChangeSetResult, error) {
	if beforeCommit == nil {
		return ChangeSetResult{}, errors.New("wiki catalog: commit barrier required")
	}
	return c.applyPreparedChangeSet(ctx, prepared, overrideCommitSHA, beforeCommit)
}

func (c *Catalog) applyPreparedChangeSet(
	ctx context.Context,
	prepared *PreparedChangeSet,
	overrideCommitSHA string,
	beforeCommit func() error,
) (ChangeSetResult, error) {
	if prepared == nil || prepared.ChangeSetSnapshot == nil || prepared.plan.repoID == 0 {
		return ChangeSetResult{}, errors.New("wiki catalog: prepared changeset required")
	}

	plan := prepared.plan
	overrideSHA := strings.ToLower(strings.TrimSpace(overrideCommitSHA))
	if overrideSHA != "" {
		if err := validateSHA(overrideSHA); err != nil {
			return ChangeSetResult{}, fmt.Errorf("wiki catalog: override commit SHA: %w", err)
		}
		plan.overrideCommitSHA = overrideSHA
	}

	// Pin the transaction to the exact catalog parent that backs preRead.
	// A zero token represents a repository with no catalog head yet.
	expectedParent := prepared.headChangesetID
	plan.parentExpect = &expectedParent
	return c.applyPlannedChangeSet(ctx, plan, &prepared.preRead, beforeCommit)
}

func (c *Catalog) applyPlannedChangeSet(
	ctx context.Context,
	plan changesetPlan,
	preparedPreRead *preReadPages,
	beforeCommit func() error,
) (ChangeSetResult, error) {

	// Compute per-change blob SHAs up front so the SQL phase always
	// knows the new head SHA without re-hashing. blobBySlug is also used
	// for the synthetic commit SHA. OpRename normally carries the
	// existing blob forward; when the caller provides ch.body on a
	// rename, we treat it as a body update applied atomically with
	// the slug move (the prefix-move planner uses this so a moved
	// page whose body references another moved slug lands the
	// rewritten content under the new slug).
	blobBySlug := make(map[string]string, len(plan.changes))
	for _, ch := range plan.changes {
		switch ch.op {
		case OpUpsert:
			blobBySlug[ch.srcSlug] = HashContent(ch.body)
		case OpRename:
			if len(ch.body) > 0 {
				blobBySlug[ch.srcSlug] = HashContent(ch.body)
			}
		}
	}

	// Upload non-inline blobs and record pending WAL rows. We do this
	// before opening the SQL txn so that a slow CAS upload does not
	// hold a transaction lock; the WAL rows make orphan reclamation
	// straightforward if the txn later fails.
	stageStarted := time.Now()
	err := c.uploadBlobs(ctx, plan, blobBySlug)
	observeApplyPhase(ctx, ApplyPhaseBlobUpload, stageStarted)
	if err != nil {
		return ChangeSetResult{}, err
	}

	maxAttempts := c.MaxCASRetries
	if maxAttempts <= 0 {
		maxAttempts = 5
	}

	var result ChangeSetResult
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var (
			casLost bool
			txErr   error
		)
		result, casLost, txErr = c.applyOnce(ctx, plan, blobBySlug, preparedPreRead, beforeCommit)
		if txErr != nil {
			return ChangeSetResult{}, txErr
		}
		if !casLost {
			// Catalog state is committed. Drive post-commit side
			// effects (search reindex, etc.) via the optional hook.
			// A hook error propagates to the caller but does not
			// roll back the changeset.
			if c.OnChangeSetCommitted != nil {
				stageStarted = time.Now()
				err := c.OnChangeSetCommitted(ctx, plan.repoID, result)
				observeApplyPhase(ctx, ApplyPhasePostCommit, stageStarted)
				if err != nil {
					return result, fmt.Errorf("wiki catalog: post-commit hook: %w", err)
				}
			}
			return result, nil
		}
		// CAS loser. If caller pinned the parent, surface the loss;
		// otherwise refresh by re-pre-reading on the next iteration.
		if plan.parentExpect != nil {
			return ChangeSetResult{}, ErrCASLost
		}
	}
	return ChangeSetResult{}, ErrCASLost
}

// PrepareChangeSet validates a changeset and returns the conflict snapshot
// needed to commit it after an external side effect such as preparing a Git
// object. It always captures the repository head along with page state.
//
// The head and touched pages are read in one database round trip. Prefix
// conflicts remain a separate indexed query because they inspect directory
// entries outside the exact touched-slug set.
func (c *Catalog) PrepareChangeSet(ctx context.Context, req ChangeSetRequest) (*PreparedChangeSet, error) {
	snapshot, err := c.SnapshotChangeSet(ctx, req)
	if err != nil {
		return nil, err
	}
	return c.ValidateChangeSetSnapshot(ctx, snapshot)
}

// SnapshotChangeSet validates request shape and reads the repository head,
// touched pages, and projection state in one round trip. The returned snapshot
// must pass ValidateChangeSetSnapshot before it can be applied.
func (c *Catalog) SnapshotChangeSet(ctx context.Context, req ChangeSetRequest) (*ChangeSetSnapshot, error) {
	plan, err := c.planChangeSet(req)
	if err != nil {
		return nil, err
	}
	headProjection, preRead, err := loadPreflightSnapshot(c.db(ctx), plan)
	if err != nil {
		return nil, err
	}
	preRead.markPlannedInbound(plan.changes)
	return &ChangeSetSnapshot{
		plan:            plan,
		headChangesetID: headProjection.ChangesetID,
		headProjection:  headProjection,
		preRead:         preRead,
	}, nil
}

// ValidateChangeSetSnapshot checks request conflicts against a snapshot and
// returns the only type accepted by ApplyPreparedChangeSet.
func (c *Catalog) ValidateChangeSetSnapshot(
	ctx context.Context,
	snapshot *ChangeSetSnapshot,
) (*PreparedChangeSet, error) {
	if snapshot == nil || snapshot.plan.repoID == 0 {
		return nil, errors.New("wiki catalog: changeset snapshot required")
	}
	plan := snapshot.plan
	if plan.parentExpect != nil && *plan.parentExpect != snapshot.headChangesetID {
		return nil, ErrCASLost
	}
	if err := c.checkConflicts(c.db(ctx), plan, snapshot.preRead); err != nil {
		return nil, err
	}
	return &PreparedChangeSet{ChangeSetSnapshot: snapshot}, nil
}

// applyOnce performs a single transaction attempt. casLost == true
// indicates the wiki_repo_heads CAS lost; the caller decides whether
// to retry.
func (c *Catalog) applyOnce(
	ctx context.Context,
	plan changesetPlan,
	blobBySlug map[string]string,
	preparedPreRead *preReadPages,
	beforeCommit func() error,
) (ChangeSetResult, bool, error) {
	var (
		result                  ChangeSetResult
		casLost                 bool
		transactionBodyDuration time.Duration
	)
	transactionStarted := time.Now()
	err := c.db(ctx).Transaction(func(tx *gorm.DB) error {
		bodyStarted := time.Now()
		defer func() {
			transactionBodyDuration = time.Since(bodyStarted)
			recordApplyPhase(ctx, ApplyPhaseTransactionBody, transactionBodyDuration)
		}()
		// Prepared writes already carry the head token captured with their
		// preflight snapshot. Use that token directly and let the head update
		// below perform the CAS; if it loses, the changeset insert and every
		// catalog mutation roll back with the transaction. Unprepared callers
		// still lock and load the head because they have no trusted snapshot.
		var parentID *uint64
		var currentParent uint64
		headExists := false
		referenceEffectsAlreadyPending := false
		preparedHead := preparedPreRead != nil && plan.parentExpect != nil
		if preparedHead {
			currentParent = *plan.parentExpect
			headExists = currentParent != 0
			if headExists {
				parent := currentParent
				parentID = &parent
				referenceEffectsAlreadyPending = preparedPreRead.referenceEffectsAlreadyPending
			}
		} else {
			head, exists, err := loadHeadForUpdate(tx, plan.repoID)
			if err != nil {
				return err
			}
			headExists = exists
			if headExists {
				currentParent = head.HeadChangesetID
				parentID = &head.HeadChangesetID
				referenceEffectsAlreadyPending = head.ReferenceEffectsThroughChangesetID != nil
			}
		}
		if plan.parentExpect != nil && !preparedHead {
			expected := *plan.parentExpect
			if expected != currentParent {
				casLost = true
				return errSentinelCASLost
			}
		}
		// Test-only hook: simulate a CAS-lost transaction without
		// depending on scheduler timing. Never set in production code.
		if c.testForceCASLoss != nil && c.testForceCASLoss() {
			casLost = true
			return errSentinelCASLost
		}

		var preRead preReadPages
		if preparedPreRead != nil {
			// parentExpect above is the token captured with this snapshot.
			// Every catalog writer advances wiki_repo_heads transactionally,
			// so a token match makes the preflight page and directory reads
			// valid here without issuing them again.
			preRead = *preparedPreRead
		} else {
			// 2. Pre-read all touched pages.
			loaded, err := loadPagesBySlug(tx, plan.repoID, plan.touchedSlugs)
			if err != nil {
				return err
			}
			preRead = loaded

			// 3. Conflict checks (read-only, no mutations yet).
			if err := c.checkConflicts(tx, plan, preRead); err != nil {
				return err
			}
		}

		// 4. Insert the changeset row. The synth commit SHA is
		// deterministic from inputs so retries within the OCC loop
		// don't produce drifting SHAs across attempts. Git-originated
		// changesets may override the SHA with the original git commit SHA.
		var synthSHA string
		if plan.overrideCommitSHA != "" {
			synthSHA = plan.overrideCommitSHA
		} else {
			synthSHA = computeSynthCommitSHA(plan.repoID, parentID, plan.committedAt, plan.message, plan.changes, blobBySlug)
		}
		referenceEffectsPending := changeSetNeedsReferenceEffects(plan, preRead)
		cs := db.WikiChangeset{
			RepositoryID:   plan.repoID,
			ParentID:       parentID,
			Message:        db.LargeText(plan.message),
			AuthorID:       plan.authorID,
			CommittedAt:    plan.committedAt,
			PageCount:      len(plan.changes),
			Source:         string(plan.source),
			SynthCommitSHA: synthSHA,
			SynthFormatVer: 0,
		}
		// An override pins the changeset to a real Git commit object. Prepared
		// REST writes reach the commit barrier below before this transaction can
		// commit, so recovery can use the stored SHA directly even when ref
		// publication is interrupted after the catalog commit.
		if plan.overrideCommitSHA != "" {
			cs.SynthFormatVer = 1
		}
		stageStarted := time.Now()
		err := tx.Create(&cs).Error
		observeApplyPhase(ctx, ApplyPhaseChangesetInsert, stageStarted)
		if err != nil {
			return err
		}
		// 5. Update wiki_repo_heads under CAS, or insert if new wiki.
		stageStarted = time.Now()
		headErr := func() error {
			rejectNewRepairObligation := preparedPreRead != nil && preparedPreRead.gitRepairObligationAbsent
			if !headExists {
				if rejectNewRepairObligation {
					var referenceEffectsThrough any
					if referenceEffectsPending {
						referenceEffectsThrough = cs.ChangesetID
					}
					res := tx.Exec(`
INSERT INTO wiki_repo_heads (repository_id, head_changeset_id, reference_effects_through_changeset_id, updated_at)
SELECT ?, ?, ?, ?
WHERE NOT EXISTS (
	SELECT 1
	FROM wiki_git_repair_obligations
	WHERE repository_id = ?
)
AND NOT EXISTS (
	SELECT 1
	FROM wiki_repo_heads
	WHERE repository_id = ?
)`,
						plan.repoID,
						cs.ChangesetID,
						referenceEffectsThrough,
						plan.committedAt,
						plan.repoID,
						plan.repoID,
					)
					if res.Error != nil {
						return res.Error
					}
					if res.RowsAffected != 1 {
						casLost = true
						return errSentinelCASLost
					}
					return nil
				}
				head := db.WikiRepoHead{
					RepositoryID:    plan.repoID,
					HeadChangesetID: cs.ChangesetID,
					UpdatedAt:       plan.committedAt,
				}
				if referenceEffectsPending {
					head.ReferenceEffectsThroughChangesetID = &cs.ChangesetID
				}
				if err := tx.Create(&head).Error; err != nil {
					// Concurrent first writer raced and won. Treat as CAS loss.
					casLost = true
					return errSentinelCASLost
				}
				return nil
			}
			updates := map[string]any{
				"head_changeset_id": cs.ChangesetID,
				"updated_at":        plan.committedAt,
			}
			if referenceEffectsPending {
				updates["reference_effects_through_changeset_id"] = cs.ChangesetID
			}
			query := tx.Model(&db.WikiRepoHead{}).
				Where("repository_id = ? AND head_changeset_id = ?", plan.repoID, currentParent)
			if rejectNewRepairObligation {
				query = query.Where(`
NOT EXISTS (
	SELECT 1
	FROM wiki_git_repair_obligations
	WHERE repository_id = ?
)`, plan.repoID)
			}
			res := query.Updates(updates)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				casLost = true
				return errSentinelCASLost
			}
			return nil
		}()
		observeApplyPhase(ctx, ApplyPhaseHeadCAS, stageStarted)
		if headErr != nil {
			return headErr
		}

		// 6. Apply each change.
		result = ChangeSetResult{
			ChangesetID:             cs.ChangesetID,
			ParentID:                parentID,
			CommitSHA:               synthSHA,
			CommitSHAOverridden:     plan.overrideCommitSHA != "",
			Message:                 plan.message,
			CommittedAt:             plan.committedAt,
			Source:                  plan.source,
			Changes:                 make([]ChangeResult, 0, len(plan.changes)),
			ReferenceEffectsPending: referenceEffectsPending,
			ReferenceEffectsCoalesced: referenceEffectsPending &&
				referenceEffectsAlreadyPending,
		}
		stageStarted = time.Now()
		changesErr := func() error {
			for _, ch := range plan.changes {
				cr, err := c.applyChange(ctx, tx, plan, &cs, ch, preRead, blobBySlug)
				if err != nil {
					return err
				}
				result.Changes = append(result.Changes, cr)
			}
			return nil
		}()
		observeApplyPhase(ctx, ApplyPhaseChanges, stageStarted)
		if changesErr != nil {
			return changesErr
		}
		if beforeCommit != nil {
			stageStarted = time.Now()
			err := beforeCommit()
			observeApplyPhase(ctx, ApplyPhaseCommitBarrier, stageStarted)
			if err != nil {
				return fmt.Errorf("wiki catalog: commit barrier: %w", err)
			}
		}
		return nil
	})
	transactionDuration := time.Since(transactionStarted)
	recordApplyPhase(ctx, ApplyPhaseTransaction, transactionDuration)
	// GORM owns BEGIN and COMMIT/ROLLBACK around the callback. Their combined
	// cost, including connection acquisition, is the measurable boundary here.
	boundaryDuration := transactionDuration - transactionBodyDuration
	if boundaryDuration < 0 {
		boundaryDuration = 0
	}
	recordApplyPhase(ctx, ApplyPhaseTransactionBoundary, boundaryDuration)
	if errors.Is(err, errSentinelCASLost) {
		return ChangeSetResult{}, true, nil
	}
	if err != nil {
		return ChangeSetResult{}, false, err
	}
	return result, casLost, nil
}

func changeSetNeedsReferenceEffects(plan changesetPlan, preRead preReadPages) bool {
	for _, ch := range plan.changes {
		switch ch.op {
		case OpDelete, OpRename:
			return true
		case OpUpsert:
			if _, ok := preRead.live[ch.srcSlug]; ok {
				return true
			}
			if _, ok := preRead.tombs[ch.srcSlug]; ok {
				return true
			}
			if bodyMayContainReferenceEffect(ch.body) {
				return true
			}
		}
	}
	return false
}

func bodyMayContainReferenceEffect(body []byte) bool {
	for i := 0; i+1 < len(body); i++ {
		if body[i] == '#' && body[i+1] >= '0' && body[i+1] <= '9' {
			return true
		}
	}
	return bytes.Contains(body, []byte("/issues/")) || bytes.Contains(body, []byte("/pull/"))
}

// errSentinelCASLost is an internal sentinel used to roll back a
// transaction when the head CAS loses. The outer loop translates it
// to either a retry or ErrCASLost depending on caller intent.
var errSentinelCASLost = errors.New("wiki catalog internal: CAS lost")

func (c *Catalog) uploadBlobs(ctx context.Context, plan changesetPlan, blobBySlug map[string]string) error {
	// Invariant for the GC story: bodies with size <= MaxBodyInlineBytes
	// live exclusively in wiki_pages.body_inline / wiki_page_revisions
	// .body_inline. They are NOT written to the CAS filesystem and
	// thus have no pending-blob WAL row — there is nothing on disk to
	// reclaim if the transaction later fails. Larger bodies go
	// through the CAS and get a WAL row that the GC can find.
	//
	// wiki_blob_refs tracks only the larger bodies that materialize in
	// the filesystem CAS. Inline bodies need neither pending WAL rows
	// nor refcount writes.
	// Independent per-blob work runs in parallel with bounded
	// concurrency. The legacy serial loop was the single biggest
	// constant-factor cost in batch upserts and git replays.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(uploadBlobConcurrency)
	for _, ch := range plan.changes {
		// OpUpsert always carries a new body. OpRename carries one
		// only when the caller is doing rename-with-body-update.
		// Other ops have nothing to upload.
		if ch.op != OpUpsert && !(ch.op == OpRename && len(ch.body) > 0) {
			continue
		}
		size := len(ch.body)
		if size <= MaxBodyInlineBytes {
			continue
		}
		ch := ch
		sha := blobBySlug[ch.srcSlug]
		g.Go(func() error {
			pending := db.WikiPendingBlob{
				BlobSHA:   sha,
				WrittenAt: c.Now(),
				Size:      size,
			}
			if err := c.db(gctx).
				Clauses(clause.OnConflict{DoNothing: true}).
				Create(&pending).Error; err != nil {
				return fmt.Errorf("wiki catalog: record pending blob: %w", err)
			}
			if _, err := c.Blob.Put(gctx, ch.body); err != nil {
				return fmt.Errorf("wiki catalog: upload blob: %w", err)
			}
			return nil
		})
	}
	return g.Wait()
}

func loadHeadForUpdate(tx *gorm.DB, repoID uint) (db.WikiRepoHead, bool, error) {
	var head db.WikiRepoHead
	q := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("repository_id = ?", repoID).
		Take(&head)
	if err := q.Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return db.WikiRepoHead{}, false, nil
		}
		return db.WikiRepoHead{}, false, err
	}
	return head, true, nil
}

func loadHead(tx *gorm.DB, repoID uint) (db.WikiRepoHead, bool, error) {
	var head db.WikiRepoHead
	q := tx.Where("repository_id = ?", repoID).Take(&head)
	if err := q.Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return db.WikiRepoHead{}, false, nil
		}
		return db.WikiRepoHead{}, false, err
	}
	return head, true, nil
}

// preReadPages partitions the catalog rows touched by a changeset into live
// and tombstoned pages. A prepared snapshot also records which touched slugs
// have unresolved inbound links, allowing create/restore to skip a no-op
// UPDATE while the repo-head token still protects the observation.
type preReadPages struct {
	live                           map[string]db.WikiPage
	tombs                          map[string]db.WikiPage
	unresolvedInbound              map[string]struct{}
	inboundKnown                   bool
	prefixSlugs                    []string
	prefixKnown                    bool
	outlinkTargets                 map[string]uint64
	outlinksKnown                  bool
	referenceEffectsAlreadyPending bool
	gitRepairObligationAbsent      bool
}

func (p preReadPages) needsInboundResolution(slug string) bool {
	if !p.inboundKnown {
		return true
	}
	_, ok := p.unresolvedInbound[slug]
	return ok
}

// markPlannedInbound preserves correctness for multi-change requests. An
// earlier change in the same transaction can create a new unresolved link
// that was not present in the database snapshot.
func (p *preReadPages) markPlannedInbound(changes []plannedChange) {
	for _, ch := range changes {
		if len(ch.body) == 0 {
			continue
		}
		for _, slug := range ExtractOutlinks(string(ch.body)) {
			p.unresolvedInbound[slug] = struct{}{}
		}
	}
}

type preflightSnapshotRow struct {
	RowKind                            int
	HeadChangesetID                    sql.NullInt64
	HeadSource                         sql.NullString
	HeadSynthCommitSHA                 sql.NullString
	HeadSynthFormatVersion             sql.NullInt64
	PendingProjectionCount             sql.NullInt64
	ReferenceEffectsThroughChangesetID sql.NullInt64
	GitRepairObligationExists          bool
	PageID                             sql.NullInt64
	Slug                               sql.NullString
	HeadBlobSHA                        sql.NullString
	BodySize                           sql.NullInt64
	BodyInline                         []byte
	HeadRevisionID                     sql.NullInt64
	DeletedAt                          sql.NullTime
}

const materializedSynthFormatVersion int16 = 1

// loadPreflightSnapshot reads the repository-head token and every exact page
// touched by a planned changeset in one round trip. For the latency-sensitive
// single-upsert path, the same statement also captures live page-prefix state,
// and live outbound-link targets. The head token protects every row in this
// statement: ApplyPreparedChangeSet rejects the snapshot if any catalog writer
// advances the repository before the transaction starts. The service consumes
// the repair-obligation flag while holding its repository serialization lock.
func loadPreflightSnapshot(tx *gorm.DB, plan changesetPlan) (HeadProjectionState, preReadPages, error) {
	out := preReadPages{
		live:              map[string]db.WikiPage{},
		tombs:             map[string]db.WikiPage{},
		unresolvedInbound: map[string]struct{}{},
		inboundKnown:      true,
		outlinkTargets:    map[string]uint64{},
	}
	if len(plan.touchedSlugs) == 0 {
		headProjection, err := loadHeadProjectionSnapshot(tx, plan.repoID)
		out.referenceEffectsAlreadyPending = headProjection.ReferenceEffectsThroughChangesetID != nil
		out.gitRepairObligationAbsent = !headProjection.GitRepairObligationExists
		return headProjection, out, err
	}

	snapshotSQL := `
SELECT
	0 AS row_kind,
	h.head_changeset_id,
	current.source AS head_source,
	current.synth_commit_sha AS head_synth_commit_sha,
	current.synth_format_ver AS head_synth_format_version,
	(
		SELECT COUNT(*)
		FROM wiki_changesets AS pending
		WHERE pending.repository_id = r.id
			AND pending.synth_format_ver < ?
			AND pending.source IN ?
	) AS pending_projection_count,
	h.reference_effects_through_changeset_id,
	EXISTS (
		SELECT 1
		FROM wiki_git_repair_obligations AS repair
		WHERE repair.repository_id = r.id
	) AS git_repair_obligation_exists,
	NULL AS page_id,
	NULL AS slug,
	NULL AS head_blob_sha,
	NULL AS body_size,
	NULL AS body_inline,
	NULL AS head_revision_id,
	NULL AS deleted_at
FROM repositories AS r
LEFT JOIN wiki_repo_heads AS h ON h.repository_id = r.id
LEFT JOIN wiki_changesets AS current
	ON current.changeset_id = h.head_changeset_id
	AND current.repository_id = r.id
WHERE r.id = ?
UNION ALL
SELECT
	1 AS row_kind,
	NULL AS head_changeset_id,
	NULL AS head_source,
	NULL AS head_synth_commit_sha,
	NULL AS head_synth_format_version,
	NULL AS pending_projection_count,
	NULL AS reference_effects_through_changeset_id,
	FALSE AS git_repair_obligation_exists,
	p.page_id,
	p.slug,
	p.head_blob_sha,
	p.body_size,
	p.body_inline,
	p.head_revision_id,
	p.deleted_at
FROM wiki_pages AS p
WHERE p.repository_id = ? AND p.slug IN ?
UNION ALL
SELECT
	2 AS row_kind,
	NULL AS head_changeset_id,
	NULL AS head_source,
	NULL AS head_synth_commit_sha,
	NULL AS head_synth_format_version,
	NULL AS pending_projection_count,
	NULL AS reference_effects_through_changeset_id,
	FALSE AS git_repair_obligation_exists,
	NULL AS page_id,
	l.dst_slug AS slug,
	NULL AS head_blob_sha,
	NULL AS body_size,
	NULL AS body_inline,
	NULL AS head_revision_id,
	NULL AS deleted_at
FROM wiki_page_links AS l
WHERE l.repository_id = ? AND l.dst_slug IN ? AND l.dst_page_id IS NULL
GROUP BY l.dst_slug`
	args := []any{
		materializedSynthFormatVersion,
		[]string{
			string(SourceREST),
			string(SourceAdmin),
			string(SourceBatch),
		},
		plan.repoID,
		plan.repoID,
		plan.touchedSlugs,
		plan.repoID,
		plan.touchedSlugs,
	}

	if slug, outlinks, ok := singleUpsertSnapshotInputs(plan); ok {
		out.prefixKnown = true
		out.outlinksKnown = true

		prefixPredicate, prefixArgs := pagePrefixCollisionPredicate("prefix_page.slug", slug)
		snapshotSQL += `
UNION ALL
SELECT
	3 AS row_kind,
	NULL AS head_changeset_id,
	NULL AS head_source,
	NULL AS head_synth_commit_sha,
	NULL AS head_synth_format_version,
	NULL AS pending_projection_count,
	NULL AS reference_effects_through_changeset_id,
	FALSE AS git_repair_obligation_exists,
	prefix_page.page_id,
	prefix_page.slug,
	NULL AS head_blob_sha,
	NULL AS body_size,
	NULL AS body_inline,
	NULL AS head_revision_id,
	NULL AS deleted_at
FROM wiki_pages AS prefix_page
WHERE prefix_page.repository_id = ?
	AND prefix_page.deleted_at IS NULL
	AND (` + prefixPredicate + `)`
		args = append(args, plan.repoID)
		args = append(args, prefixArgs...)

		if len(outlinks) > 0 {
			snapshotSQL += `
UNION ALL
SELECT
	4 AS row_kind,
	NULL AS head_changeset_id,
	NULL AS head_source,
	NULL AS head_synth_commit_sha,
	NULL AS head_synth_format_version,
	NULL AS pending_projection_count,
	NULL AS reference_effects_through_changeset_id,
	FALSE AS git_repair_obligation_exists,
	p.page_id,
	p.slug,
	NULL AS head_blob_sha,
	NULL AS body_size,
	NULL AS body_inline,
	NULL AS head_revision_id,
	NULL AS deleted_at
FROM wiki_pages AS p
WHERE p.repository_id = ? AND p.slug IN ? AND p.deleted_at IS NULL`
			args = append(args, plan.repoID, outlinks)
		}
	}

	var rows []preflightSnapshotRow
	if err := tx.Raw(snapshotSQL, args...).Scan(&rows).Error; err != nil {
		return HeadProjectionState{}, preReadPages{}, err
	}

	var headProjection HeadProjectionState
	for _, row := range rows {
		switch row.RowKind {
		case 0:
			headProjection = headProjectionFromSnapshotRow(row)
			out.referenceEffectsAlreadyPending = row.ReferenceEffectsThroughChangesetID.Valid
			out.gitRepairObligationAbsent = !row.GitRepairObligationExists
			continue
		case 2:
			if row.Slug.Valid {
				out.unresolvedInbound[row.Slug.String] = struct{}{}
			}
			continue
		case 4:
			if row.PageID.Valid && row.Slug.Valid {
				out.outlinkTargets[row.Slug.String] = uint64(row.PageID.Int64)
			}
			continue
		case 3:
			if row.Slug.Valid {
				out.prefixSlugs = append(out.prefixSlugs, row.Slug.String)
			}
			continue
		}
		if !row.PageID.Valid || !row.Slug.Valid {
			continue
		}
		page := db.WikiPage{
			PageID:         uint64(row.PageID.Int64),
			RepositoryID:   plan.repoID,
			Slug:           row.Slug.String,
			HeadBlobSHA:    row.HeadBlobSHA.String,
			BodySize:       int(row.BodySize.Int64),
			BodyInline:     row.BodyInline,
			HeadRevisionID: uint64(row.HeadRevisionID.Int64),
		}
		if row.DeletedAt.Valid {
			deletedAt := row.DeletedAt.Time
			page.DeletedAt = &deletedAt
			out.tombs[page.Slug] = page
			continue
		}
		out.live[page.Slug] = page
	}
	return headProjection, out, nil
}

func loadHeadProjectionSnapshot(tx *gorm.DB, repoID uint) (HeadProjectionState, error) {
	var row preflightSnapshotRow
	result := tx.Raw(`
SELECT
	h.head_changeset_id,
	current.source AS head_source,
	current.synth_commit_sha AS head_synth_commit_sha,
	current.synth_format_ver AS head_synth_format_version,
	(
		SELECT COUNT(*)
		FROM wiki_changesets AS pending
		WHERE pending.repository_id = r.id
			AND pending.synth_format_ver < ?
			AND pending.source IN ?
	) AS pending_projection_count,
	h.reference_effects_through_changeset_id,
	EXISTS (
		SELECT 1
		FROM wiki_git_repair_obligations AS repair
		WHERE repair.repository_id = r.id
	) AS git_repair_obligation_exists
FROM repositories AS r
LEFT JOIN wiki_repo_heads AS h ON h.repository_id = r.id
LEFT JOIN wiki_changesets AS current
	ON current.changeset_id = h.head_changeset_id
	AND current.repository_id = r.id
WHERE r.id = ?`,
		materializedSynthFormatVersion,
		[]string{
			string(SourceREST),
			string(SourceAdmin),
			string(SourceBatch),
		},
		repoID,
	).Scan(&row)
	if result.Error != nil {
		return HeadProjectionState{}, result.Error
	}
	return headProjectionFromSnapshotRow(row), nil
}

func headProjectionFromSnapshotRow(row preflightSnapshotRow) HeadProjectionState {
	state := HeadProjectionState{
		GitRepairObligationExists: row.GitRepairObligationExists,
	}
	if !row.HeadChangesetID.Valid {
		return state
	}
	var referenceEffectsThrough *uint64
	if row.ReferenceEffectsThroughChangesetID.Valid {
		through := uint64(row.ReferenceEffectsThroughChangesetID.Int64)
		referenceEffectsThrough = &through
	}
	state.Exists = true
	state.ChangesetID = uint64(row.HeadChangesetID.Int64)
	state.CommitSHA = row.HeadSynthCommitSHA.String
	state.Source = Source(row.HeadSource.String)
	state.SynthFormatVersion = int16(row.HeadSynthFormatVersion.Int64)
	state.PendingProjectionCount = row.PendingProjectionCount.Int64
	state.ReferenceEffectsThroughChangesetID = referenceEffectsThrough
	return state
}

func singleUpsertSnapshotInputs(plan changesetPlan) (string, []string, bool) {
	if len(plan.changes) != 1 || plan.changes[0].op != OpUpsert {
		return "", nil, false
	}
	ch := plan.changes[0]
	return ch.srcSlug, ExtractOutlinks(string(ch.body)), true
}

func pagePrefixCollisionPredicate(slugColumn, slug string) (string, []any) {
	clauses := make([]string, 0, 2)
	args := make([]any, 0, 3)
	if ancestors := parentChain(slug); len(ancestors) > 0 {
		clauses = append(clauses, slugColumn+" IN ?")
		args = append(args, ancestors)
	}
	// Slugs are stored as VARBINARY. Every descendant starts with slug + "/",
	// and "0" is the byte immediately after "/" in ASCII, so this is an
	// indexable prefix range without SQL LIKE wildcard semantics.
	clauses = append(clauses, "("+slugColumn+" >= ? AND "+slugColumn+" < ?)")
	args = append(args, slug+"/", slug+"0")
	return strings.Join(clauses, " OR "), args
}

func loadPagesBySlug(tx *gorm.DB, repoID uint, slugs []string) (preReadPages, error) {
	out := preReadPages{
		live:              map[string]db.WikiPage{},
		tombs:             map[string]db.WikiPage{},
		unresolvedInbound: map[string]struct{}{},
	}
	if len(slugs) == 0 {
		return out, nil
	}
	var rows []db.WikiPage
	q := tx.Where("repository_id = ? AND slug IN ?", repoID, slugs).
		Find(&rows)
	if err := q.Error; err != nil {
		return preReadPages{}, err
	}
	for _, r := range rows {
		if r.DeletedAt != nil {
			out.tombs[r.Slug] = r
			continue
		}
		out.live[r.Slug] = r
	}
	return out, nil
}

func (c *Catalog) checkConflicts(tx *gorm.DB, plan changesetPlan, preRead preReadPages) error {
	for _, ch := range plan.changes {
		switch ch.op {
		case OpUpsert:
			existing, isLive := preRead.live[ch.srcSlug]
			// IfMatch is interpreted against live state only; a
			// tombstoned row is "absent" from the caller's
			// perspective and a stale ETag must surface as a
			// conflict so the client refetches.
			if err := checkIfMatch(ch, existing.HeadBlobSHA, isLive); err != nil {
				return err
			}
			// Prefix collision only applies when a page is being
			// newly inserted into the directory tree. Live update is
			// a no-op for the leaf; restore re-materializes the chain
			// from the pruned state and assertNoPrefixCollision will
			// allow it because the tomb's leaf was already removed.
			if !isLive {
				var err error
				if preRead.prefixKnown {
					err = assertNoPrefixCollisionFromSnapshot(preRead, ch.srcSlug)
				} else {
					err = assertNoPrefixCollision(tx, plan.repoID, ch.srcSlug, 0)
				}
				if err != nil {
					return err
				}
			}

		case OpDelete:
			existing, isLive := preRead.live[ch.srcSlug]
			if !isLive {
				return fmt.Errorf("%w: %q", ErrPageNotFound, ch.srcSlug)
			}
			if err := checkIfMatch(ch, existing.HeadBlobSHA, true); err != nil {
				return err
			}

		case OpRename:
			existing, isLive := preRead.live[ch.srcSlug]
			if !isLive {
				return fmt.Errorf("%w: %q", ErrPageNotFound, ch.srcSlug)
			}
			if err := checkIfMatch(ch, existing.HeadBlobSHA, true); err != nil {
				return err
			}
			if _, taken := preRead.live[ch.dstSlug]; taken {
				return &ConflictError{
					Code:        ConflictCodeDestinationTake,
					Slug:        ch.srcSlug,
					Destination: ch.dstSlug,
					Message:     fmt.Sprintf("rename destination %q is occupied", ch.dstSlug),
				}
			}
			// A tombstone at the destination is fine for rename —
			// we'll either hard-delete it (no live row to conflict
			// with) or let the unique constraint sort it out. Today
			// we conservatively refuse if a tomb exists, because
			// "rename into a previously-deleted slug" risks the
			// destination's revision history colliding with the
			// renamed page's. Surface this as a destination-taken
			// conflict so the operator hard-deletes the tomb first.
			if _, tombAtDest := preRead.tombs[ch.dstSlug]; tombAtDest {
				return &ConflictError{
					Code:        ConflictCodeDestinationTake,
					Slug:        ch.srcSlug,
					Destination: ch.dstSlug,
					Message:     fmt.Sprintf("rename destination %q holds a tombstoned page; purge it before renaming", ch.dstSlug),
				}
			}
			if err := assertNoPrefixCollision(tx, plan.repoID, ch.dstSlug, existing.PageID); err != nil {
				return err
			}
		}
	}
	return nil
}

func assertNoPrefixCollisionFromSnapshot(preRead preReadPages, slug string) error {
	var collidesWith string
	var shadowsNested bool
	for _, candidate := range preRead.prefixSlugs {
		nested := strings.HasPrefix(candidate, slug+"/")
		if collidesWith == "" || candidate < collidesWith {
			collidesWith = candidate
			shadowsNested = nested
		}
	}
	if collidesWith == "" {
		return nil
	}
	message := fmt.Sprintf("slug %q conflicts with existing page %q", slug, collidesWith)
	if shadowsNested {
		message = fmt.Sprintf("slug %q would shadow nested page %q", slug, collidesWith)
	}
	return &ConflictError{
		Code:         ConflictCodePrefix,
		Slug:         slug,
		CollidesWith: collidesWith,
		Message:      message,
	}
}

// assertNoPrefixCollision returns a ConflictError if slug would
// collide with an existing live page through the directory-prefix
// rule: "foo" collides with "foo/bar" and vice versa.
//
// The check runs against the live wiki_pages rows using the indexed
// (repository_id, slug) prefix. Exact ancestor slugs and the descendant range
// are covered in one query, while soft-deleted pages are ignored.
//
// ignorePageID lets renames declare the source page should not count
// against the check.
func assertNoPrefixCollision(tx *gorm.DB, repoID uint, slug string, ignorePageID uint64) error {
	predicate, args := pagePrefixCollisionPredicate("slug", slug)
	q := tx.Model(&db.WikiPage{}).
		Select("page_id", "slug").
		Where("repository_id = ? AND deleted_at IS NULL", repoID).
		Where(predicate, args...)
	if ignorePageID > 0 {
		q = q.Where("page_id <> ?", ignorePageID)
	}
	var row db.WikiPage
	result := q.Order("slug ASC").Limit(1).Find(&row)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return nil
	}
	if strings.HasPrefix(row.Slug, slug+"/") {
		return &ConflictError{
			Code:         ConflictCodePrefix,
			Slug:         slug,
			CollidesWith: row.Slug,
			Message:      fmt.Sprintf("slug %q would shadow nested page %q", slug, row.Slug),
		}
	}
	return &ConflictError{
		Code:         ConflictCodePrefix,
		Slug:         slug,
		CollidesWith: row.Slug,
		Message:      fmt.Sprintf("slug %q conflicts with existing page %q", slug, row.Slug),
	}
}

// checkIfMatch translates the per-change IfMatch precondition into
// a typed *ConflictError. Empty IfMatch on a change is a no-op
// (caller did not request optimistic concurrency). A non-empty
// IfMatch against a page that is not live, or against a SHA that
// differs from the current head, is a SOURCE_STALE conflict.
func checkIfMatch(ch plannedChange, currentSHA string, isLive bool) error {
	if ch.ifMatch == "" {
		return nil
	}
	if !isLive {
		return &ConflictError{
			Code:        ConflictCodeStale,
			Slug:        ch.srcSlug,
			ExpectedSHA: ch.ifMatch,
			Message:     fmt.Sprintf("If-Match expected %q but page %q does not exist", ch.ifMatch, ch.srcSlug),
		}
	}
	if !equalNonEmptySHA(currentSHA, ch.ifMatch) {
		return &ConflictError{
			Code:        ConflictCodeStale,
			Slug:        ch.srcSlug,
			ExpectedSHA: ch.ifMatch,
			CurrentSHA:  currentSHA,
			Message:     fmt.Sprintf("If-Match expected %q but current is %q", ch.ifMatch, currentSHA),
		}
	}
	return nil
}

// equalNonEmptySHA compares two hex SHA strings case-insensitively
// and treats either empty argument as never matching. The intent is
// that an unset blob SHA (recorded for delete revisions) must never
// satisfy a caller's If-Match check, even if the caller's IfMatch is
// also empty. The function is named for this contract because it is
// load-bearing for conflict semantics.
func equalNonEmptySHA(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(a, b)
}
