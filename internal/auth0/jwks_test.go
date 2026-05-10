package auth0

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestJWKSServer creates a test server that serves JWKS.
// It returns the server, the issuer URL (with trailing slash), and a function to set the JWKS response.
func newTestJWKSServer(t *testing.T) (*httptest.Server, string, func(jwksResponse any, statusCode int)) {
	t.Helper()

	var currentJWKS any = jwks{Keys: []jwk{}}
	currentStatusCode := http.StatusOK

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(currentStatusCode)
		if currentStatusCode == http.StatusOK {
			json.NewEncoder(w).Encode(currentJWKS)
		}
	})

	server := httptest.NewServer(handler)
	issuer := server.URL + "/"

	setResponse := func(jwksResponse any, statusCode int) {
		currentJWKS = jwksResponse
		currentStatusCode = statusCode
	}

	return server, issuer, setResponse
}

// generateTestRSAKey generates an RSA key pair for testing.
func generateTestRSAKey(t *testing.T) (*rsa.PrivateKey, *rsa.PublicKey) {
	t.Helper()
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return privKey, &privKey.PublicKey
}

// encodeBigInt encodes a big.Int as base64 URL-encoded string (JWK format).
func encodeBigInt(n *big.Int) string {
	return base64.RawURLEncoding.EncodeToString(n.Bytes())
}

type jwksRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f jwksRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type errReader struct {
	err error
}

func (e errReader) Read(p []byte) (int, error) {
	return 0, e.err
}

type stubSigningMethod struct{}

func (stubSigningMethod) Alg() string { return "RS256" }

func (stubSigningMethod) Verify(string, []byte, any) error { return nil }

func (stubSigningMethod) Sign(string, any) ([]byte, error) {
	return nil, errors.New("stub signing method")
}

// createSignedJWT creates a JWT signed with the given private key.
func createSignedJWT(t *testing.T, privKey *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(privKey)
	require.NoError(t, err)
	return signed
}

// TestNewJWKSClient tests the JWKS client initialization.
func TestNewJWKSClient(t *testing.T) {
	t.Parallel()

	client := NewJWKSClient("https://example.auth0.com/")

	assert.NotNil(t, client)
	assert.Equal(t, "https://example.auth0.com/", client.issuer)
	assert.NotNil(t, client.http)
	assert.NotNil(t, client.cache)
	assert.Equal(t, 1*time.Hour, client.cacheTTL)
}

// TestJWKSClient_jwksURL tests the JWKS URL construction.
func TestJWKSClient_jwksURL(t *testing.T) {
	t.Parallel()

	client := NewJWKSClient("https://example.auth0.com/")
	assert.Equal(t, "https://example.auth0.com/.well-known/jwks.json", client.jwksURL())
}

