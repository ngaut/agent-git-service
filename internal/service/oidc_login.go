package service

import (
	"context"
)

func (s *Service) OIDCLoginWithIDToken(ctx context.Context, idToken string) (OIDCSessionResult, error) {
	profile, err := s.verifyOIDCIDToken(ctx, idToken)
	if err != nil {
		return OIDCSessionResult{}, err
	}
	return s.oidcLoginWithProfile(ctx, profile)
}

func (s *Service) OIDCLogin(ctx context.Context, deviceCode string) (OIDCSessionResult, error) {
	profile, err := s.ExchangeOIDCDeviceCode(ctx, deviceCode)
	if err != nil {
		return OIDCSessionResult{}, err
	}
	return s.oidcLoginWithProfile(ctx, profile)
}
