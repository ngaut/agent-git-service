package connectedlogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultProvider      = "connected"
	defaultLoginPath     = "/oauth/login"
	defaultCallbackPath  = "/auth/connected/callback"
	defaultTokenPath     = "/api/oauth/token"
	defaultUserinfoPath  = "/api/oauth/userinfo"
	defaultReturnToParam = "return_to"
)

type Config struct {
	Provider          string
	Origin            string
	APIOrigin         string
	ClientID          string
	ClientSecret      string
	CallbackBaseURL   string
	LoginPath         string
	CallbackPath      string
	TokenPath         string
	UserinfoPath      string
	ReturnToParam     string
	AllowInsecureHTTP bool
	HTTPClient        *http.Client

	SubjectClaim              string
	SubjectNamespaceClaim     string
	SubjectNamespaceSlugClaim string
	ActorTypeClaim            string
	HumanTypeValue            string
	AgentTypeValue            string
	ClientIDClaim             string
	ClientNameClaim           string
	PreferredUsernameClaim    string
	NameClaim                 string
	PictureClaim              string
	AvatarURLClaim            string
	DescriptionClaim          string
	ScopeClaim                string
}

func (c Config) Validate() error {
	missing := []string{}
	for _, item := range []struct {
		name  string
		value string
	}{
		{name: "Provider", value: c.Provider},
		{name: "Origin", value: c.Origin},
		{name: "APIOrigin", value: c.APIOrigin},
		{name: "ClientID", value: c.ClientID},
		{name: "ClientSecret", value: c.ClientSecret},
		{name: "CallbackBaseURL", value: c.CallbackBaseURL},
		{name: "LoginPath", value: c.LoginPath},
		{name: "CallbackPath", value: c.CallbackPath},
		{name: "TokenPath", value: c.TokenPath},
		{name: "UserinfoPath", value: c.UserinfoPath},
		{name: "ReturnToParam", value: c.ReturnToParam},
		{name: "SubjectClaim", value: c.SubjectClaim},
		{name: "ActorTypeClaim", value: c.ActorTypeClaim},
		{name: "HumanTypeValue", value: c.HumanTypeValue},
		{name: "AgentTypeValue", value: c.AgentTypeValue},
	} {
		if strings.TrimSpace(item.value) == "" {
			missing = append(missing, item.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("connectedlogin: missing config: %v", missing)
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
	for _, path := range []struct {
		name  string
		value string
	}{
		{name: "LoginPath", value: c.LoginPath},
		{name: "CallbackPath", value: c.CallbackPath},
		{name: "TokenPath", value: c.TokenPath},
		{name: "UserinfoPath", value: c.UserinfoPath},
	} {
		if err := validatePath(path.name, path.value); err != nil {
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
	cfg.Provider = firstNonEmpty(strings.TrimSpace(cfg.Provider), defaultProvider)
	cfg.Origin = strings.TrimSpace(cfg.Origin)
	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	cfg.ClientSecret = strings.TrimSpace(cfg.ClientSecret)
	cfg.CallbackBaseURL = strings.TrimSpace(cfg.CallbackBaseURL)
	cfg.LoginPath = normalizePath(cfg.LoginPath, defaultLoginPath)
	cfg.CallbackPath = normalizePath(cfg.CallbackPath, defaultCallbackPath)
	cfg.TokenPath = normalizePath(cfg.TokenPath, defaultTokenPath)
	cfg.UserinfoPath = normalizePath(cfg.UserinfoPath, defaultUserinfoPath)
	cfg.ReturnToParam = firstNonEmpty(strings.TrimSpace(cfg.ReturnToParam), defaultReturnToParam)
	cfg.SubjectClaim = firstNonEmpty(strings.TrimSpace(cfg.SubjectClaim), "sub")
	cfg.SubjectNamespaceClaim = strings.TrimSpace(cfg.SubjectNamespaceClaim)
	cfg.SubjectNamespaceSlugClaim = strings.TrimSpace(cfg.SubjectNamespaceSlugClaim)
	cfg.ActorTypeClaim = firstNonEmpty(strings.TrimSpace(cfg.ActorTypeClaim), "type")
	cfg.HumanTypeValue = firstNonEmpty(strings.TrimSpace(cfg.HumanTypeValue), "human")
	cfg.AgentTypeValue = firstNonEmpty(strings.TrimSpace(cfg.AgentTypeValue), "agent")
	cfg.ClientIDClaim = firstNonEmpty(strings.TrimSpace(cfg.ClientIDClaim), "client_id")
	cfg.ClientNameClaim = firstNonEmpty(strings.TrimSpace(cfg.ClientNameClaim), "client_name")
	cfg.PreferredUsernameClaim = firstNonEmpty(strings.TrimSpace(cfg.PreferredUsernameClaim), "preferred_username")
	cfg.NameClaim = firstNonEmpty(strings.TrimSpace(cfg.NameClaim), "name")
	cfg.PictureClaim = firstNonEmpty(strings.TrimSpace(cfg.PictureClaim), "picture")
	cfg.AvatarURLClaim = firstNonEmpty(strings.TrimSpace(cfg.AvatarURLClaim), "avatar_url")
	cfg.DescriptionClaim = firstNonEmpty(strings.TrimSpace(cfg.DescriptionClaim), "description")
	cfg.ScopeClaim = firstNonEmpty(strings.TrimSpace(cfg.ScopeClaim), "scope")
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{cfg: cfg, http: hc}, nil
}

func (c *Client) Provider() string { return c.cfg.Provider }

func (c *Client) ClientID() string { return c.cfg.ClientID }

func (c *Client) CallbackPath() string { return c.cfg.CallbackPath }

func (c *Client) CallbackURL() string {
	return strings.TrimRight(c.cfg.CallbackBaseURL, "/") + c.cfg.CallbackPath
}

func (c *Client) LoginURL(state string) string {
	q := url.Values{}
	q.Set("client_id", c.cfg.ClientID)
	q.Set(c.cfg.ReturnToParam, c.CallbackURL())
	if strings.TrimSpace(state) != "" {
		q.Set("state", strings.TrimSpace(state))
	}
	return strings.TrimRight(c.cfg.Origin, "/") + c.cfg.LoginPath + "?" + q.Encode()
}

type Token struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type,omitempty"`
	ExpiresIn   int    `json:"expires_in,omitempty"`
	Scope       string `json:"scope,omitempty"`
}

type Userinfo struct {
	Sub                  string
	Type                 string
	Scope                string
	ClientID             string
	ClientName           string
	SubjectNamespace     string
	SubjectNamespaceSlug string
	PreferredUsername    string
	Name                 string
	Picture              string
	AvatarURL            string
	Description          string
	RawClaims            map[string]any
}

type OAuthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description,omitempty"`
	Status      int    `json:"-"`
}

func (e OAuthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("connected login error %q: %s", e.Code, e.Description)
	}
	if e.Code != "" {
		return "connected login error: " + e.Code
	}
	return fmt.Sprintf("connected login http %d", e.Status)
}

func (c *Client) ExchangeCode(ctx context.Context, code string) (Token, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return Token{}, errors.New("connectedlogin: code is required")
	}
	body, err := json.Marshal(map[string]string{
		"grant_type": "authorization_code",
		"code":       code,
	})
	if err != nil {
		return Token{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL(c.cfg.TokenPath), bytes.NewReader(body))
	if err != nil {
		return Token{}, fmt.Errorf("connectedlogin: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.cfg.ClientID, c.cfg.ClientSecret)

	var tok Token
	if err := c.doJSON(req, &tok, "connectedlogin: token request failed"); err != nil {
		return Token{}, err
	}
	if strings.TrimSpace(tok.AccessToken) == "" {
		return Token{}, errors.New("connectedlogin: empty access_token in response")
	}
	return tok, nil
}

func (c *Client) Userinfo(ctx context.Context, accessToken string) (Userinfo, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return Userinfo{}, errors.New("connectedlogin: access_token is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL(c.cfg.UserinfoPath), nil)
	if err != nil {
		return Userinfo{}, fmt.Errorf("connectedlogin: build userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	var claims map[string]any
	if err := c.doJSON(req, &claims, "connectedlogin: userinfo request failed"); err != nil {
		return Userinfo{}, err
	}
	ui := c.userinfoFromClaims(claims)
	if strings.TrimSpace(ui.Sub) == "" {
		return Userinfo{}, fmt.Errorf("connectedlogin: empty %s in userinfo", c.cfg.SubjectClaim)
	}
	if c.cfg.SubjectNamespaceClaim != "" && strings.TrimSpace(ui.SubjectNamespace) == "" {
		return Userinfo{}, fmt.Errorf("connectedlogin: empty %s in userinfo", c.cfg.SubjectNamespaceClaim)
	}
	actorType := claimString(claims, c.cfg.ActorTypeClaim)
	if actorType != "" && actorType != c.cfg.HumanTypeValue && actorType != c.cfg.AgentTypeValue {
		return Userinfo{}, fmt.Errorf("connectedlogin: unexpected actor type %q in userinfo", actorType)
	}
	if ui.ClientID != "" && ui.ClientID != c.cfg.ClientID {
		return Userinfo{}, fmt.Errorf("connectedlogin: userinfo client_id mismatch: got %q", ui.ClientID)
	}
	return ui, nil
}

func (c *Client) userinfoFromClaims(claims map[string]any) Userinfo {
	actorType := claimString(claims, c.cfg.ActorTypeClaim)
	normalizedType := "human"
	if actorType == c.cfg.AgentTypeValue {
		normalizedType = "agent"
	} else if actorType != "" && actorType != c.cfg.HumanTypeValue {
		normalizedType = actorType
	}
	return Userinfo{
		Sub:                  claimString(claims, c.cfg.SubjectClaim),
		Type:                 normalizedType,
		Scope:                claimString(claims, c.cfg.ScopeClaim),
		ClientID:             claimString(claims, c.cfg.ClientIDClaim),
		ClientName:           claimString(claims, c.cfg.ClientNameClaim),
		SubjectNamespace:     claimString(claims, c.cfg.SubjectNamespaceClaim),
		SubjectNamespaceSlug: claimString(claims, c.cfg.SubjectNamespaceSlugClaim),
		PreferredUsername:    claimString(claims, c.cfg.PreferredUsernameClaim),
		Name:                 claimString(claims, c.cfg.NameClaim),
		Picture:              claimString(claims, c.cfg.PictureClaim),
		AvatarURL:            claimString(claims, c.cfg.AvatarURLClaim),
		Description:          claimString(claims, c.cfg.DescriptionClaim),
		RawClaims:            claims,
	}
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
		return fmt.Errorf("connectedlogin: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseOAuthError(resp.StatusCode, raw, genericMsg)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("connectedlogin: decode response: %w", err)
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
		return fmt.Errorf("connectedlogin: %s must be an absolute URL", name)
	}
	if !allowInsecure && u.Scheme != "https" {
		return fmt.Errorf("connectedlogin: %s must use https: %s", name, raw)
	}
	return nil
}

func validatePath(name, value string) error {
	if !strings.HasPrefix(value, "/") || strings.Contains(value, "://") {
		return fmt.Errorf("connectedlogin: %s must be an absolute URL path", name)
	}
	return nil
}

func normalizePath(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return value
}

func claimString(claims map[string]any, name string) string {
	name = strings.TrimSpace(name)
	if name == "" || claims == nil {
		return ""
	}
	value, ok := claims[name]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return strings.TrimSpace(v.String())
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strings.TrimSpace(strconv.FormatFloat(v, 'f', -1, 64))
	case bool:
		return strconv.FormatBool(v)
	default:
		return ""
	}
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
