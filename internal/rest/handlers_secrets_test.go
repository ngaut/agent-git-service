package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"gh-server/internal/crypto"
	"gh-server/internal/db"
	"gh-server/internal/service"
)

func setupSecretTestService(t *testing.T) (*service.Service, *gorm.DB) {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.sqlite")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.Exec("PRAGMA busy_timeout = 5000").Error; err != nil {
		t.Fatalf("sqlite busy_timeout: %v", err)
	}
	if err := gdb.Exec("PRAGMA journal_mode = WAL").Error; err != nil {
		t.Fatalf("sqlite journal_mode: %v", err)
	}
	if err := db.Migrate(gdb); err != nil {
		t.Fatalf("migrate db: %v", err)
	}

	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	svc := &service.Service{DB: gdb, BaseURL: "http://test.local"}
	return svc, gdb
}

func seedUser(t *testing.T, gdb *gorm.DB, login, userType string) db.User {
	t.Helper()
	user := db.User{Login: login, Name: login, Type: userType}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("seed user %s: %v", login, err)
	}
	return user
}

func seedRepo(t *testing.T, gdb *gorm.DB, owner db.User, name string) db.Repository {
	t.Helper()
	repo := db.Repository{
		Name:       name,
		FullName:   fmt.Sprintf("%s/%s", owner.Login, name),
		OwnerID:    owner.ID,
		Private:    false,
		Visibility: "public",
	}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("seed repo %s: %v", name, err)
	}
	return repo
}

func seedSecret(t *testing.T, gdb *gorm.DB, secret db.Secret) db.Secret {
	t.Helper()
	if err := gdb.Create(&secret).Error; err != nil {
		t.Fatalf("seed secret %s: %v", secret.Name, err)
	}
	return secret
}

func newJSONRequest(t *testing.T, method, target string, body any, params map[string]string) *http.Request {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(payload)
	} else {
		reader = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, target, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if len(params) > 0 {
		rc := chi.NewRouteContext()
		for key, val := range params {
			rc.URLParams.Add(key, val)
		}
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
	}
	return req
}

func decodeJSONResponse(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode json response: %v", err)
	}
	return out
}

func assertStatusCode(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status code: got %d, want %d; body: %s", w.Code, want, w.Body.String())
	}
}

func mustEncrypt(t *testing.T, value string) string {
	t.Helper()
	encrypted, err := crypto.EncryptSecret(value)
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	return encrypted
}

func TestListRepoSecrets(t *testing.T) {
	svc, gdb := setupSecretTestService(t)
	deps := &Deps{Svc: svc}

	owner := seedUser(t, gdb, "octo", db.TypeUser)
	repo := seedRepo(t, gdb, owner, "secrets")
	seedSecret(t, gdb, db.Secret{
		OwnerID:      owner.ID,
		RepositoryID: &repo.ID,
		Environment:  "",
		Name:         "TOKEN",
		Value:        "super-secret",
	})

	t.Run("success lists secrets", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodGet, "/repos/octo/secrets/actions/secrets", nil, map[string]string{
			"owner": "octo",
			"repo":  "secrets",
		})
		w := httptest.NewRecorder()

		deps.ListRepoSecrets(w, req)

		assertStatusCode(t, w, http.StatusOK)
		body := decodeJSONResponse(t, w)
		if got, ok := body["total_count"].(float64); !ok || int(got) != 1 {
			t.Fatalf("expected total_count=1, got %v", body["total_count"])
		}
		secrets, ok := body["secrets"].([]any)
		if !ok || len(secrets) != 1 {
			t.Fatalf("expected 1 secret, got %v", body["secrets"])
		}
		secret, ok := secrets[0].(map[string]any)
		if !ok {
			t.Fatalf("expected secret map, got %T", secrets[0])
		}
		if secret["name"] != "TOKEN" {
			t.Fatalf("expected secret name TOKEN, got %v", secret["name"])
		}
		if _, ok := secret["value"]; ok {
			t.Fatalf("expected secret value to be omitted from list response")
		}
		if secret["created_at"] == "" || secret["updated_at"] == "" {
			t.Fatalf("expected created_at/updated_at to be populated, got %v", secret)
		}
	})

	t.Run("missing repo returns 404", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodGet, "/repos/ghost/missing/actions/secrets", nil, map[string]string{
			"owner": "ghost",
			"repo":  "missing",
		})
		w := httptest.NewRecorder()

		deps.ListRepoSecrets(w, req)

		assertStatusCode(t, w, http.StatusNotFound)
		body := decodeJSONResponse(t, w)
		if body["message"] != "Not Found" {
			t.Fatalf("expected Not Found message, got %v", body["message"])
		}
	})
}

