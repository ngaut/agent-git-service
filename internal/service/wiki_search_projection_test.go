package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/embedding"
	"github.com/ngaut/agent-git-service/internal/testharness/testdb"
	"gorm.io/gorm"
)

func TestWikiSearchLexicalProjectionDoesNotRequireEmbeddingColumn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		embedder embedding.Embedder
	}{
		{name: "embedding disabled", embedder: embedding.NopEmbedder{}},
		{name: "vector initialization pending", embedder: &FakeEmbedder{Vec: []float32{1, 0, 0}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gdb, cleanup := testdb.OpenRaw(t, "wiki_proj_no_emb")
			defer cleanup()
			if err := gdb.AutoMigrate(&db.User{}, &db.Repository{}, &db.WikiSearchDocument{}); err != nil {
				t.Fatalf("auto-migrate: %v", err)
			}
			if gdb.Migrator().HasColumn("wiki_search_documents", "embedding") {
				t.Fatal("expected explicit wiki search migration to own the embedding column")
			}

			repoID := createWikiSearchProjectionTestRepo(t, gdb, "without-embedding")
			svc := &Service{DB: gdb, Embedder: tc.embedder}
			page := WikiPage{
				Slug: "docs/home",
				Body: "# Home\n\nLexical search remains available.",
				SHA:  strings.Repeat("a", 40),
			}
			if err := svc.upsertWikiSearchLexicalDocument(context.Background(), repoID, page); err != nil {
				t.Fatalf("upsert lexical document: %v", err)
			}

			var stored db.WikiSearchDocument
			if err := gdb.Omit("embedding").
				Where("repository_id = ? AND slug = ?", repoID, page.Slug).
				First(&stored).Error; err != nil {
				t.Fatalf("load lexical document: %v", err)
			}
			if string(stored.Body) != page.Body {
				t.Fatalf("stored body = %q, want %q", stored.Body, page.Body)
			}
		})
	}
}

func TestWikiSearchProjectionTaskCoalescesBySlugAndKind(t *testing.T) {
	gdb, cleanup := openMigratedServiceTestDB(t)
	defer cleanup()
	repoID := createWikiSearchProjectionTestRepo(t, gdb, "coalesce")

	if err := upsertWikiSearchProjectionTask(gdb, repoID, "docs/home", wikiSearchProjectionKindLexical, "", ""); err != nil {
		t.Fatalf("enqueue first task: %v", err)
	}
	if err := upsertWikiSearchProjectionTask(gdb, repoID, "docs/home", wikiSearchProjectionKindLexical, "", ""); err != nil {
		t.Fatalf("enqueue second task: %v", err)
	}

	var tasks []db.WikiSearchProjectionTask
	if err := gdb.Find(&tasks).Error; err != nil {
		t.Fatalf("load tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("task count = %d, want 1", len(tasks))
	}
	if tasks[0].Generation != 2 {
		t.Fatalf("generation = %d, want 2", tasks[0].Generation)
	}
}

func TestWikiSearchProjectionGenerationPreservesNewerTask(t *testing.T) {
	gdb, cleanup := openMigratedServiceTestDB(t)
	defer cleanup()
	svc := &Service{DB: gdb, Embedder: embedding.NopEmbedder{}}
	ctx := context.Background()
	repoID := createWikiSearchProjectionTestRepo(t, gdb, "generation")

	if err := upsertWikiSearchProjectionTask(gdb, repoID, "docs/home", wikiSearchProjectionKindLexical, "", ""); err != nil {
		t.Fatalf("enqueue task: %v", err)
	}
	claimed, err := svc.claimWikiSearchProjectionTask(ctx, wikiSearchProjectionKindLexical)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected claimed task")
	}
	if err := upsertWikiSearchProjectionTask(gdb, repoID, "docs/home", wikiSearchProjectionKindLexical, "", ""); err != nil {
		t.Fatalf("enqueue newer generation: %v", err)
	}
	if err := completeWikiSearchProjectionTask(gdb, *claimed); err != nil {
		t.Fatalf("complete old generation: %v", err)
	}

	var remaining db.WikiSearchProjectionTask
	if err := gdb.First(&remaining).Error; err != nil {
		t.Fatalf("load newer task: %v", err)
	}
	if remaining.Generation != 2 {
		t.Fatalf("remaining generation = %d, want 2", remaining.Generation)
	}
	if remaining.LeaseToken != "" || remaining.LeaseExpiresAt != nil {
		t.Fatalf("newer task remained leased: token=%q expires=%v", remaining.LeaseToken, remaining.LeaseExpiresAt)
	}
}

