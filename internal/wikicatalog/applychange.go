package wikicatalog

import (
	"context"
	"fmt"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
)

// applyChange persists one planned change inside the transaction
// opened by applyOnce. Restore (the third applyUpsert sub-path)
// reuses the same page_id as the tombstone it resurrects, so the
// revision chain stays continuous across delete + recreate.
func (c *Catalog) applyChange(ctx context.Context, tx *gorm.DB, plan changesetPlan, cs *db.WikiChangeset, ch plannedChange, preRead preReadPages, blobBySlug map[string]string) (ChangeResult, error) {
	switch ch.op {
	case OpUpsert:
		return c.applyUpsert(ctx, tx, plan, cs, ch, preRead, blobBySlug)
	case OpDelete:
		return c.applyDelete(ctx, tx, plan, cs, ch, preRead)
	case OpRename:
		return c.applyRename(ctx, tx, plan, cs, ch, preRead, blobBySlug)
	}
	return ChangeResult{}, fmt.Errorf("wiki catalog: unknown op %v", ch.op)
}

func (c *Catalog) applyUpsert(ctx context.Context, tx *gorm.DB, plan changesetPlan, cs *db.WikiChangeset, ch plannedChange, preRead preReadPages, blobBySlug map[string]string) (ChangeResult, error) {
	newSHA := blobBySlug[ch.srcSlug]
	bodySize := len(ch.body)
	var inline []byte
	if bodySize <= MaxBodyInlineBytes {
		// Alias ch.body directly. The Change contract forbids
		// caller mutation between submit and return; GORM copies
		// out into the prepared statement on Create so the alias
		// does not outlive this function.
		inline = ch.body
	}

	live, isLive := preRead.live[ch.srcSlug]
	tomb, isTomb := preRead.tombs[ch.srcSlug]

	var (
		pageID            uint64
		revisionID        uint64
		op                string
		upsertDisposition UpsertDisposition
		newlyVisible      bool   // create/restore makes a slug-to-page mapping visible
		decrementOld      bool   // skip when the tomb's blob was already decremented by applyDelete
		oldBlobSHA        string // for the decrement, if any
	)

	// Dispatch and write the page row in a single switch. Three
	// arms: update an existing live row, restore a tombstone, or
	// insert a brand-new page. Tail logic (revision row, refcount,
	// outlinks, inbound resolution, pending-WAL clear) is shared
	// below.
	switch {
	case isLive:
		pageID = live.PageID
		revisionID = live.HeadRevisionID + 1
		op = revOpUpdate
		upsertDisposition = UpsertDispositionUpdate
		decrementOld = true
		oldBlobSHA = live.HeadBlobSHA
		if err := measureApplyPhase(ctx, ApplyPhasePageWrite, func() error {
			return tx.Model(&db.WikiPage{}).
				Where("page_id = ?", pageID).
				Updates(pageUpsertColumns(ch, newSHA, bodySize, inline, revisionID, cs, plan, false)).Error
		}); err != nil {
			return ChangeResult{}, fmt.Errorf("update page %q: %w", ch.srcSlug, err)
		}
	case isTomb:
		// Restore clears deleted_at so the row is live again. Its prior
		// blob was already decremented at delete time.
		pageID = tomb.PageID
		revisionID = tomb.HeadRevisionID + 1
		op = revOpRestore
		upsertDisposition = UpsertDispositionRestore
		newlyVisible = true
		if err := measureApplyPhase(ctx, ApplyPhasePageWrite, func() error {
			return tx.Model(&db.WikiPage{}).
				Where("page_id = ?", pageID).
				Updates(pageUpsertColumns(ch, newSHA, bodySize, inline, revisionID, cs, plan, true)).Error
		}); err != nil {
			return ChangeResult{}, fmt.Errorf("restore page %q: %w", ch.srcSlug, err)
		}
	default:
		op = revOpCreate
		upsertDisposition = UpsertDispositionCreate
		revisionID = 1
		newlyVisible = true
		page := db.WikiPage{
			RepositoryID:    plan.repoID,
			Slug:            ch.srcSlug,
			Title:           TitleFromSlug(ch.srcSlug),
			HeadBlobSHA:     newSHA,
			BodySize:        bodySize,
			BodyInline:      inline,
			HeadRevisionID:  revisionID,
			HeadChangesetID: cs.ChangesetID,
			LastAuthorID:    plan.authorID,
			CreatedAt:       plan.committedAt,
			UpdatedAt:       plan.committedAt,
		}
		if err := measureApplyPhase(ctx, ApplyPhasePageWrite, func() error {
			return tx.Create(&page).Error
		}); err != nil {
			return ChangeResult{}, fmt.Errorf("create page %q: %w", ch.srcSlug, err)
		}
		pageID = page.PageID
	}

	rev := db.WikiPageRevision{
		PageID:      pageID,
		RevisionID:  revisionID,
		ChangesetID: cs.ChangesetID,
		BlobSHA:     newSHA,
		BodySize:    bodySize,
		BodyInline:  inline,
		SlugAtRev:   ch.srcSlug,
		CommitSHA:   cs.SynthCommitSHA,
		Op:          op,
		AuthorID:    plan.authorID,
		CommittedAt: plan.committedAt,
	}
	if err := measureApplyPhase(ctx, ApplyPhaseRevisionInsert, func() error {
		return tx.Create(&rev).Error
	}); err != nil {
		return ChangeResult{}, fmt.Errorf("create revision for %q: %w", ch.srcSlug, err)
	}

	if err := measureApplyPhase(ctx, ApplyPhaseBlobRefs, func() error {
		if err := incrementBlobRef(tx, newSHA, bodySize, plan.committedAt); err != nil {
			return err
		}
		if decrementOld && !equalNonEmptySHA(oldBlobSHA, newSHA) {
			return decrementBlobRef(tx, oldBlobSHA, live.BodySize)
		}
		return nil
	}); err != nil {
		return ChangeResult{}, err
	}

	if err := measureApplyPhase(ctx, ApplyPhaseOutlinks, func() error {
		return refreshOutlinks(
			tx,
			plan.repoID,
			pageID,
			ch.srcSlug,
			string(ch.body),
			!newlyVisible,
			preRead.outlinkTargets,
			preRead.outlinksKnown,
		)
	}); err != nil {
		return ChangeResult{}, err
	}

	// On create or restore, the (slug, page_id) mapping for this slug
	// is newly visible. Any inbound link whose target matches this
	// slug should now resolve. Update path leaves these untouched
	// because the mapping didn't change.
	if err := measureApplyPhase(ctx, ApplyPhaseInboundLinks, func() error {
		if newlyVisible && preRead.needsInboundResolution(ch.srcSlug) {
			return resolveInboundLinks(tx, plan.repoID, ch.srcSlug, pageID)
		}
		return nil
	}); err != nil {
		return ChangeResult{}, err
	}

	if err := measureApplyPhase(ctx, ApplyPhasePendingBlobCleanup, func() error {
		if bodySize > MaxBodyInlineBytes {
			return tx.Where("blob_sha = ?", newSHA).Delete(&db.WikiPendingBlob{}).Error
		}
		return nil
	}); err != nil {
		return ChangeResult{}, err
	}

	return ChangeResult{
		Op:                OpUpsert,
		UpsertDisposition: upsertDisposition,
		Slug:              ch.srcSlug,
		PageID:            pageID,
		RevisionID:        revisionID,
		BlobSHA:           newSHA,
		BodySize:          bodySize,
		Body:              ch.body,
		BodyAvailable:     true,
	}, nil
}

