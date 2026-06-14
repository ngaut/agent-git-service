// Package testharness provides a reusable, fully-wired router integration
// test harness. It constructs the complete dependency graph used by production
// (TiDB test DB → service.Service → REST/GraphQL/GitHTTP/OAuth handlers → router mux)
// so that downstream test packages can exercise the real HTTP surface without
// rebuilding ad-hoc wiring.
package testharness

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/githttp"
	"github.com/ngaut/agent-git-service/internal/graphql"
	"github.com/ngaut/agent-git-service/internal/oauth"
	"github.com/ngaut/agent-git-service/internal/rest"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/router"
	"github.com/ngaut/agent-git-service/internal/service"
)

// Harness holds the fully-wired test infrastructure.
type Harness struct {
	Svc     *service.Service // for direct service-layer calls in test setup
	DB      *gorm.DB         // for direct DB seeding/assertions
	Mux     http.Handler     // host-aware mux from router.RegisterRoutes
	User    db.User          // pre-seeded default user (SiteAdmin: true)
	Token   string           // pre-seeded auth token value
	BaseURL string           // e.g. "http://localhost:8080"
	GitRoot string           // temp dir root for gitstore

	transformBase atomic.Value // string; base URL for per-request transform.Init
	rootTB        testing.TB   // the testing.TB passed to New(); used for server cleanup
	srv           *httptest.Server
	srvOnce       sync.Once
}

// New creates a fully-wired test harness with an isolated TiDB database,
// temp gitstore directory, real service layer, and the full production router.
// All resources are cleaned up via tb.Cleanup.
func New(tb testing.TB) *Harness {
	tb.Helper()

	svc, cleanup := NewService(tb, ServiceConfig{})
	tb.Cleanup(cleanup)

	// Seed a default admin user (SiteAdmin: true for OAuth exchange compatibility).
	user := db.User{Login: "testuser", Name: "testuser", Type: db.TypeUser, UserKind: db.UserKindHuman, SiteAdmin: true}
	if err := svc.DB.Create(&user).Error; err != nil {
		tb.Fatalf("testharness: seed user: %v", err)
	}

	tokenValue := "test-token"
	if err := svc.DB.Create(&db.Token{UserID: user.ID, Value: tokenValue}).Error; err != nil {
		tb.Fatalf("testharness: seed token: %v", err)
	}

	// Wire handlers using production constructors.
	handlers := &rest.Deps{Svc: svc}
	gqlSrv := graphql.NewServer(svc)
	gitHandler := githttp.New(svc.Git, svc)
	oauthHandler := oauth.New(svc)

	mux := router.RegisterRoutes(chi.NewRouter(), handlers, gitHandler, gqlSrv, oauthHandler, "http://console.localhost")

	h := &Harness{
		Svc:     svc,
		DB:      svc.DB,
		User:    user,
		Token:   tokenValue,
		BaseURL: svc.BaseURL,
		GitRoot: svc.AttachmentRoot,
		rootTB:  tb,
	}
	h.transformBase.Store(svc.BaseURL)
	h.Mux = h.wrapTransform(mux)

	return h
}

// wrapTransform wraps an http.Handler with middleware that scopes transform URL
// state to this harness's base URL for the duration of each request.
func (h *Harness) wrapTransform(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		transform.Wrap(h.transformBase.Load().(string), func() {
			next.ServeHTTP(w, r)
		})
	})
}

// Server lazily starts an httptest.Server backed by h.Mux and registers
// cleanup on the root test (the *testing.T passed to New). It rebinds all
// URL-producing state (BaseURL, Service.BaseURL, transform.Init) to the
// real server URL so that generated API/git URLs use the actual test
// server host:port.
//
// Server is idempotent: the first call creates the server and rebinds
// URL state; subsequent calls return the same server without mutation.
//
// Cleanup is registered on the root benchmark/test — not the caller's tb — so
// sibling subtests sharing a harness cannot close the server prematurely.
func (h *Harness) Server(tb testing.TB) *httptest.Server {
	tb.Helper()
	h.srvOnce.Do(func() {
		h.srv = httptest.NewServer(h.Mux)
		h.rootTB.Cleanup(h.srv.Close)

		// Rebind all URL state to the live server URL.
		h.BaseURL = h.srv.URL
		h.Svc.BaseURL = h.srv.URL
		h.transformBase.Store(h.srv.URL)
	})
	return h.srv
}

