package rest_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

func TestEnvironmentRoutesByNameAndID(t *testing.T) {
	h := testharness.New(t)
	ctx := service.ContextWithUser(context.Background(), h.User)

	repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "env-api",
	})
	if err != nil {
		t.Fatalf("CreateRepo failed: %v", err)
	}

	createBody := map[string]any{
		"wait_timer": 45,
		"deployment_branch_policy": map[string]any{
			"protected_branches": true,
		},
	}
	createResp := h.DoRESTJSON(t, http.MethodPut, "/api/v3/repos/testuser/env-api/environments/production", createBody)
	assertStatusCode(t, createResp, http.StatusOK)
	created := testharness.DecodeJSON(t, createResp)
	assertFieldsPresent(t, created, map[string]string{
		"id":                       "number",
		"node_id":                  "string",
		"name":                     "string",
		"url":                      "string",
		"html_url":                 "string",
		"created_at":               "string",
		"updated_at":               "string",
		"protection_rules":         "array",
		"deployment_branch_policy": "object",
	})

	updateResp := h.DoRESTJSON(t, http.MethodPut, "/api/v3/repos/testuser/env-api/environments/production", map[string]any{
		"deployment_branch_policy": map[string]any{
			"custom_branch_policies": true,
		},
	})
	assertStatusCode(t, updateResp, http.StatusOK)

	invalidPolicyResp := h.DoRESTJSON(t, http.MethodPut, "/api/v3/repos/testuser/env-api/environments/invalid", map[string]any{
		"deployment_branch_policy": map[string]any{
			"protected_branches":     true,
			"custom_branch_policies": true,
		},
	})
	assertStatusCode(t, invalidPolicyResp, http.StatusUnprocessableEntity)

	if _, err := h.Svc.CreateVariable(ctx, repo.OwnerID, &repo.ID, "production", "ENV_VAR", "value"); err != nil {
		t.Fatalf("CreateVariable failed: %v", err)
	}
	if err := h.Svc.UpsertSecret(ctx, service.UpsertSecretInput{
		OwnerID: repo.OwnerID,
		RepoID:  &repo.ID,
		Env:     "production",
		Name:    "ENV_SECRET",
		Value:   "secret",
	}); err != nil {
		t.Fatalf("UpsertSecret failed: %v", err)
	}

	listResp := h.DoREST(t, http.MethodGet, "/api/v3/repos/testuser/env-api/environments", nil)
	assertStatusCode(t, listResp, http.StatusOK)
	listBody := testharness.DecodeJSON(t, listResp)
	if got := int(listBody["total_count"].(float64)); got != 1 {
		t.Fatalf("expected total_count=1, got %d", got)
	}

	getByNameResp := h.DoREST(t, http.MethodGet, "/api/v3/repos/testuser/env-api/environments/production", nil)
	assertStatusCode(t, getByNameResp, http.StatusOK)

	getByIDResp := h.DoREST(t, http.MethodGet, fmt.Sprintf("/api/v3/repositories/%d/environments/production", repo.ID), nil)
	assertStatusCode(t, getByIDResp, http.StatusOK)

	deleteByIDResp := h.DoREST(t, http.MethodDelete, fmt.Sprintf("/api/v3/repositories/%d/environments/production", repo.ID), nil)
	assertStatusCode(t, deleteByIDResp, http.StatusNoContent)

	missingGet := h.DoREST(t, http.MethodGet, "/api/v3/repos/testuser/env-api/environments/production", nil)
	assertStatusCode(t, missingGet, http.StatusNotFound)

	missingDelete := h.DoREST(t, http.MethodDelete, "/api/v3/repos/testuser/env-api/environments/production", nil)
	assertStatusCode(t, missingDelete, http.StatusNotFound)
}