func (c *Catalog) applyDelete(ctx context.Context, tx *gorm.DB, plan changesetPlan, cs *db.WikiChangeset, ch plannedChange, preRead preReadPages) (ChangeResult, error) {
	existing := preRead.live[ch.srcSlug] // existence verified in checkConflicts
	pageID := existing.PageID
	revisionID := existing.HeadRevisionID + 1

	rev := db.WikiPageRevision{
		PageID:      pageID,
		RevisionID:  revisionID,
		ChangesetID: cs.ChangesetID,
		BlobSHA:     "",
		BodySize:    0,
		SlugAtRev:   existing.Slug,
		CommitSHA:   cs.SynthCommitSHA,
		Op:          revOpDelete,
		AuthorID:    plan.authorID,
		CommittedAt: plan.committedAt,
	}
	if err := measureApplyPhase(ctx, ApplyPhaseRevisionInsert, func() error {
		return tx.Create(&rev).Error
	}); err != nil {
		return ChangeResult{}, fmt.Errorf("delete revision for %q: %w", existing.Slug, err)
	}

	if err := measureApplyPhase(ctx, ApplyPhasePageWrite, func() error {
		return tx.Model(&db.WikiPage{}).
			Where("page_id = ?", pageID).
			Updates(map[string]any{
				"deleted_at":        plan.committedAt,
				"updated_at":        plan.committedAt,
				"head_revision_id":  revisionID,
				"head_changeset_id": cs.ChangesetID,
			}).Error
	}); err != nil {
		return ChangeResult{}, fmt.Errorf("soft-delete page %q: %w", existing.Slug, err)
	}

	if err := measureApplyPhase(ctx, ApplyPhaseBlobRefs, func() error {
		return decrementBlobRef(tx, existing.HeadBlobSHA, existing.BodySize)
	}); err != nil {
		return ChangeResult{}, err
	}

	if err := measureApplyPhase(ctx, ApplyPhaseOutlinks, func() error {
		return tx.Where("src_page_id = ?", pageID).Delete(&db.WikiPageLink{}).Error
	}); err != nil {
		return ChangeResult{}, fmt.Errorf("clear outlinks for %q: %w", existing.Slug, err)
	}

	// Any link pointing at this page now points at a soft-deleted
	// row; clear the resolution so backlink queries via the resolved
	// page-id index don't surface phantom hits.
	if err := measureApplyPhase(ctx, ApplyPhaseInboundLinks, func() error {
		return clearInboundLinksForPage(tx, plan.repoID, pageID)
	}); err != nil {
		return ChangeResult{}, err
	}

	if err := measureApplyPhase(ctx, ApplyPhaseLabelMutation, func() error {
		return tx.Where("repository_id = ? AND slug = ?", plan.repoID, existing.Slug).
			Delete(&db.WikiPageLabel{}).Error
	}); err != nil {
		return ChangeResult{}, fmt.Errorf("clear labels for %q: %w", existing.Slug, err)
	}

	return ChangeResult{
		Op:         OpDelete,
		Slug:       existing.Slug,
		PrevSlug:   existing.Slug,
		PageID:     pageID,
		RevisionID: revisionID,
	}, nil
}

