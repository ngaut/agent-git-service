package service

import (
	"context"
	"errors"

	"github.com/ngaut/agent-git-service/internal/db"
	"gorm.io/gorm"
)

type OIDCIdentityLookupResult struct {
	Linked bool
	User   db.User
}

func (s *Service) LookupOIDCIdentityWithIDToken(ctx context.Context, idToken string) (OIDCIdentityLookupResult, error) {
	profile, err := s.verifyOIDCIDToken(ctx, idToken)
	if err != nil {
		return OIDCIdentityLookupResult{}, err
	}
	var ident db.UserIdentity
	if err := s.DBForCtx(ctx).Preload("User").First(&ident, "provider = ? AND subject = ?", profile.Provider, profile.Subject).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return OIDCIdentityLookupResult{Linked: false}, nil
		}
		return OIDCIdentityLookupResult{}, err
	}
	return OIDCIdentityLookupResult{Linked: true, User: ident.User}, nil
}
