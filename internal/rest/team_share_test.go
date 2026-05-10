package rest_test

import (
	"context"
	"net/http"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
	"gh-server/internal/testharness"
)

func TestTeamShare_UserReposIncludesSharedRepoWithEffectivePermissions(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	org, err := h.Svc.EnsureOrg(ctx, "team-share-org")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}

	member, memberToken := seedHarnessUser(t, h, "team-member", false)
	team, err := h.Svc.CreateTeam(ctx, org.ID, "shared-team", "shared-team", "", "")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if err := h.Svc.AddOrgMember(ctx, org.ID, member.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}
	if err := h.Svc.AddTeamMember(ctx, team.ID, member.ID, "member"); err != nil {
		t.Fatalf("AddTeamMember: %v", err)
	}

	repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: org.Login,
		Name:       "shared-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if err := h.Svc.AddTeamRepo(ctx, team.ID, repo.ID, "pull"); err != nil {
		t.Fatalf("AddTeamRepo: %v", err)
	}

	w := h.DoRESTWithToken(t, "GET", "/api/v3/user/repos", memberToken)
	if w.Code != 200 {
		t.Fatalf("GET /api/v3/user/repos = %d: %s", w.Code, w.Body.String())
	}
	repos := testharness.DecodeJSONArray(t, w)
	if len(repos) != 1 {
		t.Fatalf("expected 1 visible repo, got %d", len(repos))
	}
	if repos[0]["full_name"] != repo.FullName {
		t.Fatalf("full_name = %v, want %s", repos[0]["full_name"], repo.FullName)
	}
	perms, ok := repos[0]["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("permissions missing or invalid: %#v", repos[0]["permissions"])
	}
	if perms["pull"] != true || perms["triage"] != false || perms["push"] != false || perms["maintain"] != false || perms["admin"] != false {
		t.Fatalf("unexpected shared repo permissions: %#v", perms)
	}

	w = h.DoRESTWithToken(t, "GET", "/api/v3/users/team-member/repos", memberToken)
	if w.Code != 200 {
		t.Fatalf("GET /api/v3/users/team-member/repos = %d: %s", w.Code, w.Body.String())
	}
	if repos := testharness.DecodeJSONArray(t, w); len(repos) != 0 {
		t.Fatalf("team-shared repo should not appear in /users/{username}/repos: %#v", repos)
	}

	w = h.DoRESTWithToken(t, "GET", "/api/v3/repos/team-share-org/shared-repo", memberToken)
	if w.Code != 200 {
		t.Fatalf("GET /api/v3/repos/team-share-org/shared-repo = %d: %s", w.Code, w.Body.String())
	}
	repoBody := testharness.DecodeJSON(t, w)
	repoPerms, ok := repoBody["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("repo permissions missing or invalid: %#v", repoBody["permissions"])
	}
	if repoPerms["pull"] != true || repoPerms["triage"] != false || repoPerms["push"] != false || repoPerms["maintain"] != false || repoPerms["admin"] != false {
		t.Fatalf("unexpected repo permissions: %#v", repoPerms)
	}

	if err := h.Svc.RemoveTeamRepo(ctx, team.ID, repo.ID); err != nil {
		t.Fatalf("RemoveTeamRepo: %v", err)
	}
	w = h.DoRESTWithToken(t, "GET", "/api/v3/user/repos", memberToken)
	if w.Code != 200 {
		t.Fatalf("GET /api/v3/user/repos after revoke = %d: %s", w.Code, w.Body.String())
	}
	if repos := testharness.DecodeJSONArray(t, w); len(repos) != 0 {
		t.Fatalf("shared repo should disappear after revoke: %#v", repos)
	}
}

