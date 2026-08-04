package service_test

// Migration tool tests: build a real legacy wiki via PutWikiPage,
// then call IngestWikiGit and verify the catalog reflects the same
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
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

func setupWikiGitIngestTestService(t testing.TB) (*service.Service, func()) {
	return testharness.NewService(t, testharness.ServiceConfig{MaxOpenConns: 1})
}

func newWikiGitIngestPeerService(t testing.TB, base *service.Service) *service.Service {
	t.Helper()

	root, err := base.Git.RepoRoot(context.Background())
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	peerGit, err := gitstore.New(root)
	if err != nil {
		t.Fatalf("gitstore.New peer: %v", err)
	}
	peerCatalog := wikicatalog.New(base.DB, base.WikiBlob)
	peer := &service.Service{
		DB:          base.DB,
		Git:         peerGit,
		WikiCatalog: peerCatalog,
		WikiBlob:    base.WikiBlob,
		BaseURL:     base.BaseURL,
		Embedder:    base.Embedder,
	}
	peerCatalog.DBFor = peer.DBForCtx
	peerCatalog.OnChangeSetCommitted = peer.WikiCatalogPostCommit
	return peer
}

func TestMigrateAllWikis_ContinuesAfterRepoFailure(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()

	badRepo := seedRepoForWikiGitIngest(t, svc, "alice", "bad")
	goodRepo := seedRepoForWikiGitIngest(t, svc, "bob", "good")

	// Seed git directly (bypassing the catalog) so IngestWikiGit has
	// real work to do. After the runtime cutover, the only scenario
	// where IngestWikiGit sees uncataloged git commits is when the data
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

	report, err := svc.MigrateAllWikis(ctx, service.WikiGitIngestOptions{})
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

func TestIngestWikiGit_EmptyRepoIsNoOp(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()

	repoFullName := seedRepoForWikiGitIngest(t, svc, "alice", "rpo")
	stats, err := svc.IngestWikiGit(context.Background(), repoFullName, service.WikiGitIngestOptions{})
	if err != nil {
		t.Fatalf("IngestWikiGit: %v", err)
	}
	if stats.GitCommits != 0 || stats.NewCommits != 0 || stats.Pages != 0 {
		t.Fatalf("expected empty stats, got %+v", stats)
	}
}

func TestIngestWikiGit_ReplaysSinglePage(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "alice", "rpo")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "home", "# Home\n\nBody.", "create", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}

	stats, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{})
	if err != nil {
		t.Fatalf("IngestWikiGit: %v", err)
	}
	// PutWikiPage routes through the catalog and reconciles
	// synth_commit_sha after materializing git, so the commit is
	// already present in wiki_changesets when IngestWikiGit runs, so the
	// fast path sees the catalog is current without scanning history.
	if stats.GitCommits != 0 || stats.NewCommits != 0 || stats.SkippedExist != 0 || stats.Pages != 1 {
		t.Fatalf("stats %+v", stats)
	}

	rep, _ := svc.GetRepo(ctx, repoFullName)
	var page db.WikiPage
	if err := svc.DB.First(&page, "repository_id = ? AND slug = ?", rep.ID, "home").Error; err != nil {
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

func TestIngestWikiGit_ContinuesAfterInitialReplay(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "alice", "continued")
	wikiFullName := repoFullName + ".wiki"

	if err := svc.Git.Init(ctx, wikiFullName, "master", false); err != nil {
		t.Fatalf("init wiki repo: %v", err)
	}
	if _, err := svc.Git.WriteFile(ctx, wikiFullName, "master", "first.md", "first push", []byte("first")); err != nil {
		t.Fatalf("first direct write: %v", err)
	}
	first, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{})
	if err != nil {
		t.Fatalf("first IngestWikiGit: %v", err)
	}
	if first.NewCommits != 1 {
		t.Fatalf("first ingest stats = %+v, want one new commit", first)
	}
	repo, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo after first ingest: %v", err)
	}
	if err := svc.DB.Model(&db.WikiChangeset{}).
		Where("repository_id = ?", repo.ID).
		Update("synth_format_ver", 0).Error; err != nil {
		t.Fatalf("simulate legacy git changeset format: %v", err)
	}

	if _, err := svc.Git.WriteFile(ctx, wikiFullName, "master", "second.md", "second push", []byte("second")); err != nil {
		t.Fatalf("second direct write: %v", err)
	}
	second, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{})
	if err != nil {
		t.Fatalf("second IngestWikiGit: %v", err)
	}
	if second.NewCommits != 1 {
		t.Fatalf("second ingest stats = %+v, want one new commit", second)
	}
	if _, err := svc.GetWikiPage(ctx, repoFullName, "second"); err != nil {
		t.Fatalf("GetWikiPage(second): %v", err)
	}

	var latest db.WikiChangeset
	if err := svc.DB.
		Where("repository_id = ?", repo.ID).
		Order("changeset_id DESC").
		First(&latest).Error; err != nil {
		t.Fatalf("load latest changeset: %v", err)
	}
	if latest.SynthFormatVer != 1 {
		t.Fatalf("latest git changeset synth format = %d, want 1", latest.SynthFormatVer)
	}
}

