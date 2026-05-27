// Package middleware provides shared HTTP middleware.
package middleware

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/ngaut/agent-git-service/internal/metrics"
)

type operationStateKey struct{}

type operationState struct {
	channel   string
	operation string
}

// Operation annotates a request with an explicit business operation label.
func Operation(channel, operation string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if state := requestOperationState(r.Context()); state != nil {
				state.channel = channel
				state.operation = operation
			}
			next.ServeHTTP(w, r)
		})
	}
}

// MetricsInstrumentation records HTTP metrics using chi route patterns.
func MetricsInstrumentation() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			state := &operationState{}
			r = r.WithContext(context.WithValue(r.Context(), operationStateKey{}, state))
			metrics.IncHTTPInFlight()
			start := time.Now()
			ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)

			defer func() {
				metrics.DecHTTPInFlight()
				status := ww.Status()
				if status == 0 {
					status = http.StatusOK
				}
				route := "unmatched"
				if rctx := chi.RouteContext(r.Context()); rctx != nil {
					if pattern := rctx.RoutePattern(); pattern != "" {
						route = pattern
					}
				}
				duration := time.Since(start)
				metrics.ObserveHTTPRequest(r.Method, route, status, duration)
				channel, operation := deriveOperation(r.Method, route, state)
				metrics.ObserveOperation(channel, operation, classifyOperationResult(status), duration)
			}()

			next.ServeHTTP(ww, r)
		})
	}
}

func requestOperationState(ctx context.Context) *operationState {
	state, _ := ctx.Value(operationStateKey{}).(*operationState)
	return state
}

func deriveOperation(method, route string, state *operationState) (string, string) {
	if state != nil && state.channel != "" && state.operation != "" {
		return state.channel, state.operation
	}

	switch {
	case route == "" || route == "unmatched":
		return "unknown", "unmatched"
	case strings.HasSuffix(route, "/info/refs"):
		return "git", "git_discover"
	case strings.HasSuffix(route, "/git-upload-pack"):
		return "git", "git_read"
	case strings.HasSuffix(route, "/git-receive-pack"):
		return "git", "git_push"
	case route == "/api/graphql" || route == "/graphql":
		return "graphql", "graphql"
	case strings.HasPrefix(route, "/login/") || strings.HasPrefix(route, "/api/v3/oidc/"):
		return "rest", "auth"
	case route == "/api/v3" || route == "/api/v3/" || route == "/api/v3/meta" || route == "/api/v3/rate_limit":
		return "rest", "api_discovery"
	case strings.HasPrefix(route, "/api/v3/search/"):
		return "rest", "search"
	case strings.Contains(route, "/agent"):
		return "rest", readWriteOperation(method, "agent")
	case strings.Contains(route, "/tokens"):
		return "rest", readWriteOperation(method, "token")
	case strings.Contains(route, "/keys"):
		return "rest", readWriteOperation(method, "key")
	case strings.Contains(route, "/secrets"):
		return "rest", readWriteOperation(method, "secret")
	case strings.Contains(route, "/variables"):
		return "rest", readWriteOperation(method, "variable")
	case strings.Contains(route, "/rulesets") || strings.Contains(route, "/rules/branches/"):
		return "rest", readWriteOperation(method, "ruleset")
	case strings.Contains(route, "/actions/") || strings.Contains(route, "/dispatches") || strings.Contains(route, "/workflow"):
		return "rest", readWriteOperation(method, "workflow")
	case strings.Contains(route, "/gists"):
		return "rest", readWriteOperation(method, "gist")
	case strings.Contains(route, "/issues"):
		return "rest", readWriteOperation(method, "issue")
	case strings.Contains(route, "/pulls"):
		return "rest", readWriteOperation(method, "pr")
	case strings.Contains(route, "/orgs"):
		return "rest", readWriteOperation(method, "org")
	case strings.Contains(route, "/notifications"):
		return "rest", readWriteOperation(method, "notification")
	case strings.Contains(route, "/starred"):
		return "rest", readWriteOperation(method, "star")
	case strings.Contains(route, "/repos") || strings.Contains(route, "/repositories"):
		return "rest", readWriteOperation(method, "repo")
	case strings.HasPrefix(route, "/api/"):
		return "rest", readWriteOperation(method, "rest")
	default:
		return "http", readWriteOperation(method, "http")
	}
}

func readWriteOperation(method, noun string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return noun + "_read"
	default:
		return noun + "_write"
	}
}

func classifyOperationResult(status int) string {
	switch {
	case status >= 500:
		return "server_error"
	case status >= 400:
		return "client_error"
	default:
		return "success"
	}
}
