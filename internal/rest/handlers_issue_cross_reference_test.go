package rest_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

func TestGetIssueTimeline_CrossReferencedIssueBodyLifecycle(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "xref-body")

	w := h.DoRESTJSON(t, http.MethodPost, "/api/v3/repos/testuser/xref-body/issues", map[string]any{
		"title": "target",
	})
	assertStatusCode(t, w, http.StatusCreated)
	w = h.DoRESTJSON(t, http.MethodPost, "/api/v3/repos/testuser/xref-body/issues", map[string]any{
		"title": "source",
		"body":  "Related to #1",
	})
	assertStatusCode(t, w, http.StatusCreated)

	events := getTimelineEvents(t, h, "/api/v3/repos/testuser/xref-body/issues/1/timeline")
	if !hasCrossReferenceFromIssue(t, events, 2, "testuser/xref-body") {
		t.Fatalf("expected cross-referenced event from issue #2, got %#v", events)
	}

	w = h.DoRESTJSON(t, http.MethodPatch, "/api/v3/repos/testuser/xref-body/issues/2", map[string]any{
		"body": "reference removed",
	})
	assertStatusCode(t, w, http.StatusOK)

	events = getTimelineEvents(t, h, "/api/v3/repos/testuser/xref-body/issues/1/timeline")
	if hasCrossReferenceFromIssue(t, events, 2, "testuser/xref-body") {
		t.Fatalf("expected cross-reference from issue #2 to be removed, got %#v", events)
	}
}

func TestGetIssueTimeline_CrossReferencedIssueCommentDedupes(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "xref-comment")

	w := h.DoRESTJSON(t, http.MethodPost, "/api/v3/repos/testuser/xref-comment/issues", map[string]any{
		"title": "target",
	})
	assertStatusCode(t, w, http.StatusCreated)
	w = h.DoRESTJSON(t, http.MethodPost, "/api/v3/repos/testuser/xref-comment/issues", map[string]any{
		"title": "source",
	})
	assertStatusCode(t, w, http.StatusCreated)
	w = h.DoRESTJSON(t, http.MethodPost, "/api/v3/repos/testuser/xref-comment/issues/2/comments", map[string]any{
		"body": "see #1 and #1",
	})
	assertStatusCode(t, w, http.StatusCreated)

	events := getTimelineEvents(t, h, "/api/v3/repos/testuser/xref-comment/issues/1/timeline")
	count := countCrossReferencesFromIssue(t, events, 2, "testuser/xref-comment")
	if count != 1 {
		t.Fatalf("expected one deduped cross-referenced event from issue #2, got %d: %#v", count, events)
	}
	event := firstCrossReferenceFromIssue(t, events, 2, "testuser/xref-comment")
	source := event["source"].(map[string]any)
	if _, ok := source["comment"].(map[string]any); !ok {
		t.Fatalf("expected source comment to be present for comment reference, got %#v", source)
	}
}

func TestGetIssueTimeline_CrossReferencedWikiPageLifecycle(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "xref-wiki")
	full := "testuser/xref-wiki"

	w := h.DoRESTJSON(t, http.MethodPost, "/api/v3/repos/"+full+"/issues", map[string]any{
		"title": "target",
	})
	assertStatusCode(t, w, http.StatusCreated)

	w = h.DoRESTJSON(t, http.MethodPut, wikiPagePath(full, "home"), map[string]any{
		"body": "# Home\n\nRelated to #1 and #1.\n",
	})
	assertStatusCode(t, w, http.StatusOK)

	events := getTimelineEvents(t, h, "/api/v3/repos/"+full+"/issues/1/timeline")
	if !hasCrossReferenceFromWikiPage(t, events, "home", full) {
		t.Fatalf("expected cross-referenced event from wiki page home, got %#v", events)
	}

	w = h.DoRESTJSON(t, http.MethodPut, wikiPagePath(full, "home"), map[string]any{
		"body": "# Home\n\nReference removed.\n",
	})
	assertStatusCode(t, w, http.StatusOK)

	events = getTimelineEvents(t, h, "/api/v3/repos/"+full+"/issues/1/timeline")
	if hasCrossReferenceFromWikiPage(t, events, "home", full) {
		t.Fatalf("expected wiki cross-reference to be removed after update, got %#v", events)
	}

	w = h.DoRESTJSON(t, http.MethodPut, wikiPagePath(full, "home"), map[string]any{
		"body": "# Home\n\nRelated again to #1.\n",
	})
	assertStatusCode(t, w, http.StatusOK)
	events = getTimelineEvents(t, h, "/api/v3/repos/"+full+"/issues/1/timeline")
	if !hasCrossReferenceFromWikiPage(t, events, "home", full) {
		t.Fatalf("expected wiki cross-reference to return after update, got %#v", events)
	}

	w = h.DoREST(t, http.MethodDelete, wikiPagePath(full, "home"), nil)
	assertStatusCode(t, w, http.StatusNoContent)
	events = getTimelineEvents(t, h, "/api/v3/repos/"+full+"/issues/1/timeline")
	if hasCrossReferenceFromWikiPage(t, events, "home", full) {
		t.Fatalf("expected wiki cross-reference to be removed after delete, got %#v", events)
	}
}

