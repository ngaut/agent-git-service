package db

import "time"

const TeamPrivacyClosed = "closed"

// Team represents an organization team.
type Team struct {
	ID             uint   `gorm:"primaryKey;autoIncrement"`
	OrganizationID uint   `gorm:"not null;uniqueIndex:idx_org_slug"` // Composite unique index with slug
	Organization   User   `gorm:"foreignKey:OrganizationID"`
	Name           string `gorm:"size:255;not null"`
	Slug           string `gorm:"uniqueIndex:idx_org_slug;size:255;not null"`
	Description    string `gorm:"size:1024"`
	// Privacy is retained for GitHub API compatibility, but teams are modeled as
	// authorization groups only and are always persisted as closed.
	Privacy   string `gorm:"size:20;not null;default:'closed'"`
	CreatedAt time.Time
	UpdatedAt time.Time

	// Members are preloaded via many2many relation through team_members join table.
	Members []User `gorm:"many2many:team_members;foreignKey:ID;joinForeignKey:TeamID;references:ID;joinReferences:UserID"`

	// Computed fields populated by service queries for API responses.
	MembersCount int64 `gorm:"-"`
	ReposCount   int64 `gorm:"-"`
}

// TeamMember represents a user's membership in a team with a role.
type TeamMember struct {
	TeamID    uint   `gorm:"primaryKey"`
	Team      Team   `gorm:"foreignKey:TeamID"`
	UserID    uint   `gorm:"primaryKey;index:idx_team_members_user"`
	User      User   `gorm:"foreignKey:UserID"`
	Role      string `gorm:"size:20;not null;default:'member'"` // member, maintainer
	CreatedAt time.Time
}

// TeamRepository represents a repository granted to a team.
type TeamRepository struct {
	TeamID       uint       `gorm:"primaryKey"`
	Team         Team       `gorm:"foreignKey:TeamID"`
	RepositoryID uint       `gorm:"primaryKey;index:idx_team_repo_repo"`
	Repository   Repository `gorm:"foreignKey:RepositoryID"`
	Permission   string     `gorm:"size:20;not null;default:'read'"` // read, write, admin (+ compatibility aliases at the API boundary)
	CreatedAt    time.Time
}
