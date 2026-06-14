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

type OIDCSessionResult struct {
	Token  string
	UserID uint
	Login  string
}

func (s *Service) oidcLoginWithProfile(ctx context.Context, profile OIDCProfile) (OIDCSessionResult, error) {
	const (
		maxAttempts      = 5
		maxLoginAttempts = 10
	)
	userKind := strings.TrimSpace(profile.UserKind)
	explicitUserKind := userKind != ""
	switch userKind {
	case "":
		userKind = db.UserKindHuman
	case db.UserKindHuman, db.UserKindAgent:
	default:
		return OIDCSessionResult{}, fmt.Errorf("%w: invalid user_kind", ErrValidation)
	}

	makeLoginCandidates := func(p OIDCProfile) []string {
		raw := append([]string{}, p.LoginCandidates...)
		raw = append(raw, p.PreferredUsername, p.Nickname)
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
		var out OIDCSessionResult
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
						Login:    login,
						Name:     profile.DisplayName(login),
						Email:    profile.Email,
						Type:     db.TypeUser,
						UserKind: userKind,
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
			if explicitUserKind && u.UserKind != userKind {
				updates["user_kind"] = userKind
			} else if u.UserKind == "" {
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

			out = OIDCSessionResult{Token: tok.Value, UserID: u.ID, Login: u.Login}
			return nil
		})
		if err == nil {
			slog.InfoContext(ctx, "oidc login succeeded", "user_login", out.Login, "user_id", out.UserID)
			return out, nil
		}
		if errors.Is(err, ErrConflict) || isSQLiteLockErr(err) {
			slog.WarnContext(ctx, "oidc login retry", "attempt", attempt+1, "error", err)
			time.Sleep(retryDelay(attempt))
			continue attemptLoop
		}
		slog.ErrorContext(ctx, "oidc login failed", "attempt", attempt+1, "error", err)
		return OIDCSessionResult{}, err
	}

	slog.ErrorContext(ctx, "oidc login exhausted retries", "error", ErrConflict)
	return OIDCSessionResult{}, fmt.Errorf("%w: oidc login failed after retries", ErrConflict)
}
