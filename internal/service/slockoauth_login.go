package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/slockoauth"

	"gorm.io/gorm"
)

// SlockOAuthFlow is the subset of slockoauth.Client behavior the service needs.
// Defined as an interface to allow stubbing in tests.
type SlockOAuthFlow interface {
	ExchangeCode(ctx context.Context, code string) (slockoauth.Token, error)
	Userinfo(ctx context.Context, accessToken string) (slockoauth.Userinfo, error)
	LoginURL() string
	CallbackURL() string
	ClientID() string
}

// SlockSessionResult is returned to the REST callback handler.
type SlockSessionResult struct {
	Token    string
	UserID   uint
	Login    string
	Type     string // "human" | "agent"
	Sub      string
	ServerID string
}

var ErrSlockNotConfigured = errors.New("login with slock is not configured")

// SlockLoginWithCode implements the OAuth code-exchange + userinfo + token-mint
// flow for /auth/slock/callback. Token issuance goes through the existing
// db.Token path (Ray refinement #1, msg=5897ba9a) so callers receive a
// gh-server-owned token, not the Slock access token.
func (s *Service) SlockLoginWithCode(ctx context.Context, code string) (SlockSessionResult, error) {
	if s.SlockOAuth == nil {
		return SlockSessionResult{}, ErrSlockNotConfigured
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return SlockSessionResult{}, fmt.Errorf("%w: code is required", ErrValidation)
	}

	tok, err := s.SlockOAuth.ExchangeCode(ctx, code)
	if err != nil {
		slog.WarnContext(ctx, "slock oauth code exchange failed", "error", err)
		return SlockSessionResult{}, err
	}
	ui, err := s.SlockOAuth.Userinfo(ctx, tok.AccessToken)
	if err != nil {
		slog.WarnContext(ctx, "slock oauth userinfo failed", "error", err)
		return SlockSessionResult{}, err
	}

	subject := slockSubject(ui.ServerID, ui.Sub)
	loginCandidates := slockLoginCandidates(ui)
	userKind := db.UserKindHuman
	if ui.Type == "agent" {
		userKind = db.UserKindAgent
	}

	const maxAttempts = 5
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var out SlockSessionResult
		err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
			var ident db.UserIdentity
			identErr := tx.Preload("User").
				First(&ident, "provider = ? AND subject = ?", "slock", subject).Error
			var u db.User
			switch {
			case identErr == nil:
				u = ident.User
			case errors.Is(identErr, gorm.ErrRecordNotFound):
				var created db.User
				for i := 0; i < len(loginCandidates); i++ {
					candidate := loginCandidates[i]
					created = db.User{
						Login:       candidate,
						Name:        slockDisplayName(ui, candidate),
						Type:        db.TypeUser,
						UserKind:    userKind,
						IsAnonymous: false,
					}
					if cerr := tx.Create(&created).Error; cerr != nil {
						if isDuplicateErr(cerr) {
							continue
						}
						return cerr
					}
					break
				}
				if created.ID == 0 {
					return fmt.Errorf("%w: failed to allocate login after retries", ErrConflict)
				}
				ident = db.UserIdentity{
					UserID:   created.ID,
					Provider: "slock",
					Subject:  subject,
				}
				if lerr := tx.Create(&ident).Error; lerr != nil {
					if isDuplicateErr(lerr) {
						return ErrConflict
					}
					return lerr
				}
				u = created
			default:
				return identErr
			}

			updates := map[string]any{}
			if dn := slockDisplayName(ui, ""); dn != "" {
				updates["name"] = dn
			}
			if u.UserKind != userKind {
				updates["user_kind"] = userKind
			}
			if len(updates) > 0 {
				if uerr := tx.Model(&db.User{}).Where("id = ?", u.ID).Updates(updates).Error; uerr != nil {
					return uerr
				}
			}

			minted, terr := issueUserTokenTx(tx, u.ID, time.Now(), "login-with-slock", nil)
			if terr != nil {
				return terr
			}
			out = SlockSessionResult{
				Token:    minted.Value,
				UserID:   u.ID,
				Login:    u.Login,
				Type:     ui.Type,
				Sub:      ui.Sub,
				ServerID: ui.ServerID,
			}
			return nil
		})
		if err == nil {
			slog.InfoContext(ctx, "slock oauth login succeeded",
				"user_login", out.Login,
				"user_id", out.UserID,
				"type", out.Type,
				"server_id", out.ServerID,
			)
			return out, nil
		}
		if errors.Is(err, ErrConflict) || isSQLiteLockErr(err) {
			slog.WarnContext(ctx, "slock oauth login retry", "attempt", attempt+1, "error", err)
			time.Sleep(retryDelay(attempt))
			continue
		}
		slog.ErrorContext(ctx, "slock oauth login failed", "attempt", attempt+1, "error", err)
		return SlockSessionResult{}, err
	}
	return SlockSessionResult{}, fmt.Errorf("%w: slock login failed after retries", ErrConflict)
}

// slockSubject builds the composite Subject string stored in user_identities.
// Ray refinement #2 (msg=5897ba9a) requires real uniqueness on
// (slock_server_id, slock_subject). Encoding as "<server_id>:<sub>" with
// Provider="slock" satisfies the existing (provider, subject) unique index.
func slockSubject(serverID, sub string) string {
	return strings.TrimSpace(serverID) + ":" + strings.TrimSpace(sub)
}

// slockLoginCandidates produces local user.login candidates namespaced by
// principal type and server, per Ray refinement #2. Final fallback always
// yields a unique-by-construction value using the Slock subject.
func slockLoginCandidates(ui slockoauth.Userinfo) []string {
	prefix := "slock-human"
	if ui.Type == "agent" {
		prefix = "slock-agent"
	}
	server := strings.TrimSpace(ui.ServerSlug)
	if server == "" {
		server = strings.TrimSpace(ui.ServerID)
	}
	server = sanitizeLoginSegment(server)
	if server == "" {
		server = "unknown"
	}

	sub := strings.ReplaceAll(strings.TrimSpace(ui.Sub), "-", "")
	subPrefix := sub
	if len(subPrefix) > 12 {
		subPrefix = subPrefix[:12]
	}

	candidates := []string{}
	seen := map[string]struct{}{}
	add := func(c string) {
		c = sanitizeLogin(c)
		if c == "" {
			return
		}
		if _, ok := seen[c]; ok {
			return
		}
		seen[c] = struct{}{}
		candidates = append(candidates, c)
	}

	if name := sanitizeLoginSegment(ui.PreferredUsername); name != "" {
		add(prefix + ":" + server + ":" + name)
	}
	add(prefix + ":" + server + ":" + subPrefix)
	add(prefix + ":" + subPrefix)
	return candidates
}

// sanitizeLoginSegment strips chars that don't survive the login regex and
// keeps the result lowercase. Empty input returns empty.
func sanitizeLoginSegment(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-_")
}

// sanitizeLogin lowercases and truncates to fit users.login size constraints,
// preserving the namespacing colons so display tooling can split on them.
func sanitizeLogin(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	const maxLen = 200
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	return s
}

// slockDisplayName chooses a display name preferring the Slock-provided Name,
// then preferred_username, then the candidate.
func slockDisplayName(ui slockoauth.Userinfo, fallback string) string {
	if n := strings.TrimSpace(ui.Name); n != "" {
		return n
	}
	if n := strings.TrimSpace(ui.PreferredUsername); n != "" {
		return n
	}
	return strings.TrimSpace(fallback)
}
