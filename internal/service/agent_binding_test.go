package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func TestConfirmAgentBindingSuccessAndConsumedInviteConflict(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	human := db.User{Login: "binding-human", Name: "binding-human", Type: db.TypeUser, UserKind: db.UserKindHuman}
	agent := db.User{Login: "binding-agent", Name: "binding-agent", Type: db.TypeUser, UserKind: db.UserKindAgent}
	if err := svc.DB.Create(&human).Error; err != nil {
		t.Fatalf("create human: %v", err)
	}
	if err := svc.DB.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	humanCtx := service.ContextWithUser(context.Background(), human)
	agentCtx := service.ContextWithUser(context.Background(), agent)

	invite, err := svc.CreateAgentInvite(humanCtx, service.CreateAgentInviteInput{})
	if err != nil {
		t.Fatalf("CreateAgentInvite: %v", err)
	}

	binding, err := svc.ConfirmAgentBinding(agentCtx, invite.Token)
	if err != nil {
		t.Fatalf("ConfirmAgentBinding: %v", err)
	}
	if binding.HumanUserID != human.ID {
		t.Fatalf("binding.HumanUserID = %d, want %d", binding.HumanUserID, human.ID)
	}
	if binding.AgentUserID != agent.ID {
		t.Fatalf("binding.AgentUserID = %d, want %d", binding.AgentUserID, agent.ID)
	}

	var storedInvite db.AgentInvite
	if err := svc.DB.First(&storedInvite, "id = ?", invite.ID).Error; err != nil {
		t.Fatalf("stored invite: %v", err)
	}
	if storedInvite.ConsumedAt == nil {
		t.Fatal("expected consumed_at to be recorded")
	}
	if storedInvite.ConsumedByAgentUserID == nil || *storedInvite.ConsumedByAgentUserID != agent.ID {
		t.Fatalf("consumed_by_agent_user_id = %v, want %d", storedInvite.ConsumedByAgentUserID, agent.ID)
	}

	_, err = svc.ConfirmAgentBinding(agentCtx, invite.Token)
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("second ConfirmAgentBinding error = %v, want ErrConflict", err)
	}
}

func TestConfirmAgentBindingRejectsHumanToken(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	human := db.User{Login: "human-only", Name: "human-only", Type: db.TypeUser, UserKind: db.UserKindHuman}
	if err := svc.DB.Create(&human).Error; err != nil {
		t.Fatalf("create human: %v", err)
	}

	humanCtx := service.ContextWithUser(context.Background(), human)
	invite, err := svc.CreateAgentInvite(humanCtx, service.CreateAgentInviteInput{})
	if err != nil {
		t.Fatalf("CreateAgentInvite: %v", err)
	}

	_, err = svc.ConfirmAgentBinding(humanCtx, invite.Token)
	if !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("ConfirmAgentBinding error = %v, want ErrForbidden", err)
	}
}

func TestConfirmAgentBindingRejectsInvalidInvite(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	agent := db.User{Login: "invalid-invite-agent", Name: "invalid-invite-agent", Type: db.TypeUser, UserKind: db.UserKindAgent}
	if err := svc.DB.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	agentCtx := service.ContextWithUser(context.Background(), agent)
	_, err := svc.ConfirmAgentBinding(agentCtx, "missing-invite")
	if !errors.Is(err, service.ErrValidation) {
		t.Fatalf("ConfirmAgentBinding error = %v, want ErrValidation", err)
	}
}

func TestConfirmAgentBindingRejectsExpiredInvite(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	human := db.User{Login: "expired-human", Name: "expired-human", Type: db.TypeUser, UserKind: db.UserKindHuman}
	agent := db.User{Login: "expired-agent", Name: "expired-agent", Type: db.TypeUser, UserKind: db.UserKindAgent}
	if err := svc.DB.Create(&human).Error; err != nil {
		t.Fatalf("create human: %v", err)
	}
	if err := svc.DB.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	humanCtx := service.ContextWithUser(context.Background(), human)
	agentCtx := service.ContextWithUser(context.Background(), agent)
	invite, err := svc.CreateAgentInvite(humanCtx, service.CreateAgentInviteInput{})
	if err != nil {
		t.Fatalf("CreateAgentInvite: %v", err)
	}

	expiredAt := time.Now().UTC().Add(-time.Minute)
	if err := svc.DB.Model(&db.AgentInvite{}).Where("id = ?", invite.ID).Update("expires_at", expiredAt).Error; err != nil {
		t.Fatalf("expire invite: %v", err)
	}

	_, err = svc.ConfirmAgentBinding(agentCtx, invite.Token)
	if !errors.Is(err, service.ErrValidation) {
		t.Fatalf("ConfirmAgentBinding error = %v, want ErrValidation", err)
	}
}

