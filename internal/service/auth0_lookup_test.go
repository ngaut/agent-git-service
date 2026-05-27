package service_test

import (
	"context"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
)

func TestLookupAuth0IdentityWithIDToken(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	t.Run("DoesNotFallbackToOIDC", func(t *testing.T) {
		svc.Auth0 = nil
		svc.OIDC = fakeOIDCProvider{}

		_, err := svc.LookupAuth0IdentityWithIDToken(context.Background(), "id-token")
		if err == nil || err.Error() != "auth0 not configured" {
			t.Fatalf("expected 'auth0 not configured' error, got %v", err)
		}
	})

	t.Run("LinkedIdentity", func(t *testing.T) {
		user := db.User{Login: "auth0-user", Name: "Auth0 User", Type: db.TypeUser}
		if err := svc.DB.Create(&user).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
		if err := svc.DB.Create(&db.UserIdentity{
			UserID:   user.ID,
			Provider: "auth0",
			Subject:  "auth0|linked-subject",
		}).Error; err != nil {
			t.Fatalf("create identity: %v", err)
		}

		idToken := mustJWT(t, map[string]any{
			"sub":                "auth0|linked-subject",
			"email":              "linked@example.com",
			"preferred_username": "auth0-user",
		})
		svc.Auth0 = fakeAuth0DeviceFlow{
			issuer:   "https://example.auth0.com/",
			clientID: "test-client-id",
			idToken:  idToken,
		}

		result, err := svc.LookupAuth0IdentityWithIDToken(context.Background(), idToken)
		if err != nil {
			t.Fatalf("LookupAuth0IdentityWithIDToken failed: %v", err)
		}
		if !result.Linked {
			t.Fatal("expected linked result")
		}
		if result.User.ID != user.ID {
			t.Fatalf("expected user ID %d, got %d", user.ID, result.User.ID)
		}
	})
}