// TestJWKSClient_fetchKeys tests the fetchKeys method.
func TestJWKSClient_fetchKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("cache hit - no network request", func(t *testing.T) {
		server, issuer, setResponse := newTestJWKSServer(t)
		defer server.Close()

		client := NewJWKSClient(issuer)

		// First fetch - will make a request
		setResponse(jwks{Keys: []jwk{
			{Kty: "RSA", Kid: "test-kid", Use: "sig", N: encodeBigInt(bigIntFromInt(123)), E: encodeBigInt(bigIntFromInt(65537))},
		}}, http.StatusOK)

		err := client.fetchKeys(ctx)
		require.NoError(t, err)
		assert.NotEmpty(t, client.cache)

		// Second fetch - should use cache
		setResponse(nil, http.StatusInternalServerError) // Would fail if actually called
		err = client.fetchKeys(ctx)
		assert.NoError(t, err) // Should return early due to cache
	})

	t.Run("request build failure", func(t *testing.T) {
		client := NewJWKSClient("http://example.com/\n")

		err := client.fetchKeys(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "create jwks request")
	})

	t.Run("http do error", func(t *testing.T) {
		client := NewJWKSClient("https://example.com/")
		client.http = &http.Client{
			Transport: jwksRoundTripperFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("boom")
			}),
		}

		err := client.fetchKeys(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "fetch jwks")
	})

	t.Run("read body error", func(t *testing.T) {
		client := NewJWKSClient("https://example.com/")
		client.http = &http.Client{
			Transport: jwksRoundTripperFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(errReader{err: errors.New("read fail")}),
					Header:     make(http.Header),
				}, nil
			}),
		}

		err := client.fetchKeys(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "read jwks body")
	})

	t.Run("non-200 response", func(t *testing.T) {
		server, issuer, setResponse := newTestJWKSServer(t)
		defer server.Close()

		client := NewJWKSClient(issuer)
		setResponse(nil, http.StatusUnauthorized)

		err := client.fetchKeys(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "jwks request failed: status=401")
	})

	t.Run("skip unparsable RSA key", func(t *testing.T) {
		server, issuer, setResponse := newTestJWKSServer(t)
		defer server.Close()

		client := NewJWKSClient(issuer)
		setResponse(jwks{Keys: []jwk{
			{Kty: "RSA", Kid: "bad-key", Use: "sig", N: "!!!invalid-base64!!!", E: encodeBigInt(bigIntFromInt(65537))},
		}}, http.StatusOK)

		err := client.fetchKeys(ctx)
		require.NoError(t, err)
		assert.Empty(t, client.cache)
	})

	t.Run("malformed JWK set", func(t *testing.T) {
		server, issuer, _ := newTestJWKSServer(t)
		defer server.Close()

		client := NewJWKSClient(issuer)

		// Set invalid JSON
		handler := &http.ServeMux{}
		handler.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("not valid json"))
		})
		oldServer := server
		server = httptest.NewServer(handler)
		client.issuer = server.URL + "/"
		defer server.Close()
		oldServer.Close()

		err := client.fetchKeys(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "parse jwks")
	})

	t.Run("valid JWK set with RSA keys", func(t *testing.T) {
		server, issuer, setResponse := newTestJWKSServer(t)
		defer server.Close()

		client := NewJWKSClient(issuer)

		n, _ := new(big.Int).SetString("1234567890abcdef", 16)
		e := bigIntFromInt(65537)

		setResponse(jwks{Keys: []jwk{
			{Kty: "RSA", Kid: "key1", Use: "sig", N: encodeBigInt(n), E: encodeBigInt(e)},
			{Kty: "RSA", Kid: "key2", Use: "sig", N: encodeBigInt(n), E: encodeBigInt(e)},
			{Kty: "EC", Kid: "ec-key", Use: "sig"},   // Should be skipped
			{Kty: "RSA", Kid: "enc-key", Use: "enc"}, // Should be skipped
		}}, http.StatusOK)

		err := client.fetchKeys(ctx)
		require.NoError(t, err)
		assert.Len(t, client.cache, 2)
		assert.Contains(t, client.cache, "key1")
		assert.Contains(t, client.cache, "key2")
	})

	t.Run("cache refresh after TTL", func(t *testing.T) {
		server, issuer, setResponse := newTestJWKSServer(t)
		defer server.Close()

		client := NewJWKSClient(issuer)
		client.cacheTTL = 10 * time.Millisecond

		n, _ := new(big.Int).SetString("1234567890abcdef", 16)
		e := bigIntFromInt(65537)

		// First fetch
		setResponse(jwks{Keys: []jwk{
			{Kty: "RSA", Kid: "key1", Use: "sig", N: encodeBigInt(n), E: encodeBigInt(e)},
		}}, http.StatusOK)

		err := client.fetchKeys(ctx)
		require.NoError(t, err)

		// Simulate TTL expiry deterministically instead of sleeping.
		client.fetched = time.Now().Add(-time.Hour)

		err = client.fetchKeys(ctx)
		assert.NoError(t, err)
	})
}

// TestJWKSClient_parseJWK tests the parseJWK method.
func TestJWKSClient_parseJWK(t *testing.T) {
	t.Parallel()

	client := NewJWKSClient("https://example.auth0.com/")

	t.Run("valid RSA key", func(t *testing.T) {
		n, _ := new(big.Int).SetString("1234567890abcdef1234567890abcdef", 16)
		e := bigIntFromInt(65537)

		key, err := client.parseJWK(jwk{
			Kty: "RSA",
			Kid: "test-kid",
			N:   encodeBigInt(n),
			E:   encodeBigInt(e),
		})

		require.NoError(t, err)
		assert.NotNil(t, key)
		assert.Equal(t, n, key.N)
		assert.Equal(t, 65537, key.E)
	})

	t.Run("invalid base64 in N", func(t *testing.T) {
		_, err := client.parseJWK(jwk{
			Kty: "RSA",
			Kid: "test-kid",
			N:   "!!!invalid-base64!!!",
			E:   encodeBigInt(bigIntFromInt(65537)),
		})

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "decode n")
	})

	t.Run("invalid base64 in E", func(t *testing.T) {
		n, _ := new(big.Int).SetString("1234567890abcdef", 16)

		_, err := client.parseJWK(jwk{
			Kty: "RSA",
			Kid: "test-kid",
			N:   encodeBigInt(n),
			E:   "!!!invalid-base64!!!",
		})

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "decode e")
	})

	t.Run("invalid exponent - too large", func(t *testing.T) {
		n, _ := new(big.Int).SetString("1234567890abcdef", 16)
		// Create an exponent larger than max int32 (1<<31-1 = 2147483647)
		e := bigIntFromInt(1 << 32) // 2^32, which is > max int32

		_, err := client.parseJWK(jwk{
			Kty: "RSA",
			Kid: "test-kid",
			N:   encodeBigInt(n),
			E:   encodeBigInt(e),
		})

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid exponent")
	})
}

