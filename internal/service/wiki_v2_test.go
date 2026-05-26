package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func TestReconcileWikiV2_IdempotentAndLegacyBehaviorUntouched(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "wikiuser", "wiki-v2")
	full := "wikiuser/wiki-v2"

	for _, tc := range []struct {
		slug string
		body string
	}{
		{slug: "home", body: "# Home\n\nWelcome.\n"},
		{slug: "guides/setup", body: "# Setup\n\nSee [[home]].\n"},
	} {
		if _, err := svc.PutWikiPage(ctx, full, tc.slug, tc.body, "seed "+tc.slug, ""); err != nil {
			t.Fatalf("PutWikiPage(%s): %v", tc.slug, err)
		}
	}

	kick, err := svc.KickWikiV2Reconcile(ctx, full)
	if err != nil {
		t.Fatalf("KickWikiV2Reconcile: %v", err)
	}
	if kick.RepositoryID == 0 || kick.RequestedAt.IsZero() {
		t.Fatalf("unexpected kick result: %+v", kick)
	}

	first, err := svc.ReconcileWikiV2(ctx, full)
	if err != nil {
		t.Fatalf("ReconcileWikiV2 first: %v", err)
	}
	if !first.Reconciled || first.PageCount != 2 || first.IndexedCommitSHA == "" {
		t.Fatalf("unexpected first reconcile result: %+v", first)
	}

	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	var rows []db.WikiPageIndex
	if err := svc.DB.Where("repository_id = ?", repo.ID).Order("slug asc").Find(&rows).Error; err != nil {
		t.Fatalf("query wiki_page_index: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("wiki_page_index rows = %d, want 2", len(rows))
	}
	if rows[0].Slug != "guides/setup" || rows[1].Slug != "home" {
		t.Fatalf("indexed slugs = [%s %s], want [guides/setup home]", rows[0].Slug, rows[1].Slug)
	}

	var state db.WikiIndexState
	if err := svc.DB.First(&state, "repository_id = ?", repo.ID).Error; err != nil {
		t.Fatalf("query wiki_index_state: %v", err)
	}
	if state.IndexedCommitSHA != first.IndexedCommitSHA || state.ReconcileRequestedAt != nil {
		t.Fatalf("unexpected state after first reconcile: %+v", state)
	}

	second, err := svc.ReconcileWikiV2(ctx, full)
	if err != nil {
		t.Fatalf("ReconcileWikiV2 second: %v", err)
	}
	if !second.Reconciled || second.IndexedCommitSHA != first.IndexedCommitSHA || second.PageCount != first.PageCount {
		t.Fatalf("unexpected second reconcile result: %+v", second)
	}

	var rowCount int64
	if err := svc.DB.Model(&db.WikiPageIndex{}).Where("repository_id = ?", repo.ID).Count(&rowCount).Error; err != nil {
		t.Fatalf("count wiki_page_index: %v", err)
	}
	if rowCount != 2 {
		t.Fatalf("wiki_page_index count after second reconcile = %d, want 2", rowCount)
	}

	page, err := svc.GetWikiPage(ctx, full, "home")
	if err != nil {
		t.Fatalf("GetWikiPage(home): %v", err)
	}
	if page.Slug != "home" || page.Title != "Home" {
		t.Fatalf("legacy GetWikiPage changed: %+v", page)
	}

	pages, err := svc.ListWikiPages(ctx, full, service.ListWikiPagesOptions{Recursive: true})
	if err != nil {
		t.Fatalf("ListWikiPages: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("legacy ListWikiPages count = %d, want 2", len(pages))
	}
}

func TestKickWikiV2ReconcilePreservesIndexedState(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "kickuser", "wiki-v2-kick")
	full := "kickuser/wiki-v2-kick"

	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n", "seed home", ""); err != nil {
		t.Fatalf("PutWikiPage(home): %v", err)
	}
	first, err := svc.ReconcileWikiV2(ctx, full)
	if err != nil {
		t.Fatalf("ReconcileWikiV2: %v", err)
	}

	kick, err := svc.KickWikiV2Reconcile(ctx, full)
	if err != nil {
		t.Fatalf("KickWikiV2Reconcile: %v", err)
	}
	if kick.IndexedCommitSHA != first.IndexedCommitSHA {
		t.Fatalf("kick indexed sha = %q, want %q", kick.IndexedCommitSHA, first.IndexedCommitSHA)
	}

	var state db.WikiIndexState
	if err := svc.DB.First(&state, "repository_id = ?", kick.RepositoryID).Error; err != nil {
		t.Fatalf("load wiki_index_state: %v", err)
	}
	if state.IndexedCommitSHA != first.IndexedCommitSHA {
		t.Fatalf("state indexed sha = %q, want %q", state.IndexedCommitSHA, first.IndexedCommitSHA)
	}
	if state.ReconcileRequestedAt == nil {
		t.Fatal("ReconcileRequestedAt = nil, want timestamp")
	}
}

