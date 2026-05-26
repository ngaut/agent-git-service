package graphql_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stretchr/testify/require"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/githttp"
	"github.com/ngaut/agent-git-service/internal/graphql"
	"github.com/ngaut/agent-git-service/internal/oauth"
	"github.com/ngaut/agent-git-service/internal/rest"
	"github.com/ngaut/agent-git-service/internal/router"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

// setupTestEnvironment builds the full router mux wired to a freshly-seeded
// "tester" user with a known auth token, for GraphQL integration tests that
// want to drive the real HTTP dispatch path. The DB+Git+Service bootstrap
// comes from testharness.NewService; the handler wiring stays here because
// the caller assertions depend on this specific user login.
func setupTestEnvironment(t *testing.T) (*service.Service, http.Handler, db.User, func()) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})

	u := db.User{Login: "tester", Name: "tester", Type: db.TypeUser}
	if err := svc.DB.Create(&u).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := svc.DB.Create(&db.Token{UserID: u.ID, Value: "test-token"}).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}

	graphqlSrv := graphql.NewServer(svc)
	restDeps := &rest.Deps{Svc: svc}
	gitHandler := &githttp.Handler{Svc: svc}
	oauthHandler := &oauth.Handler{Svc: svc}

	mux := router.RegisterRoutes(chi.NewRouter(), restDeps, gitHandler, graphqlSrv, oauthHandler, nil, "/api/v3", "http://console.localhost")

	return svc, mux, u, cleanup
}

func doGql(t *testing.T, mux http.Handler, query string, vars ...map[string]any) map[string]any {
	reqBody := map[string]any{"query": query}
	if len(vars) > 0 && vars[0] != nil {
		reqBody["variables"] = vars[0]
	}
	b, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/graphql", bytes.NewReader(b))
	req.Header.Set("Authorization", "token test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("gql non-200: %d\n%s", w.Code, w.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("gql json decode: %v", err)
	}
	if errs, ok := res["errors"]; ok {
		eb, _ := json.MarshalIndent(errs, "", "  ")
		t.Fatalf("gql errors: %s", string(eb))
	}
	return res["data"].(map[string]any)
}

func TestDependabotVulnerabilityAlerts(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// 1. Setup Repo
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: u.Login, Name: "sec-repo"})
	require.NoError(t, err, "CreateRepo must succeed")
	require.NotZero(t, repo.ID, "created repo must have non-zero ID")

	// 2. Seed Alert
	alert := db.DependabotAlert{
		RepositoryID:              repo.ID,
		Number:                    1,
		State:                     "open",
		DependencyJSON:            `{"package":{"name":"lodash"}}`,
		SecurityVulnerabilityJSON: `{"severity":"HIGH"}`,
		SecurityAdvisoryJSON:      `{"summary":"Prototype Pollution"}`,
		CreatedAt:                 time.Now(),
		UpdatedAt:                 time.Now(),
	}
	svc.DB.WithContext(ctx).Create(&alert)

	// --- QUERY TEST ---
	q := `
	query($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) {
			vulnerabilityAlerts(first: 10) {
				nodes {
					id
					number
					state
					vulnerableManifestFilename
				}
			}
		}
	}
	`
	data := doGql(t, mux, q, map[string]any{"owner": "tester", "name": "sec-repo"})
	repAny, ok := data["repository"]
	if !ok || repAny == nil {
		b, _ := json.MarshalIndent(data, "", "  ")
		t.Fatalf("repository missing in response: %s", string(b))
	}
	rep := repAny.(map[string]any)

	alertsAny, ok := rep["vulnerabilityAlerts"]
	if !ok || alertsAny == nil {
		b, _ := json.MarshalIndent(data, "", "  ")
		t.Fatalf("vulnerabilityAlerts missing in response: %s", string(b))
	}
	alerts := alertsAny.(map[string]any)
	nodes := alerts["nodes"].([]any)

	if len(nodes) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(nodes))
	}

	n := nodes[0].(map[string]any)
	alertID := n["id"].(string)

	if n["state"] != "OPEN" {
		t.Errorf("expected state OPEN, got %v", n["state"])
	}
	if n["vulnerableManifestFilename"] != "lodash" {
		t.Errorf("expected filename 'lodash', got %v", n["vulnerableManifestFilename"])
	}

	// --- MUTATION TEST 1: DISMISS ---
	mDismiss := `
	mutation($input: DismissRepositoryVulnerabilityAlertInput!) {
		dismissRepositoryVulnerabilityAlert(input: $input) {
			repositoryVulnerabilityAlert {
				state
				dismissedReason
			}
		}
	}
	`
	mData1 := doGql(t, mux, mDismiss, map[string]any{
		"input": map[string]any{
			"repositoryVulnerabilityAlertId": alertID,
			"dismissReason":                  "TOLERABLE_RISK",
		},
	})
	mA1 := mData1["dismissRepositoryVulnerabilityAlert"].(map[string]any)["repositoryVulnerabilityAlert"].(map[string]any)

	if mA1["state"] != "DISMISSED" {
		t.Errorf("expected DISMISSED, got %v", mA1["state"])
	}
	if mA1["dismissedReason"] != "TOLERABLE_RISK" {
		t.Errorf("expected reason TOLERABLE_RISK, got %v", mA1["dismissedReason"])
	}

	// --- MUTATION TEST 2: RESOLVE ---
	mResolve := `
	mutation($input: ResolveRepositoryVulnerabilityAlertInput!) {
		resolveRepositoryVulnerabilityAlert(input: $input) {
			repositoryVulnerabilityAlert {
				state
				fixedAt
			}
		}
	}
	`
	mData2 := doGql(t, mux, mResolve, map[string]any{
		"input": map[string]any{
			"repositoryVulnerabilityAlertId": alertID,
		},
	})
	mA2 := mData2["resolveRepositoryVulnerabilityAlert"].(map[string]any)["repositoryVulnerabilityAlert"].(map[string]any)

	if mA2["state"] != "FIXED" {
		t.Errorf("expected FIXED, got %v", mA2["state"])
	}
	if mA2["fixedAt"] == nil || mA2["fixedAt"] == "" {
		t.Errorf("expected fixedAt to be populated")
	}
}

// TestSetupEnvironment_NoCrossContamination proves that two sequential calls to
// setupTestEnvironment do not share database state. This is the regression test
// for the DSN isolation fix (each invocation gets a unique DSN via atomic counter).
func TestSetupEnvironment_NoCrossContamination(t *testing.T) {
	// First environment: create a user and a repo.
	svc1, _, _, cleanup1 := setupTestEnvironment(t)
	defer cleanup1()
	ctx := context.Background()

	repo1, err := svc1.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "tester", Name: "leak-check"})
	require.NoError(t, err, "CreateRepo must succeed")
	require.NotZero(t, repo1.ID, "created repo must have non-zero ID")

	// Second environment: must start with a clean database.
	svc2, _, _, cleanup2 := setupTestEnvironment(t)
	defer cleanup2()

	var count int64
	svc2.DB.Model(&db.Repository{}).Where("full_name = ?", "tester/leak-check").Count(&count)
	if count != 0 {
		t.Fatalf("second environment leaked %d repo(s) from first; DSN isolation broken", count)
	}
}
