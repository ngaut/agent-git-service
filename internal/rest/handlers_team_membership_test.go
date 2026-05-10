package rest_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
	"gh-server/internal/testharness"
)

func TestTeamMembershipHandlers_AddNonMemberReturnsPendingThenActiveAfterAccept(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	owner, ownerToken := seedHarnessUser(t, h, "team-membership-owner", false)
	org, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, owner), "team-membership-pending-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	team, err := h.Svc.GetTeam(ctx, org.ID, "admins")
	if err != nil {
		t.Fatalf("GetTeam admins failed: %v", err)
	}
	invitee, inviteeToken := seedHarnessUser(t, h, "team-membership-pending-target", false)

	addResp := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/memberships/"+invitee.Login, ownerToken, map[string]any{
		"role": "maintainer",
	})
	assertStatusCode(t, addResp, http.StatusOK)
	addBody := testharness.DecodeJSON(t, addResp)
	if addBody["state"] != "pending" {
		t.Fatalf("membership state = %v, want pending", addBody["state"])
	}
	if addBody["role"] != "maintainer" {
		t.Fatalf("membership role = %v, want maintainer", addBody["role"])
	}

	getPendingResp := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/memberships/"+invitee.Login, ownerToken)
	assertStatusCode(t, getPendingResp, http.StatusOK)
	getPendingBody := testharness.DecodeJSON(t, getPendingResp)
	if getPendingBody["state"] != "pending" {
		t.Fatalf("pending membership state = %v, want pending", getPendingBody["state"])
	}
	if getPendingBody["role"] != "maintainer" {
		t.Fatalf("pending membership role = %v, want maintainer", getPendingBody["role"])
	}

	userInvitesResp := h.DoRESTWithToken(t, "GET", "/api/v3/user/organization_invitations", inviteeToken)
	assertStatusCode(t, userInvitesResp, http.StatusOK)
	userInvites := testharness.DecodeJSONArray(t, userInvitesResp)
	if len(userInvites) != 1 {
		t.Fatalf("expected 1 user invitation, got %#v", userInvites)
	}

	teamIDs, ok := userInvites[0]["team_ids"].([]any)
	if !ok || len(teamIDs) != 1 || teamIDs[0] != float64(team.ID) {
		t.Fatalf("team_ids = %#v, want [%d]", userInvites[0]["team_ids"], team.ID)
	}

	invitationID := uint(userInvites[0]["id"].(float64))
	acceptResp := h.DoRESTWithToken(t, "PATCH", "/api/v3/user/organization_invitations/"+strconv.Itoa(int(invitationID)), inviteeToken)
	assertStatusCode(t, acceptResp, http.StatusNoContent)

	getActiveResp := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/memberships/"+invitee.Login, ownerToken)
	assertStatusCode(t, getActiveResp, http.StatusOK)
	getActiveBody := testharness.DecodeJSON(t, getActiveResp)
	if getActiveBody["state"] != "active" {
		t.Fatalf("membership state after accept = %v, want active", getActiveBody["state"])
	}
	if getActiveBody["role"] != "maintainer" {
		t.Fatalf("membership role after accept = %v, want maintainer", getActiveBody["role"])
	}
}

func TestTeamMembershipHandlers_NonAdminCannotInviteNonMember(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	owner, ownerToken := seedHarnessUser(t, h, "team-membership-admin-owner", false)
	org, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, owner), "team-membership-admin-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	team, err := h.Svc.GetTeam(ctx, org.ID, "admins")
	if err != nil {
		t.Fatalf("GetTeam admins failed: %v", err)
	}

	member, memberToken := seedHarnessUser(t, h, "team-membership-admin-member", false)
	if err := h.Svc.AddOrgMember(ctx, org.ID, member.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember member failed: %v", err)
	}
	invitee, inviteeToken := seedHarnessUser(t, h, "team-membership-admin-invitee", false)

	addResp := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/memberships/"+invitee.Login, memberToken, map[string]any{
		"role": "member",
	})
	assertStatusCode(t, addResp, http.StatusForbidden)
	body := testharness.DecodeJSON(t, addResp)
	if body["message"] != "Resource not accessible by integration" {
		t.Fatalf("message = %v, want Resource not accessible by integration", body["message"])
	}

	userInvitesResp := h.DoRESTWithToken(t, "GET", "/api/v3/user/organization_invitations", inviteeToken)
	assertStatusCode(t, userInvitesResp, http.StatusOK)
	if invites := testharness.DecodeJSONArray(t, userInvitesResp); len(invites) != 0 {
		t.Fatalf("expected no pending invitation for invitee, got %#v", invites)
	}

	ownerAddResp := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/memberships/"+invitee.Login, ownerToken, map[string]any{
		"role": "member",
	})
	assertStatusCode(t, ownerAddResp, http.StatusOK)
	ownerBody := testharness.DecodeJSON(t, ownerAddResp)
	if ownerBody["state"] != "pending" {
		t.Fatalf("owner add state = %v, want pending", ownerBody["state"])
	}
}