func TestWikiSearchProjectionRepairResumesMissingDocument(t *testing.T) {
	gdb, cleanup := openMigratedServiceTestDB(t)
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "projection-owner", Name: "Projection Owner", Type: db.TypeUser}
	if err := gdb.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	repo := db.Repository{
		OwnerID:       owner.ID,
		Name:          "projection-repo",
		FullName:      "projection-owner/projection-repo",
		DefaultBranch: "main",
	}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("create repo: %v", err)
	}
	body := []byte("# Home\n\nRecovered lexical body.\n")
	page := db.WikiPage{
		RepositoryID:    repo.ID,
		Slug:            "docs/home",
		Title:           "Home",
		HeadBlobSHA:     strings.Repeat("a", 40),
		BodySize:        len(body),
		BodyInline:      body,
		HeadRevisionID:  1,
		HeadChangesetID: 1,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}
	if err := gdb.Create(&page).Error; err != nil {
		t.Fatalf("create wiki page: %v", err)
	}

	// Simulate a new process starting after the catalog commit but before the
	// old process could persist or drain its in-memory search job.
	svc := &Service{DB: gdb, Embedder: embedding.NopEmbedder{}}
	repaired, err := svc.repairWikiSearchProjectionTasks(ctx)
	if err != nil {
		t.Fatalf("repair tasks: %v", err)
	}
	if repaired < 1 {
		t.Fatalf("repaired rows = %d, want at least 1", repaired)
	}
	svc.startWikiSearchProjectionDrain(ctx, wikiSearchProjectionKindLexical)
	svc.Wg.Wait()

	var stored db.WikiSearchDocument
	if err := gdb.Where("repository_id = ? AND slug = ?", repo.ID, page.Slug).First(&stored).Error; err != nil {
		t.Fatalf("load repaired search document: %v", err)
	}
	if string(stored.Body) != string(body) {
		t.Fatalf("repaired body = %q, want %q", stored.Body, body)
	}
	var pending int64
	if err := gdb.Model(&db.WikiSearchProjectionTask{}).Count(&pending).Error; err != nil {
		t.Fatalf("count pending tasks: %v", err)
	}
	if pending != 0 {
		t.Fatalf("pending tasks = %d, want 0", pending)
	}
}

