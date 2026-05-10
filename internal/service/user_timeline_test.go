package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

func testEnsureOrg(t *testing.T) {
	t.Helper()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	org, err := svc.EnsureOrg(ctx, "acme")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	if org.Login != "acme" {
		t.Errorf("expected org login 'acme', got %q", org.Login)
	}
	if org.Type != db.TypeOrganization {
		t.Errorf("expected org type %q, got %q", db.TypeOrganization, org.Type)
	}
	if org.ID == 0 {
		t.Errorf("expected org ID to be set")
	}

	org2, err := svc.EnsureOrg(ctx, "acme")
	if err != nil {
		t.Fatalf("EnsureOrg second call failed: %v", err)
	}
	if org2.ID != org.ID {
		t.Errorf("expected same org ID on second call, got %d and %d", org.ID, org2.ID)
	}

	var count int64
	if err := svc.DB.Model(&db.User{}).Where("login = ?", "acme").Count(&count).Error; err != nil {
		t.Fatalf("count orgs failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 org row, got %d", count)
	}
}

func TestEnsureOrg(t *testing.T) {
	testEnsureOrg(t)
}

func TestEnsureOrg_CreatesOwnerMembershipForCreator(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	creator := db.User{Login: "org-creator", Name: "Org Creator", Type: db.TypeUser}
	if err := svc.DB.Create(&creator).Error; err != nil {
		t.Fatalf("create creator failed: %v", err)
	}

	org, err := svc.EnsureOrg(service.ContextWithUser(ctx, creator), "creator-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}

	member, err := svc.GetOrgMember(ctx, org.ID, creator.ID)
	if err != nil {
		t.Fatalf("GetOrgMember failed: %v", err)
	}
	if member.Role != db.OrganizationRoleOwner {
		t.Fatalf("expected owner role, got %q", member.Role)
	}
}

func testGetUser(t *testing.T) {
	t.Helper()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	user := db.User{Login: "alice", Name: "Alice", Type: db.TypeUser}
	if err := svc.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}

	got, err := svc.GetUser(ctx, "alice")
	if err != nil {
		t.Fatalf("GetUser failed: %v", err)
	}
	if got.Login != "alice" {
		t.Errorf("expected login 'alice', got %q", got.Login)
	}

	_, err = svc.GetUser(ctx, "missing")
	if !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing user, got %v", err)
	}
}

func TestGetUser(t *testing.T) {
	testGetUser(t)
}

func testGetCurrentUser(t *testing.T) {
	t.Helper()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	admin := db.User{Login: "admin", Name: "Admin", Type: db.TypeUser, SiteAdmin: true}
	if err := svc.DB.Create(&admin).Error; err != nil {
		t.Fatalf("create admin failed: %v", err)
	}
	user := db.User{Login: "bob", Name: "Bob", Type: db.TypeUser}
	if err := svc.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}

	authCtx := service.ContextWithUser(ctx, user)
	current, err := svc.GetCurrentUser(authCtx)
	if err != nil {
		t.Fatalf("GetCurrentUser (context) failed: %v", err)
	}
	if current.Login != user.Login {
		t.Errorf("expected current user %q, got %q", user.Login, current.Login)
	}

	fallback, err := svc.GetCurrentUser(ctx)
	if err != nil {
		t.Fatalf("GetCurrentUser (fallback) failed: %v", err)
	}
	if fallback.Login != admin.Login {
		t.Errorf("expected fallback admin %q, got %q", admin.Login, fallback.Login)
	}
}

func TestGetCurrentUser(t *testing.T) {
	testGetCurrentUser(t)
}

func testListOrgs(t *testing.T) {
	t.Helper()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Create a normal user
	alice := db.User{Login: "alice", Name: "Alice", Type: db.TypeUser}
	if err := svc.DB.Create(&alice).Error; err != nil {
		t.Fatalf("create user alice failed: %v", err)
	}

	// Create orgs
	acme := db.User{Login: "acme", Name: "Acme", Type: db.TypeOrganization}
	octoOrg := db.User{Login: "octo-org", Name: "Octo Org", Type: db.TypeOrganization}
	if err := svc.DB.Create(&acme).Error; err != nil {
		t.Fatalf("create org acme failed: %v", err)
	}
	if err := svc.DB.Create(&octoOrg).Error; err != nil {
		t.Fatalf("create org octo-org failed: %v", err)
	}

	// Create one explicit org membership, one team-only legacy row, and one org membership without a team.
	teamAcme := db.Team{OrganizationID: acme.ID, Name: "acme-team", Slug: "acme-team", Privacy: db.TeamPrivacyClosed}
	if err := svc.DB.Create(&teamAcme).Error; err != nil {
		t.Fatalf("create teamAcme failed: %v", err)
	}
	if err := svc.AddOrgMember(ctx, acme.ID, alice.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember acme failed: %v", err)
	}
	if err := svc.AddTeamMember(ctx, teamAcme.ID, alice.ID, "member"); err != nil {
		t.Fatalf("AddTeamMember acme failed: %v", err)
	}

	legacyTeam := db.Team{OrganizationID: octoOrg.ID, Name: "legacy-team", Slug: "legacy-team", Privacy: db.TeamPrivacyClosed}
	if err := svc.DB.Create(&legacyTeam).Error; err != nil {
		t.Fatalf("create legacyTeam failed: %v", err)
	}
	if err := svc.DB.Create(&db.TeamMember{TeamID: legacyTeam.ID, UserID: alice.ID, Role: "member"}).Error; err != nil {
		t.Fatalf("create legacy team member failed: %v", err)
	}

	soloOrg := db.User{Login: "solo-org", Name: "Solo Org", Type: db.TypeOrganization}
	if err := svc.DB.Create(&soloOrg).Error; err != nil {
		t.Fatalf("create solo-org failed: %v", err)
	}
	if err := svc.AddOrgMember(ctx, soloOrg.ID, alice.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember solo-org failed: %v", err)
	}

	// Test with alice's context - should see explicit org memberships only.
	aliceCtx := service.ContextWithUser(ctx, alice)
	orgs, err := svc.ListOrgs(aliceCtx)
	if err != nil {
		t.Fatalf("ListOrgs failed: %v", err)
	}
	if len(orgs) != 2 {
		t.Fatalf("expected 2 orgs (acme and solo-org), got %d", len(orgs))
	}
	got := map[string]bool{}
	for _, org := range orgs {
		got[org.Login] = true
	}
	if !got["acme"] || !got["solo-org"] || got["octo-org"] {
		t.Fatalf("unexpected org set: %#v", got)
	}

	// Test with no context - should get empty list
	orgsNoCtx, err := svc.ListOrgs(ctx)
	if err != nil {
		t.Fatalf("ListOrgs (no context) failed: %v", err)
	}
	if len(orgsNoCtx) != 0 {
		t.Errorf("expected 0 orgs for no-context user, got %d", len(orgsNoCtx))
	}
}

