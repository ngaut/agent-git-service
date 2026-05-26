package rest_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gh-server/internal/service"
	"gh-server/internal/testharness"
)

func TestWiki_CompactHistory_AdminOnlyAndRewritesHistory_Issue1460(t *testing.T) {
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

	before := h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home/history", nil)
	assertStatusCode(t, before, http.StatusOK)
	rowsBefore := testharness.DecodeJSONArray(t, before)
	if len(rowsBefore) != 2 {
		t.Fatalf("rowsBefore len = %d, want 2", len(rowsBefore))
	}
	oldRef, _ := rowsBefore[1]["sha"].(string)

	compact := h.DoRESTJSONWithToken(t, "POST", "/api/v3/repos/"+full+"/wiki/compact", ownerToken, map[string]any{})
	assertStatusCode(t, compact, http.StatusAccepted)
	body := testharness.DecodeJSON(t, compact)
	jobID, _ := body["job_id"].(string)
	if jobID == "" {
		t.Fatalf("job_id empty: %#v", body)
	}
	location, _ := body["status_url"].(string)
	if location == "" {
		t.Fatalf("status_url empty: %#v", body)
	}
	if compact.Header().Get("Location") != location {
		t.Fatalf("Location = %q, want %q", compact.Header().Get("Location"), location)
	}

	h.Svc.Wg.Wait()

	status := h.DoRESTWithToken(t, "GET", location, ownerToken)
	assertStatusCode(t, status, http.StatusOK)
	statusBody := testharness.DecodeJSON(t, status)
	if statusBody["status"] != service.WikiCompactionJobSucceeded {
		t.Fatalf("status = %v, want %q", statusBody["status"], service.WikiCompactionJobSucceeded)
	}
	if statusBody["pages"] != float64(1) {
		t.Fatalf("pages = %v, want 1", statusBody["pages"])
	}
	if statusBody["commits_removed"] != float64(1) {
		t.Fatalf("commits_removed = %v, want 1", statusBody["commits_removed"])
	}

	after := h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home/history", nil)
	assertStatusCode(t, after, http.StatusOK)
	rowsAfter := testharness.DecodeJSONArray(t, after)
	if len(rowsAfter) != 1 {
		t.Fatalf("rowsAfter len = %d, want 1", len(rowsAfter))
	}

	refRead := h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home?ref="+oldRef, nil)
	assertStatusCode(t, refRead, http.StatusNotFound)

	req := httptest.NewRequest("POST", "/api/v3/repos/"+full+"/wiki/compact", bytes.NewReader([]byte("{")))
	req.Header.Set("Authorization", "token "+ownerToken)
	req.Header.Set("Content-Type", "application/json")
	invalid := httptest.NewRecorder()
	h.Mux.ServeHTTP(invalid, req)
	assertStatusCode(t, invalid, http.StatusUnprocessableEntity)
}

func TestWiki_CompactHistory_CompletesAfterRequestContextCanceled_Issue1462(t *testing.T) {
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

	started := make(chan string, 1)
	release := make(chan struct{})
	service.SetTestWikiCompactionJobStartedForTest(h.Svc, func(jobID string) {
		select {
		case started <- jobID:
		default:
		}
	})
	service.SetTestWikiCompactionJobContinueForTest(h.Svc, func(string) {
		<-release
	})

	reqCtx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/api/v3/repos/"+full+"/wiki/compact", bytes.NewReader([]byte("{}"))).WithContext(reqCtx)
	req.Header.Set("Authorization", "token "+ownerToken)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	h.Mux.ServeHTTP(resp, req)
	assertStatusCode(t, resp, http.StatusAccepted)
	respBody := testharness.DecodeJSON(t, resp)
	location, _ := respBody["status_url"].(string)
	if location == "" {
		t.Fatalf("status_url empty: %#v", respBody)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async compaction to start")
	}

	cancel()
	close(release)
	h.Svc.Wg.Wait()

	status := h.DoRESTWithToken(t, "GET", location, ownerToken)
	assertStatusCode(t, status, http.StatusOK)
	statusBody := testharness.DecodeJSON(t, status)
	if statusBody["status"] != service.WikiCompactionJobSucceeded {
		t.Fatalf("status = %v, want %q", statusBody["status"], service.WikiCompactionJobSucceeded)
	}

	after := h.DoREST(t, "GET", "/api/v3/repos/"+full+"/wiki/pages/home/history", nil)
	assertStatusCode(t, after, http.StatusOK)
	rowsAfter := testharness.DecodeJSONArray(t, after)
	if len(rowsAfter) != 1 {
		t.Fatalf("rowsAfter len = %d, want 1", len(rowsAfter))
	}
}
