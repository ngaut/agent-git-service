package graphql

import (
	"context"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

// loadAlertWithWriteAccess fetches a Dependabot alert by ID and verifies the
// viewer holds at least write permission on its parent repository. On any
// failure it surfaces a "not found" response so existence isn't leaked to
// callers without write access.
func (s *Server) loadAlertWithWriteAccess(ctx context.Context, dbID, viewerID uint) (db.DependabotAlert, map[string]any, bool) {
	alert, err := s.Svc.GetDependabotAlertByID(ctx, dbID)
	if err != nil {
		return alert, errResp("vulnerability alert not found"), false
	}
	perm, err := s.Svc.HasRepoAccess(ctx, alert.RepositoryID, viewerID)
	if err != nil || !perm.AtLeast(service.RepoPermissionWrite) {
		return alert, errResp("vulnerability alert not found"), false
	}
	return alert, nil, true
}

// doDismissRepositoryVulnerabilityAlert handles the dismissRepositoryVulnerabilityAlert mutation.
func (s *Server) doDismissRepositoryVulnerabilityAlert(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	alertID := strFrom(inp, "repositoryVulnerabilityAlertId")
	reason := strFrom(inp, "dismissReason") // e.g. "FIX_STARTED", "INACCURATE", "NOT_USED", "TOLERABLE_RISK"

	dbID := parseNodeID(alertID, "RepositoryVulnerabilityAlert")
	if dbID == 0 {
		return errResp("invalid or missing repositoryVulnerabilityAlertId")
	}

	u, err := s.Svc.GetCurrentUser(ctx)
	if err != nil {
		return errResp("unauthorized")
	}

	alert, errResponse, ok := s.loadAlertWithWriteAccess(ctx, dbID, u.ID)
	if !ok {
		return errResponse
	}

	now := time.Now()
	uid := u.ID
	alert.State = "dismissed"
	alert.DismissedReason = reason
	alert.DismissedAt = &now
	alert.DismissedBy = &uid

	if err := s.Svc.UpdateDependabotAlert(ctx, &alert); err != nil {
		return errResp(err.Error())
	}

	return wrap("dismissRepositoryVulnerabilityAlert", map[string]any{
		"clientMutationId":             strFrom(inp, "clientMutationId"),
		"repositoryVulnerabilityAlert": s.dependabotAlertGQL(ctx, alert),
	})
}

// doResolveRepositoryVulnerabilityAlert handles the resolveRepositoryVulnerabilityAlert mutation.
func (s *Server) doResolveRepositoryVulnerabilityAlert(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	alertID := strFrom(inp, "repositoryVulnerabilityAlertId")

	dbID := parseNodeID(alertID, "RepositoryVulnerabilityAlert")
	if dbID == 0 {
		return errResp("invalid or missing repositoryVulnerabilityAlertId")
	}

	u, err := s.Svc.GetCurrentUser(ctx)
	if err != nil {
		return errResp("unauthorized")
	}

	alert, errResponse, ok := s.loadAlertWithWriteAccess(ctx, dbID, u.ID)
	if !ok {
		return errResponse
	}

	now := time.Now()
	alert.State = "fixed"
	alert.FixedAt = &now

	if err := s.Svc.UpdateDependabotAlert(ctx, &alert); err != nil {
		return errResp(err.Error())
	}

	return wrap("resolveRepositoryVulnerabilityAlert", map[string]any{
		"clientMutationId":             strFrom(inp, "clientMutationId"),
		"repositoryVulnerabilityAlert": s.dependabotAlertGQL(ctx, alert),
	})
}
