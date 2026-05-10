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

func TestOrganizationInvitationHandlers_FullFlow(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	orgOwner, ownerToken := seedHarnessUser(t, h, "org-owner", false)
	orgCtx := service.ContextWithUser(ctx, orgOwner)
	org, err := h.Svc.EnsureOrg(orgCtx, "org-invite-flow")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	team, err := h.Svc.CreateTeam(ctx, org.ID, "Platform", "platform", "", "")
	if err != nil {
		t.Fatalf("CreateTeam failed: %v", err)
	}
	invitee, inviteeToken := seedHarnessUser(t, h, "org-target", false)

	createResp := h.DoRESTJSONWithToken(t, "POST", "/api/v3/orgs/org-invite-flow/invitations", ownerToken, map[string]any{
		"invitee_login":   invitee.Login,
		"role":            db.OrganizationInvitationRoleDirectMember,
		"team_ids":        []uint{team.ID},
		"expires_in_days": 3,
	})
	assertStatusCode(t, createResp, http.StatusCreated)
	created := testharness.DecodeJSON(t, createResp)
	if created["role"] != db.OrganizationInvitationRoleDirectMember {
		t.Fatalf("role = %v, want %s", created["role"], db.OrganizationInvitationRoleDirectMember)
	}
	teamIDs, ok := created["team_ids"].([]any)
	if !ok || len(teamIDs) != 1 || teamIDs[0] != float64(team.ID) {
		t.Fatalf("team_ids = %#v, want [%d]", created["team_ids"], team.ID)
	}

	listResp := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/org-invite-flow/invitations", ownerToken)
	assertStatusCode(t, listResp, http.StatusOK)
	adminInvites := testharness.DecodeJSONArray(t, listResp)
	if len(adminInvites) != 1 {
		t.Fatalf("expected 1 org invitation, got %d", len(adminInvites))
	}

	userListResp := h.DoRESTWithToken(t, "GET", "/api/v3/user/organization_invitations", inviteeToken)
	assertStatusCode(t, userListResp, http.StatusOK)
	userInvites := testharness.DecodeJSONArray(t, userListResp)
	if len(userInvites) != 1 {
		t.Fatalf("expected 1 user org invitation, got %d", len(userInvites))
	}

	inviteID := uint(created["id"].(float64))
	acceptResp := h.DoRESTWithToken(t, "PATCH", "/api/v3/user/organization_invitations/"+strconv.Itoa(int(inviteID)), inviteeToken)
	assertStatusCode(t, acceptResp, http.StatusNoContent)

	isMember, err := h.Svc.IsOrgMember(ctx, org.ID, invitee.ID)
	if err != nil {
		t.Fatalf("IsOrgMember failed: %v", err)
	}
	if !isMember {
		t.Fatal("invitee should be org member after accepting invitation")
	}

	member, err := h.Svc.GetTeamMember(ctx, team.ID, invitee.ID)
	if err != nil {
		t.Fatalf("GetTeamMember failed: %v", err)
	}
	if member.Role != "member" {
		t.Fatalf("team member role = %q, want member", member.Role)
	}

	userListResp = h.DoRESTWithToken(t, "GET", "/api/v3/user/organization_invitations", inviteeToken)
	assertStatusCode(t, userListResp, http.StatusOK)
	if invites := testharness.DecodeJSONArray(t, userListResp); len(invites) != 0 {
		t.Fatalf("expected pending org invitations to be empty after accept, got %#v", invites)
	}

	memberListResp := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/org-invite-flow/members", ownerToken)
	assertStatusCode(t, memberListResp, http.StatusOK)
	memberRows := testharness.DecodeJSONArray(t, memberListResp)
	if len(memberRows) != 2 {
		t.Fatalf("expected owner and accepted invitee in org member list, got %#v", memberRows)
	}
	var foundInvitee bool
	for _, row := range memberRows {
		if row["login"] == invitee.Login {
			foundInvitee = true
		}
	}
	if !foundInvitee {
		t.Fatalf("expected invitee %q in org member list, got %#v", invitee.Login, memberRows)
	}

	membershipResp := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/org-invite-flow/memberships/"+invitee.Login, ownerToken)
	assertStatusCode(t, membershipResp, http.StatusOK)
	membership := testharness.DecodeJSON(t, membershipResp)
	if membership["state"] != "active" {
		t.Fatalf("membership state = %v, want active", membership["state"])
	}
	if membership["role"] != "member" {
		t.Fatalf("membership role = %v, want member", membership["role"])
	}
}

