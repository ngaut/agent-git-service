package transform_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
)

const testBase = "http://test.local"
const testHTMLBase = "https://test.local"

func init() {
	transform.Init(testBase)
}

var fixedTime = time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)

func testUser() db.User {
	return db.User{
		ID:        1,
		Login:     "alice",
		Name:      "Alice",
		Email:     "alice@example.com",
		Bio:       "dev",
		Type:      "User",
		SiteAdmin: false,
		CreatedAt: fixedTime,
		UpdatedAt: fixedTime,
	}
}

func testRepo() db.Repository {
	return db.Repository{
		ID:            10,
		Name:          "myrepo",
		FullName:      "alice/myrepo",
		Description:   "A test repo",
		Owner:         testUser(),
		Private:       false,
		Fork:          false,
		DefaultBranch: "main",
		Language:      "Go",
		HasIssues:     true,
		HasWiki:       true,
		HasProjects:   true,
		Topics:        "go,testing",
		CreatedAt:     fixedTime,
		UpdatedAt:     fixedTime,
	}
}

func TestUser(t *testing.T) {
	u := transform.User(testUser())

	assertField(t, u, "id", uint(1))
	assertField(t, u, "login", "alice")
	assertField(t, u, "name", "Alice")
	assertField(t, u, "email", "")
	assertField(t, u, "type", "User")
	assertField(t, u, "node_id", "VXNlcl8x") // base64("User_1")
	assertField(t, u, "avatar_url", testBase+"/avatars/alice")
	assertField(t, u, "url", testBase+"/api/v3/users/alice")
	assertField(t, u, "html_url", testHTMLBase+"/alice")
}

func TestUserPrivate(t *testing.T) {
	u := transform.UserPrivate(testUser())
	assertField(t, u, "email", "alice@example.com")
}

func TestUserOrganizationType(t *testing.T) {
	u := testUser()
	u.Type = "Organization"
	result := transform.User(u)
	assertField(t, result, "node_id", "T3JnYW5pemF0aW9uXzE=") // base64("Organization_1")
}

func TestRepo(t *testing.T) {
	r := transform.Repo(testRepo())

	assertField(t, r, "id", uint(10))
	assertField(t, r, "name", "myrepo")
	assertField(t, r, "full_name", "alice/myrepo")
	assertField(t, r, "default_branch", "main")
	assertField(t, r, "language", "Go")
	assertField(t, r, "clone_url", testBase+"/alice/myrepo.git")
	assertField(t, r, "html_url", testHTMLBase+"/alice/myrepo")
	assertField(t, r, "url", testBase+"/api/v3/repos/alice/myrepo")

	// Topics should be split
	topics, ok := r["topics"].([]string)
	if !ok {
		t.Fatal("expected topics to be []string")
	}
	if len(topics) != 2 || topics[0] != "go" || topics[1] != "testing" {
		t.Errorf("unexpected topics: %v", topics)
	}

	// Owner should be embedded
	owner, ok := r["owner"].(map[string]any)
	if !ok {
		t.Fatal("expected owner to be map")
	}
	if owner["login"] != "alice" {
		t.Errorf("expected owner login=alice, got %v", owner["login"])
	}
}

func TestWrap_IsolatesConcurrentState(t *testing.T) {
	t.Cleanup(func() { transform.Init(testBase) })

	type result struct {
		base string
		api  string
	}

	results := make(chan result, 2)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(2)

	run := func(base string) {
		transform.Wrap(base, func() {
			ready.Done()
			<-start
			results <- result{
				base: transform.Base(),
				api:  transform.APIBase(),
			}
		})
	}

	go run("http://one.local")
	go run("http://two.local")

	ready.Wait()
	close(start)

	got := []result{<-results, <-results}
	want := map[string]string{
		"http://one.local": "http://one.local/api/v3",
		"http://two.local": "http://two.local/api/v3",
	}
	for _, item := range got {
		if item.api != want[item.base] {
			t.Fatalf("state leaked across concurrent Wrap calls: base=%q api=%q want=%q", item.base, item.api, want[item.base])
		}
	}
}

