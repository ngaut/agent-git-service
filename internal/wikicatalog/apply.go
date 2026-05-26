package wikicatalog

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

// ApplyChangeSet is the single write entry point for the wiki
// catalog. Every REST mutation, batch operation, migration replay,
// and future push ingestion calls into this method. The contract:
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
// All other invariants (refcount maintenance, dir_index incremental
// updates, link rewrites, slug aliases for renames, label remapping)
// are kept inside the same SQL transaction. There is no dual-write.
func (c *Catalog) ApplyChangeSet(ctx context.Context, req ChangeSetRequest) (ChangeSetResult, error) {
	plan, err := c.planChangeSet(req)
	if err != nil {
		return ChangeSetResult{}, err
	}

	// Compute per-change blob SHAs up front so the SQL phase always
	// knows the new head SHA without re-hashing. blobByCI is also used
	// for the synthetic commit SHA. OpRename normally carries the
	// existing blob forward; when the caller provides ch.body on a
	// rename, we treat it as a body update applied atomically with
	// the slug move (the prefix-move planner uses this so a moved
	// page whose body references another moved slug lands the
	// rewritten content under the new slug).
	blobByCI := make(map[string]string, len(plan.changes))
	for _, ch := range plan.changes {
		switch ch.op {
		case OpUpsert:
			blobByCI[ch.srcSlugCI] = HashContent(ch.body)
		case OpRename:
			if len(ch.body) > 0 {
				blobByCI[ch.srcSlugCI] = HashContent(ch.body)
			}
		}
	}

	// Upload non-inline blobs and record pending WAL rows. We do this
	// before opening the SQL txn so that a slow CAS upload does not
	// hold a transaction lock; the WAL rows make orphan reclamation
	// straightforward if the txn later fails.
	if err := c.uploadBlobs(ctx, plan, blobByCI); err != nil {
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
		result, casLost, txErr = c.applyOnce(ctx, plan, blobByCI)
		if txErr != nil {
			return ChangeSetResult{}, txErr
		}
		if !casLost {
			// Catalog state is committed. Drive post-commit side
			// effects (search reindex, etc.) via the optional hook.
			// A hook error propagates to the caller but does not
			// roll back the changeset.
			if c.OnChangeSetCommitted != nil {
				if err := c.OnChangeSetCommitted(ctx, plan.repoID, result); err != nil {
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

// applyOnce performs a single transaction attempt. casLost == true
// indicates the wiki_repo_heads CAS lost; the caller decides whether
// to retry.
func (c *Catalog) applyOnce(ctx context.Context, plan changesetPlan, blobByCI map[string]string) (ChangeSetResult, bool, error) {
	var (
		result  ChangeSetResult
		casLost bool
	)
	err := c.db(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Read current head (may not exist for a brand-new wiki).
		head, headExists, err := loadHeadForUpdate(tx, plan.repoID)
		if err != nil {
			return err
		}
		var parentID *uint64
		if headExists {
			parentID = &head.HeadChangesetID
		}
		if plan.parentExpect != nil {
			expected := *plan.parentExpect
			currentParent := uint64(0)
			if parentID != nil {
				currentParent = *parentID
			}
			if expected != currentParent {
				casLost = true
				return errSentinelCASLost
			}
		}
		// Test-only hook: simulate a CAS-lost transaction without
		// needing real concurrency on a dialect (SQLite) that can't
		// schedule one. Never set in production code.
		if c.testForceCASLoss != nil && c.testForceCASLoss() {
			casLost = true
			return errSentinelCASLost
		}

		// 2. Pre-read all touched pages.
		preRead, err := loadPagesByCanonical(tx, plan.repoID, plan.touchedCI)
		if err != nil {
			return err
		}

		// 3. Conflict checks (read-only, no mutations yet).
		if err := c.checkConflicts(tx, plan, preRead); err != nil {
			return err
		}

		// 4. Insert the changeset row. The synth commit SHA is
		// deterministic from inputs so retries within the OCC loop
		// don't produce drifting SHAs across attempts. The migration
		// path may override the SHA with the original git commit SHA.
		var synthSHA string
		if plan.overrideCommitSHA != "" {
			synthSHA = plan.overrideCommitSHA
		} else {
			synthSHA = computeSynthCommitSHA(plan.repoID, parentID, plan.committedAt, plan.message, plan.changes, blobByCI)
		}
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
		if plan.overrideCommitSHA != "" || plan.source == SourcePush {
			cs.SynthFormatVer = 1
		}
		if err := tx.Create(&cs).Error; err != nil {
			return err
		}

		// 5. Update wiki_repo_heads under CAS, or insert if new wiki.
		if !headExists {
			if err := tx.Create(&db.WikiRepoHead{
				RepositoryID:    plan.repoID,
				HeadChangesetID: cs.ChangesetID,
				UpdatedAt:       plan.committedAt,
			}).Error; err != nil {
				// Concurrent first writer raced and won. Treat as CAS loss.
				casLost = true
				return errSentinelCASLost
			}
		} else {
			res := tx.Model(&db.WikiRepoHead{}).
				Where("repository_id = ? AND head_changeset_id = ?", plan.repoID, head.HeadChangesetID).
				Updates(map[string]any{
					"head_changeset_id": cs.ChangesetID,
					"updated_at":        plan.committedAt,
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				casLost = true
				return errSentinelCASLost
			}
		}

		// 6. Apply each change.
		result = ChangeSetResult{
			ChangesetID: cs.ChangesetID,
			ParentID:    parentID,
			CommitSHA:   synthSHA,
			Source:      plan.source,
			Changes:     make([]ChangeResult, 0, len(plan.changes)),
		}
		for _, ch := range plan.changes {
			cr, err := c.applyChange(tx, plan, &cs, ch, preRead, blobByCI)
			if err != nil {
				return err
			}
			result.Changes = append(result.Changes, cr)
		}
		return nil
	})
	if errors.Is(err, errSentinelCASLost) {
		return ChangeSetResult{}, true, nil
	}
	if err != nil {
		return ChangeSetResult{}, false, err
	}
	return result, casLost, nil
}

// errSentinelCASLost is an internal sentinel used to roll back a
// transaction when the head CAS loses. The outer loop translates it
// to either a retry or ErrCASLost depending on caller intent.
var errSentinelCASLost = errors.New("wiki catalog internal: CAS lost")

func (c *Catalog) uploadBlobs(ctx context.Context, plan changesetPlan, blobByCI map[string]string) error {
	// Invariant for the GC story: bodies with size <= MaxBodyInlineBytes
	// live exclusively in wiki_pages.body_inline / wiki_page_revisions
	// .body_inline. They are NOT written to the CAS filesystem and
	// thus have no pending-blob WAL row — there is nothing on disk to
	// reclaim if the transaction later fails. Larger bodies go
	// through the CAS and get a WAL row that the GC can find.
	//
	// Refcount semantics in wiki_blob_refs still cover both classes:
	// it tracks "how many live pages reference this SHA," not "is
	// this SHA on disk." GC consults the refcount and only deletes
	// the CAS file if one exists for that SHA.
	// Independent per-blob work runs in parallel with bounded
	// concurrency. The legacy serial loop was the single biggest
	// constant-factor cost in batch upserts and migration replays.
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
		sha := blobByCI[ch.srcSlugCI]
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

// preReadPages partitions the catalog rows touched by a changeset into
// live pages (visible to readers) and tombstoned pages (rows held by
// the unique constraint but logically deleted). Both maps key by
// slug_ci_v1. They are disjoint because the unique index guarantees at
// most one row per (repo_id, slug_ci_v1).
type preReadPages struct {
	live  map[string]db.WikiPage
	tombs map[string]db.WikiPage
}

func loadPagesByCanonical(tx *gorm.DB, repoID uint, slugs []string) (preReadPages, error) {
	out := preReadPages{
		live:  map[string]db.WikiPage{},
		tombs: map[string]db.WikiPage{},
	}
	if len(slugs) == 0 {
		return out, nil
	}
	var rows []db.WikiPage
	q := tx.Where("repository_id = ? AND slug_ci_v1 IN ?", repoID, slugs).
		Find(&rows)
	if err := q.Error; err != nil {
		return preReadPages{}, err
	}
	for _, r := range rows {
		if r.DeletedAt != nil {
			out.tombs[r.SlugCIV1] = r
			continue
		}
		out.live[r.SlugCIV1] = r
	}
	return out, nil
}

func (c *Catalog) checkConflicts(tx *gorm.DB, plan changesetPlan, preRead preReadPages) error {
	for _, ch := range plan.changes {
		switch ch.op {
		case OpUpsert:
			existing, isLive := preRead.live[ch.srcSlugCI]
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
				if err := assertNoPrefixCollision(tx, plan.repoID, ch.srcSlugCI, 0); err != nil {
					return err
				}
			}

		case OpDelete:
			existing, isLive := preRead.live[ch.srcSlugCI]
			if !isLive {
				return fmt.Errorf("%w: %q", ErrPageNotFound, ch.srcSlug)
			}
			if err := checkIfMatch(ch, existing.HeadBlobSHA, true); err != nil {
				return err
			}

		case OpRename:
			existing, isLive := preRead.live[ch.srcSlugCI]
			if !isLive {
				return fmt.Errorf("%w: %q", ErrPageNotFound, ch.srcSlug)
			}
			if err := checkIfMatch(ch, existing.HeadBlobSHA, true); err != nil {
				return err
			}
			if _, taken := preRead.live[ch.dstSlugCI]; taken {
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
			if _, tombAtDest := preRead.tombs[ch.dstSlugCI]; tombAtDest {
				return &ConflictError{
					Code:        ConflictCodeDestinationTake,
					Slug:        ch.srcSlug,
					Destination: ch.dstSlug,
					Message:     fmt.Sprintf("rename destination %q holds a tombstoned page; purge it before renaming", ch.dstSlug),
				}
			}
			if err := assertNoPrefixCollision(tx, plan.repoID, ch.dstSlugCI, existing.PageID); err != nil {
				return err
			}
		}
	}
	return nil
}

// assertNoPrefixCollision returns a ConflictError if slug would
// collide with an existing live page through the directory-prefix
// rule: "foo" collides with "foo/bar" and vice versa.
//
// The check runs entirely against wiki_dir_index, which is kept in
// sync with live state by ApplyChangeSet (deleted pages are pruned
// from the dir index, so soft-deletes do not produce phantom
// collisions). The wildcard `LIKE` against wiki_pages this used to do
// — fragile against `_sidebar`'s underscore, and visited soft-deleted
// rows — is gone.
//
// ignorePageID lets renames declare the source page should not count
// against the check (its dir leaf is removed in the same transaction
// before the new leaf is inserted).
func assertNoPrefixCollision(tx *gorm.DB, repoID uint, slugCI string, ignorePageID uint64) error {
	// Case 1: any ancestor of slugCI is occupied as a blob leaf.
	// One IN-list query against (parent_dir, child_name) tuples for
	// the whole parent chain replaces the legacy per-depth point
	// query loop. Bounded by wikiMaxSlugDepth (≤ 6 ancestors).
	ancestors := parentChain(slugCI)
	if len(ancestors) > 0 {
		// Tuples: (parent_dir, child_name). Some dialects support
		// `WHERE (a, b) IN ((..),(..))`; SQLite and TiDB do. For
		// portability with GORM we build an OR chain — at depth ≤ 6
		// this is still one round trip.
		q := tx.Model(&db.WikiDirIndex{}).
			Where("repository_id = ? AND child_kind = ?", repoID, childKindBlob)
		var clauseSQL string
		var clauseArgs []any
		for i, anc := range ancestors {
			parent, leaf := splitParentLeaf(anc)
			if i > 0 {
				clauseSQL += " OR "
			}
			clauseSQL += "(parent_dir = ? AND child_name = ?)"
			clauseArgs = append(clauseArgs, parent, leaf)
		}
		q = q.Where(clauseSQL, clauseArgs...)
		if ignorePageID > 0 {
			q = q.Where("page_id IS NULL OR page_id <> ?", ignorePageID)
		}
		var row db.WikiDirIndex
		err := q.Take(&row).Error
		if err == nil {
			// Reconstruct the ancestor slug from the matched
			// (parent_dir, child_name) tuple so the error message
			// surfaces the offending parent path.
			collides := row.ChildName
			if row.ParentDir != "" {
				collides = row.ParentDir + "/" + row.ChildName
			}
			return &ConflictError{
				Code:         ConflictCodePrefix,
				Slug:         slugCI,
				CollidesWith: collides,
				Message:      fmt.Sprintf("slug %q conflicts with existing page %q", slugCI, collides),
			}
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	// Case 2: slugCI itself is a directory containing at least one
	// live child. dir_index makes this a single index range probe.
	q := tx.Model(&db.WikiDirIndex{}).
		Where("repository_id = ? AND parent_dir = ?", repoID, slugCI)
	if ignorePageID > 0 {
		q = q.Where("page_id IS NULL OR page_id <> ?", ignorePageID)
	}
	var child db.WikiDirIndex
	err := q.Take(&child).Error
	if err == nil {
		nested := slugCI + "/" + child.ChildName
		return &ConflictError{
			Code:         ConflictCodePrefix,
			Slug:         slugCI,
			CollidesWith: nested,
			Message:      fmt.Sprintf("slug %q would shadow nested page %q", slugCI, nested),
		}
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
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
