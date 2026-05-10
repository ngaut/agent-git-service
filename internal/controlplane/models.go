// Package controlplane provides the shared control plane schema and DB routing
// for multi-agent support. The control plane is a separate database that stores
// agent users, tokens, and tenant DSN mappings.
package controlplane

import (
	"fmt"
	"time"
)

// AgentState represents the lifecycle state of a control-plane agent.
type AgentState string

const (
	AgentStatePending AgentState = "pending"
	AgentStateActive  AgentState = "active"
	AgentStateFailed  AgentState = "failed"
)

func (s AgentState) valid() bool {
	switch s {
	case AgentStatePending, AgentStateActive, AgentStateFailed:
		return true
	default:
		return false
	}
}

func validTransition(from, to AgentState) bool {
	switch from {
	case AgentStatePending:
		return to == AgentStateActive || to == AgentStateFailed
	case AgentStateFailed:
		return to == AgentStatePending || to == AgentStateActive
	case AgentStateActive:
		return to == AgentStateFailed
	default:
		return false
	}
}

// CPUser is the global registry entry for an agent in the control plane DB.
type CPUser struct {
	ID            uint       `gorm:"primaryKey"`
	Login         string     `gorm:"uniqueIndex;size:255;not null"`
	Email         string     `gorm:"size:255"`
	DSN           string     `gorm:"size:2048;null"` // encrypted tenant database connection string
	State         AgentState `gorm:"size:32;not null;default:pending"`
	FailureReason *string    `gorm:"size:1024"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CreateUser returns a control-plane user initialized in pending state.
func CreateUser(login, email, dsn string) *CPUser {
	return &CPUser{
		Login: login,
		Email: email,
		DSN:   dsn,
		State: AgentStatePending,
	}
}

// TransitionTo validates a state transition and applies it.
func (u *CPUser) TransitionTo(state AgentState, reason *string) error {
	if u == nil {
		return fmt.Errorf("controlplane: nil CPUser")
	}
	if !u.State.valid() {
		return fmt.Errorf("controlplane: invalid current state %q", u.State)
	}
	if !state.valid() {
		return fmt.Errorf("controlplane: invalid target state %q", state)
	}
	if !validTransition(u.State, state) {
		return fmt.Errorf("controlplane: invalid transition %s -> %s", u.State, state)
	}
	u.State = state
	if state == AgentStateFailed {
		u.FailureReason = reason
	} else {
		u.FailureReason = nil
	}
	return nil
}

// ActivateUser transitions the agent to active and clears any failure reason.
func (u *CPUser) ActivateUser() error {
	return u.TransitionTo(AgentStateActive, nil)
}

// FailUser transitions the agent to failed and stores the failure reason.
func (u *CPUser) FailUser(reason string) error {
	r := reason
	return u.TransitionTo(AgentStateFailed, &r)
}

// RetryUser transitions the agent to pending and clears any failure reason.
func (u *CPUser) RetryUser() error {
	return u.TransitionTo(AgentStatePending, nil)
}

// CPToken maps an auth token to a control plane user.
type CPToken struct {
	ID        uint   `gorm:"primaryKey"`
	Value     string `gorm:"uniqueIndex;size:255;not null"`
	CPUserID  uint   `gorm:"not null;index"`
	CPUser    CPUser
	CreatedAt time.Time
}
