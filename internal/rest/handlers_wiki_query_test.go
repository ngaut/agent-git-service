package rest_test

import (
	"context"
	"net/http"
	"testing"

	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

func TestPutWikiPage_QueryBudget(t *testing.T) {
	h := testharness.New(t)
	ctx := service.ContextWithUser(context.Background(), h.User)

	canonicalRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-query-canonical",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("create canonical repo: %v", err)
	}

	redirectedRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       "wiki-query-redirected",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("create redirected repo: %v", err)
	}
	redirectedLookup := h.User.Login + "/wiki-query-legacy"
	if err := h.DB.Create(&db.RepoRedirect{
		OldFullName: redirectedLookup,
		RepoID:      redirectedRepo.ID,
	}).Error; err != nil {
		t.Fatalf("create repo redirect: %v", err)
	}

	agent := db.User{
		Login:    "wiki-query-agent",
		Name:     "Wiki Query Agent",
		Type:     db.TypeUser,
		UserKind: db.UserKindAgent,
	}
	if err := h.DB.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	const agentToken = "wiki-query-agent-token"
	if err := h.DB.Create(&db.Token{UserID: agent.ID, Value: agentToken}).Error; err != nil {
		t.Fatalf("create agent token: %v", err)
	}
	agentRepo, err := h.Svc.CreateRepo(service.ContextWithUser(context.Background(), agent), service.CreateRepoInput{
		OwnerLogin: agent.Login,
		Name:       "wiki-query-agent-owned",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("create agent-owned repo: %v", err)
	}

	logger := newQueryCounterLogger()
	queryDB := h.Svc.DB.Session(&gorm.Session{Logger: logger})
	h.Svc.DB = queryDB
	h.DB = queryDB

	// With projection drains included, the base revision used 36 queries for
	// the canonical lookup and 39 for the redirect. Repository reuse removed
	// the first six/eight; the prepared catalog snapshot removes two more by
	// avoiding repeated page and prefix reads inside the transaction. Inline
	// CAS-ref elision and snapshot-backed inbound resolution remove two more.
	// The single-upsert snapshot now includes prefix state, removing the
	// remaining standalone prefix query. Joined token resolution and
	// identity-only repository loading remove three request lookup queries.
	// Agent-owned repositories avoid the human-only binding backfill and
	// generic permission aggregate as well. Prepared writes now reuse their
	// head token for the transaction CAS and carry projection state into the
	// Git consistency check, removing two more healthy-path queries.
	for _, tc := range []struct {
		name       string
		fullName   string
		token      string
		maxQueries int
	}{
		{name: "canonical", fullName: canonicalRepo.FullName, token: h.Token, maxQueries: 20},
		{name: "redirect", fullName: redirectedLookup, token: h.Token, maxQueries: 21},
		{name: "agent_owner", fullName: agentRepo.FullName, token: agentToken, maxQueries: 18},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger.Reset()
			resp := h.DoRESTJSONWithToken(t, http.MethodPut, "/api/ext/v1/repos/"+tc.fullName+"/wiki/pages/home", tc.token, map[string]any{
				"body":    "# Home\n\nQuery-count regression coverage.\n",
				"message": "create home",
			})
			if resp.Code != http.StatusOK {
				t.Fatalf("PutWikiPage status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
			}

			// The write starts an asynchronous search projection drain. Wait for
			// it so one subtest cannot change this or the next subtest's count.
			h.Svc.Wg.Wait()
			queryCount := logger.Count()
			t.Logf("PutWikiPage(%s) query count: %d", tc.name, queryCount)
			if queryCount > tc.maxQueries {
				t.Fatalf("PutWikiPage(%s) query count = %d, want <= %d", tc.name, queryCount, tc.maxQueries)
			}
		})
	}
}
