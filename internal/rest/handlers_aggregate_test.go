package rest_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/ngaut/agent-git-service/internal/testharness"
)

func TestAggregateViewerAndRepoSummary(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "aggregate-summary")
	w := h.DoRESTJSON(t, "POST", "/api/ext/v1/user/orgs", map[string]any{"login": "aggregate-viewer-org"})
	assertStatusCode(t, w, http.StatusCreated)
	w = h.DoRESTJSON(t, "POST", "/api/v3/orgs/aggregate-viewer-org/repos", map[string]any{
		"name":      "metadata",
		"auto_init": true,
	})
	assertStatusCode(t, w, http.StatusCreated)

	w = h.DoREST(t, "GET", "/api/ext/v1/viewer/summary?include=user,repositories&per_page=1", nil)
	assertStatusCode(t, w, http.StatusOK)
	viewerSummary := testharness.DecodeJSON(t, w)
	user := viewerSummary["user"].(map[string]any)
	if user["login"] != "testuser" {
		t.Fatalf("viewer login = %v, want testuser", user["login"])
	}
	repos := viewerSummary["repositories"].(map[string]any)
	if repos["total_count"].(float64) < 1 {
		t.Fatalf("viewer repositories total_count = %v, want at least 1", repos["total_count"])
	}
	items := repos["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("viewer repositories page size = %d, want 1", len(items))
	}
	w = h.DoREST(t, "GET", "/api/ext/v1/viewer/summary?include=repositories&repo_affiliation=organization_member", nil)
	assertStatusCode(t, w, http.StatusOK)
	orgRepoSummary := testharness.DecodeJSON(t, w)
	orgRepos := orgRepoSummary["repositories"].(map[string]any)
	if orgRepos["total_count"].(float64) != 1 {
		t.Fatalf("organization_member repos total_count = %v, want 1", orgRepos["total_count"])
	}
	orgRepo := orgRepos["items"].([]any)[0].(map[string]any)
	if orgRepo["full_name"] != "aggregate-viewer-org/metadata" {
		t.Fatalf("organization_member repo = %v, want aggregate-viewer-org/metadata", orgRepo["full_name"])
	}

	w = h.DoREST(t, "GET", "/api/ext/v1/repos/testuser/aggregate-summary/summary?include=repo,viewer,counts", nil)
	assertStatusCode(t, w, http.StatusOK)
	repoSummary := testharness.DecodeJSON(t, w)
	repo := repoSummary["repository"].(map[string]any)
	if repo["full_name"] != "testuser/aggregate-summary" {
		t.Fatalf("repo full_name = %v, want testuser/aggregate-summary", repo["full_name"])
	}
	viewer := repoSummary["viewer"].(map[string]any)
	if viewer["permission"] != "admin" {
		t.Fatalf("repo viewer permission = %v, want admin", viewer["permission"])
	}
	if _, ok := repoSummary["counts"].(map[string]any); !ok {
		t.Fatalf("repo summary missing counts: %#v", repoSummary)
	}
}

func TestAggregateIssueThreadAndIssueListFilters(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "aggregate-issues")

	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/aggregate-issues/issues", map[string]any{
		"title": "Run: build worker",
		"body":  "worker body",
	})
	assertStatusCode(t, w, http.StatusCreated)
	w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/aggregate-issues/issues/1/comments", map[string]any{
		"body": "first comment",
	})
	assertStatusCode(t, w, http.StatusCreated)
	w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/aggregate-issues/issues", map[string]any{
		"title": "Chat: general",
		"body":  "chat body",
	})
	assertStatusCode(t, w, http.StatusCreated)

	w = h.DoREST(t, "GET", "/api/ext/v1/repos/testuser/aggregate-issues/issues/1/thread?comments_per_page=10", nil)
	assertStatusCode(t, w, http.StatusOK)
	thread := testharness.DecodeJSON(t, w)
	issue := thread["issue"].(map[string]any)
	if issue["title"] != "Run: build worker" {
		t.Fatalf("thread issue title = %v, want Run: build worker", issue["title"])
	}
	comments := thread["comments"].(map[string]any)
	if comments["total_count"].(float64) != 1 {
		t.Fatalf("thread comments total_count = %v, want 1", comments["total_count"])
	}

	query := url.Values{}
	query.Set("kind", "issue")
	query.Set("title_prefix", "Run: ")
	query.Set("include", "body")
	query.Set("fields", "number,title,body")
	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/aggregate-issues/issues?"+query.Encode(), nil)
	assertStatusCode(t, w, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, w)
	if len(rows) != 1 {
		t.Fatalf("filtered issue rows = %d, want 1; body: %s", len(rows), w.Body.String())
	}
	row := rows[0]
	if row["title"] != "Run: build worker" || row["body"] != "worker body" {
		t.Fatalf("filtered issue row = %#v", row)
	}
	if _, ok := row["user"]; ok {
		t.Fatalf("fields filter should omit user: %#v", row)
	}
}

