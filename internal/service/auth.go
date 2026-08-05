package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	applog "github.com/ngaut/agent-git-service/internal/logging"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// tokenQueryTimeout is the timeout for individual token lookup queries.
// This prevents PD server timeout errors in TiDB from blocking requests indefinitely.
const tokenQueryTimeout = 10 * time.Second

// tokenQueryMaxRetries is the maximum number of retries for token lookup queries.
const tokenQueryMaxRetries = 3

// AuthorizationCodeTTL is the lifetime of an OAuth authorization code.
const AuthorizationCodeTTL = 10 * time.Minute

type TokenValidationFailure string

const (
	TokenValidationFailureNone               TokenValidationFailure = ""
	TokenValidationFailureUnknownToken       TokenValidationFailure = "unknown_token"
	TokenValidationFailureExpiredToken       TokenValidationFailure = "expired_token"
	TokenValidationFailureNoTokensRegistered TokenValidationFailure = "no_tokens_registered"
	TokenValidationFailureLookupError        TokenValidationFailure = "token_lookup_error"
	TokenValidationFailureAdminLookupError   TokenValidationFailure = "admin_lookup_error"
)

var errDeviceTokenNotVisible = errors.New("device token not visible after upsert")

// withTokenQueryTimeout creates a context with timeout for token queries.
func withTokenQueryTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, tokenQueryTimeout)
}

