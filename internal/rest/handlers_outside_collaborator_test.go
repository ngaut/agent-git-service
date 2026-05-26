package rest_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

func TestListOutsideCollaborators(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	orgOwner, ownerToken := seedHarnessUser(t, h, "outside-list-owner", false)
	ownerCtx := service.ContextWithUser(ctx, orgOwner)
	org, err := h.Svc.EnsureOrg(ownerCtx, "outside-list-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	repo, err := h.Svc.CreateRepo(ownerCtx, service.CreateRepoInput{
		OwnerLogin: org.Login,
		Name:       "outside-list-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("CreateRepo failed: %v", err)
	}

	outside, _ := seedHarnessUser(t, h, "outside-listed-user", false)
	member, _ := seedHarnessUser(t, h, "inside-listed-user", false)
	_, outsiderToken := seedHarnessUser(t, h, "outside-list-outsider", false)
	if err := h.Svc.AddOrgMember(ctx, org.ID, member.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember failed: %v", err)
	}
	if err := h.Svc.AddCollaborator(ctx, repo.ID, outside.ID, "read"); err != nil {
		t.Fatalf("AddCollaborator outside failed: %v", err)
	}
	if err := h.Svc.AddCollaborator(ctx, repo.ID, member.ID, "read"); err != nil {
		t.Fatalf("AddCollaborator member failed: %v", err)
	}

	w := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/outside-list-org/outside_collaborators", outsiderToken)
	assertStatusCode(t, w, http.StatusForbidden)

	w = h.DoRESTWithToken(t, "GET", "/api/v3/orgs/outside-list-org/outside_collaborators", ownerToken)
	assertStatusCode(t, w, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, w)
	if len(rows) != 1 {
		t.Fatalf("expected 1 outside collaborator, got %d", len(rows))
	}
	if rows[0]["login"] != outside.Login {
		t.Fatalf("outside collaborator login = %v, want %s", rows[0]["login"], outside.Login)
	}
	if rows[0]["outside_collaborator"] != true {
		t.Fatalf("outside_collaborator = %v, want true", rows[0]["outside_collaborator"])
	}
	if rows[0]["organization_member"] != false {
		t.Fatalf("organization_member = %v, want false", rows[0]["organization_member"])
	}
}

func TestRepoCollaboratorListAnnotatesMemberVsOutside(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	orgOwner, ownerToken := seedHarnessUser(t, h, "outside-annot-owner", false)
	ownerCtx := service.ContextWithUser(ctx, orgOwner)
	org, err := h.Svc.EnsureOrg(ownerCtx, "outside-annot-org")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}
	repo, err := h.Svc.CreateRepo(ownerCtx, service.CreateRepoInput{
		OwnerLogin: org.Login,
		Name:       "outside-annot-repo",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("CreateRepo failed: %v", err)
	}

	outside, _ := seedHarnessUser(t, h, "outside-annot-user", false)
	member, _ := seedHarnessUser(t, h, "inside-annot-user", false)
	if err := h.Svc.AddOrgMember(ctx, org.ID, member.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember failed: %v", err)
	}
	if err := h.Svc.AddCollaborator(ctx, repo.ID, outside.ID, "read"); err != nil {
		t.Fatalf("AddCollaborator outside failed: %v", err)
	}
	if err := h.Svc.AddCollaborator(ctx, repo.ID, member.ID, "write"); err != nil {
		t.Fatalf("AddCollaborator member failed: %v", err)
	}

	w := h.DoRESTWithToken(t, "GET", "/api/v3/repos/outside-annot-org/outside-annot-repo/collaborators", ownerToken)
	assertStatusCode(t, w, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, w)

	var outsideRow, memberRow map[string]any
	for _, row := range rows {
		login, _ := row["login"].(string)
		switch login {
		case outside.Login:
			outsideRow = row
		case member.Login:
			memberRow = row
		}
	}
	if outsideRow == nil {
		t.Fatal("expected outside collaborator row to be present")
	}
	if memberRow == nil {
		t.Fatal("expected org member collaborator row to be present")
	}
	if outsideRow["outside_collaborator"] != true || outsideRow["organization_member"] != false {
		t.Fatalf("unexpected outside row flags: %#v", outsideRow)
	}
	if memberRow["outside_collaborator"] != false || memberRow["organization_member"] != true {
		t.Fatalf("unexpected member row flags: %#v", memberRow)
	}
}
