package db

import "time"

// AuditLogEntry records an audit-relevant action against an organization or
// user. Initial scope is org-scoped membership changes; future PRs widen
// the set of hooks that write entries (repo create/delete/transfer,
// permission changes, etc.). The schema is GitHub-shaped enough that the
// transform layer can render it without a second translation step.
type AuditLogEntry struct {
	ID                 uint      `gorm:"primaryKey;autoIncrement"`
	OrganizationID     *uint     `gorm:"index:idx_audit_org_created,priority:1"`
	UserID             *uint     `gorm:"index"`
	ActorID            *uint     `gorm:"index"`
	ActorLogin         string    `gorm:"size:255;index"`
	Action             string    `gorm:"size:64;index"`
	RepositoryFullName string    `gorm:"size:512;index"`
	TargetLogin        string    `gorm:"size:255"`
	Details            string    `gorm:"type:text"`
	CreatedAt          time.Time `gorm:"index:idx_audit_org_created,priority:2;index"`
}
