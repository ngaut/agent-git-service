package service_test

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
	"gorm.io/gorm"
)

// ============== RenameRepo Additional Tests ==============

func TestRenameRepo_DBTransactionFailureRollback(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "rollbackuser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "rollbackuser",
		Name:       "oldrepo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	// Create an autolink that will cause the transaction to fail
	if err := svc.CreateAutolink(ctx, &db.Autolink{
		RepositoryFullName: "rollbackuser/oldrepo",
		KeyPrefix:          "BUG-",
		URLTemplate:        "https://bugs.example.com/<num>",
	}); err != nil {
		t.Fatalf("create autolink: %v", err)
	}

	// Inject failure in autolink update
	const cbName = "test:rename_autolink_fail"
	var failOnce atomic.Bool
	failOnce.Store(true)
	if err := svc.DB.Callback().Update().Before("gorm:update").Register(cbName, func(tx *gorm.DB) {
		if tx.Statement.Table == "autolinks" && failOnce.CompareAndSwap(true, false) {
			tx.AddError(errors.New("forced autolink update failure"))
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	defer func() {
		_ = svc.DB.Callback().Update().Remove(cbName)
	}()

	_, err = svc.RenameRepo(ctx, "rollbackuser/oldrepo", "newrepo")
	if err == nil {
		t.Fatalf("expected RenameRepo to fail")
	}
	if failOnce.Load() {
		t.Fatalf("expected injected failure to trigger")
	}

	// Verify rollback: old repo should exist, new should not
	if _, err := svc.GetRepo(ctx, "rollbackuser/oldrepo"); err != nil {
		t.Fatalf("expected old repo to exist after rollback: %v", err)
	}
	if _, err := svc.GetRepo(ctx, "rollbackuser/newrepo"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected new repo to not exist after rollback: %v", err)
	}

	// Git should be rolled back too
	if !svc.Git.Exists(ctx, "rollbackuser/oldrepo") {
		t.Error("expected old git path to exist after rollback")
	}
	if svc.Git.Exists(ctx, "rollbackuser/newrepo") {
		t.Error("expected new git path to be removed after rollback")
	}
}

// ============== SearchRepos Tests ==============

func TestSearchRepos_EmptyQueryReturnsEmpty(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	results, err := svc.SearchRepos(ctx, "")
	if err != nil {
		t.Fatalf("SearchRepos empty: %v", err)
	}
	if results == nil {
		t.Fatal("expected empty slice, got nil")
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty query, got %d", len(results))
	}
}

func TestSearchRepos_WhitespaceOnlyQueryReturnsEmpty(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	results, err := svc.SearchRepos(ctx, "   ")
	if err != nil {
		t.Fatalf("SearchRepos whitespace: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for whitespace query, got %d", len(results))
	}
}

func TestSearchRepos_UserQualifier(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create two users with repos
	for _, login := range []string{"userA", "userB"} {
		if err := svc.DB.Create(&db.User{Login: login, Type: db.TypeUser}).Error; err != nil {
			t.Fatalf("create user %s: %v", login, err)
		}
		_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: login,
			Name:       "repo",
			AddReadme:  true,
		})
		if err != nil {
			t.Fatalf("create repo for %s: %v", login, err)
		}
	}

	// Search by user qualifier
	results, err := svc.SearchRepos(ctx, "user:userA")
	if err != nil {
		t.Fatalf("SearchRepos user: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for user:userA, got %d", len(results))
	}
	if results[0].Owner.Login != "userA" {
		t.Errorf("expected owner userA, got %q", results[0].Owner.Login)
	}
}

func TestSearchRepos_OrgQualifier(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create org and repos
	if err := svc.DB.Create(&db.User{Login: "testorg", Type: db.TypeOrganization}).Error; err != nil {
		t.Fatalf("create org: %v", err)
	}
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testorg",
		Name:       "org-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create org repo: %v", err)
	}

	results, err := svc.SearchRepos(ctx, "org:testorg")
	if err != nil {
		t.Fatalf("SearchRepos org: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for org:testorg, got %d", len(results))
	}
}

