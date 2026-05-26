package rest_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
	"gorm.io/gorm"
)

func TestRepoHandlers(t *testing.T) {
	t.Run("CreateUserOrg_Success", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/orgs", map[string]any{
			"login":                         "acme",
			"name":                          "Acme",
			"default_repository_permission": "triage",
		})
		assertStatusCode(t, w, http.StatusCreated)
		body := testharness.DecodeJSON(t, w)
		if body["login"] != "acme" {
			t.Fatalf("expected login acme, got %v", body["login"])
		}
		if body["type"] != "Organization" {
			t.Fatalf("expected type Organization, got %v", body["type"])
		}
		if body["default_repository_permission"] != "read" {
			t.Fatalf("expected default_repository_permission read, got %v", body["default_repository_permission"])
		}

		w = h.DoREST(t, "GET", "/api/v3/user/orgs", nil)
		assertStatusCode(t, w, http.StatusOK)
		orgs := testharness.DecodeJSONArray(t, w)
		if len(orgs) != 1 || orgs[0]["login"] != "acme" {
			t.Fatalf("expected created org to appear in /user/orgs, got %#v", orgs)
		}
	})

	t.Run("CreateOrgRepo_MissingName", func(t *testing.T) {
		h := testharness.New(t)
		w := h.DoRESTJSON(t, "POST", "/api/v3/user/orgs", map[string]any{"login": "acme"})
		assertStatusCode(t, w, http.StatusCreated)

		w = h.DoRESTJSON(t, "POST", "/api/v3/orgs/acme/repos", map[string]any{})
		assertStatusCode(t, w, http.StatusUnprocessableEntity)

		body := testharness.DecodeJSON(t, w)
		if body["message"] != "name is required" {
			t.Fatalf("expected validation message 'name is required', got %v", body["message"])
		}
	})

	t.Run("CreateOrgRepo_DefaultFlags", func(t *testing.T) {
		h := testharness.New(t)
		w := h.DoRESTJSON(t, "POST", "/api/v3/user/orgs", map[string]any{"login": "acme"})
		assertStatusCode(t, w, http.StatusCreated)

		w = h.DoRESTJSON(t, "POST", "/api/v3/orgs/acme/repos", map[string]any{
			"name": "flagrepo",
		})
		assertStatusCode(t, w, http.StatusCreated)
		body := testharness.DecodeJSON(t, w)

		assertBoolField(t, body, "has_issues", true)
		assertBoolField(t, body, "has_wiki", true)
		assertBoolField(t, body, "allow_merge_commit", true)
		assertBoolField(t, body, "allow_squash_merge", true)
		assertBoolField(t, body, "allow_rebase_merge", true)
		assertBoolField(t, body, "allow_auto_merge", false)
		assertBoolField(t, body, "delete_branch_on_merge", false)
		permissions, ok := body["permissions"].(map[string]any)
		if !ok {
			t.Fatalf("expected permissions object, got %T", body["permissions"])
		}
		if permissions["admin"] != true || permissions["push"] != true || permissions["pull"] != true {
			t.Fatalf("expected creator to have admin permissions on org repo, got %#v", permissions)
		}

		w = h.DoREST(t, "GET", "/api/v3/user/repos", nil)
		assertStatusCode(t, w, http.StatusOK)
		repos := testharness.DecodeJSONArray(t, w)
		found := false
		for _, item := range repos {
			if item["full_name"] == "acme/flagrepo" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected org repo to be visible to creator in /api/v3/user/repos, got %#v", repos)
		}
	})

	t.Run("CreateUserRepo_UsesKnownDefaultStatsButGetRecomputes", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "statsrepo",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)
		created := testharness.DecodeJSON(t, w)
		if created["forks_count"] != float64(0) || created["open_issues_count"] != float64(0) || created["stargazers_count"] != float64(0) {
			t.Fatalf("expected create response to use zeroed counters, got %#v", created)
		}
		if created["size"] != float64(0) {
			t.Fatalf("expected create response to use zero disk usage, got %v", created["size"])
		}

		fullName := "testuser/statsrepo"
		wantSize := h.Svc.GitDiskUsageKB(context.Background(), fullName)
		if wantSize <= 0 {
			t.Fatalf("expected seeded repo %s to have measurable disk usage, got %dKB", fullName, wantSize)
		}

		w = h.DoREST(t, "GET", "/api/v3/repos/"+fullName, nil)
		assertStatusCode(t, w, http.StatusOK)
		got := testharness.DecodeJSON(t, w)
		if got["size"] != float64(wantSize) {
			t.Fatalf("expected follow-up GET size %dKB, got %v", wantSize, got["size"])
		}
		if got["forks_count"] != float64(0) || got["open_issues_count"] != float64(0) || got["stargazers_count"] != float64(0) {
			t.Fatalf("expected follow-up GET counters to remain zero, got %#v", got)
		}
	})

	t.Run("CreateOrgRepo_RequiresOrgAdmin", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/orgs", map[string]any{"login": "secureorg"})
		assertStatusCode(t, w, http.StatusCreated)

		org, err := h.Svc.GetUser(ctx, "secureorg")
		if err != nil {
			t.Fatalf("GetUser org failed: %v", err)
		}

		member, memberToken := seedHarnessUser(t, h, "org-member", false)
		if err := h.Svc.AddOrgMember(ctx, org.ID, member.ID, db.OrganizationRoleMember); err != nil {
			t.Fatalf("AddOrgMember failed: %v", err)
		}
		_, outsiderToken := seedHarnessUser(t, h, "org-outsider-create", false)

		for _, tc := range []struct {
			name  string
			token string
		}{
			{name: "member", token: memberToken},
			{name: "outsider", token: outsiderToken},
		} {
			t.Run(tc.name, func(t *testing.T) {
				w := h.DoRESTJSONWithToken(t, "POST", "/api/v3/orgs/secureorg/repos", tc.token, map[string]any{
					"name": "blocked-repo",
				})
				assertStatusCode(t, w, http.StatusForbidden)
				body := testharness.DecodeJSON(t, w)
				if body["message"] != "org admin access required" {
					t.Fatalf("expected authz message, got %v", body["message"])
				}
			})
		}

		w = h.DoREST(t, "GET", "/api/v3/orgs/secureorg/repos", nil)
		assertStatusCode(t, w, http.StatusOK)
		if repos := testharness.DecodeJSONArray(t, w); len(repos) != 0 {
			t.Fatalf("expected no repos after forbidden create attempts, got %#v", repos)
		}
	})

	t.Run("GetOrg_ExistingOrg_Returns200", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/orgs", map[string]any{
			"login": "testorg",
		})
		assertStatusCode(t, w, http.StatusCreated)

		// GET /api/v3/orgs/testorg should return 200
		w = h.DoREST(t, "GET", "/api/v3/orgs/testorg", nil)
		assertStatusCode(t, w, http.StatusOK)
		body := testharness.DecodeJSON(t, w)
		if body["login"] != "testorg" {
			t.Fatalf("expected login testorg, got %v", body["login"])
		}
		if body["type"] != "Organization" {
			t.Fatalf("expected type Organization, got %v", body["type"])
		}
		if body["default_repository_permission"] != "none" {
			t.Fatalf("expected default_repository_permission none, got %v", body["default_repository_permission"])
		}
	})

	t.Run("GetOrg_UserLogin_Returns404", func(t *testing.T) {
		h := testharness.New(t)

		// Create a regular user
		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name": "user-repo",
		})
		assertStatusCode(t, w, http.StatusCreated)

		// GET /api/v3/orgs/testuser should return 404 (user is not an org)
		w = h.DoREST(t, "GET", "/api/v3/orgs/testuser", nil)
		assertStatusCode(t, w, http.StatusNotFound)
	})

	t.Run("GetOrg_NonExistent_Returns404", func(t *testing.T) {
		h := testharness.New(t)

		// GET /api/v3/orgs/nonexistent-org should return 404
		w := h.DoREST(t, "GET", "/api/v3/orgs/nonexistent-org", nil)
		assertStatusCode(t, w, http.StatusNotFound)
	})

	t.Run("CreateTeam_UserLogin_Returns404", func(t *testing.T) {
		h := testharness.New(t)

		// Create a regular user first
		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name": "user-repo-for-team-test",
		})
		assertStatusCode(t, w, http.StatusCreated)

		// POST /api/v3/orgs/testuser/teams should return 404 (user is not an org)
		w = h.DoRESTJSON(t, "POST", "/api/v3/orgs/testuser/teams", map[string]any{
			"name": "test-team",
		})
		assertStatusCode(t, w, http.StatusNotFound)
	})

	t.Run("CreateTeam_NonExistentOrg_Returns404", func(t *testing.T) {
		h := testharness.New(t)

		// POST /api/v3/orgs/nonexistent-org/teams should return 404
		w := h.DoRESTJSON(t, "POST", "/api/v3/orgs/nonexistent-org/teams", map[string]any{
			"name": "test-team",
		})
		assertStatusCode(t, w, http.StatusNotFound)
	})

	t.Run("ReplaceRepoTopics", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name": "topicrepo",
		})
		assertStatusCode(t, w, http.StatusCreated)

		w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/topicrepo/topics", map[string]any{
			"names": []string{"go", "cli"},
		})
		assertStatusCode(t, w, http.StatusOK)
		resp := testharness.DecodeJSON(t, w)
		assertStringSlice(t, resp["names"], []string{"go", "cli"})

		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/topicrepo/topics", nil)
		assertStatusCode(t, w, http.StatusOK)
		resp = testharness.DecodeJSON(t, w)
		assertStringSlice(t, resp["names"], []string{"go", "cli"})
	})

	t.Run("ForkRepo_UserAndOrgTargets", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/orgs", map[string]any{"login": "sourceorg"})
		assertStatusCode(t, w, http.StatusCreated)
		w = h.DoRESTJSON(t, "POST", "/api/v3/user/orgs", map[string]any{"login": "targetorg"})
		assertStatusCode(t, w, http.StatusCreated)

		w = h.DoRESTJSON(t, "POST", "/api/v3/orgs/sourceorg/repos", map[string]any{
			"name":      "base",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)

		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/sourceorg/base/forks", map[string]any{})
		assertStatusCode(t, w, http.StatusAccepted)
		forkResp := testharness.DecodeJSON(t, w)
		if forkResp["full_name"] != "testuser/base" {
			t.Fatalf("expected fork full_name testuser/base, got %v", forkResp["full_name"])
		}
		assertBoolField(t, forkResp, "fork", true)

		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/sourceorg/base/forks", map[string]any{
			"organization": "targetorg",
			"name":         "base-fork",
		})
		assertStatusCode(t, w, http.StatusAccepted)
		forkResp = testharness.DecodeJSON(t, w)
		if forkResp["full_name"] != "targetorg/base-fork" {
			t.Fatalf("expected fork full_name targetorg/base-fork, got %v", forkResp["full_name"])
		}
		assertBoolField(t, forkResp, "fork", true)
	})

	t.Run("ForkRepo_ToOrg_RequiresOrgAdmin", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/orgs", map[string]any{"login": "fork-secure-org"})
		assertStatusCode(t, w, http.StatusCreated)
		w = h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "public-fork-source",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)

		org, err := h.Svc.GetUser(ctx, "fork-secure-org")
		if err != nil {
			t.Fatalf("GetUser org failed: %v", err)
		}
		member, memberToken := seedHarnessUser(t, h, "fork-org-member", false)
		if err := h.Svc.AddOrgMember(ctx, org.ID, member.ID, db.OrganizationRoleMember); err != nil {
			t.Fatalf("AddOrgMember failed: %v", err)
		}
		_, outsiderToken := seedHarnessUser(t, h, "fork-org-outsider", false)

		for _, tc := range []struct {
			name  string
			token string
		}{
			{name: "member", token: memberToken},
			{name: "outsider", token: outsiderToken},
		} {
			t.Run(tc.name, func(t *testing.T) {
				w := h.DoRESTJSONWithToken(t, "POST", "/api/v3/repos/testuser/public-fork-source/forks", tc.token, map[string]any{
					"organization": "fork-secure-org",
					"name":         "blocked-fork",
				})
				assertStatusCode(t, w, http.StatusForbidden)
				body := testharness.DecodeJSON(t, w)
				if body["message"] != "org admin access required" {
					t.Fatalf("expected authz message, got %v", body["message"])
				}
			})
		}

		w = h.DoREST(t, "GET", "/api/v3/orgs/fork-secure-org/repos", nil)
		assertStatusCode(t, w, http.StatusOK)
		if repos := testharness.DecodeJSONArray(t, w); len(repos) != 0 {
			t.Fatalf("expected no forks in org after forbidden attempts, got %#v", repos)
		}
	})

	t.Run("ForkRepo_FinalizeFailureReturns422AndCompensates", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/orgs", map[string]any{"login": "sourceorg"})
		assertStatusCode(t, w, http.StatusCreated)
		w = h.DoRESTJSON(t, "POST", "/api/v3/user/orgs", map[string]any{"login": "targetorg"})
		assertStatusCode(t, w, http.StatusCreated)

		w = h.DoRESTJSON(t, "POST", "/api/v3/orgs/sourceorg/repos", map[string]any{
			"name":      "base",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)

		const cbName = "test:fork_finalize_fail_returns_422"
		var failOnce atomic.Bool
		failOnce.Store(true)
		if err := h.Svc.DB.Callback().Update().Before("gorm:update").Register(cbName, func(tx *gorm.DB) {
			if tx.Statement.Table == "repositories" && failOnce.CompareAndSwap(true, false) {
				tx.AddError(errors.New("forced finalize failure"))
			}
		}); err != nil {
			t.Fatalf("register update callback: %v", err)
		}
		defer func() {
			_ = h.Svc.DB.Callback().Update().Remove(cbName)
		}()

		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/sourceorg/base/forks", map[string]any{
			"organization": "targetorg",
		})
		assertStatusCode(t, w, http.StatusUnprocessableEntity)
		body := testharness.DecodeJSON(t, w)
		if body["message"] != "validation failed: forced finalize failure" {
			t.Fatalf("expected fork failure message, got %v", body["message"])
		}
		if failOnce.Load() {
			t.Fatal("expected injected finalize failure to trigger")
		}

		if _, err := h.Svc.GetRepo(ctx, "targetorg/base"); !errors.Is(err, service.ErrNotFound) {
			t.Fatalf("expected compensated fork repo to be absent, got err=%v", err)
		}
		if h.Svc.Git.Exists(ctx, "targetorg/base") {
			t.Fatal("expected compensated fork git directory to be removed")
		}
		if _, err := h.Svc.GetRepo(ctx, "sourceorg/base"); err != nil {
			t.Fatalf("expected source repo to remain after compensated fork failure: %v", err)
		}
		if !h.Svc.Git.Exists(ctx, "sourceorg/base") {
			t.Fatal("expected source git directory to remain after compensated fork failure")
		}
	})

	t.Run("TransferRepo_ValidationAndNotFound", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "transferme",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)

		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/transferme/transfer", map[string]any{})
		assertStatusCode(t, w, http.StatusUnprocessableEntity)
		body := testharness.DecodeJSON(t, w)
		if body["message"] != "new_owner is required" {
			t.Fatalf("expected validation message 'new_owner is required', got %v", body["message"])
		}

		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/transferme/transfer", map[string]any{
			"new_owner": "ghost-org",
		})
		assertStatusCode(t, w, http.StatusNotFound)
	})

	t.Run("TransferRepo_ToOrgKeepsCreatorVisible", func(t *testing.T) {
		h := testharness.New(t)
		w := h.DoRESTJSON(t, "POST", "/api/v3/user/orgs", map[string]any{"login": "memory-org"})
		assertStatusCode(t, w, http.StatusCreated)

		w = h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "shared-memory",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)

		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/shared-memory/transfer", map[string]any{
			"new_owner": "memory-org",
		})
		assertStatusCode(t, w, http.StatusAccepted)
		body := testharness.DecodeJSON(t, w)
		if body["full_name"] != "memory-org/shared-memory" {
			t.Fatalf("expected transferred full_name memory-org/shared-memory, got %v", body["full_name"])
		}
		permissions, ok := body["permissions"].(map[string]any)
		if !ok {
			t.Fatalf("expected permissions object, got %T", body["permissions"])
		}
		if permissions["admin"] != true || permissions["push"] != true || permissions["pull"] != true {
			t.Fatalf("expected creator to retain admin permissions after transfer, got %#v", permissions)
		}

		w = h.DoREST(t, "GET", "/api/v3/user/repos", nil)
		assertStatusCode(t, w, http.StatusOK)
		repos := testharness.DecodeJSONArray(t, w)
		found := false
		for _, item := range repos {
			if item["full_name"] == "memory-org/shared-memory" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected transferred org repo to remain visible to creator, got %#v", repos)
		}
	})

	t.Run("TransferRepo_ToOrgWithExistingRepo_ReturnsConflict", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/orgs", map[string]any{"login": "memory-org"})
		assertStatusCode(t, w, http.StatusCreated)

		w = h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "shared-memory",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)

		w = h.DoRESTJSON(t, "POST", "/api/v3/orgs/memory-org/repos", map[string]any{
			"name":      "shared-memory",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)

		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/shared-memory/transfer", map[string]any{
			"new_owner": "memory-org",
		})
		assertStatusCode(t, w, http.StatusConflict)
		body := testharness.DecodeJSON(t, w)
		if body["message"] != "conflict: repository memory-org/shared-memory already exists" {
			t.Fatalf("expected conflict message for existing destination repo, got %v", body["message"])
		}
	})

	t.Run("TransferRepo_ToOrg_RequiresOrgAdmin", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/orgs", map[string]any{"login": "transfer-secure-org"})
		assertStatusCode(t, w, http.StatusCreated)

		org, err := h.Svc.GetUser(ctx, "transfer-secure-org")
		if err != nil {
			t.Fatalf("GetUser org failed: %v", err)
		}
		member, memberToken := seedHarnessUser(t, h, "transfer-org-member", false)
		if err := h.Svc.AddOrgMember(ctx, org.ID, member.ID, db.OrganizationRoleMember); err != nil {
			t.Fatalf("AddOrgMember failed: %v", err)
		}
		_, outsiderToken := seedHarnessUser(t, h, "transfer-org-outsider", false)

		for _, tc := range []struct {
			name     string
			token    string
			repoName string
		}{
			{name: "member", token: memberToken, repoName: "member-owned-repo"},
			{name: "outsider", token: outsiderToken, repoName: "outsider-owned-repo"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				w := h.DoRESTJSONWithToken(t, "POST", "/api/v3/user/repos", tc.token, map[string]any{
					"name":      tc.repoName,
					"auto_init": true,
				})
				assertStatusCode(t, w, http.StatusCreated)

				body := testharness.DecodeJSON(t, w)
				fullName, ok := body["full_name"].(string)
				if !ok || fullName == "" {
					t.Fatalf("expected full_name in create repo response, got %#v", body)
				}

				w = h.DoRESTJSONWithToken(t, "POST", "/api/v3/repos/"+fullName+"/transfer", tc.token, map[string]any{
					"new_owner": "transfer-secure-org",
				})
				assertStatusCode(t, w, http.StatusForbidden)
				body = testharness.DecodeJSON(t, w)
				if body["message"] != "org admin access required" {
					t.Fatalf("expected authz message, got %v", body["message"])
				}
			})
		}

		w = h.DoREST(t, "GET", "/api/v3/orgs/transfer-secure-org/repos", nil)
		assertStatusCode(t, w, http.StatusOK)
		if repos := testharness.DecodeJSONArray(t, w); len(repos) != 0 {
			t.Fatalf("expected no transferred repos in org after forbidden attempts, got %#v", repos)
		}
	})

	t.Run("TransferRepo_ToExistingOrgLetsCreatorCreateTeam", func(t *testing.T) {
		h := testharness.New(t)
		_, token := seedHarnessUser(t, h, "transfer-bootstrap", false)
		w := h.DoRESTJSONWithToken(t, "POST", "/api/v3/user/orgs", token, map[string]any{
			"login": "transfer-bootstrap-org",
		})
		assertStatusCode(t, w, http.StatusCreated)

		w = h.DoRESTJSONWithToken(t, "POST", "/api/v3/user/repos", token, map[string]any{
			"name":      "bootstrap-shared",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)

		w = h.DoRESTJSONWithToken(t, "POST", "/api/v3/repos/transfer-bootstrap/bootstrap-shared/transfer", token, map[string]any{
			"new_owner": "transfer-bootstrap-org",
		})
		assertStatusCode(t, w, http.StatusAccepted)

		w = h.DoRESTJSONWithToken(t, "POST", "/api/v3/orgs/transfer-bootstrap-org/teams", token, map[string]any{
			"name": "created-after-transfer",
		})
		assertStatusCode(t, w, http.StatusCreated)
		body := testharness.DecodeJSON(t, w)
		if body["slug"] != "created-after-transfer" {
			t.Fatalf("expected slug created-after-transfer, got %v", body["slug"])
		}
	})

	t.Run("TransferRepo_ToMissingOrg_Returns404", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "missing-org-target",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)

		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/missing-org-target/transfer", map[string]any{
			"new_owner": "missing-transfer-org",
		})
		assertStatusCode(t, w, http.StatusNotFound)
	})
}

func assertBoolField(t *testing.T, body map[string]any, field string, want bool) {
	t.Helper()
	got, ok := body[field].(bool)
	if !ok || got != want {
		t.Fatalf("field %q: expected %v, got %v", field, want, body[field])
	}
}

func assertStringSlice(t *testing.T, value any, want []string) {
	t.Helper()
	gotRaw, ok := value.([]any)
	if !ok {
		t.Fatalf("expected []any, got %T", value)
	}
	if len(gotRaw) != len(want) {
		t.Fatalf("expected %d elements, got %d", len(want), len(gotRaw))
	}
	for i, v := range gotRaw {
		vs, ok := v.(string)
		if !ok {
			t.Fatalf("expected string at %d, got %T", i, v)
		}
		if vs != want[i] {
			t.Fatalf("expected %q at %d, got %q", want[i], i, vs)
		}
	}
}
