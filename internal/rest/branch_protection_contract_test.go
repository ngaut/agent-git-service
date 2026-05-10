package rest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"gh-server/internal/service"
	"gh-server/internal/testharness"
)

func TestBranchProtectionBypassAllowancesRESTContract(t *testing.T) {
	h := testharness.New(t)

	w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
		"name": "branch-protection-contract",
	})
	assertStatusCode(t, w, http.StatusCreated)

	w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/branch-protection-contract/branches/main/protection", map[string]any{
		"required_pull_request_reviews": map[string]any{
			"required_approving_review_count": 1,
			"bypass_pull_request_allowances": map[string]any{
				"users": []string{"agent-1"},
			},
		},
	})
	assertStatusCode(t, w, http.StatusOK)
	body := testharness.DecodeJSON(t, w)
	reviews := body["required_pull_request_reviews"].(map[string]any)
	bypass := reviews["bypass_pull_request_allowances"].(map[string]any)
	users := bypass["users"].([]any)
	if len(users) != 1 || users[0] != "agent-1" {
		t.Fatalf("expected bypass users round-trip, got %#v", bypass["users"])
	}

	w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/branch-protection-contract/branches/main/protection", map[string]any{
		"required_pull_request_reviews": map[string]any{
			"bypass_pull_request_allowances": map[string]any{
				"apps": []string{"trusted-app"},
			},
		},
	})
	assertStatusCode(t, w, http.StatusUnprocessableEntity)
	errBody := testharness.DecodeJSON(t, w)
	if errBody["message"] != "required_pull_request_reviews.bypass_pull_request_allowances.apps is not supported" {
		t.Fatalf("unexpected validation error: %#v", errBody)
	}
}

