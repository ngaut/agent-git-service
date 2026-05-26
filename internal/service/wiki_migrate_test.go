package service_test

// Migration tool tests: build a real legacy wiki via PutWikiPage,
// then call MigrateWiki and verify the catalog reflects the same
// state (page rows, blob SHAs, commit identities). These are
// end-to-end tests that go through the actual gitstore on disk.

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

func setupWikiMigrationTestService(t testing.TB) (*service.Service, func()) {
	return testharness.NewService(t, testharness.ServiceConfig{MaxOpenConns: 1})
}

func TestMigrateAllWikis_ContinuesAfterRepoFailure(t *testing.T) {
	svc, cleanup := setupWikiMigrationTestService(t)
	defer cleanup()
	ctx := context.Background()

	badRepo := seedRepoForWikiMigration(t, svc, "alice", "bad")
	goodRepo := seedRepoForWikiMigration(t, svc, "bob", "good")

	// Seed git directly (bypassing the catalog) so MigrateWiki has
	// real work to do. After the runtime cutover, the only scenario
	// where MigrateWiki sees uncataloged git commits is when the data
	// pre-existed in git — exactly what this test models.
	for _, full := range []string{badRepo + ".wiki", goodRepo + ".wiki"} {
		if err := svc.Git.Init(ctx, full, "master", false); err != nil {
			t.Fatalf("init wiki %q: %v", full, err)
		}
	}
	if _, err := svc.Git.WriteFile(ctx, badRepo+".wiki", "master",
		"broken.md", "create bad", []byte("bad body")); err != nil {
		t.Fatalf("seed git bad: %v", err)
	}
	if _, err := svc.Git.WriteFile(ctx, goodRepo+".wiki", "master",
		"home.md", "create good", []byte("good body")); err != nil {
		t.Fatalf("seed git good: %v", err)
	}

	badIdentity, err := svc.GetRepo(ctx, badRepo)
	if err != nil {
		t.Fatalf("GetRepo bad: %v", err)
	}
	svc.WikiCatalog.OnChangeSetCommitted = func(_ context.Context, repoID uint, _ wikicatalog.ChangeSetResult) error {
		if repoID == badIdentity.ID {
			return context.DeadlineExceeded
		}
		return nil
	}

	report, err := svc.MigrateAllWikis(ctx, service.WikiMigrationOptions{})
	if err == nil {
		t.Fatal("MigrateAllWikis should surface the first repo error")
	}
	if !strings.Contains(err.Error(), `repo "alice/bad"`) {
		t.Fatalf("first error = %v, want repo-qualified bad repo error", err)
	}
	if report.FirstError == nil || report.FirstError.Error() != err.Error() {
		t.Fatalf("FirstError = %v, want %v", report.FirstError, err)
	}
	if got := report.ByRepo[goodRepo].NewCommits; got != 1 {
		t.Fatalf("good repo NewCommits = %d, want 1", got)
	}
	if got := report.ByRepo[goodRepo].Pages; got != 1 {
		t.Fatalf("good repo Pages = %d, want 1", got)
	}
}

func TestMigrateWiki_EmptyRepoIsNoOp(t *testing.T) {
	svc, cleanup := setupWikiMigrationTestService(t)
	defer cleanup()

	repoFullName := seedRepoForWikiMigration(t, svc, "alice", "rpo")
	stats, err := svc.MigrateWiki(context.Background(), repoFullName, service.WikiMigrationOptions{})
	if err != nil {
		t.Fatalf("MigrateWiki: %v", err)
	}
	if stats.GitCommits != 0 || stats.NewCommits != 0 || stats.Pages != 0 {
		t.Fatalf("expected empty stats, got %+v", stats)
	}
}

func TestMigrateWiki_ReplaysSinglePage(t *testing.T) {
	svc, cleanup := setupWikiMigrationTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiMigration(t, svc, "alice", "rpo")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "home", "# Home\n\nBody.", "create", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}

	stats, err := svc.MigrateWiki(ctx, repoFullName, service.WikiMigrationOptions{})
	if err != nil {
		t.Fatalf("MigrateWiki: %v", err)
	}
	// PutWikiPage routes through the catalog and reconciles
	// synth_commit_sha after materializing git, so the commit is
	// already present in wiki_changesets when MigrateWiki runs.
	if stats.GitCommits != 1 || stats.NewCommits != 0 || stats.SkippedExist != 1 || stats.Pages != 1 {
		t.Fatalf("stats %+v", stats)
	}

	rep, _ := svc.GetRepo(ctx, repoFullName)
	var page db.WikiPage
	if err := svc.DB.First(&page, "repository_id = ? AND slug_ci_v1 = ?", rep.ID, "home").Error; err != nil {
		t.Fatalf("catalog page not found: %v", err)
	}
	if page.Slug != "home" || page.BodySize == 0 {
		t.Fatalf("page row wrong: %+v", page)
	}
	// Catalog blob SHA must equal what git hash-object would produce
	// for the body, so existing If-Match values remain valid.
	if page.HeadBlobSHA == "" || page.BodySize != len("# Home\n\nBody.") {
		t.Fatalf("page body fields wrong: sha=%q size=%d", page.HeadBlobSHA, page.BodySize)
	}
}

