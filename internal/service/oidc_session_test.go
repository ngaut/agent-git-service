package service_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/oidc"
	"github.com/ngaut/agent-git-service/internal/service"
)

// fakeOIDCProvider implements service.OIDCProvider for testing.
type fakeOIDCProvider struct {
	provider  string
	issuer    string
	clientID  string
	idToken   string
	subject   string
	email     string
	name      string
	nickname  string
	preferred string
}

func (f fakeOIDCProvider) Issuer() string   { return f.issuer }
func (f fakeOIDCProvider) ClientID() string { return f.clientID }
func (f fakeOIDCProvider) Scopes() string   { return "openid profile email" }
func (f fakeOIDCProvider) Provider() string {
	if f.provider != "" {
		return f.provider
	}
	return "test-oidc"
}

func (f fakeOIDCProvider) RequestDeviceCode(ctx context.Context, scopes string) (oidc.DeviceCode, error) {
	return oidc.DeviceCode{
		DeviceCode:              "device-code-123",
		UserCode:                "USER-123",
		VerificationURI:         "https://example.invalid/activate",
		VerificationURIComplete: "https://example.invalid/activate?code=USER-123",
		ExpiresIn:               900,
		Interval:                5,
	}, nil
}

func (f fakeOIDCProvider) ExchangeDeviceCode(ctx context.Context, deviceCode string) (oidc.Token, error) {
	return oidc.Token{IDToken: f.idToken}, nil
}

func (f fakeOIDCProvider) VerifyIDToken(ctx context.Context, idToken string) (oidc.IDTokenClaims, error) {
	// For testing, skip signature verification.
	return oidc.DecodeIDTokenClaims(idToken)
}

// mustJWT creates a fake JWT token for testing (no signature verification in tests).
func mustJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	rawClaims, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(rawClaims)
	return header + "." + payload + ".sig"
}

// TestOIDCLogin_NewUser tests that OIDCLogin with a new user creates the user,
// links identity, and returns a token.
func TestOIDCLogin_NewUser(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	// Setup mock OIDC with a new user profile
	claims := map[string]any{
		"sub":                "oidc|123456",
		"email":              "newuser@example.com",
		"email_verified":     true,
		"name":               "New User",
		"nickname":           "newbie",
		"preferred_username": "newuser",
	}
	idToken := mustJWT(t, claims)

	svc.OIDC = fakeOIDCProvider{
		issuer:    "https://example.oidc.com/",
		clientID:  "test-client-id",
		idToken:   idToken,
		subject:   "oidc|123456",
		email:     "newuser@example.com",
		name:      "New User",
		nickname:  "newbie",
		preferred: "newuser",
	}

	result, err := svc.OIDCLogin(context.Background(), "device-code-123")
	if err != nil {
		t.Fatalf("OIDCLogin failed: %v", err)
	}

	// Verify result contains expected data
	if result.Token == "" {
		t.Fatal("expected token to be returned")
	}
	if result.UserID == 0 {
		t.Fatal("expected UserID to be set")
	}
	if result.Login == "" {
		t.Fatal("expected Login to be set")
	}

	// Verify user was created in DB
	var user db.User
	if err := svc.DB.First(&user, result.UserID).Error; err != nil {
		t.Fatalf("failed to load user from DB: %v", err)
	}
	if user.Login != result.Login {
		t.Errorf("expected login %q, got %q", result.Login, user.Login)
	}
	if user.Email != "newuser@example.com" {
		t.Errorf("expected email newuser@example.com, got %q", user.Email)
	}
	if user.Name != "New User" {
		t.Errorf("expected name 'New User', got %q", user.Name)
	}
	if user.UserKind != db.UserKindHuman {
		t.Errorf("expected user kind %q, got %q", db.UserKindHuman, user.UserKind)
	}

	// Verify identity was linked
	var identity db.UserIdentity
	if err := svc.DB.First(&identity, "user_id = ? AND provider = ? AND subject = ?", result.UserID, "test-oidc", "oidc|123456").Error; err != nil {
		t.Fatalf("failed to load user identity from DB: %v", err)
	}
	if identity.UserID != result.UserID {
		t.Errorf("expected identity UserID %d, got %d", result.UserID, identity.UserID)
	}

	// Verify token was created
	var token db.Token
	if err := svc.DB.First(&token, "value = ?", result.Token).Error; err != nil {
		t.Fatalf("failed to load token from DB: %v", err)
	}
	if token.UserID != result.UserID {
		t.Errorf("expected token UserID %d, got %d", result.UserID, token.UserID)
	}
}

