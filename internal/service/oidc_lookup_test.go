package service_test

import (
	"context"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
)

func TestLookupOIDCIdentityWithIDToken(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	t.Run("LinkedIdentity", func(t *testing.T) {
		user := db.User{Login: "oidc-user", Name: "OIDC User", Type: db.TypeUser}
		if err := svc.DB.Create(&user).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
		if err := svc.DB.Create(&db.UserIdentity{
			UserID:   user.ID,
			Provider: "test-oidc",
			Subject:  "oidc|linked-subject",
		}).Error; err != nil {
			t.Fatalf("create identity: %v", err)
		}

		idToken := mustJWT(t, map[string]any{
			"sub":                "oidc|linked-subject",
			"email":              "linked@example.com",
			"preferred_username": "oidc-user",
		})
		svc.OIDC = fakeOIDCProvider{
			issuer:   "https://example.oidc.com/",
			clientID: "test-client-id",
			idToken:  idToken,
		}

		result, err := svc.LookupOIDCIdentityWithIDToken(context.Background(), idToken)
		if err != nil {
			t.Fatalf("LookupOIDCIdentityWithIDToken failed: %v", err)
		}
		if !result.Linked {
			t.Fatal("expected linked result")
		}
		if result.User.ID != user.ID {
			t.Fatalf("expected user ID %d, got %d", user.ID, result.User.ID)
		}
	})
}
