package slockoauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(t *testing.T, srv *httptest.Server, app string) *Client {
	t.Helper()
	if app == "" {
		app = "https://app.example.com"
	}
	c, err := New(Config{
		Origin:       srv.URL,
		APIOrigin:    srv.URL,
		ClientID:     "agent-git-service",
		ClientSecret: "sekret",
		AppOrigin:    app,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestConfigValidateMissingAll(t *testing.T) {
	err := Config{}.Validate()
	if err == nil {
		t.Fatal("expected validation error for empty config")
	}
	for _, want := range []string{"Origin", "APIOrigin", "ClientID", "ClientSecret", "AppOrigin"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to mention %q, got %q", want, err.Error())
		}
	}
}

func TestLoginURL(t *testing.T) {
	c, err := New(Config{
		Origin:       "https://app.slock.ai",
		APIOrigin:    "https://api.slock.ai",
		ClientID:     "agent-git-service",
		ClientSecret: "x",
		AppOrigin:    "https://app.example.com",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := c.LoginURL()
	if !strings.HasPrefix(got, "https://app.slock.ai/login-with-slock/setup?") {
		t.Errorf("LoginURL prefix wrong: %s", got)
	}
	if !strings.Contains(got, "client_id=agent-git-service") {
		t.Errorf("LoginURL missing client_id: %s", got)
	}
	if !strings.Contains(got, "return_to=https%3A%2F%2Fapp.example.com%2Fauth%2Fslock%2Fcallback") {
		t.Errorf("LoginURL missing/wrong return_to: %s", got)
	}
}

func TestExchangeCodeSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/oauth/token" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Basic ") {
			t.Errorf("expected Basic auth, got %q", auth)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected JSON content-type, got %q", r.Header.Get("Content-Type"))
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["grant_type"] != "authorization_code" {
			t.Errorf("grant_type=%q", body["grant_type"])
		}
		if body["code"] != "abc123" {
			t.Errorf("code=%q", body["code"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600,"scope":"identity openid profile"}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "")
	tok, err := c.ExchangeCode(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if tok.AccessToken != "tok" {
		t.Errorf("access_token=%q", tok.AccessToken)
	}
	if tok.TokenType != "Bearer" {
		t.Errorf("token_type=%q", tok.TokenType)
	}
}

func TestExchangeCodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"bad secret"}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "")
	_, err := c.ExchangeCode(context.Background(), "abc")
	if err == nil {
		t.Fatal("expected error")
	}
	var oe OAuthError
	if !errors.As(err, &oe) {
		t.Fatalf("expected OAuthError, got %T: %v", err, err)
	}
	if oe.Code != "invalid_client" {
		t.Errorf("code=%q", oe.Code)
	}
	if oe.Status != http.StatusUnauthorized {
		t.Errorf("status=%d", oe.Status)
	}
}

func TestExchangeCodeEmpty(t *testing.T) {
	c, _ := New(Config{
		Origin: "http://x", APIOrigin: "http://x", ClientID: "id", ClientSecret: "s", AppOrigin: "http://app",
	})
	if _, err := c.ExchangeCode(context.Background(), "  "); err == nil {
		t.Fatal("expected error for empty code")
	}
}

func TestUserinfoSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/oauth/userinfo" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth header: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"sub":"27a3edb7-4e03-4a42-a61d-63fc04fce62c",
			"type":"agent",
			"scope":"identity openid profile",
			"client_id":"agent-git-service",
			"client_name":"agent-git-service",
			"server_id":"bb191bdf-efe0-4733-b30e-cd26bf37d609",
			"server_slug":"dev",
			"preferred_username":"assistant",
			"name":"Claude Assistant"
		}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "")
	ui, err := c.Userinfo(context.Background(), "tok")
	if err != nil {
		t.Fatalf("Userinfo: %v", err)
	}
	if ui.Type != "agent" {
		t.Errorf("type=%q", ui.Type)
	}
	if ui.Sub != "27a3edb7-4e03-4a42-a61d-63fc04fce62c" {
		t.Errorf("sub=%q", ui.Sub)
	}
	if ui.ServerID != "bb191bdf-efe0-4733-b30e-cd26bf37d609" {
		t.Errorf("server_id=%q", ui.ServerID)
	}
}

func TestUserinfoRejectsInvalidType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sub":"s","type":"bot","server_id":"sv"}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "")
	_, err := c.Userinfo(context.Background(), "tok")
	if err == nil || !strings.Contains(err.Error(), "type") {
		t.Fatalf("expected type rejection, got %v", err)
	}
}

func TestUserinfoRejectsEmptySub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sub":"","type":"agent","server_id":"sv"}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "")
	_, err := c.Userinfo(context.Background(), "tok")
	if err == nil || !strings.Contains(err.Error(), "sub") {
		t.Fatalf("expected sub rejection, got %v", err)
	}
}

func TestUserinfoRejectsEmptyServerID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sub":"s","type":"human","server_id":""}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "")
	_, err := c.Userinfo(context.Background(), "tok")
	if err == nil || !strings.Contains(err.Error(), "server_id") {
		t.Fatalf("expected server_id rejection, got %v", err)
	}
}

func TestCallbackURL(t *testing.T) {
	c, _ := New(Config{
		Origin:       "https://app.slock.ai",
		APIOrigin:    "https://api.slock.ai",
		ClientID:     "id",
		ClientSecret: "s",
		AppOrigin:    "https://app.example.com/",
	})
	if got := c.CallbackURL(); got != "https://app.example.com/auth/slock/callback" {
		t.Errorf("CallbackURL=%q", got)
	}
}
