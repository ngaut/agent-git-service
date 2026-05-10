package rest_test

import (
	"context"
	"net/http"
	"testing"

	"gh-server/internal/service"
	"gh-server/internal/testharness"
)

func TestCompat_RepoSecretGet_ByNamespace(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "compat-repo-secret-get")

	repo, err := h.Svc.GetRepo(ctx, "testuser/compat-repo-secret-get")
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if err := h.Svc.UpsertSecret(ctx, service.UpsertSecretInput{
		OwnerID: h.User.ID,
		RepoID:  &repo.ID,
		Name:    "TOKEN",
		Value:   "super-secret",
	}); err != nil {
		t.Fatalf("UpsertSecret: %v", err)
	}

	for _, namespace := range []string{"actions", "dependabot", "codespaces"} {
		t.Run(namespace, func(t *testing.T) {
			path := "/api/v3/repos/testuser/compat-repo-secret-get/" + namespace + "/secrets/TOKEN"
			w := h.DoREST(t, "GET", path, nil)
			assertStatusCode(t, w, http.StatusOK)

			body := testharness.DecodeJSON(t, w)
			if body["name"] != "TOKEN" {
				t.Fatalf("name: got %v, want TOKEN", body["name"])
			}
			if body["created_at"] == "" || body["updated_at"] == "" {
				t.Fatalf("expected timestamps in response, got %v", body)
			}
			if _, ok := body["value"]; ok {
				t.Fatalf("secret value must be omitted from response: %v", body)
			}
		})
	}

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-repo-secret-get/actions/secrets/MISSING", nil)
	assertStatusCode(t, w, http.StatusNotFound)
}
