package rest_test

import (
	"net/http"
	"testing"

	"github.com/ngaut/agent-git-service/internal/testharness"
)

// ─── Repo GET Response Fields ───────────────────────────────────────────────

func TestCompat_RepoGet_ResponseFields(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "compat-repo")

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-repo", nil)
	assertStatusCode(t, w, 200)
	body := testharness.DecodeJSON(t, w)

	// GitHub REST API Repository response fields.
	assertFieldsPresent(t, body, map[string]string{
		"id":                             "number",
		"node_id":                        "string",
		"name":                           "string",
		"full_name":                      "string",
		"description":                    "",
		"private":                        "bool",
		"visibility":                     "string",
		"fork":                           "bool",
		"archived":                       "bool",
		"disabled":                       "bool",
		"is_template":                    "bool",
		"owner":                          "object",
		"html_url":                       "string",
		"url":                            "string",
		"git_url":                        "string",
		"ssh_url":                        "string",
		"clone_url":                      "string",
		"default_branch":                 "string",
		"created_at":                     "string",
		"updated_at":                     "string",
		"pushed_at":                      "string",
		"language":                       "",
		"has_issues":                     "bool",
		"has_projects":                   "bool",
		"has_wiki":                       "bool",
		"has_pages":                      "bool",
		"has_downloads":                  "bool",
		"has_discussions":                "bool",
		"forks_count":                    "number",
		"open_issues_count":              "number",
		"stargazers_count":               "number",
		"size":                           "number",
		"allow_forking":                  "bool",
		"topics":                         "array",
		"forks":                          "number",
		"open_issues":                    "number",
		"watchers":                       "number",
		"watchers_count":                 "number",
		"allow_merge_commit":             "bool",
		"allow_squash_merge":             "bool",
		"allow_rebase_merge":             "bool",
		"allow_auto_merge":               "bool",
		"allow_update_branch":            "bool",
		"delete_branch_on_merge":         "bool",
		"use_squash_pr_title_as_default": "bool",
		"squash_merge_commit_title":      "string",
		"squash_merge_commit_message":    "string",
		"merge_commit_title":             "string",
		"merge_commit_message":           "string",
		"web_commit_signoff_required":    "bool",
		"security_and_analysis":          "object",
		"permissions":                    "object",
	})

	// Verify owner sub-object.
	owner, _ := body["owner"].(map[string]any)
	if owner != nil {
		assertFieldsPresent(t, owner, map[string]string{
			"login":      "string",
			"id":         "number",
			"avatar_url": "string",
			"url":        "string",
			"html_url":   "string",
			"type":       "string",
		})
	}

	security, ok := body["security_and_analysis"].(map[string]any)
	if !ok {
		t.Fatalf("security_and_analysis: expected object, got %T", body["security_and_analysis"])
	}
	for _, field := range []string{
		"advanced_security",
		"code_security",
		"dependabot_security_updates",
		"secret_scanning",
		"secret_scanning_push_protection",
	} {
		item, ok := security[field].(map[string]any)
		if !ok {
			t.Fatalf("security_and_analysis.%s: expected object, got %T", field, security[field])
		}
		if item["status"] != "disabled" {
			t.Fatalf("security_and_analysis.%s.status: got %v, want disabled", field, item["status"])
		}
	}
}

// ─── Repo PATCH Response Includes Stats ─────────────────────────────────────

func TestCompat_RepoPATCH_ResponseIncludesStats(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "compat-repo-patch")

	w := h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/compat-repo-patch", map[string]any{
		"description": "updated desc",
	})
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)

	// UpdateRepo should return the same shape as GetRepo, including stats.
	assertFieldsPresent(t, resp, map[string]string{
		"forks_count":       "number",
		"open_issues_count": "number",
		"stargazers_count":  "number",
		"size":              "number",
		"permissions":       "object",
	})
}

func TestCompat_RepoPATCH_AllowAutoMergeRoundTrip(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "compat-repo-auto-merge")

	w := h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/compat-repo-auto-merge", map[string]any{
		"allow_auto_merge": true,
	})
	assertStatusCode(t, w, http.StatusOK)
	resp := testharness.DecodeJSON(t, w)
	assertBoolField(t, resp, "allow_auto_merge", true)

	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-repo-auto-merge", nil)
	assertStatusCode(t, w, http.StatusOK)
	got := testharness.DecodeJSON(t, w)
	assertBoolField(t, got, "allow_auto_merge", true)
}

