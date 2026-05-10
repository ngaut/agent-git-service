package service_test

import (
	"context"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

// TestGetCurrentUser_MixedContextStates verifies context handling and admin fallback.
func TestGetCurrentUser_MixedContextStates(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	adminUser := db.User{
		Login:     "admin",
		Name:      "Admin User",
		Type:      db.TypeUser,
		SiteAdmin: true,
	}
	if err := svc.DB.Create(&adminUser).Error; err != nil {
		t.Fatalf("failed to create admin user: %v", err)
	}

	regularUser := db.User{
		Login: "regular",
		Name:  "Regular User",
		Type:  db.TypeUser,
	}
	if err := svc.DB.Create(&regularUser).Error; err != nil {
		t.Fatalf("failed to create regular user: %v", err)
	}

	t.Run("with valid user in context returns that user", func(t *testing.T) {
		userCtx := service.ContextWithUser(ctx, regularUser)
		user, err := svc.GetCurrentUser(userCtx)
		if err != nil {
			t.Errorf("expected success with valid user in context, got: %v", err)
		}
		if user.Login != regularUser.Login {
			t.Errorf("expected login %q, got %q", regularUser.Login, user.Login)
		}
	})

	t.Run("with no user in context falls back to first admin", func(t *testing.T) {
		user, err := svc.GetCurrentUser(ctx)
		if err != nil {
			t.Errorf("expected success with fallback to admin, got: %v", err)
		}
		if user.Login != adminUser.Login {
			t.Errorf("expected fallback to admin login %q, got %q", adminUser.Login, user.Login)
		}
		if !user.SiteAdmin {
			t.Error("expected fallback user to be site admin")
		}
	})
}

func TestEnsureOrg_CreatesAdminsBootstrapForBoundAgent(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	human := db.User{
		Login:    "bound-human",
		Name:     "Bound Human",
		Type:     db.TypeUser,
		UserKind: db.UserKindHuman,
	}
	agent := db.User{
		Login:    "bound-agent",
		Name:     "Bound Agent",
		Type:     db.TypeUser,
		UserKind: db.UserKindAgent,
	}
	for _, user := range []*db.User{&human, &agent} {
		if err := svc.DB.Create(user).Error; err != nil {
			t.Fatalf("create user %s: %v", user.Login, err)
		}
	}
	if err := svc.DB.Create(&db.AgentBinding{
		HumanUserID: human.ID,
		AgentUserID: agent.ID,
	}).Error; err != nil {
		t.Fatalf("create binding: %v", err)
	}

	org, err := svc.EnsureOrg(service.ContextWithUser(ctx, agent), "agent-owned-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}

	for _, user := range []db.User{human, agent} {
		member, err := svc.GetOrgMember(ctx, org.ID, user.ID)
		if err != nil {
			t.Fatalf("GetOrgMember(%s) failed: %v", user.Login, err)
		}
		if member.Role != db.OrganizationRoleOwner {
			t.Fatalf("%s role = %q, want %q", user.Login, member.Role, db.OrganizationRoleOwner)
		}
	}

	var adminsTeam db.Team
	if err := svc.DB.First(&adminsTeam, "organization_id = ? AND slug = ?", org.ID, "admins").Error; err != nil {
		t.Fatalf("load admins team: %v", err)
	}
	for _, user := range []db.User{human, agent} {
		var count int64
		if err := svc.DB.Model(&db.TeamMember{}).
			Where("team_id = ? AND user_id = ?", adminsTeam.ID, user.ID).
			Count(&count).Error; err != nil {
			t.Fatalf("count admins membership for %s: %v", user.Login, err)
		}
		if count != 1 {
			t.Fatalf("expected admins team membership for %s", user.Login)
		}
	}
}

func TestListOrgs_UsesExplicitOrganizationMembership(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	normalUser := db.User{
		Login: "dave",
		Name:  "Dave",
		Type:  db.TypeUser,
	}
	if err := svc.DB.Create(&normalUser).Error; err != nil {
		t.Fatalf("failed to create normal user: %v", err)
	}

	org1 := db.User{Login: "org1", Name: "Organization 1", Type: db.TypeOrganization}
	org2 := db.User{Login: "org2", Name: "Organization 2", Type: db.TypeOrganization}
	org3 := db.User{Login: "org3", Name: "Organization 3", Type: db.TypeOrganization}
	for _, org := range []*db.User{&org1, &org2, &org3} {
		if err := svc.DB.Create(org).Error; err != nil {
			t.Fatalf("failed to create org %s: %v", org.Login, err)
		}
	}

	team1 := db.Team{OrganizationID: org1.ID, Name: "team1", Slug: "team1", Privacy: db.TeamPrivacyClosed}
	team3 := db.Team{OrganizationID: org3.ID, Name: "team3", Slug: "team3", Privacy: db.TeamPrivacyClosed}
	for _, team := range []*db.Team{&team1, &team3} {
		if err := svc.DB.Create(team).Error; err != nil {
			t.Fatalf("failed to create team %s: %v", team.Name, err)
		}
	}

	if err := svc.AddOrgMember(ctx, org1.ID, normalUser.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("failed to add org1 membership: %v", err)
	}
	if err := svc.AddOrgMember(ctx, org2.ID, normalUser.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("failed to add org2 membership: %v", err)
	}
	if err := svc.DB.Create(&db.TeamMember{TeamID: team3.ID, UserID: normalUser.ID, Role: "member"}).Error; err != nil {
		t.Fatalf("failed to create legacy team member: %v", err)
	}
	if err := svc.AddTeamMember(ctx, team1.ID, normalUser.ID, "member"); err != nil {
		t.Fatalf("failed to add team1 member: %v", err)
	}

	t.Run("normal user can list only their orgs", func(t *testing.T) {
		normalCtx := service.ContextWithUser(ctx, normalUser)
		orgs, err := svc.ListOrgs(normalCtx)
		if err != nil {
			t.Errorf("expected success for normal user listing orgs, got: %v", err)
		}
		if len(orgs) != 2 {
			t.Errorf("expected 2 orgs (org1 and org2), got %d: %v", len(orgs), orgs)
		}
		seen := map[string]bool{}
		for _, o := range orgs {
			seen[o.Login] = true
			if o.Login == "org3" {
				t.Errorf("org3 should not be in the list for normalUser")
			}
		}
		if !seen["org1"] || !seen["org2"] {
			t.Errorf("expected org1 and org2 in result, got %#v", seen)
		}
	})

	t.Run("no context user gets empty list", func(t *testing.T) {
		orgs, err := svc.ListOrgs(ctx)
		if err != nil {
			t.Errorf("expected success for no-context user listing orgs, got: %v", err)
		}
		if len(orgs) != 0 {
			t.Errorf("expected 0 orgs for no-context user, got %d", len(orgs))
		}
	})
}

func TestListAllUsers_ReturnsNonOrgUsers(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	human := db.User{
		Login:    "eve",
		Name:     "Eve",
		Type:     db.TypeUser,
		UserKind: db.UserKindHuman,
	}
	agent := db.User{
		Login:    "eve-agent",
		Name:     "Eve Agent",
		Type:     db.TypeUser,
		UserKind: db.UserKindAgent,
	}
	org := db.User{Login: "acme", Name: "Acme", Type: db.TypeOrganization}
	for _, user := range []*db.User{&human, &agent, &org} {
		if err := svc.DB.Create(user).Error; err != nil {
			t.Fatalf("create user %s: %v", user.Login, err)
		}
	}

	users, err := svc.ListAllUsers(ctx)
	if err != nil {
		t.Fatalf("ListAllUsers failed: %v", err)
	}

	seen := map[string]bool{}
	for _, user := range users {
		seen[user.Login] = true
		if user.Type != db.TypeUser {
			t.Fatalf("expected only user accounts, got %s", user.Type)
		}
	}
	if !seen[human.Login] || !seen[agent.Login] {
		t.Fatalf("expected both human and agent accounts in result, got %#v", seen)
	}
	if seen[org.Login] {
		t.Fatalf("organization should not be returned by ListAllUsers")
	}
}

// TestUserFromContext_EdgeCases exercises basic context extraction behavior.
func TestUserFromContext_EdgeCases(t *testing.T) {
	ctx := context.Background()

	t.Run("nil context value returns user with ok=true", func(t *testing.T) {
		zeroUser := db.User{}
		ctxWithZero := service.ContextWithUser(ctx, zeroUser)
		user, ok := service.UserFromContext(ctxWithZero)
		if !ok {
			t.Error("expected ok=true for zero-value user in context")
		}
		if user.Login != "" {
			t.Errorf("expected empty login for zero-value user, got %q", user.Login)
		}
	})

	t.Run("empty context returns ok=false", func(t *testing.T) {
		_, ok := service.UserFromContext(context.Background())
		if ok {
			t.Error("expected ok=false for empty context")
		}
	})

	t.Run("context with wrong type returns ok=false", func(t *testing.T) {
		wrongCtx := context.WithValue(ctx, "wrong-key", "wrong-value")
		_, ok := service.UserFromContext(wrongCtx)
		if ok {
			t.Error("expected ok=false for wrong type in context")
		}
	})
}