func TestSetOrgSecretRepos(t *testing.T) {
	t.Run("success updates selected repos", func(t *testing.T) {
		svc, gdb := setupSecretTestService(t)
		deps := &Deps{Svc: svc}

		org := seedUser(t, gdb, "acme", db.TypeOrganization)
		repoA := seedRepo(t, gdb, org, "alpha")
		repoB := seedRepo(t, gdb, org, "beta")
		secret := seedSecret(t, gdb, db.Secret{
			OwnerID:    org.ID,
			Name:       "ORG_TOKEN",
			Visibility: "selected",
		})

		req := newJSONRequest(t, http.MethodPut, "/orgs/acme/actions/secrets/ORG_TOKEN/repositories", map[string]any{
			"selected_repository_ids": []uint{repoA.ID, repoB.ID},
		}, map[string]string{
			"org":  "acme",
			"name": "ORG_TOKEN",
		})
		w := httptest.NewRecorder()

		deps.SetOrgSecretRepos(w, req)

		assertStatusCode(t, w, http.StatusNoContent)

		var got db.Secret
		if err := gdb.First(&got, secret.ID).Error; err != nil {
			t.Fatalf("reload secret: %v", err)
		}
		expected := fmt.Sprintf("%d,%d", repoA.ID, repoB.ID)
		if got.SelectedRepoIDs != expected {
			t.Fatalf("expected selected_repo_ids %q, got %q", expected, got.SelectedRepoIDs)
		}
	})

	t.Run("missing secret returns 404", func(t *testing.T) {
		svc, gdb := setupSecretTestService(t)
		deps := &Deps{Svc: svc}

		_ = seedUser(t, gdb, "acme", db.TypeOrganization)

		req := newJSONRequest(t, http.MethodPut, "/orgs/acme/actions/secrets/missing/repositories", map[string]any{
			"selected_repository_ids": []uint{1, 2},
		}, map[string]string{
			"org":  "acme",
			"name": "missing",
		})
		w := httptest.NewRecorder()

		deps.SetOrgSecretRepos(w, req)

		assertStatusCode(t, w, http.StatusNotFound)
		body := decodeJSONResponse(t, w)
		if body["message"] != "Not Found" {
			t.Fatalf("expected Not Found message, got %v", body["message"])
		}
	})

	t.Run("missing org returns 404", func(t *testing.T) {
		svc, _ := setupSecretTestService(t)
		deps := &Deps{Svc: svc}

		req := newJSONRequest(t, http.MethodPut, "/orgs/ghost/actions/secrets/ORG_TOKEN/repositories", map[string]any{
			"selected_repository_ids": []uint{1},
		}, map[string]string{
			"org":  "ghost",
			"name": "ORG_TOKEN",
		})
		w := httptest.NewRecorder()

		deps.SetOrgSecretRepos(w, req)

		assertStatusCode(t, w, http.StatusNotFound)
		body := decodeJSONResponse(t, w)
		if body["message"] != "Not Found" {
			t.Fatalf("expected Not Found message, got %v", body["message"])
		}
	})
}

