package service_test

import (
	"context"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

func TestRepoPermission_AtLeast(t *testing.T) {
	if !service.RepoPermissionAdmin.AtLeast(service.RepoPermissionWrite) {
		t.Fatalf("expected admin >= write")
	}
	if !service.RepoPermissionMaintain.AtLeast(service.RepoPermissionWrite) {
		t.Fatalf("expected maintain >= write")
	}
	if !service.RepoPermissionWrite.AtLeast(service.RepoPermissionMaintain) {
		t.Fatalf("expected write >= maintain alias")
	}
	if !service.RepoPermissionWrite.AtLeast(service.RepoPermissionTriage) {
		t.Fatalf("expected write >= triage")
	}
	if !service.RepoPermissionRead.AtLeast(service.RepoPermissionTriage) {
		t.Fatalf("expected read >= triage alias")
	}
	if service.RepoPermissionRead.AtLeast(service.RepoPermissionWrite) {
		t.Fatalf("expected read < write")
	}
	if service.RepoPermissionTriage.AtLeast(service.RepoPermissionWrite) {
		t.Fatalf("expected triage < write")
	}
	if service.RepoPermissionNone.AtLeast(service.RepoPermissionRead) {
		t.Fatalf("expected none < read")
	}
}

func TestHasRepoAccess_PermissionPrecedence(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	admin := db.User{Login: "site-admin", Name: "site-admin", Type: db.TypeUser, SiteAdmin: true}
	owner := db.User{Login: "repo-owner", Name: "repo-owner", Type: db.TypeUser}
	collab := db.User{Login: "collab", Name: "collab", Type: db.TypeUser}
	teamUser := db.User{Login: "team-user", Name: "team-user", Type: db.TypeUser}
	multiTeam := db.User{Login: "multi-team", Name: "multi-team", Type: db.TypeUser}
	collabOverride := db.User{Login: "collab-override", Name: "collab-override", Type: db.TypeUser}
	outsider := db.User{Login: "outsider", Name: "outsider", Type: db.TypeUser}
	org := db.User{Login: "repo-org", Name: "repo-org", Type: db.TypeOrganization}

	users := []*db.User{&admin, &owner, &collab, &teamUser, &multiTeam, &collabOverride, &outsider, &org}
	for _, u := range users {
		if err := svc.DB.Create(u).Error; err != nil {
			t.Fatalf("create user %s: %v", u.Login, err)
		}
	}

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    owner.Login,
		Name:          "access-repo",
		DefaultBranch: "main",
		Private:       true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	teamRead := db.Team{OrganizationID: org.ID, Name: "team-read", Slug: "team-read"}
	teamWrite := db.Team{OrganizationID: org.ID, Name: "team-write", Slug: "team-write"}
	teamAdmin := db.Team{OrganizationID: org.ID, Name: "team-admin", Slug: "team-admin"}
	for _, team := range []*db.Team{&teamRead, &teamWrite, &teamAdmin} {
		if err := svc.DB.Create(team).Error; err != nil {
			t.Fatalf("create team %s: %v", team.Name, err)
		}
	}

	if err := svc.DB.Create(&db.Collaborator{RepositoryID: repo.ID, UserID: collab.ID, Permission: "write"}).Error; err != nil {
		t.Fatalf("add collaborator: %v", err)
	}
	if err := svc.DB.Create(&db.Collaborator{RepositoryID: repo.ID, UserID: collabOverride.ID, Permission: "read"}).Error; err != nil {
		t.Fatalf("add collab override: %v", err)
	}

	for _, userID := range []uint{teamUser.ID, multiTeam.ID, collabOverride.ID} {
		if err := svc.AddOrgMember(ctx, org.ID, userID, db.OrganizationRoleMember); err != nil {
			t.Fatalf("add org member %d: %v", userID, err)
		}
	}

	if err := svc.AddTeamMember(ctx, teamRead.ID, teamUser.ID, "member"); err != nil {
		t.Fatalf("add team member: %v", err)
	}
	if err := svc.DB.Create(&db.TeamRepository{TeamID: teamRead.ID, RepositoryID: repo.ID, Permission: "read"}).Error; err != nil {
		t.Fatalf("grant team read: %v", err)
	}

	if err := svc.AddTeamMember(ctx, teamRead.ID, multiTeam.ID, "member"); err != nil {
		t.Fatalf("add multi-team member read: %v", err)
	}
	if err := svc.AddTeamMember(ctx, teamWrite.ID, multiTeam.ID, "member"); err != nil {
		t.Fatalf("add multi-team member write: %v", err)
	}
	if err := svc.DB.Create(&db.TeamRepository{TeamID: teamWrite.ID, RepositoryID: repo.ID, Permission: "write"}).Error; err != nil {
		t.Fatalf("grant team write: %v", err)
	}

	if err := svc.AddTeamMember(ctx, teamAdmin.ID, collabOverride.ID, "member"); err != nil {
		t.Fatalf("add collab override member: %v", err)
	}
	if err := svc.DB.Create(&db.TeamRepository{TeamID: teamAdmin.ID, RepositoryID: repo.ID, Permission: "admin"}).Error; err != nil {
		t.Fatalf("grant team admin: %v", err)
	}

	tests := []struct {
		name string
		user uint
		want service.RepoPermission
	}{
		{"site admin", admin.ID, service.RepoPermissionAdmin},
		{"repo owner", owner.ID, service.RepoPermissionAdmin},
		{"collaborator write", collab.ID, service.RepoPermissionWrite},
		{"team read", teamUser.ID, service.RepoPermissionRead},
		{"team max", multiTeam.ID, service.RepoPermissionWrite},
		{"highest grant wins", collabOverride.ID, service.RepoPermissionAdmin},
		{"outsider", outsider.ID, service.RepoPermissionNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			perm, err := svc.HasRepoAccess(ctx, repo.ID, tt.user)
			if err != nil {
				t.Fatalf("HasRepoAccess: %v", err)
			}
			if perm != tt.want {
				t.Fatalf("expected %v, got %v", tt.want, perm)
			}
		})
	}
}

