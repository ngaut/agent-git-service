package oidc

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
	"sync"
	"time"

	"github.com/ngaut/agent-git-service/internal/auth0"
)

type Config struct {
	Provider          string
	Issuer            string
	DiscoveryURL      string
	ClientID          string
	ClientSecret      string
	Audience          string
	Scopes            string
	AllowInsecureHTTP bool
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Provider) == "" {
		return errors.New("provider is required")
	}
	if strings.TrimSpace(c.ClientID) == "" {
		return errors.New("client_id is required")
	}
	if strings.TrimSpace(c.DiscoveryURL) == "" && strings.TrimSpace(c.Issuer) == "" {
		return errors.New("issuer or discovery_url is required")
	}
	return nil
}

type DiscoveryDocument struct {
	Issuer                      string   `json:"issuer"`
	AuthorizationEndpoint       string   `json:"authorization_endpoint"`
	TokenEndpoint               string   `json:"token_endpoint"`
	DeviceAuthorizationEndpoint string   `json:"device_authorization_endpoint"`
	JWKSURI                     string   `json:"jwks_uri"`
	ScopesSupported             []string `json:"scopes_supported,omitempty"`
}

type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type Token struct {
	AccessToken string `json:"access_token,omitempty"`
	IDToken     string `json:"id_token,omitempty"`
	TokenType   string `json:"token_type,omitempty"`
	Scope       string `json:"scope,omitempty"`
	ExpiresIn   int    `json:"expires_in,omitempty"`
}

type OAuthError = auth0.OAuthError

type IDTokenClaims struct {
	Sub               string         `json:"sub"`
	Email             string         `json:"email,omitempty"`
	EmailVerified     bool           `json:"email_verified,omitempty"`
	Name              string         `json:"name,omitempty"`
	Nickname          string         `json:"nickname,omitempty"`
	PreferredUsername string         `json:"preferred_username,omitempty"`
	Picture           string         `json:"picture,omitempty"`
	Iss               string         `json:"iss,omitempty"`
	Aud               any            `json:"aud,omitempty"`
	Exp               int64          `json:"exp,omitempty"`
	RawClaims         map[string]any `json:"-"`
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
	}
	return false
}

func DecodeIDTokenClaims(idToken string) (IDTokenClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return IDTokenClaims{}, errors.New("invalid id_token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return IDTokenClaims{}, fmt.Errorf("decode id_token payload: %w", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return IDTokenClaims{}, fmt.Errorf("parse id_token payload: %w", err)
	}
	buf, err := json.Marshal(generic)
	if err != nil {
		return IDTokenClaims{}, err
	}
	var claims IDTokenClaims
	if err := json.Unmarshal(buf, &claims); err != nil {
		return IDTokenClaims{}, err
	}
	if claims.Sub == "" {
		return IDTokenClaims{}, errors.New("id_token missing sub")
	}
	claims.RawClaims = generic
	return claims, nil
}

type Client struct {
	provider          string
	issuer            string
	discoveryURL      string
	clientID          string
	clientSecret      string
	audience          string
	scopes            string
	allowInsecureHTTP bool
	http              *http.Client
	mu                sync.Mutex
	jwks              *auth0.JWKSClient
	discovery         DiscoveryDocument
}

