package service_test

import (
	"context"
	"errors"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

func TestRemoveOrgMember_RemovesOrgTeamMembershipsAndBecomesOutsideCollaborator(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := createTestUser(t, svc, "remove-org-owner")
	org, err := svc.EnsureOrg(service.ContextWithUser(ctx, *owner), "remove-org-member-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	member := createTestUser(t, svc, "remove-org-member-user")
	repo, err := svc.CreateRepo(service.ContextWithUser(ctx, *owner), service.CreateRepoInput{
		OwnerLogin: org.Login,
		Name:       "governed-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("CreateRepo failed: %v", err)
	}
	team, err := svc.CreateTeam(ctx, org.ID, "ops", "ops", "", "")
	if err != nil {
		t.Fatalf("CreateTeam failed: %v", err)
	}
	mustAddOrgMember(t, svc, org.ID, member.ID, db.OrganizationRoleMember)
	if err := svc.AddTeamMember(ctx, team.ID, member.ID, "member"); err != nil {
		t.Fatalf("AddTeamMember failed: %v", err)
	}
	if err := svc.AddCollaborator(ctx, repo.ID, member.ID, "read"); err != nil {
		t.Fatalf("AddCollaborator failed: %v", err)
	}

	if err := svc.RemoveOrgMember(ctx, org.ID, member.Login); err != nil {
		t.Fatalf("RemoveOrgMember failed: %v", err)
	}

	isMember, err := svc.IsOrgMember(ctx, org.ID, member.ID)
	if err != nil {
		t.Fatalf("IsOrgMember failed: %v", err)
	}
	if isMember {
		t.Fatal("expected org membership to be removed")
	}
	if _, err := svc.GetTeamMember(ctx, team.ID, member.ID); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("GetTeamMember err = %v, want not found", err)
	}
	isOutside, err := svc.IsOutsideCollaborator(ctx, org.ID, member.ID)
	if err != nil {
		t.Fatalf("IsOutsideCollaborator failed: %v", err)
	}
	if !isOutside {
		t.Fatal("expected removed org member with direct repo access to become outside collaborator")
	}
}

func TestRemoveOrgMember_RejectsLastOwner(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := createTestUser(t, svc, "last-owner")
	org, err := svc.EnsureOrg(service.ContextWithUser(ctx, *owner), "last-owner-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}

	err = svc.RemoveOrgMember(ctx, org.ID, owner.Login)
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("RemoveOrgMember err = %v, want conflict", err)
	}

	isMember, err := svc.IsOrgMember(ctx, org.ID, owner.ID)
	if err != nil {
		t.Fatalf("IsOrgMember failed: %v", err)
	}
	if !isMember {
		t.Fatal("expected last owner to remain in org")
	}
}

func TestRemoveOrgMembership_RevokesPendingInvitation(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := createTestUser(t, svc, "remove-membership-owner")
	org, err := svc.EnsureOrg(service.ContextWithUser(ctx, *owner), "remove-membership-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	invitee := createTestUser(t, svc, "remove-membership-invitee")
	if _, err := svc.CreateOrganizationInvitation(ctx, service.CreateOrganizationInvitationInput{
		OrganizationID: org.ID,
		InviteeID:      invitee.ID,
		InviterID:      owner.ID,
		Role:           db.OrganizationInvitationRoleDirectMember,
	}); err != nil {
		t.Fatalf("CreateOrganizationInvitation failed: %v", err)
	}

	if err := svc.RemoveOrgMembership(ctx, org.ID, invitee.Login); err != nil {
		t.Fatalf("RemoveOrgMembership failed: %v", err)
	}

	if _, err := svc.GetOrgMembership(ctx, org.ID, invitee.Login); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("GetOrgMembership err = %v, want not found", err)
	}

	rows, err := svc.ListOrgAuditLog(ctx, org.ID, service.AuditLogFilters{PerPage: 10})
	if err != nil {
		t.Fatalf("ListOrgAuditLog failed: %v", err)
	}
	for _, row := range rows {
		if row.Action == service.AuditActionOrgRemoveMember && row.TargetLogin == invitee.Login {
			t.Fatalf("pending invitation revocation must not emit %s for %s", row.Action, invitee.Login)
		}
	}
}
