package wikicatalog

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// applyTestEnv wires together a Catalog backed by an in-memory
// SQLite database and a temp-dir BlobStore. Returns the catalog plus
// a seeded repository id.
func applyTestEnv(t *testing.T) (*Catalog, uint, *gorm.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "catalog.db")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, err := gdb.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.Migrate(gdb); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	user := db.User{Login: "alice", Type: "User", Email: "a@example.com"}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	repo := db.Repository{OwnerID: user.ID, Name: "wiki", FullName: "alice/wiki", DefaultBranch: "main"}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	store := NewBlobStore(t.TempDir())
	cat := New(gdb, store)
	cat.Now = func() time.Time { return time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC) }
	return cat, repo.ID, gdb
}

func TestApplyChangeSet_CreateSinglePage(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	body := []byte("# Home\n\nWelcome.\n")
	res, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID,
		Source:       SourceREST,
		Message:      "create home",
		Changes: []Change{
			{Op: OpUpsert, Slug: "home", Body: body},
		},
	})
	if err != nil {
		t.Fatalf("ApplyChangeSet: %v", err)
	}
	if len(res.Changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(res.Changes))
	}
	got := res.Changes[0]
	wantSHA := HashContent(body)
	if got.BlobSHA != wantSHA {
		t.Fatalf("blob sha %q, want %q", got.BlobSHA, wantSHA)
	}
	if got.RevisionID != 1 {
		t.Fatalf("revision %d, want 1", got.RevisionID)
	}
	if got.PageID == 0 {
		t.Fatalf("page id not populated")
	}

	// Verify catalog state:
	var page db.WikiPage
	if err := gdb.First(&page, "page_id = ?", got.PageID).Error; err != nil {
		t.Fatalf("read page: %v", err)
	}
	if page.Slug != "home" || page.HeadBlobSHA != wantSHA {
		t.Fatalf("page row mismatch: %+v", page)
	}
	if page.HeadChangesetID != res.ChangesetID {
		t.Fatalf("head_changeset_id = %d, want %d", page.HeadChangesetID, res.ChangesetID)
	}
	if string(page.BodyInline) != string(body) {
		t.Fatalf("body_inline mismatch: %q vs %q", page.BodyInline, body)
	}

	var head db.WikiRepoHead
	if err := gdb.First(&head, "repository_id = ?", repoID).Error; err != nil {
		t.Fatalf("read head: %v", err)
	}
	if head.HeadChangesetID != res.ChangesetID {
		t.Fatalf("head changeset = %d, want %d", head.HeadChangesetID, res.ChangesetID)
	}

	var dir db.WikiDirIndex
	if err := gdb.Where("repository_id = ? AND parent_dir = ? AND child_name = ?",
		repoID, "", "home").Take(&dir).Error; err != nil {
		t.Fatalf("read dir leaf: %v", err)
	}
	if dir.ChildKind != "blob" || dir.PageID == nil || *dir.PageID != got.PageID {
		t.Fatalf("dir leaf wrong: %+v", dir)
	}

	var ref db.WikiBlobRef
	if err := gdb.First(&ref, "blob_sha = ?", wantSHA).Error; err != nil {
		t.Fatalf("read blob ref: %v", err)
	}
	if ref.Refcount != 1 {
		t.Fatalf("refcount = %d, want 1", ref.Refcount)
	}

	// This body is small (well under MaxBodyInlineBytes), so the
	// blob lives in body_inline only and the CAS filesystem must
	// not be touched. Asserting absence pins the inline-only path.
	ok, err := cat.Blob.Has(ctx, wantSHA)
	if err != nil {
		t.Fatalf("Has error: %v", err)
	}
	if ok {
		t.Fatalf("inline-sized body should not materialize a CAS file")
	}
}

func TestApplyChangeSet_LargeBodyGoesToCAS(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	// Body just over the inline limit forces the CAS path.
	body := make([]byte, MaxBodyInlineBytes+1)
	for i := range body {
		body[i] = byte('a' + (i % 26))
	}
	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "big", Body: body}},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	sha := HashContent(body)
	ok, err := cat.Blob.Has(ctx, sha)
	if err != nil || !ok {
		t.Fatalf("CAS missing large blob: ok=%v err=%v", ok, err)
	}
	// Pending WAL row removed in-txn.
	var pending int64
	gdb.Model(&db.WikiPendingBlob{}).Where("blob_sha = ?", sha).Count(&pending)
	if pending != 0 {
		t.Fatalf("pending row not cleared in txn: count=%d", pending)
	}
	// Page row body_inline should be nil for large body.
	var page db.WikiPage
	gdb.First(&page, "repository_id = ? AND slug = ?", repoID, "big")
	if page.BodyInline != nil {
		t.Fatalf("large body must not be inlined")
	}
}

