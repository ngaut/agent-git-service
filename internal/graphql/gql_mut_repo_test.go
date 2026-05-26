package graphql_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func doRawGql(t *testing.T, mux http.Handler, query string, vars map[string]any) map[string]any {
	t.Helper()

	reqBody := map[string]any{"query": query}
	if vars != nil {
		reqBody["variables"] = vars
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("gql marshal: %v", err)
	}

	req := httptest.NewRequest("POST", "/graphql", bytes.NewReader(b))
	req.Header.Set("Authorization", "token test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("gql non-200: %d\n%s", w.Code, w.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("gql json decode: %v", err)
	}
	return res
}

func restUserNodeID(t *testing.T, mux http.Handler, login string) string {
	t.Helper()

	req := httptest.NewRequest("GET", "/api/v3/users/"+login, nil)
	req.Header.Set("Authorization", "token test-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /users/%s returned %d: %s", login, w.Code, w.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode /users/%s: %v", login, err)
	}
	nodeID, _ := res["node_id"].(string)
	if nodeID == "" {
		t.Fatalf("GET /users/%s returned empty node_id", login)
	}
	return nodeID
}

func TestCreateRepositoryAcceptsRESTNodeIDForOrganizationOwner(t *testing.T) {
	svc, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	ctx := context.Background()
	org := db.User{Login: "testorg", Name: "Test Org", Type: db.TypeOrganization}
	if err := svc.DB.WithContext(ctx).Create(&org).Error; err != nil {
		t.Fatalf("seed org: %v", err)
	}

	ownerID := restUserNodeID(t, mux, org.Login)
	res := doRawGql(t, mux, `
		mutation($input: CreateRepositoryInput!) {
			createRepository(input: $input) {
				repository {
					name
					owner { login }
				}
			}
		}
	`, map[string]any{
		"input": map[string]any{
			"name":             "org-owned-repo",
			"visibility":       "PRIVATE",
			"ownerId":          ownerID,
			"hasIssuesEnabled": true,
			"hasWikiEnabled":   true,
		},
	})

	if errs := res["errors"]; errs != nil {
		t.Fatalf("unexpected gql errors: %v", errs)
	}

	data := res["data"].(map[string]any)
	repo := data["createRepository"].(map[string]any)["repository"].(map[string]any)
	if repo["name"] != "org-owned-repo" {
		t.Fatalf("repo.name = %v, want org-owned-repo", repo["name"])
	}
	if got := repo["owner"].(map[string]any)["login"]; got != org.Login {
		t.Fatalf("repo.owner.login = %v, want %s", got, org.Login)
	}

	stored, err := svc.GetRepo(ctx, "testorg/org-owned-repo")
	if err != nil {
		t.Fatalf("GetRepo(testorg/org-owned-repo): %v", err)
	}
	if stored.Owner.Login != org.Login {
		t.Fatalf("stored repo owner = %s, want %s", stored.Owner.Login, org.Login)
	}
}

func TestCreateRepositoryRejectsUnresolvableOwnerID(t *testing.T) {
	svc, mux, user, cleanup := setupTestEnvironment(t)
	defer cleanup()

	res := doRawGql(t, mux, `
		mutation($input: CreateRepositoryInput!) {
			createRepository(input: $input) {
				repository {
					name
					owner { login }
				}
			}
		}
	`, map[string]any{
		"input": map[string]any{
			"name":             "should-not-fallback",
			"visibility":       "PRIVATE",
			"ownerId":          "bm90LWEtdmFsaWQtbm9kZS1pZA==",
			"hasIssuesEnabled": true,
			"hasWikiEnabled":   true,
		},
	})

	if res["errors"] == nil {
		t.Fatalf("expected gql errors for invalid ownerId, got none")
	}

	if _, err := svc.GetRepo(context.Background(), user.Login+"/should-not-fallback"); err == nil {
		t.Fatalf("repo should not have been created under fallback owner %s", user.Login)
	}
}

func TestCreateRepositoryRejectsNonStringOwnerID(t *testing.T) {
	svc, mux, user, cleanup := setupTestEnvironment(t)
	defer cleanup()

	res := doRawGql(t, mux, `
		mutation($input: CreateRepositoryInput!) {
			createRepository(input: $input) {
				repository {
					name
					owner { login }
				}
			}
		}
	`, map[string]any{
		"input": map[string]any{
			"name":             "non-string-owner",
			"visibility":       "PRIVATE",
			"ownerId":          12345,
			"hasIssuesEnabled": true,
			"hasWikiEnabled":   true,
		},
	})

	if res["errors"] == nil {
		t.Fatalf("expected gql errors for non-string ownerId, got none")
	}

	if _, err := svc.GetRepo(context.Background(), user.Login+"/non-string-owner"); err == nil {
		t.Fatalf("repo should not have been created under fallback owner %s", user.Login)
	}
}