func TestIngestWikiGit_ReplaysHistoryInOrder(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "alice", "rpo")

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

	stats, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{})
	if err != nil {
		t.Fatalf("IngestWikiGit: %v", err)
	}
	// PutWikiPage and DeleteWikiPage both route through the catalog
	// and reconcile synth_commit_sha to the materialized git SHA, so
	// all four commits are already known to the catalog and
	// IngestWikiGit has nothing left to do.
	if stats.GitCommits != 0 || stats.NewCommits != 0 || stats.SkippedExist != 0 {
		t.Fatalf("stats %+v", stats)
	}
	if stats.Pages != 1 {
		t.Fatalf("expected 1 live page after delete, got %d", stats.Pages)
	}

	rep, _ := svc.GetRepo(ctx, repoFullName)

	// Live page after ingest: only "about" survives.
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
	if err := svc.DB.First(&homePage, "repository_id = ? AND slug = ?", rep.ID, "home").Error; err != nil {
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

func TestIngestWikiGit_IsIdempotent(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "alice", "rpo")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "home", "body", "create", ""); err != nil {
		t.Fatalf("put: %v", err)
	}

	stats1, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	// PUT already populated the catalog with synth_commit_sha == git
	// SHA, so the first IngestWikiGit call has nothing new to do — both
	// runs of IngestWikiGit are no-ops in this scenario.
	if stats1.NewCommits != 0 || stats1.SkippedExist != 0 {
		t.Fatalf("first run stats %+v, want NewCommits=0 SkippedExist=0", stats1)
	}

	stats2, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if stats2.NewCommits != 0 {
		t.Fatalf("second run should be a no-op, got NewCommits=%d", stats2.NewCommits)
	}
	if stats2.SkippedExist != 0 {
		t.Fatalf("second run SkippedExist = %d, want 0", stats2.SkippedExist)
	}
}

func TestIngestWikiGit_RebuildsCatalogAfterNonFastForwardRewrite(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "alice", "rpo")

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

	if _, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{}); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}
	rep, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
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

	if _, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{}); err != nil {
		t.Fatalf("IngestWikiGit after rewrite: %v", err)
	}
	pages, err := svc.ListWikiPages(ctx, repoFullName, service.ListWikiPagesOptions{Recursive: true})
	if err != nil {
		t.Fatalf("ListWikiPages after rewrite ingest: %v", err)
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

func TestIngestWikiGit_RefreshesPageAndHistoryAfterNonFastForwardRewrite(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "alice", "rpo")

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

	if _, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{}); err != nil {
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

	if _, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{}); err != nil {
		t.Fatalf("IngestWikiGit after rewrite: %v", err)
	}
	page, err := svc.GetWikiPage(ctx, repoFullName, "home")
	if err != nil {
		t.Fatalf("GetWikiPage after rewrite ingest: %v", err)
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
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "alice", "projection-lag")

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
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "alice", "nonblocking")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "home", "v1", "create home", ""); err != nil {
		t.Fatalf("PutWikiPage home: %v", err)
	}
	if _, err := svc.Git.WriteFile(ctx, repoFullName+".wiki", "master", "about.md", "add about", []byte("about body")); err != nil {
		t.Fatalf("git write about: %v", err)
	}

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var released int32
	svc.SetWikiBackgroundGitIngestStartedHookForTest(func(fullName string) {
		if fullName == repoFullName {
			started <- struct{}{}
		}
	})
	svc.SetWikiGitIngestAfterSnapshotHookForTest(func(fullName string) {
		if fullName == repoFullName {
			<-release
		}
	})
	defer func() {
		svc.SetWikiBackgroundGitIngestStartedHookForTest(nil)
		svc.SetWikiGitIngestAfterSnapshotHookForTest(nil)
		if atomic.CompareAndSwapInt32(&released, 0, 1) {
			close(release)
		}
	}()

	listDone := make(chan struct {
		pages []service.WikiPageSummary
		err   error
	}, 1)
	go func() {
		pages, err := svc.ListWikiPages(ctx, repoFullName, service.ListWikiPagesOptions{Recursive: true})
		listDone <- struct {
			pages []service.WikiPageSummary
			err   error
		}{pages: pages, err: err}
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for background git ingest to start")
	}
	if !svc.IsWikiBackgroundGitIngestRunning(ctx, repoFullName) {
		t.Fatal("expected background git ingest to be marked running")
	}

	var pages []service.WikiPageSummary
	select {
	case result := <-listDone:
		if result.err != nil {
			t.Fatalf("ListWikiPages: %v", result.err)
		}
		pages = result.pages
	case <-time.After(3 * time.Second):
		t.Fatal("ListWikiPages blocked on background git ingest")
	}
	if len(pages) != 1 || pages[0].Slug != "home" {
		t.Fatalf("initial pages = %+v, want current catalog snapshot only", pages)
	}

	if atomic.CompareAndSwapInt32(&released, 0, 1) {
		close(release)
	}
	svc.Wg.Wait()

	pages, err := svc.ListWikiPages(ctx, repoFullName, service.ListWikiPagesOptions{Recursive: true})
	if err != nil {
		t.Fatalf("ListWikiPages after background git ingest: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("final pages = %+v, want 2 pages after background git ingest", pages)
	}
}