func TestSearchRepos_LanguageQualifier(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "languser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	repo1, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "languser",
		Name:       "go-project",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo1: %v", err)
	}
	repo2, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "languser",
		Name:       "python-project",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo2: %v", err)
	}

	// Update languages
	svc.DB.Model(&db.Repository{}).Where("id = ?", repo1.ID).Update("language", "Go")
	svc.DB.Model(&db.Repository{}).Where("id = ?", repo2.ID).Update("language", "Python")

	results, err := svc.SearchRepos(ctx, "language:Go")
	if err != nil {
		t.Fatalf("SearchRepos language: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 Go repo, got %d", len(results))
	}
	if results[0].Language != "Go" {
		t.Errorf("expected Go, got %q", results[0].Language)
	}

	results, err = svc.SearchRepos(ctx, "language:Rust")
	if err != nil {
		t.Fatalf("SearchRepos language no match: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 Rust repos, got %d", len(results))
	}
}

func TestSearchRepos_TopicQualifier(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "topicuser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "topicuser",
		Name:       "ml-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	svc.DB.Model(&db.Repository{}).Where("id = ?", repo.ID).Update("topics", "machine-learning,ai,python")

	// Test exact topic match
	results, err := svc.SearchRepos(ctx, "topic:machine-learning")
	if err != nil {
		t.Fatalf("SearchRepos topic: %v", err)
	}
	if len(results) < 1 {
		t.Fatalf("expected at least 1 repo with machine-learning topic, got %d", len(results))
	}
}

func TestSearchRepos_MultiTopicQualifier(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "multitopicuser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	repo1, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "multitopicuser",
		Name:       "ml-ai-python",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo1: %v", err)
	}
	svc.DB.Model(&db.Repository{}).Where("id = ?", repo1.ID).Update("topics", "machine-learning,ai,python")

	repo2, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "multitopicuser",
		Name:       "ml-python",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo2: %v", err)
	}
	svc.DB.Model(&db.Repository{}).Where("id = ?", repo2.ID).Update("topics", "machine-learning,python")

	repo3, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "multitopicuser",
		Name:       "ml-only",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo3: %v", err)
	}
	svc.DB.Model(&db.Repository{}).Where("id = ?", repo3.ID).Update("topics", "machine-learning")

	repo4, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "multitopicuser",
		Name:       "ai-python",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo4: %v", err)
	}
	svc.DB.Model(&db.Repository{}).Where("id = ?", repo4.ID).Update("topics", "ai,python")

	results, err := svc.SearchRepos(ctx, "topic:machine-learning,python")
	if err != nil {
		t.Fatalf("SearchRepos topic multi: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 repos with machine-learning and python topics, got %d", len(results))
	}
	got := map[string]struct{}{}
	for _, rep := range results {
		got[rep.Name] = struct{}{}
	}
	for _, name := range []string{"ml-ai-python", "ml-python"} {
		if _, ok := got[name]; !ok {
			t.Fatalf("expected repo %q in results", name)
		}
	}
	if _, ok := got["ml-only"]; ok {
		t.Fatalf("did not expect repo %q in results", "ml-only")
	}
	if _, ok := got["ai-python"]; ok {
		t.Fatalf("did not expect repo %q in results", "ai-python")
	}
}

func TestSearchRepos_ArchivedQualifier(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "archuser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "archuser",
		Name:       "active-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo1: %v", err)
	}
	_, err = svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "archuser",
		Name:       "archived-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo2: %v", err)
	}

	svc.DB.Model(&db.Repository{}).Where("name = ?", "archived-repo").Update("archived", true)

	// Search for archived only
	results, err := svc.SearchRepos(ctx, "archived:true")
	if err != nil {
		t.Fatalf("SearchRepos archived: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 archived repo, got %d", len(results))
	}
	if !results[0].Archived {
		t.Error("expected archived repo")
	}
}

