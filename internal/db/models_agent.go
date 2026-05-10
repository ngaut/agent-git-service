package db

import "time"

// AgentBinding links an agent account to a human account for recovery/admin.
// Each agent can be bound to at most one human.
type AgentBinding struct {
	ID          uint `gorm:"primaryKey;autoIncrement"`
	HumanUserID uint `gorm:"index;not null"`
	HumanUser   User `gorm:"foreignKey:HumanUserID"`
	AgentUserID uint `gorm:"uniqueIndex;not null"`
	AgentUser   User `gorm:"foreignKey:AgentUserID"`
	CreatedAt   time.Time
}

// AgentInvite represents a human-generated invite token used by an agent
// to confirm binding. Tokens are single-use.
type AgentInvite struct {
	ID                    uint   `gorm:"primaryKey;autoIncrement"`
	Token                 string `gorm:"uniqueIndex;size:64;not null"`
	HumanUserID           uint   `gorm:"index;not null"`
	HumanUser             User   `gorm:"foreignKey:HumanUserID"`
	CreatedAt             time.Time
	ExpiresAt             *time.Time `gorm:"index"`
	ConsumedAt            *time.Time `gorm:"index"`
	ConsumedByAgentUserID *uint      `gorm:"index"`
}
