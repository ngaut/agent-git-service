// Package crypto provides NaCl sealed-box encryption for GitHub Actions secrets.
// The CLI encrypts secret values with the server's public key using box.SealAnonymous;
// this package generates the keypair and decrypts the sealed boxes.
//
// Configuration:
//   - SECRET_ENCRYPTION_KEY: base64-encoded 32-byte private key for
//     load-balanced deployments. When set, all server instances use the same
//     keypair.
//   - When unset, a new keypair is generated at startup (single-node mode only).
//
// Failure behavior:
//   - If SECRET_ENCRYPTION_KEY is set but invalid (wrong length, bad base64),
//     the server panics at startup with a descriptive error.
//   - In multi-instance mode, all instances MUST share the same key; otherwise
//     secrets encrypted against one instance cannot be decrypted by another.
package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"sync"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

var (
	publicKey  [32]byte
	privateKey [32]byte
	once       sync.Once
	initErr    error
)

// init loads the configured key or generates a new one.
// Panics if SECRET_ENCRYPTION_KEY is set but invalid.
func init() {
	mustInit()
}

func mustInit() {
	once.Do(func() {
		initErr = initKey()
		if initErr != nil {
			panic(fmt.Sprintf("crypto: failed to initialize keypair: %v", initErr))
		}
	})
}

// initKey loads the key from SECRET_ENCRYPTION_KEY if set, otherwise generates
// a new keypair for local development.
func initKey() error {
	keyB64 := os.Getenv("SECRET_ENCRYPTION_KEY")
	if keyB64 == "" {
		// Single-node mode: generate ephemeral keypair
		pub, priv, err := box.GenerateKey(rand.Reader)
		if err != nil {
			return fmt.Errorf("failed to generate NaCl keypair: %w", err)
		}
		publicKey = *pub
		privateKey = *priv
		return nil
	}

	// Multi-instance mode: load configured keypair.
	keyBytes, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return fmt.Errorf("SECRET_ENCRYPTION_KEY is not valid base64: %w", err)
	}
	if len(keyBytes) != 32 {
		return fmt.Errorf("SECRET_ENCRYPTION_KEY must be 32 bytes, got %d", len(keyBytes))
	}

	// Derive public key from private key (Curve25519 scalar base multiplication)
	privArray := [32]byte(keyBytes)
	var pubArray [32]byte
	curve25519.ScalarBaseMult(&pubArray, &privArray)

	privateKey = privArray
	publicKey = pubArray
	return nil
}

// PublicKeyBase64 returns the base64-encoded 32-byte Curve25519 public key.
func PublicKeyBase64() string {
	return base64.StdEncoding.EncodeToString(publicKey[:])
}

// PublicKeyID returns a stable identifier for the current public key.
func PublicKeyID() string {
	return base64.RawStdEncoding.EncodeToString(publicKey[:8])
}

// EncryptSecret encrypts a plaintext value using NaCl sealed box and returns base64-encoded ciphertext.
func EncryptSecret(plaintext string) (string, error) {
	encrypted, err := box.SealAnonymous(nil, []byte(plaintext), &publicKey, rand.Reader)
	if err != nil {
		return "", fmt.Errorf("crypto: encrypt failed: %w", err)
	}
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

// DecryptSecret decrypts a base64-encoded NaCl sealed box and returns the plaintext.
func DecryptSecret(encryptedBase64 string) (string, error) {
	encrypted, err := base64.StdEncoding.DecodeString(encryptedBase64)
	if err != nil {
		return "", fmt.Errorf("crypto: base64 decode failed: %w", err)
	}

	decrypted, ok := box.OpenAnonymous(nil, encrypted, &publicKey, &privateKey)
	if !ok {
		return "", fmt.Errorf("crypto: failed to open sealed box")
	}

	return string(decrypted), nil
}