// DoREST sends an authenticated HTTP request through the mux and returns
// the response recorder. It sets Authorization and Content-Type headers
// automatically. Callers assert the status code themselves.
func (h *Harness) DoREST(tb testing.TB, method, path string, body io.Reader) *httptest.ResponseRecorder {
	tb.Helper()
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Authorization", "token "+h.Token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)
	return w
}

// DoRESTJSON sends an authenticated HTTP request with an auto-marshaled JSON body.
// If body is nil, no request body is sent.
func (h *Harness) DoRESTJSON(tb testing.TB, method, path string, body any) *httptest.ResponseRecorder {
	tb.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			tb.Fatalf("testharness: marshal body: %v", err)
		}
		r = bytes.NewReader(b)
	}
	return h.DoREST(tb, method, path, r)
}

// DoRESTWithHost performs an authenticated HTTP request with a custom Host header.
func (h *Harness) DoRESTWithHost(tb testing.TB, method, host, path string, body any) *httptest.ResponseRecorder {
	tb.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			tb.Fatalf("testharness: marshal body: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	req.Host = host
	req.Header.Set("Authorization", "token "+h.Token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)
	return w
}

// DoRESTNoAuth performs an HTTP request without an Authorization header.
func (h *Harness) DoRESTNoAuth(tb testing.TB, method, path string) *httptest.ResponseRecorder {
	tb.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)
	return w
}

// DoRESTNoAuthWithHost performs an unauthenticated HTTP request with a custom Host header.
func (h *Harness) DoRESTNoAuthWithHost(tb testing.TB, method, host, path string) *httptest.ResponseRecorder {
	tb.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)
	return w
}

// DoRESTWithToken performs an HTTP request with a custom Authorization token.
func (h *Harness) DoRESTWithToken(tb testing.TB, method, path, token string) *httptest.ResponseRecorder {
	tb.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)
	return w
}

// DoRESTJSONWithToken performs an HTTP request with a custom Authorization token
// and an auto-marshaled JSON body.
func (h *Harness) DoRESTJSONWithToken(tb testing.TB, method, path, token string, body any) *httptest.ResponseRecorder {
	tb.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			tb.Fatalf("testharness: marshal body: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)
	return w
}

// DecodeJSON decodes a recorder's response body into a map.
func DecodeJSON(tb testing.TB, w *httptest.ResponseRecorder) map[string]any {
	tb.Helper()
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		tb.Fatalf("decode json: %v\nbody: %s", err, w.Body.String())
	}
	return result
}

// DecodeJSONArray decodes a recorder's response body into a slice of maps.
func DecodeJSONArray(tb testing.TB, w *httptest.ResponseRecorder) []map[string]any {
	tb.Helper()
	var result []any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		tb.Fatalf("decode json array: %v\nbody: %s", err, w.Body.String())
	}
	out := make([]map[string]any, len(result))
	for i, v := range result {
		m, ok := v.(map[string]any)
		if !ok {
			tb.Fatalf("decode json array: element %d is not an object", i)
		}
		out[i] = m
	}
	return out
}

// DoGraphQL sends a GraphQL request through the mux, asserts 200 status,
// fails on GraphQL-level errors, and returns the "data" map.
func (h *Harness) DoGraphQL(tb testing.TB, query string, vars map[string]any) map[string]any {
	tb.Helper()
	reqBody := map[string]any{"query": query}
	if vars != nil {
		reqBody["variables"] = vars
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		tb.Fatalf("testharness: marshal graphql request: %v", err)
	}

	w := h.DoREST(tb, "POST", "/graphql", bytes.NewReader(b))
	if w.Code != http.StatusOK {
		tb.Fatalf("testharness: GraphQL returned %d: %s", w.Code, w.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		tb.Fatalf("testharness: unmarshal graphql response: %v", err)
	}
	if errs, ok := res["errors"]; ok {
		eb, _ := json.MarshalIndent(errs, "", "  ")
		tb.Fatalf("testharness: GraphQL errors: %s", string(eb))
	}
	data, ok := res["data"].(map[string]any)
	if !ok {
		tb.Fatalf("testharness: GraphQL response missing 'data' key: %s", w.Body.String())
	}
	return data
}
