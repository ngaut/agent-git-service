package rest_test

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/service"
	"gh-server/internal/testharness"
)

func TestSearchCommitsQualifiers(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "commit-search-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	if _, err := h.Svc.Git.WriteFile(ctx, "testuser/commit-search-repo", "main", "file.txt", "fix search", []byte("content")); err != nil {
		t.Fatalf("write file: %v", err)
	}

	t.Run("keyword with committer qualifier", func(t *testing.T) {
		q := url.QueryEscape("fix committer:gh-server")
		w := h.DoREST(t, "GET", "/api/v3/search/commits?q="+q, nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		assertSearchHasResults(t, body)
	})

	t.Run("qualifier only", func(t *testing.T) {
		q := url.QueryEscape("committer:gh-server")
		w := h.DoREST(t, "GET", "/api/v3/search/commits?q="+q, nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		assertSearchHasResults(t, body)
	})
}

func TestSearchCommitsMultipleRepos(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	repoNames := []string{"commit-search-repo-one", "commit-search-repo-two"}
	for _, name := range repoNames {
		if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: h.User.Login,
			Name:       name,
			AutoInit:   true,
		}); err != nil {
			t.Fatalf("seed repo %s: %v", name, err)
		}
	}

	token := "multi-repo-commit-token"
	for _, name := range repoNames {
		if _, err := h.Svc.Git.WriteFile(ctx, fmt.Sprintf("%s/%s", h.User.Login, name), "main", "file.txt", token, []byte("content")); err != nil {
			t.Fatalf("write file in %s: %v", name, err)
		}
	}

	query := fmt.Sprintf("%s repo:%s/%s repo:%s/%s", token, h.User.Login, repoNames[0], h.User.Login, repoNames[1])
	q := url.QueryEscape(query)
	w := h.DoREST(t, "GET", "/api/v3/search/commits?q="+q, nil)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := testharness.DecodeJSON(t, w)
	assertSearchHasResults(t, body)
	assertSearchHasRepos(t, body, []string{
		fmt.Sprintf("%s/%s", h.User.Login, repoNames[0]),
		fmt.Sprintf("%s/%s", h.User.Login, repoNames[1]),
	})
}

func TestSearchCodeMultipleRepos(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	repoNames := []string{"code-search-repo-one", "code-search-repo-two"}
	for _, name := range repoNames {
		if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: h.User.Login,
			Name:       name,
			AutoInit:   true,
		}); err != nil {
			t.Fatalf("seed repo %s: %v", name, err)
		}
	}

	token := "multi-repo-code-token"
	for _, name := range repoNames {
		if _, err := h.Svc.Git.WriteFile(ctx, fmt.Sprintf("%s/%s", h.User.Login, name), "main", "file.txt", "add token", []byte("content "+token)); err != nil {
			t.Fatalf("write file in %s: %v", name, err)
		}
	}

	query := fmt.Sprintf("%s repo:%s/%s repo:%s/%s", token, h.User.Login, repoNames[0], h.User.Login, repoNames[1])
	q := url.QueryEscape(query)
	w := h.DoREST(t, "GET", "/api/v3/search/code?q="+q, nil)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := testharness.DecodeJSON(t, w)
	assertSearchHasResults(t, body)
	assertSearchHasRepos(t, body, []string{
		fmt.Sprintf("%s/%s", h.User.Login, repoNames[0]),
		fmt.Sprintf("%s/%s", h.User.Login, repoNames[1]),
	})
}

func TestSearchCodeExcludesInaccessibleRepos(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	visibleRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "visible-code-search-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("seed visible repo: %v", err)
	}

	otherOwner := db.User{Login: "hidden-owner", Name: "hidden-owner", Type: db.TypeUser}
	if err := h.DB.Create(&otherOwner).Error; err != nil {
		t.Fatalf("create hidden owner: %v", err)
	}

	hiddenRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: otherOwner.Login,
		Name:       "hidden-code-search-repo",
		AutoInit:   true,
		Private:    true,
	})
	if err != nil {
		t.Fatalf("seed hidden repo: %v", err)
	}

	token := "viewer-scoped-code-search-token"
	if _, err := h.Svc.Git.WriteFile(ctx, visibleRepo.FullName, "main", "visible.txt", "add visible", []byte("content "+token)); err != nil {
		t.Fatalf("write visible file: %v", err)
	}
	if _, err := h.Svc.Git.WriteFile(ctx, hiddenRepo.FullName, "main", "hidden.txt", "add hidden", []byte("content "+token)); err != nil {
		t.Fatalf("write hidden file: %v", err)
	}

	q := url.QueryEscape(token)
	w := h.DoREST(t, "GET", "/api/v3/search/code?q="+q, nil)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := testharness.DecodeJSON(t, w)
	assertSearchHasResults(t, body)
	assertSearchHasRepos(t, body, []string{visibleRepo.FullName})
}

