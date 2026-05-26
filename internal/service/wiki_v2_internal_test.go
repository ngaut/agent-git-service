package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/embedding"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

func TestReplaceWikiV2SnapshotSkipsStaleEmptyCandidate(t *testing.T) {
	svc, cleanup := newWikiV2InternalTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForInternalTest(t, svc, "staleempty", "wiki-v2-empty")
	full := "staleempty/wiki-v2-empty"

	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n", "seed home", ""); err != nil {
		t.Fatalf("PutWikiPage(home): %v", err)
	}
	first, err := svc.ReconcileWikiV2(ctx, full)
	if err != nil {
		t.Fatalf("ReconcileWikiV2: %v", err)
	}

	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	result, err := svc.replaceWikiV2Snapshot(ctx, wikiRepoFullName(full), repo.ID, "", nil, nil, nil, time.Now().UTC())
	if err != nil {
		t.Fatalf("replaceWikiV2Snapshot empty candidate: %v", err)
	}
	if result.Applied {
		t.Fatalf("Applied = true, want false for stale empty candidate: %+v", result)
	}
	if result.CurrentHeadSHA != first.IndexedCommitSHA {
		t.Fatalf("CurrentHeadSHA = %q, want %q", result.CurrentHeadSHA, first.IndexedCommitSHA)
	}
	if result.CurrentPageCount != 1 {
		t.Fatalf("CurrentPageCount = %d, want 1", result.CurrentPageCount)
	}

	assertWikiV2StateUnchanged(t, svc, repo.ID, first.IndexedCommitSHA, 1)
}

func TestReplaceWikiV2SnapshotSkipsStaleNonEmptyCandidate(t *testing.T) {
	svc, cleanup := newWikiV2InternalTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForInternalTest(t, svc, "stalehead", "wiki-v2-head")
	full := "stalehead/wiki-v2-head"

	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n", "seed home", ""); err != nil {
		t.Fatalf("PutWikiPage(home): %v", err)
	}
	first, err := svc.ReconcileWikiV2(ctx, full)
	if err != nil {
		t.Fatalf("ReconcileWikiV2 first: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "guides/setup", "# Setup\n", "seed setup", ""); err != nil {
		t.Fatalf("PutWikiPage(guides/setup): %v", err)
	}

	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	staleRows := []db.WikiPageIndex{{
		RepositoryID:  repo.ID,
		Slug:          "home",
		HeadBlobSHA:   "1111111111111111111111111111111111111111",
		HeadCommitSHA: first.IndexedCommitSHA,
		Title:         "Home",
		Size:          len("# Home\n"),
		UpdatedAt:     time.Now().UTC(),
	}}
	result, err := svc.replaceWikiV2Snapshot(ctx, wikiRepoFullName(full), repo.ID, first.IndexedCommitSHA, staleRows, nil, nil, time.Now().UTC())
	if err != nil {
		t.Fatalf("replaceWikiV2Snapshot stale head: %v", err)
	}
	if result.Applied {
		t.Fatalf("Applied = true, want false for stale head candidate: %+v", result)
	}
	if result.CurrentHeadSHA == first.IndexedCommitSHA {
		t.Fatalf("CurrentHeadSHA did not advance: %+v", result)
	}
	if result.CurrentPageCount != 1 {
		t.Fatalf("CurrentPageCount = %d, want existing row count 1", result.CurrentPageCount)
	}

	assertWikiV2StateUnchanged(t, svc, repo.ID, first.IndexedCommitSHA, 1)
}

func assertWikiV2StateUnchanged(t *testing.T, svc *Service, repoID uint, expectedSHA string, expectedRows int64) {
	t.Helper()

	var state db.WikiIndexState
	if err := svc.DB.First(&state, "repository_id = ?", repoID).Error; err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.IndexedCommitSHA != expectedSHA {
		t.Fatalf("IndexedCommitSHA = %q, want %q", state.IndexedCommitSHA, expectedSHA)
	}

	var rowCount int64
	if err := svc.DB.Model(&db.WikiPageIndex{}).Where("repository_id = ?", repoID).Count(&rowCount).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rowCount != expectedRows {
		t.Fatalf("rowCount = %d, want %d", rowCount, expectedRows)
	}
}

func setupRepoForInternalTest(t *testing.T, svc *Service, login, repoName string) {
	t.Helper()
	if err := svc.DB.Create(&db.User{Login: login, Name: login, Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := svc.CreateRepo(context.Background(), CreateRepoInput{OwnerLogin: login, Name: repoName}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
}

func newWikiV2InternalTestService(t *testing.T) (*Service, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "wiki-v2-internal-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	dbPath := filepath.Join(tmpDir, "test.sqlite")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("sql.DB: %v", err)
	}
	if err := gdb.Exec("PRAGMA busy_timeout = 5000").Error; err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if err := gdb.Exec("PRAGMA journal_mode = WAL").Error; err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if err := db.Migrate(gdb); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	wikiBlob := wikicatalog.NewBlobStore(tmpDir)
	wikiCat := wikicatalog.New(gdb, wikiBlob)
	svc := &Service{
		DB:             gdb,
		Git:            store,
		WikiCatalog:    wikiCat,
		WikiBlob:       wikiBlob,
		BaseURL:        "http://localhost:8080",
		AttachmentRoot: tmpDir,
		Embedder:       embedding.NopEmbedder{},
	}
	wikiCat.DBFor = svc.DBForCtx
	wikiCat.OnChangeSetCommitted = svc.WikiCatalogPostCommit

	return svc, func() {
		_ = sqlDB.Close()
		_ = os.RemoveAll(tmpDir)
	}
}
