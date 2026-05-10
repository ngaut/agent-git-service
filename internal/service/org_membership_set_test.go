package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

func TestSetOrgMembership_UpdatesActiveRoleAndGuardsLastOwner(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := createTestUser(t, svc, "set-membership-owner")
	org, err := svc.EnsureOrg(service.ContextWithUser(ctx, *owner), "set-membership-role-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	member := createTestUser(t, svc, "set-membership-member")

	view, err := svc.SetOrgMembership(ctx, org.ID, member.Login, "admin", owner.ID)
	if err != nil {
		t.Fatalf("SetOrgMembership promote failed: %v", err)
	}
	if view.State != "pending" || view.Role != "admin" {
		t.Fatalf("promote view = %#v, want pending/admin", view)
	}

	invs, err := svc.ListOrganizationInvitations(ctx, org.ID)
	if err != nil {
		t.Fatalf("ListOrganizationInvitations failed: %v", err)
	}
	if len(invs) != 1 {
		t.Fatalf("expected one invitation, got %d", len(invs))
	}
	if err := svc.AcceptOrganizationInvitation(ctx, invs[0].ID, member.ID); err != nil {
		t.Fatalf("AcceptOrganizationInvitation failed: %v", err)
	}

	view, err = svc.SetOrgMembership(ctx, org.ID, member.Login, "member", owner.ID)
	if err != nil {
		t.Fatalf("SetOrgMembership demote failed: %v", err)
	}
	if view.State != "active" || view.Role != "member" {
		t.Fatalf("demote view = %#v, want active/member", view)
	}

	err = func() error {
		_, err := svc.SetOrgMembership(ctx, org.ID, owner.Login, "member", owner.ID)
		return err
	}()
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("SetOrgMembership demote last owner err = %v, want conflict", err)
	}

	stillOwner, err := svc.GetOrgMember(ctx, org.ID, owner.ID)
	if err != nil {
		t.Fatalf("GetOrgMember owner failed: %v", err)
	}
	if stillOwner.Role != db.OrganizationRoleOwner {
		t.Fatalf("owner role = %q, want %q", stillOwner.Role, db.OrganizationRoleOwner)
	}
}

func TestSetOrgMembership_UpsertsPendingInvitationRole(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := createTestUser(t, svc, "set-pending-owner")
	org, err := svc.EnsureOrg(service.ContextWithUser(ctx, *owner), "set-pending-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	target := createTestUser(t, svc, "set-pending-target")

	first, err := svc.SetOrgMembership(ctx, org.ID, target.Login, "admin", owner.ID)
	if err != nil {
		t.Fatalf("SetOrgMembership first failed: %v", err)
	}
	if first.State != "pending" || first.Role != "admin" {
		t.Fatalf("first view = %#v, want pending/admin", first)
	}

	second, err := svc.SetOrgMembership(ctx, org.ID, target.Login, "member", owner.ID)
	if err != nil {
		t.Fatalf("SetOrgMembership second failed: %v", err)
	}
	if second.State != "pending" || second.Role != "member" {
		t.Fatalf("second view = %#v, want pending/member", second)
	}

	invs, err := svc.ListOrganizationInvitations(ctx, org.ID)
	if err != nil {
		t.Fatalf("ListOrganizationInvitations failed: %v", err)
	}
	if len(invs) != 1 {
		t.Fatalf("expected one pending invitation, got %d", len(invs))
	}
	if invs[0].Role != db.OrganizationInvitationRoleDirectMember {
		t.Fatalf("invitation role = %q, want %q", invs[0].Role, db.OrganizationInvitationRoleDirectMember)
	}
}

func TestSetOrgMembership_RejectsInvalidRole(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := createTestUser(t, svc, "set-invalid-owner")
	org, err := svc.EnsureOrg(service.ContextWithUser(ctx, *owner), "set-invalid-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	target := createTestUser(t, svc, "set-invalid-target")

	_, err = svc.SetOrgMembership(ctx, org.ID, target.Login, "direct_member", owner.ID)
	if !errors.Is(err, service.ErrValidation) {
		t.Fatalf("SetOrgMembership err = %v, want validation", err)
	}
	if !strings.Contains(err.Error(), "role must be member or admin") {
		t.Fatalf("SetOrgMembership err = %v, want role validation detail", err)
	}
}

func TestSetOrgMembership_DoesNotEmitAddAuditForActiveMemberUpdates(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := createTestUser(t, svc, "set-audit-owner")
	org, err := svc.EnsureOrg(service.ContextWithUser(ctx, *owner), "set-audit-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	member := createTestUser(t, svc, "set-audit-member")
	mustAddOrgMember(t, svc, org.ID, member.ID, db.OrganizationRoleMember)
	if err := svc.DBForCtx(ctx).Where("organization_id = ?", org.ID).Delete(&db.AuditLogEntry{}).Error; err != nil {
		t.Fatalf("clear audit log failed: %v", err)
	}

	if _, err := svc.SetOrgMembership(ctx, org.ID, member.Login, "admin", owner.ID); err != nil {
		t.Fatalf("SetOrgMembership promote existing member failed: %v", err)
	}
	if _, err := svc.SetOrgMembership(ctx, org.ID, member.Login, "admin", owner.ID); err != nil {
		t.Fatalf("SetOrgMembership idempotent existing member failed: %v", err)
	}

	rows, err := svc.ListOrgAuditLog(ctx, org.ID, service.AuditLogFilters{PerPage: 20})
	if err != nil {
		t.Fatalf("ListOrgAuditLog failed: %v", err)
	}
	for _, row := range rows {
		if row.Action == service.AuditActionOrgAddMember && row.TargetLogin == member.Login {
			t.Fatalf("active membership updates must not emit %s for %s", row.Action, member.Login)
		}
	}
}