func TestSearchCodeExcludesInaccessiblePrivateRepos(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	viewer := db.User{Login: "viewer", Name: "viewer", Type: db.TypeUser}
	if err := h.DB.Create(&viewer).Error; err != nil {
		t.Fatalf("seed viewer: %v", err)
	}
	viewerToken := "viewer-search-token"
	if err := h.DB.Create(&db.Token{UserID: viewer.ID, Value: viewerToken}).Error; err != nil {
		t.Fatalf("seed viewer token: %v", err)
	}

	owner := db.User{Login: "owner", Name: "owner", Type: db.TypeUser}
	if err := h.DB.Create(&owner).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	visibleRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: viewer.Login,
		Name:       "viewer-visible",
		Private:    true,
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("seed visible repo: %v", err)
	}
	hiddenRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "owner-hidden",
		Private:    true,
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("seed hidden repo: %v", err)
	}

	token := "search-visibility-token"
	if _, err := h.Svc.Git.WriteFile(ctx, visibleRepo.FullName, "main", "visible.txt", "add visible token", []byte("content "+token)); err != nil {
		t.Fatalf("write visible file: %v", err)
	}
	if _, err := h.Svc.Git.WriteFile(ctx, hiddenRepo.FullName, "main", "hidden.txt", "add hidden token", []byte("content "+token)); err != nil {
		t.Fatalf("write hidden file: %v", err)
	}

	q := url.QueryEscape(token)
	w := h.DoRESTWithToken(t, "GET", "/api/v3/search/code?q="+q, viewerToken)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := testharness.DecodeJSON(t, w)
	got := searchResultRepoFullNames(t, body)
	assertStringSetEqual(t, got, []string{visibleRepo.FullName})
}

// TestSearchCodeMultiTokenQuery verifies that multi-token queries use AND semantics
// matching GitHub API behavior (issue #519).
func TestSearchCodeMultiTokenQuery(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	repoName := "multi-token-test-repo"
	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       repoName,
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	// Create files with different content
	// file1.txt contains both "hello" and "world"
	if _, err := h.Svc.Git.WriteFile(ctx, fmt.Sprintf("%s/%s", h.User.Login, repoName), "main", "file1.txt", "add file1", []byte("hello world test")); err != nil {
		t.Fatalf("write file1: %v", err)
	}
	// file2.txt contains only "hello"
	if _, err := h.Svc.Git.WriteFile(ctx, fmt.Sprintf("%s/%s", h.User.Login, repoName), "main", "file2.txt", "add file2", []byte("hello test")); err != nil {
		t.Fatalf("write file2: %v", err)
	}
	// file3.txt contains only "world"
	if _, err := h.Svc.Git.WriteFile(ctx, fmt.Sprintf("%s/%s", h.User.Login, repoName), "main", "file3.txt", "add file3", []byte("world test")); err != nil {
		t.Fatalf("write file3: %v", err)
	}

	// Multi-token query "hello world" should only match file1.txt (AND semantics)
	query := url.QueryEscape("hello world repo:" + h.User.Login + "/" + repoName)
	w := h.DoREST(t, "GET", "/api/v3/search/code?q="+query, nil)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := testharness.DecodeJSON(t, w)
	items := body["items"].([]any)

	if len(items) == 0 {
		t.Fatal("expected results for multi-token query")
	}

	// Verify only file1.txt is returned (contains both "hello" AND "world")
	for _, item := range items {
		itemMap := item.(map[string]any)
		path := itemMap["path"].(string)
		if !strings.HasSuffix(path, "file1.txt") {
			t.Errorf("expected only file1.txt in results (AND semantics), got %s", path)
		}
	}
}

