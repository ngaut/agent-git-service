// Package randutil provides cryptographically secure random string helpers.
package randutil

import (
	"crypto/rand"
	"encoding/hex"
)

// Hex returns a cryptographically secure random hex string of exactly n characters.
func Hex(n int) string {
	b := make([]byte, (n+1)/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}