func TestSearchRepos_ForkQualifier(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create base repo owner and fork owner
	if err := svc.DB.Create(&db.User{Login: "baseowner", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create base owner: %v", err)
	}
	if err := svc.DB.Create(&db.User{Login: "forkowner", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create fork owner: %v", err)
	}

	base, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "baseowner",
		Name:       "upstream",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create base: %v", err)
	}

	fork, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "forkowner",
		Name:       "upstream",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create fork: %v", err)
	}

	// Mark as fork
	svc.DB.Model(&db.Repository{}).Where("id = ?", fork.ID).Updates(map[string]any{
		"fork":      true,
		"parent_id": base.ID,
	})

	// Search for forks only - use fork:only syntax
	results, err := svc.SearchRepos(ctx, "fork:only")
	if err != nil {
		t.Fatalf("SearchRepos fork: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 fork, got %d", len(results))
	}
	if !results[0].Fork {
		t.Error("expected fork repo")
	}

	results, err = svc.SearchRepos(ctx, "fork:false")
	if err != nil {
		t.Fatalf("SearchRepos fork false: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 non-fork, got %d", len(results))
	}
	if results[0].Fork {
		t.Error("expected non-fork repo")
	}

	results, err = svc.SearchRepos(ctx, "fork:true")
	if err != nil {
		t.Fatalf("SearchRepos fork true: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 repos when including forks, got %d", len(results))
	}
	got := map[string]struct{}{}
	for _, rep := range results {
		got[rep.FullName] = struct{}{}
	}
	for _, name := range []string{base.FullName, fork.FullName} {
		if _, ok := got[name]; !ok {
			t.Fatalf("expected repo %q in results", name)
		}
	}
}

func TestSearchRepos_VisibilityQualifier(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "visuser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	publicRepo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "visuser",
		Name:       "public-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create public repo: %v", err)
	}
	privateRepo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "visuser",
		Name:       "private-repo",
		Private:    true,
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create private repo: %v", err)
	}
	internalRepo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "visuser",
		Name:       "internal-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create internal repo: %v", err)
	}
	if err := svc.DB.Model(&db.Repository{}).Where("id = ?", internalRepo.ID).Update("visibility", "internal").Error; err != nil {
		t.Fatalf("update internal visibility: %v", err)
	}

	results, err := svc.SearchRepos(ctx, "visibility:public user:visuser")
	if err != nil {
		t.Fatalf("SearchRepos visibility:public: %v", err)
	}
	if len(results) != 1 || results[0].FullName != publicRepo.FullName {
		t.Fatalf("expected public repo only, got %v", results)
	}

	results, err = svc.SearchRepos(ctx, "visibility:private user:visuser")
	if err != nil {
		t.Fatalf("SearchRepos visibility:private: %v", err)
	}
	if len(results) != 1 || results[0].FullName != privateRepo.FullName {
		t.Fatalf("expected private repo only, got %v", results)
	}

	results, err = svc.SearchRepos(ctx, "visibility:internal user:visuser")
	if err != nil {
		t.Fatalf("SearchRepos visibility:internal: %v", err)
	}
	if len(results) != 1 || results[0].FullName != internalRepo.FullName {
		t.Fatalf("expected internal repo only, got %v", results)
	}

	results, err = svc.SearchRepos(ctx, "visibility:private user:visuser")
	if err != nil {
		t.Fatalf("SearchRepos visibility:private: %v", err)
	}
	if len(results) != 1 || results[0].FullName != privateRepo.FullName {
		t.Fatalf("expected private repo only for visibility qualifier, got %v", results)
	}
}

func TestSearchRepos_LicenseQualifier(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "licuser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "licuser",
		Name:       "mit-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo1: %v", err)
	}
	_, err = svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "licuser",
		Name:       "apache-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo2: %v", err)
	}

	svc.DB.Model(&db.Repository{}).Where("name = ?", "mit-repo").Update("license", "MIT")
	svc.DB.Model(&db.Repository{}).Where("name = ?", "apache-repo").Update("license", "Apache-2.0")

	results, err := svc.SearchRepos(ctx, "license:mit")
	if err != nil {
		t.Fatalf("SearchRepos license: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 MIT repo, got %d", len(results))
	}
}

func TestSearchRepos_StarsRange(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "staruser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Create repos and add stars
	for i := 1; i <= 5; i++ {
		repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: "staruser",
			Name:       "star-repo-" + string(rune('0'+i)),
			AddReadme:  true,
		})
		if err != nil {
			t.Fatalf("create repo %d: %v", i, err)
		}
		// Add i stars
		for j := 0; j < i; j++ {
			starUser := db.User{Login: "stargazer-" + string(rune('0'+i)) + "-" + string(rune('0'+j)), Type: db.TypeUser}
			svc.DB.Create(&starUser)
			svc.DB.Create(&db.Star{UserID: starUser.ID, RepositoryID: repo.ID})
		}
	}

	// Search for repos with >2 stars (repos 3, 4, 5 have 3, 4, 5 stars respectively)
	results, err := svc.SearchRepos(ctx, "stars:>2")
	if err != nil {
		t.Fatalf("SearchRepos stars: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 repos with >2 stars, got %d: %+v", len(results), results)
	}
}

