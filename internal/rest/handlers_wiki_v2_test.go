package rest_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

func TestWikiV2_ReconcileAndStateEndpoints(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	owner, ownerToken := seedHarnessUser(t, h, "wiki-v2-owner", false)
	if _, err := h.Svc.CreateRepo(service.ContextWithUser(ctx, owner), service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-v2-routes",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := owner.Login + "/wiki-v2-routes"

	initialState := h.DoRESTWithToken(t, http.MethodGet, "/api/v3/repos/"+full+"/wiki-v2/state", ownerToken)
	assertStatusCode(t, initialState, http.StatusOK)
	initialBody := testharness.DecodeJSON(t, initialState)
	if initialBody["page_count"] != float64(0) {
		t.Fatalf("initial page_count = %v, want 0", initialBody["page_count"])
	}
	if initialBody["indexed_commit_sha"] != "" {
		t.Fatalf("initial indexed_commit_sha = %v, want empty", initialBody["indexed_commit_sha"])
	}

	createHome := h.DoRESTJSONWithToken(t, http.MethodPut, "/api/v3/repos/"+full+"/wiki/pages/home", ownerToken, map[string]any{
		"body":    "# Home\n\nLanding page.\n",
		"message": "create home",
	})
	assertStatusCode(t, createHome, http.StatusOK)
	createGuide := h.DoRESTJSONWithToken(t, http.MethodPut, "/api/v3/repos/"+full+"/wiki/pages/"+url.PathEscape("guides/setup"), ownerToken, map[string]any{
		"body":    "# Setup\n\nInstall steps.\n",
		"message": "create setup guide",
	})
	assertStatusCode(t, createGuide, http.StatusOK)

	requested := h.DoRESTWithToken(t, http.MethodPost, "/api/v3/repos/"+full+"/wiki-v2/reconcile/request", ownerToken)
	assertStatusCode(t, requested, http.StatusAccepted)
	requestedBody := testharness.DecodeJSON(t, requested)
	if requestedBody["requested_at"] == nil {
		t.Fatalf("requested_at missing: %v", requestedBody)
	}

	reconcile := h.DoRESTWithToken(t, http.MethodPost, "/api/v3/repos/"+full+"/wiki-v2/reconcile", ownerToken)
	assertStatusCode(t, reconcile, http.StatusOK)
	reconcileBody := testharness.DecodeJSON(t, reconcile)
	if reconcileBody["page_count"] != float64(2) {
		t.Fatalf("reconcile page_count = %v, want 2", reconcileBody["page_count"])
	}
	if reconcileBody["reconciled"] != true {
		t.Fatalf("reconciled = %v, want true", reconcileBody["reconciled"])
	}
	indexedSHA, _ := reconcileBody["indexed_commit_sha"].(string)
	if indexedSHA == "" {
		t.Fatalf("indexed_commit_sha = %q, want non-empty", indexedSHA)
	}

	state := h.DoRESTWithToken(t, http.MethodGet, "/api/v3/repos/"+full+"/wiki-v2/state", ownerToken)
	assertStatusCode(t, state, http.StatusOK)
	stateBody := testharness.DecodeJSON(t, state)
	if stateBody["page_count"] != float64(2) {
		t.Fatalf("state page_count = %v, want 2", stateBody["page_count"])
	}
	if stateBody["indexed_commit_sha"] != indexedSHA {
		t.Fatalf("state indexed_commit_sha = %v, want %q", stateBody["indexed_commit_sha"], indexedSHA)
	}
	if stateBody["indexed_at"] == nil {
		t.Fatalf("indexed_at missing: %v", stateBody)
	}
	if stateBody["reconcile_requested_at"] != nil {
		t.Fatalf("reconcile_requested_at = %v, want nil after sync reconcile", stateBody["reconcile_requested_at"])
	}
}

func TestWikiV2_ReconcileEndpointsRequireWritePermission(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	owner, ownerToken := seedHarnessUser(t, h, "wiki-v2-perm-owner", false)
	_, strangerToken := seedHarnessUser(t, h, "wiki-v2-perm-stranger", false)
	if _, err := h.Svc.CreateRepo(service.ContextWithUser(ctx, owner), service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-v2-perm",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := owner.Login + "/wiki-v2-perm"

	readable := h.DoRESTWithToken(t, http.MethodGet, "/api/v3/repos/"+full+"/wiki-v2/state", ownerToken)
	assertStatusCode(t, readable, http.StatusOK)

	for _, path := range []string{
		"/api/v3/repos/" + full + "/wiki-v2/reconcile/request",
		"/api/v3/repos/" + full + "/wiki-v2/reconcile",
	} {
		blocked := h.DoRESTWithToken(t, http.MethodPost, path, strangerToken)
		if blocked.Code != http.StatusForbidden && blocked.Code != http.StatusNotFound {
			t.Fatalf("%s expected 403/404, got %d: %s", path, blocked.Code, blocked.Body.String())
		}
	}
}