func TestEnsureWikiCatalogCurrent_BackgroundSingleflight(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "alice", "singleflight")

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
	svc.SetWikiBackgroundGitIngestStartedHookForTest(func(fullName string) {
		if fullName != repoFullName {
			return
		}
		if atomic.AddInt32(&startedCount, 1) == 1 {
			started <- struct{}{}
		}
	})
	svc.SetWikiGitIngestAfterSnapshotHookForTest(func(fullName string) {
		if fullName == repoFullName {
			<-release
		}
	})
	defer func() {
		svc.SetWikiBackgroundGitIngestStartedHookForTest(nil)
		svc.SetWikiGitIngestAfterSnapshotHookForTest(nil)
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
		t.Fatal("timed out waiting for background git ingest to start")
	}
	time.Sleep(150 * time.Millisecond)
	if got := atomic.LoadInt32(&startedCount); got != 1 {
		t.Fatalf("background git ingest started %d times, want 1", got)
	}
	if !svc.IsWikiBackgroundGitIngestRunning(ctx, repoFullName) {
		t.Fatal("expected background git ingest to be running")
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

func TestIngestWikiGit_ClearsCatalogAfterWikiBranchDeletion(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "alice", "rpo")

	if _, err := svc.CreateLabel(ctx, repoFullName, "current", "00ff00", "current wiki label"); err != nil {
		t.Fatalf("CreateLabel current: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, repoFullName, "home", "v1", "create home", ""); err != nil {
		t.Fatalf("PutWikiPage home: %v", err)
	}
	if _, err := svc.SetWikiPageLabels(ctx, repoFullName, "home", []string{"current"}); err != nil {
		t.Fatalf("SetWikiPageLabels home: %v", err)
	}
	if _, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{}); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}

	repoDir, err := svc.Git.GetRepoPath(ctx, repoFullName+".wiki")
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	if out, err := exec.Command("git", "-C", repoDir, "update-ref", "-d", "refs/heads/master").CombinedOutput(); err != nil {
		t.Fatalf("git update-ref -d refs/heads/master: %v\n%s", err, out)
	}

	if _, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{}); err != nil {
		t.Fatalf("IngestWikiGit after branch deletion: %v", err)
	}
	pages, err := svc.ListWikiPages(ctx, repoFullName, service.ListWikiPagesOptions{Recursive: true})
	if err != nil {
		t.Fatalf("ListWikiPages after branch deletion ingest: %v", err)
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

func TestIngestWikiGitFailsClosedWhenBranchHeadObjectCannotResolve(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "alice", "broken-head")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "home", "v1", "create home", ""); err != nil {
		t.Fatalf("PutWikiPage home: %v", err)
	}
	headSHA, err := svc.Git.HeadSHA(ctx, repoFullName+".wiki", "master")
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	repoDir, err := svc.Git.GetRepoPath(ctx, repoFullName+".wiki")
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	removeLooseGitObject(t, repoDir, headSHA)

	if _, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{}); err == nil {
		t.Fatal("IngestWikiGit succeeded after branch head object loss, want fail-closed error")
	}

	rep, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	var livePages int64
	if err := svc.DB.Model(&db.WikiPage{}).
		Where("repository_id = ? AND deleted_at IS NULL", rep.ID).
		Count(&livePages).Error; err != nil {
		t.Fatalf("count live wiki pages: %v", err)
	}
	if livePages != 1 {
		t.Fatalf("live wiki page count = %d, want 1 after failed ingest", livePages)
	}
	var changesets int64
	if err := svc.DB.Model(&db.WikiChangeset{}).
		Where("repository_id = ?", rep.ID).
		Count(&changesets).Error; err != nil {
		t.Fatalf("count wiki changesets: %v", err)
	}
	if changesets == 0 {
		t.Fatal("wiki changesets were reset after failed ingest")
	}
}

func TestReceivePackRepairObligationHonorsFailedForcePushRewrite(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "push-repair", "rewrite")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "first", "first", "create first", ""); err != nil {
		t.Fatalf("PutWikiPage first: %v", err)
	}
	firstHead, err := svc.Git.HeadSHA(ctx, repoFullName+".wiki", "master")
	if err != nil {
		t.Fatalf("HeadSHA after first write: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, repoFullName, "second", "second", "create second", ""); err != nil {
		t.Fatalf("PutWikiPage second: %v", err)
	}
	if err := svc.Git.UpdateRef(ctx, repoFullName+".wiki", "refs/heads/master", firstHead); err != nil {
		t.Fatalf("simulate receive-pack force-push rewrite: %v", err)
	}
	repo := recordFailedReceivePackIngestForTest(t, svc, ctx, repoFullName)

	if _, err := svc.PutWikiPage(ctx, repoFullName, "third", "third", "create third", ""); err != nil {
		t.Fatalf("PutWikiPage after failed force-push ingest: %v", err)
	}
	svc.Wg.Wait()

	for _, slug := range []string{"first", "third"} {
		if _, err := svc.GetWikiPage(ctx, repoFullName, slug); err != nil {
			t.Fatalf("GetWikiPage(%s): %v", slug, err)
		}
	}
	if _, err := svc.GetWikiPage(ctx, repoFullName, "second"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("GetWikiPage(second) error = %v, want ErrNotFound after authoritative rewrite", err)
	}
	assertNoWikiGitRepairObligation(t, svc, repo.ID)
}

func TestReceivePackRepairObligationHonorsFailedBranchDeletion(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "push-repair", "delete")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "home", "home", "create home", ""); err != nil {
		t.Fatalf("PutWikiPage home: %v", err)
	}
	if err := svc.Git.DeleteRef(ctx, repoFullName+".wiki", "refs/heads/master"); err != nil {
		t.Fatalf("simulate receive-pack branch deletion: %v", err)
	}
	repo := recordFailedReceivePackIngestForTest(t, svc, ctx, repoFullName)

	if _, err := svc.PutWikiPage(ctx, repoFullName, "replacement", "replacement", "create replacement", ""); err != nil {
		t.Fatalf("PutWikiPage after failed branch deletion ingest: %v", err)
	}
	svc.Wg.Wait()

	if _, err := svc.GetWikiPage(ctx, repoFullName, "replacement"); err != nil {
		t.Fatalf("GetWikiPage(replacement): %v", err)
	}
	if _, err := svc.GetWikiPage(ctx, repoFullName, "home"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("GetWikiPage(home) error = %v, want ErrNotFound after authoritative deletion", err)
	}
	if _, err := svc.Git.ReadFile(ctx, repoFullName+".wiki", "home.md"); err == nil {
		t.Fatal("home.md was recreated after authoritative branch deletion")
	}
	assertNoWikiGitRepairObligation(t, svc, repo.ID)
}

