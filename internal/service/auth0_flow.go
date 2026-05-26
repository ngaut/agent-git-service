package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ngaut/agent-git-service/internal/auth0"
)

// Auth0DeviceFlow is the subset of Auth0 functionality gh-server needs.
// It is an interface to keep service tests decoupled from real network calls.
type Auth0DeviceFlow interface {
	RequestDeviceCode(ctx context.Context, scopes string) (auth0.DeviceCode, error)
	ExchangeDeviceCode(ctx context.Context, deviceCode string) (auth0.Token, error)
	VerifyIDToken(ctx context.Context, idToken string) (auth0.IDTokenClaims, error)
	Issuer() string
	ClientID() string
}

var (
	ErrAuth0NotConfigured = errors.New("auth0 not configured")
	ErrAuth0Pending       = errors.New("auth0 authorization pending")
	ErrAuth0SlowDown      = errors.New("auth0 slow down")
	ErrAuth0Expired       = errors.New("auth0 device code expired")
	ErrAuth0AccessDenied  = errors.New("auth0 access denied")
)

type Auth0Profile struct {
	Provider          string
	Subject           string
	Email             string
	EmailVerified     bool
	Name              string
	Nickname          string
	PreferredUsername string
	Picture           string
}

func (p Auth0Profile) DisplayName(fallback string) string {
	if strings.TrimSpace(p.Name) != "" {
		return strings.TrimSpace(p.Name)
	}
	if strings.TrimSpace(p.Nickname) != "" {
		return strings.TrimSpace(p.Nickname)
	}
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	return ""
}

const auth0DefaultScopes = "openid profile email"

func (s *Service) auth0Client() (Auth0DeviceFlow, error) {
	if s.Auth0 == nil {
		return nil, ErrAuth0NotConfigured
	}
	return s.Auth0, nil
}

func (s *Service) RequestAuth0DeviceCode(ctx context.Context) (auth0.DeviceCode, error) {
	c, err := s.auth0Client()
	if err != nil {
		slog.WarnContext(ctx, "auth0 device code request unavailable", "error", err)
		return auth0.DeviceCode{}, err
	}
	dc, err := c.RequestDeviceCode(ctx, auth0DefaultScopes)
	if err != nil {
		slog.WarnContext(ctx, "auth0 device code request failed", "error", err)
	}
	return dc, err
}

func (s *Service) ExchangeAuth0DeviceCode(ctx context.Context, deviceCode string) (Auth0Profile, error) {
	c, err := s.auth0Client()
	if err != nil {
		slog.WarnContext(ctx, "auth0 device code exchange unavailable", "error", err)
		return Auth0Profile{}, err
	}
	deviceCode = strings.TrimSpace(deviceCode)
	if deviceCode == "" {
		return Auth0Profile{}, fmt.Errorf("%w: device_code is required", ErrValidation)
	}

	tok, err := c.ExchangeDeviceCode(ctx, deviceCode)
	if err != nil {
		// Map Auth0 OAuth error codes to stable service-level errors.
		var oe auth0.OAuthError
		if errors.As(err, &oe) {
			switch oe.Code {
			case "authorization_pending":
				slog.InfoContext(ctx, "auth0 device authorization pending")
				return Auth0Profile{}, ErrAuth0Pending
			case "slow_down":
				slog.WarnContext(ctx, "auth0 requested slower polling")
				return Auth0Profile{}, ErrAuth0SlowDown
			case "expired_token":
				slog.WarnContext(ctx, "auth0 device code expired")
				return Auth0Profile{}, ErrAuth0Expired
			case "access_denied":
				slog.WarnContext(ctx, "auth0 device authorization denied")
				return Auth0Profile{}, ErrAuth0AccessDenied
			default:
				slog.WarnContext(ctx, "auth0 device code exchange failed", "error", err)
				return Auth0Profile{}, fmt.Errorf("auth0: %w", err)
			}
		}
		slog.WarnContext(ctx, "auth0 device code exchange failed", "error", err)
		return Auth0Profile{}, fmt.Errorf("auth0: %w", err)
	}
	if tok.IDToken == "" {
		slog.WarnContext(ctx, "auth0 exchange returned empty id_token")
		return Auth0Profile{}, errors.New("auth0: missing id_token")
	}

	return s.verifyAuth0IDToken(ctx, tok.IDToken)
}

func (s *Service) verifyAuth0IDToken(ctx context.Context, idToken string) (Auth0Profile, error) {
	c, err := s.auth0Client()
	if err != nil {
		slog.WarnContext(ctx, "auth0 id_token verification unavailable", "error", err)
		return Auth0Profile{}, err
	}
	idToken = strings.TrimSpace(idToken)
	if idToken == "" {
		return Auth0Profile{}, fmt.Errorf("%w: id_token is required", ErrValidation)
	}

	// Verify JWT signature using Auth0's JWKS and validate claims.
	claims, err := c.VerifyIDToken(ctx, idToken)
	if err != nil {
		slog.WarnContext(ctx, "auth0 id_token verification failed", "error", err)
		return Auth0Profile{}, fmt.Errorf("%w: invalid id_token", ErrValidation)
	}

	return Auth0Profile{
		Provider:          "auth0",
		Subject:           claims.Sub,
		Email:             strings.TrimSpace(claims.Email),
		EmailVerified:     claims.EmailVerified,
		Name:              strings.TrimSpace(claims.Name),
		Nickname:          strings.TrimSpace(claims.Nickname),
		PreferredUsername: strings.TrimSpace(claims.PreferredUsername),
		Picture:           strings.TrimSpace(claims.Picture),
	}, nil
}
