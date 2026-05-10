package middleware

// Re-export authn types for backward compatibility and convenience.
// This allows middleware code to use TokenResolver and related errors
// without importing authn directly in every file.

import (
	"gh-server/internal/authn"
)

// TokenResolver resolves an auth token to a tenant user and database handle.
// See authn.TokenResolver for details.
type TokenResolver = authn.TokenResolver

// ErrUnknownToken indicates the token was not found.
var ErrUnknownToken = authn.ErrUnknownToken

// ErrInactiveUser indicates the user account is not active.
var ErrInactiveUser = authn.ErrInactiveUser
