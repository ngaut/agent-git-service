package rest_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"gh-server/internal/service"
	"gh-server/internal/testharness"
)

func TestWiki_CompactHistory_StartsAsyncJob_Issue1472(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	owner, ownerToken := seedHarnessUser(t, h, "wiki-compact-owner", false)
	_, strangerToken := seedHarnessUser(t, h, "wiki-compact-stranger", false)
	if _, err := h.Svc.CreateRepo(service.ContextWithUser(ctx, owner), service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-compact-rest",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := owner.Login + "/wiki-compact-rest"

	create := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", ownerToken, map[string]any{
		"body":    "# Home\n\nFirst version.\n",
		"message": "create home",
	})
	assertStatusCode(t, create, http.StatusOK)
	page := testharness.DecodeJSON(t, create)
	currentSHA, _ := page["sha"].(string)

	update := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", ownerToken, map[string]any{
		"body":    "# Home\n\nSecond version.\n",
		"message": "update home",
		"sha":     currentSHA,
	})
	assertStatusCode(t, update, http.StatusOK)

	blocked := h.DoRESTJSONWithToken(t, "POST", "/api/v3/repos/"+full+"/wiki/compact", strangerToken, nil)
	if blocked.Code != http.StatusForbidden && blocked.Code != http.StatusNotFound {
		t.Fatalf("non-admin compact expected 403/404, got %d: %s", blocked.Code, blocked.Body.String())
	}

	compact := h.DoRESTJSONWithToken(t, "POST", "/api/v3/repos/"+full+"/wiki/compact", ownerToken, map[string]any{})
	assertStatusCode(t, compact, http.StatusAccepted)
	compactBody := testharness.DecodeJSON(t, compact)
	statusURL, _ := compactBody["status_url"].(string)
	if statusURL == "" {
		t.Fatalf("status_url = %q, want non-empty", statusURL)
	}
	h.Svc.Wg.Wait()

	status := h.DoRESTWithToken(t, "GET", statusURL, ownerToken)
	assertStatusCode(t, status, http.StatusOK)
	statusBody := testharness.DecodeJSON(t, status)
	if statusBody["status"] != service.WikiCompactionJobSucceeded {
		t.Fatalf("job status = %v, want %q", statusBody["status"], service.WikiCompactionJobSucceeded)
	}

	after := h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home/history", nil)
	assertStatusCode(t, after, http.StatusOK)
	rowsAfter := testharness.DecodeJSONArray(t, after)
	if len(rowsAfter) != 1 {
		t.Fatalf("rowsAfter len = %d, want 1", len(rowsAfter))
	}

	req := httptest.NewRequest("POST", "/api/v3/repos/"+full+"/wiki/compact", bytes.NewReader([]byte("{")))
	req.Header.Set("Authorization", "token "+ownerToken)
	req.Header.Set("Content-Type", "application/json")
	invalid := httptest.NewRecorder()
	h.Mux.ServeHTTP(invalid, req)
	assertStatusCode(t, invalid, http.StatusUnprocessableEntity)
}

func TestWiki_CompactHistory_CompletesAfterRequestContextCanceled_Issue1472(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	owner, ownerToken := seedHarnessUser(t, h, "wiki-compact-async-owner", false)
	if _, err := h.Svc.CreateRepo(service.ContextWithUser(ctx, owner), service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-compact-async-rest",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := owner.Login + "/wiki-compact-async-rest"

	create := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", ownerToken, map[string]any{
		"body":    "# Home\n\nFirst version.\n",
		"message": "create home",
	})
	assertStatusCode(t, create, http.StatusOK)
	page := testharness.DecodeJSON(t, create)
	currentSHA, _ := page["sha"].(string)

	update := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/repos/"+full+"/wiki/pages/home", ownerToken, map[string]any{
		"body":    "# Home\n\nSecond version.\n",
		"message": "update home",
		"sha":     currentSHA,
	})
	assertStatusCode(t, update, http.StatusOK)

	reqCtx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/api/v3/repos/"+full+"/wiki/compact", bytes.NewReader([]byte("{}"))).WithContext(reqCtx)
	req.Header.Set("Authorization", "token "+ownerToken)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	h.Mux.ServeHTTP(resp, req)
	cancel()
	assertStatusCode(t, resp, http.StatusAccepted)
	compactBody := testharness.DecodeJSON(t, resp)
	statusURL, _ := compactBody["status_url"].(string)
	if statusURL == "" {
		t.Fatalf("status_url = %q, want non-empty", statusURL)
	}
	h.Svc.Wg.Wait()

	status := h.DoRESTWithToken(t, "GET", statusURL, ownerToken)
	assertStatusCode(t, status, http.StatusOK)
	statusBody := testharness.DecodeJSON(t, status)
	if statusBody["status"] != service.WikiCompactionJobSucceeded {
		t.Fatalf("job status = %v, want %q", statusBody["status"], service.WikiCompactionJobSucceeded)
	}

	after := h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home/history", nil)
	assertStatusCode(t, after, http.StatusOK)
	rowsAfter := testharness.DecodeJSONArray(t, after)
	if len(rowsAfter) != 1 {
		t.Fatalf("rowsAfter len = %d, want 1", len(rowsAfter))
	}
}

func TestWiki_RepairLocks_AdminOnly(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	owner, ownerToken := seedHarnessUser(t, h, "wiki-repair-lock-owner", false)
	_, strangerToken := seedHarnessUser(t, h, "wiki-repair-lock-stranger", false)
	if _, err := h.Svc.CreateRepo(service.ContextWithUser(ctx, owner), service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-repair-locks",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := owner.Login + "/wiki-repair-locks"
	repoPath, err := h.Svc.Git.GetRepoPath(ctx, full+".wiki")
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	lockPath := filepath.Join(repoPath, "refs", "heads", "master.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("lock"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	blocked := h.DoRESTJSONWithToken(t, "POST", "/api/v3/admin/wiki/repos/"+full+"/repair-locks", strangerToken, map[string]any{})
	if blocked.Code != http.StatusForbidden && blocked.Code != http.StatusNotFound {
		t.Fatalf("non-admin repair expected 403/404, got %d: %s", blocked.Code, blocked.Body.String())
	}

	fresh := h.DoRESTJSONWithToken(t, "POST", "/api/v3/admin/wiki/repos/"+full+"/repair-locks", ownerToken, map[string]any{})
	assertStatusCode(t, fresh, http.StatusConflict)

	forced := h.DoRESTJSONWithToken(t, "POST", "/api/v3/admin/wiki/repos/"+full+"/repair-locks", ownerToken, map[string]any{"force": true})
	assertStatusCode(t, forced, http.StatusOK)
	body := testharness.DecodeJSON(t, forced)
	if body["ref"] != "refs/heads/master" {
		t.Fatalf("ref = %v, want refs/heads/master", body["ref"])
	}
	if body["cleared"] != true {
		t.Fatalf("cleared = %v, want true", body["cleared"])
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lock should be removed, stat err = %v", err)
	}
}