func TestConfirmAgentBindingAppliesRepoAndTeamGrants(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	human := db.User{Login: "grant-human", Name: "grant-human", Type: db.TypeUser, UserKind: db.UserKindHuman}
	agent := db.User{Login: "grant-agent", Name: "grant-agent", Type: db.TypeUser, UserKind: db.UserKindAgent}
	org := db.User{Login: "grant-org", Name: "grant-org", Type: db.TypeOrganization}
	if err := svc.DB.Create(&human).Error; err != nil {
		t.Fatalf("create human: %v", err)
	}
	if err := svc.DB.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := svc.DB.Create(&org).Error; err != nil {
		t.Fatalf("create org: %v", err)
	}
	if err := svc.AddOrgMember(context.Background(), org.ID, human.ID, db.OrganizationRoleOwner); err != nil {
		t.Fatalf("add human org owner: %v", err)
	}
	repo := db.Repository{Name: "widgets", FullName: "grant-org/widgets", OwnerID: org.ID, Private: true, Visibility: "private", DefaultBranch: "main", HasWiki: true, HasIssues: true}
	if err := svc.DB.Create(&repo).Error; err != nil {
		t.Fatalf("create repo: %v", err)
	}
	team, err := svc.CreateTeam(context.Background(), org.ID, "Platform", "platform", "", db.TeamPrivacyClosed)
	if err != nil {
		t.Fatalf("create team: %v", err)
	}

	humanCtx := service.ContextWithUser(context.Background(), human)
	agentCtx := service.ContextWithUser(context.Background(), agent)
	invite, err := svc.CreateAgentInvite(humanCtx, service.CreateAgentInviteInput{
		RepoGrants: []service.AgentInviteRepoGrant{{RepoFullName: repo.FullName, Permission: "write"}},
		TeamGrants: []service.AgentInviteTeamGrant{{Org: org.Login, TeamSlug: team.Slug, Role: "member"}},
	})
	if err != nil {
		t.Fatalf("CreateAgentInvite with grants: %v", err)
	}

	if _, err := svc.ConfirmAgentBinding(agentCtx, invite.Token); err != nil {
		t.Fatalf("ConfirmAgentBinding: %v", err)
	}

	perm, err := svc.HasRepoAccess(agentCtx, repo.ID, agent.ID)
	if err != nil {
		t.Fatalf("HasRepoAccess: %v", err)
	}
	if !perm.AtLeast(service.RepoPermissionWrite) {
		t.Fatalf("expected agent to have write repo access, got %v", perm)
	}

	isMember, err := svc.IsOrgMember(agentCtx, org.ID, agent.ID)
	if err != nil {
		t.Fatalf("IsOrgMember: %v", err)
	}
	if !isMember {
		t.Fatal("expected agent to become org member via team grant")
	}

	member, err := svc.GetTeamMember(agentCtx, team.ID, agent.ID)
	if err != nil {
		t.Fatalf("GetTeamMember: %v", err)
	}
	if member.Role != "member" {
		t.Fatalf("team role = %q, want member", member.Role)
	}
}

