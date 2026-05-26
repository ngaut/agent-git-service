package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func TestOrganizationInvitationAcceptFlow(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	inviter := createTestUser(t, svc, "org-inviter")
	orgCtx := service.ContextWithUser(ctx, *inviter)

	org, err := svc.EnsureOrg(orgCtx, "accept-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	team, err := svc.CreateTeam(ctx, org.ID, "Platform", "platform", "", "")
	if err != nil {
		t.Fatalf("CreateTeam failed: %v", err)
	}
	invitee := createTestUser(t, svc, "org-invitee")

	inv, err := svc.CreateOrganizationInvitation(ctx, service.CreateOrganizationInvitationInput{
		OrganizationID: org.ID,
		InviteeID:      invitee.ID,
		InviterID:      inviter.ID,
		Role:           db.OrganizationInvitationRoleDirectMember,
		TeamIDs:        []uint{team.ID},
	})
	if err != nil {
		t.Fatalf("CreateOrganizationInvitation failed: %v", err)
	}

	if err := svc.AcceptOrganizationInvitation(ctx, inv.ID, invitee.ID); err != nil {
		t.Fatalf("AcceptOrganizationInvitation failed: %v", err)
	}

	member, err := svc.GetOrgMember(ctx, org.ID, invitee.ID)
	if err != nil {
		t.Fatalf("GetOrgMember failed: %v", err)
	}
	if member.Role != db.OrganizationRoleMember {
		t.Fatalf("member role = %q, want %q", member.Role, db.OrganizationRoleMember)
	}

	teamMember, err := svc.GetTeamMember(ctx, team.ID, invitee.ID)
	if err != nil {
		t.Fatalf("GetTeamMember failed: %v", err)
	}
	if teamMember.Role != "member" {
		t.Fatalf("team member role = %q, want member", teamMember.Role)
	}

	_, err = svc.GetOrganizationInvitation(ctx, inv.ID)
	if !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected invitation to be deleted after accept, got %v", err)
	}

	orgs, err := svc.ListOrgsForUser(ctx, invitee.ID)
	if err != nil {
		t.Fatalf("ListOrgsForUser failed: %v", err)
	}
	if len(orgs) != 1 || orgs[0].ID != org.ID {
		t.Fatalf("expected invitee to belong to org %d, got %#v", org.ID, orgs)
	}
}