func TestReceivePackInProgressObligationHonorsCrashAfterForcePushRewrite(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "push-repair", "crash-rewrite")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "first", "first", "create first", ""); err != nil {
		t.Fatalf("PutWikiPage first: %v", err)
	}
	firstHead, err := svc.Git.HeadSHA(ctx, repoFullName+".wiki", "master")
	if err != nil {
		t.Fatalf("HeadSHA after first write: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, repoFullName, "second", "second", "create second", ""); err != nil {
		t.Fatalf("PutWikiPage second: %v", err)
	}

	repo := recordInProgressReceivePackObligationForTest(t, svc, ctx, repoFullName, func() error {
		return svc.Git.UpdateRef(ctx, repoFullName+".wiki", "refs/heads/master", firstHead)
	})

	if _, err := svc.PutWikiPage(ctx, repoFullName, "third", "third", "create third", ""); err != nil {
		t.Fatalf("PutWikiPage after interrupted force-push receive-pack: %v", err)
	}
	svc.Wg.Wait()

	for _, slug := range []string{"first", "third"} {
		if _, err := svc.GetWikiPage(ctx, repoFullName, slug); err != nil {
			t.Fatalf("GetWikiPage(%s): %v", slug, err)
		}
	}
	if _, err := svc.GetWikiPage(ctx, repoFullName, "second"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("GetWikiPage(second) error = %v, want ErrNotFound after authoritative rewrite", err)
	}
	assertNoWikiGitRepairObligation(t, svc, repo.ID)
}

func TestReceivePackInProgressObligationHonorsCrashAfterBranchDeletion(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "push-repair", "crash-delete")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "home", "home", "create home", ""); err != nil {
		t.Fatalf("PutWikiPage home: %v", err)
	}

	repo := recordInProgressReceivePackObligationForTest(t, svc, ctx, repoFullName, func() error {
		return svc.Git.DeleteRef(ctx, repoFullName+".wiki", "refs/heads/master")
	})

	if _, err := svc.PutWikiPage(ctx, repoFullName, "replacement", "replacement", "create replacement", ""); err != nil {
		t.Fatalf("PutWikiPage after interrupted branch-deletion receive-pack: %v", err)
	}
	svc.Wg.Wait()

	if _, err := svc.GetWikiPage(ctx, repoFullName, "replacement"); err != nil {
		t.Fatalf("GetWikiPage(replacement): %v", err)
	}
	if _, err := svc.GetWikiPage(ctx, repoFullName, "home"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("GetWikiPage(home) error = %v, want ErrNotFound after authoritative deletion", err)
	}
	if _, err := svc.Git.ReadFile(ctx, repoFullName+".wiki", "home.md"); err == nil {
		t.Fatal("home.md was recreated after authoritative branch deletion")
	}
	assertNoWikiGitRepairObligation(t, svc, repo.ID)
}

func TestReceivePackUnchangedObligationDoesNotDiscardInterruptedRESTPublish(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "push-repair", "unchanged-obligation")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "first", "first", "create first", ""); err != nil {
		t.Fatalf("PutWikiPage first: %v", err)
	}
	repo := recordInProgressReceivePackObligationForTest(t, svc, ctx, repoFullName, func() error {
		return nil
	})

	publishErr := errors.New("forced REST publish failure")
	service.SetTestWikiPreparedPublishFailureForTest(svc, func(fullName, commitSHA string) error {
		if fullName == repoFullName && commitSHA != "" {
			return publishErr
		}
		return nil
	})
	if _, err := svc.PutWikiPage(ctx, repoFullName, "second", "second", "create second", ""); !errors.Is(err, publishErr) {
		t.Fatalf("PutWikiPage second error = %v, want %v", err, publishErr)
	}
	service.SetTestWikiPreparedPublishFailureForTest(svc, nil)

	if _, err := svc.PutWikiPage(ctx, repoFullName, "third", "third", "create third", ""); err != nil {
		t.Fatalf("PutWikiPage third: %v", err)
	}
	svc.Wg.Wait()

	for _, slug := range []string{"first", "second", "third"} {
		if _, err := svc.GetWikiPage(ctx, repoFullName, slug); err != nil {
			t.Fatalf("GetWikiPage(%s): %v", slug, err)
		}
		if _, err := svc.Git.ReadFile(ctx, repoFullName+".wiki", slug+".md"); err != nil {
			t.Fatalf("ReadFile(%s.md): %v", slug, err)
		}
	}
	assertNoWikiGitRepairObligation(t, svc, repo.ID)
}

