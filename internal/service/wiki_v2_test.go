package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
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
	if state.BacklinksIndexedSHA != first.IndexedCommitSHA {
		t.Fatalf("unexpected backlinks indexed sha after first reconcile: %+v", state)
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

func TestWikiV2TreeListsDirectoriesAndPagesFromGit(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "v2tree", "wiki-v2-tree")
	full := "v2tree/wiki-v2-tree"

	for _, tc := range []struct {
		slug string
		body string
	}{
		{slug: "home", body: "# Home\n"},
		{slug: "guides/setup", body: "# Setup\n"},
		{slug: "guides/advanced/install", body: "# Install\n"},
	} {
		if _, err := svc.PutWikiPage(ctx, full, tc.slug, tc.body, "seed "+tc.slug, ""); err != nil {
			t.Fatalf("PutWikiPage(%s): %v", tc.slug, err)
		}
	}

	root, err := svc.ListWikiTreeAtRef(ctx, full, "", "")
	if err != nil {
		t.Fatalf("ListWikiTreeAtRef(root): %v", err)
	}
	if len(root) != 2 {
		t.Fatalf("root entries = %d, want 2: %+v", len(root), root)
	}
	if root[0].Kind != "directory" || root[0].Path != "guides" {
		t.Fatalf("root[0] = %+v, want guides directory", root[0])
	}
	if root[1].Kind != "page" || root[1].Slug != "home" {
		t.Fatalf("root[1] = %+v, want home page", root[1])
	}

	guides, err := svc.ListWikiTreeAtRef(ctx, full, "guides", "")
	if err != nil {
		t.Fatalf("ListWikiTreeAtRef(guides): %v", err)
	}
	if len(guides) != 2 {
		t.Fatalf("guides entries = %d, want 2: %+v", len(guides), guides)
	}
	if guides[0].Kind != "directory" || guides[0].Path != "guides/advanced" {
		t.Fatalf("guides[0] = %+v, want advanced directory", guides[0])
	}
	if guides[1].Kind != "page" || guides[1].Slug != "guides/setup" {
		t.Fatalf("guides[1] = %+v, want guides/setup page", guides[1])
	}
}

func TestWikiV2ReconcileBuildsHistoryAndBacklinkIndexes(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "v2index", "wiki-v2-index")
	full := "v2index/wiki-v2-index"

	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nSeed.\n", "seed home", ""); err != nil {
		t.Fatalf("PutWikiPage(home): %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nUpdated.\n", "update home", ""); err != nil {
		t.Fatalf("PutWikiPage(home update): %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "faq", "# FAQ\n\nSee [[home]].\n", "seed faq", ""); err != nil {
		t.Fatalf("PutWikiPage(faq): %v", err)
	}

	if _, err := svc.ReconcileWikiV2(ctx, full); err != nil {
		t.Fatalf("ReconcileWikiV2: %v", err)
	}

	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	var backlinks []db.WikiBacklink
	if err := svc.DB.Where("repository_id = ?", repo.ID).Order("src_slug asc").Find(&backlinks).Error; err != nil {
		t.Fatalf("query wiki_backlinks: %v", err)
	}
	if len(backlinks) != 1 || backlinks[0].SrcSlug != "faq" || backlinks[0].DstSlug != "home" {
		t.Fatalf("unexpected backlink rows: %+v", backlinks)
	}

	var historyRows []db.WikiPageHistory
	if err := svc.DB.Where("repository_id = ? AND slug = ?", repo.ID, "home").Order("committed_at desc").Find(&historyRows).Error; err != nil {
		t.Fatalf("query wiki_page_history: %v", err)
	}
	if len(historyRows) != 2 {
		t.Fatalf("wiki_page_history rows = %d, want 2", len(historyRows))
	}
	if historyRows[0].BodySize <= 0 || historyRows[1].BodySize <= 0 {
		t.Fatalf("expected positive body sizes, got %+v", historyRows)
	}

	if err := svc.DB.Where("repository_id = ?", repo.ID).Delete(&db.WikiPage{}).Error; err != nil {
		t.Fatalf("delete legacy wiki pages: %v", err)
	}

	history, total, err := svc.ListWikiPageHistoryPage(ctx, full, "home", 1, 10)
	if err != nil {
		t.Fatalf("ListWikiPageHistoryPage(home): %v", err)
	}
	if total != 2 || len(history) != 2 {
		t.Fatalf("unexpected v2 history page: total=%d rows=%d", total, len(history))
	}

	resolvedBacklinks, err := svc.ListWikiBacklinks(ctx, full, "home")
	if err != nil {
		t.Fatalf("ListWikiBacklinks(home): %v", err)
	}
	if len(resolvedBacklinks) != 1 || resolvedBacklinks[0].Slug != "faq" {
		t.Fatalf("unexpected v2 backlinks: %+v", resolvedBacklinks)
	}
}