func TestApplyChangeSet_UpdateExistingPage(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	_, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "home", Body: []byte("v1")}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	res2, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "home", Body: []byte("v2")}},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if res2.Changes[0].RevisionID != 2 {
		t.Fatalf("expected revision 2, got %d", res2.Changes[0].RevisionID)
	}
	wantSHA := HashContent([]byte("v2"))
	if res2.Changes[0].BlobSHA != wantSHA {
		t.Fatalf("blob sha mismatch")
	}
	// Verify old blob's refcount dropped, new blob's is 1.
	oldSHA := HashContent([]byte("v1"))
	var oldRef, newRef db.WikiBlobRef
	if err := gdb.First(&oldRef, "blob_sha = ?", oldSHA).Error; err != nil {
		t.Fatalf("read old ref: %v", err)
	}
	if oldRef.Refcount != 0 {
		t.Fatalf("old refcount = %d, want 0", oldRef.Refcount)
	}
	if err := gdb.First(&newRef, "blob_sha = ?", wantSHA).Error; err != nil {
		t.Fatalf("read new ref: %v", err)
	}
	if newRef.Refcount != 1 {
		t.Fatalf("new refcount = %d, want 1", newRef.Refcount)
	}
	// History: 2 revisions for this page.
	var revs []db.WikiPageRevision
	if err := gdb.Where("page_id = ?", res2.Changes[0].PageID).
		Order("revision_id ASC").Find(&revs).Error; err != nil {
		t.Fatalf("read revisions: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("expected 2 revisions, got %d", len(revs))
	}
	if revs[0].Op != "create" || revs[1].Op != "update" {
		t.Fatalf("revision ops: %q, %q", revs[0].Op, revs[1].Op)
	}
}

func TestApplyChangeSet_IfMatchConflict(t *testing.T) {
	cat, repoID, _ := applyTestEnv(t)
	ctx := context.Background()

	res, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "home", Body: []byte("v1")}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	currentSHA := res.Changes[0].BlobSHA

	_, err = cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{
			{Op: OpUpsert, Slug: "home", Body: []byte("v2"),
				IfMatch: "0000000000000000000000000000000000000000"},
		},
	})
	var cerr *ConflictError
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *ConflictError, got %v", err)
	}
	if cerr.Code != ConflictCodeStale {
		t.Fatalf("conflict code %q, want SOURCE_STALE", cerr.Code)
	}
	if cerr.CurrentSHA != currentSHA {
		t.Fatalf("current sha %q, want %q", cerr.CurrentSHA, currentSHA)
	}
}

func TestApplyChangeSet_IfMatchSuccess(t *testing.T) {
	cat, repoID, _ := applyTestEnv(t)
	ctx := context.Background()

	res, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "home", Body: []byte("v1")}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{
			{Op: OpUpsert, Slug: "home", Body: []byte("v2"),
				IfMatch: res.Changes[0].BlobSHA},
		},
	})
	if err != nil {
		t.Fatalf("update with correct IfMatch: %v", err)
	}
}

func TestApplyChangeSet_PrefixCollision(t *testing.T) {
	cat, repoID, _ := applyTestEnv(t)
	ctx := context.Background()

	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "guides", Body: []byte("g")}},
	}); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	// Now try to create "guides/intro" — should collide because
	// "guides" is a leaf, not a directory.
	_, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "guides/intro", Body: []byte("i")}},
	})
	var cerr *ConflictError
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *ConflictError, got %v", err)
	}
	if cerr.Code != ConflictCodePrefix {
		t.Fatalf("conflict code %q, want PREFIX_COLLISION", cerr.Code)
	}
}

func TestApplyChangeSet_DeletePage(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	res, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "home", Body: []byte("body")}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	pageID := res.Changes[0].PageID
	oldSHA := res.Changes[0].BlobSHA

	_, err = cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpDelete, Slug: "home"}},
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	var page db.WikiPage
	if err := gdb.First(&page, "page_id = ?", pageID).Error; err != nil {
		t.Fatalf("read page after delete: %v", err)
	}
	if page.DeletedAt == nil {
		t.Fatalf("page not soft-deleted")
	}

	// dir_index leaf removed.
	var dirCount int64
	gdb.Model(&db.WikiDirIndex{}).
		Where("repository_id = ? AND child_name = ?", repoID, "home").
		Count(&dirCount)
	if dirCount != 0 {
		t.Fatalf("dir leaf remained: count=%d", dirCount)
	}

	// Blob refcount dropped to 0.
	var ref db.WikiBlobRef
	if err := gdb.First(&ref, "blob_sha = ?", oldSHA).Error; err != nil {
		t.Fatalf("read ref: %v", err)
	}
	if ref.Refcount != 0 {
		t.Fatalf("refcount = %d, want 0", ref.Refcount)
	}

	// Tombstone revision recorded.
	var lastRev db.WikiPageRevision
	if err := gdb.Where("page_id = ?", pageID).
		Order("revision_id DESC").First(&lastRev).Error; err != nil {
		t.Fatalf("read last rev: %v", err)
	}
	if lastRev.Op != "delete" || lastRev.BlobSHA != "" {
		t.Fatalf("delete revision wrong: %+v", lastRev)
	}

	// Deleting again returns ErrPageNotFound.
	_, err = cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpDelete, Slug: "home"}},
	})
	if !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("re-delete: expected ErrPageNotFound, got %v", err)
	}
}