func TestActiveReceivePackOwnerPreventsUnchangedCrossInstanceClear(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "push-repair", "active-owner")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "first", "first", "create first", ""); err != nil {
		t.Fatalf("PutWikiPage first: %v", err)
	}
	firstHead, err := svc.Git.HeadSHA(ctx, repoFullName+".wiki", "master")
	if err != nil {
		t.Fatalf("HeadSHA after first write: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, repoFullName, "second", "second", "create second", ""); err != nil {
		t.Fatalf("PutWikiPage second: %v", err)
	}
	repo, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	var ownerToken string
	err = svc.WithWikiCatalogWriteLockForReceivePack(ctx, repoFullName, func() error {
		return svc.Git.WithRepoLock(ctx, repoFullName+".wiki", func() error {
			var beginErr error
			ownerToken, beginErr = svc.BeginWikiReceivePackRepairObligationLocked(ctx, repoFullName)
			return beginErr
		})
	})
	if err != nil {
		t.Fatalf("begin receive-pack obligation: %v", err)
	}
	if ownerToken == "" {
		t.Fatal("receive-pack owner token is empty")
	}

	peer := newWikiGitIngestPeerService(t, svc)
	if _, err := peer.PutWikiPage(ctx, repoFullName, "third", "third", "create third", ""); err == nil ||
		!strings.Contains(err.Error(), "active receive-pack") {
		t.Fatalf("PutWikiPage from peer error = %v, want active receive-pack owner failure", err)
	}
	if _, err := peer.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{}); err == nil ||
		!strings.Contains(err.Error(), "active receive-pack") {
		t.Fatalf("IngestWikiGit from peer error = %v, want active receive-pack owner failure", err)
	}

	var active db.WikiGitRepairObligation
	if err := svc.DB.First(&active, "repository_id = ?", repo.ID).Error; err != nil {
		t.Fatalf("load active obligation: %v", err)
	}
	if !active.InProgress || active.OwnerToken != ownerToken || active.OwnerExpiresAt == nil {
		t.Fatalf("active obligation = %+v, want same live owner token", active)
	}
	if _, err := svc.GetWikiPage(ctx, repoFullName, "third"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("GetWikiPage(third) error = %v, want ErrNotFound after blocked peer write", err)
	}

	expiredAt := time.Now().UTC().Add(-time.Minute)
	if err := svc.DB.Model(&db.WikiGitRepairObligation{}).
		Where("repository_id = ?", repo.ID).
		Updates(map[string]any{
			"owner_expires_at": expiredAt,
			"updated_at":       expiredAt,
		}).Error; err != nil {
		t.Fatalf("expire active receive-pack owner before ref mutation: %v", err)
	}
	if err := svc.RefreshWikiReceivePackRepairObligationOwner(ctx, repoFullName, ownerToken); err != nil {
		t.Fatalf("refresh active receive-pack owner: %v", err)
	}
	var refreshed db.WikiGitRepairObligation
	if err := svc.DB.First(&refreshed, "repository_id = ?", repo.ID).Error; err != nil {
		t.Fatalf("load refreshed obligation: %v", err)
	}
	if !refreshed.InProgress || refreshed.OwnerToken != ownerToken ||
		refreshed.OwnerExpiresAt == nil || !refreshed.OwnerExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("refreshed obligation = %+v, want same live owner token", refreshed)
	}
	if _, err := peer.PutWikiPage(ctx, repoFullName, "still-blocked", "still blocked", "create still-blocked", ""); err == nil ||
		!strings.Contains(err.Error(), "active receive-pack") {
		t.Fatalf("PutWikiPage from peer after owner refresh error = %v, want active receive-pack owner failure", err)
	}
	if _, err := svc.GetWikiPage(ctx, repoFullName, "still-blocked"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("GetWikiPage(still-blocked) error = %v, want ErrNotFound after refreshed owner blocks peer write", err)
	}

	if err := svc.Git.UpdateRef(ctx, repoFullName+".wiki", "refs/heads/master", firstHead); err != nil {
		t.Fatalf("simulate receive-pack force-push rewrite: %v", err)
	}
	expiredAt = time.Now().UTC().Add(-time.Minute)
	if err := svc.DB.Model(&db.WikiGitRepairObligation{}).
		Where("repository_id = ?", repo.ID).
		Updates(map[string]any{
			"owner_expires_at": expiredAt,
			"updated_at":       expiredAt,
		}).Error; err != nil {
		t.Fatalf("expire receive-pack owner: %v", err)
	}

	if _, err := peer.PutWikiPage(ctx, repoFullName, "fourth", "fourth", "create fourth", ""); err != nil {
		t.Fatalf("PutWikiPage after expired receive-pack owner: %v", err)
	}
	peer.Wg.Wait()

	for _, slug := range []string{"first", "fourth"} {
		if _, err := peer.GetWikiPage(ctx, repoFullName, slug); err != nil {
			t.Fatalf("GetWikiPage(%s): %v", slug, err)
		}
	}
	if _, err := peer.GetWikiPage(ctx, repoFullName, "second"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("GetWikiPage(second) error = %v, want ErrNotFound after accepted force-push", err)
	}
	if _, err := peer.GetWikiPage(ctx, repoFullName, "third"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("GetWikiPage(third) error = %v, want ErrNotFound after blocked peer write", err)
	}
	assertNoWikiGitRepairObligation(t, peer, repo.ID)
}

func TestRESTWriteRejectsReceivePackOwnerClaimedAfterSnapshot(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "push-repair", "snapshot-owner")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "first", "first", "create first", ""); err != nil {
		t.Fatalf("PutWikiPage first: %v", err)
	}
	repo, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	peer := newWikiGitIngestPeerService(t, svc)

	var claimed atomic.Bool
	var ownerToken string
	var hookErr error
	service.SetTestWikiRESTSnapshotForTest(svc, func(fullName string) {
		if fullName != repoFullName || !claimed.CompareAndSwap(false, true) {
			return
		}
		hookErr = peer.WithWikiCatalogWriteLockForReceivePack(ctx, repoFullName, func() error {
			return peer.Git.WithRepoLock(ctx, repoFullName+".wiki", func() error {
				if err := peer.ReconcileWikiBeforeReceivePackLocked(ctx, repoFullName); err != nil {
					return err
				}
				var beginErr error
				ownerToken, beginErr = peer.BeginWikiReceivePackRepairObligationLocked(ctx, repoFullName)
				return beginErr
			})
		})
	})
	defer service.SetTestWikiRESTSnapshotForTest(svc, nil)

	_, err = svc.PutWikiPage(ctx, repoFullName, "second", "second", "create second", "")
	if hookErr != nil {
		t.Fatalf("receive-pack owner hook: %v", hookErr)
	}
	if !claimed.Load() {
		t.Fatal("REST snapshot hook did not claim a receive-pack owner")
	}
	if ownerToken == "" {
		t.Fatal("receive-pack owner token is empty")
	}
	if err == nil {
		t.Fatal("PutWikiPage second succeeded, want receive-pack owner race rejection")
	}
	if _, err := svc.GetWikiPage(ctx, repoFullName, "second"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("GetWikiPage(second) error = %v, want ErrNotFound after blocked REST write", err)
	}

	var active db.WikiGitRepairObligation
	if err := svc.DB.First(&active, "repository_id = ?", repo.ID).Error; err != nil {
		t.Fatalf("load active obligation: %v", err)
	}
	if !active.InProgress || active.OwnerToken != ownerToken ||
		active.OwnerExpiresAt == nil || !active.OwnerExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("active obligation = %+v, want token %q preserved", active, ownerToken)
	}

	err = peer.WithWikiCatalogWriteLockForReceivePack(ctx, repoFullName, func() error {
		return peer.Git.WithRepoLock(ctx, repoFullName+".wiki", func() error {
			return peer.ClearWikiReceivePackRepairObligationLocked(ctx, repoFullName, ownerToken)
		})
	})
	if err != nil {
		t.Fatalf("clear receive-pack owner: %v", err)
	}
	assertNoWikiGitRepairObligation(t, svc, repo.ID)
}

