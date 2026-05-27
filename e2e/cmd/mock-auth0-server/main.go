// Mock Auth0 server for E2E tests.
// It supports configurable error responses to exercise all error-state contracts.
// Usage: go run ./e2e/cmd/mock-auth0-server :8891
//
// Admin endpoints:
//
//	POST /__admin/mode?mode=<authorization_pending|slow_down|expired_token|access_denied|oauth_error|success>
//	POST /__admin/reset
//	GET  /__admin/state
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type state struct {
	mu          sync.Mutex
	mode        string // authorization_pending, slow_down, expired_token, access_denied, oauth_error, success
	failCount   int    // number of times to fail before success (for authorization_pending)
	successOnce bool   // if true, switch to success mode after first successful exchange
	privateKey  *rsa.PrivateKey
	keyID       string
}

func newState() (*state, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return &state{
		mode:       "success",
		failCount:  0,
		privateKey: privateKey,
		keyID:      "mock-key-id",
	}, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func main() {
	addr := ":8891"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}

	s, err := newState()
	if err != nil {
		log.Fatalf("mock auth0 init failed: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/__admin/state", s.handleAdminState)
	mux.HandleFunc("/__admin/reset", s.handleAdminReset)
	mux.HandleFunc("/__admin/mode", s.handleAdminMode)
	mux.HandleFunc("/.well-known/openid-configuration", s.handleDiscovery)
	mux.HandleFunc("/oauth/device/code", s.handleDeviceCode)
	mux.HandleFunc("/oauth/token", s.handleToken)
	mux.HandleFunc("/.well-known/jwks.json", s.handleJWKS)

	log.Printf("mock auth0 server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("listen failed: %v", err)
	}
}

func issuerForRequest(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/", scheme, r.Host)
}

func encodeBigInt(n *big.Int) string {
	return base64.RawURLEncoding.EncodeToString(n.Bytes())
}

func (s *state) signIDToken(issuer, clientID, subject string) (string, error) {
	s.mu.Lock()
	privateKey := s.privateKey
	keyID := s.keyID
	s.mu.Unlock()

	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":                issuer,
		"aud":                clientID,
		"sub":                subject,
		"exp":                now.Add(time.Hour).Unix(),
		"iat":                now.Unix(),
		"email":              "mock@example.com",
		"email_verified":     true,
		"name":               "Mock User",
		"nickname":           "mock",
		"preferred_username": "mockuser",
	})
	token.Header["kid"] = keyID
	return token.SignedString(privateKey)
}

func (s *state) handleAdminState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":         s.mode,
		"fail_count":   s.failCount,
		"success_once": s.successOnce,
	})
}

func (s *state) handleAdminReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	s.mu.Lock()
	s.mode = "success"
	s.failCount = 0
	s.successOnce = false
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *state) handleAdminMode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	mode := strings.TrimSpace(r.URL.Query().Get("mode"))
	failCountStr := strings.TrimSpace(r.URL.Query().Get("fail_count"))
	successOnceStr := strings.TrimSpace(r.URL.Query().Get("success_once"))

	var failCount int
	if failCountStr != "" {
		if _, err := fmt.Sscanf(failCountStr, "%d", &failCount); err != nil || failCount < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid fail_count"})
			return
		}
	}

	successOnce := false
	if successOnceStr == "true" {
		successOnce = true
	}

	validModes := map[string]bool{
		"authorization_pending": true,
		"slow_down":             true,
		"expired_token":         true,
		"access_denied":         true,
		"oauth_error":           true,
		"success":               true,
	}

	if !validModes[mode] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid mode"})
		return
	}

	s.mu.Lock()
	s.mode = mode
	s.failCount = failCount
	s.successOnce = successOnce
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"mode":   mode,
	})
}

func (s *state) handleDeviceCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	// Always return a valid device code
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               "mock-device-code-123",
		"user_code":                 "MOCK-123",
		"verification_uri":          "https://mock.auth0.example.com/activate",
		"verification_uri_complete": "https://mock.auth0.example.com/activate?code=MOCK-123",
		"expires_in":                900,
		"interval":                  5,
	})
}

func (s *state) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	issuer := issuerForRequest(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                        issuer,
		"token_endpoint":                issuer + "oauth/token",
		"device_authorization_endpoint": issuer + "oauth/device/code",
		"jwks_uri":                      issuer + ".well-known/jwks.json",
	})
}

func (s *state) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid form"})
		return
	}

	s.mu.Lock()
	mode := s.mode
	failCount := s.failCount
	successOnce := s.successOnce
	s.mu.Unlock()

	// Handle fail_count for authorization_pending mode
	if mode == "authorization_pending" && failCount > 0 {
		s.mu.Lock()
		s.failCount--
		s.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":             "authorization_pending",
			"error_description": "Authorization pending, please continue polling",
		})
		return
	}

	// Handle success_once mode
	if successOnce {
		s.mu.Lock()
		s.successOnce = false
		s.mu.Unlock()
		// Fall through to success response
	} else {
		switch mode {
		case "authorization_pending":
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":             "authorization_pending",
				"error_description": "Authorization pending, please continue polling",
			})
			return
		case "slow_down":
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":             "slow_down",
				"error_description": "Polling too fast, please slow down",
			})
			return
		case "expired_token":
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":             "expired_token",
				"error_description": "Device code has expired",
			})
			return
		case "access_denied":
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":             "access_denied",
				"error_description": "User denied access",
			})
			return
		case "oauth_error":
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":             "invalid_grant",
				"error_description": "Some other OAuth error",
			})
			return
		}
	}

	clientID := strings.TrimSpace(r.Form.Get("client_id"))
	if clientID == "" {
		clientID = "test-client-id"
	}
	subject := strings.TrimSpace(r.Form.Get("subject"))
	if subject == "" {
		subject = "auth0|mock123"
	}
	idToken, err := s.signIDToken(issuerForRequest(r), clientID, subject)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "token_sign_failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"access_token": "mock-access-token",
		"id_token":     idToken,
		"token_type":   "Bearer",
		"expires_in":   "3600",
	})
}

func (s *state) handleJWKS(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	publicKey := &s.privateKey.PublicKey
	keyID := s.keyID
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"keys": []map[string]string{
			{
				"kty": "RSA",
				"kid": keyID,
				"use": "sig",
				"alg": "RS256",
				"n":   encodeBigInt(publicKey.N),
				"e":   encodeBigInt(big.NewInt(int64(publicKey.E))),
			},
		},
	})
}

// Unused but required for interface completeness
func init() {
	_ = time.Now()
}