func TestApplyChangeSet_RenamePage(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	createRes, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "old-name", Body: []byte("body")}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	pageID := createRes.Changes[0].PageID
	originalSHA := createRes.Changes[0].BlobSHA

	_, err = cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpRename, Slug: "old-name", NewSlug: "new-name"}},
	})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}

	// Page identity preserved.
	var page db.WikiPage
	if err := gdb.First(&page, "page_id = ?", pageID).Error; err != nil {
		t.Fatalf("read page: %v", err)
	}
	if page.Slug != "new-name" {
		t.Fatalf("rename did not update slug: %+v", page)
	}
	if page.HeadBlobSHA != originalSHA {
		t.Fatalf("rename should preserve blob SHA")
	}

	// dir_index reanchored.
	var oldDir int64
	gdb.Model(&db.WikiDirIndex{}).
		Where("repository_id = ? AND child_name = ?", repoID, "old-name").
		Count(&oldDir)
	if oldDir != 0 {
		t.Fatalf("old dir leaf survived")
	}
	var newDir db.WikiDirIndex
	if err := gdb.Where("repository_id = ? AND child_name = ?", repoID, "new-name").
		Take(&newDir).Error; err != nil {
		t.Fatalf("read new dir leaf: %v", err)
	}

	// Renaming back to occupied destination fails.
	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "occupied", Body: []byte("o")}},
	}); err != nil {
		t.Fatalf("create occupied: %v", err)
	}
	_, err = cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpRename, Slug: "new-name", NewSlug: "occupied"}},
	})
	var cerr *ConflictError
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *ConflictError, got %v", err)
	}
	if cerr.Code != ConflictCodeDestinationTake {
		t.Fatalf("conflict code %q, want DESTINATION_EXISTS", cerr.Code)
	}
}

func TestApplyChangeSet_DeleteMissingReturnsErrPageNotFound(t *testing.T) {
	cat, repoID, _ := applyTestEnv(t)
	_, err := cat.ApplyChangeSet(context.Background(), ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpDelete, Slug: "never-existed"}},
	})
	if !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("expected ErrPageNotFound, got %v", err)
	}
}

func TestApplyChangeSet_OutlinksRefreshed(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	// Page A points at B and C.
	res, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "a",
			Body: []byte("see [[b]] and [[c]]")}},
	})
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	pageA := res.Changes[0].PageID

	var links []db.WikiPageLink
	if err := gdb.Where("src_page_id = ?", pageA).Order("dst_slug").
		Find(&links).Error; err != nil {
		t.Fatalf("read links: %v", err)
	}
	if len(links) != 2 || links[0].DstSlug != "b" || links[1].DstSlug != "c" {
		t.Fatalf("links wrong: %+v", links)
	}
	if links[0].DstPageID != nil || links[1].DstPageID != nil {
		t.Fatalf("unresolved links should have nil DstPageID")
	}

	// Now create B and re-upsert A; A's link to B should resolve.
	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "b", Body: []byte("b body")}},
	}); err != nil {
		t.Fatalf("create b: %v", err)
	}
	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "a",
			Body: []byte("see [[b]] only now")}},
	}); err != nil {
		t.Fatalf("update a: %v", err)
	}
	links = nil
	if err := gdb.Where("src_page_id = ?", pageA).
		Find(&links).Error; err != nil {
		t.Fatalf("read links 2: %v", err)
	}
	if len(links) != 1 || links[0].DstSlug != "b" {
		t.Fatalf("links after update wrong: %+v", links)
	}
	if links[0].DstPageID == nil {
		t.Fatalf("B link should now resolve")
	}
}

func TestApplyChangeSet_ExpectedParentMismatchReturnsErrCASLost(t *testing.T) {
	cat, repoID, _ := applyTestEnv(t)
	ctx := context.Background()

	_, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "home", Body: []byte("a")}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	wrong := uint64(99999)
	_, err = cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID:   repoID,
		Source:         SourceREST,
		ExpectedParent: &wrong,
		Changes:        []Change{{Op: OpUpsert, Slug: "home", Body: []byte("b")}},
	})
	if !errors.Is(err, ErrCASLost) {
		t.Fatalf("expected ErrCASLost, got %v", err)
	}
}

// TestApplyChangeSet_PostCommitHook_FiresOnce: the post-commit hook
// runs exactly once per successful changeset and receives the result
// the caller will see. Errors from the hook surface to the caller
// but do not undo the catalog state.
func TestApplyChangeSet_PostCommitHook_FiresOnce(t *testing.T) {
	cat, repoID, _ := applyTestEnv(t)
	ctx := context.Background()

	var calls int
	var seen ChangeSetResult
	cat.OnChangeSetCommitted = func(_ context.Context, _ uint, r ChangeSetResult) error {
		calls++
		seen = r
		return nil
	}
	res, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "home", Body: []byte("body")}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if calls != 1 {
		t.Fatalf("hook fired %d times, want 1", calls)
	}
	if seen.ChangesetID != res.ChangesetID || len(seen.Changes) != 1 {
		t.Fatalf("hook saw %+v, want %+v", seen, res)
	}
}

