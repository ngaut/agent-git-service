// Package service — audit log writer and org-scoped reader.
package service

import (
	"context"
	"strings"
	"time"

	"gh-server/internal/db"
)

// Audit action constants. Centralized so callers can't drift from the
// vocabulary the REST transform / clients filter on.
const (
	AuditActionOrgAddMember    = "org.add_member"
	AuditActionOrgRemoveMember = "org.remove_member"
	AuditActionGitBlobCreate   = "git.blob.create"
	AuditActionGitTreeCreate   = "git.tree.create"
	AuditActionGitCommitCreate = "git.commit.create"
	AuditActionGitRefCreate    = "git.ref.create"
	AuditActionGitRefUpdate    = "git.ref.update"
	AuditActionGitRefDelete    = "git.ref.delete"
)

// AuditEvent describes a single audit entry to record.
type AuditEvent struct {
	OrganizationID     *uint
	UserID             *uint
	Action             string
	RepositoryFullName string
	TargetLogin        string
	Details            string
}

// LogAudit records an audit log entry. Actor is taken from context via
// UserFromContext; if absent, ActorLogin is left empty (system action).
// Errors are returned but callers commonly log-and-ignore so a failed
// audit write doesn't break the primary mutation.
func (s *Service) LogAudit(ctx context.Context, ev AuditEvent) error {
	if ev.Action == "" {
		return ErrValidation
	}
	entry := &db.AuditLogEntry{
		OrganizationID:     ev.OrganizationID,
		UserID:             ev.UserID,
		Action:             ev.Action,
		RepositoryFullName: ev.RepositoryFullName,
		TargetLogin:        ev.TargetLogin,
		Details:            ev.Details,
		CreatedAt:          time.Now().UTC(),
	}
	if actor, ok := UserFromContext(ctx); ok && actor.ID != 0 {
		id := actor.ID
		entry.ActorID = &id
		entry.ActorLogin = actor.Login
	}
	return s.DBForCtx(ctx).Create(entry).Error
}

// AuditLogFilters describes the GitHub-compatible filter options for
// the org audit-log read endpoint.
type AuditLogFilters struct {
	Phrase  string    // substring match on action/actor/target
	After   time.Time // inclusive lower bound on CreatedAt; zero = no bound
	Before  time.Time // inclusive upper bound on CreatedAt; zero = no bound
	Order   string    // "asc" or "desc"; default "desc"
	PerPage int       // bounded by [1, 100]; default 30
}

// ListOrgAuditLog returns audit entries scoped to a single organization,
// applying the supplied filters in order: phrase substring, time
// range, sort, then page-size cap.
func (s *Service) ListOrgAuditLog(ctx context.Context, orgID uint, f AuditLogFilters) ([]db.AuditLogEntry, error) {
	if orgID == 0 {
		return nil, ErrValidation
	}
	per := f.PerPage
	if per <= 0 {
		per = 30
	}
	if per > 100 {
		per = 100
	}

	q := s.DBForCtx(ctx).
		Where("organization_id = ?", orgID)

	if phrase := strings.TrimSpace(f.Phrase); phrase != "" {
		like := "%" + phrase + "%"
		q = q.Where(
			"action LIKE ? OR actor_login LIKE ? OR target_login LIKE ?",
			like, like, like,
		)
	}
	if !f.After.IsZero() {
		q = q.Where("created_at >= ?", f.After)
	}
	if !f.Before.IsZero() {
		q = q.Where("created_at <= ?", f.Before)
	}

	order := "created_at DESC, id DESC"
	if strings.EqualFold(f.Order, "asc") {
		order = "created_at ASC, id ASC"
	}

	var out []db.AuditLogEntry
	if err := q.Order(order).Limit(per).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}