func TestGetIssueTimeline_CrossRepoReferencesRespectSourcePermissions(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "xref-target")

	w := h.DoRESTJSON(t, http.MethodPost, "/api/v3/repos/testuser/xref-target/issues", map[string]any{
		"title": "target",
	})
	assertStatusCode(t, w, http.StatusCreated)

	sourceOwner, sourceOwnerToken := seedHarnessUser(t, h, "xref-source-owner", false)
	sourceCtx := service.ContextWithUser(ctx, sourceOwner)
	sourceRepo, err := h.Svc.CreateRepo(sourceCtx, service.CreateRepoInput{
		OwnerLogin: sourceOwner.Login,
		Name:       "private-xref-source",
		Private:    true,
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("create private source repo: %v", err)
	}
	if _, err := h.Svc.CreateIssue(sourceCtx, service.CreateIssueInput{
		RepoFullName: sourceRepo.FullName,
		Title:        "private source",
		Body:         "references testuser/xref-target#1",
		AuthorLogin:  sourceOwner.Login,
	}); err != nil {
		t.Fatalf("create source issue: %v", err)
	}

	w = h.DoRESTWithToken(t, http.MethodGet, "/api/v3/repos/testuser/xref-target/issues/1/timeline", sourceOwnerToken)
	assertStatusCode(t, w, http.StatusOK)
	events := testharness.DecodeJSONArray(t, w)
	if !hasCrossReferenceFromIssue(t, events, 1, sourceRepo.FullName) {
		t.Fatalf("expected source owner to see private cross-reference, got %#v", events)
	}

	_, outsiderToken := seedHarnessUser(t, h, "xref-outsider", false)
	w = h.DoRESTWithToken(t, http.MethodGet, "/api/v3/repos/testuser/xref-target/issues/1/timeline", outsiderToken)
	assertStatusCode(t, w, http.StatusOK)
	events = testharness.DecodeJSONArray(t, w)
	if hasCrossReferenceFromIssue(t, events, 1, sourceRepo.FullName) {
		t.Fatalf("outsider must not see private source cross-reference, got %#v", events)
	}
}

