package rest_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gh-server/internal/auth0"
	"gh-server/internal/db"
	"gh-server/internal/testharness"
)

// fakeAuth0DeviceFlow implements service.Auth0DeviceFlow for REST handler tests.
type fakeAuth0DeviceFlow struct {
	issuer     string
	clientID   string
	idToken    string
	deviceCode auth0.DeviceCode

	requestErr  error
	exchangeErr error
	verifyErr   error
}

func (f fakeAuth0DeviceFlow) Issuer() string   { return f.issuer }
func (f fakeAuth0DeviceFlow) ClientID() string { return f.clientID }

func (f fakeAuth0DeviceFlow) RequestDeviceCode(ctx context.Context, scopes string) (auth0.DeviceCode, error) {
	if f.requestErr != nil {
		return auth0.DeviceCode{}, f.requestErr
	}
	if f.deviceCode.DeviceCode != "" {
		return f.deviceCode, nil
	}
	return auth0DeviceCodeFixture(), nil
}

func (f fakeAuth0DeviceFlow) ExchangeDeviceCode(ctx context.Context, deviceCode string) (auth0.Token, error) {
	if f.exchangeErr != nil {
		return auth0.Token{}, f.exchangeErr
	}
	return auth0.Token{IDToken: f.idToken}, nil
}

func (f fakeAuth0DeviceFlow) VerifyIDToken(ctx context.Context, idToken string) (auth0.IDTokenClaims, error) {
	if f.verifyErr != nil {
		return auth0.IDTokenClaims{}, f.verifyErr
	}
	return auth0.DecodeIDTokenClaims(idToken)
}

func auth0DeviceCodeFixture() auth0.DeviceCode {
	return auth0.DeviceCode{
		DeviceCode:              "device-code-123",
		UserCode:                "USER-123",
		VerificationURI:         "https://example.invalid/activate",
		VerificationURIComplete: "https://example.invalid/activate?code=USER-123",
		ExpiresIn:               900,
		Interval:                5,
	}
}

func auth0FlowForLogin(t *testing.T, login string) fakeAuth0DeviceFlow {
	t.Helper()
	issuer := "https://example.auth0.com/"
	clientID := "client-123"
	claims := map[string]any{
		"iss":                issuer,
		"aud":                clientID,
		"sub":                "auth0|" + login,
		"email":              login + "@example.com",
		"email_verified":     true,
		"name":               "Auth0 " + login,
		"nickname":           login,
		"preferred_username": login,
	}
	return fakeAuth0DeviceFlow{
		issuer:     issuer,
		clientID:   clientID,
		idToken:    mustJWT(t, claims),
		deviceCode: auth0DeviceCodeFixture(),
	}
}

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

func postJSON(t *testing.T, h *testharness.Harness, path, body string, ctx context.Context) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)
	return w
}

