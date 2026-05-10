package rest

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/rest/respond"
	"gh-server/internal/rest/transform"
)

// resolveDismissedBy resolves the DismissedBy user ID to a user map if present.
func (d *Deps) resolveDismissedBy(r *http.Request, a db.DependabotAlert) map[string]any {
	if a.DismissedBy != nil {
		if u, err := d.Svc.GetUserByID(r.Context(), strconv.FormatUint(uint64(*a.DismissedBy), 10)); err == nil {
			return transform.User(u)
		}
	}
	return nil
}

func dependabotAlertJSON(a db.DependabotAlert, dismissedByUser ...map[string]any) map[string]any {
	dependency, err := a.DecodeDependency()
	if err != nil {
		slog.Warn("dependabot alert decode", "error", err, "alert_number", a.Number)
	}
	advisory, err := a.DecodeSecurityAdvisory()
	if err != nil {
		slog.Warn("dependabot alert decode", "error", err, "alert_number", a.Number)
	}
	vuln, err := a.DecodeSecurityVulnerability()
	if err != nil {
		slog.Warn("dependabot alert decode", "error", err, "alert_number", a.Number)
	}

	res := map[string]any{
		"number":                 a.Number,
		"state":                  a.State,
		"dependency":             dependency,
		"security_advisory":      advisory,
		"security_vulnerability": vuln,
		"url":                    fmt.Sprintf("%s/api/v3/repos/%s/dependabot/alerts/%d", transform.Base(), a.Repository.FullName, a.Number),
		"html_url":               fmt.Sprintf("%s/%s/security/dependabot/%d", transform.HTMLBase(), a.Repository.FullName, a.Number),
		"created_at":             a.CreatedAt.Format(time.RFC3339),
		"updated_at":             a.UpdatedAt.Format(time.RFC3339),
	}

	if a.DismissedAt != nil {
		res["dismissed_at"] = a.DismissedAt.Format(time.RFC3339)
	} else {
		res["dismissed_at"] = nil
	}
	if a.DismissedReason != "" {
		res["dismissed_reason"] = a.DismissedReason
	} else {
		res["dismissed_reason"] = nil
	}
	if len(dismissedByUser) > 0 && dismissedByUser[0] != nil {
		res["dismissed_by"] = dismissedByUser[0]
	} else {
		res["dismissed_by"] = nil
	}

	if a.FixedAt != nil {
		res["fixed_at"] = a.FixedAt.Format(time.RFC3339)
	} else {
		res["fixed_at"] = nil
	}

	return res
}

// ListDependabotAlerts handles GET /api/v3/repos/{owner}/{repo}/dependabot/alerts
func (d *Deps) ListDependabotAlerts(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}

	alerts, err := d.Svc.ListDependabotAlerts(r.Context(), repo.ID)
	if err != nil {
		respond.JSON(w, 200, []any{})
		return
	}

	var out []any
	for _, a := range alerts {
		a.Repository = *repo
		out = append(out, dependabotAlertJSON(a, d.resolveDismissedBy(r, a)))
	}
	if out == nil {
		out = []any{}
	}
	respond.JSON(w, 200, out)
}

// GetDependabotAlert handles GET /api/v3/repos/{owner}/{repo}/dependabot/alerts/{number}
func (d *Deps) GetDependabotAlert(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}

	alert, err := d.Svc.GetDependabotAlert(r.Context(), repo.ID, num)
	if err != nil {
		respond.NotFound(w)
		return
	}
	alert.Repository = *repo
	respond.JSON(w, 200, dependabotAlertJSON(*alert, d.resolveDismissedBy(r, *alert)))
}

// UpdateDependabotAlert handles PATCH /api/v3/repos/{owner}/{repo}/dependabot/alerts/{number}
func (d *Deps) UpdateDependabotAlert(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	user, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.Unauthorized(w, "Bad credentials")
		return
	}

	alert, err := d.Svc.GetDependabotAlert(r.Context(), repo.ID, num)
	if err != nil {
		respond.NotFound(w)
		return
	}
	alert.Repository = *repo

	var body struct {
		State           string `json:"state"` // "dismissed" or "open"
		DismissedReason string `json:"dismissed_reason"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid JSON")
		return
	}

	if body.State == "dismissed" {
		alert.State = "dismissed"
		now := time.Now()
		alert.DismissedAt = &now
		alert.DismissedBy = &user.ID
		alert.DismissedReason = body.DismissedReason
	} else if body.State == "open" {
		alert.State = "open"
		alert.DismissedAt = nil
		alert.DismissedBy = nil
		alert.DismissedReason = ""
	}

	if err := d.Svc.UpdateDependabotAlert(r.Context(), alert); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	// When dismissing, we already have the user; pass it directly
	var dismissedByUser map[string]any
	if alert.DismissedBy != nil {
		dismissedByUser = transform.User(user)
	}
	respond.JSON(w, 200, dependabotAlertJSON(*alert, dismissedByUser))
}