func TestCreateRepositoryPreservesExplicitFalseFlags(t *testing.T) {
	svc, mux, user, cleanup := setupTestEnvironment(t)
	defer cleanup()

	res := doRawGql(t, mux, `
		mutation($input: CreateRepositoryInput!) {
			createRepository(input: $input) {
				repository {
					name
					hasIssuesEnabled
					hasWikiEnabled
				}
			}
		}
	`, map[string]any{
		"input": map[string]any{
			"name":             "flagged-repo",
			"visibility":       "PRIVATE",
			"hasIssuesEnabled": false,
			"hasWikiEnabled":   false,
		},
	})

	if errs := res["errors"]; errs != nil {
		t.Fatalf("unexpected gql errors: %v", errs)
	}

	data := res["data"].(map[string]any)
	repo := data["createRepository"].(map[string]any)["repository"].(map[string]any)
	if repo["hasIssuesEnabled"] != false {
		t.Fatalf("repository.hasIssuesEnabled = %v, want false", repo["hasIssuesEnabled"])
	}
	if repo["hasWikiEnabled"] != false {
		t.Fatalf("repository.hasWikiEnabled = %v, want false", repo["hasWikiEnabled"])
	}

	stored, err := svc.GetRepo(context.Background(), user.Login+"/flagged-repo")
	if err != nil {
		t.Fatalf("GetRepo(%s/flagged-repo): %v", user.Login, err)
	}
	if stored.HasIssues {
		t.Fatalf("stored HasIssues = true, want false")
	}
	if stored.HasWiki {
		t.Fatalf("stored HasWiki = true, want false")
	}
}

func TestCreateRepositoryPreservesInternalVisibility(t *testing.T) {
	svc, mux, user, cleanup := setupTestEnvironment(t)
	defer cleanup()

	res := doRawGql(t, mux, `
		mutation($input: CreateRepositoryInput!) {
			createRepository(input: $input) {
				repository {
					name
					visibility
					isPrivate
				}
			}
		}
	`, map[string]any{
		"input": map[string]any{
			"name":       "internal-visibility-repo",
			"visibility": "INTERNAL",
		},
	})

	if errs := res["errors"]; errs != nil {
		t.Fatalf("unexpected gql errors: %v", errs)
	}

	repo := res["data"].(map[string]any)["createRepository"].(map[string]any)["repository"].(map[string]any)
	if repo["visibility"] != "INTERNAL" {
		t.Fatalf("repository.visibility = %v, want INTERNAL", repo["visibility"])
	}
	if repo["isPrivate"] != true {
		t.Fatalf("repository.isPrivate = %v, want true", repo["isPrivate"])
	}

	stored, err := svc.GetRepo(context.Background(), user.Login+"/internal-visibility-repo")
	if err != nil {
		t.Fatalf("GetRepo(%s/internal-visibility-repo): %v", user.Login, err)
	}
	if stored.Visibility != "internal" {
		t.Fatalf("stored.Visibility = %q, want internal", stored.Visibility)
	}
	if !stored.Private {
		t.Fatalf("stored.Private = false, want true")
	}
}

func TestCloneTemplateRepositoryAcceptsRESTNodeIDForOrganizationOwner(t *testing.T) {
	svc, mux, user, cleanup := setupTestEnvironment(t)
	defer cleanup()

	ctx := context.Background()
	org := db.User{Login: "testorg", Name: "Test Org", Type: db.TypeOrganization}
	if err := svc.DB.WithContext(ctx).Create(&org).Error; err != nil {
		t.Fatalf("seed org: %v", err)
	}
	template, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: user.Login,
		Name:       "template-src",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo(template-src): %v", err)
	}

	ownerID := restUserNodeID(t, mux, org.Login)
	res := doRawGql(t, mux, `
		mutation($input: CloneTemplateRepositoryInput!) {
			cloneTemplateRepository(input: $input) {
				repository {
					name
					owner { login }
				}
			}
		}
	`, map[string]any{
		"input": map[string]any{
			"name":               "template-clone",
			"visibility":         "PRIVATE",
			"ownerId":            ownerID,
			"repositoryId":       fmt.Sprintf("Repository_%d", template.ID),
			"includeAllBranches": false,
		},
	})

	if errs := res["errors"]; errs != nil {
		t.Fatalf("unexpected gql errors: %v", errs)
	}

	data := res["data"].(map[string]any)
	repo := data["cloneTemplateRepository"].(map[string]any)["repository"].(map[string]any)
	if got := repo["owner"].(map[string]any)["login"]; got != org.Login {
		t.Fatalf("clone owner.login = %v, want %s", got, org.Login)
	}

	if _, err := svc.GetRepo(ctx, "testorg/template-clone"); err != nil {
		t.Fatalf("GetRepo(testorg/template-clone): %v", err)
	}
	if template.Name != "template-src" {
		t.Fatalf("template repo setup corrupted: got %s", template.Name)
	}
}

func TestUpdateRepositoryClearsHomepageURL(t *testing.T) {
	svc, mux, user, cleanup := setupTestEnvironment(t)
	defer cleanup()

	ctx := context.Background()
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: user.Login,
		Name:       "homepage-repo",
		AutoInit:   true,
		Homepage:   "https://example.com/original",
	})
	if err != nil {
		t.Fatalf("CreateRepo(homepage-repo): %v", err)
	}

	res := doRawGql(t, mux, `
		mutation($input: UpdateRepositoryInput!) {
			updateRepository(input: $input) {
				repository {
					homepageUrl
				}
			}
		}
	`, map[string]any{
		"input": map[string]any{
			"repositoryId": fmt.Sprintf("Repository_%d", repo.ID),
			"homepageUrl":  nil,
		},
	})

	if errs := res["errors"]; errs != nil {
		t.Fatalf("unexpected gql errors: %v", errs)
	}

	repository := res["data"].(map[string]any)["updateRepository"].(map[string]any)["repository"].(map[string]any)
	if repository["homepageUrl"] != nil {
		t.Fatalf("repository.homepageUrl = %v, want nil", repository["homepageUrl"])
	}

	stored, err := svc.GetRepo(ctx, user.Login+"/homepage-repo")
	if err != nil {
		t.Fatalf("GetRepo(%s/homepage-repo): %v", user.Login, err)
	}
	if stored.Homepage != "" {
		t.Fatalf("stored.Homepage = %q, want empty string", stored.Homepage)
	}
}
