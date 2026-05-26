package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func TestInvitationFullFlow(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "inv-owner", "inv-repo")
	repo := getRepoForTest(t, ctx, svc, "inv-owner/inv-repo")
	invitee := createTestUser(t, svc, "inv-invitee")

	inv := &db.RepositoryInvitation{
		RepositoryID: repo.ID,
		InviterID:    repo.OwnerID,
		InviteeID:    invitee.ID,
		Permissions:  "write",
	}
	if err := svc.CreateInvitation(ctx, inv); err != nil {
		t.Fatalf("CreateInvitation failed: %v", err)
	}
	if inv.ID == 0 {
		t.Fatal("expected invitation ID to be set")
	}

	repoInvs, err := svc.ListRepoInvitations(ctx, repo.ID)
	if err != nil {
		t.Fatalf("ListRepoInvitations failed: %v", err)
	}
	if len(repoInvs) != 1 {
		t.Fatalf("expected 1 repo invitation, got %d", len(repoInvs))
	}
	if repoInvs[0].InviteeID != invitee.ID {
		t.Fatalf("expected invitee ID %d, got %d", invitee.ID, repoInvs[0].InviteeID)
	}
	if repoInvs[0].Permissions != "write" {
		t.Fatalf("expected permission write, got %q", repoInvs[0].Permissions)
	}

	userInvs, err := svc.ListUserInvitations(ctx, invitee.ID)
	if err != nil {
		t.Fatalf("ListUserInvitations failed: %v", err)
	}
	if len(userInvs) != 1 {
		t.Fatalf("expected 1 user invitation, got %d", len(userInvs))
	}
	if userInvs[0].RepositoryID != repo.ID {
		t.Fatalf("expected repo ID %d, got %d", repo.ID, userInvs[0].RepositoryID)
	}

	got, err := svc.GetInvitation(ctx, inv.ID)
	if err != nil {
		t.Fatalf("GetInvitation failed: %v", err)
	}
	if got.InviteeID != invitee.ID {
		t.Fatalf("expected invitee ID %d, got %d", invitee.ID, got.InviteeID)
	}

	if err := svc.AcceptInvitation(ctx, inv.ID, invitee.ID); err != nil {
		t.Fatalf("AcceptInvitation failed: %v", err)
	}

	_, err = svc.GetInvitation(ctx, inv.ID)
	if !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after accept, got %v", err)
	}

	isCollab, err := svc.IsCollaborator(ctx, repo.ID, invitee.ID)
	if err != nil {
		t.Fatalf("IsCollaborator failed: %v", err)
	}
	if !isCollab {
		t.Fatal("expected invitee to be collaborator after accepting")
	}

	collabs, err := svc.ListCollaborators(ctx, repo.ID)
	if err != nil {
		t.Fatalf("ListCollaborators failed: %v", err)
	}
	if len(collabs) != 1 {
		t.Fatalf("expected 1 collaborator, got %d", len(collabs))
	}
	if collabs[0].UserID != invitee.ID {
		t.Fatalf("expected collaborator user ID %d, got %d", invitee.ID, collabs[0].UserID)
	}
	if collabs[0].Permission != "write" {
		t.Fatalf("expected collaborator permission write, got %q", collabs[0].Permission)
	}
}