func TestRepoEmptyTopics(t *testing.T) {
	r := testRepo()
	r.Topics = ""
	result := transform.Repo(r)
	topics := result["topics"].([]string)
	if len(topics) != 0 {
		t.Errorf("expected empty topics slice, got %v", topics)
	}
}

func TestRepoLicenseAndPages(t *testing.T) {
	r := testRepo()
	r.License = "MIT"
	result := transform.Repo(r, transform.RepoStats{HasPages: true})
	if result["has_pages"] != true {
		t.Fatalf("has_pages: got %v, want true", result["has_pages"])
	}
	license, ok := result["license"].(map[string]any)
	if !ok {
		t.Fatalf("license: expected object, got %T", result["license"])
	}
	if license["key"] != "mit" || license["spdx_id"] != "MIT" {
		t.Fatalf("license object mismatch: %#v", license)
	}
}

func TestRepoPushedAt(t *testing.T) {
	// Without PushedAt — should fall back to CreatedAt
	r := testRepo()
	result := transform.Repo(r)
	if result["pushed_at"] != fixedTime.Format(time.RFC3339) {
		t.Errorf("expected pushed_at to be CreatedAt, got %v", result["pushed_at"])
	}

	// With PushedAt
	pushTime := fixedTime.Add(24 * time.Hour)
	r.PushedAt = &pushTime
	result = transform.Repo(r)
	if result["pushed_at"] != pushTime.Format(time.RFC3339) {
		t.Errorf("expected pushed_at to be PushedAt, got %v", result["pushed_at"])
	}
}

func TestRepoExplicitPermissions(t *testing.T) {
	result := transform.Repo(testRepo(), transform.RepoStats{
		HasPermissions: true,
		Permissions: transform.RepoPermissions{
			Pull:     true,
			Triage:   true,
			Push:     false,
			Maintain: false,
			Admin:    false,
		},
	})

	perms, ok := result["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("permissions should be an object, got %T", result["permissions"])
	}
	if perms["pull"] != true || perms["triage"] != true || perms["push"] != false || perms["maintain"] != false || perms["admin"] != false {
		t.Fatalf("unexpected permissions: %#v", perms)
	}
	if result["security_and_analysis"] != nil {
		t.Fatalf("security_and_analysis should be hidden from non-admin viewers, got %#v", result["security_and_analysis"])
	}
}

func TestRepoAdminSecurityAndMergeDefaults(t *testing.T) {
	repo := testRepo()
	repo.AllowUpdateBranch = true
	result := transform.Repo(repo, transform.RepoStats{
		HasPermissions: true,
		Permissions: transform.RepoPermissions{
			Pull:     true,
			Triage:   true,
			Push:     true,
			Maintain: true,
			Admin:    true,
		},
	})

	assertField(t, result, "allow_update_branch", true)
	assertField(t, result, "use_squash_pr_title_as_default", false)
	assertField(t, result, "squash_merge_commit_title", "COMMIT_OR_PR_TITLE")
	assertField(t, result, "squash_merge_commit_message", "COMMIT_MESSAGES")
	assertField(t, result, "merge_commit_title", "MERGE_MESSAGE")
	assertField(t, result, "merge_commit_message", "PR_TITLE")
	assertField(t, result, "web_commit_signoff_required", false)

	security, ok := result["security_and_analysis"].(map[string]any)
	if !ok {
		t.Fatalf("security_and_analysis should be an object, got %T", result["security_and_analysis"])
	}
	for _, key := range []string{"advanced_security", "code_security", "secret_scanning", "secret_scanning_push_protection"} {
		status, ok := security[key].(map[string]any)
		if !ok {
			t.Fatalf("security_and_analysis.%s should be an object, got %T", key, security[key])
		}
		if status["status"] != "disabled" {
			t.Fatalf("security_and_analysis.%s.status: got %v, want disabled", key, status["status"])
		}
	}
}