func TestApplyChangeSet_PostCommitHook_ErrorSurfaces(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	wantErr := errors.New("search index down")
	cat.OnChangeSetCommitted = func(_ context.Context, _ uint, _ ChangeSetResult) error {
		return wantErr
	}
	_, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "home", Body: []byte("body")}},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected hook error to surface, got %v", err)
	}
	// Catalog state landed even though the hook errored.
	var page db.WikiPage
	if err := gdb.First(&page, "repository_id = ? AND slug = ?", repoID, "home").Error; err != nil {
		t.Fatalf("page should be committed despite hook failure: %v", err)
	}
}

func TestApplyChangeSet_OverrideCommitSHA(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	originalSHA := "abcdef1234567890abcdef1234567890abcdef12"
	historical := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	res, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID:        repoID,
		Source:              SourceMigration,
		OverrideCommitSHA:   originalSHA,
		OverrideCommittedAt: &historical,
		Message:             "imported",
		Changes:             []Change{{Op: OpUpsert, Slug: "home", Body: []byte("legacy")}},
	})
	if err != nil {
		t.Fatalf("migration apply: %v", err)
	}
	if res.CommitSHA != originalSHA {
		t.Fatalf("CommitSHA = %q, want %q (override)", res.CommitSHA, originalSHA)
	}
	var cs db.WikiChangeset
	if err := gdb.First(&cs, "changeset_id = ?", res.ChangesetID).Error; err != nil {
		t.Fatalf("read changeset: %v", err)
	}
	if cs.SynthCommitSHA != originalSHA {
		t.Fatalf("stored synth_commit_sha = %q, want %q", cs.SynthCommitSHA, originalSHA)
	}
	if !cs.CommittedAt.Equal(historical) {
		t.Fatalf("committed_at = %v, want %v", cs.CommittedAt, historical)
	}

	// The corresponding revision row carries the same commit SHA, so
	// GetWikiPage?ref=<original SHA> can find it.
	var rev db.WikiPageRevision
	if err := gdb.First(&rev, "page_id = ?", res.Changes[0].PageID).Error; err != nil {
		t.Fatalf("read revision: %v", err)
	}
	if rev.CommitSHA != originalSHA {
		t.Fatalf("revision.commit_sha = %q, want %q", rev.CommitSHA, originalSHA)
	}
}

// TestApplyChangeSet_RecreateAfterDelete locks the soft-delete + restore
// model: the same slug may be created again after a delete,
// resulting in a single page_id whose revision chain records the full
// create→…→delete→restore history.
func TestApplyChangeSet_RecreateAfterDelete(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	createRes, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "home", Body: []byte("v1")}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	originalPageID := createRes.Changes[0].PageID

	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpDelete, Slug: "home"}},
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	recreateRes, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "home", Body: []byte("v2 — restored")}},
	})
	if err != nil {
		t.Fatalf("recreate after delete: %v", err)
	}

	// Page identity is preserved across the soft-delete cycle.
	if recreateRes.Changes[0].PageID != originalPageID {
		t.Fatalf("recreate page_id = %d, want preserved %d",
			recreateRes.Changes[0].PageID, originalPageID)
	}

	// The page row must be live again, with the new body.
	var page db.WikiPage
	if err := gdb.First(&page, "page_id = ?", originalPageID).Error; err != nil {
		t.Fatalf("read page: %v", err)
	}
	if page.DeletedAt != nil {
		t.Fatalf("page should be live after recreate; deleted_at = %v", page.DeletedAt)
	}
	if page.BodySize != len("v2 — restored") {
		t.Fatalf("body size %d, want %d", page.BodySize, len("v2 — restored"))
	}

	// Revision chain records all three ops.
	var revs []db.WikiPageRevision
	if err := gdb.Where("page_id = ?", originalPageID).
		Order("revision_id ASC").Find(&revs).Error; err != nil {
		t.Fatalf("read revisions: %v", err)
	}
	if len(revs) != 3 {
		t.Fatalf("expected 3 revisions (create, delete, restore), got %d", len(revs))
	}
	gotOps := []string{revs[0].Op, revs[1].Op, revs[2].Op}
	wantOps := []string{"create", "delete", "restore"}
	for i := range wantOps {
		if gotOps[i] != wantOps[i] {
			t.Fatalf("revision %d op = %q, want %q", i+1, gotOps[i], wantOps[i])
		}
	}

	// Dir leaf is back; pruneEmptyParents must not have orphaned it.
	var dir db.WikiDirIndex
	if err := gdb.Where("repository_id = ? AND parent_dir = ? AND child_name = ?",
		repoID, "", "home").Take(&dir).Error; err != nil {
		t.Fatalf("dir leaf missing after restore: %v", err)
	}
}

