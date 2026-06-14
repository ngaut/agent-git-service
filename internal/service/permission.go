package service

import (
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"
)

// RepoPermission represents a user's effective permission on a repository.
// The enum keeps GitHub-compatible names, while authorization decisions
// collapse triage -> read and maintain -> write.
type RepoPermission int

const (
	RepoPermissionNone RepoPermission = iota
	RepoPermissionRead
	RepoPermissionTriage
	RepoPermissionWrite
	RepoPermissionMaintain
	RepoPermissionAdmin
)

const (
	GrantPermissionValidationMessage            = "permission must be read, write, or admin (compat: pull, triage, push, maintain)"
	OrganizationBasePermissionValidationMessage = "default_repository_permission must be none, read, write, or admin (compat: pull, triage, push, maintain)"
)

// RepoWithPermission pairs a repository with the viewer's effective permission.
type RepoWithPermission struct {
	Repository db.Repository
	Permission RepoPermission
}

// AtLeast reports whether p grants at least the required permission level.
func (p RepoPermission) AtLeast(required RepoPermission) bool {
	return repoPermissionDecisionLevel(p) >= repoPermissionDecisionLevel(required)
}

func (p RepoPermission) String() string {
	switch p {
	case RepoPermissionAdmin:
		return "admin"
	case RepoPermissionMaintain, RepoPermissionWrite:
		return "write"
	case RepoPermissionTriage, RepoPermissionRead:
		return "read"
	default:
		return "none"
	}
}

func repoPermissionFromLevel(level int) RepoPermission {
	switch {
	case level >= 3:
		return RepoPermissionAdmin
	case level >= 2:
		return RepoPermissionWrite
	case level >= 1:
		return RepoPermissionRead
	default:
		return RepoPermissionNone
	}
}

// Effective collapses compatibility-only permission names into the minimal
// runtime permission set used for authorization decisions.
func (p RepoPermission) Effective() RepoPermission {
	switch p {
	case RepoPermissionTriage:
		return RepoPermissionRead
	case RepoPermissionMaintain:
		return RepoPermissionWrite
	default:
		return p
	}
}

func repoPermissionDecisionLevel(p RepoPermission) int {
	switch p.Effective() {
	case RepoPermissionAdmin:
		return 3
	case RepoPermissionWrite:
		return 2
	case RepoPermissionRead:
		return 1
	default:
		return 0
	}
}

func ParseRepoPermission(value string) RepoPermission {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "admin":
		return RepoPermissionAdmin
	case "maintain":
		return RepoPermissionMaintain
	case "write", "push":
		return RepoPermissionWrite
	case "triage":
		return RepoPermissionTriage
	case "read", "pull":
		return RepoPermissionRead
	default:
		return RepoPermissionNone
	}
}

func NormalizeGrantPermission(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "pull", "read", "triage":
		return RepoPermissionRead.String(), true
	case "push", "write", "maintain":
		return RepoPermissionWrite.String(), true
	case "admin":
		return RepoPermissionAdmin.String(), true
	default:
		return "", false
	}
}

func NormalizeOrganizationBasePermission(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "none":
		return RepoPermissionNone.String(), true
	case "pull", "read", "triage":
		return RepoPermissionRead.String(), true
	case "push", "write", "maintain":
		return RepoPermissionWrite.String(), true
	case "admin":
		return RepoPermissionAdmin.String(), true
	default:
		return "", false
	}
}