func TestIssue(t *testing.T) {
	issue := db.Issue{
		ID:         100,
		Number:     42,
		Repository: testRepo(),
		Title:      "Bug report",
		Body:       "Something broke",
		State:      "open",
		Author:     testUser(),
		CreatedAt:  fixedTime,
		UpdatedAt:  fixedTime,
	}

	result := transform.Issue(issue, nil, transform.AuthorAssociationChecks{})

	assertField(t, result, "id", uint(100))
	assertField(t, result, "number", 42)
	assertField(t, result, "title", "Bug report")
	assertField(t, result, "state", "open")
	assertField(t, result, "url", testBase+"/api/v3/repos/alice/myrepo/issues/42")
	assertField(t, result, "html_url", testHTMLBase+"/alice/myrepo/issues/42")

	// closed_at should be nil for open issues
	if result["closed_at"] != nil {
		t.Errorf("expected closed_at=nil for open issue, got %v", result["closed_at"])
	}

	// Assignees should be empty list
	assignees := result["assignees"].([]any)
	if len(assignees) != 0 {
		t.Errorf("expected empty assignees, got %v", assignees)
	}
}

func TestIssueWithClosedAt(t *testing.T) {
	closedAt := fixedTime.Add(time.Hour)
	issue := db.Issue{
		ID:         101,
		Number:     43,
		Repository: testRepo(),
		Title:      "Fixed bug",
		State:      "closed",
		Author:     testUser(),
		ClosedAt:   &closedAt,
		CreatedAt:  fixedTime,
		UpdatedAt:  fixedTime,
	}
	result := transform.Issue(issue, nil, transform.AuthorAssociationChecks{})
	if result["closed_at"] != closedAt.Format(time.RFC3339) {
		t.Errorf("expected closed_at to be set, got %v", result["closed_at"])
	}
}

func TestMilestone(t *testing.T) {
	closedAt := fixedTime.Add(24 * time.Hour)
	ms := db.Milestone{
		ID:          300,
		Number:      7,
		Title:       "v1.0",
		Description: "release milestone",
		State:       "closed",
		CreatorID:   1,
		Creator:     testUser(),
		CreatedAt:   fixedTime,
		UpdatedAt:   fixedTime,
		ClosedAt:    &closedAt,
	}
	result := transform.Milestone(&ms, "alice/myrepo", transform.MilestoneCounts{
		OpenIssues:   3,
		ClosedIssues: 5,
	})

	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("Milestone() returned %T, expected map[string]any", result)
	}

	assertField(t, resultMap, "url", testBase+"/api/v3/repos/alice/myrepo/milestones/7")
	assertField(t, resultMap, "html_url", testHTMLBase+"/alice/myrepo/milestone/7")
	assertField(t, resultMap, "labels_url", testBase+"/api/v3/repos/alice/myrepo/milestones/7/labels")
	assertField(t, resultMap, "open_issues", int64(3))
	assertField(t, resultMap, "closed_issues", int64(5))
	assertField(t, resultMap, "closed_at", closedAt.Format(time.RFC3339))

	creator, ok := resultMap["creator"].(map[string]any)
	if !ok || creator["login"] != "alice" {
		t.Errorf("creator: expected login alice, got %v", resultMap["creator"])
	}
}

