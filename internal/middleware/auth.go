// Package middleware provides shared HTTP middleware.
package middleware

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"strings"

	agsauth "github.com/ngaut/agent-git-service/auth"
	applog "github.com/ngaut/agent-git-service/internal/logging"
	"github.com/ngaut/agent-git-service/internal/ratelimit"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/service"
)

// EmbeddedIdentity is the shared trusted host-provided identity shape used
// across the embedding surface, auth middleware, and service resolver.
type EmbeddedIdentity = agsauth.Identity

// EmbeddedIdentityAuthenticator authenticates a request using host-provided
// identity instead of AGS-issued tokens. ok=false means no embedded identity
// was present and token auth should continue if applicable.
type EmbeddedIdentityAuthenticator interface {
	Authenticate(*http.Request) (EmbeddedIdentity, bool, error)
}

type EmbeddedAuthConfig struct {
	Authenticator EmbeddedIdentityAuthenticator
}

// TokenAuth returns middleware that validates GitHub-compatible auth headers.
// Accepts "token <val>" or "Bearer <val>" with any non-empty value.
func TokenAuth(svc *service.Service) func(http.Handler) http.Handler {
	return TokenAuthWithEmbeddedIdentity(svc, EmbeddedAuthConfig{})
}

// TokenAuthWithEmbeddedIdentity returns middleware that first attempts trusted
// host-provided identity injection before falling back to token auth.
func TokenAuthWithEmbeddedIdentity(svc *service.Service, embedded EmbeddedAuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			applog.AddAttrs(r.Context(), slog.String("auth_scheme", authScheme(r.Header.Get("Authorization"))))
			if ctx, handled := resolveEmbeddedIdentityAndInjectContext(w, r, svc, embedded, false); handled {
				if ctx == nil {
					return
				}
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			token := ExtractToken(r)
			if token == "" {
				logAuthFailure(r.Context(), "header_missing_or_empty", "", "", nil)
				respond.Error(w, http.StatusUnauthorized, "Requires authentication")
				return
			}

			ctx, shouldReturn := resolveTokenAndInjectContext(w, r, token, svc)
			if shouldReturn {
				return
			}
			if ctx != nil {
				r = r.WithContext(ctx)
			}

			next.ServeHTTP(w, r)
		})
	}
}

// OptionalTokenAuth returns middleware that allows unauthenticated access
// but rejects requests with invalid Authorization headers.
func OptionalTokenAuth(svc *service.Service) func(http.Handler) http.Handler {
	return OptionalTokenAuthWithEmbeddedIdentity(svc, EmbeddedAuthConfig{})
}

// OptionalTokenAuthWithEmbeddedIdentity returns middleware that first attempts
// trusted host-provided identity injection before falling back to the
// historical optional token path.
func OptionalTokenAuthWithEmbeddedIdentity(svc *service.Service, embedded EmbeddedAuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ctx, handled := resolveEmbeddedIdentityAndInjectContext(w, r, svc, embedded, true); handled {
				if ctx == nil {
					return
				}
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			auth := r.Header.Get("Authorization")
			if auth == "" {
				r = r.WithContext(service.ContextWithAnonRequest(r.Context()))
				applog.AddAttrs(r.Context(), slog.String("auth_mode", "anonymous"))
				next.ServeHTTP(w, r)
				return
			}

			applog.AddAttrs(r.Context(), slog.String("auth_scheme", authScheme(auth)))
			token := ExtractToken(r)
			if token == "" {
				logAuthFailure(r.Context(), "malformed_authorization_header", "", "", nil)
				respond.Error(w, http.StatusUnauthorized, "Bad credentials")
				return
			}

			ctx, shouldReturn := resolveTokenAndInjectContext(w, r, token, svc)
			if shouldReturn {
				return
			}
			if ctx != nil {
				r = r.WithContext(ctx)
			}

			next.ServeHTTP(w, r)
		})
	}
}

func resolveEmbeddedIdentityAndInjectContext(w http.ResponseWriter, r *http.Request, svc *service.Service, embedded EmbeddedAuthConfig, _ bool) (context.Context, bool) {
	if embedded.Authenticator == nil {
		return nil, false
	}
	identity, ok, err := embedded.Authenticator.Authenticate(r)
	if err != nil {
		logAuthFailure(r.Context(), "embedded_identity_auth_failed", "", "embedded", err)
		respond.Error(w, http.StatusUnauthorized, "Bad credentials")
		return nil, true
	}
	if !ok {
		return nil, false
	}
	resolved := service.EmbeddedIdentity{
		Provider:  identity.Provider,
		Subject:   identity.Subject,
		Login:     identity.Login,
		Name:      identity.Name,
		Email:     identity.Email,
		Groups:    append([]string(nil), identity.Groups...),
		SiteAdmin: identity.SiteAdmin,
	}
	user, err := svc.ResolveEmbeddedIdentity(r.Context(), resolved)
	if err != nil {
		logAuthFailure(r.Context(), "embedded_identity_user_resolution_failed", "", "embedded", err)
		respond.Error(w, http.StatusUnauthorized, "Bad credentials")
		return nil, true
	}
	ctx := service.ContextWithUser(r.Context(), user)
	ctx = service.ContextWithRepoCache(ctx)
	if actor := embeddedIdentityActor(resolved); actor != "" {
		ctx = ratelimit.WithActor(ctx, actor)
	}
	applog.AddAttrs(ctx,
		slog.String("auth_mode", "embedded"),
		slog.String("auth_provider", resolved.Provider),
		slog.String("user_login", user.Login),
	)
	return ctx, true
}

