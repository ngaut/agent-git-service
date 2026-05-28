// Package oauth implements the GitHub OAuth device flow.
// Required by `gh auth login --git-protocol https`.
package oauth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/randutil"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/service"
)

const slockOAuthVerifierCookieName = "slock_oauth_verifier"

// Handler holds the service dependency for OAuth endpoints.
type Handler struct {
	Svc *service.Service

	// PreapproveDeviceCodes restores the legacy local-dev behavior that auto-
	// approves device codes without an explicit browser confirmation step.
	PreapproveDeviceCodes bool
}

var pkceS256ChallengePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43,128}$`)

type accessTokenRequest struct {
	DeviceCode   string `json:"device_code"`
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
}

// New creates a new OAuth handler.
func New(svc *service.Service) *Handler {
	return &Handler{Svc: svc}
}

// RequestDeviceCode handles POST /login/device/code
// gh sends: client_id, scope
// Device codes start in "pending" state and require user approval.
func (h *Handler) RequestDeviceCode(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	code := &db.DeviceCode{
		DeviceCode: randutil.Hex(32),
		UserCode:   strings.ToUpper(randutil.Hex(4)) + "-" + strings.ToUpper(randutil.Hex(4)),
		State:      db.DeviceCodeStatePending,
		ExpiresAt:  now.Add(15 * time.Minute),
		CreatedAt:  now,
		// AccessToken is NOT pre-populated; it will be generated upon approval
	}
	if err := h.Svc.CreateDeviceCode(r.Context(), code); err != nil {
		slog.Error("failed to create device code", "error", err)
		respond.Error(w, 500, "failed to create device code")
		return
	}

	// Audit log the creation
	h.Svc.LogDeviceCodeAudit(r.Context(), code.ID, code.DeviceCode, "created", 0, "", r.RemoteAddr)
	if h.PreapproveDeviceCodes {
		adminUser, err := h.Svc.GetActiveSiteAdmin(r.Context())
		if err != nil {
			slog.Error("failed to preapprove device code", "error", err)
			respond.Error(w, 500, "failed to create device code")
			return
		}
		if _, err := h.Svc.ApproveDeviceCode(r.Context(), code.DeviceCode, adminUser.ID, adminUser.Login); err != nil {
			slog.Error("failed to preapprove device code", "error", err)
			respond.Error(w, 500, "failed to create device code")
			return
		}
	}

	base := r.Header.Get("X-Forwarded-Proto")
	scheme := "http"
	if base != "" {
		scheme = base
	}
	host := r.Host

	respond.JSON(w, 200, map[string]any{
		"device_code":      code.DeviceCode,
		"user_code":        code.UserCode,
		"verification_uri": scheme + "://" + host + "/login/device",
		"expires_in":       900, // 15 minutes
		"interval":         5,
	})
}

// AccessToken handles POST /login/oauth/access_token
// gh polls this until it gets a token.
// Returns authorization_pending if not yet approved, expired_token if timed out.
func (h *Handler) AccessToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB limit
	req, err := parseAccessTokenRequest(r)
	if err != nil {
		respond.JSON(w, 400, map[string]any{"error": "bad_verification_code"})
		return
	}

	var accessToken string
	switch {
	case req.DeviceCode != "":
		accessToken, err = h.Svc.ExchangeDeviceCode(r.Context(), req.DeviceCode)
		if err != nil {
			if errors.Is(err, service.ErrNotFound) {
				respond.JSON(w, 400, map[string]any{"error": "bad_verification_code"})
			} else if errors.Is(err, service.ErrDeviceCodeExpired) {
				respond.JSON(w, 400, map[string]any{"error": "expired_token"})
			} else if errors.Is(err, service.ErrDeviceCodePending) {
				respond.JSON(w, 400, map[string]any{"error": "authorization_pending"})
			} else if errors.Is(err, service.ErrDeviceCodeRejected) {
				respond.JSON(w, 400, map[string]any{"error": "access_denied"})
			} else {
				slog.Error("device code exchange failed", "error", err)
				respond.JSON(w, 400, map[string]any{"error": "authorization_pending"})
			}
			return
		}
	case req.Code != "":
		if strings.TrimSpace(req.CodeVerifier) == "" {
			if cookie, cookieErr := r.Cookie(slockOAuthVerifierCookieName); cookieErr == nil {
				req.CodeVerifier = strings.TrimSpace(cookie.Value)
			}
		}
		http.SetCookie(w, &http.Cookie{
			Name:     slockOAuthVerifierCookieName,
			Value:    "",
			Path:     "/login/oauth/access_token",
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteNoneMode,
			Secure:   r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https"),
		})
		accessToken, err = h.Svc.ExchangeAuthorizationCode(r.Context(), req.Code, req.CodeVerifier)
		if err != nil {
			if errors.Is(err, service.ErrNotFound) ||
				errors.Is(err, service.ErrPKCEVerifierRequired) ||
				errors.Is(err, service.ErrPKCEVerifierMismatch) {
				respond.JSON(w, 400, map[string]any{"error": "bad_verification_code"})
			} else {
				slog.Error("authorization code exchange failed", "error", err)
				respond.JSON(w, 400, map[string]any{"error": "bad_verification_code"})
			}
			return
		}
	default:
		respond.JSON(w, 400, map[string]any{"error": "bad_verification_code"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	respond.JSON(w, 200, map[string]any{
		"access_token": accessToken,
		"token_type":   "bearer",
		"scope":        "repo,read:org,read:user",
	})
}

// Authorize handles GET /login/oauth/authorize
// gh opens this in the browser; we just redirect back with a code.
// The redirect_uri must be same-origin and the request must include state + PKCE.
func (h *Handler) Authorize(w http.ResponseWriter, r *http.Request) {
	redirectURI := strings.TrimSpace(r.URL.Query().Get("redirect_uri"))
	if redirectURI == "" {
		w.WriteHeader(200)
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" {
		http.Error(w, "state is required", http.StatusBadRequest)
		return
	}
	codeChallenge := strings.TrimSpace(r.URL.Query().Get("code_challenge"))
	if codeChallenge == "" {
		http.Error(w, "code_challenge is required", http.StatusBadRequest)
		return
	}
	if !pkceS256ChallengePattern.MatchString(codeChallenge) {
		http.Error(w, "invalid code_challenge", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("code_challenge_method")) != "S256" {
		http.Error(w, "code_challenge_method must be S256", http.StatusBadRequest)
		return
	}

	// Validate redirect_uri: must be same-origin or localhost to prevent open redirect.
	parsed, err := url.Parse(redirectURI)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	targetHost := parsed.Hostname()
	requestHost := r.Host
	if h, _, splitErr := net.SplitHostPort(requestHost); splitErr == nil {
		requestHost = h
	}
	if targetHost != requestHost && targetHost != "localhost" && targetHost != "127.0.0.1" {
		http.Error(w, "redirect_uri must be same-origin", http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	code := randutil.Hex(32)
	authCode := &db.AuthorizationCode{
		Code:                code,
		RedirectURI:         parsed.String(),
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: "S256",
		ExpiresAt:           now.Add(service.AuthorizationCodeTTL),
		CreatedAt:           now,
	}
	if user, ok := service.UserFromContext(r.Context()); ok && user.ID != 0 {
		authCode.UserID = &user.ID
	}
	if err := h.Svc.CreateAuthorizationCode(r.Context(), authCode); err != nil {
		slog.Error("failed to create authorization code", "error", err)
		respond.Error(w, 500, "failed to create authorization code")
		return
	}

	query := parsed.Query()
	query.Set("code", code)
	query.Set("state", state)
	parsed.RawQuery = query.Encode()
	http.Redirect(w, r, parsed.String(), http.StatusFound)
}

func parseAccessTokenRequest(r *http.Request) (accessTokenRequest, error) {
	var req accessTokenRequest

	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return accessTokenRequest{}, err
		}
		return req, nil
	}

	if err := r.ParseForm(); err != nil {
		return accessTokenRequest{}, err
	}
	req.DeviceCode = r.FormValue("device_code")
	req.Code = r.FormValue("code")
	req.CodeVerifier = r.FormValue("code_verifier")
	return req, nil
}

// DeviceCodeVerification handles GET/POST /login/device
// GET: Shows the device code verification page
// POST: Verifies user code and approves/rejects the device code
func (h *Handler) DeviceCodeVerification(w http.ResponseWriter, r *http.Request) {
	user, ok := service.UserFromContext(r.Context())
	if !ok || user.ID == 0 {
		respond.Unauthorized(w, "Authentication required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		// Show verification page
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
<head><title>Device Code Verification</title></head>
<body>
<h1>Device Code Verification</h1>
<form method="POST" action="/login/device">
  <label for="user_code">Enter User Code:</label>
  <input type="text" id="user_code" name="user_code" required pattern="[A-Z0-9]{4}-[A-Z0-9]{4}" placeholder="XXXX-XXXX">
  <button type="submit">Verify</button>
</form>
</body>
</html>`))
	case http.MethodPost:
		// Process verification
		if err := r.ParseForm(); err != nil {
			respond.Error(w, 400, "invalid form data")
			return
		}
		userCode := strings.ToUpper(strings.TrimSpace(r.FormValue("user_code")))
		if userCode == "" {
			respond.Error(w, 400, "user code required")
			return
		}

		// Look up device code by user code
		code, err := h.Svc.GetDeviceCodeByUserCode(r.Context(), userCode)
		if err != nil {
			respond.Error(w, 404, "invalid user code")
			return
		}

		// Approve the device code
		_, err = h.Svc.ApproveDeviceCode(r.Context(), code.DeviceCode, user.ID, user.Login)
		if err != nil {
			slog.Error("device code approval failed", "error", err)
			if errors.Is(err, service.ErrForbidden) || errors.Is(err, service.ErrUnauthorized) {
				respond.Error(w, http.StatusForbidden, "approval forbidden")
				return
			}
			respond.Error(w, 500, "approval failed")
			return
		}

		// Log the approval
		h.Svc.LogDeviceCodeAudit(r.Context(), code.ID, code.DeviceCode, "verified", user.ID, user.Login, r.RemoteAddr)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
<head><title>Verification Successful</title></head>
<body>
<h1>Verification Successful!</h1>
<p>Your device has been authorized. You can close this window and return to your terminal.</p>
</body>
</html>`))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