func TestSearchReposAdvancedFilters(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	archivedRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "archived-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("seed archived repo: %v", err)
	}

	activeRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "active-repo",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("seed active repo: %v", err)
	}

	if err := h.Svc.UpdateRepoTopics(ctx, archivedRepo.FullName, "alpha,beta,gamma"); err != nil {
		t.Fatalf("update archived topics: %v", err)
	}
	if err := h.Svc.UpdateRepoTopics(ctx, activeRepo.FullName, "alpha"); err != nil {
		t.Fatalf("update active topics: %v", err)
	}

	oldCreated := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)
	newCreated := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	oldPushed := time.Date(2020, 2, 3, 0, 0, 0, 0, time.UTC)
	newPushed := time.Date(2024, 2, 3, 0, 0, 0, 0, time.UTC)

	if err := h.DB.Model(&db.Repository{}).Where("id = ?", archivedRepo.ID).UpdateColumns(map[string]any{
		"archived":   true,
		"created_at": oldCreated,
		"updated_at": oldCreated,
		"pushed_at":  oldPushed,
	}).Error; err != nil {
		t.Fatalf("update archived repo metadata: %v", err)
	}

	if err := h.DB.Model(&db.Repository{}).Where("id = ?", activeRepo.ID).UpdateColumns(map[string]any{
		"archived":   false,
		"created_at": newCreated,
		"updated_at": newCreated,
		"pushed_at":  newPushed,
	}).Error; err != nil {
		t.Fatalf("update active repo metadata: %v", err)
	}

	t.Run("archived qualifier", func(t *testing.T) {
		got := searchRepoFullNames(t, h, "archived:true")
		assertStringSetEqual(t, got, []string{archivedRepo.FullName})
	})

	t.Run("created qualifier", func(t *testing.T) {
		got := searchRepoFullNames(t, h, "created:>=2023-01-01")
		assertStringSetEqual(t, got, []string{activeRepo.FullName})
	})

	t.Run("pushed qualifier", func(t *testing.T) {
		got := searchRepoFullNames(t, h, "pushed:>=2023-01-01")
		assertStringSetEqual(t, got, []string{activeRepo.FullName})
	})

	t.Run("updated qualifier", func(t *testing.T) {
		got := searchRepoFullNames(t, h, "updated:<=2021-01-01")
		assertStringSetEqual(t, got, []string{archivedRepo.FullName})
	})

	t.Run("topics count qualifier", func(t *testing.T) {
		got := searchRepoFullNames(t, h, "topics:>=3")
		assertStringSetEqual(t, got, []string{archivedRepo.FullName})
	})
}

func TestSearchReposSizeFilter(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	smallRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "size-small",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("seed small repo: %v", err)
	}

	largeRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "size-large",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("seed large repo: %v", err)
	}

	if _, err := h.Svc.Git.WriteFile(ctx, smallRepo.FullName, "main", "small.txt", "add small", []byte("small content")); err != nil {
		t.Fatalf("write small file: %v", err)
	}

	largeContent := bytes.Repeat([]byte("a"), 1024*1024)
	if _, err := h.Svc.Git.WriteFile(ctx, largeRepo.FullName, "main", "large.bin", "add large", largeContent); err != nil {
		t.Fatalf("write large file: %v", err)
	}

	sizeSmall := h.Svc.GitDiskUsageKB(ctx, smallRepo.FullName)
	sizeLarge := h.Svc.GitDiskUsageKB(ctx, largeRepo.FullName)
	if sizeLarge <= sizeSmall {
		extraContent := bytes.Repeat([]byte("b"), 1024*1024)
		if _, err := h.Svc.Git.WriteFile(ctx, largeRepo.FullName, "main", "large2.bin", "add larger", extraContent); err != nil {
			t.Fatalf("write extra large file: %v", err)
		}
		sizeLarge = h.Svc.GitDiskUsageKB(ctx, largeRepo.FullName)
	}
	if sizeLarge <= sizeSmall {
		t.Fatalf("expected large repo size > small repo size, got small=%dKB large=%dKB", sizeSmall, sizeLarge)
	}

	query := fmt.Sprintf("size:>%d", sizeSmall)
	got := searchRepoFullNames(t, h, query)
	assertStringSetEqual(t, got, []string{largeRepo.FullName})
}