func TestSearchRepos_ForksRange(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "forkbase", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create base user: %v", err)
	}

	base, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "forkbase",
		Name:       "base-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create base: %v", err)
	}

	// Create 3 forks
	for i := 1; i <= 3; i++ {
		forkOwner := db.User{Login: "forker-" + string(rune('0'+i)), Type: db.TypeUser}
		svc.DB.Create(&forkOwner)
		fork, err := svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: forkOwner.Login,
			Name:       "base-repo",
			AddReadme:  true,
		})
		if err != nil {
			t.Fatalf("create fork %d: %v", i, err)
		}
		svc.DB.Model(&db.Repository{}).Where("id = ?", fork.ID).Updates(map[string]any{
			"fork":      true,
			"parent_id": base.ID,
		})
	}

	results, err := svc.SearchRepos(ctx, "forks:>1")
	if err != nil {
		t.Fatalf("SearchRepos forks: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 repo with >1 forks, got %d", len(results))
	}
}

func TestSearchRepos_CreatedDateRange(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "dateuser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "dateuser",
		Name:       "old-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create old: %v", err)
	}

	// Search with created qualifier (should work even if date doesn't match exactly)
	results, err := svc.SearchRepos(ctx, "created:>=2020-01-01")
	if err != nil {
		t.Fatalf("SearchRepos created: %v", err)
	}
	if len(results) < 1 {
		t.Fatalf("expected at least 1 repo, got %d", len(results))
	}
}

func TestSearchRepos_SortByStars(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "sortuser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Create repos with different star counts
	repos := make([]db.Repository, 3)
	for i := 0; i < 3; i++ {
		repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: "sortuser",
			Name:       "sort-repo-" + string(rune('0'+i)),
			AddReadme:  true,
		})
		if err != nil {
			t.Fatalf("create repo %d: %v", i, err)
		}
		repos[i] = repo
		// Add stars: 3, 1, 2
		starCount := []int{3, 1, 2}[i]
		for j := 0; j < starCount; j++ {
			starUser := db.User{Login: "sort-stargazer-" + string(rune('0'+i)) + "-" + string(rune('0'+j)), Type: db.TypeUser}
			svc.DB.Create(&starUser)
			svc.DB.Create(&db.Star{UserID: starUser.ID, RepositoryID: repo.ID})
		}
	}

	results, err := svc.SearchRepos(ctx, "user:sortuser sort:stars-desc")
	if err != nil {
		t.Fatalf("SearchRepos sort: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 repos, got %d", len(results))
	}
	// First should have most stars
	if results[0].Name != "sort-repo-0" {
		t.Errorf("expected first repo to be sort-repo-0 (3 stars), got %q", results[0].Name)
	}
}

func TestSearchRepos_SortByUpdated(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "sortuser2", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	for i := 0; i < 3; i++ {
		_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: "sortuser2",
			Name:       "upd-repo-" + string(rune('0'+i)),
			AddReadme:  true,
		})
		if err != nil {
			t.Fatalf("create repo %d: %v", i, err)
		}
	}

	results, err := svc.SearchRepos(ctx, "user:sortuser2 sort:updated-desc")
	if err != nil {
		t.Fatalf("SearchRepos sort updated: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 repos, got %d", len(results))
	}
}

// ============== SearchReposGQL Tests ==============

func TestSearchReposGQL_EmptyQuery(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	results, err := svc.SearchReposGQL(ctx, "")
	if err != nil {
		t.Fatalf("SearchReposGQL empty: %v", err)
	}
	if results == nil {
		t.Fatal("expected slice, got nil")
	}
}

func TestSearchReposGQL_LanguageFilter(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "gqluser2", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	repo1, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "gqluser2",
		Name:       "rust-project",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo1: %v", err)
	}
	repo2, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "gqluser2",
		Name:       "java-project",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo2: %v", err)
	}

	svc.DB.Model(&db.Repository{}).Where("id = ?", repo1.ID).Update("language", "Rust")
	svc.DB.Model(&db.Repository{}).Where("id = ?", repo2.ID).Update("language", "Java")

	results, err := svc.SearchReposGQL(ctx, "language:Rust")
	if err != nil {
		t.Fatalf("SearchReposGQL language: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 Rust repo, got %d", len(results))
	}
}