// TestApplyChangeSet_DeleteNullsInboundLinks: when page B is deleted,
// any page A whose link row pointed at B's page_id must have its
// dst_page_id cleared to NULL. Otherwise backlink queries via
// idx_wiki_links_dst_resolved return phantom hits against a
// soft-deleted page.
func TestApplyChangeSet_DeleteNullsInboundLinks(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "b", Body: []byte("b body")}},
	}); err != nil {
		t.Fatalf("create b: %v", err)
	}
	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "a", Body: []byte("see [[b]]")}},
	}); err != nil {
		t.Fatalf("create a: %v", err)
	}

	var pre db.WikiPageLink
	if err := gdb.Where("dst_slug = ?", "b").Take(&pre).Error; err != nil {
		t.Fatalf("read link pre-delete: %v", err)
	}
	if pre.DstPageID == nil {
		t.Fatalf("inbound link must be resolved before delete")
	}

	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpDelete, Slug: "b"}},
	}); err != nil {
		t.Fatalf("delete b: %v", err)
	}

	var post db.WikiPageLink
	if err := gdb.Where("dst_slug = ?", "b").Take(&post).Error; err != nil {
		t.Fatalf("read link post-delete: %v", err)
	}
	if post.DstPageID != nil {
		t.Fatalf("inbound link to deleted page must have NULL dst_page_id, got %d", *post.DstPageID)
	}
}

// TestApplyChangeSet_CreateResolvesPendingInboundLinks: a forward
// reference (A → B before B exists) must auto-resolve when B is
// created in a later changeset, so backlink queries do not depend on
// later rewrites of A.
func TestApplyChangeSet_CreateResolvesPendingInboundLinks(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "a", Body: []byte("see [[b]]")}},
	}); err != nil {
		t.Fatalf("create a: %v", err)
	}

	// Confirm A's link is unresolved while B does not exist.
	var unresolved db.WikiPageLink
	if err := gdb.Where("dst_slug = ?", "b").Take(&unresolved).Error; err != nil {
		t.Fatalf("read unresolved link: %v", err)
	}
	if unresolved.DstPageID != nil {
		t.Fatalf("link should be unresolved before B exists, got dst_page_id=%d", *unresolved.DstPageID)
	}

	bRes, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "b", Body: []byte("b body")}},
	})
	if err != nil {
		t.Fatalf("create b: %v", err)
	}

	var resolved db.WikiPageLink
	if err := gdb.Where("dst_slug = ?", "b").Take(&resolved).Error; err != nil {
		t.Fatalf("read resolved link: %v", err)
	}
	if resolved.DstPageID == nil || *resolved.DstPageID != bRes.Changes[0].PageID {
		t.Fatalf("link should resolve to B's page_id after B is created, got %v", resolved.DstPageID)
	}
}

// TestApplyChangeSet_OutlinksDoNotResolveToSoftDeleted: a forward
// reference to a soft-deleted page must stay unresolved, otherwise
// every backlink query via the resolved page-id index surfaces a
// phantom hit. Regression test for the strict review's CRITICAL #1.
func TestApplyChangeSet_OutlinksDoNotResolveToSoftDeleted(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "b", Body: []byte("b body")}},
	}); err != nil {
		t.Fatalf("create b: %v", err)
	}
	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpDelete, Slug: "b"}},
	}); err != nil {
		t.Fatalf("delete b: %v", err)
	}
	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "a", Body: []byte("see [[b]]")}},
	}); err != nil {
		t.Fatalf("create a: %v", err)
	}

	var link db.WikiPageLink
	if err := gdb.Where("dst_slug = ?", "b").Take(&link).Error; err != nil {
		t.Fatalf("read link: %v", err)
	}
	if link.DstPageID != nil {
		t.Fatalf("link to soft-deleted page resolved to %d; want NULL", *link.DstPageID)
	}
}

func TestApplyChangeSet_RenameWithIfMatchMismatch(t *testing.T) {
	cat, repoID, _ := applyTestEnv(t)
	ctx := context.Background()

	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "src", Body: []byte("body")}},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{
			{Op: OpRename, Slug: "src", NewSlug: "dst",
				IfMatch: "0000000000000000000000000000000000000000"},
		},
	})
	var cerr *ConflictError
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *ConflictError, got %v", err)
	}
	if cerr.Code != ConflictCodeStale {
		t.Fatalf("conflict code = %q, want SOURCE_STALE", cerr.Code)
	}
}

