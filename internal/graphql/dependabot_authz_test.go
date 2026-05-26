package graphql_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

// TestDependabotAlertMutation_RejectsNonWriter verifies that a user without
// write access to the alert's repository cannot dismiss or resolve it, even
// when they can guess the alert's Node ID.
func TestDependabotAlertMutation_RejectsNonWriter(t *testing.T) {
	svc, mux, owner, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Owner's private repo + alert.
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: owner.Login, Name: "sec-repo", Private: true})
	require.NoError(t, err)

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

	// Second user has no relationship with owner's repo.
	outsider := db.User{Login: "outsider", Name: "outsider", Type: db.TypeUser}
	svc.DB.Create(&outsider)
	svc.DB.Create(&db.Token{UserID: outsider.ID, Value: "outsider-token"})

	alertNodeID := encodeNodeID("RepositoryVulnerabilityAlert", alert.ID)

	cases := []struct {
		name     string
		mutation string
		vars     map[string]any
	}{
		{
			"dismiss",
			`mutation($input: DismissRepositoryVulnerabilityAlertInput!) {
				dismissRepositoryVulnerabilityAlert(input: $input) {
					repositoryVulnerabilityAlert { state }
				}
			}`,
			map[string]any{"input": map[string]any{
				"repositoryVulnerabilityAlertId": alertNodeID,
				"dismissReason":                  "TOLERABLE_RISK",
			}},
		},
		{
			"resolve",
			`mutation($input: ResolveRepositoryVulnerabilityAlertInput!) {
				resolveRepositoryVulnerabilityAlert(input: $input) {
					repositoryVulnerabilityAlert { state }
				}
			}`,
			map[string]any{"input": map[string]any{
				"repositoryVulnerabilityAlertId": alertNodeID,
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := postGraphQLAsOutsider(t, mux, tc.mutation, tc.vars)

			errs, _ := res["errors"].([]any)
			if len(errs) == 0 {
				t.Fatalf("expected errors from outsider mutation, got %v", res)
			}

			// Alert must remain in its original state in the DB.
			var after db.DependabotAlert
			svc.DB.First(&after, alert.ID)
			if after.State != "open" {
				t.Errorf("outsider mutated alert state to %q, expected %q", after.State, "open")
			}
		})
	}
}

// postGraphQLAsOutsider issues a GraphQL request under the outsider token.
// Unlike doGql() it tolerates an `errors` field instead of failing the test.
func postGraphQLAsOutsider(t *testing.T, mux http.Handler, query string, vars map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{"query": query, "variables": vars}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/graphql", bytes.NewReader(b))
	req.Header.Set("Authorization", "token outsider-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("gql non-200: %d\n%s", w.Code, w.Body.String())
	}
	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return res
}

// encodeNodeID reproduces the "Prefix_N" node-ID scheme parseNodeID decodes.
func encodeNodeID(kind string, id uint) string {
	return kind + "_" + strconv.FormatUint(uint64(id), 10)
}