func TestCreateOrganizationInvitation_DefaultsToDirectMemberRole(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	inviter := createTestUser(t, svc, "org-default-role-inviter")
	org, err := svc.EnsureOrg(service.ContextWithUser(ctx, *inviter), "default-role-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	invitee := createTestUser(t, svc, "org-default-role-invitee")

	inv, err := svc.CreateOrganizationInvitation(ctx, service.CreateOrganizationInvitationInput{
		OrganizationID: org.ID,
		InviteeID:      invitee.ID,
		InviterID:      inviter.ID,
	})
	if err != nil {
		t.Fatalf("CreateOrganizationInvitation failed: %v", err)
	}
	if inv.Role != db.OrganizationInvitationRoleDirectMember {
		t.Fatalf("role = %q, want %q", inv.Role, db.OrganizationInvitationRoleDirectMember)
	}
}

func TestCreateOrganizationInvitation_RejectsLegacyRoleNames(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	inviter := createTestUser(t, svc, "org-legacy-role-inviter")
	org, err := svc.EnsureOrg(service.ContextWithUser(ctx, *inviter), "legacy-role-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	invitee := createTestUser(t, svc, "org-legacy-role-invitee")

	_, err = svc.CreateOrganizationInvitation(ctx, service.CreateOrganizationInvitationInput{
		OrganizationID: org.ID,
		InviteeID:      invitee.ID,
		InviterID:      inviter.ID,
		Role:           db.OrganizationRoleMember,
	})
	if !errors.Is(err, service.ErrValidation) {
		t.Fatalf("CreateOrganizationInvitation err = %v, want validation", err)
	}
	if !strings.Contains(err.Error(), "role must be direct_member or admin") {
		t.Fatalf("CreateOrganizationInvitation err = %v, want role validation detail", err)
	}
}

func TestOrganizationInvitationAcceptFlow_OwnerRole(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	inviter := createTestUser(t, svc, "org-owner-inviter")
	orgCtx := service.ContextWithUser(ctx, *inviter)

	org, err := svc.EnsureOrg(orgCtx, "owner-accept-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	team, err := svc.CreateTeam(ctx, org.ID, "Owners", "owners", "", "")
	if err != nil {
		t.Fatalf("CreateTeam failed: %v", err)
	}
	invitee := createTestUser(t, svc, "org-owner-invitee")

	inv, err := svc.CreateOrganizationInvitation(ctx, service.CreateOrganizationInvitationInput{
		OrganizationID: org.ID,
		InviteeID:      invitee.ID,
		InviterID:      inviter.ID,
		Role:           db.OrganizationInvitationRoleAdmin,
		TeamIDs:        []uint{team.ID},
	})
	if err != nil {
		t.Fatalf("CreateOrganizationInvitation failed: %v", err)
	}

	if err := svc.AcceptOrganizationInvitation(ctx, inv.ID, invitee.ID); err != nil {
		t.Fatalf("AcceptOrganizationInvitation failed: %v", err)
	}

	member, err := svc.GetOrgMember(ctx, org.ID, invitee.ID)
	if err != nil {
		t.Fatalf("GetOrgMember failed: %v", err)
	}
	if member.Role != db.OrganizationRoleOwner {
		t.Fatalf("member role = %q, want %q", member.Role, db.OrganizationRoleOwner)
	}

	isAdmin, err := svc.IsOrgAdmin(ctx, org.ID, invitee.ID)
	if err != nil {
		t.Fatalf("IsOrgAdmin failed: %v", err)
	}
	if !isAdmin {
		t.Fatal("owner invitee should become org admin after accepting invitation")
	}

	teamMember, err := svc.GetTeamMember(ctx, team.ID, invitee.ID)
	if err != nil {
		t.Fatalf("GetTeamMember failed: %v", err)
	}
	if teamMember.Role != "member" {
		t.Fatalf("team member role = %q, want member", teamMember.Role)
	}

	_, err = svc.GetOrganizationInvitation(ctx, inv.ID)
	if !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected invitation to be deleted after accept, got %v", err)
	}
}

func TestOrganizationInvitationUpsertAndRevoke(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	inviter := createTestUser(t, svc, "reinvite-owner")
	org, err := svc.EnsureOrg(service.ContextWithUser(ctx, *inviter), "reinvite-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	teamA, err := svc.CreateTeam(ctx, org.ID, "Alpha", "alpha", "", "")
	if err != nil {
		t.Fatalf("CreateTeam alpha failed: %v", err)
	}
	teamB, err := svc.CreateTeam(ctx, org.ID, "Beta", "beta", "", "")
	if err != nil {
		t.Fatalf("CreateTeam beta failed: %v", err)
	}
	invitee := createTestUser(t, svc, "reinvite-user")

	first, err := svc.CreateOrganizationInvitation(ctx, service.CreateOrganizationInvitationInput{
		OrganizationID: org.ID,
		InviteeID:      invitee.ID,
		InviterID:      inviter.ID,
		Role:           db.OrganizationInvitationRoleDirectMember,
		TeamIDs:        []uint{teamA.ID},
	})
	if err != nil {
		t.Fatalf("first CreateOrganizationInvitation failed: %v", err)
	}

	second, err := svc.CreateOrganizationInvitation(ctx, service.CreateOrganizationInvitationInput{
		OrganizationID: org.ID,
		InviteeID:      invitee.ID,
		InviterID:      inviter.ID,
		Role:           db.OrganizationInvitationRoleAdmin,
		TeamIDs:        []uint{teamB.ID},
	})
	if err != nil {
		t.Fatalf("second CreateOrganizationInvitation failed: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected reinvite to reuse ID %d, got %d", first.ID, second.ID)
	}
	if second.Role != db.OrganizationInvitationRoleAdmin {
		t.Fatalf("role = %q, want %q", second.Role, db.OrganizationInvitationRoleAdmin)
	}
	teamIDs, err := service.DecodeOrganizationInvitationTeamIDs(second.TeamIDsJSON)
	if err != nil {
		t.Fatalf("DecodeOrganizationInvitationTeamIDs failed: %v", err)
	}
	if len(teamIDs) != 1 || teamIDs[0] != teamB.ID {
		t.Fatalf("team ids = %#v, want [%d]", teamIDs, teamB.ID)
	}

	if err := svc.RevokeOrganizationInvitation(ctx, org.ID, second.ID); err != nil {
		t.Fatalf("RevokeOrganizationInvitation failed: %v", err)
	}
	_, err = svc.GetOrganizationInvitation(ctx, second.ID)
	if !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected invitation to be deleted after revoke, got %v", err)
	}
}

func TestOrganizationInvitationDeclineAndExpiry(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	inviter := createTestUser(t, svc, "decline-owner")
	org, err := svc.EnsureOrg(service.ContextWithUser(ctx, *inviter), "decline-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	invitee := createTestUser(t, svc, "decline-invitee")

	inv, err := svc.CreateOrganizationInvitation(ctx, service.CreateOrganizationInvitationInput{
		OrganizationID: org.ID,
		InviteeID:      invitee.ID,
		InviterID:      inviter.ID,
		Role:           db.OrganizationInvitationRoleDirectMember,
	})
	if err != nil {
		t.Fatalf("CreateOrganizationInvitation failed: %v", err)
	}
	if err := svc.DeclineOrganizationInvitation(ctx, inv.ID, invitee.ID); err != nil {
		t.Fatalf("DeclineOrganizationInvitation failed: %v", err)
	}
	_, err = svc.GetOrganizationInvitation(ctx, inv.ID)
	if !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected invitation to be deleted after decline, got %v", err)
	}

	expiredAt := time.Now().UTC().Add(-time.Hour)
	expired := db.OrganizationInvitation{
		OrganizationID: org.ID,
		InviteeID:      invitee.ID,
		InviterID:      inviter.ID,
		Role:           db.OrganizationInvitationRoleDirectMember,
		ExpiresAt:      &expiredAt,
		TeamIDsJSON:    "[]",
	}
	if err := svc.DB.Create(&expired).Error; err != nil {
		t.Fatalf("seed expired invitation: %v", err)
	}

	if err := svc.AcceptOrganizationInvitation(ctx, expired.ID, invitee.ID); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound when accepting expired invite, got %v", err)
	}
	ok, err := svc.IsOrgMember(ctx, org.ID, invitee.ID)
	if err != nil {
		t.Fatalf("IsOrgMember failed: %v", err)
	}
	if ok {
		t.Fatal("invitee should not join org when invitation is expired")
	}
}
