package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/randutil"

	"gorm.io/gorm"
)

const (
	tokenHexLen         = 40
	tokenTouchMinWindow = 1 * time.Minute
)

func issueUserTokenTx(tx *gorm.DB, userID uint, now time.Time, name string, expiresAt *time.Time) (db.Token, error) {
	const maxTokenAttempts = 10

	now = now.UTC()
	if expiresAt != nil {
		exp := expiresAt.UTC()
		expiresAt = &exp
	}
	var tok db.Token
	for attempt := 0; attempt < maxTokenAttempts; attempt++ {
		tok = db.Token{
			UserID:     userID,
			Name:       name,
			Value:      randutil.Hex(tokenHexLen),
			LastUsedAt: &now,
			ExpiresAt:  expiresAt,
		}
		if err := tx.Create(&tok).Error; err != nil {
			if isDuplicateErr(err) {
				continue
			}
			return db.Token{}, fmt.Errorf("create token: %w", err)
		}
		break
	}
	if tok.ID == 0 {
		return db.Token{}, fmt.Errorf("%w: failed to allocate token after retries", ErrConflict)
	}

	return tok, nil
}

// ListTokens returns all tokens for a user.
func (s *Service) ListTokens(ctx context.Context, userID uint) ([]db.Token, error) {
	var tokens []db.Token
	if err := s.DBForCtx(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC, id DESC").
		Limit(defaultListLimit).
		Find(&tokens).Error; err != nil {
		return nil, err
	}
	return tokens, nil
}

// CreateUserToken creates a new token for a user, enforcing token caps.
func (s *Service) CreateUserToken(ctx context.Context, userID uint, name string, expiresAt *time.Time) (db.Token, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return db.Token{}, fmt.Errorf("%w: name is required", ErrValidation)
	}
	if expiresAt != nil {
		exp := expiresAt.UTC()
		if !exp.After(time.Now().UTC()) {
			return db.Token{}, fmt.Errorf("%w: expires_at must be in the future", ErrValidation)
		}
		expiresAt = &exp
	}

	var tok db.Token
	if err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		tok, err = issueUserTokenTx(tx, userID, time.Now(), name, expiresAt)
		return err
	}); err != nil {
		return db.Token{}, err
	}
	return tok, nil
}

// DeleteTokenByID deletes a token by ID scoped to the user.
func (s *Service) DeleteTokenByID(ctx context.Context, userID, tokenID uint) error {
	if tokenID == 0 {
		return fmt.Errorf("%w: token_id is required", ErrValidation)
	}
	return checkAffected(s.DBForCtx(ctx).Where("user_id = ? AND id = ?", userID, tokenID).Delete(&db.Token{}))
}

// DeleteTokenByValue deletes a token by raw token value scoped to the user.
func (s *Service) DeleteTokenByValue(ctx context.Context, userID uint, tokenValue string) error {
	tokenValue = strings.TrimSpace(tokenValue)
	if tokenValue == "" {
		return fmt.Errorf("%w: token is required", ErrValidation)
	}
	return checkAffected(s.DBForCtx(ctx).Where("user_id = ? AND value = ?", userID, tokenValue).Delete(&db.Token{}))
}

// TouchToken updates last_used_at for a token for recency tracking.
// To avoid a DB write on every request, updates are throttled to tokenTouchMinWindow
// with an in-memory dedup cache.
func (s *Service) TouchToken(ctx context.Context, tokenValue string) {
	tokenValue = strings.TrimSpace(tokenValue)
	if tokenValue == "" {
		return
	}

	now := time.Now().UTC()
	if v, ok := s.tokenTouchCache.Load(tokenValue); ok {
		if now.Sub(v.(time.Time)) < tokenTouchMinWindow {
			return // recently touched, skip DB entirely
		}
	}
	s.tokenTouchCache.Store(tokenValue, now)

	if err := s.DBForCtx(ctx).
		Model(&db.Token{}).
		Where("value = ? AND (last_used_at IS NULL OR last_used_at < ?)", tokenValue, now.Add(-tokenTouchMinWindow)).
		Update("last_used_at", now).Error; err != nil {
		s.tokenTouchCache.Delete(tokenValue)
		s.logTokenTouchFailure(ctx, tokenValue, err)
	}
}
