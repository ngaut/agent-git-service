package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
)

var claimLoginRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,38}$`)

type Auth0SessionResult struct {
	Token  string
	UserID uint
	Login  string
}

// Auth0LoginWithIDToken verifies an Auth0 id_token and returns a gh-server token
// for the linked user (creating/linking a local user when needed).
//
// This is used by web redirect login flows (Authorization Code + PKCE) where the
// browser obtains an id_token directly from Auth0 and then exchanges it with
// gh-server.
func (s *Service) Auth0LoginWithIDToken(ctx context.Context, idToken string) (Auth0SessionResult, error) {
	profile, err := s.verifyAuth0IDToken(ctx, idToken)
	if err != nil {
		return Auth0SessionResult{}, err
	}
	return s.auth0LoginWithProfile(ctx, profile)
}

// Auth0Login exchanges a device code for an Auth0 identity and returns a fresh
// gh-server token for the linked user. If the Auth0 subject is not yet linked,
// it creates a new normal user (with no repositories) and links it.
//
// Token policy: one new token per login (no revocation). Tokens are long-lived
// and no per-user LRU cap is enforced.
func (s *Service) Auth0Login(ctx context.Context, deviceCode string) (Auth0SessionResult, error) {
	profile, err := s.ExchangeAuth0DeviceCode(ctx, deviceCode)
	if err != nil {
		return Auth0SessionResult{}, err
	}
	return s.auth0LoginWithProfile(ctx, profile)
}

func (s *Service) auth0LoginWithProfile(ctx context.Context, profile Auth0Profile) (Auth0SessionResult, error) {

	const (
		maxAttempts      = 5
		maxLoginAttempts = 10
	)

	makeLoginCandidates := func(p Auth0Profile) []string {
		raw := []string{p.PreferredUsername, p.Nickname}
		if p.Email != "" {
			if at := strings.IndexByte(p.Email, '@'); at > 0 {
				raw = append(raw, p.Email[:at])
			}
		}
		raw = append(raw, p.Subject, "user")

		seen := map[string]struct{}{}
		candidates := make([]string, 0, len(raw))
		for _, c := range raw {
			base := strings.ToLower(strings.TrimSpace(c))
			if base == "" {
				continue
			}
			var b strings.Builder
			b.Grow(len(base))
			lastDash := false
			for _, r := range base {
				ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
				if ok {
					if r == '-' {
						if lastDash {
							continue
						}
						lastDash = true
					} else {
						lastDash = false
					}
					b.WriteRune(r)
					continue
				}
				if lastDash {
					continue
				}
				lastDash = true
				b.WriteByte('-')
			}
			out := strings.Trim(b.String(), "-_")
			for out != "" && !((out[0] >= 'a' && out[0] <= 'z') || (out[0] >= '0' && out[0] <= '9')) {
				out = strings.TrimLeft(out, "-_")
			}
			if out == "" {
				continue
			}
			if len(out) > 39 {
				out = out[:39]
				out = strings.TrimRight(out, "-_")
			}
			if out == "" || !claimLoginRE.MatchString(out) {
				continue
			}
			if _, exists := seen[out]; exists {
				continue
			}
			seen[out] = struct{}{}
			candidates = append(candidates, out)
		}
		return candidates
	}

	loginCandidates := makeLoginCandidates(profile)

attemptLoop:
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var out Auth0SessionResult
		err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
			var ident db.UserIdentity
			identErr := tx.Preload("User").First(&ident, "provider = ? AND subject = ?", profile.Provider, profile.Subject).Error
			var u db.User
			switch {
			case identErr == nil:
				u = ident.User
			case errors.Is(identErr, gorm.ErrRecordNotFound):
				// Create new user (no repos).
				var created db.User
				for i := 0; i < maxLoginAttempts && i < len(loginCandidates); i++ {
					login := loginCandidates[i]
					created = db.User{
						Login:       login,
						Name:        profile.DisplayName(login),
						Email:       profile.Email,
						Type:        db.TypeUser,
						UserKind:    db.UserKindHuman,
						IsAnonymous: false,
					}
					if err := tx.Create(&created).Error; err != nil {
						if isDuplicateErr(err) {
							continue
						}
						return err
					}
					break
				}
				if created.ID == 0 {
					return fmt.Errorf("%w: failed to allocate login after retries", ErrConflict)
				}
				// Link identity (race-safe via unique constraint).
				ident = db.UserIdentity{UserID: created.ID, Provider: profile.Provider, Subject: profile.Subject}
				if err := tx.Create(&ident).Error; err != nil {
					if isDuplicateErr(err) {
						return ErrConflict
					}
					return err
				}
				u = created
			default:
				return identErr
			}

			updates := map[string]any{}
			if profile.Email != "" {
				updates["email"] = profile.Email
			}
			if dn := profile.DisplayName(""); dn != "" {
				updates["name"] = dn
			}
			if u.UserKind == "" {
				updates["user_kind"] = db.UserKindHuman
			}
			if len(updates) > 0 {
				if err := tx.Model(&db.User{}).Where("id = ?", u.ID).Updates(updates).Error; err != nil {
					return err
				}
			}

			tok, err := issueUserTokenTx(tx, u.ID, time.Now(), "", nil)
			if err != nil {
				return err
			}

			out = Auth0SessionResult{Token: tok.Value, UserID: u.ID, Login: u.Login}
			return nil
		})
		if err == nil {
			slog.InfoContext(ctx, "auth0 login succeeded", "user_login", out.Login, "user_id", out.UserID)
			return out, nil
		}
		if errors.Is(err, ErrConflict) || isSQLiteLockErr(err) {
			slog.WarnContext(ctx, "auth0 login retry", "attempt", attempt+1, "error", err)
			time.Sleep(retryDelay(attempt))
			continue attemptLoop
		}
		slog.ErrorContext(ctx, "auth0 login failed", "attempt", attempt+1, "error", err)
		return Auth0SessionResult{}, err
	}

	slog.ErrorContext(ctx, "auth0 login exhausted retries", "error", ErrConflict)
	return Auth0SessionResult{}, fmt.Errorf("%w: auth0 login failed after retries", ErrConflict)
}