func TestSearchReposStarsForksLanguageLicenseSort(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	repoA, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "search-repo-a",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("seed repoA: %v", err)
	}
	repoB, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "search-repo-b",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("seed repoB: %v", err)
	}
	repoC, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "search-repo-c",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("seed repoC: %v", err)
	}

	if err := h.DB.Model(&db.Repository{}).Where("id = ?", repoA.ID).Updates(map[string]any{
		"language": "Go",
		"license":  "mit",
	}).Error; err != nil {
		t.Fatalf("update repoA metadata: %v", err)
	}
	if err := h.DB.Model(&db.Repository{}).Where("id = ?", repoB.ID).Updates(map[string]any{
		"language": "Python",
		"license":  "apache-2.0",
	}).Error; err != nil {
		t.Fatalf("update repoB metadata: %v", err)
	}

	starUsers := []string{"staruser1", "staruser2"}
	for _, login := range starUsers {
		if err := h.DB.Create(&db.User{Login: login, Name: login, Type: db.TypeUser}).Error; err != nil {
			t.Fatalf("create star user %s: %v", login, err)
		}
	}
	if err := h.Svc.StarRepo(ctx, repoA.FullName, "staruser1"); err != nil {
		t.Fatalf("star repoA by user1: %v", err)
	}
	if err := h.Svc.StarRepo(ctx, repoA.FullName, "staruser2"); err != nil {
		t.Fatalf("star repoA by user2: %v", err)
	}
	if err := h.Svc.StarRepo(ctx, repoB.FullName, "staruser1"); err != nil {
		t.Fatalf("star repoB by user1: %v", err)
	}
	if _, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, h.User), "forkorg"); err != nil {
		t.Fatalf("create forkorg: %v", err)
	}

	fork1, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "forkorg",
		Name:       "fork-one",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("seed fork1: %v", err)
	}
	fork2, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "forkorg",
		Name:       "fork-two",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("seed fork2: %v", err)
	}
	if err := h.DB.Model(&db.Repository{}).Where("id IN ?", []uint{fork1.ID, fork2.ID}).Updates(map[string]any{
		"parent_id": repoC.ID,
		"fork":      true,
	}).Error; err != nil {
		t.Fatalf("update fork parents: %v", err)
	}

	t.Run("language qualifier", func(t *testing.T) {
		got := searchRepoFullNames(t, h, "language:Go user:"+h.User.Login)
		assertStringSetEqual(t, got, []string{repoA.FullName})
	})

	t.Run("license qualifier", func(t *testing.T) {
		got := searchRepoFullNames(t, h, "license:apache-2.0 user:"+h.User.Login)
		assertStringSetEqual(t, got, []string{repoB.FullName})
	})

	t.Run("stars qualifier", func(t *testing.T) {
		got := searchRepoFullNames(t, h, "stars:>=2 user:"+h.User.Login)
		assertStringSetEqual(t, got, []string{repoA.FullName})
	})

	t.Run("forks qualifier", func(t *testing.T) {
		got := searchRepoFullNames(t, h, "forks:>=2 user:"+h.User.Login)
		assertStringSetEqual(t, got, []string{repoC.FullName})
	})

	t.Run("sort by stars desc", func(t *testing.T) {
		got := searchRepoFullNames(t, h, "sort:stars order:desc user:"+h.User.Login)
		assertStringSliceEqual(t, got, []string{repoA.FullName, repoB.FullName, repoC.FullName})
	})

	t.Run("sort by REST query params", func(t *testing.T) {
		q := url.QueryEscape("user:" + h.User.Login)
		got := searchRepoFullNamesURL(t, h, "/api/v3/search/repositories?q="+q+"&sort=stars&order=asc")
		assertStringSliceEqual(t, got, []string{repoC.FullName, repoB.FullName, repoA.FullName})
	})

	t.Run("count fields are populated", func(t *testing.T) {
		w := h.DoREST(t, "GET", "/api/v3/search/repositories?q=user:"+h.User.Login, nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		items, ok := body["items"].([]any)
		if !ok {
			t.Fatalf("expected items to be an array, got %T", body["items"])
		}
		if len(items) == 0 {
			t.Fatal("expected non-empty items")
		}

		// Find repoA (should have 2 stars) - items are repo objects directly
		var repoAItem map[string]any
		for _, item := range items {
			itemMap := item.(map[string]any)
			if itemMap["full_name"].(string) == repoA.FullName {
				repoAItem = itemMap
				break
			}
		}
		if repoAItem == nil {
			t.Fatal("repoA not found in results")
		}

		// Verify count fields
		if stars := repoAItem["stargazers_count"]; stars != float64(2) {
			t.Errorf("repoA stargazers_count: expected 2, got %v", stars)
		}
		if forks := repoAItem["forks_count"]; forks != float64(0) {
			t.Errorf("repoA forks_count: expected 0, got %v", forks)
		}
		if watchers := repoAItem["watchers_count"]; watchers != float64(2) {
			t.Errorf("repoA watchers_count: expected 2, got %v", watchers)
		}
		if forks := repoAItem["forks"]; forks != float64(0) {
			t.Errorf("repoA forks: expected 0, got %v", forks)
		}
		if watchers := repoAItem["watchers"]; watchers != float64(2) {
			t.Errorf("repoA watchers: expected 2, got %v", watchers)
		}

		// Find repoC (should have 2 forks)
		var repoCItem map[string]any
		for _, item := range items {
			itemMap := item.(map[string]any)
			if itemMap["full_name"].(string) == repoC.FullName {
				repoCItem = itemMap
				break
			}
		}
		if repoCItem == nil {
			t.Fatal("repoC not found in results")
		}

		// Verify fork count for repoC
		if forks := repoCItem["forks_count"]; forks != float64(2) {
			t.Errorf("repoC forks_count: expected 2, got %v", forks)
		}
		if forks := repoCItem["forks"]; forks != float64(2) {
			t.Errorf("repoC forks: expected 2, got %v", forks)
		}
	})
}

