// Package auth0 provides a minimal Auth0 OAuth2 Device Authorization client.
//
// This is used to proxy a device-code login flow through gh-server:
// - gh-server requests a device_code from Auth0
// - the client opens the verification URI and authenticates with Auth0
// - the client polls gh-server, and gh-server exchanges the device_code for tokens
package auth0

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	Issuer   string // e.g. https://example.us.auth0.com/
	ClientID string
	Audience string // optional
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Issuer) == "" {
		return errors.New("issuer is required")
	}
	if strings.TrimSpace(c.ClientID) == "" {
		return errors.New("client_id is required")
	}
	return nil
}

type Client struct {
	issuer   string
	clientID string
	audience string
	http     *http.Client
	jwks     *JWKSClient
}

func New(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	issuer := strings.TrimSpace(cfg.Issuer)
	if !strings.HasPrefix(issuer, "https://") && !strings.HasPrefix(issuer, "http://") {
		return nil, fmt.Errorf("issuer must be a URL, got %q", cfg.Issuer)
	}
	if !strings.HasSuffix(issuer, "/") {
		issuer += "/"
	}
	return &Client{
		issuer:   issuer,
		clientID: strings.TrimSpace(cfg.ClientID),
		audience: strings.TrimSpace(cfg.Audience),
		http:     &http.Client{Timeout: 15 * time.Second},
		jwks:     NewJWKSClient(issuer),
	}, nil
}

func (c *Client) Issuer() string   { return c.issuer }
func (c *Client) ClientID() string { return c.clientID }

type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// OAuthError matches Auth0's OAuth error body shape.
type OAuthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description,omitempty"`
}

func (e OAuthError) Error() string {
	if e.Description == "" {
		return e.Code
	}
	return e.Code + ": " + e.Description
}

func joinIssuer(issuer, p string) string {
	return strings.TrimRight(issuer, "/") + "/" + strings.TrimLeft(p, "/")
}

// RequestDeviceCode calls Auth0's /oauth/device/code endpoint.
// scopes is space-delimited per OAuth2 conventions (e.g. "openid profile email").
func (c *Client) RequestDeviceCode(ctx context.Context, scopes string) (DeviceCode, error) {
	form := url.Values{}
	form.Set("client_id", c.clientID)
	if strings.TrimSpace(scopes) != "" {
		form.Set("scope", scopes)
	}
	if c.audience != "" {
		form.Set("audience", c.audience)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinIssuer(c.issuer, "oauth/device/code"), strings.NewReader(form.Encode()))
	if err != nil {
		return DeviceCode{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return DeviceCode{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return DeviceCode{}, err
	}
	if resp.StatusCode != http.StatusOK {
		var oe OAuthError
		_ = json.Unmarshal(body, &oe)
		if oe.Code == "" {
			return DeviceCode{}, fmt.Errorf("auth0: device code request failed: status=%d", resp.StatusCode)
		}
		return DeviceCode{}, oe
	}

	var dc DeviceCode
	if err := json.Unmarshal(body, &dc); err != nil {
		return DeviceCode{}, fmt.Errorf("auth0: decode device code response: %w", err)
	}
	if dc.DeviceCode == "" || dc.UserCode == "" || dc.VerificationURI == "" {
		return DeviceCode{}, errors.New("auth0: incomplete device code response")
	}
	return dc, nil
}

type Token struct {
	AccessToken string `json:"access_token,omitempty"`
	IDToken     string `json:"id_token,omitempty"`
	TokenType   string `json:"token_type,omitempty"`
	Scope       string `json:"scope,omitempty"`
	ExpiresIn   int    `json:"expires_in,omitempty"`
}

// ExchangeDeviceCode calls Auth0's /oauth/token endpoint for the device_code grant.
func (c *Client) ExchangeDeviceCode(ctx context.Context, deviceCode string) (Token, error) {
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	form.Set("device_code", deviceCode)
	form.Set("client_id", c.clientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinIssuer(c.issuer, "oauth/token"), strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return Token{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Token{}, err
	}
	if resp.StatusCode != http.StatusOK {
		var oe OAuthError
		_ = json.Unmarshal(body, &oe)
		if oe.Code == "" {
			return Token{}, fmt.Errorf("auth0: token exchange failed: status=%d", resp.StatusCode)
		}
		return Token{}, oe
	}

	var tok Token
	if err := json.Unmarshal(body, &tok); err != nil {
		return Token{}, fmt.Errorf("auth0: decode token response: %w", err)
	}
	return tok, nil
}

type IDTokenClaims struct {
	Sub               string `json:"sub"`
	Email             string `json:"email,omitempty"`
	EmailVerified     bool   `json:"email_verified,omitempty"`
	Name              string `json:"name,omitempty"`
	Nickname          string `json:"nickname,omitempty"`
	PreferredUsername string `json:"preferred_username,omitempty"`
	Picture           string `json:"picture,omitempty"`

	Iss string `json:"iss,omitempty"`
	Aud any    `json:"aud,omitempty"`
	Exp int64  `json:"exp,omitempty"`
}

func (c IDTokenClaims) AudienceContains(clientID string) bool {
	switch v := c.Aud.(type) {
	case string:
		return v == clientID
	case []any:
		for _, it := range v {
			if s, ok := it.(string); ok && s == clientID {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// DecodeIDTokenClaims decodes the JWT payload (without signature verification).
// Deprecated: Use Client.VerifyIDToken for production use with signature verification.
// This function is retained only for testing with fake tokens.
func DecodeIDTokenClaims(idToken string) (IDTokenClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return IDTokenClaims{}, errors.New("invalid id_token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return IDTokenClaims{}, fmt.Errorf("decode id_token payload: %w", err)
	}
	var claims IDTokenClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return IDTokenClaims{}, fmt.Errorf("parse id_token payload: %w", err)
	}
	if claims.Sub == "" {
		return IDTokenClaims{}, errors.New("id_token missing sub")
	}
	return claims, nil
}

// VerifyIDToken verifies the JWT signature using Auth0's JWKS and returns the claims.
func (c *Client) VerifyIDToken(ctx context.Context, idToken string) (IDTokenClaims, error) {
	return c.jwks.VerifyIDToken(ctx, idToken, c.issuer, c.clientID)
}
