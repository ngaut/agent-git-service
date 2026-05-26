package rest

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ngaut/agent-git-service/internal/auth0"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/service"
)

// Auth0DeviceCode handles POST /api/v3/auth0/device/code (no auth).
func (d *Deps) Auth0DeviceCode(w http.ResponseWriter, r *http.Request) {
	dc, err := d.Svc.RequestAuth0DeviceCode(r.Context())
	if err != nil {
		if errors.Is(err, service.ErrAuth0NotConfigured) {
			respond.Error(w, http.StatusNotImplemented, "Auth0 is not configured")
			return
		}
		var oe auth0.OAuthError
		if errors.As(err, &oe) {
			respond.Error(w, http.StatusBadGateway, "Auth0 error: "+oe.Error())
			return
		}
		respond.Error(w, http.StatusBadGateway, "Auth0 request failed: "+err.Error())
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{
		"device_code":               dc.DeviceCode,
		"user_code":                 dc.UserCode,
		"verification_uri":          dc.VerificationURI,
		"verification_uri_complete": dc.VerificationURIComplete,
		"expires_in":                dc.ExpiresIn,
		"interval":                  dc.Interval,
	})
}

// Auth0Session handles POST /api/v3/auth0/session (no auth).
// Clients poll this endpoint until the user authorizes the device in the browser.
func (d *Deps) Auth0Session(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceCode string `json:"device_code"`
	}
	if err := decodeBodyStrict(r, &body); err != nil || strings.TrimSpace(body.DeviceCode) == "" {
		respond.ValidationFailed(w, "device_code is required")
		return
	}

	res, err := d.Svc.Auth0Login(r.Context(), body.DeviceCode)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrAuth0NotConfigured):
			respond.Error(w, http.StatusNotImplemented, "Auth0 is not configured")
		case errors.Is(err, service.ErrAuth0Pending):
			respond.JSON(w, http.StatusAccepted, map[string]any{"status": "authorization_pending"})
		case errors.Is(err, service.ErrAuth0SlowDown):
			respond.JSON(w, http.StatusAccepted, map[string]any{"status": "slow_down"})
		case errors.Is(err, service.ErrAuth0Expired):
			respond.ValidationFailed(w, "device_code expired")
		case errors.Is(err, service.ErrAuth0AccessDenied):
			respond.Forbidden(w, "access denied")
		default:
			var oe auth0.OAuthError
			if errors.As(err, &oe) {
				respond.Error(w, http.StatusBadGateway, "Auth0 error: "+oe.Error())
				return
			}
			respond.ServiceErrorRequest(r, w, err)
		}
		return
	}

	respond.JSON(w, http.StatusOK, map[string]any{
		"token":   res.Token,
		"user_id": res.UserID,
		"login":   res.Login,
	})
}

// Auth0Callback handles POST /api/v3/auth0/callback (no auth).
// It exchanges an Auth0 id_token (from redirect login flows) for a gh-server token.
func (d *Deps) Auth0Callback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDToken string `json:"id_token"`
	}
	if err := decodeBodyStrict(r, &body); err != nil || strings.TrimSpace(body.IDToken) == "" {
		respond.ValidationFailed(w, "id_token is required")
		return
	}

	res, err := d.Svc.Auth0LoginWithIDToken(r.Context(), body.IDToken)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrAuth0NotConfigured):
			respond.Error(w, http.StatusNotImplemented, "Auth0 is not configured")
		case errors.Is(err, service.ErrValidation):
			respond.Unauthorized(w, "invalid id_token")
		default:
			respond.ServiceErrorRequest(r, w, err)
		}
		return
	}

	u, uerr := d.Svc.GetUser(r.Context(), res.Login)
	if uerr != nil {
		respond.ServiceErrorRequest(r, w, uerr)
		return
	}

	respond.JSON(w, http.StatusOK, map[string]any{
		"token":   res.Token,
		"user_id": res.UserID,
		"login":   res.Login,
		"user":    transform.UserPrivate(u),
	})
}
