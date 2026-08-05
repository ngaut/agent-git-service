package githttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/githttp"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/graphql"
	"github.com/ngaut/agent-git-service/internal/oauth"
	"github.com/ngaut/agent-git-service/internal/rest"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/router"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness/testdb"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

// skipIfNoBackend skips the test if git-http-backend is not available.
func skipIfNoBackend(t *testing.T) {
	t.Helper()
	candidates := []string{
		"/usr/lib/git-core/git-http-backend",
		"/usr/libexec/git-core/git-http-backend",
		"/opt/homebrew/libexec/git-core/git-http-backend",
	}
	if out, err := exec.Command("git", "--exec-path").Output(); err == nil {
		execPath := strings.TrimSpace(string(out))
		candidates = append([]string{filepath.Join(execPath, "git-http-backend")}, candidates...)
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return
		}
	}
	t.Skip("git-http-backend not found; skipping integration test")
}

// testEnv holds shared state for a single test case.
type testEnv struct {
	Server  *httptest.Server
	TmpDir  string
	DB      *gorm.DB
	Svc     *service.Service
	Store   *gitstore.Store
	RepoURL string // e.g. http://127.0.0.1:PORT/testowner/testrepo.git
}

// setupTestServer creates an isolated test environment with a seeded bare repo.
// When seed is true, the repo gets an initial README commit.
func setupTestServer(t *testing.T, owner, repo, defaultBranch string, seed bool) *testEnv {
	t.Helper()
	skipIfNoBackend(t)

	tmpDir := t.TempDir()
	gdb, dbCleanup := testdb.OpenRaw(t, "githttp")
	t.Cleanup(dbCleanup)
	if err := gdb.AutoMigrate(
		&db.User{}, &db.Team{}, &db.TeamMember{}, &db.TeamRepository{},
		&db.Repository{}, &db.RepoRedirect{}, &db.Token{}, &db.Label{}, &db.Workflow{}, &db.Collaborator{},
		&db.Webhook{}, &db.HookDelivery{}, &db.PullRequest{},
		&db.WikiPage{}, &db.WikiPageRevision{}, &db.WikiChangeset{}, &db.WikiRepoHead{},
		&db.WikiGitRepairObligation{}, &db.WikiPageLink{}, &db.WikiBlobRef{}, &db.WikiPendingBlob{},
		&db.WikiPageLabel{}, &db.WikiSearchDocument{},
		&db.WikiSearchProjectionTask{},
		&db.WikiPageIndex{}, &db.WikiIndexState{}, &db.WikiBacklink{}, &db.WikiPageHistory{},
	); err != nil {
		t.Fatalf("auto-migrate: %v", err)
	}

	gitStore, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}

	svc := &service.Service{
		DB:  gdb,
		Git: gitStore,
	}
	wikiBlob := wikicatalog.NewBlobStore(tmpDir)
	wikiCat := wikicatalog.New(gdb, wikiBlob)
	svc.WikiBlob = wikiBlob
	svc.WikiCatalog = wikiCat
	wikiCat.DBFor = svc.DBForCtx
	wikiCat.OnChangeSetCommitted = svc.WikiCatalogPostCommit

	gitHandler := githttp.New(gitStore, svc)
	restDeps := &rest.Deps{Svc: svc}
	gqlSrv := graphql.NewServer(svc)
	oauthHandler := oauth.New(svc)

	r := chi.NewRouter()
	mux := router.RegisterRoutes(r, restDeps, gitHandler, gqlSrv, oauthHandler, "http://console.localhost")

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	svc.BaseURL = ts.URL
	transform.Init(ts.URL)

	// Seed DB.
	user := db.User{Login: owner, Name: owner, Type: db.TypeUser}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	token := fmt.Sprintf("token-%s", owner)
	if err := gdb.Create(&db.Token{UserID: user.ID, Value: token}).Error; err != nil {
		t.Fatalf("create token: %v", err)
	}
	t.Setenv("GIT_HTTP_EXTRA_HEADER", "Authorization: token "+token)
	dbRepo := db.Repository{
		Name:          repo,
		FullName:      owner + "/" + repo,
		OwnerID:       user.ID,
		DefaultBranch: defaultBranch,
	}
	if err := gdb.Create(&dbRepo).Error; err != nil {
		t.Fatalf("create repo: %v", err)
	}

	// Initialize the bare repo on disk.
	ctx := context.Background()
	fullName := owner + "/" + repo
	if err := gitStore.Init(ctx, fullName, defaultBranch, seed); err != nil {
		t.Fatalf("gitStore.Init: %v", err)
	}

	repoURL := fmt.Sprintf("%s/%s/%s.git", ts.URL, owner, repo)
	return &testEnv{
		Server:  ts,
		TmpDir:  tmpDir,
		DB:      gdb,
		Svc:     svc,
		Store:   gitStore,
		RepoURL: repoURL,
	}
}