func TestWikiSearchProjectionTaskSharesLabelTransaction(t *testing.T) {
	gdb, cleanup := openMigratedServiceTestDB(t)
	defer cleanup()
	repoID := createWikiSearchProjectionTestRepo(t, gdb, "label-transaction")
	label := db.Label{RepositoryID: repoID, Name: "runbook", Color: "123456"}
	if err := gdb.Create(&label).Error; err != nil {
		t.Fatalf("create label: %v", err)
	}

	rollbackErr := errors.New("roll back label mutation")
	err := gdb.Transaction(func(tx *gorm.DB) error {
		link := db.WikiPageLabel{RepositoryID: repoID, Slug: "docs/home", LabelID: label.ID}
		if err := tx.Create(&link).Error; err != nil {
			return err
		}
		if err := persistWikiSearchProjectionTasks(tx, repoID, []string{"docs/home"}); err != nil {
			return err
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("transaction error = %v, want %v", err, rollbackErr)
	}

	var links int64
	if err := gdb.Model(&db.WikiPageLabel{}).Where("repository_id = ?", repoID).Count(&links).Error; err != nil {
		t.Fatalf("count wiki label links: %v", err)
	}
	var tasks int64
	if err := gdb.Model(&db.WikiSearchProjectionTask{}).Where("repository_id = ?", repoID).Count(&tasks).Error; err != nil {
		t.Fatalf("count projection tasks: %v", err)
	}
	if links != 0 || tasks != 0 {
		t.Fatalf("rolled-back rows: label links=%d projection tasks=%d, want both 0", links, tasks)
	}
}

func TestWikiSearchProjectionRejectsStaleEmbeddingAfterLabelChange(t *testing.T) {
	gdb, cleanup := openMigratedServiceTestDB(t)
	defer cleanup()
	repoID := createWikiSearchProjectionTestRepo(t, gdb, "stale-embedding")
	const (
		slug         = "docs/home"
		revisionSHA  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		oldLabel     = "label:old"
		currentLabel = "label:current"
	)
	doc := db.WikiSearchDocument{
		RepositoryID: repoID,
		Slug:         slug,
		Title:        "Home",
		Body:         db.LargeText("# Home"),
		RevisionSHA:  revisionSHA,
		LabelDigest:  oldLabel,
	}
	if err := gdb.Omit("Embedding").Create(&doc).Error; err != nil {
		t.Fatalf("create search document: %v", err)
	}
	if err := upsertWikiSearchProjectionTask(
		gdb,
		repoID,
		slug,
		wikiSearchProjectionKindEmbedding,
		revisionSHA,
		oldLabel,
	); err != nil {
		t.Fatalf("enqueue embedding task: %v", err)
	}

	embedder := newBlockingProjectionEmbedder()
	svc := &Service{DB: gdb, Embedder: embedder}
	task, err := svc.claimWikiSearchProjectionTask(context.Background(), wikiSearchProjectionKindEmbedding)
	if err != nil {
		t.Fatalf("claim embedding task: %v", err)
	}
	if task == nil {
		t.Fatal("expected claimed embedding task")
	}
	done := make(chan error, 1)
	go func() {
		done <- svc.processWikiSearchEmbeddingTask(context.Background(), *task)
	}()
	select {
	case <-embedder.started:
	case <-time.After(5 * time.Second):
		t.Fatal("embedding did not start")
	}

	if err := gdb.Model(&db.WikiSearchDocument{}).
		Where("repository_id = ? AND slug = ?", repoID, slug).
		UpdateColumn("label_digest", currentLabel).Error; err != nil {
		t.Fatalf("update label digest: %v", err)
	}
	embedder.release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("process stale embedding task: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("embedding task did not finish")
	}

	var embeddingIsNull bool
	if err := gdb.Raw(
		"SELECT embedding IS NULL FROM wiki_search_documents WHERE repository_id = ? AND slug = ?",
		repoID,
		slug,
	).Scan(&embeddingIsNull).Error; err != nil {
		t.Fatalf("inspect embedding: %v", err)
	}
	if !embeddingIsNull {
		t.Fatal("stale embedding was written after label digest changed")
	}
}

func TestWikiSearchProjectionCompletesEmbeddingTaskWithoutEmbedder(t *testing.T) {
	gdb, cleanup := openMigratedServiceTestDB(t)
	defer cleanup()
	repoID := createWikiSearchProjectionTestRepo(t, gdb, "embedding-disabled")
	const (
		slug        = "docs/home"
		revisionSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		labelDigest = "label:docs"
	)
	doc := db.WikiSearchDocument{
		RepositoryID: repoID,
		Slug:         slug,
		Title:        "Home",
		Body:         db.LargeText("# Home"),
		RevisionSHA:  revisionSHA,
		LabelDigest:  labelDigest,
	}
	if err := gdb.Omit("Embedding").Create(&doc).Error; err != nil {
		t.Fatalf("create search document: %v", err)
	}
	if err := upsertWikiSearchProjectionTask(
		gdb,
		repoID,
		slug,
		wikiSearchProjectionKindEmbedding,
		revisionSHA,
		labelDigest,
	); err != nil {
		t.Fatalf("enqueue embedding task: %v", err)
	}

	svc := &Service{DB: gdb}
	task, err := svc.claimWikiSearchProjectionTask(context.Background(), wikiSearchProjectionKindEmbedding)
	if err != nil {
		t.Fatalf("claim embedding task: %v", err)
	}
	if task == nil {
		t.Fatal("expected claimed embedding task")
	}
	if err := svc.processWikiSearchEmbeddingTask(context.Background(), *task); err != nil {
		t.Fatalf("process embedding task without embedder: %v", err)
	}

	var pending int64
	if err := gdb.Model(&db.WikiSearchProjectionTask{}).
		Where("repository_id = ? AND slug = ? AND kind = ?", repoID, slug, wikiSearchProjectionKindEmbedding).
		Count(&pending).Error; err != nil {
		t.Fatalf("count pending embedding tasks: %v", err)
	}
	if pending != 0 {
		t.Fatalf("pending embedding tasks = %d, want 0", pending)
	}

	var embeddingIsNull bool
	if err := gdb.Raw(
		"SELECT embedding IS NULL FROM wiki_search_documents WHERE repository_id = ? AND slug = ?",
		repoID,
		slug,
	).Scan(&embeddingIsNull).Error; err != nil {
		t.Fatalf("inspect embedding: %v", err)
	}
	if !embeddingIsNull {
		t.Fatal("embedding should remain NULL when embedder is disabled")
	}
}

type blockingProjectionEmbedder struct {
	started     chan struct{}
	releaseCh   chan struct{}
	releaseOnce sync.Once
}

func newBlockingProjectionEmbedder() *blockingProjectionEmbedder {
	return &blockingProjectionEmbedder{
		started:   make(chan struct{}),
		releaseCh: make(chan struct{}),
	}
}

func (e *blockingProjectionEmbedder) Embed(ctx context.Context, _ string) ([]float32, error) {
	close(e.started)
	select {
	case <-e.releaseCh:
		return []float32{1, 0, 0}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *blockingProjectionEmbedder) Dimensions() int {
	return 3
}

func (e *blockingProjectionEmbedder) release() {
	e.releaseOnce.Do(func() {
		close(e.releaseCh)
	})
}

func createWikiSearchProjectionTestRepo(t *testing.T, database *gorm.DB, suffix string) uint {
	t.Helper()
	owner := db.User{
		Login: "projection-" + suffix,
		Name:  "Projection " + suffix,
		Type:  db.TypeUser,
	}
	if err := database.Create(&owner).Error; err != nil {
		t.Fatalf("create projection owner: %v", err)
	}
	repo := db.Repository{
		OwnerID:       owner.ID,
		Name:          "projection-" + suffix,
		FullName:      owner.Login + "/projection-" + suffix,
		DefaultBranch: "main",
	}
	if err := database.Create(&repo).Error; err != nil {
		t.Fatalf("create projection repo: %v", err)
	}
	return repo.ID
}
