package wikicatalog

import (
	"context"
	"testing"
	"time"

	"gh-server/internal/db"
)

func TestGCRun_ReclaimsOrphanPendingBlobs(t *testing.T) {
	cat, _, gdb := applyTestEnv(t)
	ctx := context.Background()

	// Plant an orphan pending row (no matching ref) plus a CAS file.
	body := make([]byte, MaxBodyInlineBytes+8)
	for i := range body {
		body[i] = byte(i & 0xff)
	}
	sha, err := cat.Blob.Put(ctx, body)
	if err != nil {
		t.Fatalf("plant blob: %v", err)
	}
	written := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	if err := gdb.Create(&db.WikiPendingBlob{
		BlobSHA: sha, WrittenAt: written, Size: len(body),
	}).Error; err != nil {
		t.Fatalf("plant pending: %v", err)
	}

	stats, err := cat.GCRun(ctx, written.Add(2*time.Hour), 1*time.Hour, 1*time.Hour)
	if err != nil {
		t.Fatalf("GCRun: %v", err)
	}
	if stats.PendingReclaimed != 1 {
		t.Fatalf("PendingReclaimed = %d, want 1", stats.PendingReclaimed)
	}
	var remaining int64
	gdb.Model(&db.WikiPendingBlob{}).Where("blob_sha = ?", sha).Count(&remaining)
	if remaining != 0 {
		t.Fatalf("pending row not deleted; count=%d", remaining)
	}
	ok, err := cat.Blob.Has(ctx, sha)
	if err != nil {
		t.Fatalf("Has: %v", err)
	}
	if ok {
		t.Fatalf("CAS file should have been reclaimed")
	}
}

func TestGCRun_HonorsPendingTTL(t *testing.T) {
	cat, _, gdb := applyTestEnv(t)
	ctx := context.Background()

	body := make([]byte, MaxBodyInlineBytes+8)
	sha, _ := cat.Blob.Put(ctx, body)
	written := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	gdb.Create(&db.WikiPendingBlob{BlobSHA: sha, WrittenAt: written, Size: len(body)})

	// GC at written + 30m with TTL=1h: too young; nothing reclaimed.
	stats, err := cat.GCRun(ctx, written.Add(30*time.Minute), 1*time.Hour, 1*time.Hour)
	if err != nil {
		t.Fatalf("GCRun: %v", err)
	}
	if stats.PendingReclaimed != 0 {
		t.Fatalf("must not reclaim within TTL; got %d", stats.PendingReclaimed)
	}
}

func TestGCRun_ReclaimsZeroRefcountBlobs(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	// Create then delete a large page so the ref drops to 0.
	body := make([]byte, MaxBodyInlineBytes+1)
	for i := range body {
		body[i] = 'q'
	}
	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "big", Body: body}},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpDelete, Slug: "big"}},
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	sha := HashContent(body)

	// Move LastSeen far enough into the past that the TTL trips.
	past := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	gdb.Model(&db.WikiBlobRef{}).
		Where("blob_sha = ?", sha).
		UpdateColumn("last_seen", past)

	stats, err := cat.GCRun(ctx, past.Add(2*time.Hour), 1*time.Hour, 1*time.Hour)
	if err != nil {
		t.Fatalf("GCRun: %v", err)
	}
	if stats.BlobsReclaimed != 1 {
		t.Fatalf("BlobsReclaimed = %d, want 1", stats.BlobsReclaimed)
	}
	var rows int64
	gdb.Model(&db.WikiBlobRef{}).Where("blob_sha = ?", sha).Count(&rows)
	if rows != 0 {
		t.Fatalf("ref row not deleted; count=%d", rows)
	}
	ok, _ := cat.Blob.Has(ctx, sha)
	if ok {
		t.Fatalf("CAS file should be reclaimed for zero-ref blob")
	}
}

func TestGCRun_SkipsPendingWithLiveRef(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	// Real upsert: pending gets cleared in-txn but for the test we
	// simulate the case where someone re-inserts a pending row
	// for a SHA that already has a live ref. GC must leave it alone.
	body := make([]byte, MaxBodyInlineBytes+1)
	for i := range body {
		body[i] = 'r'
	}
	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "page", Body: body}},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	sha := HashContent(body)
	written := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	if err := gdb.Create(&db.WikiPendingBlob{
		BlobSHA: sha, WrittenAt: written, Size: len(body),
	}).Error; err != nil {
		t.Fatalf("plant pending: %v", err)
	}

	stats, err := cat.GCRun(ctx, written.Add(2*time.Hour), 1*time.Hour, 1*time.Hour)
	if err != nil {
		t.Fatalf("GCRun: %v", err)
	}
	if stats.PendingReclaimed != 0 {
		t.Fatalf("must not reclaim pending with a live ref; got %d", stats.PendingReclaimed)
	}
	// The CAS file must survive.
	ok, _ := cat.Blob.Has(ctx, sha)
	if !ok {
		t.Fatalf("CAS file was reclaimed despite live ref")
	}
}
