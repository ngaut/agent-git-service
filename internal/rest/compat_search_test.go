package rest_test

import (
	"context"
	"net/url"
	"strconv"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

// ─── Search Repos Response Shape ────────────────────────────────────────────

func TestCompat_SearchRepos_ResponseShape(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "compat-search-repo")

	q := url.QueryEscape("compat-search-repo")
	w := h.DoREST(t, "GET", "/api/v3/search/repositories?q="+q, nil)
	assertStatusCode(t, w, 200)
	body := testharness.DecodeJSON(t, w)

	// GitHub search response wraps results in {total_count, incomplete_results, items}.
	assertFieldPresent(t, body, "total_count", "number")
	assertFieldPresent(t, body, "incomplete_results", "bool")
	assertFieldPresent(t, body, "items", "array")

	items, _ := body["items"].([]any)
	if len(items) == 0 {
		t.Fatal("expected at least 1 search result")
	}
	repo, _ := items[0].(map[string]any)
	assertFieldPresent(t, repo, "full_name", "string")
	assertFieldPresent(t, repo, "score", "number")
}

func TestCompat_SearchRepos_IncludesComputedSizeWithoutQualifier(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "compat-search-size")

	fullName := "testuser/compat-search-size"
	if _, err := h.Svc.Git.WriteFile(ctx, fullName, "main", "payload.txt", "add payload", []byte("search size payload")); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	wantSize := float64(h.Svc.GitDiskUsageKB(ctx, fullName))
	if wantSize <= 0 {
		t.Fatalf("expected positive repository size, got %v", wantSize)
	}

	q := url.QueryEscape("compat-search-size")
	w := h.DoREST(t, "GET", "/api/v3/search/repositories?q="+q, nil)
	assertStatusCode(t, w, 200)
	body := testharness.DecodeJSON(t, w)

	items, _ := body["items"].([]any)
	if len(items) == 0 {
		t.Fatal("expected at least 1 search result")
	}
	repo, _ := items[0].(map[string]any)
	if gotSize, ok := repo["size"].(float64); !ok || gotSize != wantSize {
		t.Fatalf("expected search result size %v, got %v", wantSize, repo["size"])
	}
}

// ─── Search Issues Response Shape ───────────────────────────────────────────

func TestCompat_SearchIssues_ResponseShape(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "compat-search-issue")

	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/compat-search-issue/issues", map[string]any{
		"title": "searchable bug report",
	})
	assertStatusCode(t, w, 201)

	q := url.QueryEscape("searchable bug")
	w = h.DoREST(t, "GET", "/api/v3/search/issues?q="+q, nil)
	assertStatusCode(t, w, 200)
	body := testharness.DecodeJSON(t, w)

	assertFieldPresent(t, body, "total_count", "number")
	assertFieldPresent(t, body, "incomplete_results", "bool")
	assertFieldPresent(t, body, "items", "array")

	items, _ := body["items"].([]any)
	if len(items) == 0 {
		t.Fatal("expected at least 1 search result")
	}
	item, _ := items[0].(map[string]any)
	assertFieldPresent(t, item, "score", "number")
}

// ─── Search Labels Response Shape ───────────────────────────────────────────

