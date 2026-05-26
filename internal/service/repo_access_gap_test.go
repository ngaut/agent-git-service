package service_test

import (
	"context"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

// TestIsPublicRepo fills the gap left by the existing repo_access_test.go:
// isPublicRepo is consumed by the anonymous-read branch of githttp and REST
// authorization but previously had no direct test.
func TestIsPublicRepo(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "pub-owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "public-repo",
		Private:    false,
	})
	if err != nil {
		t.Fatalf("create public repo: %v", err)
	}
	privateRepo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "private-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("create private repo: %v", err)
	}

	tests := []struct {
		name   string
		repoID uint
		want   bool
	}{
		{"public", repo.ID, true},
		{"private", privateRepo.ID, false},
		{"missing_repo_id_returns_false", 99999, false},
		{"zero_repo_id_returns_false", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := service.IsPublicRepoForTest(svc, ctx, tt.repoID)
			if got != tt.want {
				t.Errorf("isPublicRepo(%d) = %v, want %v", tt.repoID, got, tt.want)
			}
		})
	}
}

// TestHasRepoAccess_SiteAdminShortCircuit verifies the SiteAdmin bypass in
// hasRepoAccessFallback. This path is exercised indirectly by existing
// tests but never isolated — if the short-circuit breaks, a site admin
// without an explicit collaborator grant would silently lose repo access.
func TestHasRepoAccess_SiteAdminShortCircuit(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	siteAdmin := db.User{Login: "sa", Type: db.TypeUser, SiteAdmin: true}
	owner := db.User{Login: "some-owner", Type: db.TypeUser}
	if err := svc.DB.Create(&siteAdmin).Error; err != nil {
		t.Fatalf("create site admin: %v", err)
	}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: owner.Login, Name: "sa-test", Private: true})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	// Site admin has no collaborator row, no team membership, no org link — the
	// only reason they can access the repo is the SiteAdmin=true flag itself.
	perm, err := svc.HasRepoAccess(ctx, repo.ID, siteAdmin.ID)
	if err != nil {
		t.Fatalf("HasRepoAccess: %v", err)
	}
	if perm != service.RepoPermissionAdmin {
		t.Errorf("site admin should get admin perm, got %v", perm)
	}
}

// TestHasRepoAccess_OutsideCollaboratorRow verifies that an outside-collab
// row grants the specified permission. The existing org-base-permission
// test exercises adjacent logic; this case covers the standalone path.
func TestHasRepoAccess_OutsideCollaboratorRow(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "org-owner", Type: db.TypeOrganization}
	outside := db.User{Login: "external", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create org: %v", err)
	}
	if err := svc.DB.Create(&outside).Error; err != nil {
		t.Fatalf("create outside user: %v", err)
	}
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: owner.Login, Name: "r", Private: true})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	// Direct collaborator grant with explicit permission — outside-collab is
	// persisted as a Collaborator row with the granted permission.
	if err := svc.DB.Create(&db.Collaborator{RepositoryID: repo.ID, UserID: outside.ID, Permission: "write"}).Error; err != nil {
		t.Fatalf("create collaborator: %v", err)
	}

	perm, err := svc.HasRepoAccess(ctx, repo.ID, outside.ID)
	if err != nil {
		t.Fatalf("HasRepoAccess: %v", err)
	}
	if !perm.AtLeast(service.RepoPermissionWrite) {
		t.Errorf("outside collaborator with write grant should resolve >= write, got %v", perm)
	}
}
