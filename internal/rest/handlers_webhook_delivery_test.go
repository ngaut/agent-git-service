package rest_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/ngaut/agent-git-service/internal/testharness"
)

func findDeliveryByEventAction(deliveries []map[string]any, event, action string) map[string]any {
	for _, delivery := range deliveries {
		if delivery["event"] == event && delivery["action"] == action {
			return delivery
		}
	}
	return nil
}

func jsonIDString(t *testing.T, value any, field string) string {
	t.Helper()
	id, ok := value.(float64)
	if !ok {
		t.Fatalf("expected numeric %s, got %T (%v)", field, value, value)
	}
	return strconv.Itoa(int(id))
}

func TestWebhookHandlers_DeliveriesAndRedelivery(t *testing.T) {
	h := testharness.New(t)
	repo := "webhook-deliveries"
	compatSeedRepo(t, h, repo)
	full := "testuser/" + repo

	var hits int
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer receiver.Close()

	createResp := h.DoRESTJSON(t, http.MethodPost, fmt.Sprintf("/api/v3/repos/%s/hooks", full), map[string]any{
		"config": map[string]any{
			"url":          receiver.URL,
			"content_type": "json",
		},
		"events": []string{"push"},
	})
	assertStatusCode(t, createResp, http.StatusCreated)
	created := testharness.DecodeJSON(t, createResp)
	if created["id"] == nil {
		t.Fatalf("expected created webhook id, got %+v", created)
	}
	hookID := jsonIDString(t, created["id"], "hook id")

	listResp := h.DoREST(t, http.MethodGet, fmt.Sprintf("/api/v3/repos/%s/hooks/%s/deliveries", full, hookID), nil)
	assertStatusCode(t, listResp, http.StatusOK)
	deliveries := testharness.DecodeJSONArray(t, listResp)
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 delivery after ping, got %d", len(deliveries))
	}
	deliveryID := jsonIDString(t, deliveries[0]["id"], "delivery id")
	if deliveries[0]["event"] != "ping" {
		t.Fatalf("expected ping delivery, got %v", deliveries[0]["event"])
	}
	if deliveries[0]["status"] != "OK" {
		t.Fatalf("expected GitHub-style uppercase status, got %v", deliveries[0]["status"])
	}
	if _, ok := deliveries[0]["duration"].(float64); !ok {
		t.Fatalf("expected floating-point duration, got %T", deliveries[0]["duration"])
	}
	if deliveries[0]["url"] != receiver.URL {
		t.Fatalf("expected delivery url %q, got %v", receiver.URL, deliveries[0]["url"])
	}
	if deliveries[0]["throttled_at"] != nil {
		t.Fatalf("expected throttled_at to be null, got %v", deliveries[0]["throttled_at"])
	}
	request, ok := deliveries[0]["request"].(map[string]any)
	if !ok {
		t.Fatalf("expected request payload map, got %T", deliveries[0]["request"])
	}
	if _, ok := request["payload"].(map[string]any); !ok {
		t.Fatalf("expected request.payload object, got %T", request["payload"])
	}

	detailResp := h.DoREST(t, http.MethodGet, fmt.Sprintf("/api/v3/repos/%s/hooks/%s/deliveries/%s", full, hookID, deliveryID), nil)
	assertStatusCode(t, detailResp, http.StatusOK)
	detail := testharness.DecodeJSON(t, detailResp)
	if detail["guid"] == nil {
		t.Fatalf("expected delivery guid, got %+v", detail)
	}
	if detail["status"] != "OK" {
		t.Fatalf("expected GitHub-style detail status, got %v", detail["status"])
	}

	redeliverResp := h.DoRESTJSON(t, http.MethodPost, fmt.Sprintf("/api/v3/repos/%s/hooks/%s/deliveries/%s/attempts", full, hookID, deliveryID), nil)
	assertStatusCode(t, redeliverResp, http.StatusAccepted)
	redelivery := testharness.DecodeJSON(t, redeliverResp)
	if redelivery["redelivery"] != true {
		t.Fatalf("expected redelivery=true, got %v", redelivery["redelivery"])
	}
	if redelivery["url"] != receiver.URL {
		t.Fatalf("expected redelivery url %q, got %v", receiver.URL, redelivery["url"])
	}

	allResp := h.DoREST(t, http.MethodGet, fmt.Sprintf("/api/v3/repos/%s/hooks/%s/deliveries", full, hookID), nil)
	assertStatusCode(t, allResp, http.StatusOK)
	allDeliveries := testharness.DecodeJSONArray(t, allResp)
	if len(allDeliveries) != 2 {
		t.Fatalf("expected 2 deliveries after redelivery, got %d", len(allDeliveries))
	}
	if hits != 2 {
		t.Fatalf("expected receiver to be hit twice, got %d", hits)
	}
}

