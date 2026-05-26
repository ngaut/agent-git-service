package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateInvitation creates a new repository invitation.
func (s *Service) CreateInvitation(ctx context.Context, inv *db.RepositoryInvitation) error {
	permission, ok := NormalizeGrantPermission(inv.Permissions)
	if !ok {
		return fmt.Errorf("%w: %s", ErrValidation, GrantPermissionValidationMessage)
	}
	inv.Permissions = permission

	// Upsert to handle multiple invites
	var existing db.RepositoryInvitation
	err := s.DBForCtx(ctx).
		Where("repository_id = ? AND invitee_id = ?", inv.RepositoryID, inv.InviteeID).
		First(&existing).Error
	switch {
	case err == nil:
		inv.ID = existing.ID
		inv.CreatedAt = existing.CreatedAt
		return s.DBForCtx(ctx).
			Model(&db.RepositoryInvitation{}).
			Where("id = ?", existing.ID).
			Updates(map[string]any{
				"inviter_id":  inv.InviterID,
				"permissions": inv.Permissions,
			}).Error
	case errors.Is(err, gorm.ErrRecordNotFound):
		return s.DBForCtx(ctx).Create(inv).Error
	default:
		return err
	}
}

// ListRepoInvitations lists all pending invitations for a repository.
func (s *Service) ListRepoInvitations(ctx context.Context, repoID uint) ([]db.RepositoryInvitation, error) {
	var invs []db.RepositoryInvitation
	err := s.DBForCtx(ctx).
		Where("repository_id = ?", repoID).
		Preload("Invitee").Preload("Inviter").Preload("Repository").Preload("Repository.Owner").
		Find(&invs).Error
	return invs, err
}

// ListUserInvitations lists all pending repository invitations for the current user.
func (s *Service) ListUserInvitations(ctx context.Context, userID uint) ([]db.RepositoryInvitation, error) {
	var invs []db.RepositoryInvitation
	err := s.DBForCtx(ctx).
		Where("invitee_id = ?", userID).
		Preload("Invitee").Preload("Inviter").Preload("Repository").Preload("Repository.Owner").
		Find(&invs).Error
	return invs, err
}

// GetInvitation fetches a specific invitation.
func (s *Service) GetInvitation(ctx context.Context, inviteID uint) (*db.RepositoryInvitation, error) {
	var inv db.RepositoryInvitation
	err := s.DBForCtx(ctx).
		Where("id = ?", inviteID).
		Preload("Invitee").Preload("Inviter").Preload("Repository").Preload("Repository.Owner").
		First(&inv).Error
	if err != nil {
		return nil, wrapErr(err)
	}
	return &inv, nil
}

// AcceptInvitation adds the user as a collaborator and deletes the invitation.
func (s *Service) AcceptInvitation(ctx context.Context, inviteID, userID uint) error {
	inv, err := s.GetInvitation(ctx, inviteID)
	if err != nil {
		return err
	}
	if inv.InviteeID != userID {
		return ErrUnauthorized
	}
	err = s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		collab := db.Collaborator{
			RepositoryID: inv.RepositoryID,
			UserID:       userID,
			Permission:   inv.Permissions,
		}
		if err := upsertCollaboratorTx(tx, &collab); err != nil {
			return err
		}
		orgID, err := repoOrganizationIDTx(tx, inv.RepositoryID)
		if err != nil {
			return err
		}
		if orgID != 0 {
			if err := syncOutsideCollaboratorForOrgTx(tx, orgID, userID); err != nil {
				return err
			}
		}
		return tx.Delete(&db.RepositoryInvitation{}, inviteID).Error
	})
	return wrapErr(err)
}

// DeclineInvitation deletes the specific invitation.
func (s *Service) DeclineInvitation(ctx context.Context, inviteID, userID uint) error {
	inv, err := s.GetInvitation(ctx, inviteID)
	if err != nil {
		return err
	}
	if inv.InviteeID != userID {
		return ErrUnauthorized
	}

	return s.DBForCtx(ctx).Delete(&db.RepositoryInvitation{}, inviteID).Error
}

// AddCollaborator adds a user as a collaborator to a repository.
func (s *Service) AddCollaborator(ctx context.Context, repoID, userID uint, permission string) error {
	permission, ok := NormalizeGrantPermission(permission)
	if !ok {
		return fmt.Errorf("%w: %s", ErrValidation, GrantPermissionValidationMessage)
	}
	err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		collab := db.Collaborator{
			RepositoryID: repoID,
			UserID:       userID,
			Permission:   permission,
		}
		if err := upsertCollaboratorTx(tx, &collab); err != nil {
			return err
		}
		orgID, err := repoOrganizationIDTx(tx, repoID)
		if err != nil {
			return err
		}
		if orgID != 0 {
			if err := syncOutsideCollaboratorForOrgTx(tx, orgID, userID); err != nil {
				return err
			}
		}
		return tx.Where("repository_id = ? AND invitee_id = ?", repoID, userID).
			Delete(&db.RepositoryInvitation{}).Error
	})
	return wrapErr(err)
}

func upsertCollaboratorTx(tx *gorm.DB, collab *db.Collaborator) error {
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "repository_id"}, {Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"permission"}),
	}).Create(collab).Error
}

// RemoveCollaborator removes a user from a repository's collaborators.
func (s *Service) RemoveCollaborator(ctx context.Context, repoID, userID uint) error {
	err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		orgID, err := repoOrganizationIDTx(tx, repoID)
		if err != nil {
			return err
		}
		if err := tx.Where("repository_id = ? AND user_id = ?", repoID, userID).
			Delete(&db.Collaborator{}).Error; err != nil {
			return err
		}
		if err := tx.Where("repository_id = ? AND invitee_id = ?", repoID, userID).
			Delete(&db.RepositoryInvitation{}).Error; err != nil {
			return err
		}
		if orgID != 0 {
			if err := syncOutsideCollaboratorForOrgTx(tx, orgID, userID); err != nil {
				return err
			}
		}
		return nil
	})
	return wrapErr(err)
}

// ListCollaborators lists all collaborators for a repository.
func (s *Service) ListCollaborators(ctx context.Context, repoID uint) ([]db.Collaborator, error) {
	var collabs []db.Collaborator
	err := s.DBForCtx(ctx).
		Where("repository_id = ?", repoID).
		Preload("User").
		Find(&collabs).Error
	return collabs, err
}

// ListCollaboratorUserIDs lists only collaborator user IDs for lightweight
// membership checks that do not need full user objects.
func (s *Service) ListCollaboratorUserIDs(ctx context.Context, repoID uint) ([]uint, error) {
	var ids []uint
	err := s.DBForCtx(ctx).Model(&db.Collaborator{}).
		Where("repository_id = ?", repoID).
		Pluck("user_id", &ids).Error
	return ids, err
}

// IsCollaborator checks if a user is a collaborator on a repository.
func (s *Service) IsCollaborator(ctx context.Context, repoID, userID uint) (bool, error) {
	var count int64
	err := s.DBForCtx(ctx).Model(&db.Collaborator{}).
		Where("repository_id = ? AND user_id = ?", repoID, userID).
		Count(&count).Error
	return count > 0, err
}
