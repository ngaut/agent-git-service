package rest_test

import (
	"context"
	"net/http"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/testharness"
)

func TestTeamCRUD_RequiresOrgAdmin(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	org, err := h.Svc.EnsureOrg(ctx, "team-permissions-org")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}

	_, outsiderToken := seedHarnessUser(t, h, "team-outsider", false)
	member, memberToken := seedHarnessUser(t, h, "team-member", false)
	maintainer, maintainerToken := seedHarnessUser(t, h, "team-maintainer", false)
	owner, ownerToken := seedHarnessUser(t, h, "team-owner", false)

	admins, err := h.Svc.CreateTeam(ctx, org.ID, "admins", "admins", "", "")
	if err != nil {
		t.Fatalf("CreateTeam admins: %v", err)
	}
	if err := h.Svc.AddOrgMember(ctx, org.ID, member.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember member: %v", err)
	}
	if err := h.Svc.AddOrgMember(ctx, org.ID, maintainer.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember maintainer: %v", err)
	}
	if err := h.Svc.AddOrgMember(ctx, org.ID, owner.ID, db.OrganizationRoleOwner); err != nil {
		t.Fatalf("AddOrgMember owner: %v", err)
	}
	if err := h.Svc.AddTeamMember(ctx, admins.ID, member.ID, "member"); err != nil {
		t.Fatalf("AddTeamMember member: %v", err)
	}
	if err := h.Svc.AddTeamMember(ctx, admins.ID, maintainer.ID, "maintainer"); err != nil {
		t.Fatalf("AddTeamMember maintainer: %v", err)
	}

	if _, err := h.Svc.CreateTeam(ctx, org.ID, "patch-target", "patch-target", "", ""); err != nil {
		t.Fatalf("CreateTeam patch-target: %v", err)
	}
	if _, err := h.Svc.CreateTeam(ctx, org.ID, "delete-target", "delete-target", "", ""); err != nil {
		t.Fatalf("CreateTeam delete-target: %v", err)
	}

	w := h.DoRESTJSONWithToken(t, "POST", "/api/v3/orgs/team-permissions-org/teams", outsiderToken, map[string]any{
		"name": "blocked-outsider",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("outsider POST expected 403, got %d: %s", w.Code, w.Body.String())
	}

	w = h.DoRESTJSONWithToken(t, "POST", "/api/v3/orgs/team-permissions-org/teams", memberToken, map[string]any{
		"name": "blocked-member",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("member POST expected 403, got %d: %s", w.Code, w.Body.String())
	}

	w = h.DoRESTJSONWithToken(t, "POST", "/api/v3/orgs/team-permissions-org/teams", maintainerToken, map[string]any{
		"name": "blocked-maintainer",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("maintainer POST expected 403, got %d: %s", w.Code, w.Body.String())
	}

	w = h.DoRESTJSONWithToken(t, "POST", "/api/v3/orgs/team-permissions-org/teams", ownerToken, map[string]any{
		"name": "created-by-owner",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("owner POST expected 201, got %d: %s", w.Code, w.Body.String())
	}

	w = h.DoRESTJSON(t, "POST", "/api/v3/orgs/team-permissions-org/teams", map[string]any{
		"name": "created-by-site-admin",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("site admin POST expected 201, got %d: %s", w.Code, w.Body.String())
	}

	w = h.DoRESTJSONWithToken(t, "PATCH", "/api/v3/orgs/team-permissions-org/teams/patch-target", outsiderToken, map[string]any{
		"description": "blocked outsider update",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("outsider PATCH expected 403, got %d: %s", w.Code, w.Body.String())
	}

	w = h.DoRESTJSONWithToken(t, "PATCH", "/api/v3/orgs/team-permissions-org/teams/patch-target", memberToken, map[string]any{
		"description": "blocked member update",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("member PATCH expected 403, got %d: %s", w.Code, w.Body.String())
	}

	w = h.DoRESTJSONWithToken(t, "PATCH", "/api/v3/orgs/team-permissions-org/teams/patch-target", maintainerToken, map[string]any{
		"name":        "patched-target",
		"description": "allowed maintainer update",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("maintainer PATCH expected 403, got %d: %s", w.Code, w.Body.String())
	}

	w = h.DoRESTJSONWithToken(t, "PATCH", "/api/v3/orgs/team-permissions-org/teams/patch-target", ownerToken, map[string]any{
		"description": "owner updated team",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("owner PATCH expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = h.DoRESTJSON(t, "PATCH", "/api/v3/orgs/team-permissions-org/teams/created-by-owner", map[string]any{
		"description": "site admin updated team",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("site admin PATCH expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = h.DoRESTWithToken(t, "DELETE", "/api/v3/orgs/team-permissions-org/teams/delete-target", outsiderToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("outsider DELETE expected 403, got %d: %s", w.Code, w.Body.String())
	}

	w = h.DoRESTWithToken(t, "DELETE", "/api/v3/orgs/team-permissions-org/teams/delete-target", memberToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member DELETE expected 403, got %d: %s", w.Code, w.Body.String())
	}

	w = h.DoRESTWithToken(t, "DELETE", "/api/v3/orgs/team-permissions-org/teams/delete-target", maintainerToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("maintainer DELETE expected 403, got %d: %s", w.Code, w.Body.String())
	}

	w = h.DoRESTWithToken(t, "DELETE", "/api/v3/orgs/team-permissions-org/teams/delete-target", ownerToken)
	if w.Code != http.StatusNoContent {
		t.Fatalf("owner DELETE expected 204, got %d: %s", w.Code, w.Body.String())
	}

	if _, err := h.Svc.CreateTeam(ctx, org.ID, "site-admin-delete-target", "site-admin-delete-target", "", ""); err != nil {
		t.Fatalf("CreateTeam site-admin-delete-target: %v", err)
	}
	w = h.DoREST(t, "DELETE", "/api/v3/orgs/team-permissions-org/teams/site-admin-delete-target", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("site admin DELETE expected 204, got %d: %s", w.Code, w.Body.String())
	}
}