func TestIssueClosedBy(t *testing.T) {
	closedAt := fixedTime.Add(2 * time.Hour)
	issue := db.Issue{
		ID:            102,
		Number:        44,
		Repository:    testRepo(),
		Title:         "Closed issue",
		State:         "closed",
		Author:        testUser(),
		ClosedAt:      &closedAt,
		ClosedByLogin: "bob",
		CreatedAt:     fixedTime,
		UpdatedAt:     fixedTime,
	}

	closer := db.User{
		ID:        2,
		Login:     "bob",
		Name:      "Bob",
		Email:     "bob@example.com",
		Bio:       "dev",
		Type:      db.TypeUser,
		SiteAdmin: false,
		CreatedAt: fixedTime,
		UpdatedAt: fixedTime,
	}
	resolver := func(login string) (db.User, error) {
		if login != "bob" {
			return db.User{}, errors.New("not found")
		}
		return closer, nil
	}

	result := transform.Issue(issue, resolver, transform.AuthorAssociationChecks{})
	closedBy, ok := result["closed_by"].(map[string]any)
	if !ok || closedBy == nil {
		t.Fatalf("expected closed_by to be set, got %v", result["closed_by"])
	}
	assertField(t, closedBy, "login", "bob")
	assertField(t, closedBy, "id", uint(2))
	assertField(t, closedBy, "node_id", "VXNlcl8y") // base64("User_2")
}

func TestReactions(t *testing.T) {
	counts := map[string]int64{
		"+1":    2,
		"heart": 1,
		"weird": 3,
	}
	url := testBase + "/api/v3/repos/alice/myrepo/issues/43"
	result := transform.Reactions(url, counts)

	assertField(t, result, "url", url+"/reactions")
	assertField(t, result, "total_count", int64(6))
	assertField(t, result, "+1", int64(2))
	assertField(t, result, "heart", int64(1))
	assertField(t, result, "laugh", int64(0))
}

func TestPR(t *testing.T) {
	pr := db.PullRequest{
		ID:         200,
		Number:     5,
		Repository: testRepo(),
		Title:      "Add feature",
		Body:       "New stuff",
		State:      "open",
		Author:     testUser(),
		HeadRef:    "feature-branch",
		BaseRef:    "main",
		HeadSHA:    "abc123def456abc123def456abc123def456abc1",
		BaseSHA:    "def456abc123def456abc123def456abc123def4",
		CreatedAt:  fixedTime,
		UpdatedAt:  fixedTime,
	}

	result := transform.PR(pr, nil, transform.AuthorAssociationChecks{}, nil)

	assertField(t, result, "id", uint(200))
	assertField(t, result, "number", 5)
	assertField(t, result, "title", "Add feature")
	assertField(t, result, "state", "open")
	assertField(t, result, "draft", false)
	assertField(t, result, "merged", false)
	assertField(t, result, "url", testBase+"/api/v3/repos/alice/myrepo/pulls/5")

	head := result["head"].(map[string]any)
	if head["ref"] != "feature-branch" {
		t.Errorf("expected head ref=feature-branch, got %v", head["ref"])
	}
	if head["sha"] != "abc123def456abc123def456abc123def456abc1" {
		t.Errorf("unexpected head sha: %v", head["sha"])
	}

	base := result["base"].(map[string]any)
	if base["ref"] != "main" {
		t.Errorf("expected base ref=main, got %v", base["ref"])
	}
	if _, ok := result["reviewers"]; ok {
		t.Fatalf("expected legacy reviewers field to be absent")
	}
	reviewers, ok := result["requested_reviewers"].([]any)
	if !ok {
		t.Fatalf("expected requested_reviewers array, got %T", result["requested_reviewers"])
	}
	if len(reviewers) != 0 {
		t.Fatalf("expected empty requested_reviewers, got %v", reviewers)
	}
}

func TestPRMergedState(t *testing.T) {
	mergedAt := fixedTime.Add(2 * time.Hour)
	closedAt := fixedTime.Add(2 * time.Hour)
	pr := db.PullRequest{
		ID:         201,
		Number:     6,
		Repository: testRepo(),
		Title:      "Merged PR",
		State:      "open", // still "open" in DB — PR() should override to "closed"
		Author:     testUser(),
		HeadRef:    "feat",
		BaseRef:    "main",
		Merged:     true,
		MergedAt:   &mergedAt,
		ClosedAt:   &closedAt,
		CreatedAt:  fixedTime,
		UpdatedAt:  fixedTime,
	}

	result := transform.PR(pr, nil, transform.AuthorAssociationChecks{}, nil)
	assertField(t, result, "state", "closed")
	assertField(t, result, "merged", true)
	if result["merged_at"] != mergedAt.Format(time.RFC3339) {
		t.Errorf("expected merged_at to be set, got %v", result["merged_at"])
	}
}

