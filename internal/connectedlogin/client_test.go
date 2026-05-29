package connectedlogin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLoginURLUsesCallbackBaseURL(t *testing.T) {
	c, err := New(Config{
		Provider:              "provider",
		Origin:                "https://app.provider.example/",
		APIOrigin:             "https://api.provider.example",
		ClientID:              "connected-client",
		ClientSecret:          "connected-secret",
		CallbackBaseURL:       "https://ags.example.com/",
		LoginPath:             "/login/setup",
		SubjectNamespaceClaim: "workspace_id",
		AllowInsecureHTTP:     false,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	loginURL, err := url.Parse(c.LoginURL("csrf-state"))
	if err != nil {
		t.Fatalf("parse login URL: %v", err)
	}
	if loginURL.Scheme != "https" || loginURL.Host != "app.provider.example" || loginURL.Path != "/login/setup" {
		t.Fatalf("unexpected login URL: %s", loginURL.String())
	}
	if got := loginURL.Query().Get("client_id"); got != "connected-client" {
		t.Fatalf("client_id: got %q", got)
	}
	if got := loginURL.Query().Get("return_to"); got != "https://ags.example.com/auth/connected/callback" {
		t.Fatalf("return_to: got %q", got)
	}
	if got := loginURL.Query().Get("state"); got != "csrf-state" {
		t.Fatalf("state: got %q", got)
	}
	if got := c.CallbackURL(); got != "https://ags.example.com/auth/connected/callback" {
		t.Fatalf("CallbackURL: got %q", got)
	}
}

func TestExchangeCodeSendsBasicAuthAndJSON(t *testing.T) {
	var sawRequest bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != defaultTokenPath {
			t.Fatalf("path: got %q", r.URL.Path)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "connected-client" || pass != "connected-secret" {
			t.Fatalf("basic auth: got ok=%v user=%q pass=%q", ok, user, pass)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["grant_type"] != "authorization_code" || body["code"] != "auth-code" {
			t.Fatalf("unexpected body: %#v", body)
		}
		sawRequest = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-token","token_type":"Bearer"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	tok, err := c.ExchangeCode(context.Background(), " auth-code ")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if !sawRequest {
		t.Fatal("expected token request")
	}
	if tok.AccessToken != "access-token" {
		t.Fatalf("AccessToken: got %q", tok.AccessToken)
	}
}

func TestExchangeCodeReturnsOAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"Invalid client credentials"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.ExchangeCode(context.Background(), "auth-code")
	if err == nil {
		t.Fatal("expected error")
	}
	var oe OAuthError
	if !errors.As(err, &oe) {
		t.Fatalf("expected OAuthError, got %T %v", err, err)
	}
	if oe.Status != http.StatusUnauthorized || oe.Code != "Invalid client credentials" {
		t.Fatalf("OAuthError: got status=%d code=%q", oe.Status, oe.Code)
	}
}

func TestUserinfoValidatesResponse(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "human",
			body: `{"sub":"human-1","type":"human","client_id":"connected-client","workspace_id":"ws-1","preferred_username":"alice"}`,
		},
		{
			name: "agent",
			body: `{"sub":"agent-1","type":"agent","client_id":"connected-client","workspace_id":"ws-1","preferred_username":"assistant","picture":"https://cdn.provider.example/avatar.png","avatar_url":"pixel:random:42"}`,
		},
		{
			name: "empty-sub",
			body: `{"type":"human","client_id":"connected-client","workspace_id":"ws-1"}`,
			want: "empty sub",
		},
		{
			name: "empty-namespace",
			body: `{"sub":"human-1","type":"human","client_id":"connected-client"}`,
			want: "empty workspace_id",
		},
		{
			name: "bad-type",
			body: `{"sub":"human-1","type":"robot","client_id":"connected-client","workspace_id":"ws-1"}`,
			want: `unexpected actor type "robot"`,
		},
		{
			name: "client-id-mismatch",
			body: `{"sub":"human-1","type":"human","client_id":"other-client","workspace_id":"ws-1"}`,
			want: "client_id mismatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != defaultUserinfoPath {
					t.Fatalf("path: got %q", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
					t.Fatalf("Authorization: got %q", got)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c := newTestClient(t, srv.URL)
			ui, err := c.Userinfo(context.Background(), " access-token ")
			if tt.want != "" {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.want)
				}
				return
			}
			if err != nil {
				t.Fatalf("Userinfo: %v", err)
			}
			if ui.Sub == "" || ui.SubjectNamespace == "" || ui.Type == "" {
				t.Fatalf("userinfo not populated: %#v", ui)
			}
			if ui.SubjectNamespaceClaim != "workspace_id" {
				t.Fatalf("SubjectNamespaceClaim: got %q", ui.SubjectNamespaceClaim)
			}
			if tt.name == "agent" {
				if ui.Picture != "https://cdn.provider.example/avatar.png" {
					t.Fatalf("picture not populated: %#v", ui.Picture)
				}
			}
		})
	}
}

