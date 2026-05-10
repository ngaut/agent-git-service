package service_test

import (
	"context"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"

	"github.com/stretchr/testify/assert"
)

func TestTeamsCRUD(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// 1. Setup Org
	orgLogin := "justice-league-org"
	org, err := svc.EnsureOrg(ctx, orgLogin)
	assert.NoError(t, err)

	// 2. Setup Users
	user1 := db.User{Login: "batman-usr"}
	svc.DB.Create(&user1)
	user2 := db.User{Login: "superman-usr"}
	svc.DB.Create(&user2)

	// 3. Create Team
	teamName := "Core Founders"
	teamSlug := "core-founders"
	team, err := svc.CreateTeam(ctx, org.ID, teamName, teamSlug, "The inner circle", "")
	assert.NoError(t, err)
	assert.Equal(t, teamName, team.Name)
	assert.Equal(t, db.TeamPrivacyClosed, team.Privacy)

	mustAddOrgMember(t, svc, org.ID, user1.ID, db.OrganizationRoleMember)
	mustAddOrgMember(t, svc, org.ID, user2.ID, db.OrganizationRoleMember)

	// 4. Retrieve Team by Slug
	fetched, err := svc.GetTeam(ctx, org.ID, teamSlug)
	assert.NoError(t, err)
	assert.Equal(t, team.ID, fetched.ID)

	// 5. Add Members with roles
	err = svc.AddTeamMember(ctx, team.ID, user1.ID, "member")
	assert.NoError(t, err)
	err = svc.AddTeamMember(ctx, team.ID, user2.ID, "maintainer")
	assert.NoError(t, err)

	// 6. Verify membership roles
	member1, err := svc.GetTeamMember(ctx, team.ID, user1.ID)
	assert.NoError(t, err)
	assert.Equal(t, "member", member1.Role)

	member2, err := svc.GetTeamMember(ctx, team.ID, user2.ID)
	assert.NoError(t, err)
	assert.Equal(t, "maintainer", member2.Role)

	// 7. Update role
	err = svc.AddTeamMember(ctx, team.ID, user1.ID, "maintainer")
	assert.NoError(t, err)
	member1, err = svc.GetTeamMember(ctx, team.ID, user1.ID)
	assert.NoError(t, err)
	assert.Equal(t, "maintainer", member1.Role)

	// 8. List Members
	members, err := svc.ListTeamMembers(ctx, team.ID)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(members))

	// 9. Remove Member
	err = svc.RemoveTeamMember(ctx, team.ID, user1.ID)
	assert.NoError(t, err)
	members, _ = svc.ListTeamMembers(ctx, team.ID)
	assert.Equal(t, 1, len(members))
	assert.Equal(t, user2.ID, members[0].ID)
	_, err = svc.GetOrgMember(ctx, org.ID, user1.ID)
	assert.NoError(t, err)

	// 10. Add repo to team
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: org.Login, Name: "team-repo"})
	assert.NoError(t, err)
	err = svc.AddTeamRepo(ctx, team.ID, repo.ID, "write")
	assert.NoError(t, err)

	teamRepos, err := svc.ListTeamRepos(ctx, team.ID)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(teamRepos))
	assert.Equal(t, repo.ID, teamRepos[0].RepositoryID)
	assert.Equal(t, "write", teamRepos[0].Permission)

	// 11. Delete Team (cascades team_members and team_repositories)
	err = svc.DeleteTeam(ctx, team.ID)
	assert.NoError(t, err)

	var memberCount int64
	svc.DB.Model(&db.TeamMember{}).Where("team_id = ?", team.ID).Count(&memberCount)
	assert.Equal(t, int64(0), memberCount)

	var repoCount int64
	svc.DB.Model(&db.TeamRepository{}).Where("team_id = ?", team.ID).Count(&repoCount)
	assert.Equal(t, int64(0), repoCount)

	_, err = svc.GetOrgMember(ctx, org.ID, user2.ID)
	assert.NoError(t, err)

	_, err = svc.GetTeam(ctx, org.ID, teamSlug)
	assert.Error(t, err, "expected team to be deleted")
}

func TestCreateTeam_CollapsesRequestedPrivacyToClosed(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	org, err := svc.EnsureOrg(ctx, "privacy-org")
	assert.NoError(t, err)

	team, err := svc.CreateTeam(ctx, org.ID, "Security", "security", "Restricted team", "secret")
	assert.NoError(t, err)
	assert.Equal(t, db.TeamPrivacyClosed, team.Privacy)

	fetched, err := svc.GetTeam(ctx, org.ID, "security")
	assert.NoError(t, err)
	assert.Equal(t, db.TeamPrivacyClosed, fetched.Privacy)
}