func TestCompat_SearchLabels_ResponseShape(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "compat-search-labels")

	full := "testuser/compat-search-labels"
	repo, err := h.Svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if _, err := h.Svc.CreateLabel(ctx, full, "bug", "d73a4a", "Something is not working"); err != nil {
		t.Fatalf("CreateLabel(bug): %v", err)
	}
	if _, err := h.Svc.CreateLabel(ctx, full, "docs", "0075ca", "Documentation work"); err != nil {
		t.Fatalf("CreateLabel(docs): %v", err)
	}

	q := url.QueryEscape("bug")
	w := h.DoREST(t, "GET", "/api/v3/search/labels?q="+q+"&repository_id="+url.QueryEscape(strconv.FormatUint(uint64(repo.ID), 10)), nil)
	assertStatusCode(t, w, 200)
	body := testharness.DecodeJSON(t, w)

	assertFieldPresent(t, body, "total_count", "number")
	assertFieldPresent(t, body, "incomplete_results", "bool")
	assertFieldPresent(t, body, "items", "array")

	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 label search result, got %d: %v", len(items), items)
	}
	item, _ := items[0].(map[string]any)
	assertFieldPresent(t, item, "id", "number")
	assertFieldPresent(t, item, "node_id", "string")
	assertFieldPresent(t, item, "name", "string")
	assertFieldPresent(t, item, "color", "string")
	assertFieldPresent(t, item, "description", "string")
	assertFieldPresent(t, item, "default", "bool")
	assertFieldPresent(t, item, "url", "string")
	assertFieldPresent(t, item, "score", "number")
	if item["name"] != "bug" {
		t.Fatalf("name: got %v, want bug", item["name"])
	}

	w = h.DoREST(t, "GET", "/api/v3/search/labels?q="+q, nil)
	assertStatusCode(t, w, 422)
}

// ─── Search Users Response Shape ────────────────────────────────────────────

func TestCompat_SearchUsers_ResponseShape(t *testing.T) {
	h := testharness.New(t)

	human := db.User{
		Login: "compat-search-human",
		Name:  "Compat Search Human",
		Email: "compat-search-human@example.com",
		Bio:   "Find this human account",
		Type:  db.TypeUser,
	}
	org := db.User{
		Login: "compat-search-org",
		Name:  "Compat Search Org",
		Bio:   "Find this organization account",
		Type:  db.TypeOrganization,
	}
	for _, user := range []*db.User{&human, &org} {
		if err := h.DB.Create(user).Error; err != nil {
			t.Fatalf("create user %s: %v", user.Login, err)
		}
	}

	q := url.QueryEscape("compat-search-human type:user")
	w := h.DoREST(t, "GET", "/api/v3/search/users?q="+q, nil)
	assertStatusCode(t, w, 200)
	body := testharness.DecodeJSON(t, w)

	assertFieldPresent(t, body, "total_count", "number")
	assertFieldPresent(t, body, "incomplete_results", "bool")
	assertFieldPresent(t, body, "items", "array")

	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 user search result, got %d: %v", len(items), items)
	}
	item, _ := items[0].(map[string]any)
	assertFieldPresent(t, item, "id", "number")
	assertFieldPresent(t, item, "node_id", "string")
	assertFieldPresent(t, item, "login", "string")
	assertFieldPresent(t, item, "type", "string")
	assertFieldPresent(t, item, "html_url", "string")
	assertFieldPresent(t, item, "url", "string")
	assertFieldPresent(t, item, "score", "number")
	if item["login"] != human.Login {
		t.Fatalf("login: got %v, want %s", item["login"], human.Login)
	}
	if item["type"] != db.TypeUser {
		t.Fatalf("type: got %v, want %s", item["type"], db.TypeUser)
	}

	q = url.QueryEscape("compat-search-org type:org")
	w = h.DoREST(t, "GET", "/api/v3/search/users?q="+q, nil)
	assertStatusCode(t, w, 200)
	body = testharness.DecodeJSON(t, w)
	items, _ = body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 org search result, got %d: %v", len(items), items)
	}
	item, _ = items[0].(map[string]any)
	if item["login"] != org.Login {
		t.Fatalf("org login: got %v, want %s", item["login"], org.Login)
	}
	if item["type"] != db.TypeOrganization {
		t.Fatalf("org type: got %v, want %s", item["type"], db.TypeOrganization)
	}
}

