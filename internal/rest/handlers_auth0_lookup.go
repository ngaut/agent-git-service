package rest

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/service"
)

// Auth0Lookup handles POST /api/v3/auth0/lookup (no auth).
// It verifies an Auth0 id_token and reports whether the identity is already
// linked to an existing local user.
func (d *Deps) Auth0Lookup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDToken string `json:"id_token"`
	}
	if err := decodeBodyStrict(r, &body); err != nil || strings.TrimSpace(body.IDToken) == "" {
		respond.ValidationFailed(w, "id_token is required")
		return
	}

	res, err := d.Svc.LookupAuth0IdentityWithIDToken(r.Context(), body.IDToken)
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

	if !res.Linked {
		respond.JSON(w, http.StatusOK, map[string]any{
			"linked": false,
		})
		return
	}

	respond.JSON(w, http.StatusOK, map[string]any{
		"linked": true,
		"user": map[string]any{
			"id":    res.User.ID,
			"login": res.User.Login,
			"name":  res.User.Name,
		},
	})
}
