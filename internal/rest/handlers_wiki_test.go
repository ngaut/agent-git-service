package rest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

func wikiPagePath(full, slug string) string {
	return "/api/v3/repos/" + full + "/wiki/pages/" + url.PathEscape(slug)
}

func wikiPageSubresourcePath(full, slug, subresource string) string {
	return wikiPagePath(full, slug) + "/" + subresource
}

func TestWiki_PathHierarchyCRUD_Issue1355(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1355",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1355"

	w := h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages", nil)
	assertStatusCode(t, w, http.StatusOK)
	if rows := testharness.DecodeJSONArray(t, w); len(rows) != 0 {
		t.Fatalf("empty wiki list: got %d rows, want 0", len(rows))
	}

	writes := []struct {
		slug string
		body string
	}{
		{slug: "home", body: "# Home\n\nWelcome to the wiki.\n"},
		{slug: "guides/setup", body: "# Setup\n\nSee [[home]].\n"},
		{slug: "guides/install", body: "# Install\n\nInstall steps.\n"},
		{slug: "guides/nested/deep", body: "# Deep\n\nNested page.\n"},
	}
	for _, tc := range writes {
		w = h.DoRESTJSON(t, "PUT", wikiPagePath(full, tc.slug), map[string]any{"body": tc.body})
		assertStatusCode(t, w, http.StatusOK)
		body := testharness.DecodeJSON(t, w)
		if body["slug"] != tc.slug {
			t.Fatalf("PUT slug = %v, want %q", body["slug"], tc.slug)
		}
		if sha, _ := body["sha"].(string); sha == "" {
			t.Fatalf("PUT sha for %s must be populated", tc.slug)
		}
	}

	w = h.DoREST(t, "GET", wikiPagePath(full, "guides/setup"), nil)
	assertStatusCode(t, w, http.StatusOK)
	body := testharness.DecodeJSON(t, w)
	if body["slug"] != "guides/setup" {
		t.Fatalf("GET slug = %v, want guides/setup", body["slug"])
	}
	if sha, _ := body["sha"].(string); sha == "" {
		t.Fatalf("GET sha must be populated for nested page")
	}
	if pageURL, _ := body["url"].(string); !strings.Contains(pageURL, "guides%2Fsetup") {
		t.Fatalf("GET url = %q, want encoded nested slug", pageURL)
	}

	w = h.DoREST(t, "GET", wikiPageSubresourcePath(full, "guides/setup", "history"), nil)
	assertStatusCode(t, w, http.StatusOK)
	history := testharness.DecodeJSONArray(t, w)
	if len(history) != 1 {
		t.Fatalf("nested page history rows = %d, want 1", len(history))
	}
	if history[0]["message"] != "Update guides/setup" {
		t.Fatalf("nested page history message = %v, want Update guides/setup", history[0]["message"])
	}
	if sha, _ := history[0]["sha"].(string); sha == "" {
		t.Fatalf("nested page history sha must be populated")
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages?per_page=2", nil)
	assertStatusCode(t, w, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, w)
	if len(rows) != 2 {
		t.Fatalf("paginated list rows = %d, want 2", len(rows))
	}
	for _, row := range rows {
		if sha, _ := row["sha"].(string); sha == "" {
			t.Fatalf("list sha must be populated for %v", row["slug"])
		}
	}
	if link := w.Header().Get("Link"); link == "" {
		t.Fatal("expected Link header for paginated wiki list, got none")
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages?page=2&per_page=2", nil)
	assertStatusCode(t, w, http.StatusOK)
	rows = testharness.DecodeJSONArray(t, w)
	if len(rows) != 2 {
		t.Fatalf("page 2 rows = %d, want 2", len(rows))
	}
	if rows[0]["slug"] != "guides/setup" || rows[1]["slug"] != "home" {
		t.Fatalf("page 2 slugs = [%v %v], want [guides/setup home]", rows[0]["slug"], rows[1]["slug"])
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages?path=guides", nil)
	assertStatusCode(t, w, http.StatusOK)
	rows = testharness.DecodeJSONArray(t, w)
	if len(rows) != 3 {
		t.Fatalf("recursive path list rows = %d, want 3", len(rows))
	}
	for _, row := range rows {
		if sha, _ := row["sha"].(string); sha == "" {
			t.Fatalf("recursive list sha must be populated for %v", row["slug"])
		}
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages?path=guides&recursive=false", nil)
	assertStatusCode(t, w, http.StatusOK)
	rows = testharness.DecodeJSONArray(t, w)
	if len(rows) != 2 {
		t.Fatalf("non-recursive path list rows = %d, want 2", len(rows))
	}
	for _, row := range rows {
		if strings.Count(row["slug"].(string), "/") != 1 {
			t.Fatalf("non-recursive row = %q, want direct child under guides", row["slug"])
		}
	}
}

func TestWiki_ListPagesPaginatesAcrossMixedSlugPrefixes_Issue1472(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1472-pagination",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1472-pagination"

	slugs := []string{
		"accounts/alpha",
		"accounts/bravo",
		"accounts/charlie",
		"finance/q1",
		"finance/q2",
		"guides/install",
		"guides/setup",
		"home",
	}
	for _, slug := range slugs {
		w := h.DoRESTJSON(t, "PUT", wikiPagePath(full, slug), map[string]any{
			"body": fmt.Sprintf("# %s\n\nBody for %s.\n", titleFromSlugForTest(slug), slug),
		})
		assertStatusCode(t, w, http.StatusOK)
	}

	var seen []string
	for page := 1; page <= 4; page++ {
		w := h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/wiki/pages?page=%d&per_page=2", full, page), nil)
		assertStatusCode(t, w, http.StatusOK)
		rows := testharness.DecodeJSONArray(t, w)
		if page < 4 && len(rows) != 2 {
			t.Fatalf("page %d len = %d, want 2", page, len(rows))
		}
		if page == 4 && len(rows) != 2 {
			t.Fatalf("page 4 len = %d, want 2", len(rows))
		}
		for _, row := range rows {
			seen = append(seen, row["slug"].(string))
		}
	}

	w := h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/wiki/pages?page=5&per_page=2", full), nil)
	assertStatusCode(t, w, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, w)
	if len(rows) != 0 {
		t.Fatalf("page 5 len = %d, want 0", len(rows))
	}

	expected := []string{
		"accounts/alpha",
		"accounts/bravo",
		"accounts/charlie",
		"finance/q1",
		"finance/q2",
		"guides/install",
		"guides/setup",
		"home",
	}
	if strings.Join(seen, ",") != strings.Join(expected, ",") {
		t.Fatalf("paginated slugs = %v, want %v", seen, expected)
	}
	if link := w.Header().Get("Link"); !strings.Contains(link, "page=4") || !strings.Contains(link, "rel=\"last\"") {
		t.Fatalf("page 5 Link header = %q, want last page=4", link)
	}
}

func titleFromSlugForTest(slug string) string {
	parts := strings.Split(slug, "/")
	last := parts[len(parts)-1]
	last = strings.ReplaceAll(last, "-", " ")
	return strings.Title(last)
}

func TestWiki_PutNestedPageWithEncodedRepoName(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	const repoName = "F1 Tracks and Tastes"
	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       repoName,
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	fullPath := url.PathEscape(h.User.Login) + "/" + url.PathEscape(repoName)
	pagePath := "/api/v3/repos/" + fullPath + "/wiki/pages/" + url.PathEscape("xxx/yyy")
	w := h.DoRESTJSON(t, "PUT", pagePath, map[string]any{
		"body": "# Nested\n\nGrand prix notes.\n",
	})
	assertStatusCode(t, w, http.StatusOK)
	body := testharness.DecodeJSON(t, w)
	if body["slug"] != "xxx/yyy" {
		t.Fatalf("PUT slug = %v, want xxx/yyy", body["slug"])
	}

	w = h.DoREST(t, "GET", pagePath, nil)
	assertStatusCode(t, w, http.StatusOK)
	body = testharness.DecodeJSON(t, w)
	if body["slug"] != "xxx/yyy" {
		t.Fatalf("GET slug = %v, want xxx/yyy", body["slug"])
	}
}

func TestWiki_PutNestedPageWithLiteralPercentRepoName(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	const repoName = "foo%20bar"
	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       repoName,
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	fullPath := url.PathEscape(h.User.Login) + "/" + url.PathEscape(repoName)
	pagePath := "/api/v3/repos/" + fullPath + "/wiki/pages/" + url.PathEscape("xxx/yyy")
	w := h.DoRESTJSON(t, "PUT", pagePath, map[string]any{
		"body": "# Nested\n\nLiteral percent repo name.\n",
	})
	assertStatusCode(t, w, http.StatusOK)
	body := testharness.DecodeJSON(t, w)
	if body["slug"] != "xxx/yyy" {
		t.Fatalf("PUT slug = %v, want xxx/yyy", body["slug"])
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/"+fullPath, nil)
	assertStatusCode(t, w, http.StatusOK)
	body = testharness.DecodeJSON(t, w)
	if body["name"] != repoName {
		t.Fatalf("repo name = %v, want %s", body["name"], repoName)
	}
}

func TestWikiPageLabelsREST(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-labels-rest",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-labels-rest"
	for _, label := range []map[string]any{
		{"name": "auth", "color": "d73a4a", "description": "Authentication token lifecycle"},
		{"name": "runbook", "color": "0e8a16", "description": "Operational runbooks"},
	} {
		w := h.DoRESTJSON(t, "POST", "/api/v3/repos/"+full+"/labels", label)
		assertStatusCode(t, w, http.StatusCreated)
	}

	w := h.DoRESTJSON(t, "PUT", wikiPagePath(full, "guides/rotation"), map[string]any{
		"body": "# Rotation\n\nRotate credentials from the admin console.\n",
	})
	assertStatusCode(t, w, http.StatusOK)
	h.Svc.Wg.Wait()

	w = h.DoRESTJSON(t, "POST", wikiPageSubresourcePath(full, "guides/rotation", "labels"), map[string]any{
		"labels": []string{"auth", "runbook"},
	})
	assertStatusCode(t, w, http.StatusOK)
	labels := testharness.DecodeJSONArray(t, w)
	if len(labels) != 2 {
		t.Fatalf("POST labels count = %d, want 2", len(labels))
	}
	h.Svc.Wg.Wait()

	w = h.DoREST(t, "GET", wikiPagePath(full, "guides/rotation"), nil)
	assertStatusCode(t, w, http.StatusOK)
	page := testharness.DecodeJSON(t, w)
	pageLabels, ok := page["labels"].([]any)
	if !ok || len(pageLabels) != 2 {
		t.Fatalf("page labels = %#v, want 2 labels", page["labels"])
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages?label=auth", nil)
	assertStatusCode(t, w, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, w)
	if len(rows) != 1 || rows[0]["slug"] != "guides/rotation" {
		t.Fatalf("label-filtered wiki rows = %#v, want guides/rotation", rows)
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/search?q=token+lifecycle", nil)
	assertStatusCode(t, w, http.StatusOK)
	searchBody := testharness.DecodeJSON(t, w)
	results, ok := searchBody["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("search results = %#v, want 1 label-recalled result", searchBody["results"])
	}
	result := results[0].(map[string]any)
	if result["slug"] != "guides/rotation" {
		t.Fatalf("search result slug = %v, want guides/rotation", result["slug"])
	}

	w = h.DoREST(t, "DELETE", wikiPageSubresourcePath(full, "guides/rotation", "labels/auth"), nil)
	assertStatusCode(t, w, http.StatusOK)
	labels = testharness.DecodeJSONArray(t, w)
	if len(labels) != 1 || labels[0]["name"] != "runbook" {
		t.Fatalf("remaining labels = %#v, want runbook", labels)
	}
}

func TestWiki_ListPagesSetsMigrationInProgressHeader(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-migration-header",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-migration-header"

	w := h.DoRESTJSON(t, "PUT", wikiPagePath(full, "home"), map[string]any{"body": "# Home\n"})
	assertStatusCode(t, w, http.StatusOK)
	h.Svc.Wg.Wait()

	if _, err := h.Svc.Git.WriteFile(ctx, full+".wiki", "master", "about.md", "add about", []byte("about body")); err != nil {
		t.Fatalf("git write about: %v", err)
	}

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var released int32
	h.Svc.SetWikiBackgroundMigrationStartedHookForTest(func(repo string) {
		if repo == full {
			started <- struct{}{}
		}
	})
	h.Svc.SetWikiMigrationAfterSnapshotHookForTest(func(repo string) {
		if repo == full {
			<-release
		}
	})
	defer func() {
		h.Svc.SetWikiBackgroundMigrationStartedHookForTest(nil)
		h.Svc.SetWikiMigrationAfterSnapshotHookForTest(nil)
		if atomic.CompareAndSwapInt32(&released, 0, 1) {
			close(release)
		}
	}()

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages", nil)
	assertStatusCode(t, w, http.StatusOK)
	if got := w.Header().Get("X-Wiki-Migration-In-Progress"); got != "true" {
		t.Fatalf("X-Wiki-Migration-In-Progress = %q, want true", got)
	}

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for background migration to start")
	}

	if atomic.CompareAndSwapInt32(&released, 0, 1) {
		close(release)
	}
	h.Svc.Wg.Wait()

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages", nil)
	assertStatusCode(t, w, http.StatusOK)
	if got := w.Header().Get("X-Wiki-Migration-In-Progress"); got != "" {
		t.Fatalf("X-Wiki-Migration-In-Progress after rebuild = %q, want empty", got)
	}
}

func TestWiki_GetPageNotFoundStillSetsMigrationInProgressHeader(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-migration-header-404",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-migration-header-404"

	w := h.DoRESTJSON(t, "PUT", wikiPagePath(full, "home"), map[string]any{"body": "# Home\n"})
	assertStatusCode(t, w, http.StatusOK)
	h.Svc.Wg.Wait()

	if _, err := h.Svc.Git.WriteFile(ctx, full+".wiki", "master", "about.md", "add about", []byte("about body")); err != nil {
		t.Fatalf("git write about: %v", err)
	}

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var released int32
	h.Svc.SetWikiBackgroundMigrationStartedHookForTest(func(repo string) {
		if repo == full {
			started <- struct{}{}
		}
	})
	h.Svc.SetWikiMigrationAfterSnapshotHookForTest(func(repo string) {
		if repo == full {
			<-release
		}
	})
	defer func() {
		h.Svc.SetWikiBackgroundMigrationStartedHookForTest(nil)
		h.Svc.SetWikiMigrationAfterSnapshotHookForTest(nil)
		if atomic.CompareAndSwapInt32(&released, 0, 1) {
			close(release)
		}
	}()

	w = h.DoREST(t, "GET", wikiPagePath(full, "about"), nil)
	assertStatusCode(t, w, http.StatusNotFound)
	if got := w.Header().Get("X-Wiki-Migration-In-Progress"); got != "true" {
		t.Fatalf("X-Wiki-Migration-In-Progress on not found = %q, want true", got)
	}

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for background migration to start")
	}

	if atomic.CompareAndSwapInt32(&released, 0, 1) {
		close(release)
	}
	h.Svc.Wg.Wait()
}

func TestWikiPageLabelRoutesPreserveExistingSlugPages(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-label-route-precedence",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-label-route-precedence"
	for _, tc := range []struct {
		slug string
		body string
	}{
		{slug: "guides/setup/labels", body: "# Literal Labels Page\n\nThis slug must remain a normal page.\n"},
		{slug: "guides/literal/labels/auth", body: "# Literal Label Child\n\nThis slug must remain writable.\n"},
	} {
		if _, err := h.Svc.PutWikiPage(ctx, full, tc.slug, tc.body, "seed literal labels slug", ""); err != nil {
			t.Fatalf("seed %s: %v", tc.slug, err)
		}
	}
	h.Svc.Wg.Wait()

	w := h.DoREST(t, "GET", wikiPagePath(full, "guides/setup/labels"), nil)
	assertStatusCode(t, w, http.StatusOK)
	page := testharness.DecodeJSON(t, w)
	if page["slug"] != "guides/setup/labels" {
		t.Fatalf("literal /labels slug = %v, want guides/setup/labels", page["slug"])
	}

	w = h.DoRESTJSON(t, "PUT", wikiPagePath(full, "guides/setup/labels"), map[string]any{
		"body": "# Literal Labels Page\n\nUpdated body.\n",
	})
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoREST(t, "GET", wikiPagePath(full, "guides/literal/labels/auth"), nil)
	assertStatusCode(t, w, http.StatusOK)
	literalChild := testharness.DecodeJSON(t, w)
	childSHA, _ := literalChild["sha"].(string)

	w = h.DoRESTJSON(t, "POST", wikiPageSubresourcePath(full, "guides/literal/labels/auth", "move"), map[string]any{
		"new_slug": "guides/literal/labels/auth-renamed",
		"if_match": childSHA,
	})
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoREST(t, "GET", wikiPagePath(full, "guides/literal/labels/auth-renamed"), nil)
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoRESTJSON(t, "DELETE", wikiPagePath(full, "guides/literal/labels/auth-renamed"), map[string]any{
		"message": "delete literal child page",
	})
	assertStatusCode(t, w, http.StatusNoContent)

	w = h.DoREST(t, "GET", wikiPagePath(full, "guides/literal/labels/auth-renamed"), nil)
	assertStatusCode(t, w, http.StatusNotFound)
}

func TestWiki_MoveAndConflictSemantics_Issue1355(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1355-move",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1355-move"

	w := h.DoRESTJSON(t, "PUT", wikiPagePath(full, "guides/setup"), map[string]any{"body": "# Setup\n\nFirst body.\n"})
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoREST(t, "GET", wikiPagePath(full, "guides/setup"), nil)
	assertStatusCode(t, w, http.StatusOK)
	page := testharness.DecodeJSON(t, w)
	sourceSHA := page["sha"].(string)

	w = h.DoRESTJSON(t, "POST", wikiPageSubresourcePath(full, "guides/setup", "move"), map[string]any{
		"new_slug": "tutorials/setup",
		"if_match": sourceSHA,
		"message":  "move setup page",
	})
	assertStatusCode(t, w, http.StatusOK)
	moveResult := testharness.DecodeJSON(t, w)
	moved, ok := moveResult["moved"].(map[string]any)
	if !ok {
		t.Fatalf("moved payload type = %T, want object", moveResult["moved"])
	}
	if moved["slug"] != "tutorials/setup" {
		t.Fatalf("moved slug = %v, want tutorials/setup", moved["slug"])
	}

	w = h.DoREST(t, "GET", wikiPagePath(full, "guides/setup"), nil)
	assertStatusCode(t, w, http.StatusNotFound)
	w = h.DoREST(t, "GET", wikiPagePath(full, "tutorials/setup"), nil)
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoRESTJSON(t, "PUT", wikiPagePath(full, "tutorials/setup"), map[string]any{"body": "# Setup\n\nUpdated body.\n"})
	assertStatusCode(t, w, http.StatusOK)
	w = h.DoRESTJSON(t, "POST", wikiPageSubresourcePath(full, "tutorials/setup", "move"), map[string]any{
		"new_slug": "tutorials/final",
		"if_match": sourceSHA,
	})
	assertStatusCode(t, w, http.StatusConflict)
	if conflict := testharness.DecodeJSON(t, w); conflict["code"] != "SOURCE_STALE" {
		t.Fatalf("stale move code = %v, want SOURCE_STALE", conflict["code"])
	}
}

func TestWiki_MoveRewritesInboundReferences_Issue1361(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1361",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1361"

	w := h.DoRESTJSON(t, "PUT", wikiPagePath(full, "guides/setup"), map[string]any{"body": "# Setup\n\nFirst body.\n"})
	assertStatusCode(t, w, http.StatusOK)
	w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", map[string]any{
		"body": "# Home\n\nSee [[guides/setup]] and [Setup](guides/setup.md?view=1#intro).\n\n[setup-ref]: guides/setup.md#deep \"Guide\"\n\n`[[guides/setup]]`\n\n```md\n[[guides/setup]]\n```\n\n<pre>\n[[guides/setup]]\n</pre>\n\n[[Setup]]\n",
	})
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoREST(t, "GET", wikiPagePath(full, "guides/setup"), nil)
	assertStatusCode(t, w, http.StatusOK)
	source := testharness.DecodeJSON(t, w)

	w = h.DoRESTJSON(t, "POST", wikiPageSubresourcePath(full, "guides/setup", "move"), map[string]any{
		"new_slug": "tutorials/setup",
		"if_match": source["sha"],
	})
	assertStatusCode(t, w, http.StatusOK)
	moveResult := testharness.DecodeJSON(t, w)
	moved := moveResult["moved"].(map[string]any)
	if moved["slug"] != "tutorials/setup" {
		t.Fatalf("moved slug = %v, want tutorials/setup", moved["slug"])
	}
	rewrites, ok := moveResult["rewrites"].([]any)
	if !ok || len(rewrites) != 1 {
		t.Fatalf("rewrites payload = %#v, want one rewritten page", moveResult["rewrites"])
	}
	rewrite := rewrites[0].(map[string]any)
	if rewrite["slug"] != "home" {
		t.Fatalf("rewritten slug = %v, want home", rewrite["slug"])
	}
	skipped, ok := moveResult["skipped"].([]any)
	if !ok || len(skipped) != 0 {
		t.Fatalf("skipped payload = %#v, want empty array", moveResult["skipped"])
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home", nil)
	assertStatusCode(t, w, http.StatusOK)
	home := testharness.DecodeJSON(t, w)
	body, _ := home["body"].(string)
	if !strings.Contains(body, "[[tutorials/setup]]") {
		t.Fatalf("rewritten body missing wiki shorthand update: %q", body)
	}
	if !strings.Contains(body, "(tutorials/setup.md?view=1#intro)") {
		t.Fatalf("rewritten body missing markdown destination update: %q", body)
	}
	if !strings.Contains(body, "[setup-ref]: tutorials/setup.md#deep \"Guide\"") {
		t.Fatalf("rewritten body missing reference definition update: %q", body)
	}
	if !strings.Contains(body, "`[[guides/setup]]`") || !strings.Contains(body, "```md\n[[guides/setup]]\n```") || !strings.Contains(body, "<pre>\n[[guides/setup]]\n</pre>") {
		t.Fatalf("rewritten body touched protected regions: %q", body)
	}
	if !strings.Contains(body, "[[Setup]]") {
		t.Fatalf("rewritten body should preserve title-form wiki links: %q", body)
	}
}

func TestWiki_BulkMovePrefix_Issue1360(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1360-bulk",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1360-bulk"

	for _, tc := range []struct {
		slug string
		body string
	}{
		{slug: "tutorial/intro", body: "# Intro\n"},
		{slug: "tutorial/advanced", body: "# Advanced\n"},
		{slug: "home", body: "# Home\n\nSee [[tutorial/intro]] and [[tutorial/advanced]].\n"},
		{slug: "reference/home", body: "# Reference\n"},
	} {
		w := h.DoRESTJSON(t, "PUT", wikiPagePath(full, tc.slug), map[string]any{"body": tc.body})
		assertStatusCode(t, w, http.StatusOK)
	}

	beforeCount := wikiCommitCount(t, h, ctx, full)
	ifMatch := map[string]any{
		"tutorial/intro":    wikiPageSHA(t, h, full, "tutorial/intro"),
		"tutorial/advanced": wikiPageSHA(t, h, full, "tutorial/advanced"),
	}

	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/"+full+"/wiki/move", map[string]any{
		"from":     "tutorial",
		"to":       "guides",
		"message":  "bulk wiki move",
		"if_match": ifMatch,
	})
	assertStatusCode(t, w, http.StatusOK)
	body := testharness.DecodeJSON(t, w)
	if commit, _ := body["commit"].(string); commit == "" {
		t.Fatal("bulk move commit must be populated")
	}
	moved, ok := body["moved"].([]any)
	if !ok || len(moved) != 2 {
		t.Fatalf("bulk move moved = %#v, want 2 rows", body["moved"])
	}
	rewrites, ok := body["rewrites"].([]any)
	if !ok || len(rewrites) != 1 {
		t.Fatalf("bulk move rewrites = %#v, want 1 row", body["rewrites"])
	}
	rewrite := rewrites[0].(map[string]any)
	if rewrite["slug"] != "home" {
		t.Fatalf("bulk move rewrite slug = %v, want home", rewrite["slug"])
	}
	skipped, ok := body["skipped"].([]any)
	if !ok || len(skipped) != 0 {
		t.Fatalf("bulk move skipped = %#v, want empty array", body["skipped"])
	}

	afterCount := wikiCommitCount(t, h, ctx, full)
	if got := afterCount - beforeCount; got != 1 {
		t.Fatalf("bulk move commit delta = %d, want 1", got)
	}

	for _, slug := range []string{"tutorial/intro", "tutorial/advanced"} {
		w = h.DoREST(t, "GET", wikiPagePath(full, slug), nil)
		assertStatusCode(t, w, http.StatusNotFound)
	}
	for _, slug := range []string{"guides/intro", "guides/advanced", "reference/home"} {
		w = h.DoREST(t, "GET", wikiPagePath(full, slug), nil)
		assertStatusCode(t, w, http.StatusOK)
	}
	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home", nil)
	assertStatusCode(t, w, http.StatusOK)
	home := testharness.DecodeJSON(t, w)
	homeBody, _ := home["body"].(string)
	if !strings.Contains(homeBody, "[[guides/intro]]") || !strings.Contains(homeBody, "[[guides/advanced]]") {
		t.Fatalf("bulk move home body = %q, want rewritten wiki links", homeBody)
	}
}

func TestWiki_BulkMovePrefix_MissingIfMatchAndSourceNotFound_Issue1360(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1360-missing",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1360-missing"

	for _, slug := range []string{"tutorial/intro", "tutorial/advanced"} {
		w := h.DoRESTJSON(t, "PUT", wikiPagePath(full, slug), map[string]any{"body": "# Page\n"})
		assertStatusCode(t, w, http.StatusOK)
	}

	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/"+full+"/wiki/move", map[string]any{
		"from": "tutorial",
		"to":   "guides",
		"if_match": map[string]any{
			"tutorial/intro": wikiPageSHA(t, h, full, "tutorial/intro"),
		},
	})
	assertStatusCode(t, w, http.StatusUnprocessableEntity)
	body := testharness.DecodeJSON(t, w)
	if body["code"] != "IF_MATCH_INCOMPLETE" {
		t.Fatalf("bulk move code = %v, want IF_MATCH_INCOMPLETE", body["code"])
	}
	missing, ok := body["missing_slugs"].([]any)
	if !ok || len(missing) != 1 || missing[0] != "tutorial/advanced" {
		t.Fatalf("missing_slugs = %#v, want [tutorial/advanced]", body["missing_slugs"])
	}

	w = h.DoRESTJSON(t, "POST", "/api/v3/repos/"+full+"/wiki/move", map[string]any{
		"from":     "missing",
		"to":       "guides",
		"if_match": map[string]any{"missing": "deadbeef"},
	})
	assertStatusCode(t, w, http.StatusNotFound)
	body = testharness.DecodeJSON(t, w)
	if body["code"] != "SOURCE_NOT_FOUND" {
		t.Fatalf("not-found code = %v, want SOURCE_NOT_FOUND", body["code"])
	}
}