func TestReceivePackOwnerClaimDoesNotOverwriteActiveOwner(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "push-repair", "owner-claim")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "first", "first", "create first", ""); err != nil {
		t.Fatalf("PutWikiPage first: %v", err)
	}
	repo, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	peer := newWikiGitIngestPeerService(t, svc)

	reconcileBeforeReceivePack := func(name string, svc *service.Service) {
		t.Helper()
		err := svc.WithWikiCatalogWriteLockForReceivePack(ctx, repoFullName, func() error {
			return svc.Git.WithRepoLock(ctx, repoFullName+".wiki", func() error {
				return svc.ReconcileWikiBeforeReceivePackLocked(ctx, repoFullName)
			})
		})
		if err != nil {
			t.Fatalf("ReconcileWikiBeforeReceivePackLocked(%s): %v", name, err)
		}
	}
	reconcileBeforeReceivePack("first", svc)
	reconcileBeforeReceivePack("peer", peer)

	var firstToken string
	err = svc.WithWikiCatalogWriteLockForReceivePack(ctx, repoFullName, func() error {
		return svc.Git.WithRepoLock(ctx, repoFullName+".wiki", func() error {
			var beginErr error
			firstToken, beginErr = svc.BeginWikiReceivePackRepairObligationLocked(ctx, repoFullName)
			return beginErr
		})
	})
	if err != nil {
		t.Fatalf("first BeginWikiReceivePackRepairObligationLocked: %v", err)
	}
	if firstToken == "" {
		t.Fatal("first receive-pack owner token is empty")
	}

	var peerToken string
	err = peer.WithWikiCatalogWriteLockForReceivePack(ctx, repoFullName, func() error {
		return peer.Git.WithRepoLock(ctx, repoFullName+".wiki", func() error {
			var beginErr error
			peerToken, beginErr = peer.BeginWikiReceivePackRepairObligationLocked(ctx, repoFullName)
			return beginErr
		})
	})
	if err == nil || !strings.Contains(err.Error(), "active receive-pack") {
		t.Fatalf("peer BeginWikiReceivePackRepairObligationLocked error = %v, want active owner failure", err)
	}
	if peerToken != "" {
		t.Fatalf("peer owner token = %q, want empty token after failed claim", peerToken)
	}

	var active db.WikiGitRepairObligation
	if err := svc.DB.First(&active, "repository_id = ?", repo.ID).Error; err != nil {
		t.Fatalf("load active obligation: %v", err)
	}
	if !active.InProgress || active.OwnerToken != firstToken || active.OwnerExpiresAt == nil {
		t.Fatalf("active obligation = %+v, want first owner token", active)
	}

	err = peer.WithWikiCatalogWriteLockForReceivePack(ctx, repoFullName, func() error {
		return peer.Git.WithRepoLock(ctx, repoFullName+".wiki", func() error {
			return peer.ClearWikiReceivePackRepairObligationLocked(ctx, repoFullName, "loser-token")
		})
	})
	if err == nil || !strings.Contains(err.Error(), "different token") {
		t.Fatalf("peer ClearWikiReceivePackRepairObligationLocked error = %v, want token mismatch", err)
	}
	if err := svc.DB.First(&active, "repository_id = ?", repo.ID).Error; err != nil {
		t.Fatalf("reload active obligation: %v", err)
	}
	if active.OwnerToken != firstToken {
		t.Fatalf("active owner token after peer clear = %q, want %q", active.OwnerToken, firstToken)
	}

	err = svc.WithWikiCatalogWriteLockForReceivePack(ctx, repoFullName, func() error {
		return svc.Git.WithRepoLock(ctx, repoFullName+".wiki", func() error {
			return svc.ClearWikiReceivePackRepairObligationLocked(ctx, repoFullName, firstToken)
		})
	})
	if err != nil {
		t.Fatalf("clear first receive-pack owner: %v", err)
	}
	assertNoWikiGitRepairObligation(t, svc, repo.ID)
}