func TestSearchReposGQL_TopicFilter(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "gqltopicuser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "gqltopicuser",
		Name:       "web-framework",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	svc.DB.Model(&db.Repository{}).Where("id = ?", repo.ID).Update("topics", "web,framework,http")

	results, err := svc.SearchReposGQL(ctx, "topic:web")
	if err != nil {
		t.Fatalf("SearchReposGQL topic: %v", err)
	}
	if len(results) < 1 {
		t.Fatalf("expected at least 1 web repo, got %d", len(results))
	}
}

func TestSearchReposGQL_ArchivedFilter(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "gqlarchuser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "gqlarchuser",
		Name:       "active-gql",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo1: %v", err)
	}
	_, err = svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "gqlarchuser",
		Name:       "archived-gql",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo2: %v", err)
	}

	svc.DB.Model(&db.Repository{}).Where("name = ?", "archived-gql").Update("archived", true)

	results, err := svc.SearchReposGQL(ctx, "archived:true")
	if err != nil {
		t.Fatalf("SearchReposGQL archived: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 archived repo, got %d", len(results))
	}
}

func TestSearchReposGQL_ForkFilter(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "gqlbase", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create base: %v", err)
	}
	if err := svc.DB.Create(&db.User{Login: "gqlforker", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create forker: %v", err)
	}

	base, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "gqlbase",
		Name:       "original",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create base: %v", err)
	}

	fork, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "gqlforker",
		Name:       "original",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create fork: %v", err)
	}

	svc.DB.Model(&db.Repository{}).Where("id = ?", fork.ID).Updates(map[string]any{
		"fork":      true,
		"parent_id": base.ID,
	})

	results, err := svc.SearchReposGQL(ctx, "fork:only")
	if err != nil {
		t.Fatalf("SearchReposGQL fork: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 fork, got %d", len(results))
	}

	results, err = svc.SearchReposGQL(ctx, "fork:false")
	if err != nil {
		t.Fatalf("SearchReposGQL fork false: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 non-fork, got %d", len(results))
	}
	if results[0].Fork {
		t.Fatalf("expected non-fork repo")
	}

	results, err = svc.SearchReposGQL(ctx, "fork:true")
	if err != nil {
		t.Fatalf("SearchReposGQL fork true: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 repos when including forks, got %d", len(results))
	}
}

func TestSearchReposGQL_VisibilityFilter(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "gqlvis", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	publicRepo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "gqlvis",
		Name:       "public-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create public repo: %v", err)
	}
	privateRepo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "gqlvis",
		Name:       "private-repo",
		Private:    true,
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create private repo: %v", err)
	}
	internalRepo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "gqlvis",
		Name:       "internal-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create internal repo: %v", err)
	}
	if err := svc.DB.Model(&db.Repository{}).Where("id = ?", internalRepo.ID).Update("visibility", "internal").Error; err != nil {
		t.Fatalf("update internal visibility: %v", err)
	}

	results, err := svc.SearchReposGQL(ctx, "visibility:public user:gqlvis")
	if err != nil {
		t.Fatalf("SearchReposGQL visibility:public: %v", err)
	}
	if len(results) != 1 || results[0].FullName != publicRepo.FullName {
		t.Fatalf("expected public repo only, got %v", results)
	}

	results, err = svc.SearchReposGQL(ctx, "visibility:private user:gqlvis")
	if err != nil {
		t.Fatalf("SearchReposGQL visibility:private: %v", err)
	}
	if len(results) != 1 || results[0].FullName != privateRepo.FullName {
		t.Fatalf("expected private repo only, got %v", results)
	}

	results, err = svc.SearchReposGQL(ctx, "visibility:internal user:gqlvis")
	if err != nil {
		t.Fatalf("SearchReposGQL visibility:internal: %v", err)
	}
	if len(results) != 1 || results[0].FullName != internalRepo.FullName {
		t.Fatalf("expected internal repo only, got %v", results)
	}
}

