package rest_test

import (
	"context"
	"net/http"
	"testing"

	"gh-server/internal/service"
	"gh-server/internal/testharness"
)

// Regression for issue #1296 Phase D: Pages REST surface — config
// CRUD plus build-trigger bookkeeping. v1 doesn't run a real build
// pipeline; the test only verifies the recorded state matches the
// requests.
func TestPages_ConfigAndBuildHistory_Issue1296(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	if _, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "pages-1296",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := "testuser/pages-1296"

	// GET before enable → 404.
	w := h.DoREST(t, "GET", "/api/v3/repos/"+full+"/pages", nil)
	assertStatusCode(t, w, http.StatusNotFound)

	// POST enable.
	w = h.DoRESTJSON(t, "POST", "/api/v3/repos/"+full+"/pages", map[string]any{
		"source": map[string]any{"branch": "main", "path": "/docs"},
	})
	assertStatusCode(t, w, http.StatusCreated)
	body := testharness.DecodeJSON(t, w)
	src, _ := body["source"].(map[string]any)
	if src == nil || src["branch"] != "main" || src["path"] != "/docs" {
		t.Errorf("source after enable: got %v", src)
	}
	if body["status"] != service.PagesBuildStatusQueued {
		t.Errorf("status after enable: got %v, want %q", body["status"], service.PagesBuildStatusQueued)
	}
	if body["html_url"] != "https://localhost:8080/pages/testuser/pages-1296" {
		t.Errorf("html_url after enable: got %v", body["html_url"])
	}
	w = h.DoREST(t, "GET", "/api/v3/repos/"+full, nil)
	assertStatusCode(t, w, http.StatusOK)
	repoBody := testharness.DecodeJSON(t, w)
	if repoBody["has_pages"] != true {
		t.Errorf("repo has_pages after enable: got %v, want true", repoBody["has_pages"])
	}

	// POST again → 409.
	w = h.DoRESTJSON(t, "POST", "/api/v3/repos/"+full+"/pages", map[string]any{
		"source": map[string]any{"branch": "main", "path": "/"},
	})
	assertStatusCode(t, w, http.StatusConflict)

	// PUT partial update (only cname).
	w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+full+"/pages", map[string]any{
		"cname":          "docs.example.com",
		"https_enforced": true,
	})
	assertStatusCode(t, w, http.StatusNoContent)

	// GET reflects the update; source from POST is preserved.
	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/pages", nil)
	assertStatusCode(t, w, http.StatusOK)
	body = testharness.DecodeJSON(t, w)
	if body["cname"] != "docs.example.com" {
		t.Errorf("cname after PUT: got %v", body["cname"])
	}
	if body["html_url"] != "https://docs.example.com" {
		t.Errorf("html_url after cname PUT: got %v", body["html_url"])
	}
	if body["https_enforced"] != true {
		t.Errorf("https_enforced after PUT: got %v", body["https_enforced"])
	}
	if body["status"] != service.PagesBuildStatusQueued {
		t.Errorf("status after PUT: got %v, want %q", body["status"], service.PagesBuildStatusQueued)
	}
	src, _ = body["source"].(map[string]any)
	if src == nil || src["branch"] != "main" || src["path"] != "/docs" {
		t.Errorf("source preserved across PUT: got %v", src)
	}

	// POST a build.
	w = h.DoRESTJSON(t, "POST", "/api/v3/repos/"+full+"/pages/builds", nil)
	assertStatusCode(t, w, http.StatusCreated)
	body = testharness.DecodeJSON(t, w)
	if body["status"] != service.PagesBuildStatusQueued {
		t.Errorf("build status: got %v, want %q", body["status"], service.PagesBuildStatusQueued)
	}
	commit, _ := body["commit"].(string)
	if len(commit) != 40 {
		t.Errorf("build commit: got %v, want 40-char sha", body["commit"])
	}

	// GET build history.
	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/pages/builds", nil)
	assertStatusCode(t, w, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, w)
	if len(rows) != 1 {
		t.Fatalf("build history: got %d rows, want 1", len(rows))
	}
	if rows[0]["commit"] != commit {
		t.Errorf("build history commit: got %v, want %v", rows[0]["commit"], commit)
	}

	// DELETE pages.
	w = h.DoREST(t, "DELETE", "/api/v3/repos/"+full+"/pages", nil)
	assertStatusCode(t, w, http.StatusNoContent)

	// GET after disable → 404.
	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/pages", nil)
	assertStatusCode(t, w, http.StatusNotFound)
	w = h.DoREST(t, "GET", "/api/v3/repos/"+full, nil)
	assertStatusCode(t, w, http.StatusOK)
	repoBody = testharness.DecodeJSON(t, w)
	if repoBody["has_pages"] != false {
		t.Errorf("repo has_pages after disable: got %v, want false", repoBody["has_pages"])
	}

	// Build history is dropped with the config.
	w = h.DoREST(t, "GET", "/api/v3/repos/"+full+"/pages/builds", nil)
	assertStatusCode(t, w, http.StatusOK)
	if rows := testharness.DecodeJSONArray(t, w); len(rows) != 0 {
		t.Errorf("build history after disable: got %d rows, want 0", len(rows))
	}

	// POST build before re-enable → 404.
	w = h.DoRESTJSON(t, "POST", "/api/v3/repos/"+full+"/pages/builds", nil)
	assertStatusCode(t, w, http.StatusNotFound)
}

// Regression for issue #1296 Phase D: Pages config writes require admin.
func TestPages_AdminPermissionRequired_Issue1296(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	owner, _ := seedHarnessUser(t, h, "pages-admin-owner", false)
	if _, err := h.Svc.CreateRepo(service.ContextWithUser(ctx, owner), service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "pages-perm",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	stranger, strangerToken := seedHarnessUser(t, h, "pages-stranger", false)
	_ = stranger

	w := h.DoRESTJSONWithToken(t, "POST", "/api/v3/repos/pages-admin-owner/pages-perm/pages", strangerToken, map[string]any{
		"source": map[string]any{"branch": "main"},
	})
	if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Errorf("non-admin enable: got %d, want 403/404", w.Code)
	}
}
