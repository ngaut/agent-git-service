package rest

import (
	"net/http"
	"strings"
	"time"

	"gh-server/internal/rest/respond"
	"gh-server/internal/rest/transform"
)

// ListTokens handles GET /api/v3/user/tokens
func (d *Deps) ListTokens(w http.ResponseWriter, r *http.Request) {
	page, perPage := parsePagination(r)
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	tokens, err := d.Svc.ListTokens(r.Context(), u.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(tokens))
	for i, tok := range tokens {
		out[i] = transform.Token(tok)
	}
	respond.JSON(w, 200, paginate(w, r, d.Svc.BaseURL, out, page, perPage))
}

// CreateToken handles POST /api/v3/user/tokens
func (d *Deps) CreateToken(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	var body struct {
		Name      string  `json:"name"`
		ExpiresAt *string `json:"expires_at"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	var expiresAt *time.Time
	if body.ExpiresAt != nil {
		exp := strings.TrimSpace(*body.ExpiresAt)
		if exp != "" {
			parsed, err := time.Parse(time.RFC3339, exp)
			if err != nil {
				respond.ValidationFailed(w, "expires_at must be RFC3339")
				return
			}
			expiresAt = &parsed
		}
	}
	ok, err := d.Svc.CreateUserToken(r.Context(), u.ID, body.Name, expiresAt)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 201, transform.Token(ok))
}

// DeleteToken handles DELETE /api/v3/user/tokens
func (d *Deps) DeleteToken(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	var body struct {
		ID      uint   `json:"id"`
		TokenID uint   `json:"token_id"`
		Token   string `json:"token"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}

	tokenID := body.ID
	if tokenID == 0 {
		tokenID = body.TokenID
	}
	if tokenID != 0 {
		if err := d.Svc.DeleteTokenByID(r.Context(), u.ID, tokenID); err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		respond.NoContent(w)
		return
	}

	if strings.TrimSpace(body.Token) == "" {
		respond.ValidationFailed(w, "token_id or token is required")
		return
	}
	if err := d.Svc.DeleteTokenByValue(r.Context(), u.ID, body.Token); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}