func TestWikiV2HistoryPreservesDeleteBodySizeForDeleteRecreateFlows(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "v2recreate", "wiki-v2-recreate")
	full := "v2recreate/wiki-v2-recreate"

	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nFirst version.", "create home", ""); err != nil {
		t.Fatalf("PutWikiPage(create): %v", err)
	}
	if err := svc.DeleteWikiPage(ctx, full, "home", "delete home"); err != nil {
		t.Fatalf("DeleteWikiPage(home): %v", err)
	}
	recreatedBody := "# Home\n\nRecreated version."
	if _, err := svc.PutWikiPage(ctx, full, "home", recreatedBody, "recreate home", ""); err != nil {
		t.Fatalf("PutWikiPage(recreate): %v", err)
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

	history, total, err := svc.ListWikiPageHistoryPage(ctx, full, "home", 1, 10)
	if err != nil {
		t.Fatalf("ListWikiPageHistoryPage(home): %v", err)
	}
	if total != 3 || len(history) != 3 {
		t.Fatalf("unexpected v2 history page: total=%d rows=%d", total, len(history))
	}
	var deleteEntry *service.WikiPageHistoryEntry
	for i := range history {
		if history[i].Message == "delete home" {
			deleteEntry = &history[i]
			break
		}
	}
	if deleteEntry == nil {
		t.Fatalf("delete revision missing from v2 history: %#v", history)
	}
	if deleteEntry.BodySize != 0 {
		t.Fatalf("delete body_size = %d, want 0", deleteEntry.BodySize)
	}
}