// runGit executes a git command and returns stdout. Fatals on non-zero exit.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	args = append([]string{"-c", "http.proxy=", "-c", "https.proxy="}, args...)
	if header := os.Getenv("GIT_HTTP_EXTRA_HEADER"); header != "" {
		args = append([]string{"-c", "http.extraHeader=" + header}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(gitTestBaseEnv(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\noutput: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func runGitAllowFailure(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	args = append([]string{"-c", "http.proxy=", "-c", "https.proxy="}, args...)
	if header := os.Getenv("GIT_HTTP_EXTRA_HEADER"); header != "" {
		args = append([]string{"-c", "http.extraHeader=" + header}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(gitTestBaseEnv(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runGitExpectFail executes a git command and expects it to fail.
// It returns the combined output so callers can assert on failure details.
func runGitExpectFail(t *testing.T, dir string, args ...string) string {
	t.Helper()
	args = append([]string{"-c", "http.proxy=", "-c", "https.proxy="}, args...)
	if header := os.Getenv("GIT_HTTP_EXTRA_HEADER"); header != "" {
		args = append([]string{"-c", "http.extraHeader=" + header}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(gitTestBaseEnv(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected git %s to fail, but it succeeded", strings.Join(args, " "))
	}
	return string(out)
}

func gitTestBaseEnv() []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		upper := strings.ToUpper(kv)
		if strings.HasPrefix(upper, "HTTP_PROXY=") ||
			strings.HasPrefix(upper, "HTTPS_PROXY=") ||
			strings.HasPrefix(upper, "ALL_PROXY=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"NO_PROXY=127.0.0.1,localhost",
		"no_proxy=127.0.0.1,localhost",
	)
	return env
}

func TestReceivePackDispatchesPushWebhook(t *testing.T) {
	env := setupTestServer(t, "pushhook", "repo", "main", true)

	var repo db.Repository
	if err := env.DB.First(&repo, "full_name = ?", "pushhook/repo").Error; err != nil {
		t.Fatalf("find repo: %v", err)
	}

	var hits atomic.Int64
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer receiver.Close()

	if err := env.DB.Create(&db.Webhook{
		RepositoryID: repo.ID,
		Name:         "web",
		Active:       true,
		EventsJSON:   `["push"]`,
		ConfigJSON:   `{"url":"` + receiver.URL + `","content_type":"json"}`,
	}).Error; err != nil {
		t.Fatalf("create webhook: %v", err)
	}

	cloneDir := filepath.Join(env.TmpDir, "pushhook-clone")
	runGit(t, env.TmpDir, "clone", env.RepoURL, cloneDir)
	if _, err := os.Stat(filepath.Join(cloneDir, "README.md")); err != nil {
		t.Fatalf("expected seeded README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cloneDir, "hook.txt"), []byte("push webhook\n"), 0o644); err != nil {
		t.Fatalf("write hook file: %v", err)
	}
	runGit(t, cloneDir, "add", "hook.txt")
	runGit(t, cloneDir, "commit", "-m", "push webhook test")
	runGit(t, cloneDir, "push", "origin", "main")

	delivery := waitForPushWebhookDelivery(t, env.DB, repo.ID, &hits)
	if delivery.Event != "push" {
		t.Fatalf("expected push delivery, got %q", delivery.Event)
	}
	if delivery.Status != "ok" {
		t.Fatalf("expected ok delivery status, got %q", delivery.Status)
	}

	var payload struct {
		Ref        string `json:"ref"`
		HeadCommit struct {
			ID       string   `json:"id"`
			URL      string   `json:"url"`
			Added    []string `json:"added"`
			Removed  []string `json:"removed"`
			Modified []string `json:"modified"`
		} `json:"head_commit"`
		Commits []struct {
			ID       string   `json:"id"`
			URL      string   `json:"url"`
			Added    []string `json:"added"`
			Removed  []string `json:"removed"`
			Modified []string `json:"modified"`
		} `json:"commits"`
	}
	if err := json.Unmarshal([]byte(delivery.RequestPayload), &payload); err != nil {
		t.Fatalf("unmarshal delivery payload: %v", err)
	}
	if payload.Ref != "refs/heads/main" {
		t.Fatalf("expected push ref refs/heads/main, got %q", payload.Ref)
	}
	if len(payload.Commits) != 1 {
		t.Fatalf("expected 1 commit in push payload, got %d", len(payload.Commits))
	}
	if payload.HeadCommit.ID == "" {
		t.Fatalf("expected head commit id to be populated")
	}
	expectedURL := fmt.Sprintf("https://%s/pushhook/repo/commit/%s", strings.TrimPrefix(env.Server.URL, "http://"), payload.HeadCommit.ID)
	if payload.HeadCommit.URL != expectedURL {
		t.Fatalf("expected head commit URL %q, got %q", expectedURL, payload.HeadCommit.URL)
	}
	if len(payload.HeadCommit.Added) != 1 || payload.HeadCommit.Added[0] != "hook.txt" {
		t.Fatalf("expected added files [hook.txt], got %#v", payload.HeadCommit.Added)
	}
	if len(payload.HeadCommit.Removed) != 0 {
		t.Fatalf("expected no removed files, got %#v", payload.HeadCommit.Removed)
	}
	if len(payload.HeadCommit.Modified) != 0 {
		t.Fatalf("expected no modified files, got %#v", payload.HeadCommit.Modified)
	}
}

func TestReceivePack_AllowsOrdinaryDotWikiRepositoryNames(t *testing.T) {
	env := setupTestServer(t, "dotwiki", "project.wiki", "main", true)

	cloneDir := filepath.Join(env.TmpDir, "dotwiki-clone")
	runGit(t, env.TmpDir, "clone", env.RepoURL, cloneDir)
	if err := os.WriteFile(filepath.Join(cloneDir, "dotwiki.txt"), []byte("ordinary repo\n"), 0o644); err != nil {
		t.Fatalf("write ordinary repo file: %v", err)
	}
	runGit(t, cloneDir, "add", "dotwiki.txt")
	runGit(t, cloneDir, "commit", "-m", "ordinary dotwiki push")
	runGit(t, cloneDir, "push", "origin", "main")

	headSHA, err := env.Store.HeadSHA(context.Background(), "dotwiki/project.wiki", "main")
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if headSHA == "" {
		t.Fatalf("expected pushed head sha")
	}
}

func TestReceivePack_DoesNotTreatRealDotWikiRepoAsParentWiki(t *testing.T) {
	env := setupTestServer(t, "dotwiki", "project.wiki", "main", true)

	var parentOwner db.User
	if err := env.DB.First(&parentOwner, "login = ?", "dotwiki").Error; err != nil {
		t.Fatalf("load parent owner: %v", err)
	}
	parentRepo := db.Repository{
		Name:          "project",
		FullName:      "dotwiki/project",
		OwnerID:       parentOwner.ID,
		DefaultBranch: "main",
		HasWiki:       true,
	}
	if err := env.DB.Create(&parentRepo).Error; err != nil {
		t.Fatalf("create parent repo: %v", err)
	}
	if err := env.Store.Init(context.Background(), parentRepo.FullName, parentRepo.DefaultBranch, true); err != nil {
		t.Fatalf("init parent repo: %v", err)
	}

	ingested := make(chan string, 1)
	env.Svc.SetWikiGitIngestAfterSnapshotHookForTest(func(repoFullName string) {
		select {
		case ingested <- repoFullName:
		default:
		}
	})
	defer env.Svc.SetWikiGitIngestAfterSnapshotHookForTest(nil)

	cloneDir := filepath.Join(env.TmpDir, "dotwiki-coexist-clone")
	runGit(t, env.TmpDir, "clone", env.RepoURL, cloneDir)
	if err := os.WriteFile(filepath.Join(cloneDir, "coexist.txt"), []byte("ordinary coexist repo\n"), 0o644); err != nil {
		t.Fatalf("write coexist file: %v", err)
	}
	runGit(t, cloneDir, "add", "coexist.txt")
	runGit(t, cloneDir, "commit", "-m", "ordinary dotwiki coexist push")
	runGit(t, cloneDir, "push", "origin", "main")

	select {
	case repoFullName := <-ingested:
		t.Fatalf("unexpected wiki ingest for ordinary .wiki repo push: %s", repoFullName)
	default:
	}

	headSHA, err := env.Store.HeadSHA(context.Background(), "dotwiki/project.wiki", "main")
	if err != nil {
		t.Fatalf("HeadSHA(dotwiki/project.wiki): %v", err)
	}
	if headSHA == "" {
		t.Fatalf("expected ordinary .wiki repo push head sha")
	}
	if env.Store.Exists(context.Background(), "dotwiki/project.wiki.wiki") {
		t.Fatalf("unexpected synthetic wiki repo created for ordinary .wiki repository")
	}
}

func TestReceivePack_WikiBackingRepoPushTriggersSynchronousIngest(t *testing.T) {
	env := setupTestServer(t, "wikio", "project", "main", true)

	ingested := make(chan string, 1)
	env.Svc.SetWikiGitIngestAfterSnapshotHookForTest(func(repoFullName string) {
		select {
		case ingested <- repoFullName:
		default:
		}
	})
	defer env.Svc.SetWikiGitIngestAfterSnapshotHookForTest(nil)

	wikiURL := fmt.Sprintf("%s/%s/%s.git", env.Server.URL, "wikio", "project.wiki")
	localDir := filepath.Join(env.TmpDir, "wiki-backing-local")
	runGit(t, env.TmpDir, "init", localDir)
	runGit(t, localDir, "checkout", "-b", "master")
	if err := os.WriteFile(filepath.Join(localDir, "home.md"), []byte("# Home\n\nWiki push\n"), 0o644); err != nil {
		t.Fatalf("write wiki page: %v", err)
	}
	runGit(t, localDir, "add", "home.md")
	runGit(t, localDir, "commit", "-m", "wiki backing push")
	runGit(t, localDir, "remote", "add", "origin", wikiURL)
	runGit(t, localDir, "push", "origin", "master")

	select {
	case repoFullName := <-ingested:
		if repoFullName != "wikio/project" {
			t.Fatalf("ingested repo = %q, want %q", repoFullName, "wikio/project")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for synchronous wiki ingest")
	}

	headSHA, err := env.Store.HeadSHA(context.Background(), "wikio/project.wiki", "master")
	if err != nil {
		t.Fatalf("HeadSHA(wikio/project.wiki): %v", err)
	}
	if headSHA == "" {
		t.Fatalf("expected wiki backing repo head sha after push")
	}
}

func TestReceivePack_WikiForcePushRebuildsCatalog(t *testing.T) {
	env := setupTestServer(t, "wikiforce", "project", "main", true)

	wikiURL := fmt.Sprintf("%s/%s/%s.git", env.Server.URL, "wikiforce", "project.wiki")
	localDir := filepath.Join(env.TmpDir, "wiki-force-push-local")
	runGit(t, env.TmpDir, "init", localDir)
	runGit(t, localDir, "checkout", "-b", "master")
	pagePath := filepath.Join(localDir, "home.md")
	if err := os.WriteFile(pagePath, []byte("# Home\n\nFirst version\n"), 0o644); err != nil {
		t.Fatalf("write first wiki page: %v", err)
	}
	runGit(t, localDir, "add", "home.md")
	runGit(t, localDir, "commit", "-m", "first wiki version")
	firstCommit := strings.TrimSpace(runGit(t, localDir, "rev-parse", "HEAD"))
	runGit(t, localDir, "remote", "add", "origin", wikiURL)
	runGit(t, localDir, "push", "origin", "master")

	if err := os.WriteFile(pagePath, []byte("# Home\n\nSecond version\n"), 0o644); err != nil {
		t.Fatalf("write second wiki page: %v", err)
	}
	runGit(t, localDir, "add", "home.md")
	runGit(t, localDir, "commit", "-m", "second wiki version")
	runGit(t, localDir, "push", "origin", "master")

	page, err := env.Svc.GetWikiPage(context.Background(), "wikiforce/project", "home")
	if err != nil {
		t.Fatalf("GetWikiPage after second push: %v", err)
	}
	if page.Body != "# Home\n\nSecond version\n" {
		t.Fatalf("page body after second push = %q", page.Body)
	}

	runGit(t, localDir, "reset", "--hard", firstCommit)
	runGit(t, localDir, "push", "--force", "origin", "master")

	page, err = env.Svc.GetWikiPage(context.Background(), "wikiforce/project", "home")
	if err != nil {
		t.Fatalf("GetWikiPage after force push: %v", err)
	}
	if page.Body != "# Home\n\nFirst version\n" {
		t.Fatalf("page body after force push = %q", page.Body)
	}
	history, total, err := env.Svc.ListWikiPageHistoryPage(context.Background(), "wikiforce/project", "home", 1, 10)
	if err != nil {
		t.Fatalf("ListWikiPageHistoryPage after force push: %v", err)
	}
	if total != 1 || len(history) != 1 {
		t.Fatalf("history after force push = %d entries (%d total), want 1", len(history), total)
	}
	if history[0].SHA != firstCommit {
		t.Fatalf("history SHA after force push = %q, want %q", history[0].SHA, firstCommit)
	}
}

func TestReceivePack_WikiBranchDeletionClearsCatalog(t *testing.T) {
	env := setupTestServer(t, "wikidelete", "project", "main", true)

	wikiURL := fmt.Sprintf("%s/%s/%s.git", env.Server.URL, "wikidelete", "project.wiki")
	localDir := filepath.Join(env.TmpDir, "wiki-delete-branch-local")
	runGit(t, env.TmpDir, "init", localDir)
	runGit(t, localDir, "checkout", "-b", "master")
	if err := os.WriteFile(filepath.Join(localDir, "home.md"), []byte("# Home\n"), 0o644); err != nil {
		t.Fatalf("write wiki page: %v", err)
	}
	runGit(t, localDir, "add", "home.md")
	runGit(t, localDir, "commit", "-m", "wiki page before branch deletion")
	runGit(t, localDir, "remote", "add", "origin", wikiURL)
	runGit(t, localDir, "push", "origin", "master")

	if _, err := env.Svc.GetWikiPage(context.Background(), "wikidelete/project", "home"); err != nil {
		t.Fatalf("GetWikiPage before branch deletion: %v", err)
	}
	runGit(t, localDir, "push", "origin", "--delete", "master")

	if _, err := env.Svc.GetWikiPage(context.Background(), "wikidelete/project", "home"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("GetWikiPage after branch deletion error = %v, want ErrNotFound", err)
	}
	pages, err := env.Svc.ListWikiPages(context.Background(), "wikidelete/project", service.ListWikiPagesOptions{})
	if err != nil {
		t.Fatalf("ListWikiPages after branch deletion: %v", err)
	}
	if len(pages) != 0 {
		t.Fatalf("ListWikiPages after branch deletion returned %d pages, want 0", len(pages))
	}
}

func TestReceivePack_WikiIngestFailureReturnsErrorAndNextRESTWriteRecovers(t *testing.T) {
	env := setupTestServer(t, "wikif", "project", "main", true)

	ingestErr := errors.New("forced wiki receive-pack ingest failure")
	service.SetTestWikiReceivePackIngestFailureForTest(env.Svc, func(repoFullName string) error {
		if repoFullName == "wikif/project" {
			return ingestErr
		}
		return nil
	})
	defer service.SetTestWikiReceivePackIngestFailureForTest(env.Svc, nil)

	wikiURL := fmt.Sprintf("%s/%s/%s.git", env.Server.URL, "wikif", "project.wiki")
	localDir := filepath.Join(env.TmpDir, "wiki-failed-ingest-local")
	runGit(t, env.TmpDir, "init", localDir)
	runGit(t, localDir, "checkout", "-b", "master")
	if err := os.WriteFile(filepath.Join(localDir, "pushed.md"), []byte("# Pushed\n"), 0o644); err != nil {
		t.Fatalf("write wiki page: %v", err)
	}
	runGit(t, localDir, "add", "pushed.md")
	runGit(t, localDir, "commit", "-m", "wiki push with failed ingest")
	runGit(t, localDir, "remote", "add", "origin", wikiURL)
	if output, err := runGitAllowFailure(t, localDir, "push", "origin", "master"); err == nil {
		t.Fatalf("push succeeded despite ingest failure: %s", output)
	}

	if _, err := env.Store.HeadSHA(context.Background(), "wikif/project.wiki", "master"); err != nil {
		t.Fatalf("wiki ref did not advance before forced ingest failure: %v", err)
	}
	var repo db.Repository
	if err := env.DB.First(&repo, "full_name = ?", "wikif/project").Error; err != nil {
		t.Fatalf("load parent repo: %v", err)
	}
	var pushedBefore int64
	if err := env.DB.Model(&db.WikiPage{}).
		Where("repository_id = ? AND slug = ? AND deleted_at IS NULL", repo.ID, "pushed").
		Count(&pushedBefore).Error; err != nil {
		t.Fatalf("count pushed page before recovery: %v", err)
	}
	if pushedBefore != 0 {
		t.Fatalf("pushed page count before recovery = %d, want 0", pushedBefore)
	}

	service.SetTestWikiReceivePackIngestFailureForTest(env.Svc, nil)
	if _, err := env.Svc.PutWikiPage(context.Background(), "wikif/project", "rest", "# Rest\n", "create rest", ""); err != nil {
		t.Fatalf("PutWikiPage after failed receive-pack ingest: %v", err)
	}
	env.Svc.Wg.Wait()

	for slug := range map[string]struct{}{"pushed": {}, "rest": {}} {
		if _, err := env.Svc.GetWikiPage(context.Background(), "wikif/project", slug); err != nil {
			t.Fatalf("GetWikiPage(%s): %v", slug, err)
		}
	}
}

func TestReceivePack_WikiPreBackendServeErrorClearsRepairOwner(t *testing.T) {
	env := setupTestServer(t, "wikiclean", "project", "main", true)

	badSpoolPath := filepath.Join(env.TmpDir, "not-a-directory")
	if err := os.WriteFile(badSpoolPath, []byte("not a directory"), 0644); err != nil {
		t.Fatalf("write bad spool path: %v", err)
	}
	t.Setenv("GITHTTP_SPOOL_DIR", badSpoolPath)

	var owner db.User
	if err := env.DB.First(&owner, "login = ?", "wikiclean").Error; err != nil {
		t.Fatalf("load owner: %v", err)
	}
	handler := githttp.New(env.Store, env.Svc)
	req := newRequestWithParams(
		http.MethodPost,
		"http://example.com/wikiclean/project.wiki.git/git-receive-pack",
		strings.NewReader("not a git receive-pack request"),
		map[string]string{"owner": "wikiclean", "repo": "project.wiki.git"},
	)
	req = req.WithContext(service.ContextWithUser(req.Context(), owner))
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	req.TransferEncoding = []string{"chunked"}
	req.ContentLength = -1

	rr := httptest.NewRecorder()
	handler.ReceivePack(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d", rr.Code, http.StatusInternalServerError)
	}
	var obligations int64
	if err := env.DB.Model(&db.WikiGitRepairObligation{}).Count(&obligations).Error; err != nil {
		t.Fatalf("count repair obligations: %v", err)
	}
	if obligations != 0 {
		t.Fatalf("repair obligations after pre-backend serve error = %d, want 0", obligations)
	}
	if _, err := env.Svc.PutWikiPage(context.Background(), "wikiclean/project", "home", "# Home\n", "create home", ""); err != nil {
		t.Fatalf("PutWikiPage after pre-backend serve error: %v", err)
	}
}

func waitForPushWebhookDelivery(t *testing.T, database *gorm.DB, repoID uint, hits *atomic.Int64) db.HookDelivery {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastDelivery db.HookDelivery
	for time.Now().Before(deadline) {
		var delivery db.HookDelivery
		err := database.Where("repository_id = ?", repoID).First(&delivery).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("find delivery: %v", err)
		}
		if err == nil {
			lastDelivery = delivery
			if hits.Load() == 1 && delivery.Status != "pending" {
				return delivery
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected completed push webhook delivery, got hits=%d delivery=%#v", hits.Load(), lastDelivery)
	return db.HookDelivery{}
}

func TestReceivePackDispatchesPushWebhookForNewBranchWithoutAncestorHistory(t *testing.T) {
	env := setupTestServer(t, "pushhook", "repo", "main", true)

	var repo db.Repository
	if err := env.DB.First(&repo, "full_name = ?", "pushhook/repo").Error; err != nil {
		t.Fatalf("find repo: %v", err)
	}

	var hits atomic.Int64
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer receiver.Close()

	if err := env.DB.Create(&db.Webhook{
		RepositoryID: repo.ID,
		Name:         "web",
		Active:       true,
		EventsJSON:   `["push"]`,
		ConfigJSON:   `{"url":"` + receiver.URL + `","content_type":"json"}`,
	}).Error; err != nil {
		t.Fatalf("create webhook: %v", err)
	}

	cloneDir := filepath.Join(env.TmpDir, "pushhook-feature-clone")
	runGit(t, env.TmpDir, "clone", env.RepoURL, cloneDir)
	runGit(t, cloneDir, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(cloneDir, "feature.txt"), []byte("feature branch push\n"), 0o644); err != nil {
		t.Fatalf("write feature file: %v", err)
	}
	runGit(t, cloneDir, "add", "feature.txt")
	runGit(t, cloneDir, "commit", "-m", "feature branch push")
	runGit(t, cloneDir, "push", "origin", "feature")

	delivery := waitForPushWebhookDelivery(t, env.DB, repo.ID, &hits)
	if delivery.Status != "ok" {
		t.Fatalf("expected ok delivery status, got %q", delivery.Status)
	}

	var payload struct {
		Ref        string `json:"ref"`
		Before     string `json:"before"`
		HeadCommit struct {
			ID    string   `json:"id"`
			Added []string `json:"added"`
		} `json:"head_commit"`
		Commits []struct {
			ID    string   `json:"id"`
			Added []string `json:"added"`
		} `json:"commits"`
	}
	if err := json.Unmarshal([]byte(delivery.RequestPayload), &payload); err != nil {
		t.Fatalf("unmarshal delivery payload: %v", err)
	}
	if payload.Ref != "refs/heads/feature" {
		t.Fatalf("expected push ref refs/heads/feature, got %q", payload.Ref)
	}
	if payload.Before != strings.Repeat("0", 40) {
		t.Fatalf("expected zero before SHA for new branch push, got %q", payload.Before)
	}
	if len(payload.Commits) != 1 {
		t.Fatalf("expected only the pushed branch-tip commit, got %d commits", len(payload.Commits))
	}
	if payload.HeadCommit.ID == "" || payload.HeadCommit.ID != payload.Commits[0].ID {
		t.Fatalf("expected head commit to match the single pushed commit")
	}
	if len(payload.HeadCommit.Added) != 1 || payload.HeadCommit.Added[0] != "feature.txt" {
		t.Fatalf("expected added files [feature.txt], got %#v", payload.HeadCommit.Added)
	}
}

// waitFor polls condFn at interval until it returns true or timeout elapses.
func waitFor(t *testing.T, timeout, interval time.Duration, condFn func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condFn() {
			return
		}
		time.Sleep(interval)
	}
	t.Fatalf("waitFor timed out: %s", msg)
}

type stubStore struct {
	exists      bool
	initErr     error
	repoPath    string
	repoPathErr error
	root        string
	rootErr     error
}

func (s *stubStore) Exists(ctx context.Context, fullName string) bool {
	return s.exists
}

func (s *stubStore) Init(ctx context.Context, fullName, defaultBranch string, seed bool) error {
	return s.initErr
}

func (s *stubStore) GetRepoPath(ctx context.Context, fullName string) (string, error) {
	if s.repoPathErr != nil {
		return "", s.repoPathErr
	}
	return s.repoPath, nil
}

func (s *stubStore) RepoRoot(ctx context.Context) (string, error) {
	if s.rootErr != nil {
		return "", s.rootErr
	}
	return s.root, nil
}

func (s *stubStore) WithRepoLock(ctx context.Context, fullName string, fn func() error) error {
	return fn()
}

var _ githttp.Store = (*stubStore)(nil)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, dbCleanup := testdb.OpenRaw(t, "githttp")
	t.Cleanup(dbCleanup)
	if err := gdb.AutoMigrate(
		&db.User{}, &db.Team{}, &db.TeamMember{}, &db.TeamRepository{},
		&db.Repository{}, &db.RepoRedirect{}, &db.Token{}, &db.Label{}, &db.Workflow{}, &db.Collaborator{},
	); err != nil {
		t.Fatalf("auto-migrate: %v", err)
	}
	return gdb
}

func seedUserRepo(t *testing.T, gdb *gorm.DB, ownerLogin, repoName, fullName, defaultBranch string) (db.User, db.Repository) {
	t.Helper()
	user := db.User{Login: ownerLogin, Name: ownerLogin, Type: db.TypeUser}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	repo := db.Repository{
		Name:          repoName,
		FullName:      fullName,
		OwnerID:       user.ID,
		DefaultBranch: defaultBranch,
	}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("create repo: %v", err)
	}
	return user, repo
}

func newRequestWithParams(method, target string, body io.Reader, params map[string]string) *http.Request {
	req := httptest.NewRequest(method, target, body)
	rctx := chi.NewRouteContext()
	for key, val := range params {
		rctx.URLParams.Add(key, val)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// ---------- Tests ----------

func TestInfoRefs_LsRemote(t *testing.T) {
	env := setupTestServer(t, "testowner", "testrepo", "main", true)

	t.Run("existing_repo", func(t *testing.T) {
		out := runGit(t, t.TempDir(), "ls-remote", env.RepoURL)
		if !strings.Contains(out, "refs/heads/main") {
			t.Errorf("expected refs/heads/main in ls-remote output, got:\n%s", out)
		}
	})

	t.Run("not_found", func(t *testing.T) {
		badURL := fmt.Sprintf("%s/ghost/missing.git", env.Server.URL)
		out := runGitExpectFail(t, t.TempDir(), "ls-remote", badURL)
		// Git should report an error when the repo doesn't exist.
		// Error may be in English ("404", "not found") or other locales.
		if out == "" {
			t.Error("expected error output for missing repo, got empty string")
		}
	})
}

func TestClone(t *testing.T) {
	env := setupTestServer(t, "testowner", "testrepo", "main", true)
	cloneDir := filepath.Join(t.TempDir(), "clone")

	runGit(t, t.TempDir(), "clone", env.RepoURL, cloneDir)

	readmePath := filepath.Join(cloneDir, "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("README.md not found in clone: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty auto-init README content, got: %q", data)
	}
}

func TestClone_LiteralPercentRepoName(t *testing.T) {
	env := setupTestServer(t, "testowner", "foo%20bar", "main", true)
	cloneDir := filepath.Join(t.TempDir(), "clone")
	repoURL := fmt.Sprintf("%s/%s/%s.git", env.Server.URL, "testowner", url.PathEscape("foo%20bar"))

	runGit(t, t.TempDir(), "clone", repoURL, cloneDir)

	readmePath := filepath.Join(cloneDir, "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("README.md not found in clone: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty auto-init README content, got: %q", data)
	}
}

func TestFetch_AfterPush(t *testing.T) {
	env := setupTestServer(t, "testowner", "testrepo", "main", true)

	// Clone into two separate working copies.
	clone1 := filepath.Join(t.TempDir(), "clone1")
	runGit(t, t.TempDir(), "clone", env.RepoURL, clone1)

	clone2 := filepath.Join(t.TempDir(), "clone2")
	runGit(t, t.TempDir(), "clone", env.RepoURL, clone2)

	// Record clone2's HEAD before fetch so we can assert it advances.
	headBefore := strings.TrimSpace(runGit(t, clone2, "rev-parse", "HEAD"))

	// Create and push a new file from clone1.
	newFile := filepath.Join(clone1, "newfile.txt")
	if err := os.WriteFile(newFile, []byte("hello from clone1"), 0644); err != nil {
		t.Fatalf("write newfile.txt: %v", err)
	}
	runGit(t, clone1, "add", "newfile.txt")
	runGit(t, clone1, "commit", "-m", "add newfile")
	runGit(t, clone1, "push", "origin", "main")

	// Fetch from clone2 (the actual fetch path under test).
	runGit(t, clone2, "fetch", "origin")

	// Verify remote tracking ref advanced past the pre-fetch HEAD.
	headAfterFetch := strings.TrimSpace(runGit(t, clone2, "rev-parse", "refs/remotes/origin/main"))
	if headAfterFetch == headBefore {
		t.Fatalf("fetch did not advance origin/main; still at %s", headBefore)
	}

	// Merge the fetched changes and verify the new file is present.
	runGit(t, clone2, "merge", "origin/main")

	data, err := os.ReadFile(filepath.Join(clone2, "newfile.txt"))
	if err != nil {
		t.Fatalf("newfile.txt not found in clone2 after fetch+merge: %v", err)
	}
	if string(data) != "hello from clone1" {
		t.Errorf("expected 'hello from clone1', got: %s", data)
	}
}

func TestPush_FixHEAD(t *testing.T) {
	// Create repo with default branch "main" but NO seed commit (empty bare repo).
	env := setupTestServer(t, "testowner", "testrepo", "main", false)

	// Create a local repo, commit a file, and push to "master" (not "main").
	localDir := filepath.Join(t.TempDir(), "local")
	runGit(t, t.TempDir(), "init", localDir)
	runGit(t, localDir, "checkout", "-b", "master")

	f := filepath.Join(localDir, "file.txt")
	if err := os.WriteFile(f, []byte("data"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, localDir, "add", "file.txt")
	runGit(t, localDir, "commit", "-m", "initial on master")
	runGit(t, localDir, "remote", "add", "origin", env.RepoURL)
	runGit(t, localDir, "push", "origin", "master")

	// The bare repo's HEAD was refs/heads/main (which doesn't exist).
	// fixHEAD should correct it to refs/heads/master.
	bareRepoPath := filepath.Join(env.TmpDir, "testowner", "testrepo.git")
	waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		out, err := exec.Command("git", "-C", bareRepoPath, "symbolic-ref", "HEAD").Output()
		if err != nil {
			return false
		}
		return strings.TrimSpace(string(out)) == "refs/heads/master"
	}, "fixHEAD did not correct HEAD to refs/heads/master")
}

func TestPush_SyncWorkflows(t *testing.T) {
	env := setupTestServer(t, "testowner", "testrepo", "main", true)

	cloneDir := filepath.Join(t.TempDir(), "clone")
	runGit(t, t.TempDir(), "clone", env.RepoURL, cloneDir)

	// Create a workflow file.
	wfDir := filepath.Join(cloneDir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0755); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	wfContent := "name: CI\non: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n"
	if err := os.WriteFile(filepath.Join(wfDir, "ci.yml"), []byte(wfContent), 0644); err != nil {
		t.Fatalf("write ci.yml: %v", err)
	}
	runGit(t, cloneDir, "add", ".github/workflows/ci.yml")
	runGit(t, cloneDir, "commit", "-m", "add CI workflow")
	runGit(t, cloneDir, "push", "origin", "main")

	// Get the repo ID from DB.
	var repo db.Repository
	if err := env.DB.First(&repo, "full_name = ?", "testowner/testrepo").Error; err != nil {
		t.Fatalf("find repo: %v", err)
	}

	// Poll for the workflow row.
	waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		var wf db.Workflow
		err := env.DB.Where("repository_id = ? AND path = ?", repo.ID, ".github/workflows/ci.yml").First(&wf).Error
		return err == nil
	}, "SyncWorkflowsFromRepo did not create workflow row for ci.yml")

	// Verify the workflow name was extracted from YAML.
	var wf db.Workflow
	if err := env.DB.Where("repository_id = ? AND path = ?", repo.ID, ".github/workflows/ci.yml").First(&wf).Error; err != nil {
		t.Fatalf("query workflow: %v", err)
	}
	if wf.Name != "CI" {
		t.Errorf("expected workflow name 'CI', got '%s'", wf.Name)
	}
}

// TestRouting_GitHTTPEndpoints verifies that the production router wiring
// exposes the expected Git Smart HTTP endpoints.  If someone removes or
// renames a route in router.RegisterRoutes, this test will fail.
func TestRouting_GitHTTPEndpoints(t *testing.T) {
	env := setupTestServer(t, "testowner", "testrepo", "main", true)

	tests := []struct {
		method string
		path   string
		want   int
	}{
		{"GET", "/testowner/testrepo.git/info/refs?service=git-upload-pack", http.StatusOK},
		{"POST", "/testowner/testrepo.git/git-upload-pack", http.StatusUnsupportedMediaType},
		{"POST", "/testowner/testrepo.git/git-receive-pack", http.StatusUnsupportedMediaType},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, env.Server.URL+tt.path, nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Header.Set("Authorization", "token token-testowner")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do request: %v", err)
			}
			resp.Body.Close()

			if resp.StatusCode != tt.want {
				t.Errorf("route %s %s returned %d; want %d", tt.method, tt.path, resp.StatusCode, tt.want)
			}
		})
	}
}

// TestEnsureRepo_MissingRepo tests ensureRepo behavior when repo is not in DB.
func TestEnsureRepo_MissingRepo(t *testing.T) {
	env := setupTestServer(t, "testowner", "testrepo", "main", true)

	t.Run("info_refs_not_in_db_returns_404", func(t *testing.T) {
		// Request a repo that doesn't exist in DB at all.
		badURL := fmt.Sprintf("%s/ghost/unknown.git/info/refs?service=git-upload-pack", env.Server.URL)
		out := runGitExpectFail(t, t.TempDir(), "ls-remote", badURL)
		// Should get an error (404 or "not found" or "未找到" in Chinese locale).
		// The key assertion is that the command fails, not the specific error text.
		if out == "" {
			t.Error("expected error output for missing repo, got empty string")
		}
		t.Logf("Missing repo error (expected): %s", out)
	})

	t.Run("upload_pack_not_in_db_returns_404", func(t *testing.T) {
		// Clone should fail with 404 for non-existent repo.
		badURL := fmt.Sprintf("%s/ghost/unknown.git", env.Server.URL)
		cloneDir := filepath.Join(t.TempDir(), "clone-bad")
		out := runGitExpectFail(t, t.TempDir(), "clone", badURL, cloneDir)
		if !strings.Contains(out, "404") && !strings.Contains(strings.ToLower(out), "not found") {
			t.Errorf("expected 404 or 'not found' for missing repo, got:\n%s", out)
		}
	})
}

// TestEnsureRepo_AutoInit tests auto-init behavior when repo is in DB but not on disk.
func TestEnsureRepo_AutoInit(t *testing.T) {
	tmpDir := t.TempDir()
	gdb, dbCleanup := testdb.OpenRaw(t, "githttp")
	t.Cleanup(dbCleanup)
	if err := gdb.AutoMigrate(&db.User{}, &db.Repository{}, &db.RepoRedirect{}, &db.Label{}, &db.Workflow{}); err != nil {
		t.Fatalf("auto-migrate: %v", err)
	}

	gitStore, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}

	svc := &service.Service{DB: gdb, Git: gitStore}

	// Seed DB but NOT the on-disk repo.
	user := db.User{Login: "autoowner", Name: "autoowner", Type: db.TypeUser}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	dbRepo := db.Repository{
		Name:          "autorepo",
		FullName:      "autoowner/autorepo",
		OwnerID:       user.ID,
		DefaultBranch: "main",
	}
	if err := gdb.Create(&dbRepo).Error; err != nil {
		t.Fatalf("create repo: %v", err)
	}

	gitHandler := githttp.New(gitStore, svc)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := service.ContextWithUser(r.Context(), user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	// Use the actual InfoRefs handler which calls ensureRepo internally.
	r.Get("/{owner}/{repo}.git/info/refs", gitHandler.InfoRefs)

	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)

	// Verify repo doesn't exist on disk yet.
	ctx := context.Background()
	if gitStore.Exists(ctx, "autoowner/autorepo") {
		t.Fatal("repo should not exist on disk before auto-init")
	}

	// Trigger auto-init via HTTP request to info/refs.
	req, err := http.NewRequest("GET", ts.URL+"/autoowner/autorepo.git/info/refs?service=git-upload-pack", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	resp.Body.Close()

	// After the request, the repo should exist on disk (auto-initialized).
	if !gitStore.Exists(ctx, "autoowner/autorepo") {
		t.Error("expected repo to be auto-initialized on disk after HTTP request")
	}
}

// TestUploadPack_FailurePaths tests UploadPack failure scenarios.
func TestUploadPack_FailurePaths(t *testing.T) {
	env := setupTestServer(t, "testowner", "testrepo", "main", true)

	t.Run("invalid_request_body", func(t *testing.T) {
		// Send invalid POST body to upload-pack endpoint.
		url := fmt.Sprintf("%s/testowner/testrepo.git/git-upload-pack", env.Server.URL)
		req, err := http.NewRequest("POST", url, strings.NewReader("invalid-git-protocol-data"))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Authorization", "token token-testowner")
		req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do request: %v", err)
		}
		defer resp.Body.Close()
		// git-http-backend should handle invalid input gracefully.
		// We expect either 200 (with error in body) or an error status.
		t.Logf("UploadPack with invalid body returned status: %d", resp.StatusCode)
	})
}

func TestUploadPack_ChunkedRequest(t *testing.T) {
	env := setupTestServer(t, "testowner", "testrepo", "main", true)

	backendDir := t.TempDir()
	backendPath := filepath.Join(backendDir, "git-http-backend")
	script := "#!/bin/sh\n" +
		"body=$(cat)\n" +
		"len=$(printf \"%s\" \"$body\" | wc -c | tr -d ' \\n')\n" +
		"printf \"Status: 200 OK\\r\\n\"\n" +
		"printf \"Content-Type: text/plain\\r\\n\"\n" +
		"printf \"\\r\\n\"\n" +
		"printf \"content_length=%s\\n\" \"$CONTENT_LENGTH\"\n" +
		"printf \"body_length=%s\\n\" \"$len\"\n"
	if err := os.WriteFile(backendPath, []byte(script), 0755); err != nil {
		t.Fatalf("write stub backend: %v", err)
	}
	if err := os.Chmod(backendPath, 0755); err != nil {
		t.Fatalf("chmod stub backend: %v", err)
	}

	t.Setenv("GIT_EXEC_PATH", backendDir)

	body := "chunked-body"
	pr, pw := io.Pipe()
	req, err := http.NewRequest("POST", env.Server.URL+"/testowner/testrepo.git/git-upload-pack", pr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "token token-testowner")
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	req.TransferEncoding = []string{"chunked"}

	go func() {
		_, _ = io.WriteString(pw, body)
		_ = pw.Close()
	}()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chunked request returned %d; want %d", resp.StatusCode, http.StatusOK)
	}

	got := string(respBody)
	wantLen := fmt.Sprintf("content_length=%d", len(body))
	if !strings.Contains(got, wantLen) {
		t.Fatalf("expected %q in response, got:\n%s", wantLen, got)
	}
	wantBodyLen := fmt.Sprintf("body_length=%d", len(body))
	if !strings.Contains(got, wantBodyLen) {
		t.Fatalf("expected %q in response, got:\n%s", wantBodyLen, got)
	}
}

func TestReceivePack_ChunkedRequest(t *testing.T) {
	env := setupTestServer(t, "testowner", "testrepo", "main", true)

	backendDir := t.TempDir()
	backendPath := filepath.Join(backendDir, "git-http-backend")
	script := "#!/bin/sh\n" +
		"body=$(cat)\n" +
		"len=$(printf \"%s\" \"$body\" | wc -c | tr -d ' \\n')\n" +
		"printf \"Status: 200 OK\\r\\n\"\n" +
		"printf \"Content-Type: text/plain\\r\\n\"\n" +
		"printf \"\\r\\n\"\n" +
		"printf \"content_length=%s\\n\" \"$CONTENT_LENGTH\"\n" +
		"printf \"body_length=%s\\n\" \"$len\"\n"
	if err := os.WriteFile(backendPath, []byte(script), 0755); err != nil {
		t.Fatalf("write stub backend: %v", err)
	}
	if err := os.Chmod(backendPath, 0755); err != nil {
		t.Fatalf("chmod stub backend: %v", err)
	}

	t.Setenv("GIT_EXEC_PATH", backendDir)

	body := "chunked-body"
	pr, pw := io.Pipe()
	req, err := http.NewRequest("POST", env.Server.URL+"/testowner/testrepo.git/git-receive-pack", pr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "token token-testowner")
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	req.TransferEncoding = []string{"chunked"}

	go func() {
		_, _ = io.WriteString(pw, body)
		_ = pw.Close()
	}()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chunked request returned %d; want %d", resp.StatusCode, http.StatusOK)
	}

	got := string(respBody)
	wantLen := fmt.Sprintf("content_length=%d", len(body))
	if !strings.Contains(got, wantLen) {
		t.Fatalf("expected %q in response, got:\n%s", wantLen, got)
	}
	wantBodyLen := fmt.Sprintf("body_length=%d", len(body))
	if !strings.Contains(got, wantBodyLen) {
		t.Fatalf("expected %q in response, got:\n%s", wantBodyLen, got)
	}
}

func TestReceivePack_RejectsContentLengthOverLimit(t *testing.T) {
	gdb := newTestDB(t)
	user, _ := seedUserRepo(t, gdb, "testowner", "testrepo", "testowner/testrepo", "main")
	t.Setenv("GITHTTP_MAX_PUSH_BYTES", "1024")

	store := &stubStore{
		exists:   true,
		repoPath: t.TempDir(),
		root:     t.TempDir(),
	}
	svc := &service.Service{DB: gdb}
	handler := githttp.New(store, svc)

	req := newRequestWithParams(
		http.MethodPost,
		"http://example.com/testowner/testrepo.git/git-receive-pack",
		http.NoBody,
		map[string]string{"owner": "testowner", "repo": "testrepo.git"},
	)
	req = req.WithContext(service.ContextWithUser(req.Context(), user))
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	req.ContentLength = 1025

	rr := httptest.NewRecorder()
	handler.ReceivePack(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d want %d", rr.Code, http.StatusRequestEntityTooLarge)
	}
	if !strings.Contains(rr.Body.String(), "push body exceeds maximum size") {
		t.Fatalf("unexpected body: %q", rr.Body.String())
	}
}

func TestInfoRefs_DoesNotForwardAuthorizationToBackend(t *testing.T) {
	env := setupTestServer(t, "testowner", "testrepo", "main", true)

	backendDir := t.TempDir()
	backendPath := filepath.Join(backendDir, "git-http-backend")
	script := "#!/bin/sh\n" +
		"printf \"Status: 200 OK\\r\\n\"\n" +
		"printf \"Content-Type: text/plain\\r\\n\"\n" +
		"printf \"\\r\\n\"\n" +
		"printf \"auth=%s\\n\" \"$HTTP_AUTHORIZATION\"\n"
	if err := os.WriteFile(backendPath, []byte(script), 0755); err != nil {
		t.Fatalf("write stub backend: %v", err)
	}
	if err := os.Chmod(backendPath, 0755); err != nil {
		t.Fatalf("chmod stub backend: %v", err)
	}

	t.Setenv("GIT_EXEC_PATH", backendDir)

	req, err := http.NewRequest("GET", env.Server.URL+"/testowner/testrepo.git/info/refs?service=git-upload-pack", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "token token-testowner")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	got := string(body)
	if strings.Contains(got, "token-testowner") {
		t.Fatalf("authorization header was forwarded to backend: %q", got)
	}
	if !strings.Contains(got, "auth=") {
		t.Fatalf("expected auth marker in backend output, got: %q", got)
	}
}

// TestReceivePack_FailurePaths tests ReceivePack failure scenarios.
func TestReceivePack_FailurePaths(t *testing.T) {
	env := setupTestServer(t, "testowner", "testrepo", "main", true)

	t.Run("invalid_request_body", func(t *testing.T) {
		// Send invalid POST body to receive-pack endpoint.
		url := fmt.Sprintf("%s/testowner/testrepo.git/git-receive-pack", env.Server.URL)
		req, err := http.NewRequest("POST", url, strings.NewReader("invalid-git-protocol-data"))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Authorization", "token token-testowner")
		req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do request: %v", err)
		}
		defer resp.Body.Close()
		t.Logf("ReceivePack with invalid body returned status: %d", resp.StatusCode)
	})

	t.Run("push_to_nonexistent_branch_then_fetch", func(t *testing.T) {
		// Test that pushing to a new branch and then fetching works correctly.
		localDir := filepath.Join(t.TempDir(), "local")
		runGit(t, t.TempDir(), "init", localDir)
		runGit(t, localDir, "checkout", "-b", "feature")

		f := filepath.Join(localDir, "feature.txt")
		if err := os.WriteFile(f, []byte("feature data"), 0644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		runGit(t, localDir, "add", "feature.txt")
		runGit(t, localDir, "commit", "-m", "add feature")
		runGit(t, localDir, "remote", "add", "origin", env.RepoURL)
		runGit(t, localDir, "push", "origin", "feature")

		// Verify the branch exists on remote.
		cloneDir := filepath.Join(t.TempDir(), "clone")
		runGit(t, t.TempDir(), "clone", env.RepoURL, cloneDir)
		out := runGit(t, cloneDir, "branch", "-r")
		if !strings.Contains(out, "origin/feature") {
			t.Errorf("expected origin/feature branch after push, got:\n%s", out)
		}
	})
}

func TestInfoRefs_AnonymousReadAccess(t *testing.T) {
	t.Run("public_repo_allows_anonymous_info_refs", func(t *testing.T) {
		env := setupTestServer(t, "publicowner", "publicrepo", "main", true)
		t.Setenv("GIT_HTTP_EXTRA_HEADER", "")

		req, err := http.NewRequest("GET", env.Server.URL+"/publicowner/publicrepo.git/info/refs?service=git-upload-pack", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}
		if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "application/x-git-upload-pack-advertisement") {
			t.Fatalf("unexpected content type: %q", got)
		}
	})

	t.Run("private_repo_challenges_anonymous_info_refs", func(t *testing.T) {
		env := setupTestServer(t, "privateowner", "privaterepo", "main", true)
		if err := env.DB.Model(&db.Repository{}).
			Where("full_name = ?", "privateowner/privaterepo").
			Update("private", true).Error; err != nil {
			t.Fatalf("mark private repo: %v", err)
		}
		t.Setenv("GIT_HTTP_EXTRA_HEADER", "")

		req, err := http.NewRequest("GET", env.Server.URL+"/privateowner/privaterepo.git/info/refs?service=git-upload-pack", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 401, got %d: %s", resp.StatusCode, body)
		}
		if got := resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, "Basic") {
			t.Fatalf("expected Basic auth challenge, got %q", got)
		}
	})
}

func TestGitHTTP_Authorization_DeniesUnauthorized(t *testing.T) {
	env := setupTestServer(t, "authowner", "authrepo", "main", true)

	var repo db.Repository
	if err := env.DB.First(&repo, "full_name = ?", "authowner/authrepo").Error; err != nil {
		t.Fatalf("load repo: %v", err)
	}
	if err := env.DB.Model(&db.Repository{}).
		Where("id = ?", repo.ID).
		Update("private", true).Error; err != nil {
		t.Fatalf("mark auth repo private: %v", err)
	}

	org := db.User{Login: "authorg", Name: "authorg", Type: db.TypeOrganization}
	if err := env.DB.Create(&org).Error; err != nil {
		t.Fatalf("create org: %v", err)
	}
	team := db.Team{OrganizationID: org.ID, Name: "authteam", Slug: "authteam"}
	if err := env.DB.Create(&team).Error; err != nil {
		t.Fatalf("create team: %v", err)
	}

	teamUser := db.User{Login: "teamuser", Name: "teamuser", Type: db.TypeUser}
	if err := env.DB.Create(&teamUser).Error; err != nil {
		t.Fatalf("create team user: %v", err)
	}
	teamToken := "token-teamuser"
	if err := env.DB.Create(&db.Token{UserID: teamUser.ID, Value: teamToken}).Error; err != nil {
		t.Fatalf("create team token: %v", err)
	}
	if err := env.DB.Create(&db.TeamMember{TeamID: team.ID, UserID: teamUser.ID, Role: "member"}).Error; err != nil {
		t.Fatalf("add team member: %v", err)
	}
	if err := env.DB.Create(&db.TeamRepository{TeamID: team.ID, RepositoryID: repo.ID, Permission: "write"}).Error; err != nil {
		t.Fatalf("grant team repo: %v", err)
	}

	outsider := db.User{Login: "outsider", Name: "outsider", Type: db.TypeUser}
	if err := env.DB.Create(&outsider).Error; err != nil {
		t.Fatalf("create outsider: %v", err)
	}
	outsiderToken := "token-outsider"
	if err := env.DB.Create(&db.Token{UserID: outsider.ID, Value: outsiderToken}).Error; err != nil {
		t.Fatalf("create outsider token: %v", err)
	}

	t.Setenv("GIT_HTTP_EXTRA_HEADER", "Authorization: token "+teamToken)
	runGit(t, t.TempDir(), "ls-remote", env.RepoURL)

	// Clone the seeded repo to get matching history, then add a commit.
	localDir := filepath.Join(t.TempDir(), "local")
	runGit(t, t.TempDir(), "clone", env.RepoURL, localDir)
	if err := os.WriteFile(filepath.Join(localDir, "auth-seed.txt"), []byte("auth test"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, localDir, "add", "auth-seed.txt")
	runGit(t, localDir, "commit", "-m", "seed")
	runGit(t, localDir, "push", "origin", "main")

	t.Setenv("GIT_HTTP_EXTRA_HEADER", "Authorization: token "+outsiderToken)
	if _, err := runGitAllowFailure(t, t.TempDir(), "ls-remote", env.RepoURL); err == nil {
		t.Fatalf("expected outsider ls-remote to fail")
	}

	if err := os.WriteFile(filepath.Join(localDir, "unauth.txt"), []byte("denied"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, localDir, "add", "unauth.txt")
	runGit(t, localDir, "commit", "-m", "unauthorized push")
	if _, err := runGitAllowFailure(t, localDir, "push", "origin", "main"); err == nil {
		t.Fatalf("expected outsider push to fail")
	}
}

func TestInfoRefs_EnsureRepoFailure(t *testing.T) {
	gdb := newTestDB(t)
	owner, _ := seedUserRepo(t, gdb, "owner", "repo", "owner/repo", "main")

	svc := &service.Service{DB: gdb}
	store := &stubStore{
		exists:  false,
		initErr: errors.New("init failed"),
	}
	handler := githttp.New(store, svc)

	req := newRequestWithParams(
		http.MethodGet,
		"http://example.com/owner/repo.git/info/refs?service=git-upload-pack",
		nil,
		map[string]string{"owner": "owner", "repo": "repo"},
	)
	req = req.WithContext(service.ContextWithUser(req.Context(), owner))
	rr := httptest.NewRecorder()

	handler.InfoRefs(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rr.Code)
	}
}

func TestUploadPack_RepoPathFailure(t *testing.T) {
	gdb := newTestDB(t)
	owner, _ := seedUserRepo(t, gdb, "owner", "repo", "owner/repo", "main")

	svc := &service.Service{DB: gdb}
	store := &stubStore{
		exists:      true,
		repoPathErr: errors.New("repo path failed"),
	}
	handler := githttp.New(store, svc)

	req := newRequestWithParams(
		http.MethodPost,
		"http://example.com/owner/repo.git/git-upload-pack",
		strings.NewReader("payload"),
		map[string]string{"owner": "owner", "repo": "repo"},
	)
	req = req.WithContext(service.ContextWithUser(req.Context(), owner))
	rr := httptest.NewRecorder()

	handler.UploadPack(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rr.Code)
	}
}

func TestReceivePack_RepoRootFailure(t *testing.T) {
	gdb := newTestDB(t)
	owner, _ := seedUserRepo(t, gdb, "owner", "repo", "owner/repo", "main")

	svc := &service.Service{DB: gdb}
	store := &stubStore{
		exists:   true,
		repoPath: "/tmp/owner/repo.git",
		rootErr:  errors.New("repo root failed"),
	}
	handler := githttp.New(store, svc)

	req := newRequestWithParams(
		http.MethodPost,
		"http://example.com/owner/repo.git/git-receive-pack",
		strings.NewReader("payload"),
		map[string]string{"owner": "owner", "repo": "repo"},
	)
	req = req.WithContext(service.ContextWithUser(req.Context(), owner))
	rr := httptest.NewRecorder()

	handler.ReceivePack(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rr.Code)
	}
}

func TestInfoRefs_ServeError_InvalidRepoFullName(t *testing.T) {
	gdb := newTestDB(t)
	owner, _ := seedUserRepo(t, gdb, "owner", "bad", "owner/", "main")

	svc := &service.Service{DB: gdb}
	store := &stubStore{
		exists:   true,
		repoPath: "/tmp/owner/bad.git",
		root:     "/tmp",
	}
	handler := githttp.New(store, svc)

	req := newRequestWithParams(
		http.MethodGet,
		"http://example.com/owner/.git/info/refs?service=git-upload-pack",
		nil,
		map[string]string{"owner": "owner", "repo": ".git"},
	)
	req = req.WithContext(service.ContextWithUser(req.Context(), owner))
	rr := httptest.NewRecorder()

	handler.InfoRefs(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rr.Code)
	}
}

func TestReceivePack_PropagatesDBAndUserContext(t *testing.T) {
	skipIfNoBackend(t)

	baseDB := newTestDB(t)
	scopedDB := newTestDB(t)

	tmpDir := t.TempDir()
	gitStore, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}

	anonUser, repo := seedUserRepo(t, scopedDB, "anon", "repo", "anon/repo", "main")

	if err := gitStore.Init(context.Background(), repo.FullName, repo.DefaultBranch, false); err != nil {
		t.Fatalf("gitStore.Init: %v", err)
	}

	svc := &service.Service{DB: baseDB, Git: gitStore}
	gitHandler := githttp.New(gitStore, svc)

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			ctx = service.ContextWithDB(ctx, scopedDB)
			ctx = service.ContextWithUser(ctx, anonUser)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	r.Get("/{owner}/{repo}.git/info/refs", gitHandler.InfoRefs)
	r.Post("/{owner}/{repo}.git/git-receive-pack", gitHandler.ReceivePack)

	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)

	repoURL := fmt.Sprintf("%s/%s/%s.git", ts.URL, anonUser.Login, repo.Name)

	localDir := filepath.Join(t.TempDir(), "local")
	runGit(t, t.TempDir(), "init", localDir)
	runGit(t, localDir, "checkout", "-b", "main")

	wfDir := filepath.Join(localDir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0755); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	wfContent := "name: CI\non: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n"
	if err := os.WriteFile(filepath.Join(wfDir, "ci.yml"), []byte(wfContent), 0644); err != nil {
		t.Fatalf("write ci.yml: %v", err)
	}
	runGit(t, localDir, "add", ".github/workflows/ci.yml")
	runGit(t, localDir, "commit", "-m", "add CI workflow")
	runGit(t, localDir, "remote", "add", "origin", repoURL)
	runGit(t, localDir, "push", "origin", "main")

	waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		var wf db.Workflow
		err := scopedDB.Where("repository_id = ? AND path = ?", repo.ID, ".github/workflows/ci.yml").First(&wf).Error
		return err == nil
	}, "workflow row was not created in scoped DB")

	var wf db.Workflow
	if err := scopedDB.Where("repository_id = ? AND path = ?", repo.ID, ".github/workflows/ci.yml").First(&wf).Error; err != nil {
		t.Fatalf("query workflow: %v", err)
	}
	if wf.Name != "CI" {
		t.Fatalf("expected workflow name CI, got %q", wf.Name)
	}
}
