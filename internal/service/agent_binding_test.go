package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

func TestConfirmAgentBindingSuccessAndConsumedInviteConflict(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	human := db.User{Login: "binding-human", Name: "binding-human", Type: db.TypeUser, UserKind: db.UserKindHuman}
	agent := db.User{Login: "binding-agent", Name: "binding-agent", Type: db.TypeUser, UserKind: db.UserKindAgent}
	if err := svc.DB.Create(&human).Error; err != nil {
		t.Fatalf("create human: %v", err)
	}
	if err := svc.DB.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	humanCtx := service.ContextWithUser(context.Background(), human)
	agentCtx := service.ContextWithUser(context.Background(), agent)

	invite, err := svc.CreateAgentInvite(humanCtx)
	if err != nil {
		t.Fatalf("CreateAgentInvite: %v", err)
	}

	binding, err := svc.ConfirmAgentBinding(agentCtx, invite.Token)
	if err != nil {
		t.Fatalf("ConfirmAgentBinding: %v", err)
	}
	if binding.HumanUserID != human.ID {
		t.Fatalf("binding.HumanUserID = %d, want %d", binding.HumanUserID, human.ID)
	}
	if binding.AgentUserID != agent.ID {
		t.Fatalf("binding.AgentUserID = %d, want %d", binding.AgentUserID, agent.ID)
	}

	var storedInvite db.AgentInvite
	if err := svc.DB.First(&storedInvite, "id = ?", invite.ID).Error; err != nil {
		t.Fatalf("stored invite: %v", err)
	}
	if storedInvite.ConsumedAt == nil {
		t.Fatal("expected consumed_at to be recorded")
	}
	if storedInvite.ConsumedByAgentUserID == nil || *storedInvite.ConsumedByAgentUserID != agent.ID {
		t.Fatalf("consumed_by_agent_user_id = %v, want %d", storedInvite.ConsumedByAgentUserID, agent.ID)
	}

	_, err = svc.ConfirmAgentBinding(agentCtx, invite.Token)
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("second ConfirmAgentBinding error = %v, want ErrConflict", err)
	}
}

func TestConfirmAgentBindingRejectsHumanToken(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	human := db.User{Login: "human-only", Name: "human-only", Type: db.TypeUser, UserKind: db.UserKindHuman}
	if err := svc.DB.Create(&human).Error; err != nil {
		t.Fatalf("create human: %v", err)
	}

	humanCtx := service.ContextWithUser(context.Background(), human)
	invite, err := svc.CreateAgentInvite(humanCtx)
	if err != nil {
		t.Fatalf("CreateAgentInvite: %v", err)
	}

	_, err = svc.ConfirmAgentBinding(humanCtx, invite.Token)
	if !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("ConfirmAgentBinding error = %v, want ErrForbidden", err)
	}
}

func TestConfirmAgentBindingRejectsInvalidInvite(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	agent := db.User{Login: "invalid-invite-agent", Name: "invalid-invite-agent", Type: db.TypeUser, UserKind: db.UserKindAgent}
	if err := svc.DB.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	agentCtx := service.ContextWithUser(context.Background(), agent)
	_, err := svc.ConfirmAgentBinding(agentCtx, "missing-invite")
	if !errors.Is(err, service.ErrValidation) {
		t.Fatalf("ConfirmAgentBinding error = %v, want ErrValidation", err)
	}
}

func TestConfirmAgentBindingRejectsExpiredInvite(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	human := db.User{Login: "expired-human", Name: "expired-human", Type: db.TypeUser, UserKind: db.UserKindHuman}
	agent := db.User{Login: "expired-agent", Name: "expired-agent", Type: db.TypeUser, UserKind: db.UserKindAgent}
	if err := svc.DB.Create(&human).Error; err != nil {
		t.Fatalf("create human: %v", err)
	}
	if err := svc.DB.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	humanCtx := service.ContextWithUser(context.Background(), human)
	agentCtx := service.ContextWithUser(context.Background(), agent)
	invite, err := svc.CreateAgentInvite(humanCtx)
	if err != nil {
		t.Fatalf("CreateAgentInvite: %v", err)
	}

	expiredAt := time.Now().UTC().Add(-time.Minute)
	if err := svc.DB.Model(&db.AgentInvite{}).Where("id = ?", invite.ID).Update("expires_at", expiredAt).Error; err != nil {
		t.Fatalf("expire invite: %v", err)
	}

	_, err = svc.ConfirmAgentBinding(agentCtx, invite.Token)
	if !errors.Is(err, service.ErrValidation) {
		t.Fatalf("ConfirmAgentBinding error = %v, want ErrValidation", err)
	}
}
