package rest_test

import (
	"context"
	"net/http"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/testharness"
)

func TestCreateTeam_CollapsesRequestedPrivacyToClosed(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	org, err := h.Svc.EnsureOrg(ctx, "privacy-org")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}

	w := h.DoRESTJSON(t, "POST", "/api/v3/orgs/privacy-org/teams", map[string]any{
		"name":        "Security",
		"description": "Restricted access",
		"privacy":     "secret",
	})
	if w.Code != 201 {
		t.Fatalf("POST /api/v3/orgs/privacy-org/teams = %d: %s", w.Code, w.Body.String())
	}

	body := testharness.DecodeJSON(t, w)
	if body["privacy"] != db.TeamPrivacyClosed {
		t.Fatalf("privacy = %v, want %s", body["privacy"], db.TeamPrivacyClosed)
	}

	var team db.Team
	if err := h.DB.Where("organization_id = ? AND slug = ?", org.ID, "security").First(&team).Error; err != nil {
		t.Fatalf("fetch created team: %v", err)
	}
	if team.Privacy != db.TeamPrivacyClosed {
		t.Fatalf("stored privacy = %q, want %s", team.Privacy, db.TeamPrivacyClosed)
	}
}

func TestCreateTeam_DefaultsPrivacyClosed(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	org, err := h.Svc.EnsureOrg(ctx, "default-privacy-org")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}

	w := h.DoRESTJSON(t, "POST", "/api/v3/orgs/default-privacy-org/teams", map[string]any{
		"name":        "Platform",
		"description": "Default privacy team",
	})
	if w.Code != 201 {
		t.Fatalf("POST /api/v3/orgs/default-privacy-org/teams = %d: %s", w.Code, w.Body.String())
	}

	body := testharness.DecodeJSON(t, w)
	if body["privacy"] != db.TeamPrivacyClosed {
		t.Fatalf("privacy = %v, want %s", body["privacy"], db.TeamPrivacyClosed)
	}

	var team db.Team
	if err := h.DB.Where("organization_id = ? AND slug = ?", org.ID, "platform").First(&team).Error; err != nil {
		t.Fatalf("fetch created team: %v", err)
	}
	if team.Privacy != db.TeamPrivacyClosed {
		t.Fatalf("stored privacy = %q, want %s", team.Privacy, db.TeamPrivacyClosed)
	}
}

func TestCreateTeam_DuplicateReturnsReadableConflict(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	org, err := h.Svc.EnsureOrg(ctx, "duplicate-team-org")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}

	team := db.Team{
		OrganizationID: org.ID,
		Name:           "Platform",
		Slug:           "platform",
		Privacy:        db.TeamPrivacyClosed,
	}
	if err := h.DB.Create(&team).Error; err != nil {
		t.Fatalf("seed team: %v", err)
	}

	w := h.DoRESTJSON(t, "POST", "/api/v3/orgs/duplicate-team-org/teams", map[string]any{
		"name":        "Platform",
		"description": "Duplicate access team",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("POST /api/v3/orgs/duplicate-team-org/teams = %d: %s", w.Code, w.Body.String())
	}

	body := testharness.DecodeJSON(t, w)
	if body["message"] != `conflict: team "Platform" already exists in this organization` {
		t.Fatalf("expected readable conflict message, got %v", body["message"])
	}
}

func TestRenameTeam_DuplicateReturnsReadableConflict(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	org, err := h.Svc.EnsureOrg(ctx, "rename-conflict-org")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}

	if _, err := h.Svc.CreateTeam(ctx, org.ID, "Platform", "platform", "", ""); err != nil {
		t.Fatalf("CreateTeam platform: %v", err)
	}
	if _, err := h.Svc.CreateTeam(ctx, org.ID, "Operations", "operations", "", ""); err != nil {
		t.Fatalf("CreateTeam operations: %v", err)
	}

	w := h.DoRESTJSON(t, "PATCH", "/api/v3/orgs/rename-conflict-org/teams/operations", map[string]any{
		"name": "Platform",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("PATCH /api/v3/orgs/rename-conflict-org/teams/operations = %d: %s", w.Code, w.Body.String())
	}

	body := testharness.DecodeJSON(t, w)
	if body["message"] != `conflict: team "Platform" already exists in this organization` {
		t.Fatalf("expected readable conflict message, got %v", body["message"])
	}
}

func TestGetTeam_BlankStoredPrivacySerializesAsClosed(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	org, err := h.Svc.EnsureOrg(ctx, "legacy-privacy-org")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}

	team := db.Team{
		OrganizationID: org.ID,
		Name:           "Legacy Team",
		Slug:           "legacy-team",
		Description:    "Legacy privacy row",
		Privacy:        db.TeamPrivacyClosed,
	}
	if err := h.DB.Create(&team).Error; err != nil {
		t.Fatalf("create team: %v", err)
	}
	if err := h.DB.Model(&team).Update("privacy", "").Error; err != nil {
		t.Fatalf("blank privacy: %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/orgs/legacy-privacy-org/teams/legacy-team", nil)
	if w.Code != 200 {
		t.Fatalf("GET /api/v3/orgs/legacy-privacy-org/teams/legacy-team = %d: %s", w.Code, w.Body.String())
	}

	body := testharness.DecodeJSON(t, w)
	if body["privacy"] != db.TeamPrivacyClosed {
		t.Fatalf("privacy = %v, want %s", body["privacy"], db.TeamPrivacyClosed)
	}
}

func TestGetTeam_LegacySecretStoredPrivacySerializesAsClosed(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	org, err := h.Svc.EnsureOrg(ctx, "legacy-secret-privacy-org")
	if err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}

	team := db.Team{
		OrganizationID: org.ID,
		Name:           "Legacy Secret Team",
		Slug:           "legacy-secret-team",
		Description:    "Legacy secret privacy row",
		Privacy:        "secret",
	}
	if err := h.DB.Create(&team).Error; err != nil {
		t.Fatalf("create team: %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/orgs/legacy-secret-privacy-org/teams/legacy-secret-team", nil)
	if w.Code != 200 {
		t.Fatalf("GET /api/v3/orgs/legacy-secret-privacy-org/teams/legacy-secret-team = %d: %s", w.Code, w.Body.String())
	}

	body := testharness.DecodeJSON(t, w)
	if body["privacy"] != db.TeamPrivacyClosed {
		t.Fatalf("privacy = %v, want %s", body["privacy"], db.TeamPrivacyClosed)
	}
}
