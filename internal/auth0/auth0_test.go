package auth0

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name:    "empty issuer",
			cfg:     Config{Issuer: "", ClientID: "client"},
			wantErr: "issuer is required",
		},
		{
			name:    "whitespace issuer",
			cfg:     Config{Issuer: "   ", ClientID: "client"},
			wantErr: "issuer is required",
		},
		{
			name:    "empty client_id",
			cfg:     Config{Issuer: "https://example.com", ClientID: ""},
			wantErr: "client_id is required",
		},
		{
			name:    "whitespace client_id",
			cfg:     Config{Issuer: "https://example.com", ClientID: "  \t"},
			wantErr: "client_id is required",
		},
		{
			name: "valid",
			cfg:  Config{Issuer: "https://example.com", ClientID: "client"},
		},
		{
			name: "valid with whitespace",
			cfg:  Config{Issuer: "  https://example.com  ", ClientID: "  client  "},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error %q, got nil", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("expected error %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestNewIssuerNormalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		issuer     string
		clientID   string
		wantIssuer string
		wantClient string
	}{
		{
			name:       "adds trailing slash",
			issuer:     "https://example.com",
			clientID:   "client",
			wantIssuer: "https://example.com/",
			wantClient: "client",
		},
		{
			name:       "keeps trailing slash",
			issuer:     "https://example.com/",
			clientID:   "client",
			wantIssuer: "https://example.com/",
			wantClient: "client",
		},
		{
			name:       "trims whitespace",
			issuer:     "  https://example.com  ",
			clientID:   "  client  ",
			wantIssuer: "https://example.com/",
			wantClient: "client",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, err := New(Config{Issuer: tt.issuer, ClientID: tt.clientID})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.Issuer() != tt.wantIssuer {
				t.Fatalf("expected issuer %q, got %q", tt.wantIssuer, c.Issuer())
			}
			if c.ClientID() != tt.wantClient {
				t.Fatalf("expected client_id %q, got %q", tt.wantClient, c.ClientID())
			}
		})
	}
}

func TestNewInvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name:    "missing scheme",
			cfg:     Config{Issuer: "example.com", ClientID: "client"},
			wantErr: "issuer must be a URL",
		},
		{
			name:    "empty issuer",
			cfg:     Config{Issuer: "", ClientID: "client"},
			wantErr: "issuer is required",
		},
		{
			name:    "empty client_id",
			cfg:     Config{Issuer: "https://example.com", ClientID: ""},
			wantErr: "client_id is required",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := New(tt.cfg)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error to contain %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestRequestDeviceCodeSuccess(t *testing.T) {
	t.Parallel()

	type requestCapture struct {
		path        string
		method      string
		contentType string
		form        url.Values
	}
	captureCh := make(chan requestCapture, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		captureCh <- requestCapture{
			path:        r.URL.Path,
			method:      r.Method,
			contentType: r.Header.Get("Content-Type"),
			form:        r.Form,
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"device_code":"device123","user_code":"user456","verification_uri":"https://verify","expires_in":600,"interval":5}`)
	}))
	defer srv.Close()

	c, err := New(Config{Issuer: srv.URL, ClientID: "client", Audience: "aud"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dc, err := c.RequestDeviceCode(context.Background(), "openid profile")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cap := <-captureCh

	if cap.path != "/oauth/device/code" {
		t.Fatalf("expected path /oauth/device/code, got %q", cap.path)
	}
	if cap.method != http.MethodPost {
		t.Fatalf("expected method POST, got %q", cap.method)
	}
	if !strings.HasPrefix(cap.contentType, "application/x-www-form-urlencoded") {
		t.Fatalf("expected form content type, got %q", cap.contentType)
	}
	if cap.form.Get("client_id") != "client" {
		t.Fatalf("expected client_id=client, got %q", cap.form.Get("client_id"))
	}
	if cap.form.Get("scope") != "openid profile" {
		t.Fatalf("expected scope, got %q", cap.form.Get("scope"))
	}
	if cap.form.Get("audience") != "aud" {
		t.Fatalf("expected audience, got %q", cap.form.Get("audience"))
	}

	if dc.DeviceCode != "device123" {
		t.Fatalf("expected device_code, got %q", dc.DeviceCode)
	}
	if dc.UserCode != "user456" {
		t.Fatalf("expected user_code, got %q", dc.UserCode)
	}
	if dc.VerificationURI != "https://verify" {
		t.Fatalf("expected verification_uri, got %q", dc.VerificationURI)
	}
	if dc.ExpiresIn != 600 || dc.Interval != 5 {
		t.Fatalf("unexpected expires/interval: %d/%d", dc.ExpiresIn, dc.Interval)
	}
}

func TestRequestDeviceCodeOAuthError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_request","error_description":"missing client_id"}`)
	}))
	defer srv.Close()

	c, err := New(Config{Issuer: srv.URL, ClientID: "client"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = c.RequestDeviceCode(context.Background(), "")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	var oe OAuthError
	if !errors.As(err, &oe) {
		t.Fatalf("expected OAuthError, got %T: %v", err, err)
	}
	if oe.Code != "invalid_request" || oe.Description != "missing client_id" {
		t.Fatalf("unexpected OAuthError: %#v", oe)
	}
	if err.Error() != "invalid_request: missing client_id" {
		t.Fatalf("unexpected error string: %q", err.Error())
	}
}

func TestRequestDeviceCodeOAuthErrorMissingCode(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"message":"no error code"}`)
	}))
	defer srv.Close()

	c, err := New(Config{Issuer: srv.URL, ClientID: "client"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = c.RequestDeviceCode(context.Background(), "")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	var oe OAuthError
	if errors.As(err, &oe) {
		t.Fatalf("expected generic error, got OAuthError: %#v", oe)
	}
	if !strings.Contains(err.Error(), "device code request failed: status=400") {
		t.Fatalf("unexpected error string: %q", err.Error())
	}
}

func TestRequestDeviceCodeMalformedJSON(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"device_code":`) // malformed
	}))
	defer srv.Close()

	c, err := New(Config{Issuer: srv.URL, ClientID: "client"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = c.RequestDeviceCode(context.Background(), "")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "decode device code response") {
		t.Fatalf("expected decode error, got %q", err.Error())
	}
}

func TestRequestDeviceCodeIncompleteResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"device_code":"device123"}`)
	}))
	defer srv.Close()

	c, err := New(Config{Issuer: srv.URL, ClientID: "client"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = c.RequestDeviceCode(context.Background(), "")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if err.Error() != "auth0: incomplete device code response" {
		t.Fatalf("unexpected error string: %q", err.Error())
	}
}

func TestRequestDeviceCodeNetworkError(t *testing.T) {
	t.Parallel()

	c, err := New(Config{Issuer: "https://example.com", ClientID: "client"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c.http = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	})}

	_, err = c.RequestDeviceCode(context.Background(), "")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "network down") {
		t.Fatalf("expected network error, got %q", err.Error())
	}
}

func TestExchangeDeviceCodeSuccess(t *testing.T) {
	t.Parallel()

	type requestCapture struct {
		path string
		form url.Values
	}
	captureCh := make(chan requestCapture, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		captureCh <- requestCapture{
			path: r.URL.Path,
			form: r.Form,
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"access123","id_token":"idtoken","token_type":"bearer","scope":"openid","expires_in":3600}`)
	}))
	defer srv.Close()

	c, err := New(Config{Issuer: srv.URL, ClientID: "client"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tok, err := c.ExchangeDeviceCode(context.Background(), "device123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cap := <-captureCh

	if cap.path != "/oauth/token" {
		t.Fatalf("expected path /oauth/token, got %q", cap.path)
	}
	if cap.form.Get("client_id") != "client" {
		t.Fatalf("expected client_id=client, got %q", cap.form.Get("client_id"))
	}
	if cap.form.Get("device_code") != "device123" {
		t.Fatalf("expected device_code=device123, got %q", cap.form.Get("device_code"))
	}
	if cap.form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
		t.Fatalf("unexpected grant_type: %q", cap.form.Get("grant_type"))
	}

	if tok.AccessToken != "access123" {
		t.Fatalf("expected access_token, got %q", tok.AccessToken)
	}
	if tok.IDToken != "idtoken" {
		t.Fatalf("expected id_token, got %q", tok.IDToken)
	}
	if tok.TokenType != "bearer" || tok.Scope != "openid" || tok.ExpiresIn != 3600 {
		t.Fatalf("unexpected token fields: %#v", tok)
	}
}

func TestExchangeDeviceCodeOAuthError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"authorization_pending","error_description":"waiting"}`)
	}))
	defer srv.Close()

	c, err := New(Config{Issuer: srv.URL, ClientID: "client"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = c.ExchangeDeviceCode(context.Background(), "device123")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	var oe OAuthError
	if !errors.As(err, &oe) {
		t.Fatalf("expected OAuthError, got %T: %v", err, err)
	}
	if oe.Code != "authorization_pending" || oe.Description != "waiting" {
		t.Fatalf("unexpected OAuthError: %#v", oe)
	}
	if err.Error() != "authorization_pending: waiting" {
		t.Fatalf("unexpected error string: %q", err.Error())
	}
}

func TestExchangeDeviceCodeOAuthErrorMissingCode(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"message":"no error code"}`)
	}))
	defer srv.Close()

	c, err := New(Config{Issuer: srv.URL, ClientID: "client"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = c.ExchangeDeviceCode(context.Background(), "device123")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	var oe OAuthError
	if errors.As(err, &oe) {
		t.Fatalf("expected generic error, got OAuthError: %#v", oe)
	}
	if !strings.Contains(err.Error(), "token exchange failed: status=400") {
		t.Fatalf("unexpected error string: %q", err.Error())
	}
}

func TestExchangeDeviceCodeMalformedJSON(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "not-json")
	}))
	defer srv.Close()

	c, err := New(Config{Issuer: srv.URL, ClientID: "client"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = c.ExchangeDeviceCode(context.Background(), "device123")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "decode token response") {
		t.Fatalf("expected decode error, got %q", err.Error())
	}
}