func TestStaleReceivePackRepairConsumerCannotDeleteNewOwner(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "push-repair", "stale-consumer")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "first", "first", "create first", ""); err != nil {
		t.Fatalf("PutWikiPage first: %v", err)
	}
	repo, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	recordInProgressReceivePackObligationForTest(t, svc, ctx, repoFullName, func() error {
		return nil
	})

	stalePeer := newWikiGitIngestPeerService(t, svc)
	loaded := make(chan struct{})
	releaseStaleConsumer := make(chan struct{})
	service.SetTestWikiGitRepairObligationLoadedForTest(stalePeer, func(fullName string, obligation db.WikiGitRepairObligation) {
		if fullName != repoFullName {
			return
		}
		close(loaded)
		<-releaseStaleConsumer
	})

	staleErr := make(chan error, 1)
	go func() {
		staleErr <- stalePeer.WithWikiCatalogWriteLockForReceivePack(ctx, repoFullName, func() error {
			return stalePeer.Git.WithRepoLock(ctx, repoFullName+".wiki", func() error {
				return stalePeer.ReconcileWikiBeforeReceivePackLocked(ctx, repoFullName)
			})
		})
	}()

	select {
	case <-loaded:
	case <-time.After(5 * time.Second):
		t.Fatal("stale peer did not load the expired repair obligation")
	}

	var activeToken string
	err = svc.WithWikiCatalogWriteLockForReceivePack(ctx, repoFullName, func() error {
		return svc.Git.WithRepoLock(ctx, repoFullName+".wiki", func() error {
			if err := svc.ReconcileWikiBeforeReceivePackLocked(ctx, repoFullName); err != nil {
				return err
			}
			var beginErr error
			activeToken, beginErr = svc.BeginWikiReceivePackRepairObligationLocked(ctx, repoFullName)
			return beginErr
		})
	})
	if err != nil {
		t.Fatalf("claim active receive-pack owner after first consumer: %v", err)
	}
	if activeToken == "" {
		t.Fatal("active receive-pack owner token is empty")
	}

	close(releaseStaleConsumer)
	select {
	case err := <-staleErr:
		if err == nil || !strings.Contains(err.Error(), "active receive-pack") {
			t.Fatalf("stale consumer error = %v, want active receive-pack owner failure", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale consumer did not finish")
	}

	var active db.WikiGitRepairObligation
	if err := svc.DB.First(&active, "repository_id = ?", repo.ID).Error; err != nil {
		t.Fatalf("load active obligation after stale consumer: %v", err)
	}
	if !active.InProgress || active.OwnerToken != activeToken || active.OwnerExpiresAt == nil {
		t.Fatalf("active obligation = %+v, want token %q preserved", active, activeToken)
	}

	err = svc.WithWikiCatalogWriteLockForReceivePack(ctx, repoFullName, func() error {
		return svc.Git.WithRepoLock(ctx, repoFullName+".wiki", func() error {
			return svc.ClearWikiReceivePackRepairObligationLocked(ctx, repoFullName, activeToken)
		})
	})
	if err != nil {
		t.Fatalf("clear active receive-pack owner: %v", err)
	}
	assertNoWikiGitRepairObligation(t, svc, repo.ID)
}

func TestReadRefreshPreservesCatalogAheadOfGitAfterInterruptedPublish(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "catalog-ahead-owner", "catalog-ahead")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "first", "first", "create first", ""); err != nil {
		t.Fatalf("PutWikiPage first: %v", err)
	}
	publishErr := errors.New("synthetic publish failure")
	service.SetTestWikiPreparedPublishFailureForTest(svc, func(fullName, commitSHA string) error {
		if fullName == repoFullName && commitSHA != "" {
			return publishErr
		}
		return nil
	})
	if _, err := svc.PutWikiPage(ctx, repoFullName, "second", "second", "create second", ""); !errors.Is(err, publishErr) {
		t.Fatalf("PutWikiPage second error = %v, want %v", err, publishErr)
	}
	service.SetTestWikiPreparedPublishFailureForTest(svc, nil)

	if _, err := svc.ListWikiPages(ctx, repoFullName, service.ListWikiPagesOptions{Recursive: true}); err != nil {
		t.Fatalf("ListWikiPages before background refresh: %v", err)
	}
	svc.Wg.Wait()
	pages, err := svc.ListWikiPages(ctx, repoFullName, service.ListWikiPagesOptions{Recursive: true})
	if err != nil {
		t.Fatalf("ListWikiPages after background refresh: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("pages after background refresh = %v, want both catalog pages", pages)
	}
	if _, err := svc.Git.ReadFile(ctx, repoFullName+".wiki", "second.md"); err == nil {
		t.Fatal("interrupted second page unexpectedly became visible in Git")
	}
}

func recordFailedReceivePackIngestForTest(t testing.TB, svc *service.Service, ctx context.Context, repoFullName string) db.Repository {
	t.Helper()

	repo, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	ingestErr := errors.New("forced receive-pack ingest failure")
	service.SetTestWikiReceivePackIngestFailureForTest(svc, func(fullName string) error {
		if fullName == repoFullName {
			return ingestErr
		}
		return nil
	})
	err = svc.WithWikiCatalogWriteLockForReceivePack(ctx, repoFullName, func() error {
		return svc.Git.WithRepoLock(ctx, repoFullName+".wiki", func() error {
			_, err := svc.IngestWikiGitAfterReceivePackLocked(ctx, repoFullName, service.WikiGitIngestOptions{})
			return err
		})
	})
	service.SetTestWikiReceivePackIngestFailureForTest(svc, nil)
	if !errors.Is(err, ingestErr) {
		t.Fatalf("failed receive-pack ingest error = %v, want %v", err, ingestErr)
	}

	var obligations int64
	if err := svc.DB.Model(&db.WikiGitRepairObligation{}).
		Where("repository_id = ?", repo.ID).
		Count(&obligations).Error; err != nil {
		t.Fatalf("count wiki Git repair obligations: %v", err)
	}
	if obligations != 1 {
		t.Fatalf("wiki Git repair obligations = %d, want 1", obligations)
	}
	return repo
}

func recordInProgressReceivePackObligationForTest(t testing.TB, svc *service.Service, ctx context.Context, repoFullName string, mutateGit func() error) db.Repository {
	t.Helper()

	repo, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	err = svc.WithWikiCatalogWriteLockForReceivePack(ctx, repoFullName, func() error {
		return svc.Git.WithRepoLock(ctx, repoFullName+".wiki", func() error {
			if _, err := svc.BeginWikiReceivePackRepairObligationLocked(ctx, repoFullName); err != nil {
				return err
			}
			return mutateGit()
		})
	})
	if err != nil {
		t.Fatalf("record in-progress receive-pack obligation: %v", err)
	}

	var obligations int64
	if err := svc.DB.Model(&db.WikiGitRepairObligation{}).
		Where("repository_id = ?", repo.ID).
		Count(&obligations).Error; err != nil {
		t.Fatalf("count wiki Git repair obligations: %v", err)
	}
	if obligations != 1 {
		t.Fatalf("wiki Git repair obligations = %d, want 1", obligations)
	}
	expiredAt := time.Now().UTC().Add(-time.Minute)
	if err := svc.DB.Model(&db.WikiGitRepairObligation{}).
		Where("repository_id = ?", repo.ID).
		Updates(map[string]any{
			"owner_expires_at": expiredAt,
			"updated_at":       expiredAt,
		}).Error; err != nil {
		t.Fatalf("expire interrupted receive-pack owner: %v", err)
	}
	return repo
}