func TestBranchProtectionSubresourceRESTContract(t *testing.T) {
	h := testharness.New(t)

	w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
		"name": "branch-protection-subresources",
	})
	assertStatusCode(t, w, http.StatusCreated)

	w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/branch-protection-subresources/branches/main/protection", map[string]any{
		"required_status_checks": map[string]any{
			"strict":   false,
			"contexts": []string{"ci"},
		},
		"required_pull_request_reviews": map[string]any{
			"required_approving_review_count": 1,
		},
	})
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/branch-protection-subresources/branches/main/protection/required_status_checks", nil)
	assertStatusCode(t, w, http.StatusOK)
	status := testharness.DecodeJSON(t, w)
	if status["strict"] != false {
		t.Fatalf("strict: got %v, want false", status["strict"])
	}
	if status["url"] == "" || status["contexts_url"] == "" {
		t.Fatalf("expected status-check subresource links, got %#v", status)
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/branch-protection-subresources/branches/main/protection/required_status_checks/contexts", nil)
	assertStatusCode(t, w, http.StatusOK)
	contexts := decodeStringArray(t, w.Body.Bytes())
	if len(contexts) != 1 || contexts[0] != "ci" {
		t.Fatalf("contexts: got %#v, want [ci]", contexts)
	}

	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/branch-protection-subresources/branches/main/protection/required_status_checks", map[string]any{
		"strict":   true,
		"contexts": []string{"ci", "lint"},
	})
	assertStatusCode(t, w, http.StatusOK)
	status = testharness.DecodeJSON(t, w)
	if status["strict"] != true {
		t.Fatalf("strict after patch: got %v, want true", status["strict"])
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/branch-protection-subresources/branches/main/protection/required_pull_request_reviews", nil)
	assertStatusCode(t, w, http.StatusOK)
	reviews := testharness.DecodeJSON(t, w)
	if reviews["required_approving_review_count"] != float64(1) {
		t.Fatalf("required reviews should survive status-check patch, got %#v", reviews)
	}

	w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/branch-protection-subresources/branches/main/protection/required_status_checks/contexts", map[string]any{
		"contexts": []string{"deploy"},
	})
	assertStatusCode(t, w, http.StatusOK)
	contexts = decodeStringArray(t, w.Body.Bytes())
	if len(contexts) != 1 || contexts[0] != "deploy" {
		t.Fatalf("contexts after replace: got %#v, want [deploy]", contexts)
	}

	w = h.DoREST(t, "PUT", "/api/v3/repos/testuser/branch-protection-subresources/branches/main/protection/enforce_admins", nil)
	assertStatusCode(t, w, http.StatusOK)
	enforceAdmins := testharness.DecodeJSON(t, w)
	if enforceAdmins["enabled"] != true {
		t.Fatalf("enforce_admins enabled: got %v, want true", enforceAdmins["enabled"])
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/branch-protection-subresources/branches/main/protection/required_signatures", nil)
	assertStatusCode(t, w, http.StatusOK)
	signatures := testharness.DecodeJSON(t, w)
	if signatures["enabled"] != false {
		t.Fatalf("required_signatures default: got %v, want false", signatures["enabled"])
	}
	if signatures["url"] == "" {
		t.Fatalf("expected required_signatures url, got %#v", signatures)
	}

	w = h.DoREST(t, "POST", "/api/v3/repos/testuser/branch-protection-subresources/branches/dev/protection/required_signatures", nil)
	assertStatusCode(t, w, http.StatusNotFound)

	w = h.DoREST(t, "POST", "/api/v3/repos/testuser/branch-protection-subresources/branches/main/protection/required_signatures", nil)
	assertStatusCode(t, w, http.StatusOK)
	signatures = testharness.DecodeJSON(t, w)
	if signatures["enabled"] != true {
		t.Fatalf("required_signatures enabled: got %v, want true", signatures["enabled"])
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/branch-protection-subresources/branches/main/protection", nil)
	assertStatusCode(t, w, http.StatusOK)
	protection := testharness.DecodeJSON(t, w)
	signatures, ok := protection["required_signatures"].(map[string]any)
	if !ok || signatures["enabled"] != true {
		t.Fatalf("monolithic required_signatures: got %#v, want enabled true", protection["required_signatures"])
	}

	w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/branch-protection-subresources/branches/main/protection", map[string]any{
		"required_status_checks": map[string]any{
			"strict":   false,
			"contexts": []string{"ci", "deploy"},
		},
	})
	assertStatusCode(t, w, http.StatusOK)
	protection = testharness.DecodeJSON(t, w)
	signatures, ok = protection["required_signatures"].(map[string]any)
	if !ok || signatures["enabled"] != false {
		t.Fatalf("monolithic update should replace omitted required_signatures, got %#v", protection["required_signatures"])
	}

	w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/branch-protection-subresources/branches/main/protection", map[string]any{
		"required_status_checks": map[string]any{
			"strict":   false,
			"contexts": []string{"ci", "deploy"},
		},
		"required_signatures": true,
	})
	assertStatusCode(t, w, http.StatusOK)
	protection = testharness.DecodeJSON(t, w)
	signatures, ok = protection["required_signatures"].(map[string]any)
	if !ok || signatures["enabled"] != true {
		t.Fatalf("monolithic update should accept explicit required_signatures, got %#v", protection["required_signatures"])
	}

	w = h.DoREST(t, "DELETE", "/api/v3/repos/testuser/branch-protection-subresources/branches/main/protection/required_signatures", nil)
	assertStatusCode(t, w, http.StatusNoContent)
	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/branch-protection-subresources/branches/main/protection/required_signatures", nil)
	assertStatusCode(t, w, http.StatusOK)
	signatures = testharness.DecodeJSON(t, w)
	if signatures["enabled"] != false {
		t.Fatalf("required_signatures after delete: got %v, want false", signatures["enabled"])
	}

	w = h.DoREST(t, "DELETE", "/api/v3/repos/testuser/branch-protection-subresources/branches/main/protection/required_status_checks", nil)
	assertStatusCode(t, w, http.StatusNoContent)
	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/branch-protection-subresources/branches/main/protection/required_status_checks", nil)
	assertStatusCode(t, w, http.StatusNotFound)
}