func TestMigrateWiki_ReplaysHistoryInOrder(t *testing.T) {
	svc, cleanup := setupWikiMigrationTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiMigration(t, svc, "alice", "rpo")

	// Build a tiny history: create home, update home, create about, delete home.
	v1, err := svc.PutWikiPage(ctx, repoFullName, "home", "v1", "create home", "")
	if err != nil {
		t.Fatalf("create home: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, repoFullName, "home", "v2", "update home", v1.SHA); err != nil {
		t.Fatalf("update home: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, repoFullName, "about", "about body", "add about", ""); err != nil {
		t.Fatalf("create about: %v", err)
	}
	if err := svc.DeleteWikiPage(ctx, repoFullName, "home", "delete home"); err != nil {
		t.Fatalf("delete home: %v", err)
	}

	stats, err := svc.MigrateWiki(ctx, repoFullName, service.WikiMigrationOptions{})
	if err != nil {
		t.Fatalf("MigrateWiki: %v", err)
	}
	// PutWikiPage and DeleteWikiPage both route through the catalog
	// and reconcile synth_commit_sha to the materialized git SHA, so
	// all four commits are already known to the catalog and
	// MigrateWiki has nothing left to do.
	if stats.GitCommits != 4 || stats.NewCommits != 0 || stats.SkippedExist != 4 {
		t.Fatalf("stats %+v", stats)
	}
	if stats.Pages != 1 {
		t.Fatalf("expected 1 live page after delete, got %d", stats.Pages)
	}

	rep, _ := svc.GetRepo(ctx, repoFullName)

	// Live page after migration: only "about" survives.
	var pages []db.WikiPage
	if err := svc.DB.Where("repository_id = ? AND deleted_at IS NULL", rep.ID).Find(&pages).Error; err != nil {
		t.Fatalf("list pages: %v", err)
	}
	if len(pages) != 1 || pages[0].Slug != "about" {
		t.Fatalf("expected only about alive, got %+v", pages)
	}

	// Soft-deleted "home" page is still in catalog with revisions
	// recording create/update/delete.
	var homePage db.WikiPage
	if err := svc.DB.First(&homePage, "repository_id = ? AND slug_ci_v1 = ?", rep.ID, "home").Error; err != nil {
		t.Fatalf("read home: %v", err)
	}
	if homePage.DeletedAt == nil {
		t.Fatalf("home should be soft-deleted")
	}
	var revs []db.WikiPageRevision
	if err := svc.DB.Where("page_id = ?", homePage.PageID).Order("revision_id ASC").Find(&revs).Error; err != nil {
		t.Fatalf("read revisions: %v", err)
	}
	wantOps := []string{"create", "update", "delete"}
	gotOps := make([]string, 0, len(revs))
	for _, r := range revs {
		gotOps = append(gotOps, r.Op)
	}
	if strings.Join(gotOps, ",") != strings.Join(wantOps, ",") {
		t.Fatalf("revision ops %v, want %v", gotOps, wantOps)
	}
}

func TestMigrateWiki_IsIdempotent(t *testing.T) {
	svc, cleanup := setupWikiMigrationTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiMigration(t, svc, "alice", "rpo")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "home", "body", "create", ""); err != nil {
		t.Fatalf("put: %v", err)
	}

	stats1, err := svc.MigrateWiki(ctx, repoFullName, service.WikiMigrationOptions{})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	// PUT already populated the catalog with synth_commit_sha == git
	// SHA, so the first MigrateWiki call has nothing new to do — both
	// runs of MigrateWiki are no-ops in this scenario.
	if stats1.NewCommits != 0 || stats1.SkippedExist != 1 {
		t.Fatalf("first run stats %+v, want NewCommits=0 SkippedExist=1", stats1)
	}

	stats2, err := svc.MigrateWiki(ctx, repoFullName, service.WikiMigrationOptions{})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if stats2.NewCommits != 0 {
		t.Fatalf("second run should be a no-op, got NewCommits=%d", stats2.NewCommits)
	}
	if stats2.SkippedExist != 1 {
		t.Fatalf("second run SkippedExist = %d, want 1", stats2.SkippedExist)
	}
}

func TestListWikiPages_RebuildsCatalogAfterNonFastForwardRewrite(t *testing.T) {
	svc, cleanup := setupWikiMigrationTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiMigration(t, svc, "alice", "rpo")

	if _, err := svc.CreateLabel(ctx, repoFullName, "stale", "ff0000", "stale wiki label"); err != nil {
		t.Fatalf("CreateLabel stale: %v", err)
	}
	if _, err := svc.CreateLabel(ctx, repoFullName, "current", "00ff00", "current wiki label"); err != nil {
		t.Fatalf("CreateLabel current: %v", err)
	}

	homeV1, err := svc.PutWikiPage(ctx, repoFullName, "home", "v1", "create home", "")
	if err != nil {
		t.Fatalf("create home: %v", err)
	}
	if _, err := svc.SetWikiPageLabels(ctx, repoFullName, "home", []string{"current"}); err != nil {
		t.Fatalf("SetWikiPageLabels home: %v", err)
	}
	headA, err := svc.Git.HeadSHA(ctx, repoFullName+".wiki", "master")
	if err != nil {
		t.Fatalf("head after A: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, repoFullName, "about", "about body", "create about", ""); err != nil {
		t.Fatalf("create about: %v", err)
	}
	if _, err := svc.SetWikiPageLabels(ctx, repoFullName, "about", []string{"stale"}); err != nil {
		t.Fatalf("SetWikiPageLabels about: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, repoFullName, "home", "v2", "update home", homeV1.SHA); err != nil {
		t.Fatalf("update home: %v", err)
	}

	if _, err := svc.MigrateWiki(ctx, repoFullName, service.WikiMigrationOptions{}); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}

	repoDir, err := svc.Git.GetRepoPath(ctx, repoFullName+".wiki")
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	workDir := t.TempDir()
	if out, err := exec.Command("git", "clone", repoDir, workDir).CombinedOutput(); err != nil {
		t.Fatalf("git clone bare wiki: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", workDir, "checkout", "master").CombinedOutput(); err != nil {
		t.Fatalf("git checkout master: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", workDir, "reset", "--hard", headA).CombinedOutput(); err != nil {
		t.Fatalf("git reset --hard %s: %v\n%s", headA, err, out)
	}
	if out, err := exec.Command("git", "-C", workDir, "push", "--force", "origin", "master").CombinedOutput(); err != nil {
		t.Fatalf("git push --force origin master: %v\n%s", err, out)
	}

	pages, err := svc.ListWikiPages(ctx, repoFullName, service.ListWikiPagesOptions{Recursive: true})
	if err != nil {
		t.Fatalf("ListWikiPages after rewrite: %v", err)
	}
	svc.Wg.Wait()
	pages, err = svc.ListWikiPages(ctx, repoFullName, service.ListWikiPagesOptions{Recursive: true})
	if err != nil {
		t.Fatalf("ListWikiPages after background rebuild: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("ListWikiPages after rewrite returned %d pages, want 1: %+v", len(pages), pages)
	}
	if pages[0].Slug != "home" {
		t.Fatalf("ListWikiPages returned slug %q, want home", pages[0].Slug)
	}
	if pages[0].SHA != homeV1.SHA {
		t.Fatalf("ListWikiPages returned SHA %q, want rewritten home SHA %q", pages[0].SHA, homeV1.SHA)
	}

	rep, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	var pageRows []db.WikiPage
	if err := svc.DB.Where("repository_id = ? AND deleted_at IS NULL", rep.ID).Order("slug ASC").Find(&pageRows).Error; err != nil {
		t.Fatalf("list wiki_pages: %v", err)
	}
	if len(pageRows) != 1 || pageRows[0].Slug != "home" || pageRows[0].HeadBlobSHA != homeV1.SHA {
		t.Fatalf("live catalog rows = %+v, want only rewritten home row", pageRows)
	}

	var pageLabels []db.WikiPageLabel
	if err := svc.DB.Where("repository_id = ?", rep.ID).Order("slug ASC, label_id ASC").Find(&pageLabels).Error; err != nil {
		t.Fatalf("list wiki_page_labels: %v", err)
	}
	if len(pageLabels) != 1 || pageLabels[0].Slug != "home" {
		t.Fatalf("wiki_page_labels rows = %+v, want only home label after rebuild", pageLabels)
	}

	labels, err := svc.ListWikiPageLabels(ctx, repoFullName, "home")
	if err != nil {
		t.Fatalf("ListWikiPageLabels(home): %v", err)
	}
	if got := labelNames(labels); strings.Join(got, ",") != "current" {
		t.Fatalf("home labels = %v, want [current] after rebuild", got)
	}

	if labels, err := svc.ListWikiPageLabels(ctx, repoFullName, "about"); err == nil {
		t.Fatalf("ListWikiPageLabels(about) = %v, want not found after rebuild", labelNames(labels))
	}

	var changesets []db.WikiChangeset
	if err := svc.DB.Where("repository_id = ?", rep.ID).Order("changeset_id ASC").Find(&changesets).Error; err != nil {
		t.Fatalf("list wiki_changesets: %v", err)
	}
	if len(changesets) != 1 {
		t.Fatalf("wiki_changesets rows = %d, want 1 after rebuild", len(changesets))
	}
	if changesets[0].SynthCommitSHA != strings.ToLower(headA) {
		t.Fatalf("replayed synth_commit_sha = %q, want %q", changesets[0].SynthCommitSHA, strings.ToLower(headA))
	}
}

func TestGetWikiPageAndHistory_RefreshCatalogAfterNonFastForwardRewrite(t *testing.T) {
	svc, cleanup := setupWikiMigrationTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiMigration(t, svc, "alice", "rpo")

	homeV1, err := svc.PutWikiPage(ctx, repoFullName, "home", "v1", "create home", "")
	if err != nil {
		t.Fatalf("create home: %v", err)
	}
	headA, err := svc.Git.HeadSHA(ctx, repoFullName+".wiki", "master")
	if err != nil {
		t.Fatalf("head after A: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, repoFullName, "about", "about body", "create about", ""); err != nil {
		t.Fatalf("create about: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, repoFullName, "home", "v2", "update home", homeV1.SHA); err != nil {
		t.Fatalf("update home: %v", err)
	}

	if _, err := svc.MigrateWiki(ctx, repoFullName, service.WikiMigrationOptions{}); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}

	repoDir, err := svc.Git.GetRepoPath(ctx, repoFullName+".wiki")
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	workDir := t.TempDir()
	if out, err := exec.Command("git", "clone", repoDir, workDir).CombinedOutput(); err != nil {
		t.Fatalf("git clone bare wiki: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", workDir, "checkout", "master").CombinedOutput(); err != nil {
		t.Fatalf("git checkout master: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", workDir, "reset", "--hard", headA).CombinedOutput(); err != nil {
		t.Fatalf("git reset --hard %s: %v\n%s", headA, err, out)
	}
	if out, err := exec.Command("git", "-C", workDir, "push", "--force", "origin", "master").CombinedOutput(); err != nil {
		t.Fatalf("git push --force origin master: %v\n%s", err, out)
	}

	page, err := svc.GetWikiPage(ctx, repoFullName, "home")
	if err != nil {
		t.Fatalf("GetWikiPage after rewrite: %v", err)
	}
	svc.Wg.Wait()
	page, err = svc.GetWikiPage(ctx, repoFullName, "home")
	if err != nil {
		t.Fatalf("GetWikiPage after background rebuild: %v", err)
	}
	if page.SHA != homeV1.SHA {
		t.Fatalf("GetWikiPage returned SHA %q, want rewritten home SHA %q", page.SHA, homeV1.SHA)
	}
	if _, err := svc.GetWikiPage(ctx, repoFullName, "about"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("GetWikiPage(about) err = %v, want ErrNotFound", err)
	}

	history, total, err := svc.ListWikiPageHistoryPage(ctx, repoFullName, "home", 1, 10)
	if err != nil {
		t.Fatalf("ListWikiPageHistoryPage after rewrite: %v", err)
	}
	if total != 1 || len(history) != 1 {
		t.Fatalf("history total=%d len=%d, want 1/1", total, len(history))
	}
	if history[0].SHA != headA {
		t.Fatalf("history SHA = %q, want rewritten head %q", history[0].SHA, headA)
	}
}

func TestEnsureWikiCatalogCurrent_PreservesRESTHeadWhenGitProjectionLags_Issue1446(t *testing.T) {
	svc, cleanup := setupWikiMigrationTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiMigration(t, svc, "alice", "projection-lag")

	page, err := svc.PutWikiPage(ctx, repoFullName, "home", "catalog body", "create home", "")
	if err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}

	rep, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if err := svc.DB.Model(&db.WikiChangeset{}).
		Where("repository_id = ?", rep.ID).
		Updates(map[string]any{
			"synth_commit_sha": "1111111111111111111111111111111111111111",
			"synth_format_ver": int16(0),
		}).Error; err != nil {
		t.Fatalf("set pending synthetic SHA: %v", err)
	}

	got, err := svc.GetWikiPage(ctx, repoFullName, "home")
	if err != nil {
		t.Fatalf("GetWikiPage after git lag: %v", err)
	}
	if got.Slug != "home" || got.SHA != page.SHA || got.Body != "catalog body" {
		t.Fatalf("GetWikiPage = %+v, want slug=home sha=%s body preserved", got, page.SHA)
	}

	var changesets []db.WikiChangeset
	if err := svc.DB.Where("repository_id = ?", rep.ID).Order("changeset_id ASC").Find(&changesets).Error; err != nil {
		t.Fatalf("list wiki_changesets: %v", err)
	}
	if len(changesets) != 1 {
		t.Fatalf("wiki_changesets rows = %d, want 1", len(changesets))
	}
	if changesets[0].Source != string(wikicatalog.SourceREST) {
		t.Fatalf("changeset source = %q, want %q", changesets[0].Source, wikicatalog.SourceREST)
	}
}

func TestEnsureWikiCatalogCurrent_NonBlocking(t *testing.T) {
	svc, cleanup := setupWikiMigrationTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiMigration(t, svc, "alice", "nonblocking")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "home", "v1", "create home", ""); err != nil {
		t.Fatalf("PutWikiPage home: %v", err)
	}
	if _, err := svc.Git.WriteFile(ctx, repoFullName+".wiki", "master", "about.md", "add about", []byte("about body")); err != nil {
		t.Fatalf("git write about: %v", err)
	}

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var released int32
	svc.SetWikiBackgroundMigrationStartedHookForTest(func(fullName string) {
		if fullName == repoFullName {
			started <- struct{}{}
		}
	})
	svc.SetWikiMigrationAfterSnapshotHookForTest(func(fullName string) {
		if fullName == repoFullName {
			<-release
		}
	})
	defer func() {
		svc.SetWikiBackgroundMigrationStartedHookForTest(nil)
		svc.SetWikiMigrationAfterSnapshotHookForTest(nil)
		if atomic.CompareAndSwapInt32(&released, 0, 1) {
			close(release)
		}
	}()

	begin := time.Now()
	pages, err := svc.ListWikiPages(ctx, repoFullName, service.ListWikiPagesOptions{Recursive: true})
	if err != nil {
		t.Fatalf("ListWikiPages: %v", err)
	}
	if elapsed := time.Since(begin); elapsed > 100*time.Millisecond {
		t.Fatalf("ListWikiPages took %s, want <= 100ms while migration runs in background", elapsed)
	}
	if len(pages) != 1 || pages[0].Slug != "home" {
		t.Fatalf("initial pages = %+v, want current catalog snapshot only", pages)
	}

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for background migration to start")
	}
	if !svc.IsWikiBackgroundMigrationRunning(ctx, repoFullName) {
		t.Fatal("expected background migration to be marked running")
	}

	if atomic.CompareAndSwapInt32(&released, 0, 1) {
		close(release)
	}
	svc.Wg.Wait()

	pages, err = svc.ListWikiPages(ctx, repoFullName, service.ListWikiPagesOptions{Recursive: true})
	if err != nil {
		t.Fatalf("ListWikiPages after background migration: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("final pages = %+v, want 2 pages after background migration", pages)
	}
}

func TestEnsureWikiCatalogCurrent_BackgroundSingleflight(t *testing.T) {
	svc, cleanup := setupWikiMigrationTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiMigration(t, svc, "alice", "singleflight")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "home", "v1", "create home", ""); err != nil {
		t.Fatalf("PutWikiPage home: %v", err)
	}
	if _, err := svc.Git.WriteFile(ctx, repoFullName+".wiki", "master", "about.md", "add about", []byte("about body")); err != nil {
		t.Fatalf("git write about: %v", err)
	}

	var startedCount int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var released int32
	svc.SetWikiBackgroundMigrationStartedHookForTest(func(fullName string) {
		if fullName != repoFullName {
			return
		}
		if atomic.AddInt32(&startedCount, 1) == 1 {
			started <- struct{}{}
		}
	})
	svc.SetWikiMigrationAfterSnapshotHookForTest(func(fullName string) {
		if fullName == repoFullName {
			<-release
		}
	})
	defer func() {
		svc.SetWikiBackgroundMigrationStartedHookForTest(nil)
		svc.SetWikiMigrationAfterSnapshotHookForTest(nil)
		if atomic.CompareAndSwapInt32(&released, 0, 1) {
			close(release)
		}
	}()

	errCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := svc.ListWikiPages(ctx, repoFullName, service.ListWikiPagesOptions{Recursive: true})
			errCh <- err
		}()
	}

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for background migration to start")
	}
	time.Sleep(150 * time.Millisecond)
	if got := atomic.LoadInt32(&startedCount); got != 1 {
		t.Fatalf("background migration started %d times, want 1", got)
	}
	if !svc.IsWikiBackgroundMigrationRunning(ctx, repoFullName) {
		t.Fatal("expected background migration to be running")
	}

	if atomic.CompareAndSwapInt32(&released, 0, 1) {
		close(release)
	}
	svc.Wg.Wait()
	for i := 0; i < 8; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("concurrent ListWikiPages[%d]: %v", i, err)
		}
	}
}