func TestAuth0DeviceCode(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		h := testharness.New(t)
		dc := auth0DeviceCodeFixture()
		h.Svc.Auth0 = fakeAuth0DeviceFlow{
			issuer:     "https://example.auth0.com/",
			clientID:   "client-123",
			deviceCode: dc,
		}

		w := postJSON(t, h, "/api/v3/auth0/device/code", "", context.Background())
		assertStatusCode(t, w, http.StatusOK)
		body := testharness.DecodeJSON(t, w)

		if body["device_code"] != dc.DeviceCode {
			t.Fatalf("device_code: got %v, want %q", body["device_code"], dc.DeviceCode)
		}
		if body["user_code"] != dc.UserCode {
			t.Fatalf("user_code: got %v, want %q", body["user_code"], dc.UserCode)
		}
		if body["verification_uri"] != dc.VerificationURI {
			t.Fatalf("verification_uri: got %v, want %q", body["verification_uri"], dc.VerificationURI)
		}
		if body["verification_uri_complete"] != dc.VerificationURIComplete {
			t.Fatalf("verification_uri_complete: got %v, want %q", body["verification_uri_complete"], dc.VerificationURIComplete)
		}
		if body["expires_in"] != float64(dc.ExpiresIn) {
			t.Fatalf("expires_in: got %v, want %d", body["expires_in"], dc.ExpiresIn)
		}
		if body["interval"] != float64(dc.Interval) {
			t.Fatalf("interval: got %v, want %d", body["interval"], dc.Interval)
		}
	})

	t.Run("NotConfigured", func(t *testing.T) {
		h := testharness.New(t)

		w := postJSON(t, h, "/api/v3/auth0/device/code", "", context.Background())
		assertStatusCode(t, w, http.StatusNotImplemented)
		body := testharness.DecodeJSON(t, w)
		if body["message"] != "Auth0 is not configured" {
			t.Fatalf("message: got %v, want %q", body["message"], "Auth0 is not configured")
		}
	})

	t.Run("OAuthError", func(t *testing.T) {
		h := testharness.New(t)
		h.Svc.Auth0 = fakeAuth0DeviceFlow{
			requestErr: auth0.OAuthError{Code: "invalid_client", Description: "bad client"},
		}

		w := postJSON(t, h, "/api/v3/auth0/device/code", "", context.Background())
		assertStatusCode(t, w, http.StatusBadGateway)
		body := testharness.DecodeJSON(t, w)
		msg, _ := body["message"].(string)
		if !strings.Contains(msg, "Auth0 error: invalid_client") {
			t.Fatalf("message: got %q, want OAuth error", msg)
		}
	})

	t.Run("RequestFailed", func(t *testing.T) {
		h := testharness.New(t)
		h.Svc.Auth0 = fakeAuth0DeviceFlow{
			requestErr: context.Canceled,
		}

		w := postJSON(t, h, "/api/v3/auth0/device/code", "", context.Background())
		assertStatusCode(t, w, http.StatusBadGateway)
		body := testharness.DecodeJSON(t, w)
		msg, _ := body["message"].(string)
		if !strings.Contains(msg, "Auth0 request failed") {
			t.Fatalf("message: got %q, want request failed", msg)
		}
		if !strings.Contains(msg, "context canceled") {
			t.Fatalf("message: got %q, want context canceled", msg)
		}
	})
}