func TestCompat_SearchUsers_UnsupportedQualifiersDoNotBroadenResults(t *testing.T) {
	h := testharness.New(t)

	user := db.User{
		Login: "compat-qualifier-user",
		Name:  "Compat Qualifier User",
		Email: "compat-qualifier-user@example.com",
		Bio:   "Should not match unsupported qualifier fallbacks",
		Type:  db.TypeUser,
	}
	if err := h.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user %s: %v", user.Login, err)
	}

	cases := []struct {
		name  string
		query string
	}{
		{
			name:  "qualifier only",
			query: "followers:>999999",
		},
		{
			name:  "text plus unsupported qualifier",
			query: "compat-qualifier-user followers:>999999",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := h.DoREST(t, "GET", "/api/v3/search/users?q="+url.QueryEscape(tc.query), nil)
			assertStatusCode(t, w, 200)
			body := testharness.DecodeJSON(t, w)

			items, _ := body["items"].([]any)
			if len(items) != 0 {
				t.Fatalf("expected no user search results for %q, got %d: %v", tc.query, len(items), items)
			}
			if total, _ := body["total_count"].(float64); total != 0 {
				t.Fatalf("expected total_count=0 for %q, got %v", tc.query, body["total_count"])
			}
		})
	}
}

// ─── Search Topics Response Shape ───────────────────────────────────────────

func TestCompat_SearchTopics_ResponseShape(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "compat-search-topic-one")
	compatSeedRepo(t, h, "compat-search-topic-two")

	w := h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/compat-search-topic-one/topics", map[string]any{
		"names": []string{"compat-topic", "other-topic"},
	})
	assertStatusCode(t, w, 200)
	w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/compat-search-topic-two/topics", map[string]any{
		"names": []string{"compat-topic"},
	})
	assertStatusCode(t, w, 200)

	q := url.QueryEscape("compat-topic repositories:>=2")
	w = h.DoREST(t, "GET", "/api/v3/search/topics?q="+q, nil)
	assertStatusCode(t, w, 200)
	body := testharness.DecodeJSON(t, w)

	assertFieldPresent(t, body, "total_count", "number")
	assertFieldPresent(t, body, "incomplete_results", "bool")
	assertFieldPresent(t, body, "items", "array")

	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 topic search result, got %d: %v", len(items), items)
	}
	item, _ := items[0].(map[string]any)
	assertFieldPresent(t, item, "name", "string")
	assertFieldPresent(t, item, "display_name", "string")
	assertFieldPresent(t, item, "short_description", "string")
	assertFieldPresent(t, item, "description", "string")
	assertFieldPresent(t, item, "created_by", "string")
	assertFieldPresent(t, item, "released", "string")
	assertFieldPresent(t, item, "created_at", "string")
	assertFieldPresent(t, item, "updated_at", "string")
	assertFieldPresent(t, item, "featured", "bool")
	assertFieldPresent(t, item, "curated", "bool")
	assertFieldPresent(t, item, "score", "number")
	if item["name"] != "compat-topic" {
		t.Fatalf("name: got %v, want compat-topic", item["name"])
	}
	if count, _ := item["repository_count"].(float64); count != 2 {
		t.Fatalf("repository_count: got %v, want 2", item["repository_count"])
	}

	w = h.DoREST(t, "GET", "/api/v3/search/topics?q="+url.QueryEscape("compat-topic is:featured"), nil)
	assertStatusCode(t, w, 200)
	body = testharness.DecodeJSON(t, w)
	items, _ = body["items"].([]any)
	if len(items) != 0 {
		t.Fatalf("expected unsupported topic metadata filter to return no results, got %d: %v", len(items), items)
	}
}

// ─── Search Missing Query Validation ───────────────────────────────────────

func TestCompat_Search_MissingQueryReturns422(t *testing.T) {
	h := testharness.New(t)
	endpoints := []string{
		"/api/v3/search/repositories",
		"/api/v3/search/issues",
		"/api/v3/search/commits",
		"/api/v3/search/code",
		"/api/v3/search/labels",
		"/api/v3/search/users",
		"/api/v3/search/topics",
	}
	for _, path := range endpoints {
		w := h.DoREST(t, "GET", path, nil)
		assertStatusCode(t, w, 422)
		body := testharness.DecodeJSON(t, w)
		if body["message"] != "Validation Failed" {
			t.Fatalf("expected Validation Failed for %s, got %v", path, body["message"])
		}
	}
}