func TestListWikiPages_ClearsCatalogAfterWikiBranchDeletion(t *testing.T) {
	svc, cleanup := setupWikiMigrationTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiMigration(t, svc, "alice", "rpo")

	if _, err := svc.CreateLabel(ctx, repoFullName, "current", "00ff00", "current wiki label"); err != nil {
		t.Fatalf("CreateLabel current: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, repoFullName, "home", "v1", "create home", ""); err != nil {
		t.Fatalf("PutWikiPage home: %v", err)
	}
	if _, err := svc.SetWikiPageLabels(ctx, repoFullName, "home", []string{"current"}); err != nil {
		t.Fatalf("SetWikiPageLabels home: %v", err)
	}
	if _, err := svc.MigrateWiki(ctx, repoFullName, service.WikiMigrationOptions{}); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}

	repoDir, err := svc.Git.GetRepoPath(ctx, repoFullName+".wiki")
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	if out, err := exec.Command("git", "-C", repoDir, "update-ref", "-d", "refs/heads/master").CombinedOutput(); err != nil {
		t.Fatalf("git update-ref -d refs/heads/master: %v\n%s", err, out)
	}

	pages, err := svc.ListWikiPages(ctx, repoFullName, service.ListWikiPagesOptions{Recursive: true})
	if err != nil {
		t.Fatalf("ListWikiPages after branch deletion: %v", err)
	}
	svc.Wg.Wait()
	pages, err = svc.ListWikiPages(ctx, repoFullName, service.ListWikiPagesOptions{Recursive: true})
	if err != nil {
		t.Fatalf("ListWikiPages after background branch cleanup: %v", err)
	}
	if len(pages) != 0 {
		t.Fatalf("ListWikiPages returned %d pages after branch deletion, want 0: %+v", len(pages), pages)
	}

	filteredPages, err := svc.ListWikiPages(ctx, repoFullName, service.ListWikiPagesOptions{
		Recursive: true,
		Labels:    []string{"current"},
	})
	if err != nil {
		t.Fatalf("ListWikiPages with label filter after branch deletion: %v", err)
	}
	if len(filteredPages) != 0 {
		t.Fatalf("ListWikiPages with label filter returned %d pages after branch deletion, want 0: %+v", len(filteredPages), filteredPages)
	}

	rep, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	var pageRows []db.WikiPage
	if err := svc.DB.Where("repository_id = ? AND deleted_at IS NULL", rep.ID).Find(&pageRows).Error; err != nil {
		t.Fatalf("list wiki_pages: %v", err)
	}
	if len(pageRows) != 0 {
		t.Fatalf("live wiki_pages rows = %+v, want none after branch deletion", pageRows)
	}

	var pageLabels []db.WikiPageLabel
	if err := svc.DB.Where("repository_id = ?", rep.ID).Find(&pageLabels).Error; err != nil {
		t.Fatalf("list wiki_page_labels: %v", err)
	}
	if len(pageLabels) != 0 {
		t.Fatalf("wiki_page_labels rows = %+v, want none after branch deletion", pageLabels)
	}

	var changesets []db.WikiChangeset
	if err := svc.DB.Where("repository_id = ?", rep.ID).Find(&changesets).Error; err != nil {
		t.Fatalf("list wiki_changesets: %v", err)
	}
	if len(changesets) != 0 {
		t.Fatalf("wiki_changesets rows = %+v, want none after branch deletion", changesets)
	}
}

