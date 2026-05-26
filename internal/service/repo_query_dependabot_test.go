package service_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"gorm.io/gorm"
)

// ============== RenameRepo Tests ==============

func TestRenameRepo_Success(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	svc.DB.Create(&db.User{Login: "renameuser", Name: "renameuser", Type: db.TypeUser})

	// Create repo with readme so git dir exists.
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "renameuser",
		Name:       "oldname",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	// Rename the repo.
	renamed, err := svc.RenameRepo(ctx, "renameuser/oldname", "newname")
	if err != nil {
		t.Fatalf("RenameRepo: %v", err)
	}

	if renamed.Name != "newname" {
		t.Errorf("expected name 'newname', got %q", renamed.Name)
	}
	if renamed.FullName != "renameuser/newname" {
		t.Errorf("expected full_name 'renameuser/newname', got %q", renamed.FullName)
	}

	// Old name should redirect to new repo via repo_redirects.
	oldRepo, err := svc.GetRepo(ctx, "renameuser/oldname")
	if err != nil {
		t.Errorf("expected old name to redirect, got: %v", err)
	}
	if oldRepo.ID != renamed.ID {
		t.Errorf("expected old name to redirect to same repo ID %d, got %d", renamed.ID, oldRepo.ID)
	}

	// New name should resolve.
	got, err := svc.GetRepo(ctx, "renameuser/newname")
	if err != nil {
		t.Fatalf("get renamed repo: %v", err)
	}
	if got.ID != renamed.ID {
		t.Errorf("expected same repo ID %d, got %d", renamed.ID, got.ID)
	}

	// Git path assertions.
	if !svc.Git.Exists(ctx, "renameuser/newname") {
		t.Error("expected new git path to exist")
	}
	if svc.Git.Exists(ctx, "renameuser/oldname") {
		t.Error("expected old git path to be gone")
	}
}

func TestRenameRepo_NotFound(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	_, err := svc.RenameRepo(ctx, "nonexistent/repo", "newname")
	if !errors.Is(err, service.ErrNotFound) {
		t.Errorf("expected ErrNotFound for non-existent repo, got: %v", err)
	}
}

func TestRenameRepo_UpdatesAutolink(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "renameuser", Name: "renameuser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "renameuser",
		Name:       "oldname",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	if err := svc.CreateAutolink(ctx, &db.Autolink{
		RepositoryFullName: "renameuser/oldname",
		KeyPrefix:          "JIRA-",
		URLTemplate:        "https://example.com/browse/<num>",
		IsAlphanumeric:     true,
	}); err != nil {
		t.Fatalf("create autolink: %v", err)
	}

	_, err = svc.RenameRepo(ctx, "renameuser/oldname", "newname")
	if err != nil {
		t.Fatalf("RenameRepo: %v", err)
	}

	autolinksNew, err := svc.ListAutolinks(ctx, "renameuser/newname")
	if err != nil {
		t.Fatalf("list autolinks new: %v", err)
	}
	if len(autolinksNew) != 1 {
		t.Fatalf("expected 1 autolink for new name, got %d", len(autolinksNew))
	}
	if autolinksNew[0].RepositoryFullName != "renameuser/newname" {
		t.Errorf("expected autolink repository_full_name to be updated, got %q", autolinksNew[0].RepositoryFullName)
	}
	if autolinksNew[0].KeyPrefix != "JIRA-" {
		t.Errorf("expected autolink key_prefix to remain JIRA-, got %q", autolinksNew[0].KeyPrefix)
	}
	if !autolinksNew[0].IsAlphanumeric {
		t.Errorf("expected autolink IsAlphanumeric to remain true")
	}

	autolinksOld, err := svc.ListAutolinks(ctx, "renameuser/oldname")
	if err != nil {
		t.Fatalf("list autolinks old: %v", err)
	}
	if len(autolinksOld) != 0 {
		t.Fatalf("expected 0 autolinks for old name, got %d", len(autolinksOld))
	}
}

