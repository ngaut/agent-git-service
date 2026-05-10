package middleware

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"gh-server/internal/ratelimit"
	"gh-server/internal/rest/respond"
)

// APIRateLimitHeaders emits GitHub-compatible rate-limit headers for REST v3
// and GraphQL responses using real per-actor counters.
func APIRateLimitHeaders() func(http.Handler) http.Handler {
	return apiRateLimitHeaders(ratelimit.NewGitHubLimiter())
}

func apiRateLimitHeaders(limiter *ratelimit.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resource, ok := ratelimit.ResourceForRequest(r)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			now := time.Now().UTC()
			subject := ratelimit.SubjectForRequest(r)

			var (
				snapshot ratelimit.Snapshot
				allowed  = true
				ctx      = r.Context()
			)
			if r.URL.Path == "/api/v3/rate_limit" {
				report := limiter.Report(subject, now)
				snapshot = report.Rate
				ctx = ratelimit.WithReport(ctx, report)
			} else {
				snapshot, allowed = limiter.Allow(subject, resource, now, 1)
			}
			ratelimit.SetHeaders(w.Header(), snapshot)
			if !allowed {
				retryAfter := snapshot.Reset - now.Unix()
				if retryAfter < 1 {
					retryAfter = 1
				}
				w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("X-GitHub-Media-Type", "github.v3; format=json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = fmt.Fprint(w, `{"error":"API rate limit exceeded","message":"API rate limit exceeded","documentation_url":"https://docs.github.com/rest/using-the-rest-api/rate-limits-for-the-rest-api"}`)
				return
			}
			ctx = ratelimit.WithSnapshot(ctx, snapshot)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type fixedWindowLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	entries map[string]fixedWindowEntry
}

type fixedWindowEntry struct {
	windowStart time.Time
	count       int
}

// RateLimit applies a per-client fixed-window rate limit.
func RateLimit(limit int, window time.Duration) func(http.Handler) http.Handler {
	limiter := &fixedWindowLimiter{
		limit:   limit,
		window:  window,
		entries: make(map[string]fixedWindowEntry),
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.allow(rateLimitClientKey(r), time.Now().UTC()) {
				w.Header().Set("Retry-After", strconv.Itoa(int(window.Seconds())))
				respond.RateLimited(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (l *fixedWindowLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.IsZero() {
		now = time.Now().UTC()
	}

	l.prune(now)

	entry := l.entries[key]
	if entry.windowStart.IsZero() || now.Sub(entry.windowStart) >= l.window {
		l.entries[key] = fixedWindowEntry{
			windowStart: now,
			count:       1,
		}
		return true
	}
	if entry.count >= l.limit {
		return false
	}

	entry.count++
	l.entries[key] = entry
	return true
}

func (l *fixedWindowLimiter) prune(now time.Time) {
	for key, entry := range l.entries {
		if entry.windowStart.IsZero() || now.Sub(entry.windowStart) >= l.window {
			delete(l.entries, key)
		}
	}
}

func rateLimitClientKey(r *http.Request) string {
	if forwardedFor := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwardedFor != "" {
		if clientIP := strings.TrimSpace(strings.Split(forwardedFor, ",")[0]); clientIP != "" {
			return clientIP
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	if host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr)); err == nil && host != "" {
		return host
	}

	remoteAddr := strings.TrimSpace(r.RemoteAddr)
	if remoteAddr == "" {
		return "unknown"
	}
	return remoteAddr
}