func TestOrganizationInvitationHandlers_PendingAdminInvitationShowsAdminMembershipRole(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	orgOwner, ownerToken := seedHarnessUser(t, h, "org-pending-admin-owner", false)
	org, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, orgOwner), "org-pending-admin-role")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	invitee, _ := seedHarnessUser(t, h, "org-pending-admin-target", false)

	createResp := h.DoRESTJSONWithToken(t, "POST", "/api/v3/orgs/"+org.Login+"/invitations", ownerToken, map[string]any{
		"invitee_login": invitee.Login,
		"role":          db.OrganizationInvitationRoleAdmin,
	})
	assertStatusCode(t, createResp, http.StatusCreated)

	membershipResp := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/"+org.Login+"/memberships/"+invitee.Login, ownerToken)
	assertStatusCode(t, membershipResp, http.StatusOK)
	membership := testharness.DecodeJSON(t, membershipResp)
	if membership["state"] != "pending" {
		t.Fatalf("membership state = %v, want pending", membership["state"])
	}
	if membership["role"] != "admin" {
		t.Fatalf("membership role = %v, want admin", membership["role"])
	}
}

func TestOrganizationInvitationHandlers_RejectsLegacyRoleNames(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	orgOwner, ownerToken := seedHarnessUser(t, h, "org-role-owner", false)
	org, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, orgOwner), "org-role-validation")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	invitee, _ := seedHarnessUser(t, h, "org-role-target", false)

	resp := h.DoRESTJSONWithToken(t, "POST", "/api/v3/orgs/"+org.Login+"/invitations", ownerToken, map[string]any{
		"invitee_login": invitee.Login,
		"role":          "member",
	})
	assertStatusCode(t, resp, http.StatusUnprocessableEntity)
	body := testharness.DecodeJSON(t, resp)
	if body["message"] != "validation failed: role must be direct_member or admin" {
		t.Fatalf("message = %v, want validation role error", body["message"])
	}
}

func TestOrganizationMemberHandlers_ListRequiresOrgAdmin(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	orgOwner, ownerToken := seedHarnessUser(t, h, "org-member-list-owner", false)
	org, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, orgOwner), "org-member-list")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	member, memberToken := seedHarnessUser(t, h, "org-member-list-member", false)
	if err := h.Svc.AddOrgMember(ctx, org.ID, member.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember failed: %v", err)
	}

	memberResp := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/org-member-list/members", memberToken)
	assertStatusCode(t, memberResp, http.StatusOK)
	memberRows := testharness.DecodeJSONArray(t, memberResp)
	if len(memberRows) != 2 {
		t.Fatalf("expected org member to see 2 members, got %#v", memberRows)
	}

	ownerResp := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/org-member-list/members", ownerToken)
	assertStatusCode(t, ownerResp, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, ownerResp)
	if len(rows) != 2 {
		t.Fatalf("expected 2 org members, got %#v", rows)
	}

	adminOnlyResp := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/org-member-list/members?role=admin", ownerToken)
	assertStatusCode(t, adminOnlyResp, http.StatusOK)
	adminRows := testharness.DecodeJSONArray(t, adminOnlyResp)
	if len(adminRows) != 1 || adminRows[0]["login"] != orgOwner.Login {
		t.Fatalf("expected only org owner in admin role filter, got %#v", adminRows)
	}

	outsider, outsiderToken := seedHarnessUser(t, h, "org-member-list-outsider", false)
	_ = outsider
	outsiderResp := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/org-member-list/members", outsiderToken)
	assertStatusCode(t, outsiderResp, http.StatusOK)
	if rows := testharness.DecodeJSONArray(t, outsiderResp); len(rows) != 0 {
		t.Fatalf("expected outsider to see no public members, got %#v", rows)
	}

	blockedMembership := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/org-member-list/memberships/"+member.Login, outsiderToken)
	assertStatusCode(t, blockedMembership, http.StatusForbidden)
}

func TestOrganizationInvitationHandlers_RevokeRequiresOrgAdmin(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	orgOwner, ownerToken := seedHarnessUser(t, h, "org-admin", false)
	org, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, orgOwner), "org-invite-admin")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	invitee, inviteeToken := seedHarnessUser(t, h, "org-revoke-target", false)
	_, outsiderToken := seedHarnessUser(t, h, "org-outsider", false)

	blockedResp := h.DoRESTJSONWithToken(t, "POST", "/api/v3/orgs/org-invite-admin/invitations", outsiderToken, map[string]any{
		"invitee_login": invitee.Login,
	})
	assertStatusCode(t, blockedResp, http.StatusForbidden)

	createResp := h.DoRESTJSONWithToken(t, "POST", "/api/v3/orgs/org-invite-admin/invitations", ownerToken, map[string]any{
		"invitee_login": invitee.Login,
		"role":          db.OrganizationInvitationRoleDirectMember,
	})
	assertStatusCode(t, createResp, http.StatusCreated)
	created := testharness.DecodeJSON(t, createResp)
	inviteID := uint(created["id"].(float64))

	blockedDelete := h.DoRESTWithToken(t, "DELETE", "/api/v3/orgs/org-invite-admin/invitations/"+strconv.Itoa(int(inviteID)), outsiderToken)
	assertStatusCode(t, blockedDelete, http.StatusForbidden)

	deleteResp := h.DoRESTWithToken(t, "DELETE", "/api/v3/orgs/org-invite-admin/invitations/"+strconv.Itoa(int(inviteID)), ownerToken)
	assertStatusCode(t, deleteResp, http.StatusNoContent)

	userListResp := h.DoRESTWithToken(t, "GET", "/api/v3/user/organization_invitations", inviteeToken)
	assertStatusCode(t, userListResp, http.StatusOK)
	if invites := testharness.DecodeJSONArray(t, userListResp); len(invites) != 0 {
		t.Fatalf("expected no pending invites after revoke, got %#v", invites)
	}

	isMember, err := h.Svc.IsOrgMember(ctx, org.ID, invitee.ID)
	if err != nil {
		t.Fatalf("IsOrgMember failed: %v", err)
	}
	if isMember {
		t.Fatal("invitee should not join org after invitation is revoked")
	}
}