func TestConfirmAgentBindingBackfillsAdminsTeamForExistingAgentAdminOrg(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	human := db.User{Login: "backfill-human", Name: "backfill-human", Type: db.TypeUser, UserKind: db.UserKindHuman}
	agent := db.User{Login: "backfill-agent", Name: "backfill-agent", Type: db.TypeUser, UserKind: db.UserKindAgent}
	org := db.User{Login: "backfill-org", Name: "backfill-org", Type: db.TypeOrganization}
	if err := svc.DB.Create(&human).Error; err != nil {
		t.Fatalf("create human: %v", err)
	}
	if err := svc.DB.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := svc.DB.Create(&org).Error; err != nil {
		t.Fatalf("create org: %v", err)
	}
	if err := svc.AddOrgMember(context.Background(), org.ID, agent.ID, db.OrganizationRoleOwner); err != nil {
		t.Fatalf("add agent org owner: %v", err)
	}
	adminsTeam, err := svc.CreateTeam(context.Background(), org.ID, "Admins", "admins", "", db.TeamPrivacyClosed)
	if err != nil {
		t.Fatalf("create admins team: %v", err)
	}
	if err := svc.AddTeamMember(context.Background(), adminsTeam.ID, agent.ID, "maintainer"); err != nil {
		t.Fatalf("add agent to admins team: %v", err)
	}

	humanCtx := service.ContextWithUser(context.Background(), human)
	agentCtx := service.ContextWithUser(context.Background(), agent)
	invite, err := svc.CreateAgentInvite(humanCtx, service.CreateAgentInviteInput{})
	if err != nil {
		t.Fatalf("CreateAgentInvite: %v", err)
	}
	if _, err := svc.ConfirmAgentBinding(agentCtx, invite.Token); err != nil {
		t.Fatalf("ConfirmAgentBinding: %v", err)
	}

	isMember, err := svc.IsOrgMember(humanCtx, org.ID, human.ID)
	if err != nil {
		t.Fatalf("IsOrgMember(human): %v", err)
	}
	if !isMember {
		t.Fatal("expected human to be added to org membership during admins-team backfill")
	}
	teamMember, err := svc.GetTeamMember(humanCtx, adminsTeam.ID, human.ID)
	if err != nil {
		t.Fatalf("GetTeamMember(human, admins): %v", err)
	}
	if teamMember.Role != "maintainer" {
		t.Fatalf("admins team role = %q, want maintainer", teamMember.Role)
	}
}

func TestConfirmAgentBindingRejectsTeamGrantForUnaffiliatedAgentWhenInviterIsOnlyMaintainer(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	orgOwner := db.User{Login: "owner-human", Name: "owner-human", Type: db.TypeUser, UserKind: db.UserKindHuman}
	maintainer := db.User{Login: "team-maintainer", Name: "team-maintainer", Type: db.TypeUser, UserKind: db.UserKindHuman}
	agent := db.User{Login: "outside-agent", Name: "outside-agent", Type: db.TypeUser, UserKind: db.UserKindAgent}
	org := db.User{Login: "maintainer-org", Name: "maintainer-org", Type: db.TypeOrganization}
	if err := svc.DB.Create(&orgOwner).Error; err != nil {
		t.Fatalf("create org owner: %v", err)
	}
	if err := svc.DB.Create(&maintainer).Error; err != nil {
		t.Fatalf("create maintainer: %v", err)
	}
	if err := svc.DB.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := svc.DB.Create(&org).Error; err != nil {
		t.Fatalf("create org: %v", err)
	}
	if err := svc.AddOrgMember(context.Background(), org.ID, orgOwner.ID, db.OrganizationRoleOwner); err != nil {
		t.Fatalf("add org owner: %v", err)
	}
	if err := svc.AddOrgMember(context.Background(), org.ID, maintainer.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("add maintainer org member: %v", err)
	}
	team, err := svc.CreateTeam(context.Background(), org.ID, "Platform", "platform", "", db.TeamPrivacyClosed)
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	if err := svc.AddTeamMember(context.Background(), team.ID, maintainer.ID, "maintainer"); err != nil {
		t.Fatalf("add team maintainer: %v", err)
	}

	maintainerCtx := service.ContextWithUser(context.Background(), maintainer)
	agentCtx := service.ContextWithUser(context.Background(), agent)
	invite, err := svc.CreateAgentInvite(maintainerCtx, service.CreateAgentInviteInput{
		TeamGrants: []service.AgentInviteTeamGrant{{Org: org.Login, TeamSlug: team.Slug, Role: "member"}},
	})
	if err != nil {
		t.Fatalf("CreateAgentInvite with team grant: %v", err)
	}

	if _, err := svc.ConfirmAgentBinding(agentCtx, invite.Token); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("ConfirmAgentBinding error = %v, want ErrForbidden", err)
	}

	isMember, err := svc.IsOrgMember(agentCtx, org.ID, agent.ID)
	if err != nil {
		t.Fatalf("IsOrgMember: %v", err)
	}
	if isMember {
		t.Fatal("expected unaffiliated agent not to become org member")
	}

	var count int64
	if err := svc.DB.Model(&db.AgentBinding{}).Where("agent_user_id = ?", agent.ID).Count(&count).Error; err != nil {
		t.Fatalf("count bindings: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected binding rollback, found %d rows", count)
	}
}
