package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ngaut/agent-git-service/internal/auth0"
	"github.com/ngaut/agent-git-service/internal/oidc"
)

type OIDCProvider interface {
	RequestDeviceCode(ctx context.Context, scopes string) (oidc.DeviceCode, error)
	ExchangeDeviceCode(ctx context.Context, deviceCode string) (oidc.Token, error)
	VerifyIDToken(ctx context.Context, idToken string) (oidc.IDTokenClaims, error)
	Provider() string
	Issuer() string
	ClientID() string
	Scopes() string
}

type OIDCProfile struct {
	Provider          string
	Subject           string
	Email             string
	EmailVerified     bool
	Name              string
	Nickname          string
	PreferredUsername string
	Picture           string
	RawClaims         map[string]any
}

func (p OIDCProfile) DisplayName(fallback string) string {
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

func (s *Service) oidcClient() (OIDCProvider, error) {
	if s.OIDC == nil {
		return nil, ErrAuth0NotConfigured
	}
	return s.OIDC, nil
}

func (s *Service) RequestOIDCDeviceCode(ctx context.Context) (oidc.DeviceCode, error) {
	c, err := s.oidcClient()
	if err != nil {
		slog.WarnContext(ctx, "oidc device code request unavailable", "error", err)
		return oidc.DeviceCode{}, err
	}
	return c.RequestDeviceCode(ctx, c.Scopes())
}

func (s *Service) ExchangeOIDCDeviceCode(ctx context.Context, deviceCode string) (OIDCProfile, error) {
	c, err := s.oidcClient()
	if err != nil {
		return OIDCProfile{}, err
	}
	deviceCode = strings.TrimSpace(deviceCode)
	if deviceCode == "" {
		return OIDCProfile{}, fmt.Errorf("%w: device_code is required", ErrValidation)
	}
	tok, err := c.ExchangeDeviceCode(ctx, deviceCode)
	if err != nil {
		var oe auth0.OAuthError
		if errors.As(err, &oe) {
			switch oe.Code {
			case "authorization_pending":
				return OIDCProfile{}, ErrAuth0Pending
			case "slow_down":
				return OIDCProfile{}, ErrAuth0SlowDown
			case "expired_token":
				return OIDCProfile{}, ErrAuth0Expired
			case "access_denied":
				return OIDCProfile{}, ErrAuth0AccessDenied
			}
		}
		return OIDCProfile{}, fmt.Errorf("oidc: %w", err)
	}
	if tok.IDToken == "" {
		return OIDCProfile{}, errors.New("oidc: missing id_token")
	}
	return s.verifyOIDCIDToken(ctx, tok.IDToken)
}

func (s *Service) verifyOIDCIDToken(ctx context.Context, idToken string) (OIDCProfile, error) {
	c, err := s.oidcClient()
	if err != nil {
		return OIDCProfile{}, err
	}
	idToken = strings.TrimSpace(idToken)
	if idToken == "" {
		return OIDCProfile{}, fmt.Errorf("%w: id_token is required", ErrValidation)
	}
	claims, err := c.VerifyIDToken(ctx, idToken)
	if err != nil {
		return OIDCProfile{}, fmt.Errorf("%w: invalid id_token", ErrValidation)
	}
	name := strings.TrimSpace(claims.Name)
	if name == "" {
		if displayName, ok := claims.RawClaims["displayName"].(string); ok {
			name = strings.TrimSpace(displayName)
		}
	}
	picture := strings.TrimSpace(claims.Picture)
	if picture == "" {
		if avatar, ok := claims.RawClaims["avatar"].(string); ok {
			picture = strings.TrimSpace(avatar)
		}
	}
	return OIDCProfile{
		Provider:          c.Provider(),
		Subject:           claims.Sub,
		Email:             strings.TrimSpace(claims.Email),
		EmailVerified:     claims.EmailVerified,
		Name:              name,
		Nickname:          strings.TrimSpace(claims.Nickname),
		PreferredUsername: strings.TrimSpace(claims.PreferredUsername),
		Picture:           picture,
		RawClaims:         claims.RawClaims,
	}, nil
}

type auth0CompatFlow struct{ oidc OIDCProvider }

func NewAuth0CompatFlow(provider OIDCProvider) Auth0DeviceFlow {
	return auth0CompatFlow{oidc: provider}
}

func (a auth0CompatFlow) Issuer() string   { return a.oidc.Issuer() }
func (a auth0CompatFlow) ClientID() string { return a.oidc.ClientID() }
func (a auth0CompatFlow) RequestDeviceCode(ctx context.Context, scopes string) (auth0.DeviceCode, error) {
	dc, err := a.oidc.RequestDeviceCode(ctx, scopes)
	return auth0.DeviceCode(dc), err
}
func (a auth0CompatFlow) ExchangeDeviceCode(ctx context.Context, deviceCode string) (auth0.Token, error) {
	tok, err := a.oidc.ExchangeDeviceCode(ctx, deviceCode)
	return auth0.Token(tok), err
}
func (a auth0CompatFlow) VerifyIDToken(ctx context.Context, idToken string) (auth0.IDTokenClaims, error) {
	claims, err := a.oidc.VerifyIDToken(ctx, idToken)
	if err != nil {
		return auth0.IDTokenClaims{}, err
	}
	return auth0.IDTokenClaims{
		Sub:               claims.Sub,
		Email:             claims.Email,
		EmailVerified:     claims.EmailVerified,
		Name:              claims.Name,
		Nickname:          claims.Nickname,
		PreferredUsername: claims.PreferredUsername,
		Picture:           claims.Picture,
		Iss:               claims.Iss,
		Aud:               claims.Aud,
		Exp:               claims.Exp,
	}, nil
}