func embeddedIdentityActor(identity service.EmbeddedIdentity) string {
	provider := strings.TrimSpace(identity.Provider)
	subject := strings.TrimSpace(identity.Subject)
	if provider == "" || subject == "" {
		return ""
	}
	return "embedded:" + provider + ":" + subject
}

// RequireAuthForWrites returns middleware that rejects unauthenticated
// write requests (POST/PUT/PATCH/DELETE) with 401. GET/HEAD/OPTIONS pass through.
func RequireAuthForWrites(svc *service.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			if _, ok := service.UserFromContext(r.Context()); !ok {
				respond.Error(w, http.StatusUnauthorized, "Requires authentication")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// MaxBodySize returns middleware that limits request body size.
// Returns 413 Payload Too Large if the body exceeds maxBytes.
func MaxBodySize(maxBytes int64) func(http.Handler) http.Handler {
	return MaxBodySizeUnless(maxBytes, nil)
}

// MaxBodySizeUnless returns middleware that limits request body size unless
// skip returns true for the current request.
func MaxBodySizeUnless(maxBytes int64, skip func(*http.Request) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skip != nil && skip(r) {
				next.ServeHTTP(w, r)
				return
			}
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ExtractToken extracts the token value from an Authorization header.
// Supports "token <val>", "Bearer <val>", and "Basic <base64>" formats.
// For Basic auth the password portion is used as the token, matching the
// convention used by Git credential helpers (username:token).
func ExtractToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	authTrim := strings.TrimSpace(auth)
	lower := strings.ToLower(authTrim)
	if strings.HasPrefix(lower, "token ") {
		return strings.TrimSpace(authTrim[6:])
	}
	if strings.HasPrefix(lower, "bearer ") {
		return strings.TrimSpace(authTrim[7:])
	}
	if strings.HasPrefix(lower, "basic ") {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(authTrim[6:]))
		if err != nil {
			return ""
		}
		// Basic auth format: "username:password" — use password as token
		if _, pass, ok := strings.Cut(string(decoded), ":"); ok && pass != "" {
			return pass
		}
		return ""
	}
	return ""
}

// handleAuthError handles authentication errors consistently.
// Returns true if the request should be rejected.
func handleAuthError(w http.ResponseWriter, r *http.Request, token string, mode string, reason string, err error) bool {
	logAuthFailure(r.Context(), reason, token, mode, err)
	respond.Error(w, http.StatusUnauthorized, "Bad credentials")
	return true
}

// resolveTokenAndInjectContext resolves the token and injects user/DB context.
// Returns (newContext, shouldReturn). If shouldReturn is true, the handler should return immediately.
func resolveTokenAndInjectContext(w http.ResponseWriter, r *http.Request, token string, svc *service.Service) (context.Context, bool) {
	// Validate and resolve user in a single pass to avoid
	// duplicate COUNT(*) and SELECT queries (see #1038).
	u, failure, err := svc.ValidateAndResolveTokenDetailed(r.Context(), token)
	if err != nil || failure != service.TokenValidationFailureNone {
		return nil, handleAuthError(w, r, token, "single_db", string(failure), err)
	}
	svc.TouchToken(r.Context(), token)
	singleCtx := service.ContextWithUser(r.Context(), u)
	singleCtx = service.ContextWithRepoCache(singleCtx)
	if fingerprint := applog.TokenFingerprint(token); fingerprint != "" {
		singleCtx = ratelimit.WithActor(singleCtx, "token:"+fingerprint)
	}
	applog.AddAttrs(singleCtx,
		slog.String("auth_mode", "single_db"),
		slog.String("user_login", u.Login),
	)
	return singleCtx, false
}

func authScheme(auth string) string {
	auth = strings.TrimSpace(strings.ToLower(auth))
	switch {
	case auth == "":
		return "none"
	case strings.HasPrefix(auth, "token "):
		return "token"
	case strings.HasPrefix(auth, "bearer "):
		return "bearer"
	case strings.HasPrefix(auth, "basic "):
		return "basic"
	default:
		return "unknown"
	}
}

func logAuthFailure(ctx context.Context, reason string, token string, mode string, err error) {
	attrs := []any{
		"auth_reason", reason,
	}
	if mode != "" {
		attrs = append(attrs, "auth_mode", mode)
	}
	if fingerprint := applog.TokenFingerprint(token); fingerprint != "" {
		attrs = append(attrs, "token_fingerprint", fingerprint)
	}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	slog.WarnContext(ctx, "authentication failed", attrs...)
}
