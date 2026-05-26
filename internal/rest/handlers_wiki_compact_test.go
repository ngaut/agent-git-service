package rest_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

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
	assertStatusCode(t, compact, http.StatusOK)
	body := testharness.DecodeJSON(t, compact)
	if body["pages"] != float64(1) {
		t.Fatalf("pages = %v, want 1", body["pages"])
	}
	if body["commits_removed"] != float64(1) {
		t.Fatalf("commits_removed = %v, want 1", body["commits_removed"])
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