func (c *Catalog) applyRename(ctx context.Context, tx *gorm.DB, plan changesetPlan, cs *db.WikiChangeset, ch plannedChange, preRead preReadPages, blobBySlug map[string]string) (ChangeResult, error) {
	existing := preRead.live[ch.srcSlug] // existence verified in checkConflicts
	pageID := existing.PageID
	revisionID := existing.HeadRevisionID + 1
	oldSlug := existing.Slug

	// Decide whether this rename also updates the body. When the
	// caller supplies ch.body, blobBySlug carries the precomputed SHA
	// for it; otherwise carry the existing blob forward unchanged.
	newSHA := existing.HeadBlobSHA
	newSize := existing.BodySize
	newInline := existing.BodyInline
	bodyChanged := len(ch.body) > 0
	if bodyChanged {
		newSHA = blobBySlug[ch.srcSlug]
		newSize = len(ch.body)
		if newSize <= MaxBodyInlineBytes {
			newInline = ch.body
		} else {
			newInline = nil
		}
	}

	rev := db.WikiPageRevision{
		PageID:      pageID,
		RevisionID:  revisionID,
		ChangesetID: cs.ChangesetID,
		BlobSHA:     newSHA,
		BodySize:    newSize,
		BodyInline:  newInline,
		SlugAtRev:   ch.dstSlug,
		CommitSHA:   cs.SynthCommitSHA,
		Op:          revOpRename,
		AuthorID:    plan.authorID,
		CommittedAt: plan.committedAt,
	}
	if err := measureApplyPhase(ctx, ApplyPhaseRevisionInsert, func() error {
		return tx.Create(&rev).Error
	}); err != nil {
		return ChangeResult{}, fmt.Errorf("rename revision for %q: %w", existing.Slug, err)
	}

	updates := map[string]any{
		"slug":              ch.dstSlug,
		"title":             TitleFromSlug(ch.dstSlug),
		"head_revision_id":  revisionID,
		"head_changeset_id": cs.ChangesetID,
		"updated_at":        plan.committedAt,
	}
	if bodyChanged {
		updates["head_blob_sha"] = newSHA
		updates["body_size"] = newSize
		updates["body_inline"] = newInline
	}
	if err := measureApplyPhase(ctx, ApplyPhasePageWrite, func() error {
		return tx.Model(&db.WikiPage{}).
			Where("page_id = ?", pageID).
			Updates(updates).Error
	}); err != nil {
		return ChangeResult{}, fmt.Errorf("rename page %q -> %q: %w", existing.Slug, ch.dstSlug, err)
	}

	if err := measureApplyPhase(ctx, ApplyPhaseBlobRefs, func() error {
		if !bodyChanged {
			return nil
		}
		if err := incrementBlobRef(tx, newSHA, newSize, plan.committedAt); err != nil {
			return err
		}
		if !equalNonEmptySHA(existing.HeadBlobSHA, newSHA) {
			return decrementBlobRef(tx, existing.HeadBlobSHA, existing.BodySize)
		}
		return nil
	}); err != nil {
		return ChangeResult{}, err
	}

	if err := measureApplyPhase(ctx, ApplyPhaseOutlinks, func() error {
		if !bodyChanged {
			return nil
		}
		return refreshOutlinks(
			tx,
			plan.repoID,
			pageID,
			ch.dstSlug,
			string(ch.body),
			true,
			nil,
			false,
		)
	}); err != nil {
		return ChangeResult{}, err
	}

	if err := measureApplyPhase(ctx, ApplyPhasePendingBlobCleanup, func() error {
		if bodyChanged && newSize > MaxBodyInlineBytes {
			return tx.Where("blob_sha = ?", newSHA).Delete(&db.WikiPendingBlob{}).Error
		}
		return nil
	}); err != nil {
		return ChangeResult{}, err
	}

	// Inbound links anchored on the old slug text now point at a
	// page that no longer occupies that slug — clear them. Then resolve
	// any links that were waiting for the destination slug.
	if err := measureApplyPhase(ctx, ApplyPhaseInboundLinks, func() error {
		if err := clearInboundLinksForSlug(tx, plan.repoID, oldSlug, pageID); err != nil {
			return err
		}
		if preRead.needsInboundResolution(ch.dstSlug) {
			return resolveInboundLinks(tx, plan.repoID, ch.dstSlug, pageID)
		}
		return nil
	}); err != nil {
		return ChangeResult{}, err
	}

	if err := measureApplyPhase(ctx, ApplyPhaseLabelMutation, func() error {
		return renameLabels(tx, plan.repoID, existing.Slug, ch.dstSlug)
	}); err != nil {
		return ChangeResult{}, err
	}

	return ChangeResult{
		Op:            OpRename,
		Slug:          ch.dstSlug,
		PrevSlug:      existing.Slug,
		PageID:        pageID,
		RevisionID:    revisionID,
		BlobSHA:       newSHA,
		BodySize:      newSize,
		Body:          ch.body,
		BodyAvailable: bodyChanged,
	}, nil
}

// pageUpsertColumns is the shared column set written by both the
// update and the restore arms of applyUpsert. clearDeletedAt = true
// (restore) additionally NULLs deleted_at so the page is live again.
func pageUpsertColumns(ch plannedChange, blobSHA string, bodySize int, inline []byte, revisionID uint64, cs *db.WikiChangeset, plan changesetPlan, clearDeletedAt bool) map[string]any {
	out := map[string]any{
		"slug":              ch.srcSlug,
		"title":             TitleFromSlug(ch.srcSlug),
		"head_blob_sha":     blobSHA,
		"body_size":         bodySize,
		"body_inline":       inline,
		"head_revision_id":  revisionID,
		"head_changeset_id": cs.ChangesetID,
		"last_author_id":    plan.authorID,
		"updated_at":        plan.committedAt,
	}
	if clearDeletedAt {
		out["deleted_at"] = nil
	}
	return out
}