func TestAuth0Session(t *testing.T) {
	t.Run("ValidationMissingDeviceCode", func(t *testing.T) {
		h := testharness.New(t)
		h.Svc.Auth0 = auth0FlowForLogin(t, "auth0-user")

		w := postJSON(t, h, "/api/v3/auth0/session", "{}", context.Background())
		assertStatusCode(t, w, http.StatusUnprocessableEntity)
		body := testharness.DecodeJSON(t, w)
		if body["message"] != "device_code is required" {
			t.Fatalf("message: got %v, want %q", body["message"], "device_code is required")
		}
	})

	t.Run("NotConfigured", func(t *testing.T) {
		h := testharness.New(t)

		w := postJSON(t, h, "/api/v3/auth0/session", `{"device_code":"device-code-123"}`, context.Background())
		assertStatusCode(t, w, http.StatusNotImplemented)
		body := testharness.DecodeJSON(t, w)
		if body["message"] != "Auth0 is not configured" {
			t.Fatalf("message: got %v, want %q", body["message"], "Auth0 is not configured")
		}
	})

	t.Run("Pending", func(t *testing.T) {
		h := testharness.New(t)
		h.Svc.Auth0 = fakeAuth0DeviceFlow{
			exchangeErr: auth0.OAuthError{Code: "authorization_pending"},
		}

		w := postJSON(t, h, "/api/v3/auth0/session", `{"device_code":"device-code-123"}`, context.Background())
		assertStatusCode(t, w, http.StatusAccepted)
		body := testharness.DecodeJSON(t, w)
		if body["status"] != "authorization_pending" {
			t.Fatalf("status: got %v, want %q", body["status"], "authorization_pending")
		}
	})

	t.Run("SlowDown", func(t *testing.T) {
		h := testharness.New(t)
		h.Svc.Auth0 = fakeAuth0DeviceFlow{
			exchangeErr: auth0.OAuthError{Code: "slow_down"},
		}

		w := postJSON(t, h, "/api/v3/auth0/session", `{"device_code":"device-code-123"}`, context.Background())
		assertStatusCode(t, w, http.StatusAccepted)
		body := testharness.DecodeJSON(t, w)
		if body["status"] != "slow_down" {
			t.Fatalf("status: got %v, want %q", body["status"], "slow_down")
		}
	})

	t.Run("Expired", func(t *testing.T) {
		h := testharness.New(t)
		h.Svc.Auth0 = fakeAuth0DeviceFlow{
			exchangeErr: auth0.OAuthError{Code: "expired_token"},
		}

		w := postJSON(t, h, "/api/v3/auth0/session", `{"device_code":"device-code-123"}`, context.Background())
		assertStatusCode(t, w, http.StatusUnprocessableEntity)
		body := testharness.DecodeJSON(t, w)
		if body["message"] != "device_code expired" {
			t.Fatalf("message: got %v, want %q", body["message"], "device_code expired")
		}
	})

	t.Run("AccessDenied", func(t *testing.T) {
		h := testharness.New(t)
		h.Svc.Auth0 = fakeAuth0DeviceFlow{
			exchangeErr: auth0.OAuthError{Code: "access_denied"},
		}

		w := postJSON(t, h, "/api/v3/auth0/session", `{"device_code":"device-code-123"}`, context.Background())
		assertStatusCode(t, w, http.StatusForbidden)
		body := testharness.DecodeJSON(t, w)
		if body["message"] != "access denied" {
			t.Fatalf("message: got %v, want %q", body["message"], "access denied")
		}
	})

	t.Run("OAuthError", func(t *testing.T) {
		h := testharness.New(t)
		h.Svc.Auth0 = fakeAuth0DeviceFlow{
			exchangeErr: auth0.OAuthError{Code: "invalid_grant", Description: "bad device code"},
		}

		w := postJSON(t, h, "/api/v3/auth0/session", `{"device_code":"device-code-123"}`, context.Background())
		assertStatusCode(t, w, http.StatusBadGateway)
		body := testharness.DecodeJSON(t, w)
		msg, _ := body["message"].(string)
		if !strings.Contains(msg, "Auth0 error: invalid_grant") {
			t.Fatalf("message: got %q, want OAuth error", msg)
		}
	})

	t.Run("ServiceError", func(t *testing.T) {
		h := testharness.New(t)
		h.Svc.Auth0 = fakeAuth0DeviceFlow{}

		w := postJSON(t, h, "/api/v3/auth0/session", `{"device_code":"device-code-123"}`, context.Background())
		assertStatusCode(t, w, http.StatusInternalServerError)
		body := testharness.DecodeJSON(t, w)
		if body["message"] != "Internal Server Error" {
			t.Fatalf("message: got %v, want %q", body["message"], "Internal Server Error")
		}
	})

	t.Run("Success", func(t *testing.T) {
		h := testharness.New(t)
		flow := auth0FlowForLogin(t, "auth0-user")
		h.Svc.Auth0 = flow

		w := postJSON(t, h, "/api/v3/auth0/session", `{"device_code":"device-code-123"}`, context.Background())
		assertStatusCode(t, w, http.StatusOK)
		body := testharness.DecodeJSON(t, w)

		assertFieldsPresent(t, body, map[string]string{
			"token":   "string",
			"user_id": "number",
			"login":   "string",
		})

		token, _ := body["token"].(string)
		login, _ := body["login"].(string)
		if token == "" {
			t.Fatal("expected token to be set")
		}
		if login != "auth0-user" {
			t.Fatalf("login: got %q, want %q", login, "auth0-user")
		}
		resolved, err := h.Svc.ResolveUserByToken(context.Background(), token)
		if err != nil {
			t.Fatalf("ResolveUserByToken: %v", err)
		}
		if resolved.Login != login {
			t.Fatalf("token login: got %q, want %q", resolved.Login, login)
		}
		if gotID, ok := body["user_id"].(float64); !ok || uint(gotID) != resolved.ID {
			t.Fatalf("user_id: got %v, want %d", body["user_id"], resolved.ID)
		}
	})
}