func TestWiki_BulkMovePrefix_ConflictsStayAtomic_Issue1360(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1360-conflicts",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1360-conflicts"

	for _, tc := range []struct {
		slug string
		body string
	}{
		{slug: "tutorial/intro", body: "# Intro\n"},
		{slug: "tutorial/advanced", body: "# Advanced\n"},
		{slug: "guides", body: "# Guides\n"},
	} {
		w := h.DoRESTJSON(t, "PUT", wikiPagePath(full, tc.slug), map[string]any{"body": tc.body})
		assertStatusCode(t, w, http.StatusOK)
	}

	staleSHA := wikiPageSHA(t, h, full, "tutorial/advanced")
	update := h.DoRESTJSON(t, "PUT", wikiPagePath(full, "tutorial/advanced"), map[string]any{"body": "# Advanced\n\nUpdated\n"})
	assertStatusCode(t, update, http.StatusOK)

	beforeCount := wikiCommitCount(t, h, ctx, full)
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/"+full+"/wiki/move", map[string]any{
		"from": "tutorial",
		"to":   "guides",
		"if_match": map[string]any{
			"tutorial/intro":    wikiPageSHA(t, h, full, "tutorial/intro"),
			"tutorial/advanced": staleSHA,
		},
	})
	assertStatusCode(t, w, http.StatusConflict)
	body := testharness.DecodeJSON(t, w)
	conflicts, ok := body["conflicts"].([]any)
	if !ok || len(conflicts) != 2 {
		t.Fatalf("conflicts = %#v, want 2 rows", body["conflicts"])
	}
	gotCodes := map[string]bool{}
	for _, raw := range conflicts {
		row := raw.(map[string]any)
		gotCodes[row["code"].(string)] = true
	}
	if !gotCodes["PREFIX_COLLISION"] || !gotCodes["SOURCE_STALE"] {
		t.Fatalf("conflict codes = %#v, want PREFIX_COLLISION and SOURCE_STALE", conflicts)
	}

	afterCount := wikiCommitCount(t, h, ctx, full)
	if afterCount != beforeCount {
		t.Fatalf("conflict changed commit count: before=%d after=%d", beforeCount, afterCount)
	}
	for _, slug := range []string{"tutorial/intro", "tutorial/advanced", "guides"} {
		w = h.DoREST(t, "GET", wikiPagePath(full, slug), nil)
		assertStatusCode(t, w, http.StatusOK)
	}
	for _, slug := range []string{"guides/intro", "guides/advanced"} {
		w = h.DoREST(t, "GET", wikiPagePath(full, slug), nil)
		assertStatusCode(t, w, http.StatusNotFound)
	}
}