func TestIsOrgAdmin(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	org, err := svc.EnsureOrg(ctx, "org-admins")
	assert.NoError(t, err)

	adminTeam, err := svc.CreateTeam(ctx, org.ID, "admins", "admins", "", "")
	assert.NoError(t, err)
	memberTeam, err := svc.CreateTeam(ctx, org.ID, "members", "members", "", "")
	assert.NoError(t, err)

	siteAdmin := db.User{Login: "site-admin", Type: db.TypeUser, SiteAdmin: true}
	owner := db.User{Login: "org-owner", Type: db.TypeUser}
	teamMaintainer := db.User{Login: "org-team-maintainer", Type: db.TypeUser}
	member := db.User{Login: "org-member", Type: db.TypeUser}
	outsider := db.User{Login: "org-outsider", Type: db.TypeUser}
	for _, user := range []*db.User{&siteAdmin, &owner, &teamMaintainer, &member, &outsider} {
		assert.NoError(t, svc.DB.Create(user).Error)
	}

	mustAddOrgMember(t, svc, org.ID, owner.ID, db.OrganizationRoleOwner)
	mustAddOrgMember(t, svc, org.ID, teamMaintainer.ID, db.OrganizationRoleMember)
	mustAddOrgMember(t, svc, org.ID, member.ID, db.OrganizationRoleMember)

	assert.NoError(t, svc.AddTeamMember(ctx, adminTeam.ID, teamMaintainer.ID, "maintainer"))
	assert.NoError(t, svc.AddTeamMember(ctx, memberTeam.ID, member.ID, "member"))

	tests := []struct {
		name   string
		userID uint
		want   bool
	}{
		{name: "site admin", userID: siteAdmin.ID, want: true},
		{name: "org owner", userID: owner.ID, want: true},
		{name: "team maintainer", userID: teamMaintainer.ID, want: false},
		{name: "org member", userID: member.ID, want: false},
		{name: "org outsider", userID: outsider.ID, want: false},
		{name: "zero user", userID: 0, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.IsOrgAdmin(ctx, org.ID, tt.userID)
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAddTeamMember_RequiresOrgMembership(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	org, err := svc.EnsureOrg(ctx, "team-membership-org")
	assert.NoError(t, err)

	user := db.User{Login: "team-user", Type: db.TypeUser}
	assert.NoError(t, svc.DB.Create(&user).Error)

	team, err := svc.CreateTeam(ctx, org.ID, "platform", "platform", "", "")
	assert.NoError(t, err)

	err = svc.AddTeamMember(ctx, team.ID, user.ID, "member")
	assert.ErrorIs(t, err, service.ErrForbidden)

	mustAddOrgMember(t, svc, org.ID, user.ID, db.OrganizationRoleMember)
	assert.NoError(t, svc.AddTeamMember(ctx, team.ID, user.ID, "member"))

	member, err := svc.GetTeamMember(ctx, team.ID, user.ID)
	assert.NoError(t, err)
	assert.Equal(t, "member", member.Role)
}

func TestNormalizeSlug(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple name", "Core", "core"},
		{"name with spaces", "Test Team", "test-team"},
		{"name with multiple spaces", "Test  Team   123", "test-team-123"},
		{"name with special chars", "Test@Team#123", "testteam123"},
		{"name with leading/trailing spaces", "  Test Team  ", "test-team"},
		{"name already normalized", "test-team", "test-team"},
		{"name with hyphens", "test--team", "test-team"},
		{"name with leading hyphens", "--test-team", "test-team"},
		{"name with trailing hyphens", "test-team--", "test-team"},
		{"mixed case", "TestTeam", "testteam"},
		{"numbers", "Team123", "team123"},
		{"empty string", "", ""},
		{"only spaces", "   ", ""},
		{"only special chars", "@#$", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := service.NormalizeSlug(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCreateTeam_NormalizesSlug(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	org, err := svc.EnsureOrg(ctx, "test-org")
	assert.NoError(t, err)

	// Create team with spaced name
	team, err := svc.CreateTeam(ctx, org.ID, "Test Team 123", "", "Description", "")
	assert.NoError(t, err)
	assert.Equal(t, "Test Team 123", team.Name)
	assert.Equal(t, "test-team-123", team.Slug)
}

func TestCreateTeam_DuplicateReturnsConflict(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	org, err := svc.EnsureOrg(ctx, "duplicate-team-org")
	assert.NoError(t, err)

	_, err = svc.CreateTeam(ctx, org.ID, "Platform", "platform", "", "")
	assert.NoError(t, err)

	_, err = svc.CreateTeam(ctx, org.ID, "Platform", "platform", "", "")
	assert.ErrorIs(t, err, service.ErrConflict)
	assert.EqualError(t, err, `conflict: team "Platform" already exists in this organization`)
}

func TestUpdateTeam_UpdatesSlugOnRename(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	org, err := svc.EnsureOrg(ctx, "test-org-rename")
	assert.NoError(t, err)

	// Create team
	team, err := svc.CreateTeam(ctx, org.ID, "Initial Team", "initial-team", "Description", "")
	assert.NoError(t, err)
	assert.Equal(t, "initial-team", team.Slug)

	// Update team name - the service layer should update slug when name changes
	team.Name = "Renamed Team New"
	err = svc.UpdateTeam(ctx, &team)
	assert.NoError(t, err)

	// Fetch updated team by ID to verify the update
	updatedByID, err := svc.GetTeamByID(ctx, team.ID)
	assert.NoError(t, err)
	assert.Equal(t, "Renamed Team New", updatedByID.Name)
	assert.Equal(t, "renamed-team-new", updatedByID.Slug)

	// Verify we can fetch by new slug
	updated, err := svc.GetTeam(ctx, org.ID, "renamed-team-new")
	assert.NoError(t, err)
	assert.Equal(t, "Renamed Team New", updated.Name)
	assert.Equal(t, "renamed-team-new", updated.Slug)

	// Verify old slug no longer works
	_, err = svc.GetTeam(ctx, org.ID, "initial-team")
	assert.Error(t, err, "expected error when fetching by old slug")
}

func TestUpdateTeam_RenameToExistingSlugReturnsConflict(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	org, err := svc.EnsureOrg(ctx, "rename-conflict-org")
	assert.NoError(t, err)

	_, err = svc.CreateTeam(ctx, org.ID, "Platform", "platform", "", "")
	assert.NoError(t, err)

	team, err := svc.CreateTeam(ctx, org.ID, "Operations", "operations", "", "")
	assert.NoError(t, err)

	team.Name = "Platform"
	err = svc.UpdateTeam(ctx, &team)
	assert.ErrorIs(t, err, service.ErrConflict)
	assert.EqualError(t, err, `conflict: team "Platform" already exists in this organization`)
}