func TestCompat_RepoPATCH_AllowUpdateBranchRoundTrip(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "compat-repo-update-branch")

	w := h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/compat-repo-update-branch", map[string]any{
		"allow_update_branch": true,
	})
	assertStatusCode(t, w, http.StatusOK)
	resp := testharness.DecodeJSON(t, w)
	assertBoolField(t, resp, "allow_update_branch", true)

	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-repo-update-branch", nil)
	assertStatusCode(t, w, http.StatusOK)
	got := testharness.DecodeJSON(t, w)
	assertBoolField(t, got, "allow_update_branch", true)
}

func TestCompat_RepoPATCH_HomepageAndFeatureTogglesRoundTrip(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "compat-repo-homepage")

	w := h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/compat-repo-homepage", map[string]any{
		"homepage":        "https://example.com/project",
		"has_projects":    false,
		"has_downloads":   false,
		"has_discussions": true,
	})
	assertStatusCode(t, w, http.StatusOK)
	resp := testharness.DecodeJSON(t, w)
	assertStringField(t, resp, "homepage", "https://example.com/project")
	assertBoolField(t, resp, "has_projects", false)
	assertBoolField(t, resp, "has_downloads", false)
	assertBoolField(t, resp, "has_discussions", true)

	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-repo-homepage", nil)
	assertStatusCode(t, w, http.StatusOK)
	got := testharness.DecodeJSON(t, w)
	assertStringField(t, got, "homepage", "https://example.com/project")
	assertBoolField(t, got, "has_projects", false)
	assertBoolField(t, got, "has_downloads", false)
	assertBoolField(t, got, "has_discussions", true)

	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/compat-repo-homepage", map[string]any{
		"homepage": nil,
	})
	assertStatusCode(t, w, http.StatusOK)
	cleared := testharness.DecodeJSON(t, w)
	assertNilField(t, cleared, "homepage")

	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-repo-homepage", nil)
	assertStatusCode(t, w, http.StatusOK)
	got = testharness.DecodeJSON(t, w)
	assertNilField(t, got, "homepage")
}

// ─── Repo CREATE Modeled Options ────────────────────────────────────────────

func TestCompat_RepoCREATE_ModeledOptionsRoundTrip(t *testing.T) {
	h := testharness.New(t)

	w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
		"name":                   "compat-create-options",
		"description":            "created with GitHub repo options",
		"homepage":               "https://example.com/compat-create-options",
		"has_issues":             false,
		"has_projects":           false,
		"has_wiki":               false,
		"has_downloads":          false,
		"has_discussions":        true,
		"is_template":            true,
		"license_template":       "mit",
		"allow_merge_commit":     false,
		"allow_squash_merge":     false,
		"allow_rebase_merge":     false,
		"allow_auto_merge":       true,
		"delete_branch_on_merge": true,
	})
	assertStatusCode(t, w, http.StatusCreated)
	created := testharness.DecodeJSON(t, w)

	assertBoolField(t, created, "has_issues", false)
	assertBoolField(t, created, "has_projects", false)
	assertBoolField(t, created, "has_wiki", false)
	assertBoolField(t, created, "has_downloads", false)
	assertBoolField(t, created, "has_discussions", true)
	assertStringField(t, created, "homepage", "https://example.com/compat-create-options")
	assertBoolField(t, created, "is_template", true)
	assertBoolField(t, created, "allow_merge_commit", false)
	assertBoolField(t, created, "allow_squash_merge", false)
	assertBoolField(t, created, "allow_rebase_merge", false)
	assertBoolField(t, created, "allow_auto_merge", true)
	assertBoolField(t, created, "delete_branch_on_merge", true)
	assertRepoLicenseKey(t, created, "mit")

	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-create-options", nil)
	assertStatusCode(t, w, http.StatusOK)
	got := testharness.DecodeJSON(t, w)
	assertBoolField(t, got, "has_issues", false)
	assertBoolField(t, got, "has_projects", false)
	assertBoolField(t, got, "has_wiki", false)
	assertBoolField(t, got, "has_downloads", false)
	assertBoolField(t, got, "has_discussions", true)
	assertStringField(t, got, "homepage", "https://example.com/compat-create-options")
	assertBoolField(t, got, "is_template", true)
	assertBoolField(t, got, "allow_merge_commit", false)
	assertBoolField(t, got, "allow_squash_merge", false)
	assertBoolField(t, got, "allow_rebase_merge", false)
	assertBoolField(t, got, "allow_auto_merge", true)
	assertBoolField(t, got, "delete_branch_on_merge", true)
	assertRepoLicenseKey(t, got, "mit")
}

