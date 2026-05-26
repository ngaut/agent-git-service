package transform

import (
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
)

// AuditLogEntry shapes a db.AuditLogEntry as a GitHub-compatible audit log JSON object.
// The "@timestamp" key matches GitHub's audit log API; "created_at" is included as a
// convenience for clients that prefer the standard timestamp key.
func AuditLogEntry(e db.AuditLogEntry, orgLogin string) map[string]any {
	out := map[string]any{
		"@timestamp": e.CreatedAt.UnixMilli(),
		"created_at": e.CreatedAt.Format(time.RFC3339),
		"action":     e.Action,
		"actor":      e.ActorLogin,
		"actor_id":   uintOrNil(e.ActorID),
		"org":        orgLogin,
		"org_id":     uintOrNil(e.OrganizationID),
	}
	if e.TargetLogin != "" {
		out["user"] = e.TargetLogin
		out["user_id"] = uintOrNil(e.UserID)
	}
	if e.RepositoryFullName != "" {
		out["repo"] = e.RepositoryFullName
	}
	if e.Details != "" {
		out["details"] = e.Details
	}
	return out
}

func uintOrNil(p *uint) any {
	if p == nil {
		return nil
	}
	return *p
}