// TestOIDCLogin_ExistingUser tests that OIDCLogin with an existing user
// returns the same user with a new token.
func TestOIDCLogin_ExistingUser(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	// Create an existing user with linked identity
	existingUser := db.User{
		Login: "existinguser",
		Email: "existing@example.com",
		Name:  "Existing User",
		Type:  db.TypeUser,
	}
	if err := svc.DB.Create(&existingUser).Error; err != nil {
		t.Fatalf("failed to create existing user: %v", err)
	}

	existingIdentity := db.UserIdentity{
		UserID:   existingUser.ID,
		Provider: "test-oidc",
		Subject:  "oidc|existing-sub",
	}
	if err := svc.DB.Create(&existingIdentity).Error; err != nil {
		t.Fatalf("failed to create existing identity: %v", err)
	}

	// Create an old token (should not be revoked by new logins).
	oldToken := db.Token{
		UserID: existingUser.ID,
		Value:  "old-token-value",
	}
	if err := svc.DB.Create(&oldToken).Error; err != nil {
		t.Fatalf("failed to create old token: %v", err)
	}

	// Setup mock OIDC with the same subject
	claims := map[string]any{
		"sub":                "oidc|existing-sub",
		"email":              "updated@example.com",
		"email_verified":     true,
		"name":               "Updated Name",
		"nickname":           "updatednick",
		"preferred_username": "updateduser",
	}
	idToken := mustJWT(t, claims)

	svc.OIDC = fakeOIDCProvider{
		issuer:    "https://example.oidc.com/",
		clientID:  "test-client-id",
		idToken:   idToken,
		subject:   "oidc|existing-sub",
		email:     "updated@example.com",
		name:      "Updated Name",
		nickname:  "updatednick",
		preferred: "updateduser",
	}

	result, err := svc.OIDCLogin(context.Background(), "device-code-123")
	if err != nil {
		t.Fatalf("OIDCLogin failed: %v", err)
	}

	// Verify result returns the same user
	if result.UserID != existingUser.ID {
		t.Errorf("expected UserID %d (existing user), got %d", existingUser.ID, result.UserID)
	}
	if result.Login != existingUser.Login {
		t.Errorf("expected login %q (existing user), got %q", existingUser.Login, result.Login)
	}

	// Verify new token was created
	if result.Token == "" {
		t.Fatal("expected new token to be returned")
	}
	if result.Token == "old-token-value" {
		t.Error("expected new token, got old token")
	}

	// Verify old token was preserved
	var oldTokenCount int64
	if err := svc.DB.Model(&db.Token{}).Where("value = ?", "old-token-value").Count(&oldTokenCount).Error; err != nil {
		t.Fatalf("failed to count old tokens: %v", err)
	}
	if oldTokenCount != 1 {
		t.Errorf("expected old token to be preserved, got count=%d", oldTokenCount)
	}

	// Verify new token exists
	var newToken db.Token
	if err := svc.DB.First(&newToken, "value = ?", result.Token).Error; err != nil {
		t.Fatalf("failed to load new token from DB: %v", err)
	}
	if newToken.UserID != existingUser.ID {
		t.Errorf("expected new token UserID %d, got %d", existingUser.ID, newToken.UserID)
	}

	// Verify user profile was updated
	var updatedUser db.User
	if err := svc.DB.First(&updatedUser, existingUser.ID).Error; err != nil {
		t.Fatalf("failed to load updated user: %v", err)
	}
	if updatedUser.Email != "updated@example.com" {
		t.Errorf("expected updated email 'updated@example.com', got %q", updatedUser.Email)
	}
	if updatedUser.Name != "Updated Name" {
		t.Errorf("expected updated name 'Updated Name', got %q", updatedUser.Name)
	}
}

// TestOIDCLogin_DisplayNameFallback tests the DisplayName fallback logic.
func TestOIDCLogin_DisplayNameFallback(t *testing.T) {
	tests := []struct {
		name         string
		claims       map[string]any
		expectedName string
	}{
		{
			name: "uses name when available",
			claims: map[string]any{
				"sub":      "oidc|1",
				"email":    "user@example.com",
				"name":     "Full Name",
				"nickname": "nick",
			},
			expectedName: "Full Name",
		},
		{
			name: "falls back to nickname when name empty",
			claims: map[string]any{
				"sub":      "oidc|2",
				"email":    "user@example.com",
				"name":     "",
				"nickname": "nick",
			},
			expectedName: "nick",
		},
		{
			name: "falls back to login when name and nickname empty",
			claims: map[string]any{
				"sub":      "oidc|3",
				"email":    "user@example.com",
				"name":     "",
				"nickname": "",
			},
			expectedName: "user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, cleanup := setupTestService(t)
			defer cleanup()

			idToken := mustJWT(t, tt.claims)
			svc.OIDC = fakeOIDCProvider{
				issuer:   "https://example.oidc.com/",
				clientID: "test-client-id",
				idToken:  idToken,
			}

			result, err := svc.OIDCLogin(context.Background(), "device-code-123")
			if err != nil {
				t.Fatalf("OIDCLogin failed: %v", err)
			}

			var user db.User
			if err := svc.DB.First(&user, result.UserID).Error; err != nil {
				t.Fatalf("failed to load user: %v", err)
			}

			if user.Name != tt.expectedName {
				t.Errorf("expected name %q, got %q", tt.expectedName, user.Name)
			}
		})
	}
}