func assertSearchHasResults(t *testing.T, body map[string]any) {
	t.Helper()
	totalRaw, ok := body["total_count"].(float64)
	if !ok {
		t.Fatalf("expected total_count to be a number, got %T", body["total_count"])
	}
	if int(totalRaw) == 0 {
		t.Fatalf("expected search results, got total_count=0")
	}
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("expected items to be an array, got %T", body["items"])
	}
	if len(items) == 0 {
		t.Fatalf("expected items to be non-empty")
	}
}

func assertSearchHasRepos(t *testing.T, body map[string]any, expected []string) {
	t.Helper()
	found := make(map[string]struct{})
	for _, fullName := range searchResultRepoFullNames(t, body) {
		found[fullName] = struct{}{}
	}

	for _, repo := range expected {
		if _, ok := found[repo]; !ok {
			t.Fatalf("expected repo %s in results, got %v", repo, keys(found))
		}
	}
}

func searchResultRepoFullNames(t *testing.T, body map[string]any) []string {
	t.Helper()
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("expected items to be an array, got %T", body["items"])
	}
	if len(items) == 0 {
		return []string{}
	}

	out := make([]string, 0, len(items))
	for i, item := range items {
		itemMap, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("item %d is not an object", i)
		}
		repoAny, ok := itemMap["repository"]
		if !ok {
			t.Fatalf("item %d missing repository field", i)
		}
		repoMap, ok := repoAny.(map[string]any)
		if !ok {
			t.Fatalf("item %d repository is not an object", i)
		}
		fullName, ok := repoMap["full_name"].(string)
		if !ok {
			t.Fatalf("item %d repository full_name is not a string", i)
		}
		out = append(out, fullName)
	}
	return out
}

func keys(in map[string]struct{}) []string {
	out := make([]string, 0, len(in))
	for k := range in {
		out = append(out, k)
	}
	return out
}

func searchRepoFullNames(t *testing.T, h *testharness.Harness, query string) []string {
	t.Helper()
	q := url.QueryEscape(query)
	return searchRepoFullNamesURL(t, h, "/api/v3/search/repositories?q="+q)
}

func searchRepoFullNamesURL(t *testing.T, h *testharness.Harness, path string) []string {
	t.Helper()
	w := h.DoREST(t, "GET", path, nil)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := testharness.DecodeJSON(t, w)
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("expected items to be an array, got %T", body["items"])
	}
	if len(items) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(items))
	for i, item := range items {
		itemMap, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("item %d is not an object", i)
		}
		fullName, ok := itemMap["full_name"].(string)
		if !ok {
			t.Fatalf("item %d full_name is not a string", i)
		}
		out = append(out, fullName)
	}
	return out
}

func assertStringSetEqual(t *testing.T, got []string, expected []string) {
	t.Helper()
	gotSet := make(map[string]struct{}, len(got))
	for _, v := range got {
		gotSet[v] = struct{}{}
	}
	expectedSet := make(map[string]struct{}, len(expected))
	for _, v := range expected {
		expectedSet[v] = struct{}{}
	}
	if len(gotSet) != len(expectedSet) {
		t.Fatalf("expected %d results, got %d (expected=%v got=%v)", len(expectedSet), len(gotSet), expected, got)
	}
	for v := range expectedSet {
		if _, ok := gotSet[v]; !ok {
			t.Fatalf("missing expected %s in results (expected=%v got=%v)", v, expected, got)
		}
	}
}

func assertStringSliceEqual(t *testing.T, got []string, expected []string) {
	t.Helper()
	if len(got) != len(expected) {
		t.Fatalf("expected %d results, got %d (expected=%v got=%v)", len(expected), len(got), expected, got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Fatalf("unexpected order at %d: expected %s, got %s (expected=%v got=%v)", i, expected[i], got[i], expected, got)
		}
	}
}

