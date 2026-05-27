package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestNewRejectsInsecureByDefault(t *testing.T) {
	_, err := New(Config{Provider: "casdoor", Issuer: "http://example.com", ClientID: "client"})
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("expected https validation error, got %v", err)
	}
}

func TestRequestDeviceCodeUsesDiscovery(t *testing.T) {
	var hitDevice bool
	var baseURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                        baseURL + "/",
				"token_endpoint":                baseURL + "/oauth/token",
				"device_authorization_endpoint": baseURL + "/oauth/device/code",
				"jwks_uri":                      baseURL + "/jwks",
			})
		case "/oauth/device/code":
			hitDevice = true
			_ = json.NewEncoder(w).Encode(DeviceCode{DeviceCode: "dc", UserCode: "uc", VerificationURI: "https://verify", ExpiresIn: 600, Interval: 5})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	baseURL = srv.URL

	c, err := New(Config{Provider: "casdoor", Issuer: srv.URL, ClientID: "client", AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RequestDeviceCode(context.Background(), "openid"); err != nil {
		t.Fatal(err)
	}
	if !hitDevice {
		t.Fatal("expected device endpoint hit")
	}
}

func TestVerifyIDTokenUsesDiscoveredJWKS(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	kid := "kid-1"
	var issuer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":         issuer,
				"token_endpoint": issuer + "oauth/token",
				"jwks_uri":       issuer + "jwks",
			})
		case "/jwks":
			n := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
			e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes())
			_ = json.NewEncoder(w).Encode(map[string]any{
				"keys": []map[string]any{{
					"kty": "RSA", "use": "sig", "kid": kid, "alg": "RS256", "n": n, "e": e,
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	issuer = srv.URL + "/"

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub":  "casdoor|123",
		"iss":  issuer,
		"aud":  "client",
		"exp":  time.Now().Add(time.Hour).Unix(),
		"name": "Casdoor User",
	})
	token.Header["kid"] = kid
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}

	c, err := New(Config{Provider: "casdoor", Issuer: srv.URL, ClientID: "client", AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := c.VerifyIDToken(context.Background(), signed)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Sub != "casdoor|123" || claims.Name != "Casdoor User" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestVerifyIDTokenAcceptsIssuerWithoutTrailingSlash(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	kid := "kid-no-slash"
	var issuer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":         issuer,
				"token_endpoint": issuer + "/oauth/token",
				"jwks_uri":       issuer + "/jwks",
			})
		case "/jwks":
			n := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
			e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes())
			_ = json.NewEncoder(w).Encode(map[string]any{
				"keys": []map[string]any{{
					"kty": "RSA", "use": "sig", "kid": kid, "alg": "RS256", "n": n, "e": e,
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	issuer = srv.URL

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": "casdoor|456",
		"iss": issuer,
		"aud": "client",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	token.Header["kid"] = kid
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}

	c, err := New(Config{Provider: "casdoor", Issuer: issuer, ClientID: "client", AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := c.VerifyIDToken(context.Background(), signed)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Sub != "casdoor|456" || claims.Iss != issuer {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestLoadDiscoveryRejectsIssuerMismatch(t *testing.T) {
	var issuer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":         "http://unexpected.example.com/",
				"token_endpoint": issuer + "oauth/token",
				"jwks_uri":       issuer + "jwks",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	issuer = srv.URL + "/"

	c, err := New(Config{Provider: "casdoor", Issuer: srv.URL, ClientID: "client", AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RequestDeviceCode(context.Background(), "openid"); err == nil || !strings.Contains(err.Error(), "issuer mismatch") {
		t.Fatalf("expected issuer mismatch error, got %v", err)
	}
}

func TestLoadDiscoveryRejectsInsecureDiscoveredEndpoints(t *testing.T) {
	var issuer string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                        issuer,
				"token_endpoint":                "http://issuer.example/oauth/token",
				"device_authorization_endpoint": "http://issuer.example/oauth/device/code",
				"jwks_uri":                      "http://issuer.example/jwks",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	issuer = srv.URL + "/"

	c, err := New(Config{Provider: "casdoor", Issuer: issuer, DiscoveryURL: srv.URL + "/.well-known/openid-configuration", ClientID: "client"})
	if err != nil {
		t.Fatal(err)
	}
	c.http = srv.Client()

	if _, err := c.RequestDeviceCode(context.Background(), "openid"); err == nil || !strings.Contains(err.Error(), "oidc endpoint must use https") {
		t.Fatalf("expected insecure endpoint validation error, got %v", err)
	}
}

func TestLoadDiscoveryCachesAcrossConcurrentRequests(t *testing.T) {
	var discoveryHits atomic.Int32
	var deviceHits atomic.Int32
	var baseURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			discoveryHits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                        baseURL + "/",
				"token_endpoint":                baseURL + "/oauth/token",
				"device_authorization_endpoint": baseURL + "/oauth/device/code",
				"jwks_uri":                      baseURL + "/jwks",
			})
		case "/oauth/device/code":
			deviceHits.Add(1)
			_ = json.NewEncoder(w).Encode(DeviceCode{DeviceCode: "dc", UserCode: "uc", VerificationURI: "https://verify", ExpiresIn: 600, Interval: 5})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	baseURL = srv.URL

	c, err := New(Config{Provider: "casdoor", Issuer: srv.URL, ClientID: "client", AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 8
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.RequestDeviceCode(context.Background(), "openid"); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("unexpected concurrent request error: %v", err)
	}
	if got := discoveryHits.Load(); got != 1 {
		t.Fatalf("expected a single discovery fetch, got %d", got)
	}
	if got := deviceHits.Load(); got != workers {
		t.Fatalf("expected %d device requests, got %d", workers, got)
	}
}