func TestWikiV2HistoryPaginationBeyondRangeReturnsEmptyCurrentPage(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "v2page", "wiki-v2-page")
	full := "v2page/wiki-v2-page"

	revisions := []struct {
		body    string
		message string
	}{
		{body: "# Home\n\nRevision 1.\n", message: "revision 1"},
		{body: "# Home\n\nRevision 2.\n", message: "revision 2"},
	}
	for i, rev := range revisions {
		if _, err := svc.PutWikiPage(ctx, full, "home", rev.body, rev.message, ""); err != nil {
			t.Fatalf("PutWikiPage(%d): %v", i+1, err)
		}
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

	history, total, err := svc.ListWikiPageHistoryPage(ctx, full, "home", 2, 10)
	if err != nil {
		t.Fatalf("ListWikiPageHistoryPage(home, out of range): %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if len(history) != 0 {
		t.Fatalf("history length = %d, want 0 for out-of-range page", len(history))
	}
}

func TestWikiV2HistoryFallsBackToCatalogWhenDerivedHistoryDropsRenameRevisions(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "v2rename", "wiki-v2-rename")
	full := "v2rename/wiki-v2-rename"

	if _, err := svc.PutWikiPage(ctx, full, "old-name", "# Old Name\n\nFirst version.\n", "create old-name", ""); err != nil {
		t.Fatalf("PutWikiPage(old-name): %v", err)
	}
	page, err := svc.GetWikiPage(ctx, full, "old-name")
	if err != nil {
		t.Fatalf("GetWikiPage(old-name): %v", err)
	}
	if _, err := svc.MoveWikiPage(ctx, full, "old-name", "new-name", page.SHA, "rename to new-name"); err != nil {
		t.Fatalf("MoveWikiPage: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "new-name", "# New Name\n\nSecond version.\n", "update new-name", ""); err != nil {
		t.Fatalf("PutWikiPage(new-name): %v", err)
	}
	if _, err := svc.ReconcileWikiV2(ctx, full); err != nil {
		t.Fatalf("ReconcileWikiV2: %v", err)
	}

	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	var derivedTotal int64
	if err := svc.DB.Model(&db.WikiPageHistory{}).
		Where("repository_id = ? AND slug = ?", repo.ID, "new-name").
		Count(&derivedTotal).Error; err != nil {
		t.Fatalf("count derived history: %v", err)
	}
	if derivedTotal != 2 {
		t.Fatalf("derived history total = %d, want 2 path-local revisions", derivedTotal)
	}

	history, total, err := svc.ListWikiPageHistoryPage(ctx, full, "new-name", 1, 10)
	if err != nil {
		t.Fatalf("ListWikiPageHistoryPage(new-name): %v", err)
	}
	if total != 3 || len(history) != 3 {
		t.Fatalf("history total=%d len=%d, want 3/3 via catalog fallback", total, len(history))
	}
	if history[0].Message != "update new-name" || history[1].Message != "rename to new-name" || history[2].Message != "create old-name" {
		t.Fatalf("unexpected rename-preserving history: %+v", history)
	}
}

func TestWikiV2HistoryPreservesRevisionOrderWhenDerivedTimestampsTie(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "v2order", "wiki-v2-order")
	full := "v2order/wiki-v2-order"

	rep, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	author := db.User{Login: "wiki-bot", Name: "Wiki Bot", Email: "gh-server@localhost", Type: db.TypeUser}
	if err := svc.DB.Create(&author).Error; err != nil {
		t.Fatalf("create author: %v", err)
	}
	authorID := author.ID
	fixedTime := time.Date(2026, time.May, 26, 7, 0, 0, 0, time.UTC)
	for i, rev := range []struct {
		body    string
		message string
	}{
		{body: "# Home\n\nRevision 1.\n", message: "revision 1"},
		{body: "# Home\n\nRevision 2.\n", message: "revision 2"},
		{body: "# Home\n\nRevision 3.\n", message: "revision 3"},
	} {
		if _, err := svc.WikiCatalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
			RepositoryID:        rep.ID,
			AuthorID:            &authorID,
			Source:              wikicatalog.SourceREST,
			Message:             rev.message,
			OverrideCommittedAt: &fixedTime,
			Changes: []wikicatalog.Change{{
				Op:   wikicatalog.OpUpsert,
				Slug: "home",
				Body: []byte(rev.body),
			}},
		}); err != nil {
			t.Fatalf("ApplyChangeSet(%d): %v", i+1, err)
		}
	}

	if _, err := svc.ReconcileWikiV2(ctx, full); err != nil {
		t.Fatalf("ReconcileWikiV2: %v", err)
	}

	if history, err := svc.ListWikiPageHistory(ctx, full, "home"); err != nil {
		t.Fatalf("ListWikiPageHistory before reconcile check: %v", err)
	} else if len(history) != 3 || history[0].Message != "revision 3" || history[1].Message != "revision 2" || history[2].Message != "revision 1" {
		t.Fatalf("catalog history order mismatch: %#v", history)
	}

	if err := svc.DB.Where("repository_id = ?", rep.ID).Delete(&db.WikiPage{}).Error; err != nil {
		t.Fatalf("delete legacy wiki pages: %v", err)
	}

	history, total, err := svc.ListWikiPageHistoryPage(ctx, full, "home", 1, 10)
	if err != nil {
		t.Fatalf("ListWikiPageHistoryPage(home): %v", err)
	}
	if total != 3 || len(history) != 3 {
		t.Fatalf("unexpected history page: total=%d rows=%d", total, len(history))
	}
	if history[0].Message != "revision 3" || history[1].Message != "revision 2" || history[2].Message != "revision 1" {
		t.Fatalf("history order mismatch after reconcile: %#v", history)
	}
}

