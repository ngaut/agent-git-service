package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/slockoauth"
)

type fakeSlockOAuthProvider struct {
	loginURL     string
	token        slockoauth.Token
	userinfo     slockoauth.Userinfo
	exchangeCode string
	accessToken  string
}

func (f *fakeSlockOAuthProvider) ExchangeCode(ctx context.Context, code string) (slockoauth.Token, error) {
	f.exchangeCode = code
	if f.token.AccessToken == "" {
		return slockoauth.Token{AccessToken: "access-token"}, nil
	}
	return f.token, nil
}

func (f *fakeSlockOAuthProvider) Userinfo(ctx context.Context, accessToken string) (slockoauth.Userinfo, error) {
	f.accessToken = accessToken
	return f.userinfo, nil
}

func (f *fakeSlockOAuthProvider) LoginURL(state string) string {
	if f.loginURL != "" {
		return f.loginURL
	}
	if state == "" {
		return "https://app.slock.ai/login-with-slock/setup?client_id=slock-client"
	}
	return "https://app.slock.ai/login-with-slock/setup?client_id=slock-client&state=" + state
}

func TestSlockLoginWithCodeCreatesSession(t *testing.T) {
	tests := []struct {
		name         string
		slockType    string
		wantUserKind string
		wantLogin    string
	}{
		{
			name:         "human",
			slockType:    "human",
			wantUserKind: db.UserKindHuman,
			wantLogin:    "slock-human-",
		},
		{
			name:         "agent",
			slockType:    "agent",
			wantUserKind: db.UserKindAgent,
			wantLogin:    "slock-agent-",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, cleanup := setupTestService(t)
			defer cleanup()

			provider := &fakeSlockOAuthProvider{
				token: slockoauth.Token{AccessToken: "access-token"},
				userinfo: slockoauth.Userinfo{
					Sub:               tt.slockType + "-sub",
					Type:              tt.slockType,
					ClientID:          "slock-client",
					ServerID:          "srv-1",
					ServerSlug:        "workspace",
					PreferredUsername: "Dev Assistant",
					Name:              "Dev Assistant",
					Picture:           stringPtr("https://cdn.slock.ai/avatar.png"),
					AvatarURL:         stringPtr("pixel:random:42"),
				},
			}
			svc.SlockOAuth = provider

			result, err := svc.SlockLoginWithCode(context.Background(), " auth-code ")
			if err != nil {
				t.Fatalf("SlockLoginWithCode: %v", err)
			}
			if provider.exchangeCode != "auth-code" {
				t.Fatalf("exchange code: got %q", provider.exchangeCode)
			}
			if provider.accessToken != "access-token" {
				t.Fatalf("access token: got %q", provider.accessToken)
			}
			if result.Token == "" || result.UserID == 0 {
				t.Fatalf("expected token and user id, got %#v", result)
			}
			if result.Type != tt.slockType || result.Sub != tt.slockType+"-sub" || result.ServerID != "srv-1" {
				t.Fatalf("unexpected Slock result metadata: %#v", result)
			}
			if !strings.HasPrefix(result.Login, tt.wantLogin) {
				t.Fatalf("login %q does not have prefix %q", result.Login, tt.wantLogin)
			}

			var user db.User
			if err := svc.DB.First(&user, result.UserID).Error; err != nil {
				t.Fatalf("load user: %v", err)
			}
			if user.UserKind != tt.wantUserKind {
				t.Fatalf("UserKind: got %q, want %q", user.UserKind, tt.wantUserKind)
			}
			if user.Name != "Dev Assistant" {
				t.Fatalf("Name: got %q", user.Name)
			}
			var ident db.UserIdentity
			if err := svc.DB.First(&ident, "user_id = ? AND provider = ? AND subject = ?", result.UserID, "slock", "srv-1:"+tt.slockType+"-sub").Error; err != nil {
				t.Fatalf("load identity: %v", err)
			}

			var tok db.Token
			if err := svc.DB.First(&tok, "value = ?", result.Token).Error; err != nil {
				t.Fatalf("load token: %v", err)
			}
			if tok.UserID != result.UserID {
				t.Fatalf("token UserID: got %d, want %d", tok.UserID, result.UserID)
			}
		})
	}
}

func stringPtr(value string) *string {
	return &value
}

func TestSlockLoginWithCodeReusesExistingIdentity(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	user := db.User{Login: "existing-slock-user", Name: "Existing", Type: db.TypeUser, UserKind: db.UserKindHuman}
	if err := svc.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	ident := db.UserIdentity{UserID: user.ID, Provider: "slock", Subject: "srv-1:agent-sub"}
	if err := svc.DB.Create(&ident).Error; err != nil {
		t.Fatalf("create identity: %v", err)
	}
	svc.SlockOAuth = &fakeSlockOAuthProvider{
		token: slockoauth.Token{AccessToken: "access-token"},
		userinfo: slockoauth.Userinfo{
			Sub:               "agent-sub",
			Type:              "agent",
			ClientID:          "slock-client",
			ServerID:          "srv-1",
			PreferredUsername: "agent",
			Name:              "Updated Agent",
		},
	}

	result, err := svc.SlockLoginWithCode(context.Background(), "auth-code")
	if err != nil {
		t.Fatalf("SlockLoginWithCode: %v", err)
	}
	if result.UserID != user.ID || result.Login != user.Login {
		t.Fatalf("expected existing user, got %#v", result)
	}
	var updated db.User
	if err := svc.DB.First(&updated, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if updated.UserKind != db.UserKindAgent {
		t.Fatalf("expected existing linked user kind to update to agent, got %q", updated.UserKind)
	}
	if updated.Name != "Updated Agent" {
		t.Fatalf("Name: got %q", updated.Name)
	}
}

func TestSlockLoginErrors(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	if _, err := svc.SlockLoginURL(""); !errors.Is(err, service.ErrSlockNotConfigured) {
		t.Fatalf("SlockLoginURL err: got %v", err)
	}
	if _, err := svc.SlockLoginWithCode(context.Background(), "code"); !errors.Is(err, service.ErrSlockNotConfigured) {
		t.Fatalf("SlockLoginWithCode not configured err: got %v", err)
	}

	svc.SlockOAuth = &fakeSlockOAuthProvider{}
	if got, err := svc.SlockLoginURL(""); err != nil || !strings.Contains(got, "login-with-slock") {
		t.Fatalf("SlockLoginURL got %q err %v", got, err)
	}
	if _, err := svc.SlockLoginWithCode(context.Background(), " "); !errors.Is(err, service.ErrValidation) {
		t.Fatalf("blank code err: got %v", err)
	}
	if got, err := svc.SlockLoginURL("csrf-state"); err != nil || !strings.Contains(got, "state=csrf-state") {
		t.Fatalf("SlockLoginURL state got %q err %v", got, err)
	}
}
