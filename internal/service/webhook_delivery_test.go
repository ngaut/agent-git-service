package service_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"gh-server/internal/db"
)

type recordedWebhookRequest struct {
	Header http.Header
	Body   string
}

func TestDispatchWebhookEventPersistsAndPostsMatchingHook(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "whdelivery", "repo")

	var repo db.Repository
	if err := svc.DB.Preload("Owner").First(&repo, "full_name = ?", "whdelivery/repo").Error; err != nil {
		t.Fatalf("find repo: %v", err)
	}

	var (
		mu   sync.Mutex
		hits []recordedWebhookRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits = append(hits, recordedWebhookRequest{
			Header: r.Header.Clone(),
			Body:   string(body),
		})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	pushHook := &db.Webhook{
		RepositoryID: repo.ID,
		Name:         "web",
		Active:       true,
		EventsJSON:   `["push"]`,
		ConfigJSON:   `{"url":"` + server.URL + `","content_type":"json","secret":"topsecret"}`,
	}
	issuesHook := &db.Webhook{
		RepositoryID: repo.ID,
		Name:         "web",
		Active:       true,
		EventsJSON:   `["issues"]`,
		ConfigJSON:   `{"url":"` + server.URL + `","content_type":"json"}`,
	}
	if err := svc.CreateWebhook(ctx, pushHook); err != nil {
		t.Fatalf("create push hook: %v", err)
	}
	if err := svc.CreateWebhook(ctx, issuesHook); err != nil {
		t.Fatalf("create issues hook: %v", err)
	}

	payload := map[string]any{"ref": "refs/heads/main", "after": "abc123"}
	if err := svc.DispatchWebhookEvent(ctx, repo.ID, "push", "", payload); err != nil {
		t.Fatalf("dispatch webhook event: %v", err)
	}

	mu.Lock()
	if len(hits) != 1 {
		t.Fatalf("expected 1 outbound webhook request, got %d", len(hits))
	}
	req := hits[0]
	mu.Unlock()

	if got := req.Header.Get("X-GitHub-Event"); got != "push" {
		t.Fatalf("X-GitHub-Event: got %q", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type: got %q", got)
	}
	if got := req.Header.Get("X-Hub-Signature-256"); got == "" {
		t.Fatal("expected X-Hub-Signature-256 header")
	}

	var received map[string]any
	if err := json.Unmarshal([]byte(req.Body), &received); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if received["ref"] != "refs/heads/main" {
		t.Fatalf("payload ref: got %v", received["ref"])
	}

	deliveries, err := svc.ListHookDeliveries(ctx, repo.ID, pushHook.ID)
	if err != nil {
		t.Fatalf("list hook deliveries: %v", err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 persisted delivery, got %d", len(deliveries))
	}
	if deliveries[0].Status != "ok" {
		t.Fatalf("delivery status: got %q", deliveries[0].Status)
	}

	otherDeliveries, err := svc.ListHookDeliveries(ctx, repo.ID, issuesHook.ID)
	if err != nil {
		t.Fatalf("list issues hook deliveries: %v", err)
	}
	if len(otherDeliveries) != 0 {
		t.Fatalf("expected 0 deliveries for non-matching hook, got %d", len(otherDeliveries))
	}
}

func TestRedeliverHookDeliveryCreatesNewAttempt(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "whredeliver", "repo")

	var repo db.Repository
	if err := svc.DB.Preload("Owner").First(&repo, "full_name = ?", "whredeliver/repo").Error; err != nil {
		t.Fatalf("find repo: %v", err)
	}

	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("accepted"))
	}))
	defer server.Close()

	hook := &db.Webhook{
		RepositoryID: repo.ID,
		Name:         "web",
		Active:       true,
		EventsJSON:   `["push"]`,
		ConfigJSON:   `{"url":"` + server.URL + `","content_type":"json"}`,
	}
	if err := svc.CreateWebhook(ctx, hook); err != nil {
		t.Fatalf("create hook: %v", err)
	}
	if err := svc.DispatchWebhookEvent(ctx, repo.ID, "push", "", map[string]any{"after": "abc123"}); err != nil {
		t.Fatalf("dispatch webhook event: %v", err)
	}

	deliveries, err := svc.ListHookDeliveries(ctx, repo.ID, hook.ID)
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 initial delivery, got %d", len(deliveries))
	}

	redelivery, err := svc.RedeliverHookDelivery(ctx, repo.ID, hook.ID, deliveries[0].ID)
	if err != nil {
		t.Fatalf("redeliver hook delivery: %v", err)
	}
	if !redelivery.Redelivery {
		t.Fatal("expected redelivery flag to be true")
	}
	if redelivery.GUID != deliveries[0].GUID {
		t.Fatalf("expected GUID to be reused, got %q want %q", redelivery.GUID, deliveries[0].GUID)
	}
	if hits != 2 {
		t.Fatalf("expected 2 outbound requests after redelivery, got %d", hits)
	}

	allDeliveries, err := svc.ListHookDeliveries(ctx, repo.ID, hook.ID)
	if err != nil {
		t.Fatalf("list all deliveries: %v", err)
	}
	if len(allDeliveries) != 2 {
		t.Fatalf("expected 2 stored deliveries, got %d", len(allDeliveries))
	}
}

