package rest_test

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

func TestSwitchAgentSessionReturnsFreshTemporaryTokenWithoutRevokingExistingToken(t *testing.T) {
	h := testharness.New(t)

	agent := db.User{Login: "switch-rest-agent", Name: "switch-rest-agent", Type: db.TypeUser, UserKind: db.UserKindAgent}
	if err := h.DB.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := h.DB.Create(&db.AgentBinding{HumanUserID: h.User.ID, AgentUserID: agent.ID}).Error; err != nil {
		t.Fatalf("create binding: %v", err)
	}
	const originalToken = "switch-rest-agent-long-lived-token"
	if err := h.DB.Create(&db.Token{UserID: agent.ID, Name: "agent", Value: originalToken}).Error; err != nil {
		t.Fatalf("create original token: %v", err)
	}

	w := h.DoRESTJSON(t, http.MethodPost, "/api/v3/agent-bindings/"+agent.Login+"/switch-session", map[string]any{})
	assertStatusCode(t, w, http.StatusOK)
	body := testharness.DecodeJSON(t, w)

	if got := body["agent_login"]; got != agent.Login {
		t.Fatalf("agent_login = %v, want %s", got, agent.Login)
	}
	tokenPayload, ok := body["token"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested token payload, got %T", body["token"])
	}
	issuedToken, _ := tokenPayload["token"].(string)
	if issuedToken == "" {
		t.Fatal("expected issued token string")
	}
	if issuedToken == originalToken {
		t.Fatal("expected issued token to differ from original long-lived token")
	}
	userPayload, ok := body["user"].(map[string]any)
	if !ok {
		t.Fatalf("expected user payload, got %T", body["user"])
	}
	if got := userPayload["login"]; got != agent.Login {
		t.Fatalf("user.login = %v, want %s", got, agent.Login)
	}

	w = h.DoRESTWithToken(t, http.MethodGet, "/api/v3/user", originalToken)
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoRESTWithToken(t, http.MethodGet, "/api/v3/user", issuedToken)
	assertStatusCode(t, w, http.StatusOK)
	current := testharness.DecodeJSON(t, w)
	if got := current["login"]; got != agent.Login {
		t.Fatalf("GET /api/v3/user login = %v, want %s", got, agent.Login)
	}
}

func TestRefreshAgentSwitchSessionRotatesSessionTokenAndPreservesLongLivedToken(t *testing.T) {
	h := testharness.New(t)

	agent := db.User{Login: "refresh-rest-agent", Name: "refresh-rest-agent", Type: db.TypeUser, UserKind: db.UserKindAgent}
	if err := h.DB.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := h.DB.Create(&db.AgentBinding{HumanUserID: h.User.ID, AgentUserID: agent.ID}).Error; err != nil {
		t.Fatalf("create binding: %v", err)
	}
	const originalToken = "refresh-rest-agent-long-lived-token"
	if err := h.DB.Create(&db.Token{UserID: agent.ID, Name: "agent", Value: originalToken}).Error; err != nil {
		t.Fatalf("create original token: %v", err)
	}

	w := h.DoRESTJSON(t, http.MethodPost, "/api/v3/agent-bindings/"+agent.Login+"/switch-session", map[string]any{})
	assertStatusCode(t, w, http.StatusOK)
	issued := testharness.DecodeJSON(t, w)
	issuedPayload, ok := issued["token"].(map[string]any)
	if !ok {
		t.Fatalf("expected token payload, got %T", issued["token"])
	}
	issuedToken, _ := issuedPayload["token"].(string)
	if issuedToken == "" {
		t.Fatal("expected issued switch token")
	}

	w = h.DoRESTJSONWithToken(t, http.MethodPost, "/api/v3/agent-bindings/"+agent.Login+"/refresh-session", issuedToken, map[string]any{})
	assertStatusCode(t, w, http.StatusOK)
	refreshed := testharness.DecodeJSON(t, w)
	refreshedPayload, ok := refreshed["token"].(map[string]any)
	if !ok {
		t.Fatalf("expected refreshed token payload, got %T", refreshed["token"])
	}
	refreshedToken, _ := refreshedPayload["token"].(string)
	if refreshedToken == "" {
		t.Fatal("expected refreshed switch token")
	}
	if refreshedToken == issuedToken {
		t.Fatal("expected refreshed token to differ from issued token")
	}

	w = h.DoRESTWithToken(t, http.MethodGet, "/api/v3/user", originalToken)
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoRESTWithToken(t, http.MethodGet, "/api/v3/user", refreshedToken)
	assertStatusCode(t, w, http.StatusOK)

	w = h.DoRESTWithToken(t, http.MethodGet, "/api/v3/user", issuedToken)
	assertStatusCode(t, w, http.StatusUnauthorized)
}

func TestRefreshAgentSwitchSessionAcceptsBasicAuth(t *testing.T) {
	h := testharness.New(t)

	agent := db.User{Login: "refresh-basic-agent", Name: "refresh-basic-agent", Type: db.TypeUser, UserKind: db.UserKindAgent}
	if err := h.DB.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := h.DB.Create(&db.AgentBinding{HumanUserID: h.User.ID, AgentUserID: agent.ID}).Error; err != nil {
		t.Fatalf("create binding: %v", err)
	}
	const originalToken = "refresh-basic-agent-long-lived-token"
	if err := h.DB.Create(&db.Token{UserID: agent.ID, Name: "agent", Value: originalToken}).Error; err != nil {
		t.Fatalf("create original token: %v", err)
	}

	switchResp := h.DoRESTJSON(t, http.MethodPost, "/api/v3/agent-bindings/"+agent.Login+"/switch-session", map[string]any{})
	assertStatusCode(t, switchResp, http.StatusOK)
	issued := testharness.DecodeJSON(t, switchResp)
	issuedPayload, ok := issued["token"].(map[string]any)
	if !ok {
		t.Fatalf("expected token payload, got %T", issued["token"])
	}
	issuedToken, _ := issuedPayload["token"].(string)
	if issuedToken == "" {
		t.Fatal("expected issued switch token")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v3/agent-bindings/"+agent.Login+"/refresh-session", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+issuedToken)))
	resp := httptest.NewRecorder()
	h.Mux.ServeHTTP(resp, req)
	assertStatusCode(t, resp, http.StatusOK)
	refreshed := testharness.DecodeJSON(t, resp)
	refreshedPayload, ok := refreshed["token"].(map[string]any)
	if !ok {
		t.Fatalf("expected refreshed token payload, got %T", refreshed["token"])
	}
	refreshedToken, _ := refreshedPayload["token"].(string)
	if refreshedToken == "" {
		t.Fatal("expected refreshed switch token")
	}
	if refreshedToken == issuedToken {
		t.Fatal("expected refreshed token to differ from issued token")
	}

	resp = h.DoRESTWithToken(t, http.MethodGet, "/api/v3/user", refreshedToken)
	assertStatusCode(t, resp, http.StatusOK)
}