func TestTeamMembershipHandlers_MaintainerCanManageOrgMemberMembership(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	owner, ownerToken := seedHarnessUser(t, h, "team-maintainer-owner", false)
	org, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, owner), "team-maintainer-manage-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	team, err := h.Svc.GetTeam(ctx, org.ID, "admins")
	if err != nil {
		t.Fatalf("GetTeam admins failed: %v", err)
	}

	maintainer, maintainerToken := seedHarnessUser(t, h, "team-maintainer-manager", false)
	target, _ := seedHarnessUser(t, h, "team-maintainer-target", false)
	if err := h.Svc.AddOrgMember(ctx, org.ID, maintainer.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember maintainer failed: %v", err)
	}
	if err := h.Svc.AddOrgMember(ctx, org.ID, target.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember target failed: %v", err)
	}

	bootstrapResp := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/memberships/"+maintainer.Login, ownerToken, map[string]any{
		"role": "maintainer",
	})
	assertStatusCode(t, bootstrapResp, http.StatusOK)

	addResp := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/memberships/"+target.Login, maintainerToken, map[string]any{
		"role": "member",
	})
	assertStatusCode(t, addResp, http.StatusOK)
	addBody := testharness.DecodeJSON(t, addResp)
	if addBody["state"] != "active" || addBody["role"] != "member" {
		t.Fatalf("add membership = %#v, want active/member", addBody)
	}

	updateResp := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/memberships/"+target.Login, maintainerToken, map[string]any{
		"role": "maintainer",
	})
	assertStatusCode(t, updateResp, http.StatusOK)
	updateBody := testharness.DecodeJSON(t, updateResp)
	if updateBody["state"] != "active" || updateBody["role"] != "maintainer" {
		t.Fatalf("update membership = %#v, want active/maintainer", updateBody)
	}

	removeResp := h.DoRESTWithToken(t, "DELETE", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/memberships/"+target.Login, maintainerToken)
	assertStatusCode(t, removeResp, http.StatusNoContent)

	getResp := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/memberships/"+target.Login, ownerToken)
	assertStatusCode(t, getResp, http.StatusNotFound)
}

func TestTeamMembershipHandlers_MaintainerCannotInviteUnaffiliatedUser(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	owner, ownerToken := seedHarnessUser(t, h, "team-maintainer-invite-owner", false)
	org, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, owner), "team-maintainer-invite-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	team, err := h.Svc.GetTeam(ctx, org.ID, "admins")
	if err != nil {
		t.Fatalf("GetTeam admins failed: %v", err)
	}

	maintainer, maintainerToken := seedHarnessUser(t, h, "team-maintainer-invite-manager", false)
	outsider, outsiderToken := seedHarnessUser(t, h, "team-maintainer-invite-outsider", false)
	if err := h.Svc.AddOrgMember(ctx, org.ID, maintainer.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember maintainer failed: %v", err)
	}

	bootstrapResp := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/memberships/"+maintainer.Login, ownerToken, map[string]any{
		"role": "maintainer",
	})
	assertStatusCode(t, bootstrapResp, http.StatusOK)

	addResp := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/memberships/"+outsider.Login, maintainerToken, map[string]any{
		"role": "member",
	})
	assertStatusCode(t, addResp, http.StatusForbidden)
	body := testharness.DecodeJSON(t, addResp)
	if body["message"] != "Resource not accessible by integration" {
		t.Fatalf("message = %v, want Resource not accessible by integration", body["message"])
	}

	outsiderInvitesResp := h.DoRESTWithToken(t, "GET", "/api/v3/user/organization_invitations", outsiderToken)
	assertStatusCode(t, outsiderInvitesResp, http.StatusOK)
	if invites := testharness.DecodeJSONArray(t, outsiderInvitesResp); len(invites) != 0 {
		t.Fatalf("expected no invitations for outsider, got %#v", invites)
	}
}