func TestBranchProtectionRestrictionActorMutationSubresources(t *testing.T) {
	h := testharness.New(t)

	w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
		"name": "branch-protection-restriction-actors",
	})
	assertStatusCode(t, w, http.StatusCreated)

	base := "/api/v3/repos/testuser/branch-protection-restriction-actors/branches/main/protection"
	w = h.DoRESTJSON(t, "PUT", base, map[string]any{
		"restrictions": map[string]any{
			"users": []string{"agent-1"},
			"teams": []string{"ops"},
			"apps":  []string{"ci-app"},
		},
	})
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoREST(t, "GET", base+"/restrictions/users", nil)
	assertStatusCode(t, w, http.StatusOK)
	assertStringArray(t, decodeStringArray(t, w.Body.Bytes()), []string{"agent-1"})

	w = h.DoRESTJSON(t, "POST", base+"/restrictions/users", map[string]any{
		"users": []string{"agent-2", "agent-1"},
	})
	assertStatusCode(t, w, http.StatusOK)
	assertStringArray(t, decodeStringArray(t, w.Body.Bytes()), []string{"agent-1", "agent-2"})

	w = h.DoRESTJSON(t, "PUT", base+"/restrictions/teams", map[string]any{
		"teams": []string{"platform"},
	})
	assertStatusCode(t, w, http.StatusOK)
	assertStringArray(t, decodeStringArray(t, w.Body.Bytes()), []string{"platform"})

	w = h.DoRESTJSON(t, "POST", base+"/restrictions/apps", map[string]any{
		"apps": []string{"deploy-app"},
	})
	assertStatusCode(t, w, http.StatusOK)
	assertStringArray(t, decodeStringArray(t, w.Body.Bytes()), []string{"ci-app", "deploy-app"})

	w = h.DoRESTJSON(t, "DELETE", base+"/restrictions/users", map[string]any{
		"users": []string{"agent-1"},
	})
	assertStatusCode(t, w, http.StatusOK)
	assertStringArray(t, decodeStringArray(t, w.Body.Bytes()), []string{"agent-2"})

	w = h.DoRESTJSON(t, "DELETE", base+"/restrictions/users", map[string]any{
		"users": []string{"agent-1", "agent-1"},
	})
	assertStatusCode(t, w, http.StatusOK)
	assertStringArray(t, decodeStringArray(t, w.Body.Bytes()), []string{"agent-2"})

	w = h.DoRESTJSON(t, "DELETE", base+"/restrictions/users", map[string]any{
		"users": []string{"missing-user"},
	})
	assertStatusCode(t, w, http.StatusOK)
	assertStringArray(t, decodeStringArray(t, w.Body.Bytes()), []string{"agent-2"})

	w = h.DoRESTJSON(t, "DELETE", base+"/restrictions/users", map[string]any{
		"users": []string{},
	})
	assertStatusCode(t, w, http.StatusOK)
	assertStringArray(t, decodeStringArray(t, w.Body.Bytes()), []string{"agent-2"})

	w = h.DoREST(t, "GET", base+"/restrictions", nil)
	assertStatusCode(t, w, http.StatusOK)
	restrictions := testharness.DecodeJSON(t, w)
	if restrictions["users_url"] == "" || restrictions["teams_url"] == "" || restrictions["apps_url"] == "" {
		t.Fatalf("expected restriction actor URLs, got %#v", restrictions)
	}
	assertAnyStringArray(t, restrictions["users"], []string{"agent-2"})
	assertAnyStringArray(t, restrictions["teams"], []string{"platform"})
	assertAnyStringArray(t, restrictions["apps"], []string{"ci-app", "deploy-app"})
}

func TestBranchProtectionRestrictionActorMutationSubresourcesRejectInvalidBodies(t *testing.T) {
	h := testharness.New(t)

	w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
		"name": "branch-protection-restriction-actors-invalid",
	})
	assertStatusCode(t, w, http.StatusCreated)

	base := "/api/v3/repos/testuser/branch-protection-restriction-actors-invalid/branches/main/protection"
	w = h.DoRESTJSON(t, "PUT", base, map[string]any{
		"restrictions": map[string]any{
			"users": []string{"agent-1"},
		},
	})
	assertStatusCode(t, w, http.StatusOK)

	assertInvalidRestrictionActorMutationBody(t, h, "POST", base+"/restrictions/users", "")
	assertInvalidRestrictionActorMutationBody(t, h, "PUT", base+"/restrictions/users", `{}`)
	assertInvalidRestrictionActorMutationBody(t, h, "DELETE", base+"/restrictions/users", `{"teams":["ops"]}`)
	assertInvalidRestrictionActorMutationBody(t, h, "POST", base+"/restrictions/users", `{"users":"agent-2"}`)

	w = h.DoREST(t, "GET", base+"/restrictions/users", nil)
	assertStatusCode(t, w, http.StatusOK)
	assertStringArray(t, decodeStringArray(t, w.Body.Bytes()), []string{"agent-1"})
}