func TestInvitationCreateUpsertSemantics(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "upsert-owner", "upsert-repo")
	repo := getRepoForTest(t, ctx, svc, "upsert-owner/upsert-repo")
	invitee := createTestUser(t, svc, "upsert-invitee")

	inv := &db.RepositoryInvitation{
		RepositoryID: repo.ID,
		InviterID:    repo.OwnerID,
		InviteeID:    invitee.ID,
		Permissions:  "read",
	}
	if err := svc.CreateInvitation(ctx, inv); err != nil {
		t.Fatalf("CreateInvitation failed: %v", err)
	}
	if inv.ID == 0 {
		t.Fatal("expected invitation ID to be set")
	}
	firstID := inv.ID
	firstCreatedAt := inv.CreatedAt
	if firstCreatedAt.IsZero() {
		t.Fatal("expected created_at to be populated on first invitation")
	}

	reinvite := &db.RepositoryInvitation{
		RepositoryID: repo.ID,
		InviterID:    repo.OwnerID,
		InviteeID:    invitee.ID,
		Permissions:  "admin",
	}
	if err := svc.CreateInvitation(ctx, reinvite); err != nil {
		t.Fatalf("CreateInvitation reinvite failed: %v", err)
	}
	if reinvite.ID != firstID {
		t.Fatalf("expected invitation ID %d to be preserved, got %d", firstID, reinvite.ID)
	}
	if !reinvite.CreatedAt.Equal(firstCreatedAt) {
		t.Fatalf("expected reinvite created_at %s, got %s", firstCreatedAt.Format(time.RFC3339Nano), reinvite.CreatedAt.Format(time.RFC3339Nano))
	}

	repoInvs, err := svc.ListRepoInvitations(ctx, repo.ID)
	if err != nil {
		t.Fatalf("ListRepoInvitations failed: %v", err)
	}
	if len(repoInvs) != 1 {
		t.Fatalf("expected 1 repo invitation, got %d", len(repoInvs))
	}
	if repoInvs[0].ID != firstID {
		t.Fatalf("expected repo invitation ID %d, got %d", firstID, repoInvs[0].ID)
	}
	if repoInvs[0].Permissions != "admin" {
		t.Fatalf("expected permission admin, got %q", repoInvs[0].Permissions)
	}
	if !repoInvs[0].CreatedAt.Equal(firstCreatedAt) {
		t.Fatalf("expected stored created_at %s, got %s", firstCreatedAt.Format(time.RFC3339Nano), repoInvs[0].CreatedAt.Format(time.RFC3339Nano))
	}
}

func TestInvitationDeclineFlow(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "decline-owner", "decline-repo")
	repo := getRepoForTest(t, ctx, svc, "decline-owner/decline-repo")
	invitee := createTestUser(t, svc, "decline-invitee")

	inv := &db.RepositoryInvitation{
		RepositoryID: repo.ID,
		InviterID:    repo.OwnerID,
		InviteeID:    invitee.ID,
		Permissions:  "read",
	}
	if err := svc.CreateInvitation(ctx, inv); err != nil {
		t.Fatalf("CreateInvitation failed: %v", err)
	}

	if err := svc.DeclineInvitation(ctx, inv.ID, invitee.ID); err != nil {
		t.Fatalf("DeclineInvitation failed: %v", err)
	}

	isCollab, err := svc.IsCollaborator(ctx, repo.ID, invitee.ID)
	if err != nil {
		t.Fatalf("IsCollaborator failed: %v", err)
	}
	if isCollab {
		t.Fatal("expected invitee to not be collaborator after decline")
	}

	_, err = svc.GetInvitation(ctx, inv.ID)
	if !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after decline, got %v", err)
	}
}

func TestInvitationAcceptNotFound(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	user := createTestUser(t, svc, "accept-missing")

	if err := svc.AcceptInvitation(ctx, 999999, user.ID); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing accept, got %v", err)
	}
}

func TestInvitationDeclineNotFound(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	user := createTestUser(t, svc, "decline-missing")

	if err := svc.DeclineInvitation(ctx, 999999, user.ID); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing decline, got %v", err)
	}
}

func TestInvitationUnauthorizedAccess(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "unauth-owner", "unauth-repo")
	repo := getRepoForTest(t, ctx, svc, "unauth-owner/unauth-repo")
	invitee := createTestUser(t, svc, "unauth-invitee")
	other := createTestUser(t, svc, "unauth-other")

	inv := &db.RepositoryInvitation{
		RepositoryID: repo.ID,
		InviterID:    repo.OwnerID,
		InviteeID:    invitee.ID,
		Permissions:  "write",
	}
	if err := svc.CreateInvitation(ctx, inv); err != nil {
		t.Fatalf("CreateInvitation failed: %v", err)
	}

	if err := svc.AcceptInvitation(ctx, inv.ID, other.ID); !errors.Is(err, service.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized for accept, got %v", err)
	}

	if err := svc.DeclineInvitation(ctx, inv.ID, other.ID); !errors.Is(err, service.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized for decline, got %v", err)
	}

	_, err := svc.GetInvitation(ctx, inv.ID)
	if err != nil {
		t.Fatalf("expected invitation to remain, got %v", err)
	}

	isCollab, err := svc.IsCollaborator(ctx, repo.ID, other.ID)
	if err != nil {
		t.Fatalf("IsCollaborator failed: %v", err)
	}
	if isCollab {
		t.Fatal("expected unauthorized user to not be collaborator")
	}
}