func New(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	issuer := strings.TrimSpace(cfg.Issuer)
	discoveryURL := strings.TrimSpace(cfg.DiscoveryURL)
	if discoveryURL == "" && issuer != "" {
		discoveryURL = strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	}
	if !cfg.AllowInsecureHTTP {
		for _, raw := range []string{issuer, discoveryURL} {
			if raw == "" {
				continue
			}
			if !strings.HasPrefix(raw, "https://") {
				return nil, fmt.Errorf("oidc endpoint must use https: %s", raw)
			}
		}
	}
	return &Client{
		provider:          strings.TrimSpace(cfg.Provider),
		issuer:            issuer,
		discoveryURL:      discoveryURL,
		clientID:          strings.TrimSpace(cfg.ClientID),
		clientSecret:      strings.TrimSpace(cfg.ClientSecret),
		audience:          strings.TrimSpace(cfg.Audience),
		scopes:            firstNonEmpty(strings.TrimSpace(cfg.Scopes), "openid profile email"),
		allowInsecureHTTP: cfg.AllowInsecureHTTP,
		http:              &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (c *Client) Provider() string { return c.provider }
func (c *Client) Issuer() string   { return c.issuer }
func (c *Client) ClientID() string { return c.clientID }
func (c *Client) Scopes() string   { return c.scopes }

func (c *Client) RequestDeviceCode(ctx context.Context, scopes string) (DeviceCode, error) {
	doc, err := c.loadDiscovery(ctx)
	if err != nil {
		return DeviceCode{}, err
	}
	if strings.TrimSpace(doc.DeviceAuthorizationEndpoint) == "" {
		return DeviceCode{}, errors.New("oidc: device authorization endpoint not supported")
	}
	form := url.Values{}
	form.Set("client_id", c.clientID)
	form.Set("scope", firstNonEmpty(strings.TrimSpace(scopes), c.scopes))
	if c.audience != "" {
		form.Set("audience", c.audience)
	}
	return doForm[DeviceCode](ctx, c.http, doc.DeviceAuthorizationEndpoint, form, "oidc: device code request failed")
}

func (c *Client) ExchangeDeviceCode(ctx context.Context, deviceCode string) (Token, error) {
	doc, err := c.loadDiscovery(ctx)
	if err != nil {
		return Token{}, err
	}
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	form.Set("device_code", deviceCode)
	form.Set("client_id", c.clientID)
	if c.clientSecret != "" {
		form.Set("client_secret", c.clientSecret)
	}
	return doForm[Token](ctx, c.http, doc.TokenEndpoint, form, "oidc: token exchange failed")
}

func (c *Client) VerifyIDToken(ctx context.Context, idToken string) (IDTokenClaims, error) {
	doc, err := c.loadDiscovery(ctx)
	if err != nil {
		return IDTokenClaims{}, err
	}
	jwks := c.jwksClient(doc)
	auth0Claims, err := jwks.VerifyIDToken(ctx, idToken, doc.Issuer, c.clientID)
	if err != nil {
		return IDTokenClaims{}, err
	}
	claims, err := DecodeIDTokenClaims(idToken)
	if err != nil {
		return IDTokenClaims{}, err
	}
	claims.Sub = auth0Claims.Sub
	claims.Email = firstNonEmpty(strings.TrimSpace(claims.Email), strings.TrimSpace(auth0Claims.Email))
	claims.EmailVerified = claims.EmailVerified || auth0Claims.EmailVerified
	claims.Name = firstNonEmpty(strings.TrimSpace(claims.Name), strings.TrimSpace(auth0Claims.Name))
	claims.Nickname = firstNonEmpty(strings.TrimSpace(claims.Nickname), strings.TrimSpace(auth0Claims.Nickname))
	claims.PreferredUsername = firstNonEmpty(strings.TrimSpace(claims.PreferredUsername), strings.TrimSpace(auth0Claims.PreferredUsername))
	claims.Picture = firstNonEmpty(strings.TrimSpace(claims.Picture), strings.TrimSpace(auth0Claims.Picture))
	claims.Iss = auth0Claims.Iss
	claims.Aud = auth0Claims.Aud
	claims.Exp = auth0Claims.Exp
	return claims, nil
}

func (c *Client) loadDiscovery(ctx context.Context) (DiscoveryDocument, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.discovery.Issuer != "" {
		return c.discovery, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.discoveryURL, nil)
	if err != nil {
		return DiscoveryDocument{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return DiscoveryDocument{}, fmt.Errorf("oidc: fetch discovery: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return DiscoveryDocument{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return DiscoveryDocument{}, fmt.Errorf("oidc: discovery request failed: status=%d", resp.StatusCode)
	}
	var doc DiscoveryDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return DiscoveryDocument{}, fmt.Errorf("oidc: decode discovery response: %w", err)
	}
	configuredIssuer := normalizeIssuerForCompare(c.issuer)
	discoveredIssuer := normalizeIssuerForCompare(doc.Issuer)
	if configuredIssuer != "" && discoveredIssuer != "" && discoveredIssuer != configuredIssuer {
		return DiscoveryDocument{}, fmt.Errorf("oidc: discovery issuer mismatch: configured=%s discovered=%s", configuredIssuer, discoveredIssuer)
	}
	doc.Issuer = strings.TrimSpace(firstNonEmpty(doc.Issuer, c.issuer))
	if doc.Issuer == "" || doc.TokenEndpoint == "" {
		return DiscoveryDocument{}, errors.New("oidc: incomplete discovery document")
	}
	if !c.allowInsecureHTTP {
		for _, endpoint := range []string{
			doc.Issuer,
			doc.TokenEndpoint,
			doc.DeviceAuthorizationEndpoint,
			doc.JWKSURI,
		} {
			if err := validateSecureEndpoint(endpoint); err != nil {
				return DiscoveryDocument{}, err
			}
		}
	}
	c.discovery = doc
	return doc, nil
}

func (c *Client) jwksClient(doc DiscoveryDocument) *auth0.JWKSClient {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.jwks == nil {
		c.jwks = auth0.NewJWKSClient(doc.Issuer)
		if strings.TrimSpace(doc.JWKSURI) != "" {
			c.jwks.OverrideURL(doc.JWKSURI)
		}
	}
	return c.jwks
}

func doForm[T any](ctx context.Context, hc *http.Client, endpoint string, form url.Values, genericMsg string) (T, error) {
	var zero T
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return zero, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return zero, err
	}
	if resp.StatusCode != http.StatusOK {
		var oe OAuthError
		_ = json.Unmarshal(body, &oe)
		if oe.Code != "" {
			return zero, oe
		}
		return zero, fmt.Errorf("%s: status=%d", genericMsg, resp.StatusCode)
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		return zero, err
	}
	return out, nil
}

func normalizeIssuerForCompare(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	for strings.HasSuffix(raw, "/") {
		raw = strings.TrimSuffix(raw, "/")
	}
	return raw
}

func validateSecureEndpoint(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if !strings.HasPrefix(raw, "https://") {
		return fmt.Errorf("oidc endpoint must use https: %s", raw)
	}
	return nil
}

func firstNonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