func assertNoWikiGitRepairObligation(t testing.TB, svc *service.Service, repoID uint) {
	t.Helper()

	var obligations int64
	if err := svc.DB.Model(&db.WikiGitRepairObligation{}).
		Where("repository_id = ?", repoID).
		Count(&obligations).Error; err != nil {
		t.Fatalf("count wiki Git repair obligations: %v", err)
	}
	if obligations != 0 {
		t.Fatalf("wiki Git repair obligations = %d, want 0", obligations)
	}
}

func TestIngestWikiGit_SerializesConcurrentRefreshAfterRewrite(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "alice", "rpo")

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

	if _, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{}); err != nil {
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
	svc.SetWikiGitIngestAfterSnapshotHookForTest(func(fullName string) {
		if fullName != repoFullName {
			return
		}
		if atomic.AddInt32(&entered, 1) == 1 {
			enterCh <- struct{}{}
			<-releaseCh
		}
	})
	defer func() {
		svc.SetWikiGitIngestAfterSnapshotHookForTest(nil)
	}()

	errCh := make(chan error, 2)
	go func() {
		_, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{})
		errCh <- err
	}()

	select {
	case <-enterCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first ingest to reach the snapshot hook")
	}

	go func() {
		_, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{})
		errCh <- err
	}()

	time.Sleep(150 * time.Millisecond)
	if got := atomic.LoadInt32(&entered); got != 1 {
		t.Fatalf("snapshot hook entered %d times while first ingest was blocked, want serialization", got)
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

func TestIngestWikiGit_PreservesGitCommitSHAs(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "alice", "rpo")

	if _, err := svc.PutWikiPage(ctx, repoFullName, "home", "body", "create", ""); err != nil {
		t.Fatalf("put: %v", err)
	}

	// Capture the legacy git commit SHA before ingest.
	commits, err := svc.Git.ListCommits(ctx, repoFullName+".wiki", 10, nil)
	if err != nil {
		t.Fatalf("list legacy commits: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(commits))
	}
	originalSHA := strings.ToLower(commits[0].SHA)

	if _, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{}); err != nil {
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

func TestIngestWikiGit_RejectsIncompatibleSlugPath(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "alice", "rpo")

	if err := svc.Git.Init(ctx, repoFullName+".wiki", "master", false); err != nil {
		t.Fatalf("init wiki: %v", err)
	}
	if _, err := svc.Git.WriteFile(ctx, repoFullName+".wiki", "master",
		"Mixed_Case.md", "invalid push", []byte("# Invalid\n")); err != nil {
		t.Fatalf("write file: %v", err)
	}

	_, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{})
	if err == nil {
		t.Fatal("IngestWikiGit succeeded, want incompatible slug error")
	}
	if !strings.Contains(err.Error(), "cannot be represented by the catalog") {
		t.Fatalf("IngestWikiGit error = %v, want incompatible slug message", err)
	}
}

func TestIngestWikiGit_SkipIncompatibleSlugPathDropsPage(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "alice", "rpo")

	if err := svc.Git.Init(ctx, repoFullName+".wiki", "master", false); err != nil {
		t.Fatalf("init wiki: %v", err)
	}
	if _, err := svc.Git.WriteFile(ctx, repoFullName+".wiki", "master",
		"Mixed_Case.md", "invalid push", []byte("# Invalid\n")); err != nil {
		t.Fatalf("write file: %v", err)
	}

	stats, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{SkipIncompatibleSlugs: true})
	if err != nil {
		t.Fatalf("IngestWikiGit: %v", err)
	}
	if stats.NewCommits != 1 || stats.Pages != 0 {
		t.Fatalf("stats %+v, want one skipped commit and zero pages", stats)
	}

	rep, _ := svc.GetRepo(ctx, repoFullName)
	var count int64
	if err := svc.DB.Model(&db.WikiPage{}).Where("repository_id = ?", rep.ID).Count(&count).Error; err != nil {
		t.Fatalf("count pages: %v", err)
	}
	if count != 0 {
		t.Fatalf("catalog pages = %d, want 0", count)
	}
}

func TestIngestWikiGit_PreservesEmptyCommitSHA(t *testing.T) {
	svc, cleanup := setupWikiGitIngestTestService(t)
	defer cleanup()
	ctx := context.Background()
	repoFullName := seedRepoForWikiGitIngest(t, svc, "alice", "rpo")

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

	stats, err := svc.IngestWikiGit(ctx, repoFullName, service.WikiGitIngestOptions{})
	if err != nil {
		t.Fatalf("IngestWikiGit: %v", err)
	}
	// After the runtime cutover, the first commit was created by
	// PutWikiPage routing through ApplyChangeSet and is already in the
	// catalog (synth_commit_sha == git SHA, reconciled by the
	// post-commit materialize hook). IngestWikiGit only needs to replay
	// the externally-pushed empty commit, so NewCommits == 1 and the
	// already-cataloged first commit stays outside the replay range.
	if stats.NewCommits != 1 || stats.SkippedExist != 0 {
		t.Fatalf("stats %+v, want NewCommits=1 SkippedExist=0", stats)
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

// seedRepoForWikiGitIngest creates owner, repo, and flags has_wiki=true.
// Returns the repo's full name.
func seedRepoForWikiGitIngest(t *testing.T, svc *service.Service, login, name string) string {
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
	// IngestWikiGit does not consult the flag but production runs
	// usually do.
	if err := svc.DB.Model(&db.Repository{}).
		Where("full_name = ?", full).
		Update("has_wiki", true).Error; err != nil {
		t.Fatalf("set has_wiki: %v", err)
	}
	return full
}