func TestInvitationGetNotFound(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	_, err := svc.GetInvitation(ctx, 999999)
	if !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestInvitationPermissionLevels(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "perm-owner", "perm-repo")
	repo := getRepoForTest(t, ctx, svc, "perm-owner/perm-repo")

	readUser := createTestUser(t, svc, "perm-read")
	writeUser := createTestUser(t, svc, "perm-write")
	adminUser := createTestUser(t, svc, "perm-admin")

	invs := []*db.RepositoryInvitation{
		{RepositoryID: repo.ID, InviterID: repo.OwnerID, InviteeID: readUser.ID, Permissions: "read"},
		{RepositoryID: repo.ID, InviterID: repo.OwnerID, InviteeID: writeUser.ID, Permissions: "write"},
		{RepositoryID: repo.ID, InviterID: repo.OwnerID, InviteeID: adminUser.ID, Permissions: "admin"},
	}
	for _, inv := range invs {
		if err := svc.CreateInvitation(ctx, inv); err != nil {
			t.Fatalf("CreateInvitation failed: %v", err)
		}
	}

	repoInvs, err := svc.ListRepoInvitations(ctx, repo.ID)
	if err != nil {
		t.Fatalf("ListRepoInvitations failed: %v", err)
	}
	if len(repoInvs) != 3 {
		t.Fatalf("expected 3 invitations, got %d", len(repoInvs))
	}

	permByInvitee := map[uint]string{}
	for _, inv := range repoInvs {
		permByInvitee[inv.InviteeID] = inv.Permissions
	}
	if permByInvitee[readUser.ID] != "read" {
		t.Fatalf("expected read permission, got %q", permByInvitee[readUser.ID])
	}
	if permByInvitee[writeUser.ID] != "write" {
		t.Fatalf("expected write permission, got %q", permByInvitee[writeUser.ID])
	}
	if permByInvitee[adminUser.ID] != "admin" {
		t.Fatalf("expected admin permission, got %q", permByInvitee[adminUser.ID])
	}

	if err := svc.AcceptInvitation(ctx, invs[2].ID, adminUser.ID); err != nil {
		t.Fatalf("AcceptInvitation failed: %v", err)
	}

	collabs, err := svc.ListCollaborators(ctx, repo.ID)
	if err != nil {
		t.Fatalf("ListCollaborators failed: %v", err)
	}
	if len(collabs) != 1 {
		t.Fatalf("expected 1 collaborator after accept, got %d", len(collabs))
	}
	if collabs[0].UserID != adminUser.ID {
		t.Fatalf("expected admin collaborator user ID %d, got %d", adminUser.ID, collabs[0].UserID)
	}
	if collabs[0].Permission != "admin" {
		t.Fatalf("expected admin permission, got %q", collabs[0].Permission)
	}
}