// TestOIDCProfile_DisplayName tests the OIDCProfile.DisplayName method directly.
func TestOIDCProfile_DisplayName(t *testing.T) {
	tests := []struct {
		name     string
		profile  service.OIDCProfile
		fallback string
		expected string
	}{
		{
			name: "returns name when available",
			profile: service.OIDCProfile{
				Name:     "Full Name",
				Nickname: "nick",
			},
			fallback: "fallback",
			expected: "Full Name",
		},
		{
			name: "returns nickname when name empty",
			profile: service.OIDCProfile{
				Name:     "",
				Nickname: "nick",
			},
			fallback: "fallback",
			expected: "nick",
		},
		{
			name: "returns fallback when name and nickname empty",
			profile: service.OIDCProfile{
				Name:     "",
				Nickname: "",
			},
			fallback: "fallback",
			expected: "fallback",
		},
		{
			name: "trims whitespace",
			profile: service.OIDCProfile{
				Name:     "  ",
				Nickname: "  nick  ",
			},
			fallback: "fallback",
			expected: "nick",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.profile.DisplayName(tt.fallback)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestOIDCLogin_DoesNotCapExistingTokens(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	// Existing user + identity.
	u := db.User{Login: "existinguser", Email: "existing@example.com", Name: "Existing User", Type: db.TypeUser}
	if err := svc.DB.Create(&u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := svc.DB.Create(&db.UserIdentity{UserID: u.ID, Provider: "test-oidc", Subject: "oidc|existing-sub"}).Error; err != nil {
		t.Fatalf("create identity: %v", err)
	}

	// Seed 25 tokens with ascending recency (tok-00 oldest ... tok-24 newest).
	now := time.Now().UTC()
	for i := 0; i < 25; i++ {
		usedAt := now.Add(-time.Duration(25-i) * time.Hour)
		val := fmt.Sprintf("tok-%02d", i)
		if err := svc.DB.Create(&db.Token{UserID: u.ID, Value: val, LastUsedAt: &usedAt}).Error; err != nil {
			t.Fatalf("create token %s: %v", val, err)
		}
	}

	// OIDC login issues one new token and no longer enforces a per-user token cap.
	claims := map[string]any{
		"sub":   "oidc|existing-sub",
		"email": "updated@example.com",
		"name":  "Updated Name",
	}
	svc.OIDC = fakeOIDCProvider{
		issuer:   "https://example.oidc.com/",
		clientID: "test-client-id",
		idToken:  mustJWT(t, claims),
	}

	if _, err := svc.OIDCLogin(context.Background(), "device-code-123"); err != nil {
		t.Fatalf("OIDCLogin: %v", err)
	}

	var count int64
	if err := svc.DB.Model(&db.Token{}).Where("user_id = ?", u.ID).Count(&count).Error; err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if count != 26 {
		t.Fatalf("expected 26 tokens after login, got %d", count)
	}

	// Existing tokens should be preserved.
	for i := 0; i < 6; i++ {
		val := fmt.Sprintf("tok-%02d", i)
		var n int64
		if err := svc.DB.Model(&db.Token{}).Where("user_id = ? AND value = ?", u.ID, val).Count(&n).Error; err != nil {
			t.Fatalf("count %s: %v", val, err)
		}
		if n != 1 {
			t.Fatalf("expected %s to be preserved, count=%d", val, n)
		}
	}
}

// TestOIDCLoginWithIDToken tests the OIDCLoginWithIDToken service method.
func TestOIDCLoginWithIDToken(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	t.Run("EmptyIDToken", func(t *testing.T) {
		_, err := svc.OIDCLoginWithIDToken(context.Background(), "")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
	})

	t.Run("WhitespaceIDToken", func(t *testing.T) {
		_, err := svc.OIDCLoginWithIDToken(context.Background(), "   ")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
	})

	t.Run("InvalidIDToken", func(t *testing.T) {
		svc.OIDC = fakeOIDCProvider{
			issuer:   "https://example.oidc.com/",
			clientID: "test-client-id",
		}
		_, err := svc.OIDCLoginWithIDToken(context.Background(), "invalid-token")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
	})

	t.Run("NewUser", func(t *testing.T) {
		claims := map[string]any{
			"sub":                "oidc|newuser123",
			"email":              "newuser@example.com",
			"email_verified":     true,
			"name":               "New User",
			"nickname":           "newbie",
			"preferred_username": "newuser",
		}
		idToken := mustJWT(t, claims)

		svc.OIDC = fakeOIDCProvider{
			issuer:   "https://example.oidc.com/",
			clientID: "test-client-id",
			idToken:  idToken,
		}

		result, err := svc.OIDCLoginWithIDToken(context.Background(), idToken)
		if err != nil {
			t.Fatalf("OIDCLoginWithIDToken failed: %v", err)
		}
		if result.Token == "" {
			t.Fatal("expected token to be returned")
		}
		if result.UserID == 0 {
			t.Fatal("expected user ID to be returned")
		}
		if result.Login != "newuser" {
			t.Fatalf("expected login 'newuser', got %q", result.Login)
		}
	})

	t.Run("ExistingUser", func(t *testing.T) {
		// Create existing user with identity link
		u := db.User{Login: "existinguser", Name: "Existing User", Type: db.TypeUser}
		if err := svc.DB.Create(&u).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
		if err := svc.DB.Create(&db.UserIdentity{UserID: u.ID, Provider: "test-oidc", Subject: "oidc|existing-sub"}).Error; err != nil {
			t.Fatalf("create identity: %v", err)
		}

		claims := map[string]any{
			"sub":                "oidc|existing-sub",
			"email":              "existing@example.com",
			"email_verified":     true,
			"name":               "Updated Name",
			"nickname":           "existing",
			"preferred_username": "existinguser",
		}
		idToken := mustJWT(t, claims)

		svc.OIDC = fakeOIDCProvider{
			issuer:   "https://example.oidc.com/",
			clientID: "test-client-id",
			idToken:  idToken,
		}

		result, err := svc.OIDCLoginWithIDToken(context.Background(), idToken)
		if err != nil {
			t.Fatalf("OIDCLoginWithIDToken failed: %v", err)
		}
		if result.UserID != u.ID {
			t.Fatalf("expected user ID %d, got %d", u.ID, result.UserID)
		}
	})
}

// TestRequestOIDCDeviceCode tests the RequestOIDCDeviceCode service method.
func TestRequestOIDCDeviceCode(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	t.Run("OIDCNotConfigured", func(t *testing.T) {
		svc.OIDC = nil
		svc.OIDC = nil
		_, err := svc.RequestOIDCDeviceCode(context.Background())
		if err == nil || err.Error() != "oidc not configured" {
			t.Fatalf("expected 'oidc not configured' error, got %v", err)
		}
	})

	t.Run("Success", func(t *testing.T) {
		svc.OIDC = fakeOIDCProvider{
			issuer:   "https://example.oidc.com/",
			clientID: "test-client-id",
		}

		dc, err := svc.RequestOIDCDeviceCode(context.Background())
		if err != nil {
			t.Fatalf("RequestOIDCDeviceCode failed: %v", err)
		}
		if dc.DeviceCode == "" {
			t.Fatal("expected device code to be returned")
		}
		if dc.UserCode == "" {
			t.Fatal("expected user code to be returned")
		}
		if dc.VerificationURI == "" {
			t.Fatal("expected verification URI to be returned")
		}
	})
}

// TestExchangeOIDCDeviceCode tests the ExchangeOIDCDeviceCode service method.
func TestExchangeOIDCDeviceCode(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	t.Run("OIDCNotConfigured", func(t *testing.T) {
		svc.OIDC = nil
		svc.OIDC = nil
		_, err := svc.ExchangeOIDCDeviceCode(context.Background(), "device-code")
		if err == nil || err.Error() != "oidc not configured" {
			t.Fatalf("expected 'oidc not configured' error, got %v", err)
		}
	})

	t.Run("EmptyDeviceCode", func(t *testing.T) {
		svc.OIDC = fakeOIDCProvider{
			issuer:   "https://example.oidc.com/",
			clientID: "test-client-id",
		}
		_, err := svc.ExchangeOIDCDeviceCode(context.Background(), "")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
	})

	t.Run("WhitespaceDeviceCode", func(t *testing.T) {
		svc.OIDC = fakeOIDCProvider{
			issuer:   "https://example.oidc.com/",
			clientID: "test-client-id",
		}
		_, err := svc.ExchangeOIDCDeviceCode(context.Background(), "   ")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
	})

	t.Run("AuthorizationPending", func(t *testing.T) {
		svc.OIDC = fakeOIDCProviderWithError{
			exchangeErr: oidc.OAuthError{Code: "authorization_pending"},
		}
		_, err := svc.ExchangeOIDCDeviceCode(context.Background(), "device-code")
		if err == nil || err.Error() != "oidc authorization pending" {
			t.Fatalf("expected 'oidc authorization pending' error, got %v", err)
		}
	})

	t.Run("SlowDown", func(t *testing.T) {
		svc.OIDC = fakeOIDCProviderWithError{
			exchangeErr: oidc.OAuthError{Code: "slow_down"},
		}
		_, err := svc.ExchangeOIDCDeviceCode(context.Background(), "device-code")
		if err == nil || err.Error() != "oidc slow down" {
			t.Fatalf("expected 'oidc slow down' error, got %v", err)
		}
	})

	t.Run("ExpiredToken", func(t *testing.T) {
		svc.OIDC = fakeOIDCProviderWithError{
			exchangeErr: oidc.OAuthError{Code: "expired_token"},
		}
		_, err := svc.ExchangeOIDCDeviceCode(context.Background(), "device-code")
		if err == nil || err.Error() != "oidc device code expired" {
			t.Fatalf("expected 'oidc device code expired' error, got %v", err)
		}
	})

	t.Run("AccessDenied", func(t *testing.T) {
		svc.OIDC = fakeOIDCProviderWithError{
			exchangeErr: oidc.OAuthError{Code: "access_denied"},
		}
		_, err := svc.ExchangeOIDCDeviceCode(context.Background(), "device-code")
		if err == nil || err.Error() != "oidc access denied" {
			t.Fatalf("expected 'oidc access denied' error, got %v", err)
		}
	})

	t.Run("UnknownOAuthError", func(t *testing.T) {
		svc.OIDC = fakeOIDCProviderWithError{
			exchangeErr: oidc.OAuthError{Code: "unknown_error"},
		}
		_, err := svc.ExchangeOIDCDeviceCode(context.Background(), "device-code")
		if err == nil {
			t.Fatal("expected error for unknown OAuth error")
		}
	})

	t.Run("Success", func(t *testing.T) {
		claims := map[string]any{
			"sub":                "oidc|user123",
			"email":              "user@example.com",
			"email_verified":     true,
			"name":               "Test User",
			"nickname":           "test",
			"preferred_username": "testuser",
		}
		idToken := mustJWT(t, claims)

		svc.OIDC = fakeOIDCProvider{
			issuer:   "https://example.oidc.com/",
			clientID: "test-client-id",
			idToken:  idToken,
		}

		profile, err := svc.ExchangeOIDCDeviceCode(context.Background(), "device-code")
		if err != nil {
			t.Fatalf("ExchangeOIDCDeviceCode failed: %v", err)
		}
		if profile.Subject != "oidc|user123" {
			t.Fatalf("expected subject 'oidc|user123', got %q", profile.Subject)
		}
		if profile.Email != "user@example.com" {
			t.Fatalf("expected email 'user@example.com', got %q", profile.Email)
		}
	})
}

// fakeOIDCProviderWithError implements OIDCDeviceFlow for error testing.
type fakeOIDCProviderWithError struct {
	exchangeErr error
}

func (f fakeOIDCProviderWithError) Issuer() string   { return "https://example.oidc.com/" }
func (f fakeOIDCProviderWithError) ClientID() string { return "test-client-id" }
func (f fakeOIDCProviderWithError) Provider() string { return "test-oidc" }
func (f fakeOIDCProviderWithError) Scopes() string   { return "openid profile email" }
func (f fakeOIDCProviderWithError) RequestDeviceCode(ctx context.Context, scopes string) (oidc.DeviceCode, error) {
	return oidc.DeviceCode{DeviceCode: "device-code-123"}, nil
}
func (f fakeOIDCProviderWithError) ExchangeDeviceCode(ctx context.Context, deviceCode string) (oidc.Token, error) {
	return oidc.Token{}, f.exchangeErr
}
func (f fakeOIDCProviderWithError) VerifyIDToken(ctx context.Context, idToken string) (oidc.IDTokenClaims, error) {
	return oidc.IDTokenClaims{}, nil
}