func TestWiki_PrefixCollisionAndCaseValidation_Issue1355(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1355-conflicts",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1355-conflicts"

	w := h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/Home", map[string]any{"body": "# Home\n"})
	assertStatusCode(t, w, http.StatusUnprocessableEntity)
	w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/tutorial", map[string]any{"body": "# Tutorial\n"})
	assertStatusCode(t, w, http.StatusOK)
	w = h.DoRESTJSON(t, "PUT", wikiPagePath(full, "tutorial/getting-started"), map[string]any{"body": "# Getting Started\n"})
	assertStatusCode(t, w, http.StatusConflict)
	w = h.DoRESTJSON(t, "PUT", wikiPagePath(full, "guides/setup"), map[string]any{"body": "# Setup\n"})
	assertStatusCode(t, w, http.StatusOK)
	w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/guides", map[string]any{"body": "# Guides\n"})
	assertStatusCode(t, w, http.StatusConflict)
}

func TestWiki_WritePermissionRequired_Issue1296(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	owner, _ := seedHarnessUser(t, h, "wiki-owner", false)
	if _, err := h.Svc.CreateRepo(service.ContextWithUser(ctx, owner), service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-perm",
		Private:    true,
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	_, strangerToken := seedHarnessUser(t, h, "wiki-stranger", false)
	full := fmt.Sprintf("%s/wiki-perm", owner.Login)
	w := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", strangerToken, map[string]any{"body": "drive-by edit"})
	if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Errorf("non-collaborator wiki PUT: got %d, want 403/404", w.Code)
	}
}

