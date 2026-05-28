package slockoauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	loginPath    = "/login-with-slock/setup"
	callbackPath = "/auth/slock/callback"
	tokenPath    = "/api/oauth/token"
	userinfoPath = "/api/oauth/userinfo"
)

type Config struct {
	Origin            string
	APIOrigin         string
	ClientID          string
	ClientSecret      string
	CallbackBaseURL   string
	AllowInsecureHTTP bool
	HTTPClient        *http.Client
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
	if strings.TrimSpace(c.CallbackBaseURL) == "" {
		missing = append(missing, "CallbackBaseURL")
	}
	if len(missing) > 0 {
		return fmt.Errorf("slockoauth: missing config: %v", missing)
	}
	for _, endpoint := range []struct {
		name string
		raw  string
	}{
		{name: "Origin", raw: c.Origin},
		{name: "APIOrigin", raw: c.APIOrigin},
		{name: "CallbackBaseURL", raw: c.CallbackBaseURL},
	} {
		if err := validateBaseURL(endpoint.name, endpoint.raw, c.AllowInsecureHTTP); err != nil {
			return err
		}
	}
	return nil
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) (*Client, error) {
	cfg.Origin = strings.TrimSpace(cfg.Origin)
	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	cfg.ClientSecret = strings.TrimSpace(cfg.ClientSecret)
	cfg.CallbackBaseURL = strings.TrimSpace(cfg.CallbackBaseURL)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{cfg: cfg, http: hc}, nil
}

func (c *Client) ClientID() string { return c.cfg.ClientID }

func (c *Client) CallbackURL() string {
	return strings.TrimRight(c.cfg.CallbackBaseURL, "/") + callbackPath
}

func (c *Client) LoginURL(state string) string {
	q := url.Values{}
	q.Set("client_id", c.cfg.ClientID)
	q.Set("return_to", c.CallbackURL())
	if strings.TrimSpace(state) != "" {
		q.Set("state", strings.TrimSpace(state))
	}
	return strings.TrimRight(c.cfg.Origin, "/") + loginPath + "?" + q.Encode()
}

type Token struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type,omitempty"`
	ExpiresIn   int    `json:"expires_in,omitempty"`
	Scope       string `json:"scope,omitempty"`
}

type Userinfo struct {
	Sub               string  `json:"sub"`
	Type              string  `json:"type"`
	Scope             string  `json:"scope,omitempty"`
	ClientID          string  `json:"client_id,omitempty"`
	ClientName        string  `json:"client_name,omitempty"`
	ServerID          string  `json:"server_id"`
	ServerSlug        string  `json:"server_slug,omitempty"`
	ServerRole        *string `json:"server_role,omitempty"`
	PreferredUsername string  `json:"preferred_username,omitempty"`
	Name              string  `json:"name,omitempty"`
	Picture           *string `json:"picture,omitempty"`
	AvatarURL         *string `json:"avatar_url,omitempty"`
	Description       *string `json:"description,omitempty"`
}

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

func (c *Client) ExchangeCode(ctx context.Context, code string) (Token, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return Token{}, errors.New("slockoauth: code is required")
	}
	body, err := json.Marshal(map[string]string{
		"grant_type": "authorization_code",
		"code":       code,
	})
	if err != nil {
		return Token{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL(tokenPath), bytes.NewReader(body))
	if err != nil {
		return Token{}, fmt.Errorf("slockoauth: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.cfg.ClientID, c.cfg.ClientSecret)

	var tok Token
	if err := c.doJSON(req, &tok, "slockoauth: token request failed"); err != nil {
		return Token{}, err
	}
	if strings.TrimSpace(tok.AccessToken) == "" {
		return Token{}, errors.New("slockoauth: empty access_token in response")
	}
	return tok, nil
}

func (c *Client) Userinfo(ctx context.Context, accessToken string) (Userinfo, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return Userinfo{}, errors.New("slockoauth: access_token is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL(userinfoPath), nil)
	if err != nil {
		return Userinfo{}, fmt.Errorf("slockoauth: build userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	var ui Userinfo
	if err := c.doJSON(req, &ui, "slockoauth: userinfo request failed"); err != nil {
		return Userinfo{}, err
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
	if ui.ClientID != "" && ui.ClientID != c.cfg.ClientID {
		return Userinfo{}, fmt.Errorf("slockoauth: userinfo client_id mismatch: got %q", ui.ClientID)
	}
	return ui, nil
}

func (c *Client) apiURL(path string) string {
	return strings.TrimRight(c.cfg.APIOrigin, "/") + path
}

func (c *Client) doJSON(req *http.Request, out any, genericMsg string) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", genericMsg, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("slockoauth: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseOAuthError(resp.StatusCode, raw, genericMsg)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("slockoauth: decode response: %w", err)
	}
	return nil
}

func parseOAuthError(status int, raw []byte, genericMsg string) error {
	var oe OAuthError
	_ = json.Unmarshal(raw, &oe)
	oe.Status = status
	if oe.Code != "" {
		return oe
	}
	return fmt.Errorf("%s: status=%d", genericMsg, status)
}

func validateBaseURL(name, raw string, allowInsecure bool) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("slockoauth: %s must be an absolute URL", name)
	}
	if !allowInsecure && u.Scheme != "https" {
		return fmt.Errorf("slockoauth: %s must use https: %s", name, raw)
	}
	return nil
}