func TestGetIssueTimeline_CrossRepoWikiReferencesRespectSourcePermissions(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "xref-wiki-target")

	w := h.DoRESTJSON(t, http.MethodPost, "/api/v3/repos/testuser/xref-wiki-target/issues", map[string]any{
		"title": "target",
	})
	assertStatusCode(t, w, http.StatusCreated)

	sourceOwner, sourceOwnerToken := seedHarnessUser(t, h, "xref-wiki-source-owner", false)
	sourceCtx := service.ContextWithUser(ctx, sourceOwner)
	sourceRepo, err := h.Svc.CreateRepo(sourceCtx, service.CreateRepoInput{
		OwnerLogin: sourceOwner.Login,
		Name:       "private-wiki-xref-source",
		Private:    true,
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("create private source repo: %v", err)
	}
	if _, err := h.Svc.PutWikiPage(sourceCtx, sourceRepo.FullName, "home", "# Home\n\nreferences testuser/xref-wiki-target#1\n", "create wiki source", ""); err != nil {
		t.Fatalf("create source wiki page: %v", err)
	}

	w = h.DoRESTWithToken(t, http.MethodGet, "/api/v3/repos/testuser/xref-wiki-target/issues/1/timeline", sourceOwnerToken)
	assertStatusCode(t, w, http.StatusOK)
	events := testharness.DecodeJSONArray(t, w)
	if !hasCrossReferenceFromWikiPage(t, events, "home", sourceRepo.FullName) {
		t.Fatalf("expected source owner to see private wiki cross-reference, got %#v", events)
	}

	_, outsiderToken := seedHarnessUser(t, h, "xref-wiki-outsider", false)
	w = h.DoRESTWithToken(t, http.MethodGet, "/api/v3/repos/testuser/xref-wiki-target/issues/1/timeline", outsiderToken)
	assertStatusCode(t, w, http.StatusOK)
	events = testharness.DecodeJSONArray(t, w)
	if hasCrossReferenceFromWikiPage(t, events, "home", sourceRepo.FullName) {
		t.Fatalf("outsider must not see private wiki cross-reference, got %#v", events)
	}
}

func getTimelineEvents(t *testing.T, h *testharness.Harness, path string) []map[string]any {
	t.Helper()
	w := h.DoREST(t, http.MethodGet, path, nil)
	assertStatusCode(t, w, http.StatusOK)
	return testharness.DecodeJSONArray(t, w)
}

func crossReferenceEvents(events []map[string]any) []map[string]any {
	out := make([]map[string]any, 0)
	for _, event := range events {
		if event["event"] == "cross-referenced" {
			out = append(out, event)
		}
	}
	return out
}

func hasCrossReferenceFromIssue(t *testing.T, events []map[string]any, number int, repoFullName string) bool {
	t.Helper()
	return countCrossReferencesFromIssue(t, events, number, repoFullName) > 0
}

func countCrossReferencesFromIssue(t *testing.T, events []map[string]any, number int, repoFullName string) int {
	t.Helper()
	count := 0
	for _, event := range crossReferenceEvents(events) {
		if crossReferenceSourceIssueMatches(t, event, number, repoFullName) {
			count++
		}
	}
	return count
}

func firstCrossReferenceFromIssue(t *testing.T, events []map[string]any, number int, repoFullName string) map[string]any {
	t.Helper()
	for _, event := range crossReferenceEvents(events) {
		if crossReferenceSourceIssueMatches(t, event, number, repoFullName) {
			return event
		}
	}
	t.Fatalf("cross-reference from %s#%d not found in %#v", repoFullName, number, events)
	return nil
}

func crossReferenceSourceIssueMatches(t *testing.T, event map[string]any, number int, repoFullName string) bool {
	t.Helper()
	source, ok := event["source"].(map[string]any)
	if !ok {
		return false
	}
	issue, ok := source["issue"].(map[string]any)
	if !ok {
		return false
	}
	gotNumber, ok := issue["number"].(float64)
	if !ok || int(gotNumber) != number {
		return false
	}
	repoURL, _ := issue["repository_url"].(string)
	return strings.HasSuffix(repoURL, "/api/v3/repos/"+repoFullName)
}

func hasCrossReferenceFromWikiPage(t *testing.T, events []map[string]any, slug, repoFullName string) bool {
	t.Helper()
	for _, event := range crossReferenceEvents(events) {
		source, ok := event["source"].(map[string]any)
		if !ok || source["type"] != "wiki_page" {
			continue
		}
		page, ok := source["wiki_page"].(map[string]any)
		if !ok {
			continue
		}
		gotSlug, _ := page["slug"].(string)
		if gotSlug != slug {
			continue
		}
		pageURL, _ := page["url"].(string)
		if strings.Contains(pageURL, "/api/v3/repos/"+repoFullName+"/wiki/pages/") {
			return true
		}
	}
	return false
}