// tokenQueryWithRetry executes a token query function with retry logic.
// It handles transient TiDB PD server timeout errors by retrying with exponential backoff.
func tokenQueryWithRetry(ctx context.Context, fn func(ctx context.Context) error) error {
	var lastErr error
	for attempt := 0; attempt < tokenQueryMaxRetries; attempt++ {
		timeoutCtx, cancel := withTokenQueryTimeout(ctx)
		err := fn(timeoutCtx)
		cancel()

		if err == nil {
			return nil
		}

		lastErr = err
		// Check if this is a retryable error (timeout, connection issues)
		if isRetryableTokenErr(err) {
			// Exponential backoff: 100ms, 200ms, 400ms
			backoff := time.Duration(100*(1<<uint(attempt))) * time.Millisecond
			slog.WarnContext(ctx, "token query retry",
				"attempt", attempt+1,
				"max_retries", tokenQueryMaxRetries,
				"backoff_ms", backoff.Milliseconds(),
				"error", err,
			)
			select {
			case <-time.After(backoff):
				// Continue to next retry
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		// Non-retryable error, return immediately
		return err
	}
	return fmt.Errorf("token query failed after %d retries: %w", tokenQueryMaxRetries, lastErr)
}

// isRetryableTokenErr checks if an error is retryable for token queries.
// This includes TiDB PD server timeout errors and transient connection issues.
func isRetryableTokenErr(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// TiDB PD server timeout errors
	if containsAny(errStr, "pd server timeout", "context deadline exceeded", "connection refused", "connection reset") {
		return true
	}
	return false
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func normalizeUserStatus(status string) string {
	normalized := strings.ToLower(strings.TrimSpace(status))
	if normalized == "" {
		return db.UserStatusActive
	}
	return normalized
}

func isUserStatusActive(status string) bool {
	return normalizeUserStatus(status) == db.UserStatusActive
}

// CreateDeviceCode stores a new OAuth device code and returns it.
func (s *Service) CreateDeviceCode(ctx context.Context, code *db.DeviceCode) error {
	return s.DBForCtx(ctx).Create(code).Error
}

// CreateAuthorizationCode stores a new OAuth authorization code and returns it.
func (s *Service) CreateAuthorizationCode(ctx context.Context, code *db.AuthorizationCode) error {
	return s.DBForCtx(ctx).Create(code).Error
}

// GetDeviceCodeByUserCode looks up a device code by its user-facing code.
func (s *Service) GetDeviceCodeByUserCode(ctx context.Context, userCode string) (*db.DeviceCode, error) {
	var code db.DeviceCode
	if err := s.DBForCtx(ctx).First(&code, "user_code = ?", userCode).Error; err != nil {
		return nil, wrapErr(err)
	}
	return &code, nil
}

// LogDeviceCodeAudit creates an audit log entry for device code events.
func (s *Service) LogDeviceCodeAudit(ctx context.Context, deviceCodeID uint, deviceCode, event string, userID uint, userLogin, details string) error {
	audit := &db.DeviceCodeAuditLog{
		DeviceCodeID: deviceCodeID,
		DeviceCode:   deviceCode,
		Event:        event,
		Details:      details,
		CreatedAt:    time.Now().UTC(),
	}
	if userID != 0 {
		audit.UserID = &userID
	}
	if userLogin != "" {
		audit.UserLogin = userLogin
	}
	return s.DBForCtx(ctx).Create(audit).Error
}

// ApproveDeviceCode approves a device code by generating an access token and
// associating it with the specified user. Returns the generated access token.
// This implements the user verification and approval requirement.
func (s *Service) ApproveDeviceCode(ctx context.Context, deviceCode string, userID uint, userLogin string) (string, error) {
	if userID == 0 {
		return "", ErrUnauthorized
	}

	var accessToken string
	err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		var approver db.User
		if err := tx.Select("id", "login", "type", "status").First(&approver, "id = ?", userID).Error; err != nil {
			return wrapErr(err)
		}
		if approver.Type != db.TypeUser {
			return ErrUnauthorized
		}
		if !isUserStatusActive(approver.Status) {
			return ErrForbidden
		}

		var code db.DeviceCode
		if err := tx.First(&code, "device_code = ?", deviceCode).Error; err != nil {
			return ErrNotFound
		}

		// Check expiration
		if time.Now().UTC().After(code.ExpiresAt) {
			tx.Model(&code).Updates(map[string]any{
				"state": db.DeviceCodeStateExpired,
			})
			return ErrDeviceCodeExpired
		}

		// Must be in pending state to approve
		if code.State != db.DeviceCodeStatePending {
			return fmt.Errorf("%w: device code not in pending state: %s", ErrConflict, code.State)
		}

		now := time.Now().UTC()
		tok, err := issueUserTokenTx(tx, approver.ID, now, "oauth device code", nil)
		if err != nil {
			return err
		}
		accessToken = tok.Value

		// Update device code
		if err := tx.Model(&code).Updates(map[string]any{
			"state":        db.DeviceCodeStateApproved,
			"access_token": accessToken,
			"approved_by":  approver.ID,
			"approved_at":  now,
		}).Error; err != nil {
			return err
		}

		// Log approval
		audit := &db.DeviceCodeAuditLog{
			DeviceCodeID: code.ID,
			DeviceCode:   code.DeviceCode,
			Event:        "approved",
			UserID:       &approver.ID,
			UserLogin:    approver.Login,
			CreatedAt:    now,
		}
		return tx.Create(audit).Error
	})
	if err != nil {
		return "", err
	}
	return accessToken, nil
}

// RejectDeviceCode rejects a device code, preventing token issuance.
func (s *Service) RejectDeviceCode(ctx context.Context, deviceCode string, userID uint, userLogin, reason string) error {
	if userID == 0 {
		return ErrUnauthorized
	}

	return s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		var rejecter db.User
		if err := tx.Select("id", "login", "type", "status").First(&rejecter, "id = ?", userID).Error; err != nil {
			return wrapErr(err)
		}
		if rejecter.Type != db.TypeUser {
			return ErrUnauthorized
		}
		if !isUserStatusActive(rejecter.Status) {
			return ErrForbidden
		}

		var code db.DeviceCode
		if err := tx.First(&code, "device_code = ?", deviceCode).Error; err != nil {
			return ErrNotFound
		}

		now := time.Now().UTC()
		if now.After(code.ExpiresAt) {
			tx.Model(&code).Updates(map[string]any{
				"state": db.DeviceCodeStateExpired,
			})
			return ErrDeviceCodeExpired
		}
		if code.State != db.DeviceCodeStatePending {
			return fmt.Errorf("%w: device code not in pending state: %s", ErrConflict, code.State)
		}

		if err := tx.Model(&code).Updates(map[string]any{
			"state": db.DeviceCodeStateRejected,
		}).Error; err != nil {
			return err
		}

		// Log rejection
		audit := &db.DeviceCodeAuditLog{
			DeviceCodeID: code.ID,
			DeviceCode:   code.DeviceCode,
			Event:        "rejected",
			UserID:       &rejecter.ID,
			UserLogin:    rejecter.Login,
			Details:      reason,
			CreatedAt:    now,
		}
		return tx.Create(audit).Error
	})
}

