package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/slockoauth"
)

type SlockOAuthProvider interface {
	ExchangeCode(ctx context.Context, code string) (slockoauth.Token, error)
	Userinfo(ctx context.Context, accessToken string) (slockoauth.Userinfo, error)
	LoginURL(state string) string
}

type SlockSessionResult struct {
	Token    string
	UserID   uint
	Login    string
	Type     string
	Sub      string
	ServerID string
}

var ErrSlockNotConfigured = errors.New("login with slock is not configured")

func (s *Service) SlockLoginURL(state string) (string, error) {
	if s.SlockOAuth == nil {
		return "", ErrSlockNotConfigured
	}
	return s.SlockOAuth.LoginURL(state), nil
}

func (s *Service) SlockLoginWithCode(ctx context.Context, code string) (SlockSessionResult, error) {
	if s.SlockOAuth == nil {
		return SlockSessionResult{}, ErrSlockNotConfigured
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return SlockSessionResult{}, fmt.Errorf("%w: code is required", ErrValidation)
	}

	tok, err := s.SlockOAuth.ExchangeCode(ctx, code)
	if err != nil {
		slog.WarnContext(ctx, "slock oauth code exchange failed", "error", err)
		return SlockSessionResult{}, err
	}
	ui, err := s.SlockOAuth.Userinfo(ctx, tok.AccessToken)
	if err != nil {
		slog.WarnContext(ctx, "slock oauth userinfo failed", "error", err)
		return SlockSessionResult{}, err
	}

	userKind := db.UserKindHuman
	if ui.Type == "agent" {
		userKind = db.UserKindAgent
	}
	profile := OIDCProfile{
		Provider:          "slock",
		Subject:           slockSubject(ui.ServerID, ui.Sub),
		Name:              strings.TrimSpace(ui.Name),
		Nickname:          strings.TrimSpace(ui.PreferredUsername),
		PreferredUsername: strings.TrimSpace(ui.PreferredUsername),
		Picture:           slockOptionalString(ui.AvatarURL),
		UserKind:          userKind,
		LoginCandidates:   slockLoginCandidates(ui),
		RawClaims:         slockRawClaims(ui),
	}
	session, err := s.oidcLoginWithProfile(ctx, profile)
	if err != nil {
		slog.ErrorContext(ctx, "slock oauth login failed", "error", err)
		return SlockSessionResult{}, err
	}
	slog.InfoContext(ctx, "slock oauth login succeeded",
		"user_login", session.Login,
		"user_id", session.UserID,
		"type", ui.Type,
		"server_id", ui.ServerID,
	)
	return SlockSessionResult{
		Token:    session.Token,
		UserID:   session.UserID,
		Login:    session.Login,
		Type:     ui.Type,
		Sub:      ui.Sub,
		ServerID: ui.ServerID,
	}, nil
}

func slockSubject(serverID, sub string) string {
	return strings.TrimSpace(serverID) + ":" + strings.TrimSpace(sub)
}

func slockLoginCandidates(ui slockoauth.Userinfo) []string {
	prefix := "slock-human"
	if ui.Type == "agent" {
		prefix = "slock-agent"
	}
	server := slockLoginSegment(firstNonEmptySlock(ui.ServerSlug, ui.ServerID))
	name := slockLoginSegment(ui.PreferredUsername)
	hash := slockSubjectHash(ui.ServerID, ui.Sub)

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
	if server != "" && name != "" {
		add(slockBoundedLogin(prefix, server, name, hash))
	}
	if name != "" {
		add(slockBoundedLogin(prefix, name, hash))
	}
	if server != "" {
		add(slockBoundedLogin(prefix, server, hash))
	}
	add(slockBoundedLogin(prefix, hash))
	return out
}

func slockBoundedLogin(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		part = slockLoginSegment(part)
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

func slockLoginSegment(value string) string {
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

func slockSubjectHash(serverID, sub string) string {
	sum := sha256.Sum256([]byte(slockSubject(serverID, sub)))
	return hex.EncodeToString(sum[:])[:10]
}

func slockOptionalString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func slockRawClaims(ui slockoauth.Userinfo) map[string]any {
	claims := map[string]any{
		"sub":                ui.Sub,
		"type":               ui.Type,
		"scope":              ui.Scope,
		"client_id":          ui.ClientID,
		"client_name":        ui.ClientName,
		"server_id":          ui.ServerID,
		"server_slug":        ui.ServerSlug,
		"preferred_username": ui.PreferredUsername,
		"name":               ui.Name,
		"avatar_url":         slockOptionalString(ui.AvatarURL),
		"description":        slockOptionalString(ui.Description),
	}
	if ui.ServerRole != nil {
		claims["server_role"] = strings.TrimSpace(*ui.ServerRole)
	}
	return claims
}

func firstNonEmptySlock(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
