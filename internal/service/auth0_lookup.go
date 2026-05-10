package service

import (
	"context"
	"errors"

	"gh-server/internal/db"

	"gorm.io/gorm"
)

type Auth0IdentityLookupResult struct {
	Linked bool
	User   db.User
}

// LookupAuth0IdentityWithIDToken verifies an Auth0 id_token and reports whether
// the identity is already linked to a local user.
func (s *Service) LookupAuth0IdentityWithIDToken(ctx context.Context, idToken string) (Auth0IdentityLookupResult, error) {
	profile, err := s.verifyAuth0IDToken(ctx, idToken)
	if err != nil {
		return Auth0IdentityLookupResult{}, err
	}

	var ident db.UserIdentity
	if err := s.DBForCtx(ctx).Preload("User").First(&ident, "provider = ? AND subject = ?", profile.Provider, profile.Subject).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Auth0IdentityLookupResult{Linked: false}, nil
		}
		return Auth0IdentityLookupResult{}, err
	}

	return Auth0IdentityLookupResult{
		Linked: true,
		User:   ident.User,
	}, nil
}