func TestWikiV2HistoryPreservesSameSecondOrderAcrossInterleavedPageCommits(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "v2history", "wiki-v2-history-interleaved")
	full := "v2history/wiki-v2-history-interleaved"

	rep, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	author := db.User{Login: "wiki-bot", Name: "Wiki Bot", Email: "gh-server@localhost", Type: db.TypeUser}
	if err := svc.DB.Create(&author).Error; err != nil {
		t.Fatalf("create author: %v", err)
	}
	authorID := author.ID
	fixedTime := time.Date(2026, time.May, 26, 7, 0, 0, 0, time.UTC)
	for _, change := range []struct {
		message string
		slug    string
		body    string
	}{
		{message: "home revision 1", slug: "home", body: "# Home\n\nRevision 1.\n"},
		{message: "faq revision 1", slug: "faq", body: "# FAQ\n\nRevision 1.\n"},
		{message: "home revision 2", slug: "home", body: "# Home\n\nRevision 2.\n"},
	} {
		if _, err := svc.WikiCatalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
			RepositoryID:        rep.ID,
			AuthorID:            &authorID,
			Source:              wikicatalog.SourceREST,
			Message:             change.message,
			OverrideCommittedAt: &fixedTime,
			Changes: []wikicatalog.Change{{
				Op:   wikicatalog.OpUpsert,
				Slug: change.slug,
				Body: []byte(change.body),
			}},
		}); err != nil {
			t.Fatalf("ApplyChangeSet(%s): %v", change.message, err)
		}
	}

	if _, err := svc.ReconcileWikiV2(ctx, full); err != nil {
		t.Fatalf("ReconcileWikiV2: %v", err)
	}
	if err := svc.DB.Where("repository_id = ?", rep.ID).Delete(&db.WikiPage{}).Error; err != nil {
		t.Fatalf("delete legacy wiki pages: %v", err)
	}

	history, total, err := svc.ListWikiPageHistoryPage(ctx, full, "home", 1, 10)
	if err != nil {
		t.Fatalf("ListWikiPageHistoryPage(home): %v", err)
	}
	if total != 2 || len(history) != 2 {
		t.Fatalf("unexpected history page: total=%d rows=%d", total, len(history))
	}
	if history[0].Message != "home revision 2" || history[1].Message != "home revision 1" {
		t.Fatalf("history order mismatch after interleaved same-second commits: %#v", history)
	}
}

func TestWikiV2BacklinksFallBackUntilBacklinkSnapshotCatchesUp(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "v2backfill", "wiki-v2-backfill")
	full := "v2backfill/wiki-v2-backfill"

	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nSeed.\n", "seed home", ""); err != nil {
		t.Fatalf("PutWikiPage(home): %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "faq", "# FAQ\n\nSee [[home]].\n", "seed faq", ""); err != nil {
		t.Fatalf("PutWikiPage(faq): %v", err)
	}
	if _, err := svc.ReconcileWikiV2(ctx, full); err != nil {
		t.Fatalf("ReconcileWikiV2: %v", err)
	}

	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if err := svc.DB.Model(&db.WikiIndexState{}).
		Where("repository_id = ?", repo.ID).
		Update("backlinks_indexed_sha", "").Error; err != nil {
		t.Fatalf("clear backlinks indexed sha: %v", err)
	}
	if err := svc.DB.Where("repository_id = ?", repo.ID).Delete(&db.WikiBacklink{}).Error; err != nil {
		t.Fatalf("delete derived backlinks: %v", err)
	}
	if err := svc.DB.Where("repository_id = ?", repo.ID).Delete(&db.WikiPage{}).Error; err != nil {
		t.Fatalf("delete legacy wiki pages: %v", err)
	}

	backlinks, err := svc.ListWikiBacklinks(ctx, full, "home")
	if err != nil {
		t.Fatalf("ListWikiBacklinks(home): %v", err)
	}
	if len(backlinks) != 1 || backlinks[0].Slug != "faq" {
		t.Fatalf("unexpected fallback backlinks: %+v", backlinks)
	}
}