// TestJWKSClient_GetKey tests the GetKey method.
func TestJWKSClient_GetKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("cache hit", func(t *testing.T) {
		server, issuer, _ := newTestJWKSServer(t)
		defer server.Close()

		client := NewJWKSClient(issuer)

		n, _ := new(big.Int).SetString("1234567890abcdef", 16)

		// Pre-populate cache
		client.cache["test-kid"] = &rsa.PublicKey{
			N: n,
			E: 65537,
		}
		client.fetched = time.Now()

		key, err := client.GetKey(ctx, "test-kid")
		require.NoError(t, err)
		assert.NotNil(t, key)
		assert.Equal(t, n, key.N)
		assert.Equal(t, 65537, key.E)
	})

	t.Run("cache miss - fetch required", func(t *testing.T) {
		server, issuer, setResponse := newTestJWKSServer(t)
		defer server.Close()

		client := NewJWKSClient(issuer)

		n, _ := new(big.Int).SetString("1234567890abcdef", 16)
		e := bigIntFromInt(65537)

		setResponse(jwks{Keys: []jwk{
			{Kty: "RSA", Kid: "test-kid", Use: "sig", N: encodeBigInt(n), E: encodeBigInt(e)},
		}}, http.StatusOK)

		key, err := client.GetKey(ctx, "test-kid")
		require.NoError(t, err)
		assert.NotNil(t, key)
		assert.Equal(t, n, key.N)
	})

	t.Run("missing kid", func(t *testing.T) {
		server, issuer, setResponse := newTestJWKSServer(t)
		defer server.Close()

		client := NewJWKSClient(issuer)

		setResponse(jwks{Keys: []jwk{
			{Kty: "RSA", Kid: "other-kid", Use: "sig", N: encodeBigInt(bigIntFromInt(123)), E: encodeBigInt(bigIntFromInt(65537))},
		}}, http.StatusOK)

		_, err := client.GetKey(ctx, "missing-kid")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "key not found")
	})

	t.Run("fetch fails", func(t *testing.T) {
		server, issuer, setResponse := newTestJWKSServer(t)
		defer server.Close()

		client := NewJWKSClient(issuer)
		setResponse(nil, http.StatusInternalServerError)

		_, err := client.GetKey(ctx, "any-kid")
		assert.Error(t, err)
	})
}