func validateAuthorizationCodePKCE(code db.AuthorizationCode, codeVerifier string) error {
	if code.CodeChallenge == "" && code.CodeChallengeMethod == "" {
		return nil
	}
	if strings.TrimSpace(codeVerifier) == "" {
		return ErrPKCEVerifierRequired
	}
	if code.CodeChallengeMethod != "S256" {
		return fmt.Errorf("authorization code uses unsupported pkce method %q: %w", code.CodeChallengeMethod, ErrInvalidState)
	}

	sum := sha256.Sum256([]byte(codeVerifier))
	encoded := base64.RawURLEncoding.EncodeToString(sum[:])
	if encoded != code.CodeChallenge {
		return ErrPKCEVerifierMismatch
	}
	return nil
}

// ExchangeAuthorizationCode validates an OAuth authorization code and returns
// a freshly-issued access token for the stored user.
func (s *Service) ExchangeAuthorizationCode(ctx context.Context, codeValue, codeVerifier string) (accessToken string, err error) {
	const maxRetries = 5

	for attempt := 0; attempt < maxRetries; attempt++ {
		if err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
			var code db.AuthorizationCode
			if err := tx.First(&code, "code = ?", strings.TrimSpace(codeValue)).Error; err != nil {
				return ErrNotFound
			}

			now := time.Now().UTC()
			if code.UsedAt != nil || now.After(code.ExpiresAt) {
				return ErrNotFound
			}
			if err := validateAuthorizationCodePKCE(code, codeVerifier); err != nil {
				return err
			}
			if code.UserID == nil || *code.UserID == 0 {
				return ErrUnauthorized
			}

			var user db.User
			if err := tx.Select("id", "type", "status").First(&user, "id = ?", *code.UserID).Error; err != nil {
				return wrapErr(err)
			}
			if user.Type != db.TypeUser {
				return ErrUnauthorized
			}
			if !isUserStatusActive(user.Status) {
				return ErrForbidden
			}

			tok, err := issueUserTokenTx(tx, user.ID, now, "oauth authorization code", nil)
			if err != nil {
				return err
			}

			result := tx.Model(&db.AuthorizationCode{}).
				Where("id = ? AND used_at IS NULL", code.ID).
				Update("used_at", now)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrConflict
			}

			accessToken = tok.Value
			return nil
		}); err != nil {
			return "", err
		}
		return accessToken, nil
	}

	return "", fmt.Errorf("exchange authorization code: failed after %d retries", maxRetries)
}

