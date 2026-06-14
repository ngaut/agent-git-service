package db

import (
	"strings"
	"testing"

	"gorm.io/gorm"
)

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb := openTiDB(t)
	if err := gdb.AutoMigrate(&User{}, &Token{}, &OrganizationMember{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return gdb
}

func TestSeed_Fallback(t *testing.T) {
	gdb := openTestDB(t)
	if err := Seed(gdb, "", ""); err != nil {
		t.Fatalf("Seed returned unexpected error: %v", err)
	}

	var user User
	if err := gdb.First(&user, "login = ?", "testadmin").Error; err != nil {
		t.Fatalf("expected testadmin user: %v", err)
	}
	if !user.SiteAdmin {
		t.Error("expected testadmin to be site admin")
	}

	var tok Token
	if err := gdb.First(&tok, "value = ?", "mytoken").Error; err != nil {
		t.Fatalf("expected mytoken token: %v", err)
	}
	if tok.UserID != user.ID {
		t.Errorf("token user_id = %d, want %d", tok.UserID, user.ID)
	}

	var org User
	if err := gdb.First(&org, "login = ?", "testorg").Error; err != nil {
		t.Fatalf("expected testorg organization: %v", err)
	}
	if org.Type != TypeOrganization {
		t.Fatalf("testorg type = %q, want %q", org.Type, TypeOrganization)
	}

	var membership OrganizationMember
	if err := gdb.First(&membership, "organization_id = ? AND user_id = ?", org.ID, user.ID).Error; err != nil {
		t.Fatalf("expected testadmin owner membership in testorg: %v", err)
	}
	if membership.Role != OrganizationRoleOwner {
		t.Fatalf("membership role = %q, want %q", membership.Role, OrganizationRoleOwner)
	}
}

func TestSeed_CustomCredentials(t *testing.T) {
	gdb := openTestDB(t)
	if err := Seed(gdb, "agent-42", "ghp_secret"); err != nil {
		t.Fatalf("Seed returned unexpected error: %v", err)
	}

	var user User
	if err := gdb.First(&user, "login = ?", "agent-42").Error; err != nil {
		t.Fatalf("expected agent-42 user: %v", err)
	}
	if !user.SiteAdmin {
		t.Error("expected agent-42 to be site admin")
	}

	var tok Token
	if err := gdb.First(&tok, "value = ?", "ghp_secret").Error; err != nil {
		t.Fatalf("expected ghp_secret token: %v", err)
	}
	if tok.UserID != user.ID {
		t.Errorf("token user_id = %d, want %d", tok.UserID, user.ID)
	}

	// Ensure fallback credentials were NOT created.
	var count int64
	gdb.Model(&User{}).Where("login = ?", "testadmin").Count(&count)
	if count != 0 {
		t.Error("testadmin should not exist when custom credentials are provided")
	}
}

func TestSeed_Idempotent(t *testing.T) {
	gdb := openTestDB(t)
	if err := Seed(gdb, "agent-42", "ghp_secret"); err != nil {
		t.Fatalf("Seed returned unexpected error: %v", err)
	}
	if err := Seed(gdb, "agent-42", "ghp_secret"); err != nil {
		t.Fatalf("Seed returned unexpected error: %v", err)
	}

	var userCount int64
	gdb.Model(&User{}).Where("login = ?", "agent-42").Count(&userCount)
	if userCount != 1 {
		t.Errorf("expected 1 user, got %d", userCount)
	}

	var tokCount int64
	gdb.Model(&Token{}).Where("value = ?", "ghp_secret").Count(&tokCount)
	if tokCount != 1 {
		t.Errorf("expected 1 token, got %d", tokCount)
	}

	var org User
	if err := gdb.First(&org, "login = ?", "testorg").Error; err != nil {
		t.Fatalf("expected testorg organization: %v", err)
	}

	var membershipCount int64
	gdb.Model(&OrganizationMember{}).
		Where("organization_id = ? AND role = ?", org.ID, OrganizationRoleOwner).
		Count(&membershipCount)
	if membershipCount != 1 {
		t.Errorf("expected 1 owner membership for testorg, got %d", membershipCount)
	}
}

func TestSeed_ExistingNonAdminUser(t *testing.T) {
	gdb := openTestDB(t)

	// Pre-create a non-admin user with the same login.
	gdb.Create(&User{Login: "agent-42", Name: "agent-42", Type: TypeUser, SiteAdmin: false})

	if err := Seed(gdb, "agent-42", "ghp_secret"); err != nil {
		t.Fatalf("Seed returned unexpected error: %v", err)
	}

	var user User
	if err := gdb.First(&user, "login = ?", "agent-42").Error; err != nil {
		t.Fatalf("expected agent-42 user: %v", err)
	}
	if !user.SiteAdmin {
		t.Error("expected existing non-admin user to be upgraded to site admin")
	}

	var tok Token
	if err := gdb.First(&tok, "value = ?", "ghp_secret").Error; err != nil {
		t.Fatalf("expected ghp_secret token: %v", err)
	}
	if tok.UserID != user.ID {
		t.Errorf("token user_id = %d, want %d", tok.UserID, user.ID)
	}
}

func TestSeed_PartialEnv_LoginOnly(t *testing.T) {
	gdb := openTestDB(t)
	err := Seed(gdb, "some-login", "")
	if err == nil {
		t.Fatal("expected error for login-only partial config, got nil")
	}

	// Error must not leak the raw login value.
	if strings.Contains(err.Error(), "some-login") {
		t.Errorf("error message leaks raw login: %v", err)
	}

	// Verify no data was seeded.
	var count int64
	gdb.Model(&User{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 users after rejected partial config, got %d", count)
	}
}

func TestSeed_PartialEnv_TokenOnly(t *testing.T) {
	gdb := openTestDB(t)
	err := Seed(gdb, "", "super-secret-token")
	if err == nil {
		t.Fatal("expected error for token-only partial config, got nil")
	}

	// Error must not leak the raw token value.
	if strings.Contains(err.Error(), "super-secret-token") {
		t.Errorf("error message leaks raw token: %v", err)
	}

	// Verify no data was seeded.
	var count int64
	gdb.Model(&User{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 users after rejected partial config, got %d", count)
	}
}
