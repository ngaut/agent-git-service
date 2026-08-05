package wikicatalog

import (
	"time"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// incrementBlobRef bumps the filesystem CAS refcount via a single ON CONFLICT
// statement. Inline bodies never materialize in the CAS and therefore need no
// reference row.
func incrementBlobRef(tx *gorm.DB, blobSHA string, size int, now time.Time) error {
	if blobSHA == "" || size <= MaxBodyInlineBytes {
		return nil
	}
	row := db.WikiBlobRef{
		BlobSHA:   blobSHA,
		Refcount:  1,
		Size:      size,
		FirstSeen: now,
		LastSeen:  now,
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "blob_sha"}},
		DoUpdates: clause.Assignments(map[string]any{
			"refcount":  gorm.Expr("refcount + 1"),
			"last_seen": now,
		}),
	}).Create(&row).Error
}

// decrementBlobRef lowers the filesystem CAS refcount for blobSHA. A row
// hitting zero is left in place so a follow-up GC pass can reclaim both the
// row and the on-disk blob without racing a concurrent fresh reference.
func decrementBlobRef(tx *gorm.DB, blobSHA string, size int) error {
	if blobSHA == "" || size <= MaxBodyInlineBytes {
		return nil
	}
	return tx.Model(&db.WikiBlobRef{}).
		Where("blob_sha = ?", blobSHA).
		UpdateColumn("refcount", gorm.Expr("refcount - 1")).Error
}

// refreshOutlinks writes the current outbound link set for srcPageID. When
// replace is true it first removes the previous set; creates and restores pass
// false because no live outlinks can exist for their page. A prepared
// single-upsert snapshot can provide every live target and avoid re-reading
// wiki_pages inside the transaction.
func refreshOutlinks(
	tx *gorm.DB,
	repoID uint,
	srcPageID uint64,
	srcSlug string,
	body string,
	replace bool,
	snapshotTargets map[string]uint64,
	snapshotKnown bool,
) error {
	outs := ExtractOutlinks(body)
	if replace {
		if err := tx.Where("src_page_id = ?", srcPageID).Delete(&db.WikiPageLink{}).Error; err != nil {
			return err
		}
	}
	if len(outs) == 0 {
		return nil
	}
	// Resolve dst_page_id for any link target that matches a LIVE
	// page (deleted_at IS NULL). Without this filter, a forward
	// reference to a soft-deleted slug would resolve to the
	// tombstoned page_id, breaking the catalog invariant that every
	// non-NULL dst_page_id points at a live page.
	resolved := snapshotTargets
	if !snapshotKnown {
		var matches []db.WikiPage
		if err := tx.Select("page_id", "slug").
			Where("repository_id = ? AND slug IN ? AND deleted_at IS NULL", repoID, outs).
			Find(&matches).Error; err != nil {
			return err
		}
		resolved = make(map[string]uint64, len(matches))
		for _, m := range matches {
			resolved[m.Slug] = m.PageID
		}
	}
	rows := make([]db.WikiPageLink, 0, len(outs))
	for _, dst := range outs {
		link := db.WikiPageLink{
			RepositoryID: repoID,
			SrcPageID:    srcPageID,
			DstSlug:      dst,
		}
		pid, ok := resolved[dst]
		if snapshotKnown && dst == srcSlug {
			pid, ok = srcPageID, true
		}
		if ok {
			pidCopy := pid
			link.DstPageID = &pidCopy
		}
		rows = append(rows, link)
	}
	return tx.Create(&rows).Error
}

// resolveInboundLinks fills in dst_page_id for any wiki_page_links
// row whose textual target matches slug but whose resolution had
// been left NULL because the target page did not exist when the
// source page was last written. Called from applyUpsert (create or
// restore) and applyRename (destination side) so that backlink
// queries on the just-materialized slug return immediately.
func resolveInboundLinks(tx *gorm.DB, repoID uint, slug string, pageID uint64) error {
	return tx.Model(&db.WikiPageLink{}).
		Where("repository_id = ? AND dst_slug = ? AND dst_page_id IS NULL", repoID, slug).
		UpdateColumn("dst_page_id", pageID).Error
}

// clearInboundLinksForPage clears dst_page_id for every link that was
// resolved to pageID. Used by applyDelete: the page no longer
// occupies any slug, so the cached resolution is now phantom.
//
// The textual dst_slug is left untouched so the resolver can
// re-link the row if a future create or rename re-occupies the slug.
func clearInboundLinksForPage(tx *gorm.DB, repoID uint, pageID uint64) error {
	return tx.Model(&db.WikiPageLink{}).
		Where("repository_id = ? AND dst_page_id = ?", repoID, pageID).
		UpdateColumn("dst_page_id", nil).Error
}

// clearInboundLinksForSlug clears dst_page_id for links whose textual
// dst_slug is oldSlug and whose dst_page_id was pageID. Used by
// applyRename: the page has moved away from this slug, so the
// cached resolution is phantom; future incarnations of oldSlug
// (e.g. a recreate) will be picked up by resolveInboundLinks.
func clearInboundLinksForSlug(tx *gorm.DB, repoID uint, oldSlug string, pageID uint64) error {
	return tx.Model(&db.WikiPageLink{}).
		Where("repository_id = ? AND dst_slug = ? AND dst_page_id = ?", repoID, oldSlug, pageID).
		UpdateColumn("dst_page_id", nil).Error
}

// renameLabels moves WikiPageLabel rows from oldSlug to newSlug.
// The legacy implementation read-deleted-reinserted in three round
// trips because slug is part of the composite PK; in practice we
// only enter this path from applyRename, where the destination slug
// is guaranteed not to have its own label rows (the move's
// destination-occupied check enforces that). One UPDATE is enough.
//
// If a follow-up workflow ever introduces a way for the destination
// slug to already carry labels before the rename, this needs to
// revert to read-delete-reinsert with ON CONFLICT DO NOTHING.
func renameLabels(tx *gorm.DB, repoID uint, oldSlug, newSlug string) error {
	return tx.Model(&db.WikiPageLabel{}).
		Where("repository_id = ? AND slug = ?", repoID, oldSlug).
		UpdateColumn("slug", newSlug).Error
}