func TestTeamMembershipHandlers_NonMaintainerCannotRemoveMember(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	owner, ownerToken := seedHarnessUser(t, h, "team-remove-owner", false)
	org, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, owner), "team-remove-member-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	team, err := h.Svc.GetTeam(ctx, org.ID, "admins")
	if err != nil {
		t.Fatalf("GetTeam admins failed: %v", err)
	}

	member, memberToken := seedHarnessUser(t, h, "team-remove-member", false)
	target, _ := seedHarnessUser(t, h, "team-remove-target", false)
	if err := h.Svc.AddOrgMember(ctx, org.ID, member.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember member failed: %v", err)
	}
	if err := h.Svc.AddOrgMember(ctx, org.ID, target.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember target failed: %v", err)
	}

	seedResp := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/memberships/"+target.Login, ownerToken, map[string]any{
		"role": "member",
	})
	assertStatusCode(t, seedResp, http.StatusOK)

	removeResp := h.DoRESTWithToken(t, "DELETE", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/memberships/"+target.Login, memberToken)
	assertStatusCode(t, removeResp, http.StatusForbidden)
	body := testharness.DecodeJSON(t, removeResp)
	if body["message"] != "Resource not accessible by integration" {
		t.Fatalf("message = %v, want Resource not accessible by integration", body["message"])
	}
}

func TestTeamMembershipHandlers_ListPendingTeamInvitations(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	owner, ownerToken := seedHarnessUser(t, h, "team-invite-list-owner", false)
	org, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, owner), "team-invite-list-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	team, err := h.Svc.GetTeam(ctx, org.ID, "admins")
	if err != nil {
		t.Fatalf("GetTeam admins failed: %v", err)
	}
	invitee, _ := seedHarnessUser(t, h, "team-invite-list-target", false)

	addResp := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/memberships/"+invitee.Login, ownerToken, map[string]any{
		"role": "member",
	})
	assertStatusCode(t, addResp, http.StatusOK)
	addBody := testharness.DecodeJSON(t, addResp)
	if addBody["state"] != "pending" {
		t.Fatalf("membership state = %v, want pending", addBody["state"])
	}

	listResp := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/invitations", ownerToken)
	assertStatusCode(t, listResp, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, listResp)
	if len(rows) != 1 {
		t.Fatalf("expected 1 pending invitation, got %#v", rows)
	}
	if rows[0]["login"] != invitee.Login {
		t.Fatalf("login = %v, want %s", rows[0]["login"], invitee.Login)
	}
	if rows[0]["role"] != db.OrganizationInvitationRoleDirectMember {
		t.Fatalf("role = %v, want %s", rows[0]["role"], db.OrganizationInvitationRoleDirectMember)
	}
	if rows[0]["team_count"] != float64(1) {
		t.Fatalf("team_count = %v, want 1", rows[0]["team_count"])
	}
}

func TestTeamMembershipHandlers_MaintainerCanListPendingTeamInvitations(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	owner, ownerToken := seedHarnessUser(t, h, "team-invite-maint-owner", false)
	org, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, owner), "team-invite-maint-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	team, err := h.Svc.GetTeam(ctx, org.ID, "admins")
	if err != nil {
		t.Fatalf("GetTeam admins failed: %v", err)
	}

	maintainer, maintainerToken := seedHarnessUser(t, h, "team-invite-maint-user", false)
	if err := h.Svc.AddOrgMember(ctx, org.ID, maintainer.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember maintainer failed: %v", err)
	}
	invitee, _ := seedHarnessUser(t, h, "team-invite-maint-target", false)

	bootstrapResp := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/memberships/"+maintainer.Login, ownerToken, map[string]any{
		"role": "maintainer",
	})
	assertStatusCode(t, bootstrapResp, http.StatusOK)

	addResp := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/memberships/"+invitee.Login, ownerToken, map[string]any{
		"role": "member",
	})
	assertStatusCode(t, addResp, http.StatusOK)

	listResp := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/invitations", maintainerToken)
	assertStatusCode(t, listResp, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, listResp)
	if len(rows) != 1 {
		t.Fatalf("expected 1 pending invitation, got %#v", rows)
	}
}

func TestTeamMembershipHandlers_NonMaintainerCannotListPendingTeamInvitations(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	owner, ownerToken := seedHarnessUser(t, h, "team-invite-deny-owner", false)
	org, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, owner), "team-invite-deny-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	team, err := h.Svc.GetTeam(ctx, org.ID, "admins")
	if err != nil {
		t.Fatalf("GetTeam admins failed: %v", err)
	}
	member, memberToken := seedHarnessUser(t, h, "team-invite-deny-member", false)
	if err := h.Svc.AddOrgMember(ctx, org.ID, member.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember member failed: %v", err)
	}
	invitee, _ := seedHarnessUser(t, h, "team-invite-deny-target", false)

	addResp := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/memberships/"+invitee.Login, ownerToken, map[string]any{
		"role": "member",
	})
	assertStatusCode(t, addResp, http.StatusOK)

	listResp := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/"+org.Login+"/teams/"+team.Slug+"/invitations", memberToken)
	assertStatusCode(t, listResp, http.StatusForbidden)
	body := testharness.DecodeJSON(t, listResp)
	if body["message"] != "Resource not accessible by integration" {
		t.Fatalf("message = %v, want Resource not accessible by integration", body["message"])
	}
}
