package db

import "time"

const (
	OrganizationRoleOwner  = "owner"
	OrganizationRoleMember = "member"

	OrganizationInvitationRoleDirectMember = "direct_member"
	OrganizationInvitationRoleAdmin        = "admin"
)

// OrganizationMember represents a user's membership in an organization.
type OrganizationMember struct {
	OrganizationID uint   `gorm:"primaryKey"`
	Organization   User   `gorm:"foreignKey:OrganizationID"`
	UserID         uint   `gorm:"primaryKey;index:idx_organization_members_user"`
	User           User   `gorm:"foreignKey:UserID"`
	Role           string `gorm:"size:20;not null;default:'member'"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// OrganizationInvitation represents a pending invitation for a user to join an organization.
type OrganizationInvitation struct {
	ID             uint       `gorm:"primaryKey;autoIncrement"`
	OrganizationID uint       `gorm:"uniqueIndex:idx_org_invitation"`
	Organization   User       `gorm:"foreignKey:OrganizationID"`
	InviteeID      uint       `gorm:"uniqueIndex:idx_org_invitation"`
	Invitee        User       `gorm:"foreignKey:InviteeID"`
	InviterID      uint       `gorm:"index"`
	Inviter        User       `gorm:"foreignKey:InviterID"`
	Role           string     `gorm:"size:20;not null;default:'direct_member'"`
	TeamIDsJSON    string     `gorm:"type:text"`
	ExpiresAt      *time.Time `gorm:"index"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// OutsideCollaborator represents a non-member user with direct repo access in an organization.
type OutsideCollaborator struct {
	OrganizationID uint `gorm:"primaryKey"`
	Organization   User `gorm:"foreignKey:OrganizationID"`
	UserID         uint `gorm:"primaryKey;index:idx_outside_collaborators_user"`
	User           User `gorm:"foreignKey:UserID"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