func TestWiki_ListPageMetadataResolvesLastAuthor_Issue1345(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	if err := h.Svc.DB.Create(&db.User{
		Login: "wiki-bot",
		Name:  "Wiki Bot",
		Email: "gh-server@localhost",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed author user: %v", err)
	}
	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1345",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1345"

	w := h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", map[string]any{
		"body":    "# Home\n\nWelcome to the wiki.",
		"message": "create home page",
	})
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages", nil)
	assertStatusCode(t, w, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, w)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	author, ok := rows[0]["last_author"].(map[string]any)
	if !ok {
		t.Fatalf("last_author type = %T, want object", rows[0]["last_author"])
	}
	// After the catalog cutover, last_author is the authenticated
	// REST caller (recorded as wiki_changesets.author_id and copied
	// onto wiki_pages.last_author_id). The legacy behaviour of
	// resolving last_author from the default git committer's email
	// no longer applies — the catalog is SOT and records the actual
	// caller's identity.
	if author["login"] != h.User.Login {
		t.Fatalf("last_author.login = %v, want %q (REST caller)", author["login"], h.User.Login)
	}
}

func TestWiki_GetPageIncludesLastAuthor_Issue1372(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	initialAuthor := db.User{
		Login: "wiki-bot",
		Name:  "Wiki Bot",
		Email: "gh-server@localhost",
		Type:  db.TypeUser,
	}
	if err := h.Svc.DB.Create(&initialAuthor).Error; err != nil {
		t.Fatalf("seed initial author user: %v", err)
	}
	editor := db.User{
		Login: "page-editor",
		Name:  "page-editor",
		Email: "editor@example.com",
		Type:  db.TypeUser,
	}
	if err := h.Svc.DB.Create(&editor).Error; err != nil {
		t.Fatalf("seed editor user: %v", err)
	}
	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1372",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1372"

	created, err := h.Svc.PutWikiPage(service.ContextWithUser(ctx, initialAuthor), full, "home", "# Home\n\nFirst version.", "create home page", "")
	if err != nil {
		t.Fatalf("seed initial page: %v", err)
	}
	createdPayload := transform.WikiPage(full, created)
	initialAuthorPayload, ok := createdPayload["last_author"].(map[string]any)
	if !ok || initialAuthorPayload["login"] != "wiki-bot" {
		t.Fatalf("create last_author = %v, want wiki-bot", createdPayload["last_author"])
	}
	writeWikiAuthorCommitREST(t, ctx, h, full, "home.md", "# Home\n\nSecond version.\n", "update home page", editor.Name, editor.Email)

	get := h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home", nil)
	assertStatusCode(t, get, http.StatusOK)
	page := testharness.DecodeJSON(t, get)
	author, ok := page["last_author"].(map[string]any)
	if !ok {
		t.Fatalf("last_author type = %T, want object", page["last_author"])
	}
	if author["login"] != "page-editor" {
		t.Fatalf("last_author.login = %v, want page-editor", author["login"])
	}
	if page["updated_at"] == nil {
		t.Fatalf("updated_at = nil, want git-backed timestamp")
	}
}

