package rest

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/rest/respond"
	"gh-server/internal/rest/transform"
	"gh-server/internal/service"
)

func organizationInvitationJSON(inv db.OrganizationInvitation) map[string]any {
	var organization any
	if inv.Organization.ID != 0 {
		organization = transform.User(inv.Organization)
	}
	var invitee any
	if inv.Invitee.ID != 0 {
		invitee = transform.User(inv.Invitee)
	}
	var inviter any
	if inv.Inviter.ID != 0 {
		inviter = transform.User(inv.Inviter)
	}

	teamIDs, err := service.DecodeOrganizationInvitationTeamIDs(inv.TeamIDsJSON)
	if err != nil {
		teamIDs = nil
	}

	payload := map[string]any{
		"id":           inv.ID,
		"organization": organization,
		"invitee":      invitee,
		"inviter":      inviter,
		"role":         inv.Role,
		"team_ids":     teamIDs,
		"created_at":   inv.CreatedAt.Format(time.RFC3339),
		"url":          fmt.Sprintf("%s/api/v3/user/organization_invitations/%d", transform.Base(), inv.ID),
	}
	if inv.Organization.Login != "" {
		payload["html_url"] = fmt.Sprintf("%s/orgs/%s/invitations/%d", transform.HTMLBase(), inv.Organization.Login, inv.ID)
	}
	if inv.ExpiresAt != nil {
		payload["expires_at"] = inv.ExpiresAt.Format(time.RFC3339)
	} else {
		payload["expires_at"] = nil
	}
	return payload
}

func (d *Deps) resolveOrganizationInvitee(r *http.Request, inviteeID *uint, inviteeLogin string) (db.User, error) {
	if inviteeID != nil && *inviteeID != 0 {
		return d.Svc.GetUserByID(r.Context(), strconv.FormatUint(uint64(*inviteeID), 10))
	}

	login := strings.TrimSpace(inviteeLogin)
	if login == "" {
		return db.User{}, service.ErrValidation
	}
	return d.Svc.GetUser(r.Context(), login)
}

// ListOrganizationInvitations handles GET /api/v3/orgs/{org}/invitations.
func (d *Deps) ListOrganizationInvitations(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	if !d.requireOrgAdmin(w, r, org) {
		return
	}

	invs, err := d.Svc.ListOrganizationInvitations(r.Context(), org.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	out := make([]any, 0, len(invs))
	for _, inv := range invs {
		out = append(out, organizationInvitationJSON(inv))
	}
	if out == nil {
		out = []any{}
	}
	respond.JSON(w, http.StatusOK, out)
}

// CreateOrganizationInvitation handles POST /api/v3/orgs/{org}/invitations.
func (d *Deps) CreateOrganizationInvitation(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	if !d.requireOrgAdmin(w, r, org) {
		return
	}

	inviter, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req struct {
		InviteeID     *uint  `json:"invitee_id"`
		InviteeLogin  string `json:"invitee_login"`
		Invitee       string `json:"invitee"`
		Role          string `json:"role"`
		TeamIDs       []uint `json:"team_ids"`
		ExpiresInDays int    `json:"expires_in_days"`
	}
	if err := decodeBodyStrict(r, &req); err != nil {
		respond.ValidationFailed(w, "invalid JSON")
		return
	}

	inviteeLogin := req.InviteeLogin
	if strings.TrimSpace(inviteeLogin) == "" {
		inviteeLogin = req.Invitee
	}
	invitee, err := d.resolveOrganizationInvitee(r, req.InviteeID, inviteeLogin)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	var expiresAt *time.Time
	if req.ExpiresInDays < 0 {
		respond.ValidationFailed(w, "expires_in_days must be greater than or equal to 0")
		return
	}
	if req.ExpiresInDays > 0 {
		ts := time.Now().UTC().Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)
		expiresAt = &ts
	}

	inv, err := d.Svc.CreateOrganizationInvitation(r.Context(), service.CreateOrganizationInvitationInput{
		OrganizationID: org.ID,
		InviteeID:      invitee.ID,
		InviterID:      inviter.ID,
		Role:           req.Role,
		TeamIDs:        req.TeamIDs,
		ExpiresAt:      expiresAt,
	})
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	respond.JSON(w, http.StatusCreated, organizationInvitationJSON(inv))
}

// RevokeOrganizationInvitation handles DELETE /api/v3/orgs/{org}/invitations/{invitation_id}.
func (d *Deps) RevokeOrganizationInvitation(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	if !d.requireOrgAdmin(w, r, org) {
		return
	}

	id, ok := mustIntParam(w, r, "invitation_id")
	if !ok {
		return
	}

	if err := d.Svc.RevokeOrganizationInvitation(r.Context(), org.ID, uint(id)); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListUserOrganizationInvitations handles GET /api/v3/user/organization_invitations.
func (d *Deps) ListUserOrganizationInvitations(w http.ResponseWriter, r *http.Request) {
	user, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	invs, err := d.Svc.ListUserOrganizationInvitations(r.Context(), user.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	out := make([]any, 0, len(invs))
	for _, inv := range invs {
		out = append(out, organizationInvitationJSON(inv))
	}
	if out == nil {
		out = []any{}
	}
	respond.JSON(w, http.StatusOK, out)
}

// AcceptOrganizationInvitation handles PATCH /api/v3/user/organization_invitations/{invitation_id}.
func (d *Deps) AcceptOrganizationInvitation(w http.ResponseWriter, r *http.Request) {
	user, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	id, ok := mustIntParam(w, r, "invitation_id")
	if !ok {
		return
	}

	if err := d.Svc.AcceptOrganizationInvitation(r.Context(), uint(id), user.ID); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeclineOrganizationInvitation handles DELETE /api/v3/user/organization_invitations/{invitation_id}.
func (d *Deps) DeclineOrganizationInvitation(w http.ResponseWriter, r *http.Request) {
	user, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	id, ok := mustIntParam(w, r, "invitation_id")
	if !ok {
		return
	}

	if err := d.Svc.DeclineOrganizationInvitation(r.Context(), uint(id), user.ID); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