func TestHasRepoAccess_OrganizationBasePermission(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	org := db.User{
		Login:                       "base-org",
		Name:                        "base-org",
		Type:                        db.TypeOrganization,
		DefaultRepositoryPermission: "triage",
	}
	member := db.User{Login: "base-member", Name: "base-member", Type: db.TypeUser}
	for _, u := range []*db.User{&org, &member} {
		if err := svc.DB.Create(u).Error; err != nil {
			t.Fatalf("create user %s: %v", u.Login, err)
		}
	}

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: org.Login,
		Name:       "base-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	if err := svc.AddOrgMember(ctx, org.ID, member.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("add org member: %v", err)
	}

	perm, err := svc.HasRepoAccess(ctx, repo.ID, member.ID)
	if err != nil {
		t.Fatalf("HasRepoAccess base: %v", err)
	}
	if perm != service.RepoPermissionRead {
		t.Fatalf("expected org base triage alias to resolve to read, got %v", perm)
	}

	if err := svc.AddCollaborator(ctx, repo.ID, member.ID, "read"); err != nil {
		t.Fatalf("add collaborator: %v", err)
	}
	perm, err = svc.HasRepoAccess(ctx, repo.ID, member.ID)
	if err != nil {
		t.Fatalf("HasRepoAccess with read collaborator: %v", err)
	}
	if perm != service.RepoPermissionRead {
		t.Fatalf("expected org base triage alias to stay at read, got %v", perm)
	}

	team, err := svc.CreateTeam(ctx, org.ID, "maintainers", "maintainers", "", "")
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	if err := svc.AddTeamMember(ctx, team.ID, member.ID, "member"); err != nil {
		t.Fatalf("add team member: %v", err)
	}
	if err := svc.AddTeamRepo(ctx, team.ID, repo.ID, "maintain"); err != nil {
		t.Fatalf("add team repo: %v", err)
	}

	perm, err = svc.HasRepoAccess(ctx, repo.ID, member.ID)
	if err != nil {
		t.Fatalf("HasRepoAccess with maintain team: %v", err)
	}
	if perm != service.RepoPermissionWrite {
		t.Fatalf("expected maintain alias to resolve to write, got %v", perm)
	}
}