func TestWebhookHandlers_ListDeliveriesMissingHookReturnsNotFound(t *testing.T) {
	h := testharness.New(t)
	repo := "webhook-deliveries-missing"
	compatSeedRepo(t, h, repo)
	full := "testuser/" + repo

	resp := h.DoREST(t, http.MethodGet, fmt.Sprintf("/api/v3/repos/%s/hooks/999/deliveries", full), nil)
	assertStatusCode(t, resp, http.StatusNotFound)
}

func TestWebhookHandlers_IssueMilestoneOnlyPatchDispatchesEditedEvent(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	repo := "webhook-issue-edited"
	compatSeedRepo(t, h, repo)
	full := "testuser/" + repo

	createResp := h.DoRESTJSON(t, http.MethodPost, fmt.Sprintf("/api/v3/repos/%s/hooks", full), map[string]any{
		"config": map[string]any{
			"url":          "http://example.invalid/issues",
			"content_type": "json",
		},
		"events": []string{"issues"},
	})
	assertStatusCode(t, createResp, http.StatusCreated)
	created := testharness.DecodeJSON(t, createResp)
	hookID := jsonIDString(t, created["id"], "hook id")

	issueResp := h.DoRESTJSON(t, http.MethodPost, fmt.Sprintf("/api/v3/repos/%s/issues", full), map[string]any{
		"title": "issue webhook target",
	})
	assertStatusCode(t, issueResp, http.StatusCreated)

	ms, err := h.Svc.CreateMilestone(ctx, full, "v1.0", "", "open")
	if err != nil {
		t.Fatalf("create milestone: %v", err)
	}

	patchResp := h.DoRESTJSON(t, http.MethodPatch, fmt.Sprintf("/api/v3/repos/%s/issues/1", full), map[string]any{
		"milestone": ms.Number,
	})
	assertStatusCode(t, patchResp, http.StatusOK)

	listResp := h.DoREST(t, http.MethodGet, fmt.Sprintf("/api/v3/repos/%s/hooks/%s/deliveries", full, hookID), nil)
	assertStatusCode(t, listResp, http.StatusOK)
	deliveries := testharness.DecodeJSONArray(t, listResp)
	if len(deliveries) != 3 {
		t.Fatalf("expected ping + opened + edited deliveries, got %d", len(deliveries))
	}
	if edited := findDeliveryByEventAction(deliveries, "issues", "edited"); edited == nil {
		t.Fatalf("expected issues.edited delivery, got %+v", deliveries)
	}
}

func TestWebhookHandlers_PRMilestoneOnlyPatchDispatchesEditedEvent(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	repo := "webhook-pr-edited"
	compatSeedRepo(t, h, repo)
	full := "testuser/" + repo

	createResp := h.DoRESTJSON(t, http.MethodPost, fmt.Sprintf("/api/v3/repos/%s/hooks", full), map[string]any{
		"config": map[string]any{
			"url":          "http://example.invalid/pulls",
			"content_type": "json",
		},
		"events": []string{"pull_request"},
	})
	assertStatusCode(t, createResp, http.StatusCreated)
	created := testharness.DecodeJSON(t, createResp)
	hookID := jsonIDString(t, created["id"], "hook id")

	if err := h.Svc.Git.CreateBranch(ctx, full, "feature", "main"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	prResp := h.DoRESTJSON(t, http.MethodPost, fmt.Sprintf("/api/v3/repos/%s/pulls", full), map[string]any{
		"title": "milestone-only edit",
		"head":  "feature",
		"base":  "main",
	})
	assertStatusCode(t, prResp, http.StatusCreated)
	prBody := testharness.DecodeJSON(t, prResp)
	prNumber, ok := prBody["number"].(float64)
	if !ok {
		t.Fatalf("expected PR number in response, got %+v", prBody)
	}

	ms, err := h.Svc.CreateMilestone(ctx, full, "v2.0", "", "open")
	if err != nil {
		t.Fatalf("create milestone: %v", err)
	}

	patchResp := h.DoRESTJSON(t, http.MethodPatch, fmt.Sprintf("/api/v3/repos/%s/issues/%s", full, strconv.Itoa(int(prNumber))), map[string]any{
		"milestone": ms.Number,
	})
	assertStatusCode(t, patchResp, http.StatusOK)

	listResp := h.DoREST(t, http.MethodGet, fmt.Sprintf("/api/v3/repos/%s/hooks/%s/deliveries", full, hookID), nil)
	assertStatusCode(t, listResp, http.StatusOK)
	deliveries := testharness.DecodeJSONArray(t, listResp)
	if len(deliveries) != 3 {
		t.Fatalf("expected ping + opened + edited deliveries, got %d", len(deliveries))
	}
	if edited := findDeliveryByEventAction(deliveries, "pull_request", "edited"); edited == nil {
		t.Fatalf("expected pull_request.edited delivery, got %+v", deliveries)
	}
}