func TestIssueFromPR_MergedState(t *testing.T) {
	mergedAt := fixedTime.Add(2 * time.Hour)
	closedAt := fixedTime.Add(2 * time.Hour)
	pr := db.PullRequest{
		ID:         203,
		Number:     8,
		Repository: testRepo(),
		Title:      "Merged PR via IssueFromPR",
		State:      "open",
		Author:     testUser(),
		HeadRef:    "feat",
		BaseRef:    "main",
		Merged:     true,
		MergedAt:   &mergedAt,
		ClosedAt:   &closedAt,
		CreatedAt:  fixedTime,
		UpdatedAt:  fixedTime,
	}

	result := transform.IssueFromPR(pr, nil, transform.AuthorAssociationChecks{})
	assertField(t, result, "state", "closed")
	// Check pull_request.merged_at is populated for merged PRs
	prMap, ok := result["pull_request"].(map[string]any)
	if !ok {
		t.Fatal("expected pull_request to be a map")
	}
	mergedAtVal := prMap["merged_at"]
	if mergedAtVal != mergedAt.Format(time.RFC3339) {
		t.Errorf("expected merged_at to be %q, got %v", mergedAt.Format(time.RFC3339), mergedAtVal)
	}
}

func TestPREmptySHADefaults(t *testing.T) {
	pr := db.PullRequest{
		ID:         202,
		Number:     7,
		Repository: testRepo(),
		Title:      "No SHA",
		State:      "open",
		Author:     testUser(),
		HeadRef:    "feat",
		BaseRef:    "main",
		HeadSHA:    "",
		BaseSHA:    "",
		CreatedAt:  fixedTime,
		UpdatedAt:  fixedTime,
	}

	result := transform.PR(pr, nil, transform.AuthorAssociationChecks{}, nil)
	head := result["head"].(map[string]any)
	base := result["base"].(map[string]any)
	zeros := "0000000000000000000000000000000000000000"
	if head["sha"] != zeros {
		t.Errorf("expected zero SHA for head, got %v", head["sha"])
	}
	if base["sha"] != zeros {
		t.Errorf("expected zero SHA for base, got %v", base["sha"])
	}
}

func TestBranch(t *testing.T) {
	result := transform.Branch("alice/myrepo", "main", "abc123")

	assertField(t, result, "name", "main")
	assertField(t, result, "protected", false)

	commit := result["commit"].(map[string]any)
	if commit["sha"] != "abc123" {
		t.Errorf("expected sha=abc123, got %v", commit["sha"])
	}
	if commit["url"] != testBase+"/api/v3/repos/alice/myrepo/commits/abc123" {
		t.Errorf("unexpected commit url: %v", commit["url"])
	}
}

func TestLabel(t *testing.T) {
	label := db.Label{
		ID:          5,
		Name:        "bug",
		Color:       "d73a4a",
		Description: "Something isn't working",
		Default:     true,
		Repository:  testRepo(),
	}

	result := transform.Label(label)
	assertField(t, result, "id", uint(5))
	assertField(t, result, "name", "bug")
	assertField(t, result, "color", "d73a4a")
	assertField(t, result, "default", true)
	assertField(t, result, "url", testBase+"/api/v3/repos/alice/myrepo/labels/bug")
}