func TestAuth0Callback(t *testing.T) {
	t.Run("ValidationMissingIDToken", func(t *testing.T) {
		h := testharness.New(t)
		h.Svc.Auth0 = auth0FlowForLogin(t, "auth0-user")

		w := postJSON(t, h, "/api/v3/auth0/callback", "{}", context.Background())
		assertStatusCode(t, w, http.StatusUnprocessableEntity)
		body := testharness.DecodeJSON(t, w)
		if body["message"] != "id_token is required" {
			t.Fatalf("message: got %v, want %q", body["message"], "id_token is required")
		}
	})

	t.Run("NotConfigured", func(t *testing.T) {
		h := testharness.New(t)

		w := postJSON(t, h, "/api/v3/auth0/callback", `{"id_token":"token"}`, context.Background())
		assertStatusCode(t, w, http.StatusNotImplemented)
		body := testharness.DecodeJSON(t, w)
		if body["message"] != "Auth0 is not configured" {
			t.Fatalf("message: got %v, want %q", body["message"], "Auth0 is not configured")
		}
	})

	t.Run("InvalidIDToken", func(t *testing.T) {
		h := testharness.New(t)
		h.Svc.Auth0 = fakeAuth0DeviceFlow{
			verifyErr: context.Canceled,
		}

		w := postJSON(t, h, "/api/v3/auth0/callback", `{"id_token":"bad"}`, context.Background())
		assertStatusCode(t, w, http.StatusUnauthorized)
		body := testharness.DecodeJSON(t, w)
		if body["message"] != "invalid id_token" {
			t.Fatalf("message: got %v, want %q", body["message"], "invalid id_token")
		}
	})

	t.Run("ServiceError", func(t *testing.T) {
		h := testharness.New(t)
		flow := auth0FlowForLogin(t, "auth0-user")
		h.Svc.Auth0 = flow

		sqlDB, err := h.DB.DB()
		if err != nil {
			t.Fatalf("sql DB: %v", err)
		}
		if err := sqlDB.Close(); err != nil {
			t.Fatalf("close DB: %v", err)
		}

		body := `{"id_token":"` + flow.idToken + `"}`
		w := postJSON(t, h, "/api/v3/auth0/callback", body, context.Background())
		assertStatusCode(t, w, http.StatusInternalServerError)
		resp := testharness.DecodeJSON(t, w)
		if resp["message"] != "Internal Server Error" {
			t.Fatalf("message: got %v, want %q", resp["message"], "Internal Server Error")
		}
	})

	t.Run("Success", func(t *testing.T) {
		h := testharness.New(t)
		flow := auth0FlowForLogin(t, "auth0-user")
		h.Svc.Auth0 = flow

		body := `{"id_token":"` + flow.idToken + `"}`
		w := postJSON(t, h, "/api/v3/auth0/callback", body, context.Background())
		assertStatusCode(t, w, http.StatusOK)
		resp := testharness.DecodeJSON(t, w)

		assertFieldsPresent(t, resp, map[string]string{
			"token":   "string",
			"user_id": "number",
			"login":   "string",
			"user":    "object",
		})

		token, _ := resp["token"].(string)
		login, _ := resp["login"].(string)
		if token == "" {
			t.Fatal("expected token to be set")
		}
		if login != "auth0-user" {
			t.Fatalf("login: got %q, want %q", login, "auth0-user")
		}

		userMap, ok := resp["user"].(map[string]any)
		if !ok {
			t.Fatalf("user: expected object, got %T", resp["user"])
		}
		if userMap["login"] != login {
			t.Fatalf("user.login: got %v, want %q", userMap["login"], login)
		}
		if userMap["email"] != login+"@example.com" {
			t.Fatalf("user.email: got %v, want %q", userMap["email"], login+"@example.com")
		}

		resolved, err := h.Svc.ResolveUserByToken(context.Background(), token)
		if err != nil {
			t.Fatalf("ResolveUserByToken: %v", err)
		}
		if resolved.Login != login {
			t.Fatalf("token login: got %q, want %q", resolved.Login, login)
		}

		var dbUser db.User
		if err := h.DB.First(&dbUser, "login = ?", login).Error; err != nil {
			t.Fatalf("load user: %v", err)
		}
		if gotID, ok := resp["user_id"].(float64); !ok || uint(gotID) != dbUser.ID {
			t.Fatalf("user_id: got %v, want %d", resp["user_id"], dbUser.ID)
		}
		if userID, ok := userMap["id"].(float64); !ok || uint(userID) != dbUser.ID {
			t.Fatalf("user.id: got %v, want %d", userMap["id"], dbUser.ID)
		}
	})
}
