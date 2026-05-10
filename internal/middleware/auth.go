// Package middleware provides shared HTTP middleware.
package middleware

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"strings"

	applog "gh-server/internal/logging"
	"gh-server/internal/ratelimit"
	"gh-server/internal/rest/respond"
	"gh-server/internal/service"
	"gh-server/internal/tenant"
)

// TokenAuth returns middleware that validates GitHub-compatible auth headers.
// Accepts "token <val>" or "Bearer <val>" with any non-empty value.
//
// When router is non-nil (control-plane mode), tokens are resolved through the
// control plane and both ContextWithDB and ContextWithUser are injected.
// When router is nil (single-DB mode), the current behavior is preserved.
func TokenAuth(svc *service.Service, router TokenResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			applog.AddAttrs(r.Context(), slog.String("auth_scheme", authScheme(r.Header.Get("Authorization"))))
			token := extractToken(r)
			if token == "" {
				logAuthFailure(r.Context(), "header_missing_or_empty", "", "", nil)
				respond.Error(w, http.StatusUnauthorized, "Requires authentication")
				return
			}

			ctx, shouldReturn := resolveTokenAndInjectContext(w, r, token, router, svc)
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
//
// When router is non-nil and an Authorization header is present, the token
// is resolved through the control plane. When router is nil, current behavior
// is preserved.
func OptionalTokenAuth(svc *service.Service, router TokenResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if auth == "" {
				r = r.WithContext(service.ContextWithAnonRequest(r.Context()))
				applog.AddAttrs(r.Context(), slog.String("auth_mode", "anonymous"))
				next.ServeHTTP(w, r)
				return
			}

			applog.AddAttrs(r.Context(), slog.String("auth_scheme", authScheme(auth)))
			token := extractToken(r)
			if token == "" {
				logAuthFailure(r.Context(), "malformed_authorization_header", "", "", nil)
				respond.Error(w, http.StatusUnauthorized, "Bad credentials")
				return
			}

			ctx, shouldReturn := resolveTokenAndInjectContext(w, r, token, router, svc)
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

// extractToken extracts the token value from an Authorization header.
// Supports "token <val>", "Bearer <val>", and "Basic <base64>" formats.
// For Basic auth the password portion is used as the token, matching the
// convention used by Git credential helpers (username:token).
func extractToken(r *http.Request) string {
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
func resolveTokenAndInjectContext(w http.ResponseWriter, r *http.Request, token string, router TokenResolver, svc *service.Service) (context.Context, bool) {
	if hasTokenResolver(router) {
		// Control-plane mode: resolve token → tenant user + DB
		user, tenantDB, err := router.ResolveToken(r.Context(), token)
		if err != nil {
			return nil, handleAuthError(w, r, token, "control_plane", classifyControlPlaneAuthError(err), err)
		}
		ctx := service.ContextWithDB(r.Context(), tenantDB)
		ctx = service.ContextWithUser(ctx, user)
		ctx = tenant.ContextWithTenant(ctx, user.Login)
		ctx = service.ContextWithRepoCache(ctx)
		if fingerprint := applog.TokenFingerprint(token); fingerprint != "" {
			ctx = ratelimit.WithActor(ctx, "token:"+fingerprint)
		}
		applog.AddAttrs(ctx,
			slog.String("auth_mode", "control_plane"),
			slog.String("user_login", user.Login),
			slog.String("tenant", user.Login),
		)
		return ctx, false
	}

	// Single-DB mode: validate and resolve user in a single pass to avoid
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

func hasTokenResolver(router TokenResolver) bool {
	if router == nil {
		return false
	}

	v := reflect.ValueOf(router)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !v.IsNil()
	default:
		return true
	}
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

func classifyControlPlaneAuthError(err error) string {
	switch {
	case errors.Is(err, ErrUnknownToken):
		return "unknown_token"
	case errors.Is(err, ErrInactiveUser):
		return "inactive_user"
	default:
		return "token_resolution_error"
	}
}