func TestSlockCompatibleConfigCoversLegacyAdapterShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != defaultUserinfoPath {
			t.Fatalf("path: got %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("Authorization: got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"sub":"agent-sub",
			"type":"agent",
			"scope":"repo",
			"client_id":"slock-client",
			"client_name":"AGS",
			"server_id":"server-1",
			"server_slug":"server-one",
			"server_role":"admin",
			"preferred_username":"dev-agent",
			"name":"Dev Agent",
			"picture":"https://cdn.slock.example/avatar.png",
			"avatar_url":"pixel:random:42",
			"description":"automation agent"
		}`))
	}))
	defer srv.Close()

	c, err := New(Config{
		Provider:                  "slock",
		Origin:                    srv.URL + "/",
		APIOrigin:                 srv.URL,
		ClientID:                  "slock-client",
		ClientSecret:              "slock-secret",
		CallbackBaseURL:           "https://ags.example.com",
		LoginPath:                 "/login-with-slock/setup",
		SubjectNamespaceClaim:     "server_id",
		SubjectNamespaceSlugClaim: "server_slug",
		AllowInsecureHTTP:         true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	loginURL, err := url.Parse(c.LoginURL("csrf-state"))
	if err != nil {
		t.Fatalf("parse login URL: %v", err)
	}
	if loginURL.Path != "/login-with-slock/setup" {
		t.Fatalf("login path: got %q", loginURL.Path)
	}
	if got := loginURL.Query().Get("client_id"); got != "slock-client" {
		t.Fatalf("client_id: got %q", got)
	}
	if got := loginURL.Query().Get("return_to"); got != "https://ags.example.com/auth/connected/callback" {
		t.Fatalf("return_to: got %q", got)
	}

	ui, err := c.Userinfo(context.Background(), "access-token")
	if err != nil {
		t.Fatalf("Userinfo: %v", err)
	}
	if ui.Sub != "agent-sub" || ui.Type != "agent" || ui.SubjectNamespace != "server-1" || ui.SubjectNamespaceSlug != "server-one" {
		t.Fatalf("unexpected slock-compatible userinfo: %#v", ui)
	}
	if ui.SubjectNamespaceClaim != "server_id" {
		t.Fatalf("SubjectNamespaceClaim: got %q", ui.SubjectNamespaceClaim)
	}
	if got, _ := ui.RawClaims["server_role"].(string); got != "admin" {
		t.Fatalf("server_role raw claim: got %q", got)
	}
}

func newTestClient(t *testing.T, serverURL string) *Client {
	t.Helper()
	c, err := New(Config{
		Provider:              "provider",
		Origin:                serverURL,
		APIOrigin:             serverURL,
		ClientID:              "connected-client",
		ClientSecret:          "connected-secret",
		CallbackBaseURL:       serverURL,
		SubjectNamespaceClaim: "workspace_id",
		AllowInsecureHTTP:     true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}
