package rest_test

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ngaut/agent-git-service/internal/testharness"
	"gorm.io/gorm"
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

	w = h.DoREST(t, "GET", "/api/ext/v1/repos/"+full+"/wiki/catalog?include=tree&path=guides", nil)
	assertStatusCode(t, w, http.StatusOK)
	treeOnly := testharness.DecodeJSON(t, w)
	if treeOnly["total_count"].(float64) != 1 {
		t.Fatalf("tree-only catalog total_count = %v, want 1", treeOnly["total_count"])
	}
	if _, ok := treeOnly["pages"]; ok {
		t.Fatalf("tree-only catalog unexpectedly included pages: %#v", treeOnly)
	}
	if _, ok := treeOnly["labels"]; ok {
		t.Fatalf("tree-only catalog unexpectedly included labels: %#v", treeOnly)
	}
	tree := treeOnly["tree"].([]any)
	if len(tree) != 1 || tree[0].(map[string]any)["slug"] != "guides/setup" {
		t.Fatalf("tree-only catalog tree = %#v, want guides/setup", tree)
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

func TestWikiCatalogTreeCompressionAndSemanticETag(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "catalog-cache")
	full := "testuser/catalog-cache"
	for _, slug := range []string{"generated/perf/page-0001", "generated/perf/page-0002"} {
		w := h.DoRESTJSON(t, "PUT", wikiPagePath(full, slug), map[string]any{"body": "# " + slug + "\n\ncompressible body"})
		assertStatusCode(t, w, http.StatusOK)
	}
	h.Svc.Wg.Wait()

	path := "/api/ext/v1/repos/" + full + "/wiki/catalog?include=tree&path=generated%2Fperf"
	rejectedGzipReq := httptest.NewRequest(http.MethodGet, path, nil)
	rejectedGzipReq.Header.Set("Authorization", "token "+h.Token)
	rejectedGzipReq.Header.Set("Accept-Encoding", "gzip;q=0")
	rejectedGzip := httptest.NewRecorder()
	h.Mux.ServeHTTP(rejectedGzip, rejectedGzipReq)
	assertStatusCode(t, rejectedGzip, http.StatusOK)
	if got := rejectedGzip.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding with gzip;q=0 = %q, want identity", got)
	}
	if got := testharness.DecodeJSON(t, rejectedGzip)["total_count"]; got != float64(2) {
		t.Fatalf("identity catalog total_count = %v, want 2", got)
	}

	var wikiPageQueries atomic.Int64
	var wikiPageLabelQueries atomic.Int64
	var wikiChangesetQueries atomic.Int64
	callbackName := "test:count-wiki-catalog-queries"
	if err := h.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil {
			return
		}
		switch tx.Statement.Table {
		case "wiki_pages":
			wikiPageQueries.Add(1)
		case "wiki_page_labels":
			wikiPageLabelQueries.Add(1)
		case "wiki_changesets":
			wikiChangesetQueries.Add(1)
		}
	}); err != nil {
		t.Fatalf("register query callback: %v", err)
	}
	t.Cleanup(func() { _ = h.DB.Callback().Query().Remove(callbackName) })

	firstReq := httptest.NewRequest(http.MethodGet, path, nil)
	firstReq.Header.Set("Authorization", "token "+h.Token)
	firstReq.Header.Set("Accept-Encoding", "gzip")
	first := httptest.NewRecorder()
	h.Mux.ServeHTTP(first, firstReq)
	assertStatusCode(t, first, http.StatusOK)
	if got := first.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if vary := strings.Join(first.Header().Values("Vary"), ","); !strings.Contains(vary, "Accept-Encoding") || !strings.Contains(vary, "Authorization") {
		t.Fatalf("Vary = %q, want Accept-Encoding and Authorization", vary)
	}
	etag := first.Header().Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) {
		t.Fatalf("ETag = %q, want weak semantic ETag", etag)
	}
	zr, err := gzip.NewReader(first.Body)
	if err != nil {
		t.Fatalf("open gzip response: %v", err)
	}
	decoded, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gzip response: %v", err)
	}
	if err := zr.Close(); err != nil {
		t.Fatalf("close gzip response: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(decoded, &body); err != nil {
		t.Fatalf("decode compressed catalog: %v", err)
	}
	if body["total_count"].(float64) != 2 {
		t.Fatalf("compressed catalog total_count = %v, want 2", body["total_count"])
	}
	if got := wikiPageQueries.Load(); got != 1 {
		t.Fatalf("tree-only catalog queried wiki_pages %d times, want one shared tree/count scan", got)
	}
	if got := wikiPageLabelQueries.Load(); got != 0 {
		t.Fatalf("tree-only catalog queried wiki_page_labels %d times, want 0", got)
	}
	if got := wikiChangesetQueries.Load(); got != 1 {
		t.Fatalf("tree-only catalog checked wiki freshness %d times, want 1 per request", got)
	}
	wikiPageQueries.Store(0)
	wikiChangesetQueries.Store(0)

	secondReq := httptest.NewRequest(http.MethodGet, path, nil)
	secondReq.Header.Set("Authorization", "token "+h.Token)
	secondReq.Header.Set("Accept-Encoding", "gzip")
	secondReq.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	h.Mux.ServeHTTP(second, secondReq)
	if second.Code != http.StatusNotModified || second.Body.Len() != 0 {
		t.Fatalf("conditional catalog = %d %q, want empty 304", second.Code, second.Body.String())
	}
	if got := wikiPageQueries.Load(); got != 0 {
		t.Fatalf("matching conditional request queried wiki_pages %d times, want 0", got)
	}
	if got := wikiChangesetQueries.Load(); got != 1 {
		t.Fatalf("matching conditional request checked wiki freshness %d times, want 1", got)
	}

	w := h.DoRESTJSON(t, "PUT", wikiPagePath(full, "generated/perf/page-0003"), map[string]any{"body": "# Page 3"})
	assertStatusCode(t, w, http.StatusOK)
	thirdReq := httptest.NewRequest(http.MethodGet, path, nil)
	thirdReq.Header.Set("Authorization", "token "+h.Token)
	thirdReq.Header.Set("If-None-Match", etag)
	third := httptest.NewRecorder()
	h.Mux.ServeHTTP(third, thirdReq)
	assertStatusCode(t, third, http.StatusOK)
	if got := third.Header().Get("ETag"); got == etag {
		t.Fatalf("ETag stayed %q after Wiki head advanced", got)
	}
	if got := testharness.DecodeJSON(t, third)["total_count"]; got != float64(3) {
		t.Fatalf("catalog total_count after write = %v, want 3", got)
	}
}

func TestWikiCatalogPreservesGitTreeFallbackWithoutPageProjection(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "catalog-git-fallback")
	full := "testuser/catalog-git-fallback"
	ctx := context.Background()
	if err := h.Svc.Git.Init(ctx, full+".wiki", "master", false); err != nil {
		t.Fatalf("init legacy wiki: %v", err)
	}
	if _, err := h.Svc.Git.WriteFile(ctx, full+".wiki", "master", "guides/setup.md", "add setup", []byte("# Setup")); err != nil {
		t.Fatalf("write legacy wiki page: %v", err)
	}

	w := h.DoREST(t, http.MethodGet, "/api/ext/v1/repos/"+full+"/wiki/catalog?include=pages,tree", nil)
	assertStatusCode(t, w, http.StatusOK)
	catalog := testharness.DecodeJSON(t, w)
	tree := catalog["tree"].([]any)
	if len(tree) != 1 {
		t.Fatalf("legacy catalog tree = %#v, want one guides directory", tree)
	}
	entry := tree[0].(map[string]any)
	if entry["path"] != "guides" || entry["kind"] != "directory" {
		t.Fatalf("legacy catalog tree entry = %#v, want guides directory", entry)
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