func TestDispatchWebhookEventHonorsInsecureSSL(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "whinsecure", "repo")

	var repo db.Repository
	if err := svc.DB.Preload("Owner").First(&repo, "full_name = ?", "whinsecure/repo").Error; err != nil {
		t.Fatalf("find repo: %v", err)
	}

	var hits int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	strictHook := &db.Webhook{
		RepositoryID: repo.ID,
		Name:         "web",
		Active:       true,
		EventsJSON:   `["push"]`,
		ConfigJSON:   `{"url":"` + server.URL + `","content_type":"json","insecure_ssl":"0"}`,
	}
	insecureHook := &db.Webhook{
		RepositoryID: repo.ID,
		Name:         "web",
		Active:       true,
		EventsJSON:   `["push"]`,
		ConfigJSON:   `{"url":"` + server.URL + `","content_type":"json","insecure_ssl":"1"}`,
	}
	if err := svc.CreateWebhook(ctx, strictHook); err != nil {
		t.Fatalf("create strict hook: %v", err)
	}
	if err := svc.CreateWebhook(ctx, insecureHook); err != nil {
		t.Fatalf("create insecure hook: %v", err)
	}

	if err := svc.DispatchWebhookEvent(ctx, repo.ID, "push", "", map[string]any{"after": "abc123"}); err != nil {
		t.Fatalf("dispatch webhook event: %v", err)
	}

	strictDeliveries, err := svc.ListHookDeliveries(ctx, repo.ID, strictHook.ID)
	if err != nil {
		t.Fatalf("list strict deliveries: %v", err)
	}
	if len(strictDeliveries) != 1 {
		t.Fatalf("expected 1 strict delivery, got %d", len(strictDeliveries))
	}
	if strictDeliveries[0].Status != "error" {
		t.Fatalf("expected strict delivery to fail TLS verification, got %q", strictDeliveries[0].Status)
	}

	insecureDeliveries, err := svc.ListHookDeliveries(ctx, repo.ID, insecureHook.ID)
	if err != nil {
		t.Fatalf("list insecure deliveries: %v", err)
	}
	if len(insecureDeliveries) != 1 {
		t.Fatalf("expected 1 insecure delivery, got %d", len(insecureDeliveries))
	}
	if insecureDeliveries[0].Status != "ok" {
		t.Fatalf("expected insecure delivery to succeed, got %q", insecureDeliveries[0].Status)
	}
	if hits != 1 {
		t.Fatalf("expected only insecure delivery to reach the TLS server, got %d hits", hits)
	}
}