func TestTeamShare_TeamRepoMutationsRequireRepoAdmin(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	org, err := h.Svc.EnsureOrg(ctx, "team-admin-org")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}

	adminUser, adminToken := seedHarnessUser(t, h, "repo-admin", false)
	_, outsiderToken := seedHarnessUser(t, h, "repo-outsider", false)

	_, err = h.Svc.CreateTeam(ctx, org.ID, "ops", "ops", "", "")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: org.Login,
		Name:       "org-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if err := h.Svc.AddCollaborator(ctx, repo.ID, adminUser.ID, "admin"); err != nil {
		t.Fatalf("AddCollaborator admin: %v", err)
	}

	w := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/team-admin-org/teams/ops/repos/team-admin-org/org-repo", outsiderToken, map[string]any{
		"permission": "push",
	})
	if w.Code != 404 {
		t.Fatalf("outsider PUT expected 404, got %d: %s", w.Code, w.Body.String())
	}

	w = h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/team-admin-org/teams/ops/repos/team-admin-org/org-repo", adminToken, map[string]any{
		"permission": "push",
	})
	if w.Code != 204 {
		t.Fatalf("repo admin PUT expected 204, got %d: %s", w.Code, w.Body.String())
	}

	w = h.DoRESTWithToken(t, "GET", "/api/v3/orgs/team-admin-org/teams/ops/repos", adminToken)
	if w.Code != 200 {
		t.Fatalf("GET team repos = %d: %s", w.Code, w.Body.String())
	}
	repos := testharness.DecodeJSONArray(t, w)
	if len(repos) != 1 {
		t.Fatalf("expected team repo to be linked once, got %d", len(repos))
	}

	w = h.DoRESTWithToken(t, "DELETE", "/api/v3/orgs/team-admin-org/teams/ops/repos/team-admin-org/org-repo", outsiderToken)
	if w.Code != 404 {
		t.Fatalf("outsider DELETE expected 404, got %d: %s", w.Code, w.Body.String())
	}

	w = h.DoRESTWithToken(t, "DELETE", "/api/v3/orgs/team-admin-org/teams/ops/repos/team-admin-org/org-repo", adminToken)
	if w.Code != 204 {
		t.Fatalf("repo admin DELETE expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestTeamShare_EnableRepoSharingRequiresExistingOrgMembership(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	org, err := h.Svc.EnsureOrg(ctx, "team-enable-org")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}

	adminUser, adminToken := seedHarnessUser(t, h, "sharing-repo-admin", false)
	_, outsiderToken := seedHarnessUser(t, h, "sharing-outsider", false)

	repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: org.Login,
		Name:       "cedar-pebble",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if err := h.Svc.AddCollaborator(ctx, repo.ID, adminUser.ID, "admin"); err != nil {
		t.Fatalf("AddCollaborator admin: %v", err)
	}

	w := h.DoRESTWithToken(t, "POST", "/api/v3/repos/team-enable-org/cedar-pebble/team-sharing/enable", outsiderToken)
	if w.Code != http.StatusNotFound {
		t.Fatalf("outsider POST expected 404, got %d: %s", w.Code, w.Body.String())
	}

	w = h.DoRESTWithToken(t, "POST", "/api/v3/repos/team-enable-org/cedar-pebble/team-sharing/enable", adminToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("repo admin without org membership POST expected 403, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := h.Svc.GetOrgMember(ctx, org.ID, adminUser.ID); err == nil {
		t.Fatal("repo admin should not be auto-added to the org by team-sharing enable")
	}

	var teamCount int64
	if err := h.DB.Model(&db.Team{}).Where("organization_id = ?", org.ID).Count(&teamCount).Error; err != nil {
		t.Fatalf("count teams after forbidden enable: %v", err)
	}
	if teamCount != 0 {
		t.Fatalf("team count after forbidden enable = %d, want 0", teamCount)
	}

	if err := h.Svc.AddOrgMember(ctx, org.ID, adminUser.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember adminUser: %v", err)
	}

	w = h.DoRESTWithToken(t, "POST", "/api/v3/repos/team-enable-org/cedar-pebble/team-sharing/enable", adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("repo admin with org membership POST expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := testharness.DecodeJSON(t, w)
	if body["slug"] != "cedar-pebble-share" {
		t.Fatalf("team slug = %v, want cedar-pebble-share", body["slug"])
	}

	team, err := h.Svc.GetTeam(ctx, org.ID, "cedar-pebble-share")
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	member, err := h.Svc.GetTeamMember(ctx, team.ID, adminUser.ID)
	if err != nil {
		t.Fatalf("GetTeamMember: %v", err)
	}
	if member.Role != "maintainer" {
		t.Fatalf("team member role = %q, want maintainer", member.Role)
	}
	orgMember, err := h.Svc.GetOrgMember(ctx, org.ID, adminUser.ID)
	if err != nil {
		t.Fatalf("GetOrgMember: %v", err)
	}
	if orgMember.Role != db.OrganizationRoleMember {
		t.Fatalf("org member role = %q, want member", orgMember.Role)
	}

	var teamRepo db.TeamRepository
	if err := h.DB.Where("team_id = ? AND repository_id = ?", team.ID, repo.ID).First(&teamRepo).Error; err != nil {
		t.Fatalf("fetch team repo link: %v", err)
	}
	if teamRepo.Permission != "read" {
		t.Fatalf("team repo permission = %q, want read", teamRepo.Permission)
	}

	w = h.DoRESTWithToken(t, "POST", "/api/v3/repos/team-enable-org/cedar-pebble/team-sharing/enable", adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("second repo admin POST expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if err := h.DB.Model(&db.Team{}).Where("organization_id = ?", org.ID).Count(&teamCount).Error; err != nil {
		t.Fatalf("count teams: %v", err)
	}
	if teamCount != 1 {
		t.Fatalf("team count = %d, want 1", teamCount)
	}

	var memberCount int64
	if err := h.DB.Model(&db.TeamMember{}).Where("team_id = ? AND user_id = ?", team.ID, adminUser.ID).Count(&memberCount).Error; err != nil {
		t.Fatalf("count team members: %v", err)
	}
	if memberCount != 1 {
		t.Fatalf("membership count = %d, want 1", memberCount)
	}

	var repoCount int64
	if err := h.DB.Model(&db.TeamRepository{}).Where("team_id = ? AND repository_id = ?", team.ID, repo.ID).Count(&repoCount).Error; err != nil {
		t.Fatalf("count team repos: %v", err)
	}
	if repoCount != 1 {
		t.Fatalf("team repo count = %d, want 1", repoCount)
	}
}

func TestTeamShare_TeamRepoMutationRequiresOrgOwnedRepo(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	org, err := h.Svc.EnsureOrg(ctx, "team-org-only")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}
	_, err = h.Svc.CreateTeam(ctx, org.ID, "ops", "ops", "", "")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	_, ownerToken := seedHarnessUser(t, h, "solo-owner", false)
	repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "solo-owner",
		Name:       "personal-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	w := h.DoRESTJSONWithToken(t, "PUT", "/api/v3/orgs/team-org-only/teams/ops/repos/solo-owner/personal-repo", ownerToken, map[string]any{
		"permission": "pull",
	})
	if w.Code != 404 {
		t.Fatalf("user-owned repo should be rejected, got %d: %s", w.Code, w.Body.String())
	}

	var count int64
	if err := h.DB.Model(&db.TeamRepository{}).Where("repository_id = ?", repo.ID).Count(&count).Error; err != nil {
		t.Fatalf("count team repositories: %v", err)
	}
	if count != 0 {
		t.Fatalf("user-owned repo should not have team links, found %d", count)
	}
}

func TestTeamShare_ListOrgTeamsIncludesCounts(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	org, err := h.Svc.EnsureOrg(ctx, "team-counts-org")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}
	memberA, _ := seedHarnessUser(t, h, "counts-a", false)
	memberB, _ := seedHarnessUser(t, h, "counts-b", false)
	team, err := h.Svc.CreateTeam(ctx, org.ID, "analytics", "analytics", "team with counts", "")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if err := h.Svc.AddOrgMember(ctx, org.ID, memberA.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember A: %v", err)
	}
	if err := h.Svc.AddOrgMember(ctx, org.ID, memberB.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember B: %v", err)
	}
	if err := h.Svc.AddTeamMember(ctx, team.ID, memberA.ID, "member"); err != nil {
		t.Fatalf("AddTeamMember A: %v", err)
	}
	if err := h.Svc.AddTeamMember(ctx, team.ID, memberB.ID, "maintainer"); err != nil {
		t.Fatalf("AddTeamMember B: %v", err)
	}
	repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: org.Login,
		Name:       "counts-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if err := h.Svc.AddTeamRepo(ctx, team.ID, repo.ID, "push"); err != nil {
		t.Fatalf("AddTeamRepo: %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/orgs/team-counts-org/teams", nil)
	if w.Code != 200 {
		t.Fatalf("GET /api/v3/orgs/team-counts-org/teams = %d: %s", w.Code, w.Body.String())
	}
	teams := testharness.DecodeJSONArray(t, w)
	if len(teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(teams))
	}
	if teams[0]["members_count"] != float64(2) {
		t.Fatalf("members_count = %v, want 2", teams[0]["members_count"])
	}
	if teams[0]["repos_count"] != float64(1) {
		t.Fatalf("repos_count = %v, want 1", teams[0]["repos_count"])
	}
}

