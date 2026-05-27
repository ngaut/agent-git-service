package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	agsauth "github.com/ngaut/agent-git-service/auth"
	"github.com/ngaut/agent-git-service/internal/db"
	"gorm.io/gorm"
)

var embeddedIdentityLoginRE = regexp.MustCompile(`^[a-zA-Z0-9](?:-?[a-zA-Z0-9]){0,38}$`)

// EmbeddedIdentity is the internal auth vocabulary for host-provided
// identities. The public package surface lives under auth.Identity.
type EmbeddedIdentity = agsauth.Identity

func normalizeEmbeddedIdentity(identity EmbeddedIdentity) EmbeddedIdentity {
	identity.Provider = strings.TrimSpace(identity.Provider)
	identity.Subject = strings.TrimSpace(identity.Subject)
	identity.Login = strings.TrimSpace(identity.Login)
	identity.Name = strings.TrimSpace(identity.Name)
	identity.Email = strings.TrimSpace(identity.Email)
	if len(identity.Groups) > 0 {
		groups := make([]string, 0, len(identity.Groups))
		for _, group := range identity.Groups {
			group = strings.TrimSpace(group)
			if group != "" {
				groups = append(groups, group)
			}
		}
		identity.Groups = groups
	}
	return identity
}

func validateEmbeddedIdentity(identity EmbeddedIdentity) error {
	switch {
	case identity.Provider == "":
		return fmt.Errorf("%w: embedded identity provider is required", ErrValidation)
	case identity.Subject == "":
		return fmt.Errorf("%w: embedded identity subject is required", ErrValidation)
	case identity.Login == "":
		return fmt.Errorf("%w: embedded identity login is required", ErrValidation)
	case !embeddedIdentityLoginRE.MatchString(identity.Login):
		return fmt.Errorf("%w: embedded identity login must match %s", ErrValidation, embeddedIdentityLoginRE.String())
	default:
		return nil
	}
}

func canBindEmbeddedIdentityToUser(user db.User) bool {
	if user.Type != db.TypeUser {
		return false
	}
	return user.UserKind == "" || user.UserKind == db.UserKindHuman
}

// ResolveEmbeddedIdentity maps a trusted external identity to an AGS internal
// user, creating the user and identity link on first use.
func (s *Service) ResolveEmbeddedIdentity(ctx context.Context, identity EmbeddedIdentity) (db.User, error) {
	identity = normalizeEmbeddedIdentity(identity)
	if err := validateEmbeddedIdentity(identity); err != nil {
		return db.User{}, err
	}

	const maxAttempts = 4

attemptLoop:
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var out db.User
		err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
			var ident db.UserIdentity
			identErr := tx.Preload("User").First(&ident, "provider = ? AND subject = ?", identity.Provider, identity.Subject).Error
			switch {
			case identErr == nil:
				out = ident.User
			case errors.Is(identErr, gorm.ErrRecordNotFound):
				existing := db.User{}
				switch userErr := tx.First(&existing, "login = ?", identity.Login).Error; {
				case userErr == nil:
					if canBindEmbeddedIdentityToUser(existing) {
						return fmt.Errorf("%w: embedded identity login %q is already bound to an existing AGS user", ErrConflict, identity.Login)
					}
					return fmt.Errorf("%w: embedded identity login %q is already claimed by a non-human account", ErrConflict, identity.Login)
				case errors.Is(userErr, gorm.ErrRecordNotFound):
					candidate := db.User{
						Login:       identity.Login,
						Name:        identity.Name,
						Email:       identity.Email,
						Type:        db.TypeUser,
						UserKind:    db.UserKindHuman,
						SiteAdmin:   identity.SiteAdmin,
						IsAnonymous: false,
					}
					if err := tx.Create(&candidate).Error; err != nil {
						if isDuplicateErr(err) {
							return ErrConflict
						}
						return err
					}
					out = candidate
				default:
					return userErr
				}
				if err := tx.Create(&db.UserIdentity{
					UserID:   out.ID,
					Provider: identity.Provider,
					Subject:  identity.Subject,
				}).Error; err != nil {
					if isDuplicateErr(err) {
						return ErrConflict
					}
					return err
				}
			default:
				return identErr
			}

			updates := map[string]any{}
			if identity.Name != "" && identity.Name != out.Name {
				updates["name"] = identity.Name
			}
			if identity.Email != "" && identity.Email != out.Email {
				updates["email"] = identity.Email
			}
			if out.UserKind == "" {
				updates["user_kind"] = db.UserKindHuman
			}
			if out.SiteAdmin != identity.SiteAdmin {
				updates["site_admin"] = identity.SiteAdmin
			}
			if len(updates) > 0 {
				if err := tx.Model(&db.User{}).Where("id = ?", out.ID).Updates(updates).Error; err != nil {
					return err
				}
				if err := tx.First(&out, out.ID).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err == nil {
			return out, nil
		}
		if errors.Is(err, ErrConflict) || isSQLiteLockErr(err) {
			time.Sleep(retryDelay(attempt))
			continue attemptLoop
		}
		return db.User{}, wrapErr(err)
	}

	return db.User{}, fmt.Errorf("%w: embedded identity resolution failed after retries", ErrConflict)
}
