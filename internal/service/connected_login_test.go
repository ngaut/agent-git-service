package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/connectedlogin"
	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

type fakeConnectedLoginProvider struct {
	provider     string
	loginURL     string
	token        connectedlogin.Token
	userinfo     connectedlogin.Userinfo
	exchangeCode string
	accessToken  string
}

func (f *fakeConnectedLoginProvider) Provider() string {
	if f.provider == "" {
		return "provider"
	}
	return f.provider
}

func (f *fakeConnectedLoginProvider) ExchangeCode(ctx context.Context, code string) (connectedlogin.Token, error) {
	f.exchangeCode = code
	if f.token.AccessToken == "" {
		return connectedlogin.Token{AccessToken: "access-token"}, nil
	}
	return f.token, nil
}

func (f *fakeConnectedLoginProvider) Userinfo(ctx context.Context, accessToken string) (connectedlogin.Userinfo, error) {
	f.accessToken = accessToken
	return f.userinfo, nil
}

func (f *fakeConnectedLoginProvider) LoginURL(state string) string {
	if f.loginURL != "" {
		return f.loginURL
	}
	if state == "" {
		return "https://app.provider.example/oauth/login?client_id=connected-client"
	}
	return "https://app.provider.example/oauth/login?client_id=connected-client&state=" + state
}

func TestConnectedLoginWithCodeCreatesSession(t *testing.T) {
	tests := []struct {
		name         string
		actorType    string
		wantUserKind string
		wantLogin    string
	}{
		{
			name:         "human",
			actorType:    "human",
			wantUserKind: db.UserKindHuman,
			wantLogin:    "provider-human-",
		},
		{
			name:         "agent",
			actorType:    "agent",
			wantUserKind: db.UserKindAgent,
			wantLogin:    "provider-agent-",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, cleanup := setupTestService(t)
			defer cleanup()

			provider := &fakeConnectedLoginProvider{
				token: connectedlogin.Token{AccessToken: "access-token"},
				userinfo: connectedlogin.Userinfo{
					Sub:                  tt.actorType + "-sub",
					Type:                 tt.actorType,
					ClientID:             "connected-client",
					SubjectNamespace:     "workspace-1",
					SubjectNamespaceSlug: "workspace",
					PreferredUsername:    "Dev Assistant",
					Name:                 "Dev Assistant",
					Picture:              "https://cdn.provider.example/avatar.png",
					AvatarURL:            "pixel:random:42",
				},
			}
			svc.ConnectedLogin = provider

			result, err := svc.ConnectedLoginWithCode(context.Background(), " auth-code ")
			if err != nil {
				t.Fatalf("ConnectedLoginWithCode: %v", err)
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
			if result.Type != tt.actorType || result.Sub != tt.actorType+"-sub" || result.SubjectNamespace != "workspace-1" {
				t.Fatalf("unexpected external result metadata: %#v", result)
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
			if err := svc.DB.First(&ident, "user_id = ? AND provider = ? AND subject = ?", result.UserID, "provider", "workspace-1:"+tt.actorType+"-sub").Error; err != nil {
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

func TestConnectedLoginWithCodeCoversSlockIdentityShape(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	svc.ConnectedLogin = &fakeConnectedLoginProvider{
		provider: "slock",
		token:    connectedlogin.Token{AccessToken: "access-token"},
		userinfo: connectedlogin.Userinfo{
			Sub:                   "agent-sub",
			Type:                  "agent",
			ClientID:              "slock-client",
			SubjectNamespace:      "server-1",
			SubjectNamespaceClaim: "server_id",
			SubjectNamespaceSlug:  "server",
			PreferredUsername:     "Dev Agent",
			Name:                  "Dev Agent",
			Picture:               "https://cdn.slock.example/avatar.png",
			RawClaims: map[string]any{
				"server_id":   "server-1",
				"server_role": "admin",
			},
		},
	}

	result, err := svc.ConnectedLoginWithCode(context.Background(), "auth-code")
	if err != nil {
		t.Fatalf("ConnectedLoginWithCode: %v", err)
	}
	if result.Type != "agent" || result.Sub != "agent-sub" || result.SubjectNamespace != "server-1" || result.SubjectNamespaceClaim != "server_id" {
		t.Fatalf("unexpected slock-compatible result metadata: %#v", result)
	}
	if !strings.HasPrefix(result.Login, "slock-agent-server-dev-agent-") {
		t.Fatalf("login %q does not keep slock-compatible candidate shape", result.Login)
	}
	var ident db.UserIdentity
	if err := svc.DB.First(&ident, "user_id = ? AND provider = ? AND subject = ?", result.UserID, "slock", "server-1:agent-sub").Error; err != nil {
		t.Fatalf("load identity: %v", err)
	}
}

func TestConnectedLoginWithCodeReusesExistingIdentity(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	user := db.User{Login: "existing-external-user", Name: "Existing", Type: db.TypeUser, UserKind: db.UserKindHuman}
	if err := svc.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	ident := db.UserIdentity{UserID: user.ID, Provider: "provider", Subject: "workspace-1:agent-sub"}
	if err := svc.DB.Create(&ident).Error; err != nil {
		t.Fatalf("create identity: %v", err)
	}
	svc.ConnectedLogin = &fakeConnectedLoginProvider{
		token: connectedlogin.Token{AccessToken: "access-token"},
		userinfo: connectedlogin.Userinfo{
			Sub:               "agent-sub",
			Type:              "agent",
			ClientID:          "connected-client",
			SubjectNamespace:  "workspace-1",
			PreferredUsername: "agent",
			Name:              "Updated Agent",
		},
	}

	result, err := svc.ConnectedLoginWithCode(context.Background(), "auth-code")
	if err != nil {
		t.Fatalf("ConnectedLoginWithCode: %v", err)
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

func TestConnectedLoginErrors(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	if _, err := svc.ConnectedLoginURL(""); !errors.Is(err, service.ErrConnectedLoginNotConfigured) {
		t.Fatalf("ConnectedLoginURL err: got %v", err)
	}
	if _, err := svc.ConnectedLoginWithCode(context.Background(), "code"); !errors.Is(err, service.ErrConnectedLoginNotConfigured) {
		t.Fatalf("ConnectedLoginWithCode not configured err: got %v", err)
	}

	svc.ConnectedLogin = &fakeConnectedLoginProvider{}
	if got, err := svc.ConnectedLoginURL(""); err != nil || !strings.Contains(got, "/oauth/login") {
		t.Fatalf("ConnectedLoginURL got %q err %v", got, err)
	}
	if _, err := svc.ConnectedLoginWithCode(context.Background(), " "); !errors.Is(err, service.ErrValidation) {
		t.Fatalf("blank code err: got %v", err)
	}
	if got, err := svc.ConnectedLoginURL("csrf-state"); err != nil || !strings.Contains(got, "state=csrf-state") {
		t.Fatalf("ConnectedLoginURL state got %q err %v", got, err)
	}
}