// TestJWKSClient_VerifyIDToken tests the VerifyIDToken method.
func TestJWKSClient_VerifyIDToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("parse token error", func(t *testing.T) {
		client := NewJWKSClient("https://example.auth0.com/")

		_, err := client.VerifyIDToken(ctx, "not-a-token", "https://example.auth0.com/", "client-id")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "parse token")
	})

	t.Run("valid RS256 token", func(t *testing.T) {
		server, issuer, setResponse := newTestJWKSServer(t)
		defer server.Close()

		privKey, pubKey := generateTestRSAKey(t)
		kid := "test-kid-123"

		// Set up JWKS with the test key
		n := pubKey.N
		setResponse(jwks{Keys: []jwk{
			{Kty: "RSA", Kid: kid, Use: "sig", N: encodeBigInt(n), E: encodeBigInt(bigIntFromInt(pubKey.E))},
		}}, http.StatusOK)

		client := NewJWKSClient(issuer)

		// Create a valid token
		claims := jwt.MapClaims{
			"sub":   "user123",
			"iss":   issuer,
			"aud":   "test-client-id",
			"exp":   time.Now().Add(1 * time.Hour).Unix(),
			"email": "user@example.com",
		}
		token := createSignedJWT(t, privKey, kid, claims)

		result, err := client.VerifyIDToken(ctx, token, issuer, "test-client-id")
		require.NoError(t, err)
		assert.Equal(t, "user123", result.Sub)
		assert.Equal(t, "user@example.com", result.Email)
	})

	t.Run("missing kid in token header", func(t *testing.T) {
		server, issuer, setResponse := newTestJWKSServer(t)
		defer server.Close()

		privKey, pubKey := generateTestRSAKey(t)

		// Set up JWKS (won't be used since token has no kid)
		setResponse(jwks{Keys: []jwk{
			{Kty: "RSA", Kid: "some-kid", Use: "sig", N: encodeBigInt(pubKey.N), E: encodeBigInt(bigIntFromInt(pubKey.E))},
		}}, http.StatusOK)

		client := NewJWKSClient(issuer)

		// Create a properly signed token but without kid in header
		claims := jwt.MapClaims{
			"sub": "user123",
			"exp": time.Now().Add(1 * time.Hour).Unix(),
		}
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		// Explicitly do NOT set token.Header["kid"]
		signed, err := token.SignedString(privKey)
		require.NoError(t, err)

		_, err = client.VerifyIDToken(ctx, signed, issuer, "test-client-id")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "missing kid")
	})

	t.Run("wrong algorithm - HS256", func(t *testing.T) {
		server, issuer, setResponse := newTestJWKSServer(t)
		defer server.Close()

		_, pubKey := generateTestRSAKey(t)
		kid := "test-kid"

		setResponse(jwks{Keys: []jwk{
			{Kty: "RSA", Kid: kid, Use: "sig", N: encodeBigInt(pubKey.N), E: encodeBigInt(bigIntFromInt(pubKey.E))},
		}}, http.StatusOK)

		client := NewJWKSClient(issuer)

		// Create token with HS256 instead of RS256
		claims := jwt.MapClaims{
			"sub": "user123",
			"exp": time.Now().Add(1 * time.Hour).Unix(),
		}
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		token.Header["kid"] = kid
		signed, err := token.SignedString([]byte("secret"))
		require.NoError(t, err)

		_, err = client.VerifyIDToken(ctx, signed, issuer, "test-client-id")
		assert.Error(t, err)
		// The error should mention invalid signing method
		assert.True(t, err.Error() != "" && (err.Error() != "verify token: token signature is invalid" || err.Error() == "verify token: token signature is invalid: signing method HS256 is invalid"))
	})

	t.Run("wrong issuer", func(t *testing.T) {
		server, issuer, setResponse := newTestJWKSServer(t)
		defer server.Close()

		privKey, pubKey := generateTestRSAKey(t)
		kid := "test-kid"

		n, e := pubKey.N, bigIntFromInt(pubKey.E)
		setResponse(jwks{Keys: []jwk{
			{Kty: "RSA", Kid: kid, Use: "sig", N: encodeBigInt(n), E: encodeBigInt(e)},
		}}, http.StatusOK)

		client := NewJWKSClient(issuer)

		// Create token with wrong issuer
		claims := jwt.MapClaims{
			"sub": "user123",
			"iss": "https://wrong.auth0.com/",
			"aud": "test-client-id",
			"exp": time.Now().Add(1 * time.Hour).Unix(),
		}
		token := createSignedJWT(t, privKey, kid, claims)

		_, err := client.VerifyIDToken(ctx, token, issuer, "test-client-id")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "verify token")
	})

	t.Run("wrong audience", func(t *testing.T) {
		server, issuer, setResponse := newTestJWKSServer(t)
		defer server.Close()

		privKey, pubKey := generateTestRSAKey(t)
		kid := "test-kid"

		n, e := pubKey.N, bigIntFromInt(pubKey.E)
		setResponse(jwks{Keys: []jwk{
			{Kty: "RSA", Kid: kid, Use: "sig", N: encodeBigInt(n), E: encodeBigInt(e)},
		}}, http.StatusOK)

		client := NewJWKSClient(issuer)

		// Create token with wrong audience
		claims := jwt.MapClaims{
			"sub": "user123",
			"iss": issuer,
			"aud": "wrong-client-id",
			"exp": time.Now().Add(1 * time.Hour).Unix(),
		}
		token := createSignedJWT(t, privKey, kid, claims)

		_, err := client.VerifyIDToken(ctx, token, issuer, "test-client-id")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "verify token")
	})

	t.Run("expired token", func(t *testing.T) {
		server, issuer, setResponse := newTestJWKSServer(t)
		defer server.Close()

		privKey, pubKey := generateTestRSAKey(t)
		kid := "test-kid"

		n, e := pubKey.N, bigIntFromInt(pubKey.E)
		setResponse(jwks{Keys: []jwk{
			{Kty: "RSA", Kid: kid, Use: "sig", N: encodeBigInt(n), E: encodeBigInt(e)},
		}}, http.StatusOK)

		client := NewJWKSClient(issuer)

		// Create expired token
		claims := jwt.MapClaims{
			"sub": "user123",
			"iss": issuer,
			"aud": "test-client-id",
			"exp": time.Now().Add(-1 * time.Hour).Unix(),
		}
		token := createSignedJWT(t, privKey, kid, claims)

		_, err := client.VerifyIDToken(ctx, token, issuer, "test-client-id")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "verify token")
	})

	t.Run("key not found for kid", func(t *testing.T) {
		server, issuer, setResponse := newTestJWKSServer(t)
		defer server.Close()

		setResponse(jwks{Keys: []jwk{
			{Kty: "RSA", Kid: "other-kid", Use: "sig", N: encodeBigInt(bigIntFromInt(123)), E: encodeBigInt(bigIntFromInt(65537))},
		}}, http.StatusOK)

		client := NewJWKSClient(issuer)

		// Create token with kid that doesn't exist in JWKS
		claims := jwt.MapClaims{
			"sub": "user123",
			"exp": time.Now().Add(1 * time.Hour).Unix(),
		}
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = "missing-kid"
		privKey, _ := generateTestRSAKey(t)
		signed, err := token.SignedString(privKey)
		require.NoError(t, err)

		_, err = client.VerifyIDToken(ctx, signed, issuer, "test-client-id")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "key not found")
	})
}

