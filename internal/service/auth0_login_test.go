package service_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/auth0"
	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

// fakeAuth0DeviceFlow implements service.Auth0DeviceFlow for testing.
type fakeAuth0DeviceFlow struct {
	issuer    string
	clientID  string
	idToken   string
	subject   string
	email     string
	name      string
	nickname  string
	preferred string
}

func (f fakeAuth0DeviceFlow) Issuer() string   { return f.issuer }
func (f fakeAuth0DeviceFlow) ClientID() string { return f.clientID }

func (f fakeAuth0DeviceFlow) RequestDeviceCode(ctx context.Context, scopes string) (auth0.DeviceCode, error) {
	return auth0.DeviceCode{
		DeviceCode:              "device-code-123",
		UserCode:                "USER-123",
		VerificationURI:         "https://example.invalid/activate",
		VerificationURIComplete: "https://example.invalid/activate?code=USER-123",
		ExpiresIn:               900,
		Interval:                5,
	}, nil
}

func (f fakeAuth0DeviceFlow) ExchangeDeviceCode(ctx context.Context, deviceCode string) (auth0.Token, error) {
	return auth0.Token{IDToken: f.idToken}, nil
}

func (f fakeAuth0DeviceFlow) VerifyIDToken(ctx context.Context, idToken string) (auth0.IDTokenClaims, error) {
	// For testing, skip signature verification.
	return auth0.DecodeIDTokenClaims(idToken)
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

// TestAuth0Login_NewUser tests that Auth0Login with a new user creates the user,
// links identity, and returns a token.
func TestAuth0Login_NewUser(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	// Setup mock Auth0 with a new user profile
	claims := map[string]any{
		"sub":                "auth0|123456",
		"email":              "newuser@example.com",
		"email_verified":     true,
		"name":               "New User",
		"nickname":           "newbie",
		"preferred_username": "newuser",
	}
	idToken := mustJWT(t, claims)

	svc.Auth0 = fakeAuth0DeviceFlow{
		issuer:    "https://example.auth0.com/",
		clientID:  "test-client-id",
		idToken:   idToken,
		subject:   "auth0|123456",
		email:     "newuser@example.com",
		name:      "New User",
		nickname:  "newbie",
		preferred: "newuser",
	}

	result, err := svc.Auth0Login(context.Background(), "device-code-123")
	if err != nil {
		t.Fatalf("Auth0Login failed: %v", err)
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
	if user.IsAnonymous {
		t.Error("expected user to not be anonymous")
	}

	// Verify identity was linked
	var identity db.UserIdentity
	if err := svc.DB.First(&identity, "user_id = ? AND provider = ? AND subject = ?", result.UserID, "auth0", "auth0|123456").Error; err != nil {
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

// TestAuth0Login_ExistingUser tests that Auth0Login with an existing user
// returns the same user with a new token.
func TestAuth0Login_ExistingUser(t *testing.T) {
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
		Provider: "auth0",
		Subject:  "auth0|existing-sub",
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

	// Setup mock Auth0 with the same subject
	claims := map[string]any{
		"sub":                "auth0|existing-sub",
		"email":              "updated@example.com",
		"email_verified":     true,
		"name":               "Updated Name",
		"nickname":           "updatednick",
		"preferred_username": "updateduser",
	}
	idToken := mustJWT(t, claims)

	svc.Auth0 = fakeAuth0DeviceFlow{
		issuer:    "https://example.auth0.com/",
		clientID:  "test-client-id",
		idToken:   idToken,
		subject:   "auth0|existing-sub",
		email:     "updated@example.com",
		name:      "Updated Name",
		nickname:  "updatednick",
		preferred: "updateduser",
	}

	result, err := svc.Auth0Login(context.Background(), "device-code-123")
	if err != nil {
		t.Fatalf("Auth0Login failed: %v", err)
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

// TestAuth0Login_DisplayNameFallback tests the DisplayName fallback logic.
func TestAuth0Login_DisplayNameFallback(t *testing.T) {
	tests := []struct {
		name         string
		claims       map[string]any
		expectedName string
	}{
		{
			name: "uses name when available",
			claims: map[string]any{
				"sub":      "auth0|1",
				"email":    "user@example.com",
				"name":     "Full Name",
				"nickname": "nick",
			},
			expectedName: "Full Name",
		},
		{
			name: "falls back to nickname when name empty",
			claims: map[string]any{
				"sub":      "auth0|2",
				"email":    "user@example.com",
				"name":     "",
				"nickname": "nick",
			},
			expectedName: "nick",
		},
		{
			name: "falls back to login when name and nickname empty",
			claims: map[string]any{
				"sub":      "auth0|3",
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
			svc.Auth0 = fakeAuth0DeviceFlow{
				issuer:   "https://example.auth0.com/",
				clientID: "test-client-id",
				idToken:  idToken,
			}

			result, err := svc.Auth0Login(context.Background(), "device-code-123")
			if err != nil {
				t.Fatalf("Auth0Login failed: %v", err)
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

// TestAuth0Profile_DisplayName tests the Auth0Profile.DisplayName method directly.
func TestAuth0Profile_DisplayName(t *testing.T) {
	tests := []struct {
		name     string
		profile  service.Auth0Profile
		fallback string
		expected string
	}{
		{
			name: "returns name when available",
			profile: service.Auth0Profile{
				Name:     "Full Name",
				Nickname: "nick",
			},
			fallback: "fallback",
			expected: "Full Name",
		},
		{
			name: "returns nickname when name empty",
			profile: service.Auth0Profile{
				Name:     "",
				Nickname: "nick",
			},
			fallback: "fallback",
			expected: "nick",
		},
		{
			name: "returns fallback when name and nickname empty",
			profile: service.Auth0Profile{
				Name:     "",
				Nickname: "",
			},
			fallback: "fallback",
			expected: "fallback",
		},
		{
			name: "trims whitespace",
			profile: service.Auth0Profile{
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

func TestAuth0Login_DoesNotCapExistingTokens(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	// Existing user + identity.
	u := db.User{Login: "existinguser", Email: "existing@example.com", Name: "Existing User", Type: db.TypeUser}
	if err := svc.DB.Create(&u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := svc.DB.Create(&db.UserIdentity{UserID: u.ID, Provider: "auth0", Subject: "auth0|existing-sub"}).Error; err != nil {
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

	// Auth0 login issues one new token and no longer enforces a per-user token cap.
	claims := map[string]any{
		"sub":   "auth0|existing-sub",
		"email": "updated@example.com",
		"name":  "Updated Name",
	}
	svc.Auth0 = fakeAuth0DeviceFlow{
		issuer:   "https://example.auth0.com/",
		clientID: "test-client-id",
		idToken:  mustJWT(t, claims),
	}

	if _, err := svc.Auth0Login(context.Background(), "device-code-123"); err != nil {
		t.Fatalf("Auth0Login: %v", err)
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

// TestAuth0LoginWithIDToken tests the Auth0LoginWithIDToken service method.
func TestAuth0LoginWithIDToken(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	t.Run("EmptyIDToken", func(t *testing.T) {
		_, err := svc.Auth0LoginWithIDToken(context.Background(), "")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
	})

	t.Run("WhitespaceIDToken", func(t *testing.T) {
		_, err := svc.Auth0LoginWithIDToken(context.Background(), "   ")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
	})

	t.Run("InvalidIDToken", func(t *testing.T) {
		svc.Auth0 = fakeAuth0DeviceFlow{
			issuer:   "https://example.auth0.com/",
			clientID: "test-client-id",
		}
		_, err := svc.Auth0LoginWithIDToken(context.Background(), "invalid-token")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
	})

	t.Run("NewUser", func(t *testing.T) {
		claims := map[string]any{
			"sub":                "auth0|newuser123",
			"email":              "newuser@example.com",
			"email_verified":     true,
			"name":               "New User",
			"nickname":           "newbie",
			"preferred_username": "newuser",
		}
		idToken := mustJWT(t, claims)

		svc.Auth0 = fakeAuth0DeviceFlow{
			issuer:   "https://example.auth0.com/",
			clientID: "test-client-id",
			idToken:  idToken,
		}

		result, err := svc.Auth0LoginWithIDToken(context.Background(), idToken)
		if err != nil {
			t.Fatalf("Auth0LoginWithIDToken failed: %v", err)
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
		if err := svc.DB.Create(&db.UserIdentity{UserID: u.ID, Provider: "auth0", Subject: "auth0|existing-sub"}).Error; err != nil {
			t.Fatalf("create identity: %v", err)
		}

		claims := map[string]any{
			"sub":                "auth0|existing-sub",
			"email":              "existing@example.com",
			"email_verified":     true,
			"name":               "Updated Name",
			"nickname":           "existing",
			"preferred_username": "existinguser",
		}
		idToken := mustJWT(t, claims)

		svc.Auth0 = fakeAuth0DeviceFlow{
			issuer:   "https://example.auth0.com/",
			clientID: "test-client-id",
			idToken:  idToken,
		}

		result, err := svc.Auth0LoginWithIDToken(context.Background(), idToken)
		if err != nil {
			t.Fatalf("Auth0LoginWithIDToken failed: %v", err)
		}
		if result.UserID != u.ID {
			t.Fatalf("expected user ID %d, got %d", u.ID, result.UserID)
		}
	})
}

// TestRequestAuth0DeviceCode tests the RequestAuth0DeviceCode service method.
func TestRequestAuth0DeviceCode(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	t.Run("Auth0NotConfigured", func(t *testing.T) {
		svc.Auth0 = nil
		_, err := svc.RequestAuth0DeviceCode(context.Background())
		if err == nil || err.Error() != "auth0 not configured" {
			t.Fatalf("expected 'auth0 not configured' error, got %v", err)
		}
	})

	t.Run("Success", func(t *testing.T) {
		svc.Auth0 = fakeAuth0DeviceFlow{
			issuer:   "https://example.auth0.com/",
			clientID: "test-client-id",
		}

		dc, err := svc.RequestAuth0DeviceCode(context.Background())
		if err != nil {
			t.Fatalf("RequestAuth0DeviceCode failed: %v", err)
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

// TestExchangeAuth0DeviceCode tests the ExchangeAuth0DeviceCode service method.
func TestExchangeAuth0DeviceCode(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	t.Run("Auth0NotConfigured", func(t *testing.T) {
		svc.Auth0 = nil
		_, err := svc.ExchangeAuth0DeviceCode(context.Background(), "device-code")
		if err == nil || err.Error() != "auth0 not configured" {
			t.Fatalf("expected 'auth0 not configured' error, got %v", err)
		}
	})

	t.Run("EmptyDeviceCode", func(t *testing.T) {
		svc.Auth0 = fakeAuth0DeviceFlow{
			issuer:   "https://example.auth0.com/",
			clientID: "test-client-id",
		}
		_, err := svc.ExchangeAuth0DeviceCode(context.Background(), "")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
	})

	t.Run("WhitespaceDeviceCode", func(t *testing.T) {
		svc.Auth0 = fakeAuth0DeviceFlow{
			issuer:   "https://example.auth0.com/",
			clientID: "test-client-id",
		}
		_, err := svc.ExchangeAuth0DeviceCode(context.Background(), "   ")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
	})

	t.Run("AuthorizationPending", func(t *testing.T) {
		svc.Auth0 = fakeAuth0DeviceFlowWithError{
			exchangeErr: auth0.OAuthError{Code: "authorization_pending"},
		}
		_, err := svc.ExchangeAuth0DeviceCode(context.Background(), "device-code")
		if err == nil || err.Error() != "auth0 authorization pending" {
			t.Fatalf("expected 'auth0 authorization pending' error, got %v", err)
		}
	})

	t.Run("SlowDown", func(t *testing.T) {
		svc.Auth0 = fakeAuth0DeviceFlowWithError{
			exchangeErr: auth0.OAuthError{Code: "slow_down"},
		}
		_, err := svc.ExchangeAuth0DeviceCode(context.Background(), "device-code")
		if err == nil || err.Error() != "auth0 slow down" {
			t.Fatalf("expected 'auth0 slow down' error, got %v", err)
		}
	})

	t.Run("ExpiredToken", func(t *testing.T) {
		svc.Auth0 = fakeAuth0DeviceFlowWithError{
			exchangeErr: auth0.OAuthError{Code: "expired_token"},
		}
		_, err := svc.ExchangeAuth0DeviceCode(context.Background(), "device-code")
		if err == nil || err.Error() != "auth0 device code expired" {
			t.Fatalf("expected 'auth0 device code expired' error, got %v", err)
		}
	})

	t.Run("AccessDenied", func(t *testing.T) {
		svc.Auth0 = fakeAuth0DeviceFlowWithError{
			exchangeErr: auth0.OAuthError{Code: "access_denied"},
		}
		_, err := svc.ExchangeAuth0DeviceCode(context.Background(), "device-code")
		if err == nil || err.Error() != "auth0 access denied" {
			t.Fatalf("expected 'auth0 access denied' error, got %v", err)
		}
	})

	t.Run("UnknownOAuthError", func(t *testing.T) {
		svc.Auth0 = fakeAuth0DeviceFlowWithError{
			exchangeErr: auth0.OAuthError{Code: "unknown_error"},
		}
		_, err := svc.ExchangeAuth0DeviceCode(context.Background(), "device-code")
		if err == nil {
			t.Fatal("expected error for unknown OAuth error")
		}
	})

	t.Run("Success", func(t *testing.T) {
		claims := map[string]any{
			"sub":                "auth0|user123",
			"email":              "user@example.com",
			"email_verified":     true,
			"name":               "Test User",
			"nickname":           "test",
			"preferred_username": "testuser",
		}
		idToken := mustJWT(t, claims)

		svc.Auth0 = fakeAuth0DeviceFlow{
			issuer:   "https://example.auth0.com/",
			clientID: "test-client-id",
			idToken:  idToken,
		}

		profile, err := svc.ExchangeAuth0DeviceCode(context.Background(), "device-code")
		if err != nil {
			t.Fatalf("ExchangeAuth0DeviceCode failed: %v", err)
		}
		if profile.Subject != "auth0|user123" {
			t.Fatalf("expected subject 'auth0|user123', got %q", profile.Subject)
		}
		if profile.Email != "user@example.com" {
			t.Fatalf("expected email 'user@example.com', got %q", profile.Email)
		}
	})
}

// fakeAuth0DeviceFlowWithError implements Auth0DeviceFlow for error testing.
type fakeAuth0DeviceFlowWithError struct {
	exchangeErr error
}

func (f fakeAuth0DeviceFlowWithError) Issuer() string   { return "https://example.auth0.com/" }
func (f fakeAuth0DeviceFlowWithError) ClientID() string { return "test-client-id" }
func (f fakeAuth0DeviceFlowWithError) RequestDeviceCode(ctx context.Context, scopes string) (auth0.DeviceCode, error) {
	return auth0.DeviceCode{DeviceCode: "device-code-123"}, nil
}
func (f fakeAuth0DeviceFlowWithError) ExchangeDeviceCode(ctx context.Context, deviceCode string) (auth0.Token, error) {
	return auth0.Token{}, f.exchangeErr
}
func (f fakeAuth0DeviceFlowWithError) VerifyIDToken(ctx context.Context, idToken string) (auth0.IDTokenClaims, error) {
	return auth0.IDTokenClaims{}, nil
}