// TestSearchCommits_UserOrgVisibilityFilters tests user, org, and visibility filters
func TestSearchCommits_UserOrgVisibilityFilters(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	// Create repos with different visibility settings
	publicRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "public-commit-repo",
		Private:    false,
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("create public repo: %v", err)
	}

	privateRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "private-commit-repo",
		Private:    true,
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("create private repo: %v", err)
	}

	// Create an org and org-owned repo
	orgLogin := "testorg-commit"
	if _, err := h.Svc.EnsureOrg(ctx, orgLogin); err != nil {
		t.Fatalf("ensure org: %v", err)
	}
	orgRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: orgLogin,
		Name:       "org-commit-repo",
		Private:    false,
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("create org repo: %v", err)
	}

	// Add commits to each repo with unique tokens
	publicToken := "public-commit-token-xyz"
	privateToken := "private-commit-token-xyz"
	orgToken := "org-commit-token-xyz"

	if _, err := h.Svc.Git.WriteFile(ctx, publicRepo.FullName, "main", "public.txt", publicToken, []byte("public content")); err != nil {
		t.Fatalf("write public file: %v", err)
	}
	if _, err := h.Svc.Git.WriteFile(ctx, privateRepo.FullName, "main", "private.txt", privateToken, []byte("private content")); err != nil {
		t.Fatalf("write private file: %v", err)
	}
	if _, err := h.Svc.Git.WriteFile(ctx, orgRepo.FullName, "main", "org.txt", orgToken, []byte("org content")); err != nil {
		t.Fatalf("write org file: %v", err)
	}

	t.Run("user filter returns only user-owned repos", func(t *testing.T) {
		q := url.QueryEscape(fmt.Sprintf("%s user:%s", publicToken, h.User.Login))
		w := h.DoREST(t, "GET", "/api/v3/search/commits?q="+q, nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		items := body["items"].([]any)
		if len(items) == 0 {
			t.Fatal("expected results for user filter")
		}
		// Verify all results are from user-owned repos
		for _, item := range items {
			itemMap := item.(map[string]any)
			repo := itemMap["repository"].(map[string]any)
			owner := repo["owner"].(map[string]any)
			if owner["login"] != h.User.Login {
				t.Errorf("expected user %s, got %s", h.User.Login, owner["login"])
			}
		}
	})

	t.Run("org filter returns only org-owned repos", func(t *testing.T) {
		q := url.QueryEscape(fmt.Sprintf("%s org:%s", orgToken, orgLogin))
		w := h.DoREST(t, "GET", "/api/v3/search/commits?q="+q, nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		items := body["items"].([]any)
		if len(items) == 0 {
			t.Fatal("expected results for org filter")
		}
		// Verify all results are from org-owned repos
		for _, item := range items {
			itemMap := item.(map[string]any)
			repo := itemMap["repository"].(map[string]any)
			owner := repo["owner"].(map[string]any)
			if owner["login"] != orgLogin {
				t.Errorf("expected org %s, got %s", orgLogin, owner["login"])
			}
		}
	})

	t.Run("visibility public returns only public repos", func(t *testing.T) {
		q := url.QueryEscape(fmt.Sprintf("commit visibility:public"))
		w := h.DoREST(t, "GET", "/api/v3/search/commits?q="+q, nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		items := body["items"].([]any)
		// Should include publicRepo and orgRepo, but not privateRepo
		for _, item := range items {
			itemMap := item.(map[string]any)
			repo := itemMap["repository"].(map[string]any)
			// Check that private repo is not included
			if repo["full_name"] == privateRepo.FullName {
				t.Errorf("visibility:public should not include private repo %s", privateRepo.FullName)
			}
		}
	})

	t.Run("visibility private returns only private repos", func(t *testing.T) {
		q := url.QueryEscape("commit visibility:private")
		w := h.DoREST(t, "GET", "/api/v3/search/commits?q="+q, nil)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := testharness.DecodeJSON(t, w)
		items := body["items"].([]any)
		// Should include privateRepo only
		for _, item := range items {
			itemMap := item.(map[string]any)
			repo := itemMap["repository"].(map[string]any)
			if repo["full_name"] != privateRepo.FullName {
				t.Errorf("visibility:private should only include %s, got %s", privateRepo.FullName, repo["full_name"])
			}
		}
	})
}