// TestApplyChangeSet_PrefixMoveAsBatch: simulate the prefix-move
// service-level operation as a multi-rename changeset and verify
// dir_index updates and revision rows land consistently within one
// transaction.
func TestApplyChangeSet_PrefixMoveAsBatch(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{
			{Op: OpUpsert, Slug: "foo/a", Body: []byte("a")},
			{Op: OpUpsert, Slug: "foo/b", Body: []byte("b")},
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceBatch,
		Message: "rename foo/* to bar/*",
		Changes: []Change{
			{Op: OpRename, Slug: "foo/a", NewSlug: "bar/a"},
			{Op: OpRename, Slug: "foo/b", NewSlug: "bar/b"},
		},
	}); err != nil {
		t.Fatalf("prefix-move: %v", err)
	}

	// Both renamed pages live at new slugs.
	for _, slug := range []string{"bar/a", "bar/b"} {
		var p db.WikiPage
		if err := gdb.Where("repository_id = ? AND slug = ?", repoID, slug).
			Take(&p).Error; err != nil {
			t.Fatalf("missing renamed page %q: %v", slug, err)
		}
		if p.DeletedAt != nil {
			t.Fatalf("page %q should be live", slug)
		}
	}

	// Old parent directory pruned.
	var oldFooChildren int64
	gdb.Model(&db.WikiDirIndex{}).
		Where("repository_id = ? AND parent_dir = ?", repoID, "foo").
		Count(&oldFooChildren)
	if oldFooChildren != 0 {
		t.Fatalf("expected old parent 'foo' to be empty, got %d children", oldFooChildren)
	}
	var oldFooTree int64
	gdb.Model(&db.WikiDirIndex{}).
		Where("repository_id = ? AND parent_dir = ? AND child_name = ? AND child_kind = ?",
			repoID, "", "foo", "tree").
		Count(&oldFooTree)
	if oldFooTree != 0 {
		t.Fatalf("expected 'foo' tree row to be pruned, got %d", oldFooTree)
	}

	// New parent directory materialized.
	var newBarTree int64
	gdb.Model(&db.WikiDirIndex{}).
		Where("repository_id = ? AND parent_dir = ? AND child_name = ? AND child_kind = ?",
			repoID, "", "bar", "tree").
		Count(&newBarTree)
	if newBarTree != 1 {
		t.Fatalf("expected 'bar' tree row, got %d", newBarTree)
	}

}

// TestApplyChangeSet_PrefixCollisionDetectsNestedPage: creating a
// blob whose slug shadows an existing nested page must fail with the
// PREFIX_COLLISION conflict, regardless of whether the existing
// nested page lives behind a tree row in dir_index.
func TestApplyChangeSet_PrefixCollisionDetectsNestedPage(t *testing.T) {
	cat, repoID, _ := applyTestEnv(t)
	ctx := context.Background()

	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "guides/intro", Body: []byte("g")}},
	}); err != nil {
		t.Fatalf("create nested: %v", err)
	}

	_, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "guides", Body: []byte("would shadow")}},
	})
	var cerr *ConflictError
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *ConflictError, got %v", err)
	}
	if cerr.Code != ConflictCodePrefix {
		t.Fatalf("conflict code = %q, want PREFIX_COLLISION", cerr.Code)
	}
}

// TestApplyChangeSet_BlobRefcountDedupAcrossSlugs: two distinct slugs
// holding the same body share a single wiki_blob_refs row with
// refcount=2, exercising the upsert path.
func TestApplyChangeSet_BlobRefcountDedupAcrossSlugs(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	body := []byte("identical")
	for _, slug := range []string{"a", "b"} {
		if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
			RepositoryID: repoID, Source: SourceREST,
			Changes: []Change{{Op: OpUpsert, Slug: slug, Body: body}},
		}); err != nil {
			t.Fatalf("upsert %q: %v", slug, err)
		}
	}
	sha := HashContent(body)
	var ref db.WikiBlobRef
	if err := gdb.First(&ref, "blob_sha = ?", sha).Error; err != nil {
		t.Fatalf("read ref: %v", err)
	}
	if ref.Refcount != 2 {
		t.Fatalf("refcount = %d after two slugs share blob, want 2", ref.Refcount)
	}
}

func TestApplyChangeSet_BatchUpsert(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	res, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceBatch,
		Message: "import 3 pages",
		Changes: []Change{
			{Op: OpUpsert, Slug: "a", Body: []byte("a")},
			{Op: OpUpsert, Slug: "b", Body: []byte("b")},
			{Op: OpUpsert, Slug: "c", Body: []byte("c")},
		},
	})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(res.Changes) != 3 {
		t.Fatalf("expected 3 changes, got %d", len(res.Changes))
	}

	var count int64
	gdb.Model(&db.WikiPage{}).Where("repository_id = ?", repoID).Count(&count)
	if count != 3 {
		t.Fatalf("expected 3 pages, got %d", count)
	}
}

