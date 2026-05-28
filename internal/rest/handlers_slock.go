package rest

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/randutil"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/slockoauth"
)

const slockOAuthStateCookieName = "slock_oauth_state"
const slockOAuthVerifierCookieName = "slock_oauth_verifier"

func (d *Deps) SlockLogin(w http.ResponseWriter, r *http.Request) {
	state := randutil.Hex(32)
	loginURL, err := d.Svc.SlockLoginURL(state)
	if err != nil {
		if errors.Is(err, service.ErrSlockNotConfigured) {
			respond.Error(w, http.StatusNotImplemented, "login with slock is not configured")
			return
		}
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	http.SetCookie(w, slockOAuthStateCookie(state, r, false))
	http.Redirect(w, r, loginURL, http.StatusFound)
}

func (d *Deps) SlockCallback(w http.ResponseWriter, r *http.Request) {
	clearStateCookie := func() {
		http.SetCookie(w, slockOAuthStateCookie("", r, true))
	}

	state := strings.TrimSpace(r.URL.Query().Get("state"))
	cookie, err := r.Cookie(slockOAuthStateCookieName)
	stateValidated := false
	if err == nil {
		if state == "" || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(cookie.Value)), []byte(state)) != 1 {
			clearStateCookie()
			respond.ValidationFailed(w, "invalid or missing state")
			return
		}
		stateValidated = true
		clearStateCookie()
	}

	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		if oauthErr := strings.TrimSpace(r.URL.Query().Get("error")); oauthErr != "" {
			respond.Error(w, http.StatusBadRequest, "slock oauth error: "+oauthErr)
			return
		}
		respond.ValidationFailed(w, "code is required")
		return
	}

	res, err := d.Svc.SlockLoginWithCode(r.Context(), code)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrSlockNotConfigured):
			respond.Error(w, http.StatusNotImplemented, "login with slock is not configured")
		case errors.Is(err, service.ErrValidation):
			respond.ValidationFailed(w, err.Error())
		default:
			var oe slockoauth.OAuthError
			if errors.As(err, &oe) {
				respond.Error(w, http.StatusBadGateway, oe.Error())
				return
			}
			respond.ServiceErrorRequest(r, w, err)
		}
		return
	}

	if !stateValidated || slockCallbackWantsTokenJSON(r) {
		respond.JSON(w, http.StatusOK, map[string]any{
			"token":     res.Token,
			"user_id":   res.UserID,
			"login":     res.Login,
			"type":      res.Type,
			"sub":       res.Sub,
			"server_id": res.ServerID,
		})
		return
	}

	authCode, codeVerifier, err := d.createSlockConsoleAuthorizationCode(r, res)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if err := d.Svc.DeleteTokenByValue(r.Context(), res.UserID, res.Token); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	target, ok := d.slockConsoleRedirectURL(authCode, res)
	if !ok {
		respond.JSON(w, http.StatusOK, map[string]any{
			"code":       authCode,
			"expires_in": int(service.AuthorizationCodeTTL / time.Second),
			"user_id":    res.UserID,
			"login":      res.Login,
			"type":       res.Type,
			"sub":        res.Sub,
			"server_id":  res.ServerID,
		})
		return
	}
	http.SetCookie(w, slockOAuthVerifierCookie(codeVerifier, r, false))
	http.Redirect(w, r, target, http.StatusFound)
}

func slockCallbackWantsTokenJSON(r *http.Request) bool {
	if r == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "json") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("response")), "token") {
		return true
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/html")
}

func slockOAuthStateCookie(value string, r *http.Request, expire bool) *http.Cookie {
	cookie := &http.Cookie{
		Name:     slockOAuthStateCookieName,
		Value:    value,
		Path:     "/auth/slock",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   slockOAuthCookieSecure(r),
	}
	if expire {
		cookie.MaxAge = -1
	}
	return cookie
}

func slockOAuthVerifierCookie(value string, r *http.Request, expire bool) *http.Cookie {
	cookie := &http.Cookie{
		Name:     slockOAuthVerifierCookieName,
		Value:    value,
		Path:     "/login/oauth/access_token",
		HttpOnly: true,
		SameSite: http.SameSiteNoneMode,
		Secure:   slockOAuthCookieSecure(r),
	}
	if expire {
		cookie.MaxAge = -1
	}
	return cookie
}

func slockOAuthCookieSecure(r *http.Request) bool {
	if r != nil {
		if r.TLS != nil {
			return true
		}
		if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
			return true
		}
	}
	return false
}

func (d *Deps) createSlockConsoleAuthorizationCode(r *http.Request, res service.SlockSessionResult) (string, string, error) {
	now := time.Now().UTC()
	codeVerifier := randutil.Hex(64)
	sum := sha256.Sum256([]byte(codeVerifier))
	code := &db.AuthorizationCode{
		Code:                randutil.Hex(32),
		UserID:              &res.UserID,
		RedirectURI:         d.slockAuthorizationCodeRedirectURI(r),
		CodeChallenge:       base64.RawURLEncoding.EncodeToString(sum[:]),
		CodeChallengeMethod: "S256",
		ExpiresAt:           now.Add(service.AuthorizationCodeTTL),
		CreatedAt:           now,
	}
	if err := d.Svc.CreateAuthorizationCode(r.Context(), code); err != nil {
		return "", "", err
	}
	return code.Code, codeVerifier, nil
}

func (d *Deps) slockAuthorizationCodeRedirectURI(r *http.Request) string {
	if base := strings.TrimSpace(d.ConsoleBaseURL); base != "" {
		return base
	}
	baseURL := strings.TrimRight(strings.TrimSpace(d.Svc.BaseURL), "/")
	if baseURL == "" {
		return "urn:ags:slock-console"
	}
	return baseURL + "/auth/slock/callback"
}

func (d *Deps) slockConsoleRedirectURL(authCode string, res service.SlockSessionResult) (string, bool) {
	base := strings.TrimSpace(d.ConsoleBaseURL)
	if base == "" {
		return "", false
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	q := u.Query()
	q.Set("code", authCode)
	q.Set("login", res.Login)
	q.Set("user_id", fmt.Sprintf("%d", res.UserID))
	q.Set("type", res.Type)
	q.Set("sub", res.Sub)
	q.Set("server_id", res.ServerID)
	u.RawQuery = q.Encode()
	return u.String(), true
}