func TestWiki_GetPageUsesNullLastAuthorWhenCommitIdentityDoesNotMatch_Issue1372(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1372-unresolved",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1372-unresolved"

	// Seed via REST to establish a master branch HEAD, then overwrite
	// with a direct git commit whose author email matches no user
	// in the DB. After the catalog sync, last_author should be null —
	// the migration resolver leaves it unresolved for unknown
	// committers.
	w := h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", map[string]any{
		"body":    "# Home\n\nFirst version.",
		"message": "create home page",
	})
	assertStatusCode(t, w, http.StatusOK)
	writeWikiAuthorCommitREST(t, ctx, h, full, "home.md", "# Home\n\noverwrite.\n", "overwrite", "anonymous", "no-such-user@example.invalid")

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home", nil)
	assertStatusCode(t, w, http.StatusOK)
	page := testharness.DecodeJSON(t, w)
	if val, exists := page["last_author"]; !exists || val != nil {
		t.Fatalf("last_author = %v (exists=%v), want explicit null", val, exists)
	}
}

func writeWikiAuthorCommitREST(t *testing.T, ctx context.Context, h *testharness.Harness, repoFullName, path, body, message, authorName, authorEmail string) {
	t.Helper()

	repoPath, err := h.Svc.Git.GetRepoPath(ctx, repoFullName+".wiki")
	if err != nil {
		t.Fatalf("GetRepoPath(%s.wiki): %v", repoFullName, err)
	}
	headSHA, err := h.Svc.Git.HeadSHA(ctx, repoFullName+".wiki", "master")
	if err != nil {
		t.Fatalf("HeadSHA(%s.wiki): %v", repoFullName, err)
	}

	var stream strings.Builder
	fmt.Fprintf(&stream, "blob\nmark :1\ndata %d\n%s", len(body), body)
	stream.WriteString("commit refs/heads/master\nmark :2\n")
	fmt.Fprintf(&stream, "author %s <%s> 1714766400 +0000\n", authorName, authorEmail)
	fmt.Fprintf(&stream, "committer %s <%s> 1714766400 +0000\n", authorName, authorEmail)
	fmt.Fprintf(&stream, "data %d\n%s\n", len(message), message)
	fmt.Fprintf(&stream, "from %s\n", headSHA)
	stream.WriteString("M 100644 :1 ")
	stream.WriteString(path)
	stream.WriteString("\n\n")
	stream.WriteString("done\n")

	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "fast-import", "--quiet")
	cmd.Stdin = strings.NewReader(stream.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fast-import: %v, output=%s", err, out)
	}
	// After a direct git write, run MigrateWiki to incorporate the
	// new commit into the catalog (catalog is SOT after the runtime
	// cutover). Production wires the same call behind receive-pack.
	if _, err := h.Svc.MigrateWiki(ctx, repoFullName, service.WikiMigrationOptions{}); err != nil {
		t.Fatalf("MigrateWiki after fast-import: %v", err)
	}
}

