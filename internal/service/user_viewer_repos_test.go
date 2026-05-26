package service_test

import (
	"context"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func TestListViewerRepos_IncludesEffectiveAccessAndDeduplicates(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	viewer := db.User{Login: "viewer", Name: "viewer", Type: db.TypeUser}
	org := db.User{Login: "viewer-org", Name: "viewer-org", Type: db.TypeOrganization}
	org.DefaultRepositoryPermission = "triage"
	otherOwner := db.User{Login: "other-owner", Name: "other-owner", Type: db.TypeUser}
	for _, user := range []*db.User{&viewer, &org, &otherOwner} {
		if err := svc.DB.Create(user).Error; err != nil {
			t.Fatalf("create user %s: %v", user.Login, err)
		}
	}

	ownedRepo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: viewer.Login,
		Name:       "owned-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("create owned repo: %v", err)
	}

	collabRepo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: otherOwner.Login,
		Name:       "collab-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("create collaborator repo: %v", err)
	}
	if err := svc.AddCollaborator(ctx, collabRepo.ID, viewer.ID, "write"); err != nil {
		t.Fatalf("add collaborator: %v", err)
	}

	teamRepo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: org.Login,
		Name:       "team-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("create team repo: %v", err)
	}

	overrideRepo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: org.Login,
		Name:       "override-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("create override repo: %v", err)
	}

	orgBaseRepo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: org.Login,
		Name:       "org-base-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("create org base repo: %v", err)
	}

	hiddenRepo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: otherOwner.Login,
		Name:       "hidden-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("create hidden repo: %v", err)
	}

	teamRead, err := svc.CreateTeam(ctx, org.ID, "readers", "readers", "", "")
	if err != nil {
		t.Fatalf("create readers team: %v", err)
	}
	teamWrite, err := svc.CreateTeam(ctx, org.ID, "writers", "writers", "", "")
	if err != nil {
		t.Fatalf("create writers team: %v", err)
	}
	teamAdmin, err := svc.CreateTeam(ctx, org.ID, "admins", "admins", "", "")
	if err != nil {
		t.Fatalf("create admins team: %v", err)
	}

	if err := svc.AddOrgMember(ctx, org.ID, viewer.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("add org member: %v", err)
	}

	for _, teamID := range []uint{teamRead.ID, teamWrite.ID, teamAdmin.ID} {
		if err := svc.AddTeamMember(ctx, teamID, viewer.ID, "member"); err != nil {
			t.Fatalf("add team member to %d: %v", teamID, err)
		}
	}

	if err := svc.AddTeamRepo(ctx, teamRead.ID, teamRepo.ID, "pull"); err != nil {
		t.Fatalf("grant read team repo: %v", err)
	}
	if err := svc.AddTeamRepo(ctx, teamWrite.ID, teamRepo.ID, "push"); err != nil {
		t.Fatalf("grant write team repo: %v", err)
	}

	if err := svc.AddTeamRepo(ctx, teamAdmin.ID, overrideRepo.ID, "admin"); err != nil {
		t.Fatalf("grant admin team repo: %v", err)
	}
	if err := svc.AddCollaborator(ctx, overrideRepo.ID, viewer.ID, "read"); err != nil {
		t.Fatalf("add override collaborator: %v", err)
	}

	viewerCtx := service.ContextWithUser(ctx, viewer)
	repos, err := svc.ListViewerRepos(viewerCtx)
	if err != nil {
		t.Fatalf("ListViewerRepos: %v", err)
	}

	got := make(map[string]service.RepoPermission, len(repos))
	for _, repo := range repos {
		if _, exists := got[repo.Repository.FullName]; exists {
			t.Fatalf("duplicate repo in viewer list: %s", repo.Repository.FullName)
		}
		got[repo.Repository.FullName] = repo.Permission
	}

	if got[ownedRepo.FullName] != service.RepoPermissionAdmin {
		t.Fatalf("owned repo permission = %v, want admin", got[ownedRepo.FullName])
	}
	if got[collabRepo.FullName] != service.RepoPermissionWrite {
		t.Fatalf("collaborator repo permission = %v, want write", got[collabRepo.FullName])
	}
	if got[teamRepo.FullName] != service.RepoPermissionWrite {
		t.Fatalf("team repo permission = %v, want write", got[teamRepo.FullName])
	}
	if got[overrideRepo.FullName] != service.RepoPermissionAdmin {
		t.Fatalf("override repo permission = %v, want admin", got[overrideRepo.FullName])
	}
	if got[orgBaseRepo.FullName] != service.RepoPermissionRead {
		t.Fatalf("org base repo permission = %v, want read via triage alias", got[orgBaseRepo.FullName])
	}
	if _, exists := got[hiddenRepo.FullName]; exists {
		t.Fatalf("hidden repo should not be visible")
	}

	if err := svc.RemoveTeamMember(ctx, teamRead.ID, viewer.ID); err != nil {
		t.Fatalf("remove viewer from read team: %v", err)
	}
	if err := svc.RemoveTeamMember(ctx, teamWrite.ID, viewer.ID); err != nil {
		t.Fatalf("remove viewer from write team: %v", err)
	}

	repos, err = svc.ListViewerRepos(viewerCtx)
	if err != nil {
		t.Fatalf("ListViewerRepos after revoke: %v", err)
	}

	afterRevoke := make(map[string]service.RepoPermission, len(repos))
	for _, repo := range repos {
		afterRevoke[repo.Repository.FullName] = repo.Permission
	}
	if afterRevoke[teamRepo.FullName] != service.RepoPermissionRead {
		t.Fatalf("team repo permission after revoke = %v, want read via triage alias", afterRevoke[teamRepo.FullName])
	}
}