func TestSearchReposGQL_SortOptions(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "gqlsortuser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	for i := 0; i < 3; i++ {
		_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: "gqlsortuser",
			Name:       "gql-sort-" + string(rune('0'+i)),
			AddReadme:  true,
		})
		if err != nil {
			t.Fatalf("create repo %d: %v", i, err)
		}
	}

	// Test different sort options
	sortTests := []string{"stars-desc", "updated-asc", "created-desc", "created-asc"}
	for _, sort := range sortTests {
		results, err := svc.SearchReposGQL(ctx, "sort:"+sort)
		if err != nil {
			t.Fatalf("SearchReposGQL sort %s: %v", sort, err)
		}
		if len(results) != 3 {
			t.Errorf("sort %s: expected 3 repos, got %d", sort, len(results))
		}
	}
}

// ============== ListAllRepos Tests ==============

func TestListAllRepos_Limit1000(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "limituser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Create 100 repos (testing the limit is applied, not necessarily hitting 1000)
	for i := 0; i < 100; i++ {
		_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: "limituser",
			Name:       "limit-repo-" + string(rune('0'+i%10)) + string(rune('0'+i/10)),
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

	// Should return all 100 (under the 1000 limit)
	if len(results) != 100 {
		t.Errorf("expected 100 repos, got %d", len(results))
	}
}

func TestListAllRepos_PreloadOwner(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "preloaduser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "preloaduser",
		Name:       "preload-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	results, err := svc.ListAllRepos(ctx)
	if err != nil {
		t.Fatalf("ListAllRepos: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(results))
	}

	// Owner should be preloaded
	if results[0].Owner.Login != "preloaduser" {
		t.Errorf("expected owner to be preloaded, got %q", results[0].Owner.Login)
	}
	if results[0].Owner.ID == 0 {
		t.Error("expected owner ID to be populated")
	}
}

// ============== TransferRepo Tests ==============

func TestTransferRepo_Success(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "srcowner", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create src owner: %v", err)
	}
	if err := svc.DB.Create(&db.User{Login: "dstowner", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create dst owner: %v", err)
	}

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "srcowner",
		Name:       "transfer-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	transferred, err := svc.TransferRepo(ctx, "srcowner/transfer-repo", "dstowner")
	if err != nil {
		t.Fatalf("TransferRepo: %v", err)
	}

	if transferred.FullName != "dstowner/transfer-repo" {
		t.Errorf("expected full_name dstowner/transfer-repo, got %q", transferred.FullName)
	}
	if transferred.Owner.Login != "dstowner" {
		t.Errorf("expected owner dstowner, got %q", transferred.Owner.Login)
	}

	// Old name should redirect to new repo via repo_redirects
	oldRepo, err := svc.GetRepo(ctx, "srcowner/transfer-repo")
	if err != nil {
		t.Errorf("expected old name to redirect, got: %v", err)
	}
	if oldRepo.ID != transferred.ID {
		t.Errorf("expected old name to redirect to same repo ID %d, got %d", transferred.ID, oldRepo.ID)
	}

	// Git path should be moved
	if !svc.Git.Exists(ctx, "dstowner/transfer-repo") {
		t.Error("expected new git path to exist")
	}
	if svc.Git.Exists(ctx, "srcowner/transfer-repo") {
		t.Error("expected old git path to be gone")
	}
}

func TestTransferRepo_MissingTargetOwnerFails(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "transferuser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "transferuser",
		Name:       "repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	_, err = svc.TransferRepo(ctx, "transferuser/repo", "newowner")
	if !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound when target owner does not exist, got %v", err)
	}
}

func TestTransferRepo_TargetRepoAlreadyExistsReturnsConflict(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "srcowner", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create src owner: %v", err)
	}
	if err := svc.DB.Create(&db.User{Login: "dstowner", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create dst owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "srcowner",
		Name:       "transfer-repo",
		AddReadme:  true,
	}); err != nil {
		t.Fatalf("create source repo: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "dstowner",
		Name:       "transfer-repo",
		AddReadme:  true,
	}); err != nil {
		t.Fatalf("create destination repo: %v", err)
	}

	_, err := svc.TransferRepo(ctx, "srcowner/transfer-repo", "dstowner")
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("expected ErrConflict when destination repo already exists, got %v", err)
	}

	if !svc.Git.Exists(ctx, "srcowner/transfer-repo") {
		t.Fatal("expected source git path to remain after conflict")
	}
	if !svc.Git.Exists(ctx, "dstowner/transfer-repo") {
		t.Fatal("expected destination git path to remain after conflict")
	}
}

