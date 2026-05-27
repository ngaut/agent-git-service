package auth0

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// jwks represents the Auth0 JSON Web Key Set response.
type jwks struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// JWKSClient fetches and caches Auth0's JSON Web Key Set.
type JWKSClient struct {
	issuer   string
	override string
	http     *http.Client
	cache    map[string]*rsa.PublicKey
	mu       sync.RWMutex
	fetched  time.Time
	cacheTTL time.Duration
}

// NewJWKSClient creates a new JWKS client for the given issuer.
func NewJWKSClient(issuer string) *JWKSClient {
	return &JWKSClient{
		issuer:   issuer,
		http:     &http.Client{Timeout: 10 * time.Second},
		cache:    make(map[string]*rsa.PublicKey),
		cacheTTL: 1 * time.Hour,
	}
}

// jwksURL returns the well-known JWKS endpoint for the issuer.
func (j *JWKSClient) jwksURL() string {
	if j.override != "" {
		return j.override
	}
	return j.issuer + ".well-known/jwks.json"
}

func (j *JWKSClient) OverrideURL(raw string) {
	j.override = raw
}

// fetchKeys retrieves the JWKS from Auth0 and caches the keys.
func (j *JWKSClient) fetchKeys(ctx context.Context) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	// Check if we have a valid cache
	if time.Since(j.fetched) < j.cacheTTL && len(j.cache) > 0 {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.jwksURL(), nil)
	if err != nil {
		return fmt.Errorf("create jwks request: %w", err)
	}

	resp, err := j.http.Do(req)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks request failed: status=%d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return fmt.Errorf("read jwks body: %w", err)
	}

	var keys jwks
	if err := json.Unmarshal(body, &keys); err != nil {
		return fmt.Errorf("parse jwks: %w", err)
	}

	// Parse and cache RSA keys
	j.cache = make(map[string]*rsa.PublicKey)
	for _, key := range keys.Keys {
		if key.Kty != "RSA" || key.Use != "sig" {
			continue
		}

		pubKey, err := j.parseJWK(key)
		if err != nil {
			continue // Skip keys we can't parse
		}
		j.cache[key.Kid] = pubKey
	}

	j.fetched = time.Now()
	return nil
}

// parseJWK converts a JWK to an RSA public key.
func (j *JWKSClient) parseJWK(key jwk) (*rsa.PublicKey, error) {
	nBytes, err := base64URLEncoding.DecodeString(key.N)
	if err != nil {
		return nil, fmt.Errorf("decode n: %w", err)
	}

	eBytes, err := base64URLEncoding.DecodeString(key.E)
	if err != nil {
		return nil, fmt.Errorf("decode e: %w", err)
	}

	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)

	if e.Int64() > 1<<31-1 {
		return nil, errors.New("invalid exponent")
	}

	return &rsa.PublicKey{
		N: n,
		E: int(e.Int64()),
	}, nil
}

// GetKey returns the RSA public key for the given kid.
func (j *JWKSClient) GetKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	j.mu.RLock()
	if key, ok := j.cache[kid]; ok && time.Since(j.fetched) < j.cacheTTL {
		j.mu.RUnlock()
		return key, nil
	}
	j.mu.RUnlock()

	// Need to fetch
	if err := j.fetchKeys(ctx); err != nil {
		return nil, err
	}

	j.mu.RLock()
	defer j.mu.RUnlock()

	key, ok := j.cache[kid]
	if !ok {
		return nil, fmt.Errorf("key not found: kid=%s", kid)
	}
	return key, nil
}

// base64URLEncoding is the base64 raw URL encoding (no padding).
var base64URLEncoding = base64.RawURLEncoding

// test hooks for error injection in VerifyIDToken.
var (
	jwtParse      = jwt.Parse
	jsonMarshal   = json.Marshal
	jsonUnmarshal = json.Unmarshal
)

// VerifyIDToken verifies the JWT signature and returns the claims.
func (j *JWKSClient) VerifyIDToken(ctx context.Context, idToken string, expectedIssuer, expectedAudience string) (IDTokenClaims, error) {
	// First, parse without verification to get the kid
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	token, _, err := parser.ParseUnverified(idToken, jwt.MapClaims{})
	if err != nil {
		return IDTokenClaims{}, fmt.Errorf("parse token: %w", err)
	}

	kid, ok := token.Header["kid"].(string)
	if !ok {
		return IDTokenClaims{}, errors.New("missing kid in token header")
	}

	// Get the public key
	pubKey, err := j.GetKey(ctx, kid)
	if err != nil {
		return IDTokenClaims{}, fmt.Errorf("get key: %w", err)
	}

	// Now verify the token with proper validation
	token, err = jwtParse(idToken, func(token *jwt.Token) (any, error) {
		// Validate signing method
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return pubKey, nil
	}, jwt.WithIssuer(expectedIssuer), jwt.WithAudience(expectedAudience), jwt.WithValidMethods([]string{"RS256"}))

	if err != nil {
		return IDTokenClaims{}, fmt.Errorf("verify token: %w", err)
	}

	// Extract claims from token
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return IDTokenClaims{}, errors.New("invalid claims type")
	}

	// Convert MapClaims to IDTokenClaims
	claimsJSON, err := jsonMarshal(claims)
	if err != nil {
		return IDTokenClaims{}, fmt.Errorf("marshal claims: %w", err)
	}

	var result IDTokenClaims
	if err := jsonUnmarshal(claimsJSON, &result); err != nil {
		return IDTokenClaims{}, fmt.Errorf("unmarshal claims: %w", err)
	}

	return result, nil
}