func TestWiki_PutPagePreconditions_Issue1347(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1347",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1347"

	create := h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", map[string]any{
		"body":    "# Home\n\nv1",
		"message": "create home page",
	})
	assertStatusCode(t, create, http.StatusOK)
	created := testharness.DecodeJSON(t, create)
	initialSHA, _ := created["sha"].(string)

	update := h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", map[string]any{
		"body":    "# Home\n\nv2",
		"message": "update home page",
		"sha":     initialSHA,
	})
	assertStatusCode(t, update, http.StatusOK)
	updated := testharness.DecodeJSON(t, update)
	currentSHA, _ := updated["sha"].(string)

	conflict := h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", map[string]any{
		"body":    "# Home\n\nstale write",
		"message": "stale update",
		"sha":     initialSHA,
	})
	assertStatusCode(t, conflict, http.StatusConflict)
	conflictBody := testharness.DecodeJSON(t, conflict)
	currentPage, _ := conflictBody["current_page"].(map[string]any)
	if currentPage == nil || currentPage["sha"] != currentSHA {
		t.Fatalf("conflict current_page = %v, want sha %q", currentPage, currentSHA)
	}

	withHeader := doWikiPutWithHeaders(t, h, full, "home", map[string]string{
		"If-Match": fmt.Sprintf("%q", currentSHA),
	}, map[string]any{
		"body":    "# Home\n\nv3",
		"message": "header precondition update",
	})
	assertStatusCode(t, withHeader, http.StatusOK)
}

func TestWiki_SearchEndpoint_Issue1362(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1362",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1362"

	w := h.DoRESTJSON(t, "PUT", wikiPagePath(full, "tutorial/auth"), map[string]any{
		"body": "# Authentication\n\nThe session token expires after 30 minutes of inactivity.",
	})
	assertStatusCode(t, w, http.StatusOK)
	h.Svc.Wg.Wait()

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/search?q=token%20expires&limit=5&offset=0", nil)
	assertStatusCode(t, w, http.StatusOK)
	body := testharness.DecodeJSON(t, w)
	if body["method"] != "substring" {
		t.Fatalf("method = %v, want substring", body["method"])
	}
	results, ok := body["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("results = %#v, want 1 row", body["results"])
	}
	row := results[0].(map[string]any)
	if row["slug"] != "tutorial/auth" {
		t.Fatalf("slug = %v, want tutorial/auth", row["slug"])
	}
	if !strings.Contains(row["snippet"].(string), "<mark>") {
		t.Fatalf("snippet = %q, want marked highlight", row["snippet"])
	}
}

func doWikiPutWithHeaders(t *testing.T, h *testharness.Harness, full, slug string, headers map[string]string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req := httptest.NewRequest("PUT", wikiPagePath(full, slug), bytes.NewReader(b))
	req.Header.Set("Authorization", "token "+h.Token)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)
	return w
}

func wikiPageSHA(t *testing.T, h *testharness.Harness, full, slug string) string {
	t.Helper()
	w := h.DoREST(t, "GET", wikiPagePath(full, slug), nil)
	assertStatusCode(t, w, http.StatusOK)
	body := testharness.DecodeJSON(t, w)
	sha, _ := body["sha"].(string)
	if sha == "" {
		t.Fatalf("wiki page %s sha must be populated", slug)
	}
	return sha
}

func wikiCommitCount(t *testing.T, h *testharness.Harness, ctx context.Context, full string) int {
	t.Helper()
	repoPath, err := h.Svc.Git.GetRepoPath(ctx, full+".wiki")
	if err != nil {
		t.Fatalf("GetRepoPath(%s.wiki): %v", full, err)
	}
	out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-list", "--count", "master").CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-list --count master: %v: %s", err, out)
	}
	var count int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &count); err != nil {
		t.Fatalf("parse commit count %q: %v", strings.TrimSpace(string(out)), err)
	}
	return count
}

