package service_test

import (
	"context"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

func TestOutsideCollaborator_DistinguishesOrgMembersFromRepoOnlyCollaborators(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := createTestUser(t, svc, "outside-owner")
	org, err := svc.EnsureOrg(service.ContextWithUser(ctx, *owner), "outside-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	repo, err := svc.CreateRepo(service.ContextWithUser(ctx, *owner), service.CreateRepoInput{
		OwnerLogin: org.Login,
		Name:       "governed-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("CreateRepo failed: %v", err)
	}

	outside := createTestUser(t, svc, "outside-user")
	member := createTestUser(t, svc, "inside-user")
	if err := svc.AddOrgMember(ctx, org.ID, member.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember failed: %v", err)
	}

	if err := svc.AddCollaborator(ctx, repo.ID, outside.ID, "read"); err != nil {
		t.Fatalf("AddCollaborator outside failed: %v", err)
	}
	if err := svc.AddCollaborator(ctx, repo.ID, member.ID, "read"); err != nil {
		t.Fatalf("AddCollaborator member failed: %v", err)
	}

	isOutside, err := svc.IsOutsideCollaborator(ctx, org.ID, outside.ID)
	if err != nil {
		t.Fatalf("IsOutsideCollaborator(outside) failed: %v", err)
	}
	if !isOutside {
		t.Fatal("expected outside user to be marked as outside collaborator")
	}

	isOutside, err = svc.IsOutsideCollaborator(ctx, org.ID, member.ID)
	if err != nil {
		t.Fatalf("IsOutsideCollaborator(member) failed: %v", err)
	}
	if isOutside {
		t.Fatal("org member should not be marked as outside collaborator")
	}

	rows, err := svc.ListOutsideCollaborators(ctx, org.ID)
	if err != nil {
		t.Fatalf("ListOutsideCollaborators failed: %v", err)
	}
	if len(rows) != 1 || rows[0].UserID != outside.ID {
		t.Fatalf("outside collaborators = %#v, want only user %d", rows, outside.ID)
	}

	if err := svc.AddOrgMember(ctx, org.ID, outside.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("promote outside user into org failed: %v", err)
	}
	isOutside, err = svc.IsOutsideCollaborator(ctx, org.ID, outside.ID)
	if err != nil {
		t.Fatalf("IsOutsideCollaborator(after org join) failed: %v", err)
	}
	if isOutside {
		t.Fatal("outside collaborator row should be removed once user joins the org")
	}
}

func TestOutsideCollaborator_AcceptingOrgRepoInvitationCreatesOutsideCollaborator(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := createTestUser(t, svc, "outside-invite-owner")
	org, err := svc.EnsureOrg(service.ContextWithUser(ctx, *owner), "outside-invite-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	repo, err := svc.CreateRepo(service.ContextWithUser(ctx, *owner), service.CreateRepoInput{
		OwnerLogin: org.Login,
		Name:       "invite-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("CreateRepo failed: %v", err)
	}

	invitee := createTestUser(t, svc, "outside-invitee")
	inv := &db.RepositoryInvitation{
		RepositoryID: repo.ID,
		InviteeID:    invitee.ID,
		InviterID:    owner.ID,
		Permissions:  "read",
	}
	if err := svc.CreateInvitation(ctx, inv); err != nil {
		t.Fatalf("CreateInvitation failed: %v", err)
	}

	isOutside, err := svc.IsOutsideCollaborator(ctx, org.ID, invitee.ID)
	if err != nil {
		t.Fatalf("IsOutsideCollaborator(before accept) failed: %v", err)
	}
	if isOutside {
		t.Fatal("pending repository invitation should not create outside collaborator row before acceptance")
	}

	if err := svc.AcceptInvitation(ctx, inv.ID, invitee.ID); err != nil {
		t.Fatalf("AcceptInvitation failed: %v", err)
	}
	isOutside, err = svc.IsOutsideCollaborator(ctx, org.ID, invitee.ID)
	if err != nil {
		t.Fatalf("IsOutsideCollaborator(after accept) failed: %v", err)
	}
	if !isOutside {
		t.Fatal("accepted org-owned repo invitation should create outside collaborator row for non-member")
	}
}

func TestOutsideCollaborator_ReconcilesAcrossRepoRemovalAndDelete(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := createTestUser(t, svc, "outside-cleanup-owner")
	org, err := svc.EnsureOrg(service.ContextWithUser(ctx, *owner), "outside-cleanup-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	ownerCtx := service.ContextWithUser(ctx, *owner)
	repo1, err := svc.CreateRepo(ownerCtx, service.CreateRepoInput{OwnerLogin: org.Login, Name: "repo-a", Private: true})
	if err != nil {
		t.Fatalf("CreateRepo repo-a failed: %v", err)
	}
	repo2, err := svc.CreateRepo(ownerCtx, service.CreateRepoInput{OwnerLogin: org.Login, Name: "repo-b", Private: true})
	if err != nil {
		t.Fatalf("CreateRepo repo-b failed: %v", err)
	}
	outside := createTestUser(t, svc, "outside-cleanup-user")

	if err := svc.AddCollaborator(ctx, repo1.ID, outside.ID, "read"); err != nil {
		t.Fatalf("AddCollaborator repo1 failed: %v", err)
	}
	if err := svc.AddCollaborator(ctx, repo2.ID, outside.ID, "write"); err != nil {
		t.Fatalf("AddCollaborator repo2 failed: %v", err)
	}

	if err := svc.RemoveCollaborator(ctx, repo1.ID, outside.ID); err != nil {
		t.Fatalf("RemoveCollaborator repo1 failed: %v", err)
	}
	isOutside, err := svc.IsOutsideCollaborator(ctx, org.ID, outside.ID)
	if err != nil {
		t.Fatalf("IsOutsideCollaborator(after repo1 remove) failed: %v", err)
	}
	if !isOutside {
		t.Fatal("outside collaborator should persist while another org repo still grants access")
	}

	if err := svc.DeleteRepo(ownerCtx, repo2.FullName); err != nil {
		t.Fatalf("DeleteRepo repo2 failed: %v", err)
	}
	isOutside, err = svc.IsOutsideCollaborator(ctx, org.ID, outside.ID)
	if err != nil {
		t.Fatalf("IsOutsideCollaborator(after repo2 delete) failed: %v", err)
	}
	if isOutside {
		t.Fatal("outside collaborator should be removed after the last org-owned repo access disappears")
	}
}

func TestOutsideCollaborator_TransferRepoIntoAndOutOfOrg(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	sourceOwner := createTestUser(t, svc, "transfer-source-owner")
	destinationOwner := createTestUser(t, svc, "transfer-destination-owner")
	outside := createTestUser(t, svc, "transfer-outside-user")
	orgCreator := createTestUser(t, svc, "transfer-org-creator")

	org, err := svc.EnsureOrg(service.ContextWithUser(ctx, *orgCreator), "transfer-outside-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	mustAddOrgMember(t, svc, org.ID, sourceOwner.ID, db.OrganizationRoleOwner)

	sourceCtx := service.ContextWithUser(ctx, *sourceOwner)
	repo, err := svc.CreateRepo(sourceCtx, service.CreateRepoInput{
		OwnerLogin: sourceOwner.Login,
		Name:       "portable-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("CreateRepo failed: %v", err)
	}
	if err := svc.AddCollaborator(ctx, repo.ID, outside.ID, "read"); err != nil {
		t.Fatalf("AddCollaborator failed: %v", err)
	}

	transferred, err := svc.TransferRepo(sourceCtx, repo.FullName, org.Login)
	if err != nil {
		t.Fatalf("TransferRepo into org failed: %v", err)
	}
	isOutside, err := svc.IsOutsideCollaborator(ctx, org.ID, outside.ID)
	if err != nil {
		t.Fatalf("IsOutsideCollaborator(after transfer in) failed: %v", err)
	}
	if !isOutside {
		t.Fatal("non-member collaborator should become an outside collaborator after transfer into org")
	}

	if _, err := svc.TransferRepo(sourceCtx, transferred.FullName, destinationOwner.Login); err != nil {
		t.Fatalf("TransferRepo out of org failed: %v", err)
	}
	isOutside, err = svc.IsOutsideCollaborator(ctx, org.ID, outside.ID)
	if err != nil {
		t.Fatalf("IsOutsideCollaborator(after transfer out) failed: %v", err)
	}
	if isOutside {
		t.Fatal("outside collaborator should be removed once the repo leaves the org")
	}
}