// TestApplyChangeSet_MultiRepoIsolation: two repos with overlapping
// slug names must not see each other's catalog state. Catches
// accidental cross-repo aliasing in dir_index, links, refcounts.
func TestApplyChangeSet_MultiRepoIsolation(t *testing.T) {
	cat, repoA, gdb := applyTestEnv(t)
	ctx := context.Background()

	// Seed a second repo under the same user.
	repo2 := db.Repository{OwnerID: 1, Name: "wiki2", FullName: "alice/wiki2", DefaultBranch: "main"}
	if err := gdb.Create(&repo2).Error; err != nil {
		t.Fatalf("seed repo2: %v", err)
	}
	repoB := repo2.ID

	bodyA := []byte("body A")
	bodyB := []byte("body B")
	for repo, body := range map[uint][]byte{repoA: bodyA, repoB: bodyB} {
		if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
			RepositoryID: repo, Source: SourceREST,
			Changes: []Change{{Op: OpUpsert, Slug: "shared", Body: body}},
		}); err != nil {
			t.Fatalf("upsert in repo %d: %v", repo, err)
		}
	}

	// Each repo has exactly one live page named "shared".
	for _, r := range []uint{repoA, repoB} {
		var count int64
		gdb.Model(&db.WikiPage{}).
			Where("repository_id = ? AND deleted_at IS NULL", r).
			Count(&count)
		if count != 1 {
			t.Fatalf("repo %d page count = %d, want 1", r, count)
		}
	}

	// Delete in A must not touch B.
	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoA, Source: SourceREST,
		Changes: []Change{{Op: OpDelete, Slug: "shared"}},
	}); err != nil {
		t.Fatalf("delete in repo A: %v", err)
	}
	var liveA, liveB int64
	gdb.Model(&db.WikiPage{}).
		Where("repository_id = ? AND deleted_at IS NULL", repoA).Count(&liveA)
	gdb.Model(&db.WikiPage{}).
		Where("repository_id = ? AND deleted_at IS NULL", repoB).Count(&liveB)
	if liveA != 0 || liveB != 1 {
		t.Fatalf("isolation broken: liveA=%d liveB=%d (want 0,1)", liveA, liveB)
	}

	// Repo A's wiki_repo_heads advanced; B's didn't.
	var headA, headB db.WikiRepoHead
	gdb.First(&headA, "repository_id = ?", repoA)
	gdb.First(&headB, "repository_id = ?", repoB)
	if headA.HeadChangesetID == headB.HeadChangesetID {
		t.Fatalf("repo heads should be independent: A=%d B=%d", headA.HeadChangesetID, headB.HeadChangesetID)
	}
}

// TestApplyChangeSet_OCCRetryExhausted exercises the bounded-retry
// arm of the optimistic concurrency loop: a writer that loses CAS on
// every attempt eventually returns ErrCASLost after MaxCASRetries.
//
// We simulate the perpetual racer by inserting a Catalog test hook
// (forceCASLoss) that flips casLost=true unconditionally on every
// applyOnce attempt. This isolates the retry-budget logic from
// dialect-specific concurrency semantics — SQLite's WAL would
// serialize an external racer behind the catalog's transaction, so
// the only reliable way to test the loop is to drive the lost-CAS
// signal directly. The retry-and-succeed path is symmetric: any
// attempt where forceCASLoss is off behaves like a normal write,
// already covered by every other ApplyChangeSet test in this file.
func TestApplyChangeSet_OCCRetryExhausted(t *testing.T) {
	cat, repoID, _ := applyTestEnv(t)
	ctx := context.Background()

	cat.MaxCASRetries = 3
	var attempts int
	cat.testForceCASLoss = func() bool {
		attempts++
		return true
	}

	_, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "home", Body: []byte("body")}},
	})
	if !errors.Is(err, ErrCASLost) {
		t.Fatalf("expected ErrCASLost after exhausting retries, got %v", err)
	}
	if attempts != cat.MaxCASRetries {
		t.Fatalf("retry budget consumed %d times, want %d", attempts, cat.MaxCASRetries)
	}
}

// TestApplyChangeSet_RenameMissingSourceReturnsErrPageNotFound
// pins the OpRename source-missing branch in apply.go's
// checkConflicts. Without this test, the rename-with-no-source path
// is untested and could silently change behaviour.
func TestApplyChangeSet_RenameMissingSourceReturnsErrPageNotFound(t *testing.T) {
	cat, repoID, _ := applyTestEnv(t)
	_, err := cat.ApplyChangeSet(context.Background(), ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpRename, Slug: "ghost", NewSlug: "after"}},
	})
	if !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("expected ErrPageNotFound, got %v", err)
	}
}

// TestApplyChangeSet_RenameIntoTombstonedDestination pins the
// destination-tombstoned branch of checkConflicts. Renames into a
// previously-deleted slug must surface the destination-taken
// conflict so an operator can hard-purge before retrying.
func TestApplyChangeSet_RenameIntoTombstonedDestination(t *testing.T) {
	cat, repoID, _ := applyTestEnv(t)
	ctx := context.Background()

	for _, body := range []struct{ slug, body string }{
		{"keep-me", "body"},
		{"will-tomb", "doomed"},
	} {
		if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
			RepositoryID: repoID, Source: SourceREST,
			Changes: []Change{{Op: OpUpsert, Slug: body.slug, Body: []byte(body.body)}},
		}); err != nil {
			t.Fatalf("seed %s: %v", body.slug, err)
		}
	}
	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpDelete, Slug: "will-tomb"}},
	}); err != nil {
		t.Fatalf("delete will-tomb: %v", err)
	}

	_, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpRename, Slug: "keep-me", NewSlug: "will-tomb"}},
	})
	var cerr *ConflictError
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *ConflictError, got %v", err)
	}
	if cerr.Code != ConflictCodeDestinationTake {
		t.Fatalf("conflict code = %q, want DESTINATION_EXISTS", cerr.Code)
	}
}