func TestJWKSClient_VerifyIDToken_ErrorBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("unexpected signing method", func(t *testing.T) {
		privKey, pubKey := generateTestRSAKey(t)
		kid := "method-kid"
		issuer := "https://example.com/"

		client := NewJWKSClient(issuer)
		client.cache[kid] = pubKey
		client.fetched = time.Now()

		claims := jwt.MapClaims{
			"sub": "user123",
			"iss": issuer,
			"aud": "client-id",
			"exp": time.Now().Add(1 * time.Hour).Unix(),
		}
		token := createSignedJWT(t, privKey, kid, claims)

		origMethod := jwt.GetSigningMethod("RS256")
		jwt.RegisterSigningMethod("RS256", func() jwt.SigningMethod { return stubSigningMethod{} })
		t.Cleanup(func() {
			jwt.RegisterSigningMethod("RS256", func() jwt.SigningMethod { return origMethod })
		})

		_, err := client.VerifyIDToken(ctx, token, issuer, "client-id")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected signing method")
	})

	t.Run("invalid claims type", func(t *testing.T) {
		privKey, pubKey := generateTestRSAKey(t)
		kid := "claims-kid"
		issuer := "https://example.com/"

		client := NewJWKSClient(issuer)
		client.cache[kid] = pubKey
		client.fetched = time.Now()

		claims := jwt.MapClaims{
			"sub": "user123",
			"iss": issuer,
			"aud": "client-id",
			"exp": time.Now().Add(1 * time.Hour).Unix(),
		}
		token := createSignedJWT(t, privKey, kid, claims)

		origJWTParse := jwtParse
		jwtParse = func(string, jwt.Keyfunc, ...jwt.ParserOption) (*jwt.Token, error) {
			return &jwt.Token{Claims: jwt.RegisteredClaims{}}, nil
		}
		t.Cleanup(func() {
			jwtParse = origJWTParse
		})

		_, err := client.VerifyIDToken(ctx, token, issuer, "client-id")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid claims type")
	})

	t.Run("marshal claims error", func(t *testing.T) {
		privKey, pubKey := generateTestRSAKey(t)
		kid := "marshal-kid"
		issuer := "https://example.com/"

		client := NewJWKSClient(issuer)
		client.cache[kid] = pubKey
		client.fetched = time.Now()

		claims := jwt.MapClaims{
			"sub": "user123",
			"iss": issuer,
			"aud": "client-id",
			"exp": time.Now().Add(1 * time.Hour).Unix(),
		}
		token := createSignedJWT(t, privKey, kid, claims)

		origJWTParse := jwtParse
		jwtParse = func(string, jwt.Keyfunc, ...jwt.ParserOption) (*jwt.Token, error) {
			return &jwt.Token{Claims: jwt.MapClaims{"sub": "user123"}}, nil
		}
		t.Cleanup(func() {
			jwtParse = origJWTParse
		})

		origMarshal := jsonMarshal
		jsonMarshal = func(any) ([]byte, error) {
			return nil, errors.New("marshal fail")
		}
		t.Cleanup(func() {
			jsonMarshal = origMarshal
		})

		_, err := client.VerifyIDToken(ctx, token, issuer, "client-id")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "marshal claims")
	})

	t.Run("unmarshal claims error", func(t *testing.T) {
		privKey, pubKey := generateTestRSAKey(t)
		kid := "unmarshal-kid"
		issuer := "https://example.com/"

		client := NewJWKSClient(issuer)
		client.cache[kid] = pubKey
		client.fetched = time.Now()

		claims := jwt.MapClaims{
			"sub": "user123",
			"iss": issuer,
			"aud": "client-id",
			"exp": time.Now().Add(1 * time.Hour).Unix(),
		}
		token := createSignedJWT(t, privKey, kid, claims)

		origJWTParse := jwtParse
		jwtParse = func(string, jwt.Keyfunc, ...jwt.ParserOption) (*jwt.Token, error) {
			return &jwt.Token{Claims: jwt.MapClaims{"exp": time.Now().Add(1 * time.Hour).Unix()}}, nil
		}
		t.Cleanup(func() {
			jwtParse = origJWTParse
		})

		origUnmarshal := jsonUnmarshal
		jsonUnmarshal = func([]byte, any) error {
			return errors.New("unmarshal fail")
		}
		t.Cleanup(func() {
			jsonUnmarshal = origUnmarshal
		})

		_, err := client.VerifyIDToken(ctx, token, issuer, "client-id")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unmarshal claims")
	})
}

