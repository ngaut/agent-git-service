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

	"github.com/ngaut/agent-git-service/internal/connectedlogin"
	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/randutil"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/service"
)

const connectedLoginStateCookieName = "connected_login_state"
const connectedLoginVerifierCookieName = "connected_login_verifier"

func (d *Deps) ConnectedLogin(w http.ResponseWriter, r *http.Request) {
	state := randutil.Hex(32)
	loginURL, err := d.Svc.ConnectedLoginURL(state)
	if err != nil {
		if errors.Is(err, service.ErrConnectedLoginNotConfigured) {
			respond.Error(w, http.StatusNotImplemented, "connected login is not configured")
			return
		}
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	http.SetCookie(w, connectedLoginStateCookie(state, r, false))
	http.Redirect(w, r, loginURL, http.StatusFound)
}

func (d *Deps) ConnectedCallback(w http.ResponseWriter, r *http.Request) {
	clearStateCookie := func() {
		http.SetCookie(w, connectedLoginStateCookie("", r, true))
	}

	state := strings.TrimSpace(r.URL.Query().Get("state"))
	cookie, err := r.Cookie(connectedLoginStateCookieName)
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
			respond.Error(w, http.StatusBadRequest, "connected login error: "+oauthErr)
			return
		}
		respond.ValidationFailed(w, "code is required")
		return
	}

	res, err := d.Svc.ConnectedLoginWithCode(r.Context(), code)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrConnectedLoginNotConfigured):
			respond.Error(w, http.StatusNotImplemented, "connected login is not configured")
		case errors.Is(err, service.ErrValidation):
			respond.ValidationFailed(w, err.Error())
		default:
			var oe connectedlogin.OAuthError
			if errors.As(err, &oe) {
				respond.Error(w, http.StatusBadGateway, oe.Error())
				return
			}
			respond.ServiceErrorRequest(r, w, err)
		}
		return
	}

	if !stateValidated {
		respond.JSON(w, http.StatusOK, connectedSessionResponse(res, map[string]any{
			"token": res.Token,
		}))
		return
	}

	authCode, codeVerifier, err := d.createConnectedConsoleAuthorizationCode(r, res)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if err := d.Svc.DeleteTokenByValue(r.Context(), res.UserID, res.Token); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	target, ok := d.connectedConsoleRedirectURL(authCode, res)
	if !ok {
		respond.JSON(w, http.StatusOK, connectedSessionResponse(res, map[string]any{
			"code":       authCode,
			"expires_in": int(service.AuthorizationCodeTTL / time.Second),
		}))
		return
	}
	http.SetCookie(w, connectedLoginVerifierCookie(codeVerifier, r, false))
	http.Redirect(w, r, target, http.StatusFound)
}

func connectedSessionResponse(res service.ConnectedSessionResult, base map[string]any) map[string]any {
	base["user_id"] = res.UserID
	base["login"] = res.Login
	base["type"] = res.Type
	base["sub"] = res.Sub
	if res.SubjectNamespace != "" {
		base["subject_namespace"] = res.SubjectNamespace
		if alias := connectedSubjectNamespaceAlias(res); alias != "" {
			if _, exists := base[alias]; !exists {
				base[alias] = res.SubjectNamespace
			}
		}
	}
	return base
}

func connectedSubjectNamespaceAlias(res service.ConnectedSessionResult) string {
	alias := strings.TrimSpace(res.SubjectNamespaceClaim)
	switch alias {
	case "", "subject_namespace", "code", "expires_in", "login", "sub", "token", "type", "user_id":
		return ""
	default:
		return alias
	}
}

func connectedLoginStateCookie(value string, r *http.Request, expire bool) *http.Cookie {
	cookie := &http.Cookie{
		Name:     connectedLoginStateCookieName,
		Value:    value,
		Path:     "/auth/connected",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   connectedLoginCookieSecure(r),
	}
	if expire {
		cookie.MaxAge = -1
	}
	return cookie
}

func connectedLoginVerifierCookie(value string, r *http.Request, expire bool) *http.Cookie {
	cookie := &http.Cookie{
		Name:     connectedLoginVerifierCookieName,
		Value:    value,
		Path:     "/login/oauth/access_token",
		HttpOnly: true,
		SameSite: http.SameSiteNoneMode,
		Secure:   connectedLoginCookieSecure(r),
	}
	if expire {
		cookie.MaxAge = -1
	}
	return cookie
}

func connectedLoginCookieSecure(r *http.Request) bool {
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

func (d *Deps) createConnectedConsoleAuthorizationCode(r *http.Request, res service.ConnectedSessionResult) (string, string, error) {
	now := time.Now().UTC()
	codeVerifier := randutil.Hex(64)
	sum := sha256.Sum256([]byte(codeVerifier))
	code := &db.AuthorizationCode{
		Code:                randutil.Hex(32),
		UserID:              &res.UserID,
		RedirectURI:         d.connectedAuthorizationCodeRedirectURI(r),
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

func (d *Deps) connectedAuthorizationCodeRedirectURI(r *http.Request) string {
	if base := strings.TrimSpace(d.ConsoleBaseURL); base != "" {
		return base
	}
	baseURL := strings.TrimRight(strings.TrimSpace(d.Svc.BaseURL), "/")
	if baseURL == "" {
		return "urn:ags:connected-console"
	}
	return baseURL + "/auth/connected/callback"
}

func (d *Deps) connectedConsoleRedirectURL(authCode string, res service.ConnectedSessionResult) (string, bool) {
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
	if res.SubjectNamespace != "" {
		q.Set("subject_namespace", res.SubjectNamespace)
		if alias := connectedSubjectNamespaceAlias(res); alias != "" {
			q.Set(alias, res.SubjectNamespace)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), true
}