// ExchangeDeviceCode looks up a device code and, if valid + approved, ensures
// a Token row exists and returns the access token. Returns empty string on failure.
// Device codes must be in "approved" state and not expired.
func (s *Service) ExchangeDeviceCode(ctx context.Context, deviceCode string) (accessToken string, err error) {
	const maxRetries = 5
	for attempt := 0; attempt < maxRetries; attempt++ {
		if err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
			var code db.DeviceCode
			if err := tx.First(&code, "device_code = ?", deviceCode).Error; err != nil {
				return ErrNotFound
			}

			// Check expiration first
			if time.Now().UTC().After(code.ExpiresAt) {
				// Mark as expired if still pending
				if code.State == db.DeviceCodeStatePending {
					tx.Model(&code).Updates(map[string]any{
						"state": db.DeviceCodeStateExpired,
					})
				}
				return ErrDeviceCodeExpired
			}

			// Check state
			switch code.State {
			case db.DeviceCodeStatePending:
				return ErrDeviceCodePending
			case db.DeviceCodeStateRejected:
				return ErrDeviceCodeRejected
			case db.DeviceCodeStateExpired:
				return ErrDeviceCodeExpired
			case db.DeviceCodeStateApproved:
				// Continue to exchange
			default:
				return fmt.Errorf("unknown device code state: %s", code.State)
			}

			// Must have access token if approved
			if code.AccessToken == "" {
				return fmt.Errorf("device code approved but no access token: %w", ErrInvalidState)
			}
			if code.ApprovedBy == nil || *code.ApprovedBy == 0 {
				return fmt.Errorf("device code approved but has no approver: %w", ErrInvalidState)
			}

			// Ensure a persistent token row exists for the access token atomically.
			var approver db.User
			if err := tx.Select("id", "type").First(&approver, "id = ?", *code.ApprovedBy).Error; err != nil {
				return fmt.Errorf("lookup approving user: %w", wrapErr(err))
			}
			if approver.Type != db.TypeUser {
				return fmt.Errorf("device code approver is not a user: %w", ErrInvalidState)
			}

			now := time.Now().UTC()
			tok := db.Token{UserID: approver.ID, Value: code.AccessToken, LastUsedAt: &now}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "value"}},
				DoUpdates: clause.Assignments(map[string]any{
					"last_used_at": now,
				}),
			}).Create(&tok).Error; err != nil {
				return err
			}

			// Ensure this token belongs to the approving user (defense in depth).
			var persisted db.Token
			if err := tx.Select("id", "user_id").Take(&persisted, "value = ?", code.AccessToken).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errDeviceTokenNotVisible
				}
				return err
			}
			if persisted.UserID != approver.ID {
				return ErrConflict
			}

			// Mark this token as recently used.
			if err := tx.Model(&db.Token{}).Where("id = ?", persisted.ID).Update("last_used_at", now).Error; err != nil {
				return err
			}

			accessToken = code.AccessToken
			return nil
		}); err != nil {
			if errors.Is(err, errDeviceTokenNotVisible) {
				time.Sleep(retryDelay(attempt))
				continue
			}
			return "", err
		}
		return accessToken, nil
	}

	return "", fmt.Errorf("exchange device code: failed after %d retries", maxRetries)
}

// ValidateAndResolveToken validates the token and resolves the owning user in
// a single pass.
//
// Returns the user and true if valid, or zero-value and false if invalid.
func (s *Service) ValidateAndResolveToken(ctx context.Context, token string) (db.User, bool) {
	u, failure, err := s.ValidateAndResolveTokenDetailed(ctx, token)
	return u, err == nil && failure == TokenValidationFailureNone
}

// ValidateAndResolveTokenDetailed validates the token and returns a stable
// failure classification for logging and diagnostics.
func (s *Service) ValidateAndResolveTokenDetailed(ctx context.Context, token string) (db.User, TokenValidationFailure, error) {
	// Happy path: look up token directly (1 query instead of COUNT + SELECT).
	var tok db.Token
	// Use retry logic for token lookup to handle TiDB PD timeouts
	if err := tokenQueryWithRetry(ctx, func(qctx context.Context) error {
		return s.DBForCtx(qctx).Joins("User").Take(&tok, "value = ?", token).Error
	}); err == nil {
		if tok.ExpiresAt != nil && !tok.ExpiresAt.After(time.Now().UTC()) {
			return db.User{}, TokenValidationFailureExpiredToken, nil
		}
		return tok.User, TokenValidationFailureNone, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return db.User{}, TokenValidationFailureLookupError, err
	}

	var count int64
	// Use retry logic for token count query to handle TiDB PD timeouts
	if err := tokenQueryWithRetry(ctx, func(qctx context.Context) error {
		return s.DBForCtx(qctx).Model(&db.Token{}).Count(&count).Error
	}); err != nil {
		return db.User{}, TokenValidationFailureLookupError, err
	}

	// Token not found — check AllowAnyToken fallback (empty table).
	if !s.AllowAnyToken {
		if count == 0 {
			return db.User{}, TokenValidationFailureNoTokensRegistered, nil
		}
		return db.User{}, TokenValidationFailureUnknownToken, nil
	}
	if count > 0 {
		return db.User{}, TokenValidationFailureUnknownToken, nil
	}
	var u db.User
	if err := s.DBForCtx(ctx).First(&u, "type = ? AND site_admin = ?", db.TypeUser, true).Error; err != nil {
		return db.User{}, TokenValidationFailureAdminLookupError, err
	}
	return u, TokenValidationFailureNone, nil
}

func (s *Service) logTokenTouchFailure(ctx context.Context, tokenValue string, err error) {
	slog.WarnContext(ctx, "token touch failed",
		"token_fingerprint", applog.TokenFingerprint(tokenValue),
		"error", err,
	)
}
