package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"gorm.io/gorm"
)

func TestResolveEmbeddedIdentity_RejectsExistingHumanLoginCollision(t *testing.T) {
	t.Parallel()

	svc, cleanup := setupTestService(t)
	defer cleanup()

	existing := db.User{
		Login:    "gateway-user",
		Name:     "Existing User",
		Email:    "existing@example.com",
		Type:     db.TypeUser,
		UserKind: db.UserKindHuman,
	}
	if err := svc.DB.Create(&existing).Error; err != nil {
		t.Fatalf("create existing user: %v", err)
	}

	resolved, err := svc.ResolveEmbeddedIdentity(context.Background(), service.EmbeddedIdentity{
		Provider: "meshx",
		Subject:  "subject-1",
		Login:    "gateway-user",
		Name:     "Gateway User",
		Email:    "gateway@example.com",
	})
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("ResolveEmbeddedIdentity error = %v, want ErrConflict", err)
	}
	if resolved.ID != 0 {
		t.Fatalf("resolved user id = %d, want 0", resolved.ID)
	}

	var identity db.UserIdentity
	if err := svc.DB.First(&identity, "provider = ? AND subject = ?", "meshx", "subject-1").Error; !errors.Is(err, gorm.ErrRecordNotFound) && err != nil {
		t.Fatalf("load linked identity: %v", err)
	}
	if identity.ID != 0 {
		t.Fatalf("linked identity id = %d, want 0", identity.ID)
	}

	var userCount int64
	if err := svc.DB.Model(&db.User{}).Where("login = ?", "gateway-user").Count(&userCount).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if userCount != 1 {
		t.Fatalf("user count = %d, want 1", userCount)
	}
	var reloaded db.User
	if err := svc.DB.First(&reloaded, existing.ID).Error; err != nil {
		t.Fatalf("reload existing user: %v", err)
	}
	if reloaded.Name != existing.Name {
		t.Fatalf("reloaded name = %q, want %q", reloaded.Name, existing.Name)
	}
	if reloaded.Email != existing.Email {
		t.Fatalf("reloaded email = %q, want %q", reloaded.Email, existing.Email)
	}
}

func TestResolveEmbeddedIdentity_RejectsOrganizationLoginCollision(t *testing.T) {
	t.Parallel()

	svc, cleanup := setupTestService(t)
	defer cleanup()

	existing := db.User{
		Login:    "shared-login",
		Name:     "Shared Org",
		Type:     db.TypeOrganization,
		UserKind: db.UserKindHuman,
	}
	if err := svc.DB.Create(&existing).Error; err != nil {
		t.Fatalf("create organization: %v", err)
	}

	_, err := svc.ResolveEmbeddedIdentity(context.Background(), service.EmbeddedIdentity{
		Provider: "meshx",
		Subject:  "subject-org",
		Login:    "shared-login",
		Name:     "Gateway User",
	})
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("ResolveEmbeddedIdentity error = %v, want ErrConflict", err)
	}

	var identityCount int64
	if err := svc.DB.Model(&db.UserIdentity{}).Where("provider = ? AND subject = ?", "meshx", "subject-org").Count(&identityCount).Error; err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if identityCount != 0 {
		t.Fatalf("identity count = %d, want 0", identityCount)
	}
}

func TestResolveEmbeddedIdentity_RejectsAgentLoginCollision(t *testing.T) {
	t.Parallel()

	svc, cleanup := setupTestService(t)
	defer cleanup()

	existing := db.User{
		Login:    "shared-agent",
		Name:     "Shared Agent",
		Type:     db.TypeUser,
		UserKind: db.UserKindAgent,
	}
	if err := svc.DB.Create(&existing).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	_, err := svc.ResolveEmbeddedIdentity(context.Background(), service.EmbeddedIdentity{
		Provider: "meshx",
		Subject:  "subject-agent",
		Login:    "shared-agent",
		Name:     "Gateway User",
	})
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("ResolveEmbeddedIdentity error = %v, want ErrConflict", err)
	}

	var identityCount int64
	if err := svc.DB.Model(&db.UserIdentity{}).Where("provider = ? AND subject = ?", "meshx", "subject-agent").Count(&identityCount).Error; err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if identityCount != 0 {
		t.Fatalf("identity count = %d, want 0", identityCount)
	}
}