func TestTransferRepo_TargetGitPathAlreadyExistsReturnsConflict(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "pathsrc", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create src owner: %v", err)
	}
	if err := svc.DB.Create(&db.User{Login: "pathdst", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create dst owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "pathsrc",
		Name:       "transfer-repo",
		AddReadme:  true,
	}); err != nil {
		t.Fatalf("create source repo: %v", err)
	}

	dstPath, err := svc.Git.GetRepoPath(ctx, "pathdst/transfer-repo")
	if err != nil {
		t.Fatalf("get destination path: %v", err)
	}
	if err := os.MkdirAll(dstPath, 0o750); err != nil {
		t.Fatalf("create stale destination path: %v", err)
	}

	_, err = svc.TransferRepo(ctx, "pathsrc/transfer-repo", "pathdst")
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("expected ErrConflict when destination git path already exists, got %v", err)
	}

	if !svc.Git.Exists(ctx, "pathsrc/transfer-repo") {
		t.Fatal("expected source git path to remain after path conflict")
	}
}

func TestTransferRepo_RollbackOnFailure(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "rollsrc", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create src: %v", err)
	}
	if err := svc.DB.Create(&db.User{Login: "rolldst", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create dst: %v", err)
	}

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "rollsrc",
		Name:       "rollback-transfer",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	// Create autolink that will cause failure
	if err := svc.CreateAutolink(ctx, &db.Autolink{
		RepositoryFullName: "rollsrc/rollback-transfer",
		KeyPrefix:          "T-",
		URLTemplate:        "https://t.example.com/<num>",
	}); err != nil {
		t.Fatalf("create autolink: %v", err)
	}

	const cbName = "test:transfer_rollback"
	var failOnce atomic.Bool
	failOnce.Store(true)
	if err := svc.DB.Callback().Update().Before("gorm:update").Register(cbName, func(tx *gorm.DB) {
		if tx.Statement.Table == "autolinks" && failOnce.CompareAndSwap(true, false) {
			tx.AddError(errors.New("forced autolink update failure"))
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	defer func() {
		_ = svc.DB.Callback().Update().Remove(cbName)
	}()

	_, err = svc.TransferRepo(ctx, "rollsrc/rollback-transfer", "rolldst")
	if err == nil {
		t.Fatalf("expected TransferRepo to fail")
	}

	// Verify rollback
	if _, err := svc.GetRepo(ctx, "rollsrc/rollback-transfer"); err != nil {
		t.Fatalf("expected old repo to exist: %v", err)
	}
	if _, err := svc.GetRepo(ctx, "rolldst/rollback-transfer"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected new repo to not exist: %v", err)
	}

	if !svc.Git.Exists(ctx, "rollsrc/rollback-transfer") {
		t.Error("expected old git path to exist")
	}
	if svc.Git.Exists(ctx, "rolldst/rollback-transfer") {
		t.Error("expected new git path to be removed")
	}
}

func TestTransferRepo_UpdatesAutolink(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "autolinksrc", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create src: %v", err)
	}
	if err := svc.DB.Create(&db.User{Login: "autolinkdst", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create dst: %v", err)
	}

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "autolinksrc",
		Name:       "autolink-repo",
		AddReadme:  true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	if err := svc.CreateAutolink(ctx, &db.Autolink{
		RepositoryFullName: "autolinksrc/autolink-repo",
		KeyPrefix:          "AUTO-",
		URLTemplate:        "https://auto.example.com/<num>",
	}); err != nil {
		t.Fatalf("create autolink: %v", err)
	}

	_, err = svc.TransferRepo(ctx, "autolinksrc/autolink-repo", "autolinkdst")
	if err != nil {
		t.Fatalf("TransferRepo: %v", err)
	}

	autolinks, err := svc.ListAutolinks(ctx, "autolinkdst/autolink-repo")
	if err != nil {
		t.Fatalf("list autolinks: %v", err)
	}
	if len(autolinks) != 1 {
		t.Fatalf("expected 1 autolink, got %d", len(autolinks))
	}
	if autolinks[0].RepositoryFullName != "autolinkdst/autolink-repo" {
		t.Errorf("expected autolink to be updated, got %q", autolinks[0].RepositoryFullName)
	}
}