func TestAggregateWikiCatalogAndBatch(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "aggregate-wiki")
	full := "testuser/aggregate-wiki"

	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/"+full+"/labels", map[string]any{
		"name":        "ops",
		"color":       "8be9fd",
		"description": "Operations",
	})
	assertStatusCode(t, w, http.StatusCreated)
	w = h.DoRESTJSON(t, "PUT", wikiPagePath(full, "home"), map[string]any{
		"body": "# Home\n\nwelcome",
	})
	assertStatusCode(t, w, http.StatusOK)
	w = h.DoRESTJSON(t, "PUT", wikiPagePath(full, "guides/setup"), map[string]any{
		"body": "# Setup\n\n0123456789abcdefghijklmnopqrstuvwxyz",
	})
	assertStatusCode(t, w, http.StatusOK)
	h.Svc.Wg.Wait()

	w = h.DoRESTJSON(t, "POST", wikiPageSubresourcePath(full, "guides/setup", "labels"), map[string]any{
		"labels": []string{"ops"},
	})
	assertStatusCode(t, w, http.StatusOK)
	h.Svc.Wg.Wait()

	w = h.DoREST(t, "GET", "/api/ext/v1/repos/"+full+"/wiki/catalog?include=pages,tree,labels&path=guides", nil)
	assertStatusCode(t, w, http.StatusOK)
	catalog := testharness.DecodeJSON(t, w)
	if catalog["total_count"].(float64) != 1 {
		t.Fatalf("catalog total_count = %v, want 1", catalog["total_count"])
	}
	labels := catalog["labels"].([]any)
	if len(labels) != 1 || labels[0] != "ops" {
		t.Fatalf("catalog labels = %#v, want [ops]", labels)
	}

	w = h.DoRESTJSON(t, "POST", "/api/ext/v1/repos/"+full+"/wiki/pages/batch", map[string]any{
		"slugs":      []string{"guides/setup", "missing"},
		"include":    []string{"body", "labels"},
		"body_limit": 8,
	})
	assertStatusCode(t, w, http.StatusOK)
	batch := testharness.DecodeJSON(t, w)
	items := batch["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("batch items = %d, want 1", len(items))
	}
	item := items[0].(map[string]any)
	if item["slug"] != "guides/setup" {
		t.Fatalf("batch item slug = %v, want guides/setup", item["slug"])
	}
	if item["body_truncated"] != true {
		t.Fatalf("batch item body_truncated = %v, want true", item["body_truncated"])
	}
	missing := batch["missing"].([]any)
	if len(missing) != 1 || missing[0] != "missing" {
		t.Fatalf("batch missing = %#v, want [missing]", missing)
	}
}

func TestAggregateOrgManagementSummary(t *testing.T) {
	h := testharness.New(t)

	w := h.DoRESTJSON(t, "POST", "/api/ext/v1/user/orgs", map[string]any{"login": "aggregate-org"})
	assertStatusCode(t, w, http.StatusCreated)
	w = h.DoRESTJSON(t, "POST", "/api/v3/orgs/aggregate-org/repos", map[string]any{
		"name":      "metadata",
		"auto_init": true,
	})
	assertStatusCode(t, w, http.StatusCreated)

	w = h.DoREST(t, "GET", "/api/ext/v1/orgs/aggregate-org/management-summary?include=org,viewer,repos,members,teams,invitations,outside_collaborators", nil)
	assertStatusCode(t, w, http.StatusOK)
	summary := testharness.DecodeJSON(t, w)
	org := summary["organization"].(map[string]any)
	if org["login"] != "aggregate-org" {
		t.Fatalf("organization login = %v, want aggregate-org", org["login"])
	}
	counts := summary["counts"].(map[string]any)
	if counts["repos"].(float64) != 1 {
		t.Fatalf("org repos count = %v, want 1", counts["repos"])
	}
	if counts["members"].(float64) != 1 {
		t.Fatalf("org members count = %v, want 1", counts["members"])
	}
	viewer := summary["viewer"].(map[string]any)
	if viewer["role"] != "admin" {
		t.Fatalf("viewer org role = %v, want admin", viewer["role"])
	}
}
