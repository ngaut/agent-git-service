// Package authn provides authentication interfaces and error types.
// This package sits at a low layer to avoid import cycles between
// middleware, controlplane, and rest packages.
package authn

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/db"
)

// Sentinel errors for token resolution failures.
var (
	ErrUnknownToken = errors.New("unknown token")
	ErrInactiveUser = errors.New("inactive user")
)

// TokenResolver resolves an auth token to a tenant user and database handle.
// This interface allows higher layers to depend on an abstraction rather than
// the concrete controlplane.DBRouter type, preserving layer boundaries.
//
// Implementations should return ErrUnknownToken or ErrInactiveUser for
// specific failure modes.
type TokenResolver interface {
	ResolveToken(ctx context.Context, token string) (db.User, *gorm.DB, error)
}