func TestTeamShare_ListTeamReposCanonicalizesCompatibilityRole(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	org, err := h.Svc.EnsureOrg(ctx, "team-maintain-org")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}
	team, err := h.Svc.CreateTeam(ctx, org.ID, "platform", "platform", "", "")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: org.Login,
		Name:       "maintain-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if err := h.Svc.AddTeamRepo(ctx, team.ID, repo.ID, "maintain"); err != nil {
		t.Fatalf("AddTeamRepo: %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/orgs/team-maintain-org/teams/platform/repos", nil)
	if w.Code != 200 {
		t.Fatalf("GET /api/v3/orgs/team-maintain-org/teams/platform/repos = %d: %s", w.Code, w.Body.String())
	}
	repos := testharness.DecodeJSONArray(t, w)
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(repos))
	}
	if repos[0]["role_name"] != "write" {
		t.Fatalf("role_name = %v, want write", repos[0]["role_name"])
	}
	perms, ok := repos[0]["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("permissions missing or invalid: %#v", repos[0]["permissions"])
	}
	if perms["pull"] != true || perms["triage"] != false || perms["push"] != true || perms["maintain"] != false || perms["admin"] != false {
		t.Fatalf("unexpected canonicalized permissions: %#v", perms)
	}
}

func seedHarnessUser(t *testing.T, h *testharness.Harness, login string, siteAdmin bool) (db.User, string) {
	t.Helper()

	user := db.User{Login: login, Name: login, Type: db.TypeUser, SiteAdmin: siteAdmin}
	if err := h.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user %s: %v", login, err)
	}

	token := login + "-token"
	if err := h.DB.Create(&db.Token{UserID: user.ID, Value: token}).Error; err != nil {
		t.Fatalf("create token for %s: %v", login, err)
	}

	return user, token
}
