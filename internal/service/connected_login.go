package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ngaut/agent-git-service/internal/connectedlogin"
	"github.com/ngaut/agent-git-service/internal/db"
)

type ConnectedLoginProvider interface {
	Provider() string
	ExchangeCode(ctx context.Context, code string) (connectedlogin.Token, error)
	Userinfo(ctx context.Context, accessToken string) (connectedlogin.Userinfo, error)
	LoginURL(state string) string
}

type ConnectedSessionResult struct {
	Token                 string
	UserID                uint
	Login                 string
	Type                  string
	Sub                   string
	SubjectNamespace      string
	SubjectNamespaceClaim string
}

var ErrConnectedLoginNotConfigured = errors.New("connected login is not configured")

func (s *Service) ConnectedLoginURL(state string) (string, error) {
	if s.ConnectedLogin == nil {
		return "", ErrConnectedLoginNotConfigured
	}
	return s.ConnectedLogin.LoginURL(state), nil
}

func (s *Service) ConnectedLoginWithCode(ctx context.Context, code string) (ConnectedSessionResult, error) {
	if s.ConnectedLogin == nil {
		return ConnectedSessionResult{}, ErrConnectedLoginNotConfigured
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return ConnectedSessionResult{}, fmt.Errorf("%w: code is required", ErrValidation)
	}

	provider := connectedProviderName(s.ConnectedLogin.Provider())
	tok, err := s.ConnectedLogin.ExchangeCode(ctx, code)
	if err != nil {
		slog.WarnContext(ctx, "connected login code exchange failed", "provider", provider, "error", err)
		return ConnectedSessionResult{}, err
	}
	ui, err := s.ConnectedLogin.Userinfo(ctx, tok.AccessToken)
	if err != nil {
		slog.WarnContext(ctx, "connected login userinfo failed", "provider", provider, "error", err)
		return ConnectedSessionResult{}, err
	}

	userKind := db.UserKindHuman
	if ui.Type == "agent" {
		userKind = db.UserKindAgent
	}
	profile := OIDCProfile{
		Provider:          provider,
		Subject:           connectedSubject(ui.SubjectNamespace, ui.Sub),
		Name:              strings.TrimSpace(ui.Name),
		Nickname:          strings.TrimSpace(ui.PreferredUsername),
		PreferredUsername: strings.TrimSpace(ui.PreferredUsername),
		Picture:           strings.TrimSpace(ui.Picture),
		UserKind:          userKind,
		LoginCandidates:   connectedLoginCandidates(provider, ui),
		RawClaims:         connectedRawClaims(ui),
	}
	session, err := s.oidcLoginWithProfile(ctx, profile)
	if err != nil {
		slog.ErrorContext(ctx, "connected login failed", "provider", provider, "error", err)
		return ConnectedSessionResult{}, err
	}
	slog.InfoContext(ctx, "connected login succeeded",
		"provider", provider,
		"user_login", session.Login,
		"user_id", session.UserID,
		"type", ui.Type,
		"subject_namespace", ui.SubjectNamespace,
	)
	return ConnectedSessionResult{
		Token:                 session.Token,
		UserID:                session.UserID,
		Login:                 session.Login,
		Type:                  ui.Type,
		Sub:                   ui.Sub,
		SubjectNamespace:      ui.SubjectNamespace,
		SubjectNamespaceClaim: ui.SubjectNamespaceClaim,
	}, nil
}

func connectedProviderName(provider string) string {
	provider = loginSegment(provider)
	if provider == "" {
		return "connected"
	}
	return provider
}

func connectedSubject(namespace, sub string) string {
	namespace = strings.TrimSpace(namespace)
	sub = strings.TrimSpace(sub)
	if namespace == "" {
		return sub
	}
	return namespace + ":" + sub
}

func connectedLoginCandidates(provider string, ui connectedlogin.Userinfo) []string {
	actorType := loginSegment(ui.Type)
	if actorType == "" {
		actorType = "human"
	}
	prefix := boundedLogin(provider, actorType)
	namespace := loginSegment(firstNonEmpty(ui.SubjectNamespaceSlug, ui.SubjectNamespace))
	name := loginSegment(ui.PreferredUsername)
	hash := connectedSubjectHash(ui.SubjectNamespace, ui.Sub)

	var out []string
	add := func(candidate string) {
		candidate = strings.Trim(candidate, "-_")
		if candidate == "" || !claimLoginRE.MatchString(candidate) {
			return
		}
		for _, existing := range out {
			if existing == candidate {
				return
			}
		}
		out = append(out, candidate)
	}
	if namespace != "" && name != "" {
		add(boundedLogin(prefix, namespace, name, hash))
	}
	if name != "" {
		add(boundedLogin(prefix, name, hash))
	}
	if namespace != "" {
		add(boundedLogin(prefix, namespace, hash))
	}
	add(boundedLogin(prefix, hash))
	return out
}

func boundedLogin(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		part = loginSegment(part)
		if part != "" {
			clean = append(clean, part)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	login := strings.Join(clean, "-")
	if len(login) <= maxLoginLen {
		return login
	}
	if len(clean) < 3 {
		return strings.TrimRight(login[:maxLoginLen], "-_")
	}
	prefix := clean[0]
	suffix := clean[len(clean)-1]
	middle := strings.Join(clean[1:len(clean)-1], "-")
	remaining := maxLoginLen - len(prefix) - len(suffix) - 2
	if remaining <= 0 {
		return strings.Join([]string{prefix, suffix}, "-")
	}
	if len(middle) > remaining {
		middle = strings.TrimRight(middle[:remaining], "-_")
	}
	if middle == "" {
		return strings.Join([]string{prefix, suffix}, "-")
	}
	return strings.Join([]string{prefix, middle, suffix}, "-")
}

func loginSegment(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	b.Grow(len(value))
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
		if ok {
			if r == '-' || r == '_' {
				if lastDash {
					continue
				}
				lastDash = true
			} else {
				lastDash = false
			}
			b.WriteRune(r)
			continue
		}
		if lastDash {
			continue
		}
		lastDash = true
		b.WriteByte('-')
	}
	return strings.Trim(b.String(), "-_")
}

func connectedSubjectHash(namespace, sub string) string {
	sum := sha256.Sum256([]byte(connectedSubject(namespace, sub)))
	return hex.EncodeToString(sum[:])[:10]
}

func connectedRawClaims(ui connectedlogin.Userinfo) map[string]any {
	claims := make(map[string]any, len(ui.RawClaims)+12)
	for key, value := range ui.RawClaims {
		claims[key] = value
	}
	claims["sub"] = ui.Sub
	claims["type"] = ui.Type
	claims["scope"] = ui.Scope
	claims["client_id"] = ui.ClientID
	claims["client_name"] = ui.ClientName
	claims["subject_namespace"] = ui.SubjectNamespace
	claims["subject_namespace_claim"] = ui.SubjectNamespaceClaim
	claims["subject_namespace_slug"] = ui.SubjectNamespaceSlug
	claims["preferred_username"] = ui.PreferredUsername
	claims["name"] = ui.Name
	claims["picture"] = ui.Picture
	claims["avatar_url"] = ui.AvatarURL
	claims["description"] = ui.Description
	return claims
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