func TestUpsertSecret(t *testing.T) {
	type upsertCase struct {
		name       string
		seed       func(t *testing.T, gdb *gorm.DB, owner db.User, repo db.Repository)
		body       map[string]any
		wantStatus int
		assert     func(t *testing.T, gdb *gorm.DB, owner db.User, repo db.Repository)
	}

	cases := []upsertCase{
		{
			name: "create new secret",
			body: map[string]any{
				"encrypted_value": mustEncrypt(t, "shh"),
				"key_id":          "1",
			},
			wantStatus: http.StatusCreated,
			assert: func(t *testing.T, gdb *gorm.DB, owner db.User, repo db.Repository) {
				var got db.Secret
				err := gdb.Where("name = ? AND repository_id = ?", "TOKEN", repo.ID).First(&got).Error
				if err != nil {
					t.Fatalf("expected secret to be created: %v", err)
				}
				if got.Value != "shh" {
					t.Fatalf("expected secret value 'shh', got %q", got.Value)
				}
			},
		},
		{
			name: "update existing secret",
			seed: func(t *testing.T, gdb *gorm.DB, owner db.User, repo db.Repository) {
				seedSecret(t, gdb, db.Secret{
					OwnerID:      owner.ID,
					RepositoryID: &repo.ID,
					Name:         "TOKEN",
					Value:        "old",
				})
			},
			body: map[string]any{
				"encrypted_value": mustEncrypt(t, "new"),
				"key_id":          "1",
			},
			wantStatus: http.StatusNoContent,
			assert: func(t *testing.T, gdb *gorm.DB, owner db.User, repo db.Repository) {
				var got db.Secret
				err := gdb.Where("name = ? AND repository_id = ?", "TOKEN", repo.ID).First(&got).Error
				if err != nil {
					t.Fatalf("expected secret to exist: %v", err)
				}
				if got.Value != "new" {
					t.Fatalf("expected secret value 'new', got %q", got.Value)
				}
			},
		},
		{
			name: "invalid ciphertext does not overwrite",
			seed: func(t *testing.T, gdb *gorm.DB, owner db.User, repo db.Repository) {
				seedSecret(t, gdb, db.Secret{
					OwnerID:      owner.ID,
					RepositoryID: &repo.ID,
					Name:         "TOKEN",
					Value:        "keep",
				})
			},
			body: map[string]any{
				"encrypted_value": "not-base64",
				"key_id":          "1",
			},
			wantStatus: http.StatusNoContent,
			assert: func(t *testing.T, gdb *gorm.DB, owner db.User, repo db.Repository) {
				var got db.Secret
				err := gdb.Where("name = ? AND repository_id = ?", "TOKEN", repo.ID).First(&got).Error
				if err != nil {
					t.Fatalf("expected secret to exist: %v", err)
				}
				if got.Value != "keep" {
					t.Fatalf("expected secret value to remain 'keep', got %q", got.Value)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, gdb := setupSecretTestService(t)
			deps := &Deps{Svc: svc}

			owner := seedUser(t, gdb, "octo", db.TypeUser)
			repo := seedRepo(t, gdb, owner, "secrets")
			if tc.seed != nil {
				tc.seed(t, gdb, owner, repo)
			}

			req := newJSONRequest(t, http.MethodPut, "/repos/octo/secrets/actions/secrets/TOKEN", tc.body, map[string]string{
				"name": "TOKEN",
			})
			w := httptest.NewRecorder()

			deps.upsertSecret(w, req, &repo.ID, owner.ID, "")

			assertStatusCode(t, w, tc.wantStatus)
			if tc.assert != nil {
				tc.assert(t, gdb, owner, repo)
			}
		})
	}

	t.Run("missing name returns 422", func(t *testing.T) {
		svc, gdb := setupSecretTestService(t)
		deps := &Deps{Svc: svc}

		owner := seedUser(t, gdb, "octo", db.TypeUser)
		repo := seedRepo(t, gdb, owner, "secrets")

		req := newJSONRequest(t, http.MethodPut, "/repos/octo/secrets/actions/secrets", map[string]any{
			"encrypted_value": mustEncrypt(t, "oops"),
			"key_id":          "1",
		}, map[string]string{})
		w := httptest.NewRecorder()

		deps.upsertSecret(w, req, &repo.ID, owner.ID, "")

		assertStatusCode(t, w, http.StatusUnprocessableEntity)
		body := decodeJSONResponse(t, w)
		msg, _ := body["message"].(string)
		if !strings.Contains(msg, "name is required") {
			t.Fatalf("expected validation message to mention name is required, got %v", body["message"])
		}
	})

	t.Run("org scope creates secret when repoID nil", func(t *testing.T) {
		svc, gdb := setupSecretTestService(t)
		deps := &Deps{Svc: svc}

		org := seedUser(t, gdb, "acme", db.TypeOrganization)

		req := newJSONRequest(t, http.MethodPut, "/orgs/acme/actions/secrets/ORG_TOKEN", map[string]any{
			"encrypted_value": mustEncrypt(t, "org-secret"),
			"key_id":          "1",
		}, map[string]string{
			"name": "ORG_TOKEN",
		})
		w := httptest.NewRecorder()

		deps.upsertSecret(w, req, nil, org.ID, "")

		assertStatusCode(t, w, http.StatusCreated)
		var got db.Secret
		err := gdb.Where("name = ? AND repository_id IS NULL", "ORG_TOKEN").First(&got).Error
		if err != nil {
			t.Fatalf("expected org secret to be created: %v", err)
		}
		if got.OwnerID != org.ID {
			t.Fatalf("expected owner id %d, got %d", org.ID, got.OwnerID)
		}
	})
}

func TestUpsertOrgSecret(t *testing.T) {
	t.Run("create defaults visibility and stores selection", func(t *testing.T) {
		svc, gdb := setupSecretTestService(t)
		deps := &Deps{Svc: svc}

		org := seedUser(t, gdb, "acme", db.TypeOrganization)
		repoA := seedRepo(t, gdb, org, "alpha")
		repoB := seedRepo(t, gdb, org, "beta")

		req := newJSONRequest(t, http.MethodPut, "/orgs/acme/actions/secrets/ORG_TOKEN", map[string]any{
			"encrypted_value":         mustEncrypt(t, "org-secret"),
			"key_id":                  "1",
			"selected_repository_ids": []uint{repoA.ID, repoB.ID},
		}, map[string]string{
			"name": "ORG_TOKEN",
		})
		w := httptest.NewRecorder()

		deps.upsertOrgSecret(w, req, org.ID)

		assertStatusCode(t, w, http.StatusCreated)
		var got db.Secret
		err := gdb.Where("name = ? AND repository_id IS NULL", "ORG_TOKEN").First(&got).Error
		if err != nil {
			t.Fatalf("expected org secret to be created: %v", err)
		}
		if got.Visibility != "private" {
			t.Fatalf("expected default visibility private, got %q", got.Visibility)
		}
		expected := fmt.Sprintf("%d,%d", repoA.ID, repoB.ID)
		if got.SelectedRepoIDs != expected {
			t.Fatalf("expected selected_repo_ids %q, got %q", expected, got.SelectedRepoIDs)
		}
		if got.Value != "org-secret" {
			t.Fatalf("expected secret value 'org-secret', got %q", got.Value)
		}
	})

	t.Run("update existing secret preserves value when empty", func(t *testing.T) {
		svc, gdb := setupSecretTestService(t)
		deps := &Deps{Svc: svc}

		org := seedUser(t, gdb, "acme", db.TypeOrganization)
		repoA := seedRepo(t, gdb, org, "alpha")
		seedSecret(t, gdb, db.Secret{
			OwnerID:    org.ID,
			Name:       "ORG_TOKEN",
			Value:      "keep",
			Visibility: "private",
		})

		req := newJSONRequest(t, http.MethodPut, "/orgs/acme/actions/secrets/ORG_TOKEN", map[string]any{
			"visibility":              "selected",
			"selected_repository_ids": []uint{repoA.ID},
		}, map[string]string{
			"name": "ORG_TOKEN",
		})
		w := httptest.NewRecorder()

		deps.upsertOrgSecret(w, req, org.ID)

		assertStatusCode(t, w, http.StatusNoContent)
		var got db.Secret
		err := gdb.Where("name = ? AND repository_id IS NULL", "ORG_TOKEN").First(&got).Error
		if err != nil {
			t.Fatalf("expected org secret to exist: %v", err)
		}
		if got.Value != "keep" {
			t.Fatalf("expected secret value to remain 'keep', got %q", got.Value)
		}
		if got.Visibility != "selected" {
			t.Fatalf("expected visibility selected, got %q", got.Visibility)
		}
		expected := fmt.Sprintf("%d", repoA.ID)
		if got.SelectedRepoIDs != expected {
			t.Fatalf("expected selected_repo_ids %q, got %q", expected, got.SelectedRepoIDs)
		}
	})

	t.Run("missing name returns 422", func(t *testing.T) {
		svc, gdb := setupSecretTestService(t)
		deps := &Deps{Svc: svc}

		org := seedUser(t, gdb, "acme", db.TypeOrganization)

		req := newJSONRequest(t, http.MethodPut, "/orgs/acme/actions/secrets", map[string]any{
			"encrypted_value": mustEncrypt(t, "oops"),
			"key_id":          "1",
		}, map[string]string{})
		w := httptest.NewRecorder()

		deps.upsertOrgSecret(w, req, org.ID)

		assertStatusCode(t, w, http.StatusUnprocessableEntity)
		body := decodeJSONResponse(t, w)
		msg, _ := body["message"].(string)
		if !strings.Contains(msg, "name is required") {
			t.Fatalf("expected validation message to mention name is required, got %v", body["message"])
		}
	})
}