// TestApplyChangeSet_BodyAtInlineBoundary pins the inline-vs-CAS
// boundary at MaxBodyInlineBytes. A body of exactly the limit must
// stay inline; one byte over must materialize in the CAS. These
// two cases bracket a class of off-by-one bugs that would otherwise
// hide behind the typical-size test.
func TestApplyChangeSet_BodyAtInlineBoundary(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	atLimit := make([]byte, MaxBodyInlineBytes)
	for i := range atLimit {
		atLimit[i] = byte('x')
	}
	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "edge", Body: atLimit}},
	}); err != nil {
		t.Fatalf("upsert at limit: %v", err)
	}
	sha := HashContent(atLimit)
	if ok, _ := cat.Blob.Has(ctx, sha); ok {
		t.Fatalf("body == MaxBodyInlineBytes must not materialize a CAS file")
	}
	var page db.WikiPage
	if err := gdb.First(&page, "slug = ?", "edge").Error; err != nil {
		t.Fatalf("read page: %v", err)
	}
	if len(page.BodyInline) != MaxBodyInlineBytes {
		t.Fatalf("body_inline at boundary len=%d, want %d", len(page.BodyInline), MaxBodyInlineBytes)
	}
}

func TestApplyChangeSet_BodyJustOverInlineBoundary(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	over := make([]byte, MaxBodyInlineBytes+1)
	for i := range over {
		over[i] = byte('y')
	}
	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "over", Body: over}},
	}); err != nil {
		t.Fatalf("upsert over limit: %v", err)
	}
	sha := HashContent(over)
	if ok, _ := cat.Blob.Has(ctx, sha); !ok {
		t.Fatalf("body > MaxBodyInlineBytes must materialize a CAS file")
	}
	var page db.WikiPage
	if err := gdb.First(&page, "slug = ?", "over").Error; err != nil {
		t.Fatalf("read page: %v", err)
	}
	if page.BodyInline != nil {
		t.Fatalf("body > inline boundary must NOT be inlined; got %d bytes", len(page.BodyInline))
	}
}

// TestApplyChangeSet_EmptyBodyAllowed pins the contract that an
// empty body (zero bytes) is a valid page contents — distinct from
// "no body provided" which is rejected by planChangeSet.
func TestApplyChangeSet_EmptyBodyAllowed(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()
	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceREST,
		Changes: []Change{{Op: OpUpsert, Slug: "blank", Body: []byte{}}},
	}); err != nil {
		t.Fatalf("upsert empty body: %v", err)
	}
	var page db.WikiPage
	if err := gdb.First(&page, "slug = ?", "blank").Error; err != nil {
		t.Fatalf("read page: %v", err)
	}
	if page.BodySize != 0 {
		t.Fatalf("body_size = %d, want 0", page.BodySize)
	}
	if page.HeadBlobSHA != HashContent([]byte{}) {
		t.Fatalf("blob sha = %q, want git-empty-blob SHA", page.HeadBlobSHA)
	}
}

// TestApplyChangeSet_MixedOpsInOneChangeset confirms upsert + delete
// + rename can coexist in a single transaction. Migration replay
// produces these; the test pins that the changeset commits atomically.
func TestApplyChangeSet_MixedOpsInOneChangeset(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()
	for _, slug := range []string{"a", "b", "c"} {
		if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
			RepositoryID: repoID, Source: SourceREST,
			Changes: []Change{{Op: OpUpsert, Slug: slug, Body: []byte(slug)}},
		}); err != nil {
			t.Fatalf("seed %s: %v", slug, err)
		}
	}
	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID, Source: SourceBatch,
		Message: "mixed",
		Changes: []Change{
			{Op: OpUpsert, Slug: "d", Body: []byte("d")},
			{Op: OpDelete, Slug: "b"},
			{Op: OpRename, Slug: "c", NewSlug: "renamed-c"},
		},
	}); err != nil {
		t.Fatalf("mixed changeset: %v", err)
	}
	// a unchanged, d created, b tombstoned, c → renamed-c.
	var aliveCount int64
	gdb.Model(&db.WikiPage{}).
		Where("repository_id = ? AND deleted_at IS NULL", repoID).
		Count(&aliveCount)
	if aliveCount != 3 {
		t.Fatalf("expected 3 live pages (a, d, renamed-c), got %d", aliveCount)
	}
	for _, want := range []string{"a", "d", "renamed-c"} {
		var n int64
		gdb.Model(&db.WikiPage{}).
			Where("repository_id = ? AND slug = ? AND deleted_at IS NULL", repoID, want).
			Count(&n)
		if n != 1 {
			t.Fatalf("missing live page %q", want)
		}
	}
}
