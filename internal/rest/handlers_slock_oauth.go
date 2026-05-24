package rest

import (
	"errors"
	"net/http"
	"strings"

	"gh-server/internal/rest/respond"
	"gh-server/internal/service"
	"gh-server/internal/slockoauth"
)

// SlockLogin handles GET /auth/slock/login.
// It 302-redirects the browser (or agent's chosen client) to the Slock setup
// URL, which after authentication redirects back to the registered callback.
func (d *Deps) SlockLogin(w http.ResponseWriter, r *http.Request) {
	if d.Svc.SlockOAuth == nil {
		respond.Error(w, http.StatusNotImplemented, "login with slock is not configured")
		return
	}
	http.Redirect(w, r, d.Svc.SlockOAuth.LoginURL(), http.StatusFound)
}

// SlockCallback handles GET /auth/slock/callback.
//
// Flow (per Ray's integration doc msg=8b939914 + refinements msg=5897ba9a):
//  1. read `code` from query
//  2. POST <SLOCK_API_ORIGIN>/api/oauth/token (Basic client_id:client_secret)
//  3. GET  <SLOCK_API_ORIGIN>/api/oauth/userinfo (Bearer access_token)
//  4. upsert local user via UserIdentity(Provider="slock",Subject="<server_id>:<sub>")
//  5. mint a gh-server token (db.Token) and return as JSON
//
// The Slock access token is never stored. The callback `code` is never stored.
func (d *Deps) SlockCallback(w http.ResponseWriter, r *http.Request) {
	if d.Svc.SlockOAuth == nil {
		respond.Error(w, http.StatusNotImplemented, "login with slock is not configured")
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		if e := strings.TrimSpace(r.URL.Query().Get("error")); e != "" {
			respond.Error(w, http.StatusBadRequest, "slock oauth error: "+e)
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
			return
		case errors.Is(err, service.ErrValidation):
			respond.ValidationFailed(w, err.Error())
			return
		}
		var oe slockoauth.OAuthError
		if errors.As(err, &oe) {
			respond.Error(w, http.StatusBadGateway, oe.Error())
			return
		}
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	respond.JSON(w, http.StatusOK, map[string]any{
		"token":     res.Token,
		"user_id":   res.UserID,
		"login":     res.Login,
		"type":      res.Type,
		"sub":       res.Sub,
		"server_id": res.ServerID,
	})
}
