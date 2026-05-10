package service

import (
	"context"

	"gh-server/internal/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func ensureOutsideCollaboratorTx(tx *gorm.DB, orgID, userID uint) error {
	if orgID == 0 || userID == 0 {
		return ErrValidation
	}
	row := db.OutsideCollaborator{
		OrganizationID: orgID,
		UserID:         userID,
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}

func deleteOutsideCollaboratorTx(tx *gorm.DB, orgID, userID uint) error {
	if orgID == 0 || userID == 0 {
		return nil
	}
	return tx.Where("organization_id = ? AND user_id = ?", orgID, userID).
		Delete(&db.OutsideCollaborator{}).Error
}

func hasOrgDirectRepoAccessTx(tx *gorm.DB, orgID, userID uint) (bool, error) {
	if orgID == 0 || userID == 0 {
		return false, nil
	}

	var count int64
	err := tx.
		Model(&db.Collaborator{}).
		Joins("JOIN repositories ON repositories.id = collaborators.repository_id").
		Where("repositories.owner_id = ? AND collaborators.user_id = ?", orgID, userID).
		Count(&count).Error
	return count > 0, err
}

func syncOutsideCollaboratorForOrgTx(tx *gorm.DB, orgID, userID uint) error {
	if orgID == 0 || userID == 0 {
		return nil
	}

	var memberCount int64
	if err := tx.Model(&db.OrganizationMember{}).
		Where("organization_id = ? AND user_id = ?", orgID, userID).
		Count(&memberCount).Error; err != nil {
		return err
	}
	if memberCount > 0 {
		return deleteOutsideCollaboratorTx(tx, orgID, userID)
	}

	hasDirectAccess, err := hasOrgDirectRepoAccessTx(tx, orgID, userID)
	if err != nil {
		return err
	}
	if !hasDirectAccess {
		return deleteOutsideCollaboratorTx(tx, orgID, userID)
	}
	return ensureOutsideCollaboratorTx(tx, orgID, userID)
}

func repoOrganizationIDTx(tx *gorm.DB, repoID uint) (uint, error) {
	var repo db.Repository
	if err := tx.Select("id", "owner_id").First(&repo, "id = ?", repoID).Error; err != nil {
		return 0, err
	}

	var owner db.User
	if err := tx.Select("id", "type").First(&owner, "id = ?", repo.OwnerID).Error; err != nil {
		return 0, err
	}
	if owner.Type != db.TypeOrganization {
		return 0, nil
	}
	return repo.OwnerID, nil
}

func repoCollaboratorUserIDsTx(tx *gorm.DB, repoID uint) ([]uint, error) {
	var userIDs []uint
	if err := tx.Model(&db.Collaborator{}).
		Where("repository_id = ?", repoID).
		Pluck("user_id", &userIDs).Error; err != nil {
		return nil, err
	}
	return userIDs, nil
}

// ListOutsideCollaborators lists non-member users with direct repository access in an organization.
func (s *Service) ListOutsideCollaborators(ctx context.Context, orgID uint) ([]db.OutsideCollaborator, error) {
	var rows []db.OutsideCollaborator
	err := s.DBForCtx(ctx).
		Where("organization_id = ?", orgID).
		Preload("User").
		Order("user_id ASC").
		Find(&rows).Error
	return rows, wrapErr(err)
}

// IsOutsideCollaborator reports whether a user is currently an outside collaborator for an organization.
func (s *Service) IsOutsideCollaborator(ctx context.Context, orgID, userID uint) (bool, error) {
	if orgID == 0 || userID == 0 {
		return false, nil
	}

	var count int64
	err := s.DBForCtx(ctx).
		Model(&db.OutsideCollaborator{}).
		Where("organization_id = ? AND user_id = ?", orgID, userID).
		Count(&count).Error
	return count > 0, wrapErr(err)
}