func TestWiki_BacklinksPathResolution_Issue1355(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1355-links",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1355-links"

	for _, tc := range []struct {
		slug string
		body string
	}{
		{slug: "home", body: "# Home\n\nStart here.\n"},
		{slug: "guides/setup", body: "# Setup\n\nNested page.\n"},
		{slug: "plain-page", body: "# Plain Page\n\nTop-level page with a hyphenated slug.\n"},
		{slug: "faq", body: "# FAQ\n\nSee [[home]] and [Home](home.md).\n"},
		{slug: "guide-index", body: "# Guide Index\n\nUse [[guides/setup]].\n"},
		{slug: "plain-ref", body: "# Plain Ref\n\nUse [[plain-page]].\n"},
		{slug: "shortcut-miss", body: "# Shortcut Miss\n\nUse [[Setup]].\n"},
		{slug: "assets", body: "# Assets\n\n![Architecture](home.md)\n"},
	} {
		w := h.DoRESTJSON(t, "PUT", wikiPagePath(full, tc.slug), map[string]any{"body": tc.body})
		assertStatusCode(t, w, http.StatusOK)
	}

	w := h.DoREST(t, "GET", wikiPageSubresourcePath(full, "home", "backlinks"), nil)
	assertStatusCode(t, w, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, w)
	if len(rows) != 1 || rows[0]["slug"] != "faq" {
		t.Fatalf("home backlinks = %+v, want faq only", rows)
	}

	w = h.DoREST(t, "GET", wikiPageSubresourcePath(full, "guides/setup", "backlinks"), nil)
	assertStatusCode(t, w, http.StatusOK)
	rows = testharness.DecodeJSONArray(t, w)
	if len(rows) != 1 || rows[0]["slug"] != "guide-index" {
		t.Fatalf("nested backlinks = %+v, want guide-index only", rows)
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/plain-page/backlinks", nil)
	assertStatusCode(t, w, http.StatusOK)
	rows = testharness.DecodeJSONArray(t, w)
	if len(rows) != 1 || rows[0]["slug"] != "plain-ref" {
		t.Fatalf("hyphenated backlinks = %+v, want plain-ref only", rows)
	}
}

func TestWiki_NestedMoveAndBacklinksSuffixesRemainPageSlugs_Issue1355(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1355-suffixes",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1355-suffixes"

	for _, tc := range []struct {
		slug string
		body string
	}{
		{slug: "guides/move", body: "# Move\n\nNested page named move.\n"},
		{slug: "guides/backlinks", body: "# Backlinks\n\nNested page named backlinks.\n"},
	} {
		w := h.DoRESTJSON(t, "PUT", wikiPagePath(full, tc.slug), map[string]any{"body": tc.body})
		assertStatusCode(t, w, http.StatusOK)
	}

	w := h.DoREST(t, "GET", wikiPagePath(full, "guides/move"), nil)
	assertStatusCode(t, w, http.StatusOK)
	if body := testharness.DecodeJSON(t, w); body["slug"] != "guides/move" {
		t.Fatalf("guides/move GET slug = %v, want guides/move", body["slug"])
	}

	w = h.DoREST(t, "GET", wikiPagePath(full, "guides/backlinks"), nil)
	assertStatusCode(t, w, http.StatusOK)
	if body := testharness.DecodeJSON(t, w); body["slug"] != "guides/backlinks" {
		t.Fatalf("guides/backlinks GET slug = %v, want guides/backlinks", body["slug"])
	}
}

func TestWiki_PageHistory_Issue1346(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	if err := h.Svc.DB.Create(&db.User{
		Login: "wiki-bot",
		Name:  "Wiki Bot",
		Email: "gh-server@localhost",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed author user: %v", err)
	}
	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1346",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1346"

	bodies := []string{
		"# Home\n\nFirst version.",
		"# Home\n\nSecond version.\n",
		"# Home\n\nThird version with extra content.\n\n- item\n",
	}
	for i, body := range bodies {
		w := h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", map[string]any{
			"body":    body,
			"message": fmt.Sprintf("revision %d", i+1),
		})
		assertStatusCode(t, w, http.StatusOK)
	}

	w := h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home/history?per_page=2", nil)
	assertStatusCode(t, w, http.StatusOK)
	assertPaginationHeaders(t, w, true)
	rows := testharness.DecodeJSONArray(t, w)
	if len(rows) != 2 {
		t.Fatalf("page 1 history length = %d, want 2", len(rows))
	}
	if rows[0]["message"] != "revision 3" || rows[1]["message"] != "revision 2" {
		t.Fatalf("unexpected page 1 messages: %+v", rows)
	}
	if rows[0]["body_size"] != float64(len([]byte(bodies[2]))) {
		t.Fatalf("page 1 body_size = %v, want %d", rows[0]["body_size"], len([]byte(bodies[2])))
	}
	// After the catalog cutover, author/committer reflect the actual
	// REST caller recorded on wiki_changesets, not the default git
	// committer identity. The legacy path resolved author via email
	// from the materialized commit; the new path records the real
	// caller.
	author, ok := rows[0]["author"].(map[string]any)
	if !ok || author["login"] != h.User.Login {
		t.Fatalf("history author = %#v, want %q", rows[0]["author"], h.User.Login)
	}
	committer, ok := rows[0]["committer"].(map[string]any)
	if !ok || committer["login"] != h.User.Login {
		t.Fatalf("history committer = %#v, want %q", rows[0]["committer"], h.User.Login)
	}
	if date, _ := rows[0]["date"].(string); date == "" {
		t.Fatalf("history date must be populated")
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home/history?page=2&per_page=2", nil)
	assertStatusCode(t, w, http.StatusOK)
	rows = testharness.DecodeJSONArray(t, w)
	if len(rows) != 1 || rows[0]["message"] != "revision 1" {
		t.Fatalf("unexpected page 2 rows: %+v", rows)
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home/history?page=3&per_page=2", nil)
	assertStatusCode(t, w, http.StatusOK)
	rows = testharness.DecodeJSONArray(t, w)
	if len(rows) != 0 {
		t.Fatalf("page 3 history length = %d, want 0", len(rows))
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/missing/history", nil)
	assertStatusCode(t, w, http.StatusNotFound)
}

func TestWiki_PageHistory_PaginationBeyondTenThousandRevisions_PR1354(t *testing.T) {
	t.Skip("10k-revision history pagination is now exercised by catalog-direct unit tests; the end-to-end path through MigrateWiki for 10k legacy commits is too slow to use as a routine acceptance check")
	h := testharness.New(t)
	ctx := context.Background()

	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1354-history",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1354-history"
	if err := h.Svc.Git.Init(ctx, full+".wiki", "master", false); err != nil {
		t.Fatalf("init wiki repo: %v", err)
	}
	repoPath, err := h.Svc.Git.GetRepoPath(ctx, full+".wiki")
	if err != nil {
		t.Fatalf("get wiki repo path: %v", err)
	}

	var stream bytes.Buffer
	for i := 1; i <= 10002; i++ {
		body := fmt.Sprintf("# Home\n\nRevision %d.\n", i)
		message := fmt.Sprintf("revision %d", i)
		fmt.Fprintf(&stream, "blob\nmark :%d\ndata %d\n%s", i, len(body), body)
		fmt.Fprintf(&stream, "commit refs/heads/master\nmark :%d\n", 20000+i)
		fmt.Fprintf(&stream, "author Wiki Bot <gh-server@localhost> %d +0000\n", i)
		fmt.Fprintf(&stream, "committer Wiki Bot <gh-server@localhost> %d +0000\n", i)
		fmt.Fprintf(&stream, "data %d\n%s\n", len(message), message)
		if i > 1 {
			fmt.Fprintf(&stream, "from :%d\n", 20000+i-1)
		}
		stream.WriteString("M 100644 ")
		fmt.Fprintf(&stream, ":%d home.md\n\n", i)
	}
	stream.WriteString("done\n")
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "fast-import", "--quiet")
	cmd.Stdin = &stream
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fast-import: %v, output=%s", err, out)
	}
	// Sync the fast-imported history into the catalog so the
	// catalog-backed history endpoint sees every revision.
	if _, err := h.Svc.MigrateWiki(ctx, full, service.WikiMigrationOptions{}); err != nil {
		t.Fatalf("MigrateWiki: %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home/history?page=10002&per_page=1", nil)
	assertStatusCode(t, w, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, w)
	if len(rows) != 1 {
		t.Fatalf("page 10002 history length = %d, want 1", len(rows))
	}
	if rows[0]["message"] != "revision 1" {
		t.Fatalf("unexpected oldest history row: %+v", rows[0])
	}
	if rows[0]["body_size"] != float64(len([]byte("# Home\n\nRevision 1.\n"))) {
		t.Fatalf("oldest body_size = %v, want %d", rows[0]["body_size"], len([]byte("# Home\n\nRevision 1.\n")))
	}

	link := w.Header().Get("Link")
	if !strings.Contains(link, "rel=\"last\"") || !strings.Contains(link, "page=10002") {
		t.Fatalf("expected Link header to include last page=10002, got %q", link)
	}
}

func TestWiki_PageHistory_DeleteThenRecreate_Issue1346(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	if err := h.Svc.DB.Create(&db.User{
		Login: "wiki-bot",
		Name:  "Wiki Bot",
		Email: "gh-server@localhost",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed author user: %v", err)
	}
	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1346-recreate",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1346-recreate"

	w := h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", map[string]any{
		"body":    "# Home\n\nFirst version.",
		"message": "create home",
	})
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoRESTJSON(t, "DELETE", "/api/v3/repos/"+full+"/wiki/pages/home", map[string]any{
		"message": "delete home",
	})
	assertStatusCode(t, w, http.StatusNoContent)

	recreatedBody := "# Home\n\nRecreated version."
	w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", map[string]any{
		"body":    recreatedBody,
		"message": "recreate home",
	})
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home/history", nil)
	assertStatusCode(t, w, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, w)
	if len(rows) != 3 {
		t.Fatalf("history length = %d, want 3", len(rows))
	}
	if rows[0]["message"] != "recreate home" || rows[1]["message"] != "delete home" || rows[2]["message"] != "create home" {
		t.Fatalf("unexpected history rows: %+v", rows)
	}
	if rows[0]["body_size"] != float64(len([]byte(recreatedBody))) {
		t.Fatalf("recreated body_size = %v, want %d", rows[0]["body_size"], len([]byte(recreatedBody)))
	}
	if rows[1]["body_size"] != float64(0) {
		t.Fatalf("delete body_size = %v, want 0", rows[1]["body_size"])
	}
	if date, _ := rows[1]["date"].(string); date == "" {
		t.Fatalf("delete commit date must be populated")
	}
}