func TestRelease(t *testing.T) {
	pubTime := fixedTime.Add(time.Hour)
	release := db.Release{
		ID:          50,
		Repository:  testRepo(),
		TagName:     "v1.0.0",
		Name:        "Release 1.0",
		Body:        "First release",
		Draft:       false,
		PreRelease:  false,
		Author:      testUser(),
		PublishedAt: &pubTime,
		Assets:      []db.ReleaseAsset{},
		CreatedAt:   fixedTime,
	}

	result := transform.Release(release)
	assertField(t, result, "id", uint(50))
	assertField(t, result, "tag_name", "v1.0.0")
	assertField(t, result, "name", "Release 1.0")
	assertField(t, result, "html_url", testHTMLBase+"/alice/myrepo/releases/tag/v1.0.0")

	assets := result["assets"].([]any)
	if len(assets) != 0 {
		t.Errorf("expected empty assets, got %d", len(assets))
	}
}

func TestReleaseNilPublishedAt(t *testing.T) {
	release := db.Release{
		ID:         52,
		Repository: testRepo(),
		TagName:    "v0.1.0",
		Name:       "Pre-release",
		Author:     testUser(),
		Assets:     []db.ReleaseAsset{},
		CreatedAt:  fixedTime,
		// PublishedAt intentionally nil
	}

	result := transform.Release(release)
	assertField(t, result, "published_at", "")
}

func TestReleaseWithAssets(t *testing.T) {
	release := db.Release{
		ID:         51,
		Repository: testRepo(),
		TagName:    "v2.0.0",
		Name:       "Release 2.0",
		Author:     testUser(),
		Assets: []db.ReleaseAsset{
			{
				ID:          1,
				Name:        "binary.tar.gz",
				ContentType: "application/gzip",
				Size:        1024,
				CreatedAt:   fixedTime,
				UpdatedAt:   fixedTime,
			},
		},
		CreatedAt: fixedTime,
	}

	result := transform.Release(release)
	assets := result["assets"].([]any)
	if len(assets) != 1 {
		t.Fatalf("expected 1 asset, got %d", len(assets))
	}
	asset := assets[0].(map[string]any)
	assertField(t, asset, "name", "binary.tar.gz")
	assertField(t, asset, "content_type", "application/gzip")
	assertField(t, asset, "size", int64(1024))
}

func TestIssueAssigneesMultiple(t *testing.T) {
	issue := db.Issue{
		ID:             102,
		Number:         44,
		Repository:     testRepo(),
		Title:          "Assigned issue",
		State:          "open",
		Author:         testUser(),
		AssigneeLogins: "alice,bob",
		CreatedAt:      fixedTime,
		UpdatedAt:      fixedTime,
	}

	assigneeUsers := map[string]db.User{
		"alice": testUser(),
		"bob": {
			ID:        2,
			Login:     "bob",
			Name:      "Bob",
			Email:     "bob@example.com",
			Bio:       "dev",
			Type:      db.TypeUser,
			SiteAdmin: false,
			CreatedAt: fixedTime,
			UpdatedAt: fixedTime,
		},
	}
	resolver := func(login string) (db.User, error) {
		u, ok := assigneeUsers[login]
		if !ok {
			return db.User{}, errors.New("not found")
		}
		return u, nil
	}

	result := transform.Issue(issue, resolver, transform.AuthorAssociationChecks{})
	assignees := result["assignees"].([]any)
	if len(assignees) != 2 {
		t.Fatalf("expected 2 assignees, got %d", len(assignees))
	}
	a0 := assignees[0].(map[string]any)
	a1 := assignees[1].(map[string]any)
	if a0["login"] != "alice" {
		t.Errorf("expected first assignee=alice, got %v", a0["login"])
	}
	if a1["login"] != "bob" {
		t.Errorf("expected second assignee=bob, got %v", a1["login"])
	}
	assertField(t, a0, "id", uint(1))
	assertField(t, a0, "node_id", "VXNlcl8x") // base64("User_1")
	assertField(t, a1, "id", uint(2))
	assertField(t, a1, "node_id", "VXNlcl8y") // base64("User_2")
	if a0["url"] != testBase+"/api/v3/users/alice" {
		t.Errorf("unexpected assignee url: %v", a0["url"])
	}
}