func TestReconcileWikiV2UsesPerPageCommitMetadata(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "metauser", "wiki-v2-meta")
	full := "metauser/wiki-v2-meta"

	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n", "seed home", ""); err != nil {
		t.Fatalf("PutWikiPage(home): %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := svc.PutWikiPage(ctx, full, "guides/setup", "# Setup\n", "seed setup", ""); err != nil {
		t.Fatalf("PutWikiPage(guides/setup): %v", err)
	}

	if _, err := svc.ReconcileWikiV2(ctx, full); err != nil {
		t.Fatalf("ReconcileWikiV2: %v", err)
	}

	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	var rows []db.WikiPageIndex
	if err := svc.DB.Where("repository_id = ?", repo.ID).Order("slug asc").Find(&rows).Error; err != nil {
		t.Fatalf("query wiki_page_index: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].Slug != "guides/setup" || rows[1].Slug != "home" {
		t.Fatalf("slugs = [%s, %s], want [guides/setup home]", rows[0].Slug, rows[1].Slug)
	}
	if !rows[0].UpdatedAt.After(rows[1].UpdatedAt) {
		t.Fatalf("updated_at order = [%s, %s], want later commit on guides/setup", rows[0].UpdatedAt, rows[1].UpdatedAt)
	}
}

func TestWikiV2HeadReadsUseDerivedIndexWhenCurrent(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "v2reader", "wiki-v2-derived")
	full := "v2reader/wiki-v2-derived"

	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nFrom git.\n", "seed home", ""); err != nil {
		t.Fatalf("PutWikiPage(home): %v", err)
	}
	if _, err := svc.ReconcileWikiV2(ctx, full); err != nil {
		t.Fatalf("ReconcileWikiV2: %v", err)
	}

	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if err := svc.DB.Where("repository_id = ?", repo.ID).Delete(&db.WikiPage{}).Error; err != nil {
		t.Fatalf("delete legacy wiki pages: %v", err)
	}

	page, err := svc.GetWikiPage(ctx, full, "home")
	if err != nil {
		t.Fatalf("GetWikiPage(home): %v", err)
	}
	if page.Slug != "home" || page.Body != "# Home\n\nFrom git.\n" {
		t.Fatalf("unexpected v2 page: %+v", page)
	}

	pages, err := svc.ListWikiPages(ctx, full, service.ListWikiPagesOptions{Recursive: true})
	if err != nil {
		t.Fatalf("ListWikiPages: %v", err)
	}
	if len(pages) != 1 || pages[0].Slug != "home" {
		t.Fatalf("unexpected v2 list result: %+v", pages)
	}
}

func TestWikiV2HeadReadsFallBackWhenIndexStateIsStale(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "v2stale", "wiki-v2-stale")
	full := "v2stale/wiki-v2-stale"

	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n", "seed home", ""); err != nil {
		t.Fatalf("PutWikiPage(home): %v", err)
	}
	if _, err := svc.ReconcileWikiV2(ctx, full); err != nil {
		t.Fatalf("ReconcileWikiV2: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "guides/setup", "# Setup\n", "seed setup", ""); err != nil {
		t.Fatalf("PutWikiPage(guides/setup): %v", err)
	}

	pages, err := svc.ListWikiPages(ctx, full, service.ListWikiPagesOptions{Recursive: true})
	if err != nil {
		t.Fatalf("ListWikiPages: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("ListWikiPages count = %d, want 2 after fallback", len(pages))
	}

	page, err := svc.GetWikiPage(ctx, full, "guides/setup")
	if err != nil {
		t.Fatalf("GetWikiPage(guides/setup): %v", err)
	}
	if page.Slug != "guides/setup" {
		t.Fatalf("unexpected fallback page: %+v", page)
	}
}