func TestOrganizationInvitationHandlers_DeclineRequiresInvitee(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	orgOwner, ownerToken := seedHarnessUser(t, h, "org-decline-owner-authz", false)
	org, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, orgOwner), "org-invite-decline-authz")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	invitee, inviteeToken := seedHarnessUser(t, h, "org-decline-target-authz", false)
	_, otherToken := seedHarnessUser(t, h, "org-decline-other-authz", false)

	createResp := h.DoRESTJSONWithToken(t, "POST", "/api/v3/orgs/org-invite-decline-authz/invitations", ownerToken, map[string]any{
		"invitee_login": invitee.Login,
		"role":          db.OrganizationInvitationRoleDirectMember,
	})
	assertStatusCode(t, createResp, http.StatusCreated)
	created := testharness.DecodeJSON(t, createResp)
	inviteID := uint(created["id"].(float64))

	blockedResp := h.DoRESTWithToken(t, "DELETE", "/api/v3/user/organization_invitations/"+strconv.Itoa(int(inviteID)), otherToken)
	assertStatusCode(t, blockedResp, http.StatusUnauthorized)
	body := testharness.DecodeJSON(t, blockedResp)
	if body["message"] != "Bad credentials" {
		t.Fatalf("expected unauthorized decline message, got %v", body["message"])
	}

	userListResp := h.DoRESTWithToken(t, "GET", "/api/v3/user/organization_invitations", inviteeToken)
	assertStatusCode(t, userListResp, http.StatusOK)
	if invites := testharness.DecodeJSONArray(t, userListResp); len(invites) != 1 {
		t.Fatalf("expected invite to remain pending for invitee, got %#v", invites)
	}

	adminListResp := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/org-invite-decline-authz/invitations", ownerToken)
	assertStatusCode(t, adminListResp, http.StatusOK)
	if invites := testharness.DecodeJSONArray(t, adminListResp); len(invites) != 1 {
		t.Fatalf("expected org invite list to retain pending invite, got %#v", invites)
	}

	isMember, err := h.Svc.IsOrgMember(ctx, org.ID, invitee.ID)
	if err != nil {
		t.Fatalf("IsOrgMember failed: %v", err)
	}
	if isMember {
		t.Fatal("invitee should not join org when another user attempts to decline the invitation")
	}
}

func TestOrganizationInvitationHandlers_DeclineFlow_RemovesPendingInviteFromUserList(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	orgOwner, ownerToken := seedHarnessUser(t, h, "org-decline-owner", false)
	org, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, orgOwner), "org-invite-decline")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	invitee, inviteeToken := seedHarnessUser(t, h, "org-decline-target", false)

	createResp := h.DoRESTJSONWithToken(t, "POST", "/api/v3/orgs/org-invite-decline/invitations", ownerToken, map[string]any{
		"invitee_login": invitee.Login,
		"role":          db.OrganizationInvitationRoleDirectMember,
	})
	assertStatusCode(t, createResp, http.StatusCreated)
	created := testharness.DecodeJSON(t, createResp)
	inviteID := uint(created["id"].(float64))

	declineResp := h.DoRESTWithToken(t, "DELETE", "/api/v3/user/organization_invitations/"+strconv.Itoa(int(inviteID)), inviteeToken)
	assertStatusCode(t, declineResp, http.StatusNoContent)

	userListResp := h.DoRESTWithToken(t, "GET", "/api/v3/user/organization_invitations", inviteeToken)
	assertStatusCode(t, userListResp, http.StatusOK)
	if invites := testharness.DecodeJSONArray(t, userListResp); len(invites) != 0 {
		t.Fatalf("expected pending org invitations to be empty after decline, got %#v", invites)
	}

	adminListResp := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/org-invite-decline/invitations", ownerToken)
	assertStatusCode(t, adminListResp, http.StatusOK)
	if invites := testharness.DecodeJSONArray(t, adminListResp); len(invites) != 0 {
		t.Fatalf("expected org invitation list to be empty after decline, got %#v", invites)
	}

	isMember, err := h.Svc.IsOrgMember(ctx, org.ID, invitee.ID)
	if err != nil {
		t.Fatalf("IsOrgMember failed: %v", err)
	}
	if isMember {
		t.Fatal("invitee should not join org after declining invitation")
	}
}