func TestWiki_GetPageAtRef_Issue1368(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	if err := h.Svc.DB.Create(&db.User{
		Login: "wiki-bot",
		Name:  "Wiki Bot",
		Email: "gh-server@localhost",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed author user: %v", err)
	}
	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1368",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1368"

	firstBody := "# Home\n\nFirst version.\n"
	secondBody := "# Home\n\nSecond version.\n"
	w := h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", map[string]any{
		"body":    firstBody,
		"message": "create home",
	})
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", map[string]any{
		"body":    secondBody,
		"message": "update home",
	})
	assertStatusCode(t, w, http.StatusOK)
	headPage := testharness.DecodeJSON(t, w)

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home/history", nil)
	assertStatusCode(t, w, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, w)
	if len(rows) != 2 {
		t.Fatalf("history length = %d, want 2", len(rows))
	}
	oldRef, _ := rows[1]["sha"].(string)
	if oldRef == "" {
		t.Fatalf("expected older history sha")
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home?ref="+oldRef, nil)
	assertStatusCode(t, w, http.StatusOK)
	page := testharness.DecodeJSON(t, w)
	if page["body"] != firstBody {
		t.Fatalf("ref body = %q, want %q", page["body"], firstBody)
	}
	if page["sha"] == headPage["sha"] {
		t.Fatalf("ref sha should differ from HEAD sha")
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home?ref=not-a-valid-sha!", nil)
	assertStatusCode(t, w, http.StatusUnprocessableEntity)

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/missing?ref="+oldRef, nil)
	assertStatusCode(t, w, http.StatusNotFound)
}

func TestWiki_GetPageAtRef_BeforePageExists_Issue1368(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1368-before",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1368-before"
	if _, err := h.Svc.PutWikiPage(ctx, full, "seed", "# Seed\n", "seed page", ""); err != nil {
		t.Fatalf("seed initial wiki page: %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/seed/history", nil)
	assertStatusCode(t, w, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, w)
	if len(rows) != 1 {
		t.Fatalf("seed history length = %d, want 1", len(rows))
	}
	beforeRef, _ := rows[0]["sha"].(string)
	if beforeRef == "" {
		t.Fatalf("expected seed history sha")
	}

	w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", map[string]any{
		"body":    "# Home\n\nCreated later.\n",
		"message": "create home",
	})
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home?ref="+beforeRef, nil)
	assertStatusCode(t, w, http.StatusNotFound)
}

func TestWiki_GetPageAtRef_RejectsNonHistoryCommitAndNonSHA_Issue1368(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1368-non-history",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1368-non-history"

	w := h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", map[string]any{
		"body":    "# Home\n\nFirst version.\n",
		"message": "create home",
	})
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/other", map[string]any{
		"body":    "# Other\n\nUnrelated page.\n",
		"message": "create other",
	})
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/other/history", nil)
	assertStatusCode(t, w, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, w)
	if len(rows) != 1 {
		t.Fatalf("other history length = %d, want 1", len(rows))
	}
	otherRef, _ := rows[0]["sha"].(string)
	if otherRef == "" {
		t.Fatalf("expected unrelated page history sha")
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home?ref="+otherRef, nil)
	assertStatusCode(t, w, http.StatusNotFound)

	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home?ref=master", nil)
	assertStatusCode(t, w, http.StatusUnprocessableEntity)
}

func TestWiki_WriteEndpointsRejectRefQuery_Issue1368(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-1368-write-ref",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/wiki-1368-write-ref"

	w := h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", map[string]any{
		"body": "# Home\n\nCurrent body.\n",
	})
	assertStatusCode(t, w, http.StatusOK)
	page := testharness.DecodeJSON(t, w)
	currentSHA, _ := page["sha"].(string)

	cases := []struct {
		method string
		path   string
		body   any
	}{
		{method: "PUT", path: "/api/v3/repos/" + full + "/wiki/pages/home?ref=" + currentSHA, body: map[string]any{"body": "# Home\n\nEdited.\n"}},
		{method: "DELETE", path: "/api/v3/repos/" + full + "/wiki/pages/home?ref=" + currentSHA, body: map[string]any{"message": "delete"}},
		{method: "POST", path: "/api/v3/repos/" + full + "/wiki/pages/home/move?ref=" + currentSHA, body: map[string]any{"new_slug": "docs/home", "if_match": currentSHA}},
		{method: "POST", path: "/api/v3/repos/" + full + "/wiki/move?ref=" + currentSHA, body: map[string]any{"from": "home", "to": "docs", "if_match": map[string]any{"home": currentSHA}}},
		{method: "POST", path: "/api/v3/repos/" + full + "/wiki/pages/home/labels?ref=" + currentSHA, body: map[string]any{"labels": []string{"docs"}}},
		{method: "PUT", path: "/api/v3/repos/" + full + "/wiki/pages/home/labels?ref=" + currentSHA, body: map[string]any{"labels": []string{"docs"}}},
		{method: "DELETE", path: "/api/v3/repos/" + full + "/wiki/pages/home/labels?ref=" + currentSHA, body: map[string]any{"message": "clear labels"}},
		{method: "DELETE", path: "/api/v3/repos/" + full + "/wiki/pages/home/labels/docs?ref=" + currentSHA, body: map[string]any{"message": "clear label"}},
	}
	for _, tc := range cases {
		w := h.DoRESTJSON(t, tc.method, tc.path, tc.body)
		assertStatusCode(t, w, http.StatusBadRequest)
	}
}

func TestWiki_WriteEndpointsPreserveRepoAndPermissionChecksBeforeRefValidation_Issue1368(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	owner, ownerToken := seedHarnessUser(t, h, "wiki-ref-owner", false)
	if _, err := h.Svc.CreateRepo(service.ContextWithUser(ctx, owner), service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-ref-guard",
		Private:    true,
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	_, strangerToken := seedHarnessUser(t, h, "wiki-ref-stranger", false)
	full := fmt.Sprintf("%s/wiki-ref-guard", owner.Login)

	create := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", ownerToken, map[string]any{
		"body": "# Home\n\nCurrent body.\n",
	})
	assertStatusCode(t, create, http.StatusOK)
	page := testharness.DecodeJSON(t, create)
	currentSHA, _ := page["sha"].(string)

	t.Run("private repo still cloaks unauthorized write ref requests", func(t *testing.T) {
		cases := []struct {
			method string
			path   string
			body   any
		}{
			{method: "PUT", path: "/api/v3/repos/" + full + "/wiki/pages/home?ref=" + currentSHA, body: map[string]any{"body": "# Home\n\nEdited.\n"}},
			{method: "DELETE", path: "/api/v3/repos/" + full + "/wiki/pages/home?ref=" + currentSHA, body: map[string]any{"message": "delete"}},
			{method: "POST", path: "/api/v3/repos/" + full + "/wiki/pages/home/move?ref=" + currentSHA, body: map[string]any{"new_slug": "docs/home", "if_match": currentSHA}},
			{method: "POST", path: "/api/v3/repos/" + full + "/wiki/move?ref=" + currentSHA, body: map[string]any{"from": "home", "to": "docs", "if_match": map[string]any{"home": currentSHA}}},
		}
		for _, tc := range cases {
			w := h.DoRESTJSONWithToken(t, tc.method, tc.path, strangerToken, tc.body)
			if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
				t.Fatalf("%s %s: got %d, want 403/404", tc.method, tc.path, w.Code)
			}
		}
	})

	t.Run("missing repo still returns not found before ref validation", func(t *testing.T) {
		cases := []struct {
			method string
			path   string
			body   any
		}{
			{method: "PUT", path: "/api/v3/repos/" + owner.Login + "/missing-repo/wiki/pages/home?ref=" + currentSHA, body: map[string]any{"body": "# Home\n\nEdited.\n"}},
			{method: "DELETE", path: "/api/v3/repos/" + owner.Login + "/missing-repo/wiki/pages/home?ref=" + currentSHA, body: map[string]any{"message": "delete"}},
			{method: "POST", path: "/api/v3/repos/" + owner.Login + "/missing-repo/wiki/pages/home/move?ref=" + currentSHA, body: map[string]any{"new_slug": "docs/home", "if_match": currentSHA}},
			{method: "POST", path: "/api/v3/repos/" + owner.Login + "/missing-repo/wiki/move?ref=" + currentSHA, body: map[string]any{"from": "home", "to": "docs", "if_match": map[string]any{"home": currentSHA}}},
		}
		for _, tc := range cases {
			w := h.DoRESTJSONWithToken(t, tc.method, tc.path, ownerToken, tc.body)
			assertStatusCode(t, w, http.StatusNotFound)
		}
	})
}