func TestRenameRepo_RollbackOnUpdateFailure(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "renameuser", Name: "renameuser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "renameuser",
		Name:       "oldname",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	if err := svc.CreateAutolink(ctx, &db.Autolink{
		RepositoryFullName: "renameuser/oldname",
		KeyPrefix:          "JIRA-",
		URLTemplate:        "https://example.com/browse/<num>",
	}); err != nil {
		t.Fatalf("create autolink: %v", err)
	}

	const cbName = "test:rename_repo_update_fail"
	var failOnce atomic.Bool
	failOnce.Store(true)
	if err := svc.DB.Callback().Update().Before("gorm:update").Register(cbName, func(tx *gorm.DB) {
		if tx.Statement.Table == "repositories" && failOnce.CompareAndSwap(true, false) {
			tx.AddError(errors.New("forced rename update failure"))
		}
	}); err != nil {
		t.Fatalf("register update callback: %v", err)
	}
	defer func() {
		_ = svc.DB.Callback().Update().Remove(cbName)
	}()

	if _, err := svc.RenameRepo(ctx, "renameuser/oldname", "newname"); err == nil {
		t.Fatalf("expected RenameRepo to fail when update is forced to error")
	}
	if failOnce.Load() {
		t.Fatalf("expected injected update failure to trigger")
	}

	if _, err := svc.GetRepo(ctx, "renameuser/oldname"); err != nil {
		t.Fatalf("expected old repo to remain after rollback, got err=%v", err)
	}
	if _, err := svc.GetRepo(ctx, "renameuser/newname"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected new name to be absent after rollback, got err=%v", err)
	}

	if !svc.Git.Exists(ctx, "renameuser/oldname") {
		t.Error("expected old git path to exist after rollback")
	}
	if svc.Git.Exists(ctx, "renameuser/newname") {
		t.Error("expected new git path to be removed after rollback")
	}

	autolinksOld, err := svc.ListAutolinks(ctx, "renameuser/oldname")
	if err != nil {
		t.Fatalf("list autolinks old: %v", err)
	}
	if len(autolinksOld) != 1 {
		t.Fatalf("expected 1 autolink for old name after rollback, got %d", len(autolinksOld))
	}
	autolinksNew, err := svc.ListAutolinks(ctx, "renameuser/newname")
	if err != nil {
		t.Fatalf("list autolinks new: %v", err)
	}
	if len(autolinksNew) != 0 {
		t.Fatalf("expected 0 autolinks for new name after rollback, got %d", len(autolinksNew))
	}
}

// ============== SearchRepos Tests ==============

func TestSearchRepos_ByName(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	svc.DB.Create(&db.User{Login: "searchuser", Name: "searchuser", Type: db.TypeUser})

	// Create multiple repos.
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "searchuser",
		Name:       "alpha-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create alpha: %v", err)
	}

	_, err = svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "searchuser",
		Name:       "beta-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create beta: %v", err)
	}

	_, err = svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:  "searchuser",
		Name:        "gamma-repo",
		AddReadme:   true,
		Description: "This is the gamma repository",
	})
	if err != nil {
		t.Fatalf("create gamma: %v", err)
	}

	// Search by name substring.
	results, err := svc.SearchRepos(ctx, "alpha")
	if err != nil {
		t.Fatalf("SearchRepos: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'alpha', got %d", len(results))
	}
	if results[0].Name != "alpha-repo" {
		t.Errorf("expected 'alpha-repo', got %q", results[0].Name)
	}

	// Search by description.
	results, err = svc.SearchRepos(ctx, "gamma")
	if err != nil {
		t.Fatalf("SearchRepos: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'gamma', got %d", len(results))
	}
	if results[0].Name != "gamma-repo" {
		t.Errorf("expected 'gamma-repo', got %q", results[0].Name)
	}

	// No match returns empty slice.
	results, err = svc.SearchRepos(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("SearchRepos no match: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for 'nonexistent', got %d", len(results))
	}
}

// ============== SearchReposGQL Tests ==============

func TestSearchReposGQL_ByUser(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	svc.DB.Create(&db.User{Login: "gqluser", Name: "gqluser", Type: db.TypeUser})

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "gqluser",
		Name:       "repo1",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo1: %v", err)
	}

	_, err = svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "gqluser",
		Name:       "repo2",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo2: %v", err)
	}

	// Search by user.
	results, err := svc.SearchReposGQL(ctx, "user:gqluser")
	if err != nil {
		t.Fatalf("SearchReposGQL: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 repos for user:gqluser, got %d", len(results))
	}
}

func TestSearchReposGQL_ByLanguage(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	svc.DB.Create(&db.User{Login: "languser", Name: "languser", Type: db.TypeUser})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "languser",
		Name:       "go-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create go-repo: %v", err)
	}
	svc.DB.Model(&repo).Update("language", "Go")

	_, err = svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "languser",
		Name:       "python-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create python-repo: %v", err)
	}

	// Search by language.
	results, err := svc.SearchReposGQL(ctx, "language:Go")
	if err != nil {
		t.Fatalf("SearchReposGQL language: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 Go repo, got %d", len(results))
	}
	if len(results) > 0 && results[0].Language != "Go" {
		t.Errorf("expected Go language, got %q", results[0].Language)
	}
}

func TestSearchReposGQL_ByTopic(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	svc.DB.Create(&db.User{Login: "topicuser", Name: "topicuser", Type: db.TypeUser})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "topicuser",
		Name:       "topic-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create topic-repo: %v", err)
	}
	svc.DB.Model(&repo).Update("topics", "machine-learning,python")

	// Search by topic.
	results, err := svc.SearchReposGQL(ctx, "topic:machine-learning")
	if err != nil {
		t.Fatalf("SearchReposGQL topic: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 repo with machine-learning topic, got %d", len(results))
	}
}

