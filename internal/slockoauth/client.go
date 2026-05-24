// Package slockoauth implements the third-party Login-with-Slock OAuth2-like
// flow described in Ray's integration doc (msg=8b939914, 2026-05-23):
//
//	GET <SLOCK_ORIGIN>/login-with-slock/setup?client_id=...&return_to=...
//	POST <SLOCK_API_ORIGIN>/api/oauth/token        (Basic client_id:client_secret)
//	GET  <SLOCK_API_ORIGIN>/api/oauth/userinfo     (Bearer access_token)
//
// The client is intentionally tiny: just the two server-side calls. It does
// not store or persist the Slock access_token — gh-server mints its own token
// after the userinfo response is validated (Ray refinement #1, msg=5897ba9a).
package slockoauth

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

// Config holds the five env-driven settings for login-with-slock.
type Config struct {
	Origin       string // e.g. https://app.slock.ai
	APIOrigin    string // e.g. https://api.slock.ai
	ClientID     string
	ClientSecret string
	AppOrigin    string // this app's public origin, e.g. https://app.example.com
}

func (c Config) Validate() error {
	missing := []string{}
	if strings.TrimSpace(c.Origin) == "" {
		missing = append(missing, "Origin")
	}
	if strings.TrimSpace(c.APIOrigin) == "" {
		missing = append(missing, "APIOrigin")
	}
	if strings.TrimSpace(c.ClientID) == "" {
		missing = append(missing, "ClientID")
	}
	if strings.TrimSpace(c.ClientSecret) == "" {
		missing = append(missing, "ClientSecret")
	}
	if strings.TrimSpace(c.AppOrigin) == "" {
		missing = append(missing, "AppOrigin")
	}
	if len(missing) > 0 {
		return fmt.Errorf("slockoauth: missing config: %v", missing)
	}
	return nil
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (c *Client) ClientID() string  { return c.cfg.ClientID }
func (c *Client) Origin() string    { return c.cfg.Origin }
func (c *Client) APIOrigin() string { return c.cfg.APIOrigin }
func (c *Client) AppOrigin() string { return c.cfg.AppOrigin }

// CallbackURL returns the canonical callback URL registered with Slock.
// Must match the Connected App registration return URL exactly.
func (c *Client) CallbackURL() string {
	return strings.TrimRight(c.cfg.AppOrigin, "/") + "/auth/slock/callback"
}

// LoginURL builds the browser-facing setup URL the agent or human opens to
// start the flow.
func (c *Client) LoginURL() string {
	q := url.Values{}
	q.Set("client_id", c.cfg.ClientID)
	q.Set("return_to", c.CallbackURL())
	return strings.TrimRight(c.cfg.Origin, "/") + "/login-with-slock/setup?" + q.Encode()
}

// Token is the response body of POST /api/oauth/token.
type Token struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
}

// Userinfo is the response body of GET /api/oauth/userinfo.
//
// The `type` field carries the principal kind ("human" or "agent"). The true
// identity key is the composite (server_id, sub) — preferred_username is a
// display field only (Ray refinement #2, msg=5897ba9a).
type Userinfo struct {
	Sub               string  `json:"sub"`
	Type              string  `json:"type"`
	Scope             string  `json:"scope"`
	ClientID          string  `json:"client_id"`
	ClientName        string  `json:"client_name"`
	ServerID          string  `json:"server_id"`
	ServerSlug        string  `json:"server_slug"`
	ServerRole        *string `json:"server_role,omitempty"`
	PreferredUsername string  `json:"preferred_username"`
	Name              string  `json:"name"`
	AvatarURL         *string `json:"avatar_url"`
	Description       *string `json:"description"`
}

// OAuthError is the JSON error shape returned by Slock OAuth endpoints when
// the body parses as { "error": ..., "error_description": ... }.
type OAuthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description,omitempty"`
	Status      int    `json:"-"`
}

func (e OAuthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("slock oauth error %q: %s", e.Code, e.Description)
	}
	if e.Code != "" {
		return "slock oauth error: " + e.Code
	}
	return fmt.Sprintf("slock oauth http %d", e.Status)
}

// ExchangeCode trades a callback `code` for a Slock access token. The Slock
// access token is used once (to fetch userinfo) and then discarded — never
// stored in the gh-server DB.
func (c *Client) ExchangeCode(ctx context.Context, code string) (Token, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return Token{}, errors.New("slockoauth: code is required")
	}
	body, _ := json.Marshal(map[string]string{
		"grant_type": "authorization_code",
		"code":       code,
	})
	endpoint := strings.TrimRight(c.cfg.APIOrigin, "/") + "/api/oauth/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return Token{}, fmt.Errorf("slockoauth: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	basic := base64.StdEncoding.EncodeToString([]byte(c.cfg.ClientID + ":" + c.cfg.ClientSecret))
	req.Header.Set("Authorization", "Basic "+basic)

	resp, err := c.http.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("slockoauth: token request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Token{}, fmt.Errorf("slockoauth: read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var oe OAuthError
		_ = json.Unmarshal(raw, &oe)
		oe.Status = resp.StatusCode
		return Token{}, oe
	}
	var tok Token
	if err := json.Unmarshal(raw, &tok); err != nil {
		return Token{}, fmt.Errorf("slockoauth: decode token response: %w", err)
	}
	if strings.TrimSpace(tok.AccessToken) == "" {
		return Token{}, errors.New("slockoauth: empty access_token in response")
	}
	return tok, nil
}

// Userinfo calls the userinfo endpoint with a Bearer access token.
func (c *Client) Userinfo(ctx context.Context, accessToken string) (Userinfo, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return Userinfo{}, errors.New("slockoauth: access_token is required")
	}
	endpoint := strings.TrimRight(c.cfg.APIOrigin, "/") + "/api/oauth/userinfo"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Userinfo{}, fmt.Errorf("slockoauth: build userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return Userinfo{}, fmt.Errorf("slockoauth: userinfo request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Userinfo{}, fmt.Errorf("slockoauth: read userinfo response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var oe OAuthError
		_ = json.Unmarshal(raw, &oe)
		oe.Status = resp.StatusCode
		return Userinfo{}, oe
	}
	var ui Userinfo
	if err := json.Unmarshal(raw, &ui); err != nil {
		return Userinfo{}, fmt.Errorf("slockoauth: decode userinfo response: %w", err)
	}
	if strings.TrimSpace(ui.Sub) == "" {
		return Userinfo{}, errors.New("slockoauth: empty sub in userinfo")
	}
	if strings.TrimSpace(ui.ServerID) == "" {
		return Userinfo{}, errors.New("slockoauth: empty server_id in userinfo")
	}
	if ui.Type != "human" && ui.Type != "agent" {
		return Userinfo{}, fmt.Errorf("slockoauth: unexpected type %q in userinfo", ui.Type)
	}
	return ui, nil
}