func TestCompat_OrgRepoCREATE_VisibilityRoundTrip(t *testing.T) {
	h := testharness.New(t)

	w := h.DoRESTJSON(t, "POST", "/api/ext/v1/user/orgs", map[string]any{"login": "compat-create-org"})
	assertStatusCode(t, w, http.StatusCreated)

	w = h.DoRESTJSON(t, "POST", "/api/v3/orgs/compat-create-org/repos", map[string]any{
		"name":       "private-by-visibility",
		"private":    false,
		"visibility": "private",
	})
	assertStatusCode(t, w, http.StatusCreated)
	created := testharness.DecodeJSON(t, w)
	assertBoolField(t, created, "private", true)
	if created["visibility"] != "private" {
		t.Fatalf("visibility: got %v, want private", created["visibility"])
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/compat-create-org/private-by-visibility", nil)
	assertStatusCode(t, w, http.StatusOK)
	got := testharness.DecodeJSON(t, w)
	assertBoolField(t, got, "private", true)
	if got["visibility"] != "private" {
		t.Fatalf("visibility after GET: got %v, want private", got["visibility"])
	}
}

func TestCompat_OrgRepoCREATE_InternalVisibilityRoundTrip(t *testing.T) {
	h := testharness.New(t)

	w := h.DoRESTJSON(t, "POST", "/api/ext/v1/user/orgs", map[string]any{"login": "compat-internal-org"})
	assertStatusCode(t, w, http.StatusCreated)

	w = h.DoRESTJSON(t, "POST", "/api/v3/orgs/compat-internal-org/repos", map[string]any{
		"name":       "internal-by-visibility",
		"private":    false,
		"visibility": "internal",
	})
	assertStatusCode(t, w, http.StatusCreated)
	created := testharness.DecodeJSON(t, w)
	assertBoolField(t, created, "private", true)
	if created["visibility"] != "internal" {
		t.Fatalf("visibility: got %v, want internal", created["visibility"])
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/compat-internal-org/internal-by-visibility", nil)
	assertStatusCode(t, w, http.StatusOK)
	got := testharness.DecodeJSON(t, w)
	assertBoolField(t, got, "private", true)
	if got["visibility"] != "internal" {
		t.Fatalf("visibility after GET: got %v, want internal", got["visibility"])
	}
}

func assertRepoLicenseKey(t *testing.T, body map[string]any, want string) {
	t.Helper()
	license, ok := body["license"].(map[string]any)
	if !ok {
		t.Fatalf("license: expected object, got %T", body["license"])
	}
	if license["key"] != want {
		t.Fatalf("license.key: got %v, want %s", license["key"], want)
	}
}

func assertStringField(t *testing.T, body map[string]any, field, want string) {
	t.Helper()
	got, ok := body[field].(string)
	if !ok || got != want {
		t.Fatalf("field %q: expected %q, got %v", field, want, body[field])
	}
}

func assertNilField(t *testing.T, body map[string]any, field string) {
	t.Helper()
	if got, ok := body[field]; !ok || got != nil {
		t.Fatalf("field %q: expected nil, got %v", field, got)
	}
}

// ─── Repo Branch Protected Field ────────────────────────────────────────────

func TestCompat_RepoBranch_ProtectedField(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "compat-repo-branch")

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-repo-branch/branches/main", nil)
	assertStatusCode(t, w, 200)
	body := testharness.DecodeJSON(t, w)

	// Branch response should have "protected" field.
	assertFieldPresent(t, body, "protected", "bool")
	assertFieldPresent(t, body, "name", "string")

	commit, ok := body["commit"].(map[string]any)
	if !ok {
		t.Fatal("commit: expected object")
	}
	assertFieldPresent(t, commit, "sha", "string")
	assertFieldPresent(t, commit, "url", "string")
}