func TestInvitationDirectCollaboratorAddRemove(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "collab-owner", "collab-repo")
	repo := getRepoForTest(t, ctx, svc, "collab-owner/collab-repo")

	readUser := createTestUser(t, svc, "collab-read")
	writeUser := createTestUser(t, svc, "collab-write")
	adminUser := createTestUser(t, svc, "collab-admin")

	before, err := svc.IsCollaborator(ctx, repo.ID, readUser.ID)
	if err != nil {
		t.Fatalf("IsCollaborator before add failed: %v", err)
	}
	if before {
		t.Fatal("expected IsCollaborator to be false before add")
	}

	if err := svc.AddCollaborator(ctx, repo.ID, readUser.ID, "read"); err != nil {
		t.Fatalf("AddCollaborator(read) failed: %v", err)
	}
	if err := svc.AddCollaborator(ctx, repo.ID, writeUser.ID, "write"); err != nil {
		t.Fatalf("AddCollaborator(write) failed: %v", err)
	}
	if err := svc.AddCollaborator(ctx, repo.ID, adminUser.ID, "admin"); err != nil {
		t.Fatalf("AddCollaborator(admin) failed: %v", err)
	}

	afterAdd, err := svc.IsCollaborator(ctx, repo.ID, readUser.ID)
	if err != nil {
		t.Fatalf("IsCollaborator after add failed: %v", err)
	}
	if !afterAdd {
		t.Fatal("expected IsCollaborator to be true after add")
	}

	collabs, err := svc.ListCollaborators(ctx, repo.ID)
	if err != nil {
		t.Fatalf("ListCollaborators failed: %v", err)
	}
	if len(collabs) != 3 {
		t.Fatalf("expected 3 collaborators, got %d", len(collabs))
	}

	permByUser := map[uint]string{}
	for _, collab := range collabs {
		permByUser[collab.UserID] = collab.Permission
	}
	if permByUser[readUser.ID] != "read" {
		t.Fatalf("expected read permission, got %q", permByUser[readUser.ID])
	}
	if permByUser[writeUser.ID] != "write" {
		t.Fatalf("expected write permission, got %q", permByUser[writeUser.ID])
	}
	if permByUser[adminUser.ID] != "admin" {
		t.Fatalf("expected admin permission, got %q", permByUser[adminUser.ID])
	}

	if err := svc.RemoveCollaborator(ctx, repo.ID, writeUser.ID); err != nil {
		t.Fatalf("RemoveCollaborator failed: %v", err)
	}

	after, err := svc.IsCollaborator(ctx, repo.ID, writeUser.ID)
	if err != nil {
		t.Fatalf("IsCollaborator after remove failed: %v", err)
	}
	if after {
		t.Fatal("expected removed collaborator to be absent")
	}
}

func TestAddCollaboratorUpdatePreservesCreatedAt(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "collab-update-owner", "collab-update-repo")
	repo := getRepoForTest(t, ctx, svc, "collab-update-owner/collab-update-repo")
	user := createTestUser(t, svc, "collab-update-user")

	if err := svc.AddCollaborator(ctx, repo.ID, user.ID, "read"); err != nil {
		t.Fatalf("AddCollaborator(read) failed: %v", err)
	}

	var first db.Collaborator
	if err := svc.DB.First(&first, "repository_id = ? AND user_id = ?", repo.ID, user.ID).Error; err != nil {
		t.Fatalf("load collaborator after create: %v", err)
	}
	if first.CreatedAt.IsZero() {
		t.Fatal("expected created_at to be populated after collaborator create")
	}

	if err := svc.AddCollaborator(ctx, repo.ID, user.ID, "write"); err != nil {
		t.Fatalf("AddCollaborator(write) failed: %v", err)
	}

	var updated db.Collaborator
	if err := svc.DB.First(&updated, "repository_id = ? AND user_id = ?", repo.ID, user.ID).Error; err != nil {
		t.Fatalf("load collaborator after update: %v", err)
	}
	if updated.Permission != "write" {
		t.Fatalf("expected permission write, got %q", updated.Permission)
	}
	if updated.CreatedAt.IsZero() {
		t.Fatal("expected created_at to remain populated after collaborator update")
	}
	if !updated.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("expected created_at %s to be preserved, got %s", first.CreatedAt.Format(time.RFC3339Nano), updated.CreatedAt.Format(time.RFC3339Nano))
	}
}

func createTestUser(t *testing.T, svc *service.Service, login string) *db.User {
	t.Helper()
	user := &db.User{Login: login, Name: login, Type: db.TypeUser}
	if err := svc.DB.Create(user).Error; err != nil {
		t.Fatalf("create user %s: %v", login, err)
	}
	return user
}

func getRepoForTest(t *testing.T, ctx context.Context, svc *service.Service, fullName string) db.Repository {
	t.Helper()
	repo, err := svc.GetRepo(ctx, fullName)
	if err != nil {
		t.Fatalf("GetRepo(%s) failed: %v", fullName, err)
	}
	return repo
}