func TestListOrgs(t *testing.T) {
	testListOrgs(t)
}

func testGetIssueTimeline(t *testing.T) {
	t.Helper()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	pr, _, user := setupPRWithRealBranches(t, svc, "timeline-user", "timeline-repo")
	fullName := "timeline-user/timeline-repo"

	c1, err := svc.AddCommentByPRID(ctx, pr.ID, "first comment", user.Login)
	if err != nil {
		t.Fatalf("AddCommentByPRID (first) failed: %v", err)
	}
	c2, err := svc.AddCommentByPRID(ctx, pr.ID, "second comment", user.Login)
	if err != nil {
		t.Fatalf("AddCommentByPRID (second) failed: %v", err)
	}
	review, err := svc.AddPRReview(ctx, pr.ID, user.Login, "APPROVED", "lgtm", "deadbeef")
	if err != nil {
		t.Fatalf("AddPRReview failed: %v", err)
	}

	base := time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC)
	t1 := base.Add(1 * time.Hour)
	t2 := base.Add(2 * time.Hour)
	t3 := base.Add(3 * time.Hour)

	if err := svc.DB.Model(&db.IssueComment{}).Where("id = ?", c1.ID).
		Updates(map[string]any{"created_at": t1, "updated_at": t1}).Error; err != nil {
		t.Fatalf("update first comment time failed: %v", err)
	}
	if err := svc.DB.Model(&db.PullRequestReview{}).Where("id = ?", review.ID).
		Updates(map[string]any{"submitted_at": t2, "updated_at": t2}).Error; err != nil {
		t.Fatalf("update review time failed: %v", err)
	}
	if err := svc.DB.Model(&db.IssueComment{}).Where("id = ?", c2.ID).
		Updates(map[string]any{"created_at": t3, "updated_at": t3}).Error; err != nil {
		t.Fatalf("update second comment time failed: %v", err)
	}

	events, err := svc.GetIssueTimeline(ctx, fullName, pr.Number)
	if err != nil {
		t.Fatalf("GetIssueTimeline failed: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 timeline events, got %d", len(events))
	}

	if events[0].Event != "commented" || events[0].Comment == nil || events[0].Comment.ID != c1.ID {
		t.Fatalf("expected first event to be first comment, got %+v", events[0])
	}
	if events[1].Event != "reviewed" || events[1].Review == nil || events[1].Review.ID != review.ID {
		t.Fatalf("expected second event to be review, got %+v", events[1])
	}
	if events[2].Event != "commented" || events[2].Comment == nil || events[2].Comment.ID != c2.ID {
		t.Fatalf("expected third event to be second comment, got %+v", events[2])
	}

	if !events[0].CreatedAt.Equal(t1) {
		t.Errorf("expected first event time %v, got %v", t1, events[0].CreatedAt)
	}
	if !events[1].CreatedAt.Equal(t2) {
		t.Errorf("expected second event time %v, got %v", t2, events[1].CreatedAt)
	}
	if !events[2].CreatedAt.Equal(t3) {
		t.Errorf("expected third event time %v, got %v", t3, events[2].CreatedAt)
	}
}

func TestGetIssueTimeline(t *testing.T) {
	testGetIssueTimeline(t)
}

// Wrappers so -run 'TestUser|TestTimeline' exercises this file's coverage.
func TestUser(t *testing.T) {
	t.Run("EnsureOrg", testEnsureOrg)
	t.Run("GetUser", testGetUser)
	t.Run("GetCurrentUser", testGetCurrentUser)
	t.Run("ListOrgs", testListOrgs)
}

func TestTimeline(t *testing.T) {
	t.Run("GetIssueTimeline", testGetIssueTimeline)
}