func assertInvalidRestrictionActorMutationBody(t *testing.T, h *testharness.Harness, method, path, body string) {
	t.Helper()
	w := h.DoREST(t, method, path, strings.NewReader(body))
	assertStatusCode(t, w, http.StatusUnprocessableEntity)
	errBody := testharness.DecodeJSON(t, w)
	if errBody["message"] != "invalid body" {
		t.Fatalf("unexpected validation error: %#v", errBody)
	}
}

func TestBranchProtectionRoutesWithProtectionInBranchName(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "branch-protection-name-regression",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	branchName := "release/protection/hotfix"
	if err := h.Svc.Git.CreateBranch(ctx, "testuser/branch-protection-name-regression", branchName, "main"); err != nil {
		t.Fatalf("create branch: %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/branch-protection-name-regression/branches/"+branchName, nil)
	assertStatusCode(t, w, http.StatusOK)
	branch := testharness.DecodeJSON(t, w)
	if branch["name"] != branchName {
		t.Fatalf("branch name: got %v, want %q", branch["name"], branchName)
	}

	w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/branch-protection-name-regression/branches/"+branchName+"/protection", map[string]any{
		"required_status_checks": map[string]any{
			"strict":   true,
			"contexts": []string{"ci"},
		},
	})
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/branch-protection-name-regression/branches/"+branchName+"/protection/required_status_checks/contexts", nil)
	assertStatusCode(t, w, http.StatusOK)
	contexts := decodeStringArray(t, w.Body.Bytes())
	if len(contexts) != 1 || contexts[0] != "ci" {
		t.Fatalf("contexts: got %#v, want [ci]", contexts)
	}
}

func TestBranchProtectionRoutesPreferExactSuffixBranchName(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	fullName := "testuser/branch-protection-suffix-regression"
	_, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "branch-protection-suffix-regression",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	branchName := "hotfix/protection/required_status_checks"
	if err := h.Svc.Git.CreateBranch(ctx, fullName, branchName, "main"); err != nil {
		t.Fatalf("create suffix branch: %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/repos/"+fullName+"/branches/"+branchName, nil)
	assertStatusCode(t, w, http.StatusOK)
	branch := testharness.DecodeJSON(t, w)
	if branch["name"] != branchName {
		t.Fatalf("branch name: got %v, want %q", branch["name"], branchName)
	}

	w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/"+fullName+"/branches/"+branchName+"/protection", map[string]any{
		"required_status_checks": map[string]any{
			"strict":   true,
			"contexts": []string{"suffix-ci"},
		},
	})
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoREST(t, "GET", "/api/v3/repos/"+fullName+"/branches/"+branchName+"/protection/required_status_checks/contexts", nil)
	assertStatusCode(t, w, http.StatusOK)
	contexts := decodeStringArray(t, w.Body.Bytes())
	if len(contexts) != 1 || contexts[0] != "suffix-ci" {
		t.Fatalf("suffix branch protection contexts: got %#v, want [suffix-ci]", contexts)
	}

	w = h.DoREST(t, "DELETE", "/api/v3/repos/"+fullName+"/branches/"+branchName, nil)
	assertStatusCode(t, w, http.StatusNotFound)
}

func decodeStringArray(t *testing.T, raw []byte) []string {
	t.Helper()
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode string array: %v\nbody: %s", err, string(raw))
	}
	return out
}

func assertStringArray(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("string array length: got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("string array: got %#v, want %#v", got, want)
		}
	}
}

func assertAnyStringArray(t *testing.T, raw any, want []string) {
	t.Helper()
	values, ok := raw.([]any)
	if !ok {
		t.Fatalf("expected array, got %T", raw)
	}
	got := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("expected string item, got %T", value)
		}
		got = append(got, text)
	}
	assertStringArray(t, got, want)
}
