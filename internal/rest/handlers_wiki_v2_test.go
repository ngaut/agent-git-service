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

func TestWikiV2_ReadEndpointsExposeProvisionalURLs(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	owner, ownerToken := seedHarnessUser(t, h, "wiki-v2-read-owner", false)
	if _, err := h.Svc.CreateRepo(service.ContextWithUser(ctx, owner), service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-v2-read",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	full := owner.Login + "/wiki-v2-read"

	home := h.DoRESTJSONWithToken(t, http.MethodPut, "/api/v3/repos/"+full+"/wiki/pages/home", ownerToken, map[string]any{
		"body":    "# Home\n\nSee [[guides/setup]].\n",
		"message": "create home",
	})
	assertStatusCode(t, home, http.StatusOK)
	guide := h.DoRESTJSONWithToken(t, http.MethodPut, "/api/v3/repos/"+full+"/wiki/pages/"+url.PathEscape("guides/setup"), ownerToken, map[string]any{
		"body":    "# Setup\n\nBack to [[home]].\n",
		"message": "create setup guide",
	})
	assertStatusCode(t, guide, http.StatusOK)

	reconcile := h.DoRESTWithToken(t, http.MethodPost, "/api/v3/repos/"+full+"/wiki-v2/reconcile", ownerToken)
	assertStatusCode(t, reconcile, http.StatusOK)

	list := h.DoRESTWithToken(t, http.MethodGet, "/api/v3/repos/"+full+"/wiki-v2/pages", ownerToken)
	assertStatusCode(t, list, http.StatusOK)
	listBody := testharness.DecodeJSONArray(t, list)
	if len(listBody) != 2 {
		t.Fatalf("wiki-v2 list: got %#v", listBody)
	}
	first := listBody[0]
	if got, _ := first["url"].(string); !strings.Contains(got, "/wiki-v2/pages/") {
		t.Fatalf("wiki-v2 list url = %q, want wiki-v2 path", got)
	}

	getPage := h.DoRESTWithToken(t, http.MethodGet, "/api/v3/repos/"+full+"/wiki-v2/pages/home", ownerToken)
	assertStatusCode(t, getPage, http.StatusOK)
	pageBody := testharness.DecodeJSON(t, getPage)
	if got, _ := pageBody["url"].(string); !strings.Contains(got, "/wiki-v2/pages/home") {
		t.Fatalf("wiki-v2 page url = %q, want wiki-v2 path", got)
	}

	history := h.DoRESTWithToken(t, http.MethodGet, "/api/v3/repos/"+full+"/wiki-v2/pages/home/history", ownerToken)
	assertStatusCode(t, history, http.StatusOK)
	historyBody := testharness.DecodeJSONArray(t, history)
	if len(historyBody) == 0 {
		t.Fatalf("wiki-v2 history: got %#v", historyBody)
	}

	backlinks := h.DoRESTWithToken(t, http.MethodGet, "/api/v3/repos/"+full+"/wiki-v2/pages/home/backlinks", ownerToken)
	assertStatusCode(t, backlinks, http.StatusOK)
	backlinkBody := testharness.DecodeJSONArray(t, backlinks)
	if len(backlinkBody) != 1 {
		t.Fatalf("wiki-v2 backlinks: got %#v", backlinkBody)
	}
	backlink := backlinkBody[0]
	if got, _ := backlink["url"].(string); !strings.Contains(got, "/wiki-v2/pages/") {
		t.Fatalf("wiki-v2 backlink url = %q, want wiki-v2 path", got)
	}

	search := h.DoRESTWithToken(t, http.MethodGet, "/api/v3/repos/"+full+"/wiki-v2/search?q=setup", ownerToken)
	assertStatusCode(t, search, http.StatusOK)
	searchBody := testharness.DecodeJSON(t, search)
	results, ok := searchBody["results"].([]any)
	if !ok || len(results) == 0 {
		t.Fatalf("wiki-v2 search results: got %#v", searchBody)
	}
	result, ok := results[0].(map[string]any)
	if !ok {
		t.Fatalf("wiki-v2 search result: expected map, got %T", results[0])
	}
	if got, _ := result["url"].(string); !strings.Contains(got, "/wiki-v2/pages/") {
		t.Fatalf("wiki-v2 search url = %q, want wiki-v2 path", got)
	}

	if _, err := h.Svc.CreateLabel(service.ContextWithUser(ctx, owner), full, "runbook", "0e8a16", ""); err != nil {
		t.Fatalf("create label: %v", err)
	}
	listLabels := h.DoRESTWithToken(t, http.MethodGet, "/api/v3/repos/"+full+"/wiki-v2/pages/home/labels", ownerToken)
	assertStatusCode(t, listLabels, http.StatusOK)
	listLabelsBody := testharness.DecodeJSONArray(t, listLabels)
	if len(listLabelsBody) != 0 {
		t.Fatalf("wiki-v2 list labels: got %#v", listLabelsBody)
	}

	addLabels := h.DoRESTJSONWithToken(t, http.MethodPost, "/api/v3/repos/"+full+"/wiki-v2/pages/home/labels", ownerToken, map[string]any{
		"labels": []string{"runbook"},
	})
	assertStatusCode(t, addLabels, http.StatusMethodNotAllowed)

	setLabels := h.DoRESTJSONWithToken(t, http.MethodPut, "/api/v3/repos/"+full+"/wiki-v2/pages/home/labels", ownerToken, map[string]any{
		"labels": []string{"runbook"},
	})
	assertStatusCode(t, setLabels, http.StatusMethodNotAllowed)

	removeLabel := h.DoRESTWithToken(t, http.MethodDelete, "/api/v3/repos/"+full+"/wiki-v2/pages/home/labels/runbook", ownerToken)
	assertStatusCode(t, removeLabel, http.StatusNotFound)

	removeAllLabels := h.DoRESTWithToken(t, http.MethodDelete, "/api/v3/repos/"+full+"/wiki-v2/pages/home/labels", ownerToken)
	assertStatusCode(t, removeAllLabels, http.StatusMethodNotAllowed)

	legacyAddLabels := h.DoRESTJSONWithToken(t, http.MethodPost, "/api/v3/repos/"+full+"/wiki/pages/home/labels", ownerToken, map[string]any{
		"labels": []string{"runbook"},
	})
	assertStatusCode(t, legacyAddLabels, http.StatusOK)

	listLabels = h.DoRESTWithToken(t, http.MethodGet, "/api/v3/repos/"+full+"/wiki-v2/pages/home/labels", ownerToken)
	assertStatusCode(t, listLabels, http.StatusOK)
	listLabelsBody = testharness.DecodeJSONArray(t, listLabels)
	if len(listLabelsBody) != 1 {
		t.Fatalf("wiki-v2 list labels after legacy add: got %#v", listLabelsBody)
	}
	if got, _ := listLabelsBody[0]["name"].(string); got != "runbook" {
		t.Fatalf("wiki-v2 label name = %q, want runbook", got)
	}

	tree := h.DoRESTWithToken(t, http.MethodGet, "/api/v3/repos/"+full+"/wiki-v2/tree", ownerToken)
	assertStatusCode(t, tree, http.StatusOK)
	treeBody := testharness.DecodeJSONArray(t, tree)
	if len(treeBody) != 2 {
		t.Fatalf("wiki-v2 tree root: got %#v", treeBody)
	}
	if got, _ := treeBody[0]["kind"].(string); got != "directory" {
		t.Fatalf("wiki-v2 tree first kind = %q, want directory", got)
	}
	if got, _ := treeBody[0]["url"].(string); !strings.Contains(got, "/wiki-v2/tree?path=guides") {
		t.Fatalf("wiki-v2 tree directory url = %q, want wiki-v2 tree path", got)
	}
	if got, _ := treeBody[1]["url"].(string); !strings.Contains(got, "/wiki-v2/pages/home") {
		t.Fatalf("wiki-v2 tree page url = %q, want wiki-v2 page path", got)
	}

	subtree := h.DoRESTWithToken(t, http.MethodGet, "/api/v3/repos/"+full+"/wiki-v2/tree?path=guides", ownerToken)
	assertStatusCode(t, subtree, http.StatusOK)
	subtreeBody := testharness.DecodeJSONArray(t, subtree)
	if len(subtreeBody) != 1 {
		t.Fatalf("wiki-v2 tree guides: got %#v", subtreeBody)
	}
	if got, _ := subtreeBody[0]["slug"].(string); got != "guides/setup" {
		t.Fatalf("wiki-v2 subtree slug = %q, want guides/setup", got)
	}
}