func TestDecodeIDTokenClaims(t *testing.T) {
	t.Parallel()

	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		token := buildJWT(t, map[string]any{"alg": "none"}, map[string]any{"sub": "user123", "email": "a@example.com"})
		claims, err := DecodeIDTokenClaims(token)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if claims.Sub != "user123" {
			t.Fatalf("expected sub user123, got %q", claims.Sub)
		}
		if claims.Email != "a@example.com" {
			t.Fatalf("expected email, got %q", claims.Email)
		}
	})

	t.Run("malformed jwt", func(t *testing.T) {
		t.Parallel()
		_, err := DecodeIDTokenClaims("a.b")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if err.Error() != "invalid id_token" {
			t.Fatalf("unexpected error: %q", err.Error())
		}
	})

	t.Run("base64 error", func(t *testing.T) {
		t.Parallel()
		_, err := DecodeIDTokenClaims("aaa.%%%.ccc")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "decode id_token payload") {
			t.Fatalf("expected base64 decode error, got %q", err.Error())
		}
	})

	t.Run("missing sub", func(t *testing.T) {
		t.Parallel()
		token := buildJWT(t, map[string]any{"alg": "none"}, map[string]any{"email": "a@example.com"})
		_, err := DecodeIDTokenClaims(token)
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if err.Error() != "id_token missing sub" {
			t.Fatalf("unexpected error: %q", err.Error())
		}
	})

	t.Run("invalid json payload", func(t *testing.T) {
		t.Parallel()
		payload := base64.RawURLEncoding.EncodeToString([]byte("{"))
		_, err := DecodeIDTokenClaims("a." + payload + ".c")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "parse id_token payload") {
			t.Fatalf("expected parse error, got %q", err.Error())
		}
	})

	t.Run("audience string", func(t *testing.T) {
		t.Parallel()
		token := buildJWT(t, map[string]any{"alg": "none"}, map[string]any{
			"sub": "user123",
			"aud": "client",
		})
		claims, err := DecodeIDTokenClaims(token)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := claims.Aud.(string); !ok {
			t.Fatalf("expected aud string, got %T", claims.Aud)
		}
		if !claims.AudienceContains("client") {
			t.Fatalf("expected audience match")
		}
	})

	t.Run("audience array", func(t *testing.T) {
		t.Parallel()
		token := buildJWT(t, map[string]any{"alg": "none"}, map[string]any{
			"sub": "user123",
			"aud": []any{"client", "other"},
		})
		claims, err := DecodeIDTokenClaims(token)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := claims.Aud.([]any); !ok {
			t.Fatalf("expected aud array, got %T", claims.Aud)
		}
		if !claims.AudienceContains("client") {
			t.Fatalf("expected audience match")
		}
	})
}

func TestVerifyIDTokenDelegatesToJWKS(t *testing.T) {
	t.Parallel()

	hitCh := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCh <- struct{}{}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, err := New(Config{Issuer: srv.URL, ClientID: "client"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c.jwks.http = srv.Client()

	token := buildJWT(t, map[string]any{"alg": "RS256", "kid": "kid1"}, map[string]any{
		"sub": "user123",
		"iss": c.Issuer(),
		"aud": c.ClientID(),
	})

	_, err = c.VerifyIDToken(context.Background(), token)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	select {
	case <-hitCh:
	default:
		t.Fatalf("expected JWKS fetch to be attempted")
	}
	if !strings.Contains(err.Error(), "jwks request failed") {
		t.Fatalf("expected jwks error, got %q", err.Error())
	}
}

func TestIDTokenClaimsAudienceContains(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		aud      any
		clientID string
		want     bool
	}{
		{name: "string match", aud: "client", clientID: "client", want: true},
		{name: "string mismatch", aud: "client", clientID: "other", want: false},
		{name: "array match", aud: []any{"client", "other"}, clientID: "client", want: true},
		{name: "array mismatch", aud: []any{"one", "two"}, clientID: "client", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claims := IDTokenClaims{Aud: tt.aud}
			if got := claims.AudienceContains(tt.clientID); got != tt.want {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
		})
	}
}

func buildJWT(t *testing.T, header map[string]any, payload map[string]any) string {
	t.Helper()
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	headerSeg := base64.RawURLEncoding.EncodeToString(headerJSON)
	payloadSeg := base64.RawURLEncoding.EncodeToString(payloadJSON)
	sigSeg := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	return headerSeg + "." + payloadSeg + "." + sigSeg
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
