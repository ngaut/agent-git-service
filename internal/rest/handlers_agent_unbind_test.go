package rest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

func TestUnbindAgentEndpointPreservesAgentCredentialAndRevokesSwitchSessions(t *testing.T) {
	h := testharness.New(t)
	human, humanToken := seedHarnessUser(t, h, "unbind-endpoint-human", false)

	registered, err := h.Svc.RegisterAgent(context.Background(), "unbind-endpoint-agent", "memory")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	var agent db.User
	if err := h.Svc.DB.First(&agent, "login = ?", registered.Login).Error; err != nil {
		t.Fatalf("load registered agent: %v", err)
	}
	if err := h.Svc.DB.Create(&db.AgentBinding{HumanUserID: human.ID, AgentUserID: agent.ID}).Error; err != nil {
		t.Fatalf("create binding: %v", err)
	}
	switchSession, err := h.Svc.CreateAgentSwitchSession(context.Background(), human.ID, agent.Login)
	if err != nil {
		t.Fatalf("create switch session: %v", err)
	}

	w := h.DoRESTWithToken(t, http.MethodDelete, "/api/ext/v1/agent-bindings/"+agent.Login, humanToken)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE binding expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		AgentLogin            string `json:"agent_login"`
		RevokedSwitchSessions int64  `json:"revoked_switch_sessions"`
	}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode unbind response: %v", err)
	}
	if result.AgentLogin != agent.Login || result.RevokedSwitchSessions != 1 {
		t.Fatalf("unbind response = %#v, want %q and one revoked switch session", result, agent.Login)
	}

	w = h.DoRESTWithToken(t, http.MethodGet, "/api/v3/user", registered.Token)
	if w.Code != http.StatusOK {
		t.Fatalf("agent token should remain valid, got %d: %s", w.Code, w.Body.String())
	}
	w = h.DoRESTWithToken(t, http.MethodGet, "/api/v3/user", switchSession.Token.Value)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked switch session expected 401, got %d: %s", w.Code, w.Body.String())
	}
	w = h.DoRESTJSONWithToken(t, http.MethodPost, "/api/ext/v1/agent-bindings/"+agent.Login+"/switch-session", humanToken, map[string]any{})
	if w.Code != http.StatusNotFound {
		t.Fatalf("human should not switch after unbind, got %d: %s", w.Code, w.Body.String())
	}

	var bindingCount int64
	if err := h.Svc.DB.Model(&db.AgentBinding{}).Where("human_user_id = ? AND agent_user_id = ?", human.ID, agent.ID).Count(&bindingCount).Error; err != nil {
		t.Fatalf("count bindings: %v", err)
	}
	if bindingCount != 0 {
		t.Fatalf("binding count = %d, want 0", bindingCount)
	}
}
