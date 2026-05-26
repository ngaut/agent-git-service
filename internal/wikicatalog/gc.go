package wikicatalog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
)

// GCStats reports what one GCRun reclaimed.
type GCStats struct {
	PendingReclaimed int // wiki_pending_blobs rows + matching CAS files removed
	BlobsReclaimed   int // wiki_blob_refs rows with refcount=0 reclaimed
}

// GCRun reclaims orphaned blobs and zero-refcount entries. The
// operation is two-phase and idempotent:
//
//  1. wiki_pending_blobs rows older than pendingTTL whose SHA has no
//     wiki_blob_refs row mean an upload landed but the SQL
//     transaction failed; reclaim the CAS file and delete the
//     pending row.
//  2. wiki_blob_refs rows with refcount=0 older than refcountTTL
//     mean every reference is gone; reclaim the CAS file (if any)
//     and delete the refcount row.
//
// The TTLs deliberately exclude very-recently-written entries so a
// race between a CAS writer and the GC cannot reclaim a blob that's
// about to be referenced. pendingTTL = 1h matches the WAL retention
// the RFC §6.6 documents.
//
// Safe to call concurrently with ApplyChangeSet; the reclaim operates
// row-by-row with point queries and deletes only rows that survive
// the staleness check.
func (c *Catalog) GCRun(ctx context.Context, now time.Time, pendingTTL, refcountTTL time.Duration) (GCStats, error) {
	var stats GCStats

	// Phase 1: orphan pending blobs — rows older than pendingTTL
	// with no matching wiki_blob_refs row. One LEFT JOIN reads
	// exactly the reclaimable set instead of fetching every pending
	// row and then point-querying refs per orphan.
	pendingCutoff := now.Add(-pendingTTL)
	var orphans []db.WikiPendingBlob
	err := c.db(ctx).
		Table("wiki_pending_blobs AS p").
		Select("p.*").
		Joins("LEFT JOIN wiki_blob_refs AS r ON r.blob_sha = p.blob_sha").
		Where("p.written_at < ? AND r.blob_sha IS NULL", pendingCutoff).
		Find(&orphans).Error
	if err != nil {
		return stats, fmt.Errorf("wiki gc: list pending: %w", err)
	}
	for _, p := range orphans {
		reclaimed, err := c.reclaimPending(ctx, p)
		if err != nil {
			return stats, err
		}
		if reclaimed {
			stats.PendingReclaimed++
		}
	}

	// Phase 2: zero-refcount blobs.
	refcountCutoff := now.Add(-refcountTTL)
	var zeros []db.WikiBlobRef
	err = c.db(ctx).
		Where("refcount <= 0 AND last_seen < ?", refcountCutoff).
		Find(&zeros).Error
	if err != nil {
		return stats, fmt.Errorf("wiki gc: list zero refs: %w", err)
	}
	for _, r := range zeros {
		reclaimed, err := c.reclaimRef(ctx, r)
		if err != nil {
			return stats, err
		}
		if reclaimed {
			stats.BlobsReclaimed++
		}
	}

	return stats, nil
}

// reclaimPending removes a pending-WAL row plus the CAS file for the
// SHA. The LEFT JOIN scan above already excluded SHAs with a live
// reference; we recheck inside the reclaim to close the gap between
// list-time and delete-time (a fresh reference may have appeared in
// that interval).
func (c *Catalog) reclaimPending(ctx context.Context, p db.WikiPendingBlob) (bool, error) {
	var ref db.WikiBlobRef
	err := c.db(ctx).
		Where("blob_sha = ?", p.BlobSHA).
		Take(&ref).Error
	if err == nil {
		// A reference appeared after the JOIN; leave the pending
		// row alone — applyChange will clear it on next reference.
		return false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, fmt.Errorf("wiki gc: check ref for %s: %w", p.BlobSHA, err)
	}
	if c.Blob != nil {
		if err := c.Blob.Delete(ctx, p.BlobSHA); err != nil {
			return false, fmt.Errorf("wiki gc: delete CAS %s: %w", p.BlobSHA, err)
		}
	}
	if err := c.db(ctx).
		Where("blob_sha = ?", p.BlobSHA).
		Delete(&db.WikiPendingBlob{}).Error; err != nil {
		return false, fmt.Errorf("wiki gc: delete pending %s: %w", p.BlobSHA, err)
	}
	return true, nil
}

// reclaimRef removes a refcount=0 row and its CAS file (if any). The
// refcount > 0 race protection mirrors reclaimPending: an applyUpsert
// taking a fresh reference would have bumped refcount > 0; if that
// happened after our listing query, we leave the row alone.
func (c *Catalog) reclaimRef(ctx context.Context, r db.WikiBlobRef) (bool, error) {
	res := c.db(ctx).
		Where("blob_sha = ? AND refcount <= 0", r.BlobSHA).
		Delete(&db.WikiBlobRef{})
	if res.Error != nil {
		return false, fmt.Errorf("wiki gc: delete ref %s: %w", r.BlobSHA, res.Error)
	}
	if res.RowsAffected == 0 {
		// Someone took a fresh reference between list and delete; skip.
		return false, nil
	}
	if c.Blob != nil {
		if err := c.Blob.Delete(ctx, r.BlobSHA); err != nil {
			return false, fmt.Errorf("wiki gc: delete CAS %s: %w", r.BlobSHA, err)
		}
	}
	return true, nil
}