// ============== ListAllRepos Tests ==============

func TestListAllRepos_ReturnsAll(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	svc.DB.Create(&db.User{Login: "listuser", Name: "listuser", Type: db.TypeUser})

	// Create multiple repos.
	for i := 1; i <= 5; i++ {
		_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: "listuser",
			Name:       "list-repo-" + string(rune('0'+i)),
			AddReadme:  true,
		})
		if err != nil {
			t.Fatalf("create repo %d: %v", i, err)
		}
	}

	results, err := svc.ListAllRepos(ctx)
	if err != nil {
		t.Fatalf("ListAllRepos: %v", err)
	}
	if len(results) != 5 {
		t.Errorf("expected 5 repos, got %d", len(results))
	}
}

// ============== Dependabot Tests ==============

func TestDependabotAlerts_List(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	svc.DB.Create(&db.User{Login: "depuser", Name: "depuser", Type: db.TypeUser})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "depuser",
		Name:       "dep-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create dep repo: %v", err)
	}

	// Create some alerts directly in DB.
	alert1 := db.DependabotAlert{
		RepositoryID: repo.ID,
		Number:       1,
		State:        "open",
	}
	alert2 := db.DependabotAlert{
		RepositoryID: repo.ID,
		Number:       2,
		State:        "open",
	}
	svc.DB.Create(&alert1)
	svc.DB.Create(&alert2)

	// List alerts.
	alerts, err := svc.ListDependabotAlerts(ctx, repo.ID)
	if err != nil {
		t.Fatalf("ListDependabotAlerts: %v", err)
	}
	if len(alerts) != 2 {
		t.Errorf("expected 2 alerts, got %d", len(alerts))
	}

	// Verify order (DESC by number).
	if alerts[0].Number != 2 {
		t.Errorf("expected first alert number 2, got %d", alerts[0].Number)
	}
	if alerts[1].Number != 1 {
		t.Errorf("expected second alert number 1, got %d", alerts[1].Number)
	}
}

func TestDependabotAlerts_Get(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	svc.DB.Create(&db.User{Login: "depuser2", Name: "depuser2", Type: db.TypeUser})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "depuser2",
		Name:       "dep-repo2",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create dep repo2: %v", err)
	}

	alert := db.DependabotAlert{
		RepositoryID: repo.ID,
		Number:       42,
		State:        "open",
	}
	svc.DB.Create(&alert)

	// Get alert.
	got, err := svc.GetDependabotAlert(ctx, repo.ID, 42)
	if err != nil {
		t.Fatalf("GetDependabotAlert: %v", err)
	}
	if got.Number != 42 {
		t.Errorf("expected alert number 42, got %d", got.Number)
	}
	if got.State != "open" {
		t.Errorf("expected state 'open', got %q", got.State)
	}

	// Not found case.
	_, err = svc.GetDependabotAlert(ctx, repo.ID, 999)
	if !errors.Is(err, service.ErrNotFound) {
		t.Errorf("expected ErrNotFound for non-existent alert, got: %v", err)
	}
}

func TestDependabotAlerts_Update(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	svc.DB.Create(&db.User{Login: "depuser3", Name: "depuser3", Type: db.TypeUser})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "depuser3",
		Name:       "dep-repo3",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create dep repo3: %v", err)
	}

	alert := db.DependabotAlert{
		RepositoryID: repo.ID,
		Number:       10,
		State:        "open",
	}
	svc.DB.Create(&alert)

	// Update alert state to dismissed.
	alert.State = "dismissed"
	alert.DismissedReason = "fixed"
	if err := svc.UpdateDependabotAlert(ctx, &alert); err != nil {
		t.Fatalf("UpdateDependabotAlert: %v", err)
	}

	// Verify update.
	got, err := svc.GetDependabotAlert(ctx, repo.ID, 10)
	if err != nil {
		t.Fatalf("GetDependabotAlert after update: %v", err)
	}
	if got.State != "dismissed" {
		t.Errorf("expected state 'dismissed', got %q", got.State)
	}
	if got.DismissedReason != "fixed" {
		t.Errorf("expected dismissed_reason 'fixed', got %q", got.DismissedReason)
	}
}

func TestDependabotAlerts_ListEmptyRepo(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	svc.DB.Create(&db.User{Login: "depuser4", Name: "depuser4", Type: db.TypeUser})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "depuser4",
		Name:       "dep-repo4",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create dep repo4: %v", err)
	}

	// List alerts for repo with no alerts.
	alerts, err := svc.ListDependabotAlerts(ctx, repo.ID)
	if err != nil {
		t.Fatalf("ListDependabotAlerts empty: %v", err)
	}
	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts, got %d", len(alerts))
	}
}
