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

func (d *Deps) OIDCDeviceCode(w http.ResponseWriter, r *http.Request) {
	dc, err := d.Svc.RequestOIDCDeviceCode(r.Context())
	if err != nil {
		if errors.Is(err, service.ErrAuth0NotConfigured) {
			respond.Error(w, http.StatusNotImplemented, "OIDC is not configured")
			return
		}
		var oe auth0.OAuthError
		if errors.As(err, &oe) {
			respond.Error(w, http.StatusBadGateway, "OIDC error: "+oe.Error())
			return
		}
		respond.Error(w, http.StatusBadGateway, "OIDC request failed: "+err.Error())
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

func (d *Deps) OIDCSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceCode string `json:"device_code"`
	}
	if err := decodeBodyStrict(r, &body); err != nil || strings.TrimSpace(body.DeviceCode) == "" {
		respond.ValidationFailed(w, "device_code is required")
		return
	}
	res, err := d.Svc.OIDCLogin(r.Context(), body.DeviceCode)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrAuth0NotConfigured):
			respond.Error(w, http.StatusNotImplemented, "OIDC is not configured")
		case errors.Is(err, service.ErrAuth0Pending):
			respond.JSON(w, http.StatusAccepted, map[string]any{"status": "authorization_pending"})
		case errors.Is(err, service.ErrAuth0SlowDown):
			respond.JSON(w, http.StatusAccepted, map[string]any{"status": "slow_down"})
		case errors.Is(err, service.ErrAuth0Expired):
			respond.ValidationFailed(w, "device_code expired")
		case errors.Is(err, service.ErrAuth0AccessDenied):
			respond.Forbidden(w, "access denied")
		default:
			respond.ServiceErrorRequest(r, w, err)
		}
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"token": res.Token, "user_id": res.UserID, "login": res.Login})
}

func (d *Deps) OIDCCallback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDToken string `json:"id_token"`
	}
	if err := decodeBodyStrict(r, &body); err != nil || strings.TrimSpace(body.IDToken) == "" {
		respond.ValidationFailed(w, "id_token is required")
		return
	}
	res, err := d.Svc.OIDCLoginWithIDToken(r.Context(), body.IDToken)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrAuth0NotConfigured):
			respond.Error(w, http.StatusNotImplemented, "OIDC is not configured")
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
	respond.JSON(w, http.StatusOK, map[string]any{"token": res.Token, "user_id": res.UserID, "login": res.Login, "user": transform.UserPrivate(u)})
}

func (d *Deps) OIDCLookup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDToken string `json:"id_token"`
	}
	if err := decodeBodyStrict(r, &body); err != nil || strings.TrimSpace(body.IDToken) == "" {
		respond.ValidationFailed(w, "id_token is required")
		return
	}
	res, err := d.Svc.LookupOIDCIdentityWithIDToken(r.Context(), body.IDToken)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrAuth0NotConfigured):
			respond.Error(w, http.StatusNotImplemented, "OIDC is not configured")
		case errors.Is(err, service.ErrValidation):
			respond.Unauthorized(w, "invalid id_token")
		default:
			respond.ServiceErrorRequest(r, w, err)
		}
		return
	}
	if !res.Linked {
		respond.JSON(w, http.StatusOK, map[string]any{"linked": false})
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"linked": true, "user": map[string]any{"id": res.User.ID, "login": res.User.Login, "name": res.User.Name}})
}