func TestPagesConfig(t *testing.T) {
	cfg := db.PagesConfig{
		SourceBranch:  "main",
		SourcePath:    "/docs",
		HTTPSEnforced: true,
		CreatedAt:     fixedTime,
		UpdatedAt:     fixedTime,
	}

	result := transform.PagesConfig("alice/myrepo", cfg)
	assertField(t, result, "html_url", testHTMLBase+"/pages/alice/myrepo")
	assertField(t, result, "status", "queued")

	cfg.CNAME = "docs.example.com"
	result = transform.PagesConfig("alice/myrepo", cfg)
	assertField(t, result, "html_url", "https://docs.example.com")
}

// TestGist_MalformedJSON verifies that malformed or empty Files JSON produces an
// empty map (not nil/panic) — the "warn + zero-value fallback" contract.
func TestGist_MalformedJSON(t *testing.T) {
	cases := []struct {
		name  string
		files string
	}{
		{"empty string", ""},
		{"invalid JSON", `{not json`},
		{"wrong type", `"just a string"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := db.Gist{
				ID:          "gist123",
				Description: "test",
				Public:      true,
				Owner:       testUser(),
				Files:       tc.files,
				CreatedAt:   fixedTime,
				UpdatedAt:   fixedTime,
			}

			result := transform.Gist(g)

			files, ok := result["files"].(map[string]any)
			if !ok {
				t.Fatal("expected files to be map[string]any")
			}
			if len(files) != 0 {
				t.Errorf("expected empty files map, got %v", files)
			}
			// Non-JSON fields should still be populated.
			assertField(t, result, "id", "gist123")
			assertField(t, result, "description", "test")
		})
	}
}

// TestRuleset_MalformedJSON verifies that malformed or empty Conditions/Rules JSON
// produces nil values (not panic) — the "warn + zero-value fallback" contract.
func TestRuleset_MalformedJSON(t *testing.T) {
	cases := []struct {
		name       string
		conditions string
		rules      string
	}{
		{"both empty", "", ""},
		{"both malformed", `{broken`, `[invalid`},
		{"conditions malformed", `not json`, `[{"type":"creation"}]`},
		{"rules malformed", `{"ref_name":{"include":["main"]}}`, `{bad`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rs := db.Ruleset{
				ID:             1,
				Name:           "test-rule",
				Target:         "branch",
				Enforcement:    "active",
				ConditionsJSON: tc.conditions,
				RulesJSON:      tc.rules,
				CreatedAt:      fixedTime,
				UpdatedAt:      fixedTime,
			}

			result := transform.Ruleset(rs, "owner/repo")

			// Non-JSON fields should always be populated.
			assertField(t, result, "name", "test-rule")
			assertField(t, result, "target", "branch")

			// Malformed/empty JSON fields should be nil, not panic.
			// Valid JSON that unmarshals successfully will be non-nil;
			// empty or malformed JSON should always produce nil.
			if tc.conditions == "" || tc.conditions == `{broken` || tc.conditions == `not json` {
				if result["conditions"] != nil {
					t.Errorf("expected conditions=nil for input %q, got %v", tc.conditions, result["conditions"])
				}
			} else {
				if result["conditions"] == nil {
					t.Errorf("expected conditions!=nil for valid input %q", tc.conditions)
				}
			}
			if tc.rules == "" || tc.rules == `[invalid` || tc.rules == `{bad` {
				if result["rules"] != nil {
					t.Errorf("expected rules=nil for input %q, got %v", tc.rules, result["rules"])
				}
			} else {
				if result["rules"] == nil {
					t.Errorf("expected rules!=nil for valid input %q", tc.rules)
				}
			}
		})
	}
}

// assertField is a test helper that compares a map field to an expected value.
func assertField(t *testing.T, m map[string]any, key string, want any) {
	t.Helper()
	got, ok := m[key]
	if !ok {
		t.Errorf("missing key %q", key)
		return
	}
	if got != want {
		t.Errorf("key %q: expected %v (%T), got %v (%T)", key, want, want, got, got)
	}
}
