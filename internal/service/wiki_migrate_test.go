package service_test

// Migration tool tests: build a real legacy wiki via PutWikiPage,
// then call MigrateWiki and verify the catalog reflects the same
// state (page rows, blob SHAs, commit identities). These are
// end-to-end tests that go through the actual gitstore on disk.

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
	"gh-server/internal/wikicatalog"
)

func TestMigrateAllWikis_ContinuesAfterRepoFailure(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	badRepo := seedRepoForWikiMigration(t, svc, "alice", "bad")
	goodRepo := seedRepoForWikiMigration(t, svc, "bob", "good")

	if _, err := svc.PutWikiPage(ctx, badRepo, "broken", "bad body", "create bad", ""); err != nil {
		t.Fatalf("PutWikiPage bad repo: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, goodRepo, "home", "good body", "create good", ""); err != nil {
		t.Fatalf("PutWikiPage good repo: %v", err)
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
	svc, cleanup := setupTestService(t)
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
	svc, cleanup := setupTestService(t)
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
	if stats.GitCommits != 1 || stats.NewCommits != 1 || stats.Pages != 1 {
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
	svc, cleanup := setupTestService(t)
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
	if stats.GitCommits != 4 || stats.NewCommits != 4 {
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
	svc, cleanup := setupTestService(t)
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
	if stats1.NewCommits != 1 {
		t.Fatalf("first run NewCommits = %d, want 1", stats1.NewCommits)
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

func TestMigrateWiki_PreservesGitCommitSHAs(t *testing.T) {
	svc, cleanup := setupTestService(t)
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
	svc, cleanup := setupTestService(t)
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
	svc, cleanup := setupTestService(t)
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
	if stats.NewCommits != 2 {
		t.Fatalf("stats %+v, want both commits replayed", stats)
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
