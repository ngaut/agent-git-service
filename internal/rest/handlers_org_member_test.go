package rest_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

func TestOrganizationMemberHandlers_DeleteOrgMemberRemovesMemberships(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	orgOwner, ownerToken := seedHarnessUser(t, h, "org-delete-owner", false)
	org, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, orgOwner), "org-delete-member")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	member, _ := seedHarnessUser(t, h, "org-delete-target", false)
	repo, err := h.Svc.CreateRepo(service.ContextWithUser(ctx, orgOwner), service.CreateRepoInput{
		OwnerLogin: org.Login,
		Name:       "org-delete-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("CreateRepo failed: %v", err)
	}
	team, err := h.Svc.CreateTeam(ctx, org.ID, "platform", "platform", "", "")
	if err != nil {
		t.Fatalf("CreateTeam failed: %v", err)
	}
	if err := h.Svc.AddOrgMember(ctx, org.ID, member.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember failed: %v", err)
	}
	if err := h.Svc.AddTeamMember(ctx, team.ID, member.ID, "member"); err != nil {
		t.Fatalf("AddTeamMember failed: %v", err)
	}
	if err := h.Svc.AddCollaborator(ctx, repo.ID, member.ID, "read"); err != nil {
		t.Fatalf("AddCollaborator failed: %v", err)
	}

	deleteResp := h.DoRESTWithToken(t, "DELETE", "/api/v3/orgs/org-delete-member/members/"+member.Login, ownerToken)
	assertStatusCode(t, deleteResp, http.StatusNoContent)

	memberListResp := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/org-delete-member/members", ownerToken)
	assertStatusCode(t, memberListResp, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, memberListResp)
	if len(rows) != 1 || rows[0]["login"] != orgOwner.Login {
		t.Fatalf("unexpected member list after delete: %#v", rows)
	}

	if _, err := h.Svc.GetTeamMember(ctx, team.ID, member.ID); err == nil {
		t.Fatal("expected team membership to be removed with org membership")
	}
	isOutside, err := h.Svc.IsOutsideCollaborator(ctx, org.ID, member.ID)
	if err != nil {
		t.Fatalf("IsOutsideCollaborator failed: %v", err)
	}
	if !isOutside {
		t.Fatal("expected removed org member with direct repo access to become outside collaborator")
	}
}

func TestOrganizationMemberHandlers_DeleteOrgMembershipRevokesPendingInvitation(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	orgOwner, ownerToken := seedHarnessUser(t, h, "org-delete-membership-owner", false)
	if _, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, orgOwner), "org-delete-membership"); err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	invitee, inviteeToken := seedHarnessUser(t, h, "org-delete-membership-target", false)

	createResp := h.DoRESTJSONWithToken(t, "POST", "/api/v3/orgs/org-delete-membership/invitations", ownerToken, map[string]any{
		"invitee_login": invitee.Login,
		"role":          db.OrganizationInvitationRoleDirectMember,
	})
	assertStatusCode(t, createResp, http.StatusCreated)

	deleteResp := h.DoRESTWithToken(t, "DELETE", "/api/v3/orgs/org-delete-membership/memberships/"+invitee.Login, ownerToken)
	assertStatusCode(t, deleteResp, http.StatusNoContent)

	userInvites := h.DoRESTWithToken(t, "GET", "/api/v3/user/organization_invitations", inviteeToken)
	assertStatusCode(t, userInvites, http.StatusOK)
	if rows := testharness.DecodeJSONArray(t, userInvites); len(rows) != 0 {
		t.Fatalf("expected pending invites to be removed, got %#v", rows)
	}

	membershipResp := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/org-delete-membership/memberships/"+invitee.Login, ownerToken)
	assertStatusCode(t, membershipResp, http.StatusNotFound)
}

func TestOrganizationMemberHandlers_DeleteOrgMemberRejectsLastOwner(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	orgOwner, ownerToken := seedHarnessUser(t, h, "org-last-owner", false)
	if _, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, orgOwner), "org-last-owner-delete"); err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}

	deleteResp := h.DoRESTWithToken(t, "DELETE", "/api/v3/orgs/org-last-owner-delete/members/"+orgOwner.Login, ownerToken)
	assertStatusCode(t, deleteResp, http.StatusConflict)
}

func TestOrganizationMemberHandlers_SetOrgMembershipCreatesPendingInvitation(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	orgOwner, ownerToken := seedHarnessUser(t, h, "org-set-membership-owner", false)
	if _, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, orgOwner), "org-set-membership"); err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	target, targetToken := seedHarnessUser(t, h, "org-set-membership-target", false)

	setResp := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/org-set-membership/memberships/"+target.Login, ownerToken, map[string]any{
		"role": "admin",
	})
	assertStatusCode(t, setResp, http.StatusOK)
	payload := testharness.DecodeJSON(t, setResp)
	if payload["state"] != "pending" {
		t.Fatalf("state = %v, want pending", payload["state"])
	}
	if payload["role"] != "admin" {
		t.Fatalf("role = %v, want admin", payload["role"])
	}

	userInvites := h.DoRESTWithToken(t, "GET", "/api/v3/user/organization_invitations", targetToken)
	assertStatusCode(t, userInvites, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, userInvites)
	if len(rows) != 1 {
		t.Fatalf("expected one pending invite, got %#v", rows)
	}
	if rows[0]["role"] != db.OrganizationInvitationRoleAdmin {
		t.Fatalf("invitation role = %v, want %s", rows[0]["role"], db.OrganizationInvitationRoleAdmin)
	}
}

func TestOrganizationMemberHandlers_SetOrgMembershipUpdatesActiveRole(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	orgOwner, ownerToken := seedHarnessUser(t, h, "org-set-active-owner", false)
	org, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, orgOwner), "org-set-active")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	target, _ := seedHarnessUser(t, h, "org-set-active-target", false)
	if err := h.Svc.AddOrgMember(ctx, org.ID, target.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember failed: %v", err)
	}

	promoteResp := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/org-set-active/memberships/"+target.Login, ownerToken, map[string]any{
		"role": "admin",
	})
	assertStatusCode(t, promoteResp, http.StatusOK)
	promoted := testharness.DecodeJSON(t, promoteResp)
	if promoted["state"] != "active" || promoted["role"] != "admin" {
		t.Fatalf("promoted payload = %#v, want active/admin", promoted)
	}

	demoteResp := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/org-set-active/memberships/"+target.Login, ownerToken, map[string]any{
		"role": "member",
	})
	assertStatusCode(t, demoteResp, http.StatusOK)
	demoted := testharness.DecodeJSON(t, demoteResp)
	if demoted["state"] != "active" || demoted["role"] != "member" {
		t.Fatalf("demoted payload = %#v, want active/member", demoted)
	}
}

func TestOrganizationMemberHandlers_SetOrgMembershipRejectsInvalidRole(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	orgOwner, ownerToken := seedHarnessUser(t, h, "org-set-invalid-owner", false)
	if _, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, orgOwner), "org-set-invalid"); err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	target, _ := seedHarnessUser(t, h, "org-set-invalid-target", false)

	resp := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/org-set-invalid/memberships/"+target.Login, ownerToken, map[string]any{
		"role": db.OrganizationInvitationRoleDirectMember,
	})
	assertStatusCode(t, resp, http.StatusUnprocessableEntity)
	body := testharness.DecodeJSON(t, resp)
	if body["message"] != "validation failed: role must be member or admin" {
		t.Fatalf("message = %v, want validation role error", body["message"])
	}
}
