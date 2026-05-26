package rest_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

// Regression for issue #1296 Phase B: org audit log read endpoint must
// surface the org-membership add/remove events that the service layer
// records, scoped to the requested org and gated by org-admin
// permission.
func TestOrgAuditLog_MembershipEvents_Issue1296(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	owner, ownerToken := seedHarnessUser(t, h, "audit-owner", false)
	org, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, owner), "audit-org")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}
	member, _ := seedHarnessUser(t, h, "audit-member", false)

	if err := h.Svc.AddOrgMember(ctx, org.ID, member.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}
	if err := h.Svc.RemoveOrgMember(ctx, org.ID, member.Login); err != nil {
		t.Fatalf("RemoveOrgMember: %v", err)
	}

	w := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/audit-org/audit-log", ownerToken)
	assertStatusCode(t, w, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, w)
	if len(rows) < 2 {
		t.Fatalf("expected >= 2 audit entries, got %d: %v", len(rows), rows)
	}
	actions := map[string]int{}
	for _, row := range rows {
		if a, _ := row["action"].(string); a != "" {
			actions[a]++
		}
		if row["org"] != org.Login {
			t.Errorf("audit entry org: got %v, want %q", row["org"], org.Login)
		}
	}
	if actions[service.AuditActionOrgAddMember] == 0 {
		t.Errorf("missing %s entry; got actions=%v", service.AuditActionOrgAddMember, actions)
	}
	if actions[service.AuditActionOrgRemoveMember] == 0 {
		t.Errorf("missing %s entry; got actions=%v", service.AuditActionOrgRemoveMember, actions)
	}

	// Phrase filter narrows to a single action.
	w = h.DoRESTWithToken(t, "GET", "/api/v3/orgs/audit-org/audit-log?phrase=add_member", ownerToken)
	assertStatusCode(t, w, http.StatusOK)
	rows = testharness.DecodeJSONArray(t, w)
	if len(rows) == 0 {
		t.Fatalf("phrase filter returned 0 rows")
	}
	for _, row := range rows {
		if a, _ := row["action"].(string); a != service.AuditActionOrgAddMember {
			t.Errorf("phrase filter leaked action %q", a)
		}
	}

	// Non-admin caller must be forbidden (403, not 404 — requireOrgAdmin
	// returns Forbidden once the org is resolved).
	_, otherToken := seedHarnessUser(t, h, "audit-stranger", false)
	w = h.DoRESTWithToken(t, "GET", "/api/v3/orgs/audit-org/audit-log", otherToken)
	assertStatusCode(t, w, http.StatusForbidden)

	// Cross-org isolation: a second org's events must not leak into the
	// first org's audit log even though both orgs share the same admin.
	otherOrg, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, owner), "audit-org-other")
	if err != nil {
		t.Fatalf("EnsureOrg other: %v", err)
	}
	otherMember, _ := seedHarnessUser(t, h, "audit-other-member", false)
	if err := h.Svc.AddOrgMember(ctx, otherOrg.ID, otherMember.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember other: %v", err)
	}
	w = h.DoRESTWithToken(t, "GET", "/api/v3/orgs/audit-org/audit-log", ownerToken)
	assertStatusCode(t, w, http.StatusOK)
	rows = testharness.DecodeJSONArray(t, w)
	for _, row := range rows {
		if row["org"] == otherOrg.Login {
			t.Errorf("first-org audit-log leaked entry from %q: %v", otherOrg.Login, row)
		}
		if row["user"] == otherMember.Login {
			t.Errorf("first-org audit-log leaked target user from other org: %v", row)
		}
	}
}

// Regression for the second adversarial pass on PR #1300: AddOrgMember
// and SetOrgMembership must NOT emit org.add_member when the call is
// an idempotent re-save (already a member with the same role) or a
// pure role change. Audit-log truthfulness is the Phase B contract.
func TestOrgAuditLog_NoFalsePositiveOnRoleUpdate_Issue1296(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	owner, ownerToken := seedHarnessUser(t, h, "audit-fp-owner", false)
	org, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, owner), "audit-fp-org")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}
	member, _ := seedHarnessUser(t, h, "audit-fp-member", false)

	// Initial add — emits exactly one org.add_member.
	if err := h.Svc.AddOrgMember(ctx, org.ID, member.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember initial: %v", err)
	}
	// Idempotent re-add with the same role — must NOT emit a second row.
	if err := h.Svc.AddOrgMember(ctx, org.ID, member.ID, db.OrganizationRoleMember); err != nil {
		t.Fatalf("AddOrgMember idempotent: %v", err)
	}
	// Promotion via AddOrgMember (member -> owner). The merge logic
	// updates the role; this is NOT a member addition and must not
	// emit org.add_member.
	if err := h.Svc.AddOrgMember(ctx, org.ID, member.ID, db.OrganizationRoleOwner); err != nil {
		t.Fatalf("AddOrgMember promote: %v", err)
	}
	// SetOrgMembership active path (role change owner -> member with
	// another owner already present so the last-owner guard doesn't
	// trip). Also must not emit org.add_member.
	if _, err := h.Svc.SetOrgMembership(ctx, org.ID, member.Login, "member", owner.ID); err != nil {
		t.Fatalf("SetOrgMembership demote: %v", err)
	}

	w := h.DoRESTWithToken(t, "GET", "/api/v3/orgs/audit-fp-org/audit-log", ownerToken)
	assertStatusCode(t, w, http.StatusOK)
	rows := testharness.DecodeJSONArray(t, w)
	addRows := 0
	for _, row := range rows {
		if a, _ := row["action"].(string); a == service.AuditActionOrgAddMember {
			addRows++
		}
	}
	if addRows != 1 {
		t.Errorf("expected exactly 1 org.add_member after add+idempotent+promote+demote, got %d (rows=%v)", addRows, rows)
	}
}
