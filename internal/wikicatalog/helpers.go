package wikicatalog

import (
	"time"

	"gh-server/internal/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// incrementBlobRef bumps refcount via a single ON CONFLICT statement.
// Size is consulted only on first insert; the SHA is content-derived
// so concurrent inserts can't disagree on it.
func incrementBlobRef(tx *gorm.DB, blobSHA string, size int, now time.Time) error {
	if blobSHA == "" {
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

// decrementBlobRef lowers the refcount for blobSHA. A row hitting
// zero is left in place (with refcount=0) so a follow-up GC pass can
// reclaim both the row and the on-disk blob; this avoids racing with
// concurrent inserts that may take a fresh reference.
func decrementBlobRef(tx *gorm.DB, blobSHA string) error {
	if blobSHA == "" {
		return nil
	}
	return tx.Model(&db.WikiBlobRef{}).
		Where("blob_sha = ?", blobSHA).
		UpdateColumn("refcount", gorm.Expr("refcount - 1")).Error
}

// ensureDirChain inserts a "tree" row for every intermediate directory
// in slugCI's parent chain. Inserts are idempotent via DoNothing
// upsert so concurrent creators of sibling pages don't conflict.
func ensureDirChain(tx *gorm.DB, repoID uint, slugCI string) error {
	for _, dir := range parentChain(slugCI) {
		parent, leaf := splitParentLeaf(dir)
		row := db.WikiDirIndex{
			RepositoryID: repoID,
			ParentDir:    parent,
			ChildName:    leaf,
			ChildKind:    ChildKindTree,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

// insertDirLeaf records slugCI's leaf entry in its parent directory.
func insertDirLeaf(tx *gorm.DB, repoID uint, slugCI string, pageID uint64) error {
	parent, leaf := splitParentLeaf(slugCI)
	row := db.WikiDirIndex{
		RepositoryID: repoID,
		ParentDir:    parent,
		ChildName:    leaf,
		ChildKind:    ChildKindBlob,
		PageID:       &pageID,
	}
	return tx.Create(&row).Error
}

// removeDirLeaf removes slugCI's leaf entry from its parent directory.
// Idempotent — missing rows are fine.
func removeDirLeaf(tx *gorm.DB, repoID uint, slugCI string) error {
	parent, leaf := splitParentLeaf(slugCI)
	return tx.Where("repository_id = ? AND parent_dir = ? AND child_name = ?",
		repoID, parent, leaf).
		Delete(&db.WikiDirIndex{}).Error
}

// pruneEmptyParents walks slugCI's ancestor chain from leaf up to
// root, removing each tree row whose directory has become empty.
// Stops at the first non-empty ancestor.
//
// Implementation: one GROUP BY query collects the live child counts
// for every ancestor at once, then we walk deepest-first deleting
// tree rows until we hit one with children. Replaces the legacy
// "COUNT then DELETE per ancestor" loop that did 2·depth round
// trips. Bounded by wikiMaxSlugDepth (≤ 6 ancestors per page).
func pruneEmptyParents(tx *gorm.DB, repoID uint, slugCI string) error {
	chain := parentChain(slugCI)
	if len(chain) == 0 {
		return nil
	}
	type row struct {
		ParentDir string
		N         int64
	}
	var rows []row
	if err := tx.Model(&db.WikiDirIndex{}).
		Select("parent_dir, COUNT(*) AS n").
		Where("repository_id = ? AND parent_dir IN ?", repoID, chain).
		Group("parent_dir").
		Find(&rows).Error; err != nil {
		return err
	}
	childCount := make(map[string]int64, len(chain))
	for _, r := range rows {
		childCount[r.ParentDir] = r.N
	}
	for i := len(chain) - 1; i >= 0; i-- {
		dir := chain[i]
		if childCount[dir] > 0 {
			return nil
		}
		parent, leaf := splitParentLeaf(dir)
		if err := tx.Where("repository_id = ? AND parent_dir = ? AND child_name = ? AND child_kind = ?",
			repoID, parent, leaf, ChildKindTree).
			Delete(&db.WikiDirIndex{}).Error; err != nil {
			return err
		}
		// Pruning this tree row decrements the parent's live count
		// for any subsequent iteration on the same chain.
		childCount[parent]--
	}
	return nil
}

// refreshOutlinks replaces the wiki_page_links rows for srcPageID
// with the current outbound link set extracted from body. Dangling
// links (no matching wiki_pages row in this repo) keep dst_page_id
// NULL and remain queryable by dst_slug_ci for the future resolver.
func refreshOutlinks(tx *gorm.DB, repoID uint, srcPageID uint64, body string) error {
	if err := tx.Where("src_page_id = ?", srcPageID).Delete(&db.WikiPageLink{}).Error; err != nil {
		return err
	}
	outs := ExtractOutlinks(body)
	if len(outs) == 0 {
		return nil
	}
	// Resolve dst_page_id for any link target that matches a LIVE
	// page (deleted_at IS NULL). Without this filter, a forward
	// reference to a soft-deleted slug would resolve to the
	// tombstoned page_id, breaking the catalog invariant that every
	// non-NULL dst_page_id points at a live page.
	var matches []db.WikiPage
	if err := tx.Select("page_id", "slug_ci_v1").
		Where("repository_id = ? AND slug_ci_v1 IN ? AND deleted_at IS NULL", repoID, outs).
		Find(&matches).Error; err != nil {
		return err
	}
	resolved := make(map[string]uint64, len(matches))
	for _, m := range matches {
		resolved[m.SlugCIV1] = m.PageID
	}
	rows := make([]db.WikiPageLink, 0, len(outs))
	for _, dst := range outs {
		link := db.WikiPageLink{
			RepositoryID: repoID,
			SrcPageID:    srcPageID,
			DstSlugCI:    dst,
		}
		if pid, ok := resolved[dst]; ok {
			pidCopy := pid
			link.DstPageID = &pidCopy
		}
		rows = append(rows, link)
	}
	return tx.Create(&rows).Error
}

// resolveInboundLinks fills in dst_page_id for any wiki_page_links
// row whose textual target matches slugCI but whose resolution had
// been left NULL because the target page did not exist when the
// source page was last written. Called from applyUpsert (create or
// restore) and applyRename (destination side) so that backlink
// queries on the just-materialized slug return immediately.
func resolveInboundLinks(tx *gorm.DB, repoID uint, slugCI string, pageID uint64) error {
	return tx.Model(&db.WikiPageLink{}).
		Where("repository_id = ? AND dst_slug_ci = ? AND dst_page_id IS NULL", repoID, slugCI).
		UpdateColumn("dst_page_id", pageID).Error
}

// clearInboundLinksForPage clears dst_page_id for every link that was
// resolved to pageID. Used by applyDelete: the page no longer
// occupies any slug, so the cached resolution is now phantom.
//
// The textual dst_slug_ci is left untouched so the resolver can
// re-link the row if a future create or rename re-occupies the slug.
func clearInboundLinksForPage(tx *gorm.DB, repoID uint, pageID uint64) error {
	return tx.Model(&db.WikiPageLink{}).
		Where("repository_id = ? AND dst_page_id = ?", repoID, pageID).
		UpdateColumn("dst_page_id", nil).Error
}

// clearInboundLinksForSlug clears dst_page_id for links whose textual
// dst_slug_ci is oldSlugCI and whose dst_page_id was pageID. Used by
// applyRename: the page has moved away from this slug, so the
// cached resolution is phantom; future incarnations of oldSlugCI
// (e.g. a recreate) will be picked up by resolveInboundLinks.
func clearInboundLinksForSlug(tx *gorm.DB, repoID uint, oldSlugCI string, pageID uint64) error {
	return tx.Model(&db.WikiPageLink{}).
		Where("repository_id = ? AND dst_slug_ci = ? AND dst_page_id = ?", repoID, oldSlugCI, pageID).
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
