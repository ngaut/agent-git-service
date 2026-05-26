package rest_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/ngaut/agent-git-service/internal/crypto"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

func TestCompat_UserCodespacesSecrets(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "compat-user-codespaces-secret")

	repo, err := h.Svc.GetRepo(ctx, "testuser/compat-user-codespaces-secret")
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	repoID := strconv.FormatUint(uint64(repo.ID), 10)

	w := h.DoREST(t, "GET", "/api/v3/user/codespaces/secrets", nil)
	assertStatusCode(t, w, 200)
	body := testharness.DecodeJSON(t, w)
	if body["total_count"] != float64(0) {
		t.Fatalf("initial total_count: got %v, want 0", body["total_count"])
	}

	w = h.DoREST(t, "GET", "/api/v3/user/codespaces/secrets/public-key", nil)
	assertStatusCode(t, w, 200)
	key := testharness.DecodeJSON(t, w)
	if key["key_id"] == "" || key["key"] == "" {
		t.Fatalf("expected public key response, got %v", key)
	}

	encrypted, err := crypto.EncryptSecret("super-secret")
	if err != nil {
		t.Fatalf("EncryptSecret: %v", err)
	}
	w = h.DoRESTJSON(t, "PUT", "/api/v3/user/codespaces/secrets/DEV_TOKEN", map[string]any{
		"encrypted_value":         encrypted,
		"key_id":                  key["key_id"],
		"selected_repository_ids": []string{repoID},
	})
	assertStatusCode(t, w, 201)

	w = h.DoREST(t, "GET", "/api/v3/user/codespaces/secrets/DEV_TOKEN", nil)
	assertStatusCode(t, w, 200)
	secret := testharness.DecodeJSON(t, w)
	if secret["name"] != "DEV_TOKEN" {
		t.Fatalf("name: got %v, want DEV_TOKEN", secret["name"])
	}
	if secret["visibility"] != "selected" {
		t.Fatalf("visibility: got %v, want selected", secret["visibility"])
	}
	if secret["selected_repositories_url"] == "" {
		t.Fatalf("expected selected_repositories_url, got %v", secret)
	}
	if _, ok := secret["value"]; ok {
		t.Fatalf("secret value must be omitted from response: %v", secret)
	}

	w = h.DoREST(t, "GET", "/api/v3/user/codespaces/secrets", nil)
	assertStatusCode(t, w, 200)
	body = testharness.DecodeJSON(t, w)
	if body["total_count"] != float64(1) {
		t.Fatalf("total_count after create: got %v, want 1", body["total_count"])
	}

	w = h.DoREST(t, "GET", "/api/v3/user/codespaces/secrets/DEV_TOKEN/repositories", nil)
	assertStatusCode(t, w, 200)
	repos := testharness.DecodeJSON(t, w)
	if repos["total_count"] != float64(1) {
		t.Fatalf("selected repos count: got %v, want 1", repos["total_count"])
	}

	w = h.DoREST(t, "DELETE", "/api/v3/user/codespaces/secrets/DEV_TOKEN/repositories/"+repoID, nil)
	assertStatusCode(t, w, 204)
	w = h.DoREST(t, "GET", "/api/v3/user/codespaces/secrets/DEV_TOKEN/repositories", nil)
	assertStatusCode(t, w, 200)
	repos = testharness.DecodeJSON(t, w)
	if repos["total_count"] != float64(0) {
		t.Fatalf("selected repos count after remove: got %v, want 0", repos["total_count"])
	}

	w = h.DoRESTJSON(t, "PUT", "/api/v3/user/codespaces/secrets/DEV_TOKEN/repositories", map[string]any{
		"selected_repository_ids": []any{float64(repo.ID)},
	})
	assertStatusCode(t, w, 204)
	w = h.DoREST(t, "GET", "/api/v3/user/codespaces/secrets/DEV_TOKEN/repositories", nil)
	assertStatusCode(t, w, 200)
	repos = testharness.DecodeJSON(t, w)
	if repos["total_count"] != float64(1) {
		t.Fatalf("selected repos count after set: got %v, want 1", repos["total_count"])
	}

	w = h.DoREST(t, "DELETE", "/api/v3/user/codespaces/secrets/DEV_TOKEN", nil)
	assertStatusCode(t, w, 204)
	w = h.DoREST(t, "GET", "/api/v3/user/codespaces/secrets/DEV_TOKEN", nil)
	assertStatusCode(t, w, 404)
}