func TestMigrateWiki_SerializesConcurrentRefreshAfterRewrite(t *testing.T) {
	svc, cleanup := setupWikiMigrationTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiMigration(t, svc, "alice", "rpo")

	homeV1, err := svc.PutWikiPage(ctx, repoFullName, "home", "v1", "create home", "")
	if err != nil {
		t.Fatalf("create home: %v", err)
	}
	headA, err := svc.Git.HeadSHA(ctx, repoFullName+".wiki", "master")
	if err != nil {
		t.Fatalf("head after A: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, repoFullName, "about", "about body", "create about", ""); err != nil {
		t.Fatalf("create about: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, repoFullName, "home", "v2", "update home", homeV1.SHA); err != nil {
		t.Fatalf("update home: %v", err)
	}

	if _, err := svc.MigrateWiki(ctx, repoFullName, service.WikiMigrationOptions{}); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}

	repoDir, err := svc.Git.GetRepoPath(ctx, repoFullName+".wiki")
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	workDir := t.TempDir()
	if out, err := exec.Command("git", "clone", repoDir, workDir).CombinedOutput(); err != nil {
		t.Fatalf("git clone bare wiki: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", workDir, "checkout", "master").CombinedOutput(); err != nil {
		t.Fatalf("git checkout master: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", workDir, "reset", "--hard", headA).CombinedOutput(); err != nil {
		t.Fatalf("git reset --hard %s: %v\n%s", headA, err, out)
	}
	if out, err := exec.Command("git", "-C", workDir, "push", "--force", "origin", "master").CombinedOutput(); err != nil {
		t.Fatalf("git push --force origin master: %v\n%s", err, out)
	}

	enterCh := make(chan struct{}, 1)
	releaseCh := make(chan struct{})
	var entered int32
	svc.SetWikiMigrationAfterSnapshotHookForTest(func(fullName string) {
		if fullName != repoFullName {
			return
		}
		if atomic.AddInt32(&entered, 1) == 1 {
			enterCh <- struct{}{}
			<-releaseCh
		}
	})
	defer func() {
		svc.SetWikiMigrationAfterSnapshotHookForTest(nil)
	}()

	errCh := make(chan error, 2)
	go func() {
		_, err := svc.MigrateWiki(ctx, repoFullName, service.WikiMigrationOptions{})
		errCh <- err
	}()

	select {
	case <-enterCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first migration to reach the snapshot hook")
	}

	go func() {
		_, err := svc.MigrateWiki(ctx, repoFullName, service.WikiMigrationOptions{})
		errCh <- err
	}()

	time.Sleep(150 * time.Millisecond)
	if got := atomic.LoadInt32(&entered); got != 1 {
		t.Fatalf("snapshot hook entered %d times while first migration was blocked, want serialization", got)
	}

	close(releaseCh)
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("concurrent migrate[%d]: %v", i, err)
		}
	}

	rep, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	var changesets []db.WikiChangeset
	if err := svc.DB.Where("repository_id = ?", rep.ID).Order("changeset_id ASC").Find(&changesets).Error; err != nil {
		t.Fatalf("list wiki_changesets: %v", err)
	}
	if len(changesets) != 1 {
		t.Fatalf("wiki_changesets rows = %d, want 1 after serialized concurrent refresh", len(changesets))
	}
	if changesets[0].SynthCommitSHA != strings.ToLower(headA) {
		t.Fatalf("replayed synth_commit_sha = %q, want %q", changesets[0].SynthCommitSHA, strings.ToLower(headA))
	}
}

func TestMigrateWiki_PreservesGitCommitSHAs(t *testing.T) {
	svc, cleanup := setupWikiMigrationTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiMigration(t, svc, "alice", "rpo")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "home", "body", "create", ""); err != nil {
		t.Fatalf("put: %v", err)
	}

	// Capture the legacy git commit SHA before migration.
	commits, err := svc.Git.ListCommits(ctx, repoFullName+".wiki", 10, nil)
	if err != nil {
		t.Fatalf("list legacy commits: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(commits))
	}
	originalSHA := strings.ToLower(commits[0].SHA)

	if _, err := svc.MigrateWiki(ctx, repoFullName, service.WikiMigrationOptions{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	rep, _ := svc.GetRepo(ctx, repoFullName)
	var cs db.WikiChangeset
	if err := svc.DB.First(&cs, "repository_id = ?", rep.ID).Error; err != nil {
		t.Fatalf("read changeset: %v", err)
	}
	if cs.SynthCommitSHA != originalSHA {
		t.Fatalf("synth_commit_sha = %q, want original git SHA %q",
			cs.SynthCommitSHA, originalSHA)
	}
}

func TestMigrateWiki_PreservesLegacyReadableSlug(t *testing.T) {
	svc, cleanup := setupWikiMigrationTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiMigration(t, svc, "alice", "rpo")

	// Push a mixed-case slug directly via the gitstore, bypassing the
	// current write validator. Migration must preserve legacy-readable
	// slugs because pre-cutover reads already resolve them.
	if err := svc.Git.Init(ctx, repoFullName+".wiki", "master", false); err != nil {
		t.Fatalf("init wiki: %v", err)
	}
	if _, err := svc.Git.WriteFile(ctx, repoFullName+".wiki", "master",
		"Mixed_Case.md", "legacy push", []byte("# Legacy\n")); err != nil {
		t.Fatalf("write file: %v", err)
	}

	stats, err := svc.MigrateWiki(ctx, repoFullName, service.WikiMigrationOptions{})
	if err != nil {
		t.Fatalf("MigrateWiki: %v", err)
	}
	if stats.Pages != 1 || stats.NewCommits != 1 {
		t.Fatalf("stats %+v", stats)
	}

	rep, _ := svc.GetRepo(ctx, repoFullName)
	var rows []db.WikiPage
	if err := svc.DB.Where("repository_id = ?", rep.ID).Find(&rows).Error; err != nil {
		t.Fatalf("list rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one row in catalog, got %+v", rows)
	}
	if rows[0].Slug != "Mixed_Case" || rows[0].SlugCIV1 != "mixed-case" {
		t.Fatalf("row %+v, want preserved readable slug with canonical lookup key", rows[0])
	}
}

func TestMigrateWiki_PreservesEmptyCommitSHA(t *testing.T) {
	svc, cleanup := setupWikiMigrationTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiMigration(t, svc, "alice", "rpo")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "home", "body", "create", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}

	repoDir, err := svc.Git.GetRepoPath(ctx, repoFullName+".wiki")
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	workDir := t.TempDir()
	if out, err := exec.Command("git", "clone", repoDir, workDir).CombinedOutput(); err != nil {
		t.Fatalf("git clone bare wiki: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", workDir, "checkout", "master").CombinedOutput(); err != nil {
		t.Fatalf("git checkout master: %v\n%s", err, out)
	}
	cmd := exec.Command("git", "-C", workDir,
		"-c", "user.name=Test User",
		"-c", "user.email=test@example.com",
		"commit", "--allow-empty", "-m", "empty history marker")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit --allow-empty: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", workDir, "push", "origin", "master").CombinedOutput(); err != nil {
		t.Fatalf("git push empty commit: %v\n%s", err, out)
	}

	commits, err := svc.Git.ListAllCommits(ctx, repoFullName+".wiki", nil)
	if err != nil {
		t.Fatalf("ListAllCommits: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("expected 2 git commits, got %d", len(commits))
	}
	emptySHA := strings.ToLower(strings.TrimSpace(commits[0].SHA))

	stats, err := svc.MigrateWiki(ctx, repoFullName, service.WikiMigrationOptions{})
	if err != nil {
		t.Fatalf("MigrateWiki: %v", err)
	}
	// After the runtime cutover, the first commit was created by
	// PutWikiPage routing through ApplyChangeSet and is already in the
	// catalog (synth_commit_sha == git SHA, reconciled by the
	// post-commit materialize hook). MigrateWiki only needs to replay
	// the externally-pushed empty commit, so NewCommits == 1 and the
	// first commit shows up as SkippedExist == 1.
	if stats.NewCommits != 1 || stats.SkippedExist != 1 {
		t.Fatalf("stats %+v, want NewCommits=1 SkippedExist=1", stats)
	}

	rep, _ := svc.GetRepo(ctx, repoFullName)
	var cs db.WikiChangeset
	if err := svc.DB.Where("repository_id = ? AND synth_commit_sha = ?", rep.ID, emptySHA).First(&cs).Error; err != nil {
		t.Fatalf("empty commit changeset not found: %v", err)
	}
	if cs.PageCount != 0 {
		t.Fatalf("empty commit PageCount = %d, want 0", cs.PageCount)
	}
}

// seedRepoForWikiMigration creates owner, repo, and flags has_wiki=true.
// Returns the repo's full name.
func seedRepoForWikiMigration(t *testing.T, svc *service.Service, login, name string) string {
	t.Helper()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: login, Name: login, Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: login, Name: name, AutoInit: true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	full := login + "/" + name
	// Flag has_wiki=true so MigrateAllWikis picks it up; per-repo
	// MigrateWiki does not consult the flag but production runs
	// usually do.
	if err := svc.DB.Model(&db.Repository{}).
		Where("full_name = ?", full).
		Update("has_wiki", true).Error; err != nil {
		t.Fatalf("set has_wiki: %v", err)
	}
	return full
}
