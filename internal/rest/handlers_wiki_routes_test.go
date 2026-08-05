package rest_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

func TestWiki_ReconcileAndStateEndpoints(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	owner, ownerToken := seedHarnessUser(t, h, "wiki-state-owner", false)
	if _, err := h.Svc.CreateRepo(service.ContextWithUser(ctx, owner), service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-state-routes",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := owner.Login + "/wiki-state-routes"

	initialState := h.DoRESTWithToken(t, http.MethodGet, "/api/ext/v1/repos/"+full+"/wiki/state", ownerToken)
	assertStatusCode(t, initialState, http.StatusOK)
	initialBody := testharness.DecodeJSON(t, initialState)
	if initialBody["page_count"] != float64(0) {
		t.Fatalf("initial page_count = %v, want 0", initialBody["page_count"])
	}
	if initialBody["indexed_commit_sha"] != "" {
		t.Fatalf("initial indexed_commit_sha = %v, want empty", initialBody["indexed_commit_sha"])
	}

	createHome := h.DoRESTJSONWithToken(t, http.MethodPut, "/api/ext/v1/repos/"+full+"/wiki/pages/home", ownerToken, map[string]any{
		"body":    "# Home\n\nLanding page.\n",
		"message": "create home",
	})
	assertStatusCode(t, createHome, http.StatusOK)
	createGuide := h.DoRESTJSONWithToken(t, http.MethodPut, "/api/ext/v1/repos/"+full+"/wiki/pages/"+url.PathEscape("guides/setup"), ownerToken, map[string]any{
		"body":    "# Setup\n\nInstall steps.\n",
		"message": "create setup guide",
	})
	assertStatusCode(t, createGuide, http.StatusOK)

	requested := h.DoRESTWithToken(t, http.MethodPost, "/api/ext/v1/repos/"+full+"/wiki/reconcile/request", ownerToken)
	assertStatusCode(t, requested, http.StatusAccepted)
	requestedBody := testharness.DecodeJSON(t, requested)
	if requestedBody["requested_at"] == nil {
		t.Fatalf("requested_at missing: %v", requestedBody)
	}

	reconcile := h.DoRESTWithToken(t, http.MethodPost, "/api/ext/v1/repos/"+full+"/wiki/reconcile", ownerToken)
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

	state := h.DoRESTWithToken(t, http.MethodGet, "/api/ext/v1/repos/"+full+"/wiki/state", ownerToken)
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

func TestWiki_ReconcileEndpointsRequireWritePermission(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	owner, ownerToken := seedHarnessUser(t, h, "wiki-perm-owner", false)
	_, strangerToken := seedHarnessUser(t, h, "wiki-perm-stranger", false)
	if _, err := h.Svc.CreateRepo(service.ContextWithUser(ctx, owner), service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-perm",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := owner.Login + "/wiki-perm"

	readable := h.DoRESTWithToken(t, http.MethodGet, "/api/ext/v1/repos/"+full+"/wiki/state", ownerToken)
	assertStatusCode(t, readable, http.StatusOK)

	for _, path := range []string{
		"/api/ext/v1/repos/" + full + "/wiki/reconcile/request",
		"/api/ext/v1/repos/" + full + "/wiki/reconcile",
	} {
		blocked := h.DoRESTWithToken(t, http.MethodPost, path, strangerToken)
		if blocked.Code != http.StatusForbidden && blocked.Code != http.StatusNotFound {
			t.Fatalf("%s expected 403/404, got %d: %s", path, blocked.Code, blocked.Body.String())
		}
	}
}

func TestWiki_ReadRoutesExposeAuthoritativeURLs(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	owner, ownerToken := seedHarnessUser(t, h, "wiki-read-owner", false)
	if _, err := h.Svc.CreateRepo(service.ContextWithUser(ctx, owner), service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-read",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := owner.Login + "/wiki-read"

	home := h.DoRESTJSONWithToken(t, http.MethodPut, "/api/ext/v1/repos/"+full+"/wiki/pages/home", ownerToken, map[string]any{
		"body":    "# Home\n\nSee [[guides/setup]].\n",
		"message": "create home",
	})
	assertStatusCode(t, home, http.StatusOK)
	guide := h.DoRESTJSONWithToken(t, http.MethodPut, "/api/ext/v1/repos/"+full+"/wiki/pages/"+url.PathEscape("guides/setup"), ownerToken, map[string]any{
		"body":    "# Setup\n\nBack to [[home]].\n",
		"message": "create setup guide",
	})
	assertStatusCode(t, guide, http.StatusOK)

	reconcile := h.DoRESTWithToken(t, http.MethodPost, "/api/ext/v1/repos/"+full+"/wiki/reconcile", ownerToken)
	assertStatusCode(t, reconcile, http.StatusOK)

	list := h.DoRESTWithToken(t, http.MethodGet, "/api/ext/v1/repos/"+full+"/wiki/pages", ownerToken)
	assertStatusCode(t, list, http.StatusOK)
	listBody := testharness.DecodeJSONArray(t, list)
	if len(listBody) != 2 {
		t.Fatalf("wiki list: got %#v", listBody)
	}
	first := listBody[0]
	if got, _ := first["url"].(string); !strings.Contains(got, "/wiki/pages/") {
		t.Fatalf("wiki list url = %q, want wiki path", got)
	}

	getPage := h.DoRESTWithToken(t, http.MethodGet, "/api/ext/v1/repos/"+full+"/wiki/pages/home", ownerToken)
	assertStatusCode(t, getPage, http.StatusOK)
	pageBody := testharness.DecodeJSON(t, getPage)
	if got, _ := pageBody["url"].(string); !strings.Contains(got, "/wiki/pages/home") {
		t.Fatalf("wiki page url = %q, want wiki path", got)
	}

	history := h.DoRESTWithToken(t, http.MethodGet, "/api/ext/v1/repos/"+full+"/wiki/pages/home/history", ownerToken)
	assertStatusCode(t, history, http.StatusOK)
	historyBody := testharness.DecodeJSONArray(t, history)
	if len(historyBody) == 0 {
		t.Fatalf("wiki history: got %#v", historyBody)
	}

	backlinks := h.DoRESTWithToken(t, http.MethodGet, "/api/ext/v1/repos/"+full+"/wiki/pages/home/backlinks", ownerToken)
	assertStatusCode(t, backlinks, http.StatusOK)
	backlinkBody := testharness.DecodeJSONArray(t, backlinks)
	if len(backlinkBody) != 1 {
		t.Fatalf("wiki backlinks: got %#v", backlinkBody)
	}
	backlink := backlinkBody[0]
	if got, _ := backlink["url"].(string); !strings.Contains(got, "/wiki/pages/") {
		t.Fatalf("wiki backlink url = %q, want wiki path", got)
	}

	search := h.DoRESTWithToken(t, http.MethodGet, "/api/ext/v1/repos/"+full+"/wiki/search?q=setup", ownerToken)
	assertStatusCode(t, search, http.StatusOK)
	searchBody := testharness.DecodeJSON(t, search)
	results, ok := searchBody["results"].([]any)
	if !ok || len(results) == 0 {
		t.Fatalf("wiki search results: got %#v", searchBody)
	}
	result, ok := results[0].(map[string]any)
	if !ok {
		t.Fatalf("wiki search result: expected map, got %T", results[0])
	}
	if got, _ := result["url"].(string); !strings.Contains(got, "/wiki/pages/") {
		t.Fatalf("wiki search url = %q, want wiki path", got)
	}

	if _, err := h.Svc.CreateLabel(service.ContextWithUser(ctx, owner), full, "runbook", "0e8a16", ""); err != nil {
		t.Fatalf("create label: %v", err)
	}
	listLabels := h.DoRESTWithToken(t, http.MethodGet, "/api/ext/v1/repos/"+full+"/wiki/pages/home/labels", ownerToken)
	assertStatusCode(t, listLabels, http.StatusOK)
	listLabelsBody := testharness.DecodeJSONArray(t, listLabels)
	if len(listLabelsBody) != 0 {
		t.Fatalf("wiki list labels: got %#v", listLabelsBody)
	}

	addLabels := h.DoRESTJSONWithToken(t, http.MethodPost, "/api/ext/v1/repos/"+full+"/wiki/pages/home/labels", ownerToken, map[string]any{
		"labels": []string{"runbook"},
	})
	assertStatusCode(t, addLabels, http.StatusOK)

	setLabels := h.DoRESTJSONWithToken(t, http.MethodPut, "/api/ext/v1/repos/"+full+"/wiki/pages/home/labels", ownerToken, map[string]any{
		"labels": []string{"runbook"},
	})
	assertStatusCode(t, setLabels, http.StatusOK)

	removeLabel := h.DoRESTWithToken(t, http.MethodDelete, "/api/ext/v1/repos/"+full+"/wiki/pages/home/labels/runbook", ownerToken)
	assertStatusCode(t, removeLabel, http.StatusOK)

	reAddLabels := h.DoRESTJSONWithToken(t, http.MethodPost, "/api/ext/v1/repos/"+full+"/wiki/pages/home/labels", ownerToken, map[string]any{
		"labels": []string{"runbook"},
	})
	assertStatusCode(t, reAddLabels, http.StatusOK)

	removeAllLabels := h.DoRESTWithToken(t, http.MethodDelete, "/api/ext/v1/repos/"+full+"/wiki/pages/home/labels", ownerToken)
	assertStatusCode(t, removeAllLabels, http.StatusNoContent)

	listLabels = h.DoRESTWithToken(t, http.MethodGet, "/api/ext/v1/repos/"+full+"/wiki/pages/home/labels", ownerToken)
	assertStatusCode(t, listLabels, http.StatusOK)
	listLabelsBody = testharness.DecodeJSONArray(t, listLabels)
	if len(listLabelsBody) != 0 {
		t.Fatalf("wiki list labels after clear: got %#v", listLabelsBody)
	}

	tree := h.DoRESTWithToken(t, http.MethodGet, "/api/ext/v1/repos/"+full+"/wiki/tree", ownerToken)
	assertStatusCode(t, tree, http.StatusOK)
	treeBody := testharness.DecodeJSONArray(t, tree)
	if len(treeBody) != 2 {
		t.Fatalf("wiki tree root: got %#v", treeBody)
	}
	if got, _ := treeBody[0]["kind"].(string); got != "directory" {
		t.Fatalf("wiki tree first kind = %q, want directory", got)
	}
	if got, _ := treeBody[0]["url"].(string); !strings.Contains(got, "/wiki/tree?path=guides") {
		t.Fatalf("wiki tree directory url = %q, want wiki tree path", got)
	}
	if got, _ := treeBody[1]["url"].(string); !strings.Contains(got, "/wiki/pages/home") {
		t.Fatalf("wiki tree page url = %q, want wiki page path", got)
	}

	subtree := h.DoRESTWithToken(t, http.MethodGet, "/api/ext/v1/repos/"+full+"/wiki/tree?path=guides", ownerToken)
	assertStatusCode(t, subtree, http.StatusOK)
	subtreeBody := testharness.DecodeJSONArray(t, subtree)
	if len(subtreeBody) != 1 {
		t.Fatalf("wiki tree guides: got %#v", subtreeBody)
	}
	if got, _ := subtreeBody[0]["slug"].(string); got != "guides/setup" {
		t.Fatalf("wiki subtree slug = %q, want guides/setup", got)
	}
}