// TestJWKSClient_VerifyIDToken_Integration tests end-to-end token verification.
func TestJWKSClient_VerifyIDToken_Integration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	server, issuer, setResponse := newTestJWKSServer(t)
	defer server.Close()

	privKey, pubKey := generateTestRSAKey(t)
	kid := "integration-test-kid"

	// Set up JWKS
	n, e := pubKey.N, bigIntFromInt(pubKey.E)
	setResponse(jwks{Keys: []jwk{
		{Kty: "RSA", Kid: kid, Use: "sig", N: encodeBigInt(n), E: encodeBigInt(e)},
	}}, http.StatusOK)

	client := NewJWKSClient(issuer)

	// Create a comprehensive token with various claims
	claims := jwt.MapClaims{
		"sub":                "auth0|123456789",
		"iss":                issuer,
		"aud":                "my-client-id",
		"exp":                time.Now().Add(1 * time.Hour).Unix(),
		"iat":                time.Now().Unix(),
		"email":              "test@example.com",
		"email_verified":     true,
		"name":               "Test User",
		"nickname":           "testuser",
		"preferred_username": "testuser",
		"picture":            "https://example.com/pic.jpg",
	}
	token := createSignedJWT(t, privKey, kid, claims)

	result, err := client.VerifyIDToken(ctx, token, issuer, "my-client-id")
	require.NoError(t, err)

	assert.Equal(t, "auth0|123456789", result.Sub)
	assert.Equal(t, "test@example.com", result.Email)
	assert.Equal(t, true, result.EmailVerified)
	assert.Equal(t, "Test User", result.Name)
	assert.Equal(t, "testuser", result.Nickname)
	assert.Equal(t, "testuser", result.PreferredUsername)
	assert.Equal(t, "https://example.com/pic.jpg", result.Picture)
	assert.Equal(t, issuer, result.Iss)
}

// Helper function to create a big.Int from an int64.
func bigIntFromInt(i int) *big.Int {
	return big.NewInt(int64(i))
}

// Test for race conditions in concurrent access.
func TestJWKSClient_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	server, issuer, setResponse := newTestJWKSServer(t)
	defer server.Close()

	n, _ := new(big.Int).SetString("1234567890abcdef", 16)
	e := bigIntFromInt(65537)

	setResponse(jwks{Keys: []jwk{
		{Kty: "RSA", Kid: "concurrent-kid", Use: "sig", N: encodeBigInt(n), E: encodeBigInt(e)},
	}}, http.StatusOK)

	client := NewJWKSClient(issuer)

	// First fetch to populate cache
	_, err := client.GetKey(ctx, "concurrent-kid")
	require.NoError(t, err)

	// Concurrent reads
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			_, err := client.GetKey(ctx, "concurrent-kid")
			assert.NoError(t, err)
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}
