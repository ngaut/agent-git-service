package wikicatalog

import (
	"fmt"

	"gh-server/internal/db"

	"gorm.io/gorm"
)

// applyChange persists one planned change inside the transaction
// opened by applyOnce. Restore (the third applyUpsert sub-path)
// reuses the same page_id as the tombstone it resurrects, so the
// revision chain stays continuous across delete + recreate.
func (c *Catalog) applyChange(tx *gorm.DB, plan changesetPlan, cs *db.WikiChangeset, ch plannedChange, preRead preReadPages, blobByCI map[string]string) (ChangeResult, error) {
	switch ch.op {
	case OpUpsert:
		return c.applyUpsert(tx, plan, cs, ch, preRead, blobByCI)
	case OpDelete:
		return c.applyDelete(tx, plan, cs, ch, preRead)
	case OpRename:
		return c.applyRename(tx, plan, cs, ch, preRead)
	}
	return ChangeResult{}, fmt.Errorf("wiki catalog: unknown op %v", ch.op)
}

func (c *Catalog) applyUpsert(tx *gorm.DB, plan changesetPlan, cs *db.WikiChangeset, ch plannedChange, preRead preReadPages, blobByCI map[string]string) (ChangeResult, error) {
	newSHA := blobByCI[ch.srcSlugCI]
	bodySize := len(ch.body)
	var inline []byte
	if bodySize <= MaxBodyInlineBytes {
		// Alias ch.body directly. The Change contract forbids
		// caller mutation between submit and return; GORM copies
		// out into the prepared statement on Create so the alias
		// does not outlive this function.
		inline = ch.body
	}

	live, isLive := preRead.live[ch.srcSlugCI]
	tomb, isTomb := preRead.tombs[ch.srcSlugCI]

	var (
		pageID       uint64
		revisionID   uint64
		op           string
		needsNewLeaf bool   // dir_index leaf + parent chain must be inserted
		decrementOld bool   // skip when the tomb's blob was already decremented by applyDelete
		oldBlobSHA   string // for the decrement, if any
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
		decrementOld = true
		oldBlobSHA = live.HeadBlobSHA
		if err := tx.Model(&db.WikiPage{}).
			Where("page_id = ?", pageID).
			Updates(pageUpsertColumns(ch, newSHA, bodySize, inline, revisionID, cs, plan, false)).Error; err != nil {
			return ChangeResult{}, fmt.Errorf("update page %q: %w", ch.srcSlug, err)
		}
	case isTomb:
		// applyDelete pruned the leaf and parents; restore
		// re-materializes them and clears deleted_at so the row is
		// live again. Its prior blob was already decremented at
		// delete time.
		pageID = tomb.PageID
		revisionID = tomb.HeadRevisionID + 1
		op = revOpRestore
		needsNewLeaf = true
		if err := tx.Model(&db.WikiPage{}).
			Where("page_id = ?", pageID).
			Updates(pageUpsertColumns(ch, newSHA, bodySize, inline, revisionID, cs, plan, true)).Error; err != nil {
			return ChangeResult{}, fmt.Errorf("restore page %q: %w", ch.srcSlug, err)
		}
	default:
		op = revOpCreate
		revisionID = 1
		needsNewLeaf = true
		page := db.WikiPage{
			RepositoryID:    plan.repoID,
			Slug:            ch.srcSlug,
			SlugCIV1:        ch.srcSlugCI,
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
		if err := tx.Create(&page).Error; err != nil {
			return ChangeResult{}, fmt.Errorf("create page %q: %w", ch.srcSlug, err)
		}
		pageID = page.PageID
	}

	if needsNewLeaf {
		if err := ensureDirChain(tx, plan.repoID, ch.srcSlugCI); err != nil {
			return ChangeResult{}, err
		}
		if err := insertDirLeaf(tx, plan.repoID, ch.srcSlugCI, pageID); err != nil {
			return ChangeResult{}, err
		}
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
	if err := tx.Create(&rev).Error; err != nil {
		return ChangeResult{}, fmt.Errorf("create revision for %q: %w", ch.srcSlug, err)
	}

	if err := incrementBlobRef(tx, newSHA, bodySize, plan.committedAt); err != nil {
		return ChangeResult{}, err
	}
	if decrementOld && !equalNonEmptySHA(oldBlobSHA, newSHA) {
		if err := decrementBlobRef(tx, oldBlobSHA); err != nil {
			return ChangeResult{}, err
		}
	}

	if err := refreshOutlinks(tx, plan.repoID, pageID, string(ch.body)); err != nil {
		return ChangeResult{}, err
	}

	// On create or restore, the (slug, page_id) mapping for this slug
	// is newly visible. Any inbound link whose target matches this
	// slug should now resolve. Update path leaves these untouched
	// because the mapping didn't change.
	if needsNewLeaf {
		if err := resolveInboundLinks(tx, plan.repoID, ch.srcSlugCI, pageID); err != nil {
			return ChangeResult{}, err
		}
	}

	if err := tx.Where("blob_sha = ?", newSHA).Delete(&db.WikiPendingBlob{}).Error; err != nil {
		return ChangeResult{}, err
	}

	return ChangeResult{
		Op:         OpUpsert,
		Slug:       ch.srcSlug,
		PageID:     pageID,
		RevisionID: revisionID,
		BlobSHA:    newSHA,
		BodySize:   bodySize,
	}, nil
}

func (c *Catalog) applyDelete(tx *gorm.DB, plan changesetPlan, cs *db.WikiChangeset, ch plannedChange, preRead preReadPages) (ChangeResult, error) {
	existing := preRead.live[ch.srcSlugCI] // existence verified in checkConflicts
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
	if err := tx.Create(&rev).Error; err != nil {
		return ChangeResult{}, fmt.Errorf("delete revision for %q: %w", existing.Slug, err)
	}

	if err := tx.Model(&db.WikiPage{}).
		Where("page_id = ?", pageID).
		Updates(map[string]any{
			"deleted_at":        plan.committedAt,
			"updated_at":        plan.committedAt,
			"head_revision_id":  revisionID,
			"head_changeset_id": cs.ChangesetID,
		}).Error; err != nil {
		return ChangeResult{}, fmt.Errorf("soft-delete page %q: %w", existing.Slug, err)
	}

	if err := decrementBlobRef(tx, existing.HeadBlobSHA); err != nil {
		return ChangeResult{}, err
	}

	if err := removeDirLeaf(tx, plan.repoID, existing.SlugCIV1); err != nil {
		return ChangeResult{}, err
	}
	if err := pruneEmptyParents(tx, plan.repoID, existing.SlugCIV1); err != nil {
		return ChangeResult{}, err
	}

	if err := tx.Where("src_page_id = ?", pageID).Delete(&db.WikiPageLink{}).Error; err != nil {
		return ChangeResult{}, fmt.Errorf("clear outlinks for %q: %w", existing.Slug, err)
	}

	// Any link pointing at this page now points at a soft-deleted
	// row; clear the resolution so backlink queries via the resolved
	// page-id index don't surface phantom hits.
	if err := clearInboundLinksForPage(tx, plan.repoID, pageID); err != nil {
		return ChangeResult{}, err
	}

	if err := tx.Where("repository_id = ? AND slug = ?", plan.repoID, existing.Slug).
		Delete(&db.WikiPageLabel{}).Error; err != nil {
		return ChangeResult{}, fmt.Errorf("clear labels for %q: %w", existing.Slug, err)
	}

	return ChangeResult{
		Op:         OpDelete,
		Slug:       existing.Slug,
		PageID:     pageID,
		RevisionID: revisionID,
	}, nil
}

func (c *Catalog) applyRename(tx *gorm.DB, plan changesetPlan, cs *db.WikiChangeset, ch plannedChange, preRead preReadPages) (ChangeResult, error) {
	existing := preRead.live[ch.srcSlugCI] // existence verified in checkConflicts
	pageID := existing.PageID
	revisionID := existing.HeadRevisionID + 1
	oldSlugCI := existing.SlugCIV1

	rev := db.WikiPageRevision{
		PageID:      pageID,
		RevisionID:  revisionID,
		ChangesetID: cs.ChangesetID,
		BlobSHA:     existing.HeadBlobSHA,
		BodySize:    existing.BodySize,
		BodyInline:  existing.BodyInline,
		SlugAtRev:   ch.dstSlug,
		CommitSHA:   cs.SynthCommitSHA,
		Op:          revOpRename,
		AuthorID:    plan.authorID,
		CommittedAt: plan.committedAt,
	}
	if err := tx.Create(&rev).Error; err != nil {
		return ChangeResult{}, fmt.Errorf("rename revision for %q: %w", existing.Slug, err)
	}

	if err := tx.Model(&db.WikiPage{}).
		Where("page_id = ?", pageID).
		Updates(map[string]any{
			"slug":              ch.dstSlug,
			"slug_ci_v1":        ch.dstSlugCI,
			"title":             TitleFromSlug(ch.dstSlug),
			"head_revision_id":  revisionID,
			"head_changeset_id": cs.ChangesetID,
			"updated_at":        plan.committedAt,
		}).Error; err != nil {
		return ChangeResult{}, fmt.Errorf("rename page %q -> %q: %w", existing.Slug, ch.dstSlug, err)
	}

	if err := removeDirLeaf(tx, plan.repoID, oldSlugCI); err != nil {
		return ChangeResult{}, err
	}
	if err := ensureDirChain(tx, plan.repoID, ch.dstSlugCI); err != nil {
		return ChangeResult{}, err
	}
	if err := insertDirLeaf(tx, plan.repoID, ch.dstSlugCI, pageID); err != nil {
		return ChangeResult{}, err
	}
	if err := pruneEmptyParents(tx, plan.repoID, oldSlugCI); err != nil {
		return ChangeResult{}, err
	}

	// Inbound links anchored on the old slug text now point at a
	// page that no longer occupies that slug — clear them.
	if err := clearInboundLinksForSlug(tx, plan.repoID, oldSlugCI, pageID); err != nil {
		return ChangeResult{}, err
	}
	// Inbound links waiting for the new slug to materialize can now
	// resolve. (Symmetric to the create/restore case in applyUpsert.)
	if err := resolveInboundLinks(tx, plan.repoID, ch.dstSlugCI, pageID); err != nil {
		return ChangeResult{}, err
	}

	if err := renameLabels(tx, plan.repoID, existing.Slug, ch.dstSlug); err != nil {
		return ChangeResult{}, err
	}

	return ChangeResult{
		Op:         OpRename,
		Slug:       ch.dstSlug,
		PageID:     pageID,
		RevisionID: revisionID,
		BlobSHA:    existing.HeadBlobSHA,
		BodySize:   existing.BodySize,
	}, nil
}

// pageUpsertColumns is the shared column set written by both the
// update and the restore arms of applyUpsert. clearDeletedAt = true
// (restore) additionally NULLs deleted_at so the page is live again.
func pageUpsertColumns(ch plannedChange, blobSHA string, bodySize int, inline []byte, revisionID uint64, cs *db.WikiChangeset, plan changesetPlan, clearDeletedAt bool) map[string]any {
	out := map[string]any{
		"slug":              ch.srcSlug,
		"slug_ci_v1":        ch.srcSlugCI,
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
