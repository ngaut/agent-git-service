package service_test

import (
	"context"
	"strings"
	"testing"

	"gh-server/internal/db"
)

const validArmoredGPGKey = `-----BEGIN PGP PUBLIC KEY BLOCK-----

mDMEZxpWhhYJKwYBBAHaRw8BAQdAmYiobR2ai/lVWOBtlAPRG1ZEMG5Effavpt5w
n+wQ//W0R0dIIENMSSSBhY2NlcHRhbmNlIHRlc3QgKGZvciBHSCBDTEkgYWNjZXB0
YW5jZSB0ZXN0aW5nKSA8Y2xpQGdpdGh1Yi5jb20+iJkEExYKAEEWIQTEAQLLUl1x
MDSmbL0kww+ckRXnRwUCZxpWhgIbAwUJAAFRgAULCQgHAgIiAgYVCgkICwIEFgID
AQIeBwIXgAAKCRAkww+ckRXnRxkuAP9GiFi/etWxRjnkomdTaOU8Ccd6oHspuEzB
PFxOJdYslQD+MXgY5UhM/q2iEVj0tiVsfRzDqB+g2weaF5EpqIwWcQ+4OARnGlaG
EgorBgEEAZdVAQUBAQdA3D1vnVTc9URDQw/oAd1mG/zRX7vF4QrjFqFIt7uMf2gD
AQgHiH4EGBYKACYWIQTEAQLLUl1xMDSmbL0kww+ckRXnRwUCZxpWhgIbDAUJAAFR
gAAKCRAkww+ckRXnRxVuAQCngnR11jh2mob0FN0rPWce2juoJsh5gPB2d7LS4r5P
VwEA6F2FeetcP51EyKyQGTp3GpmZgk0uCGJa1G5uqT+9mgc=
=RLWi
-----END PGP PUBLIC KEY BLOCK-----`

// TestDeployKeyCRUD tests the CRUD lifecycle for deploy keys.
func TestDeployKeyCRUD(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user and repo
	svc.DB.Create(&db.User{Login: "deploykeyuser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "deploykeyuser")

	// Create repo
	repoName := "test-repo"
	fullName := u.Login + "/" + repoName
	svc.DB.Create(&db.Repository{
		OwnerID:       u.ID,
		Owner:         u,
		Name:          repoName,
		FullName:      fullName,
		DefaultBranch: "main",
	})

	// Create deploy key
	sshKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDTest1234567890 test@example.com"
	created, err := svc.CreateDeployKey(ctx, fullName, "test-deploy-key", sshKey, false)
	if err != nil {
		t.Fatalf("CreateDeployKey failed: %v", err)
	}
	if created.Title != "test-deploy-key" {
		t.Errorf("expected title 'test-deploy-key', got %s", created.Title)
	}

	// List deploy keys
	keys, err := svc.ListDeployKeys(ctx, fullName)
	if err != nil {
		t.Fatalf("ListDeployKeys failed: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("expected 1 deploy key, got %d", len(keys))
	}

	// Delete deploy key
	err = svc.DeleteDeployKey(ctx, created.ID)
	if err != nil {
		t.Fatalf("DeleteDeployKey failed: %v", err)
	}

	// Verify deletion
	keys, err = svc.ListDeployKeys(ctx, fullName)
	if err != nil {
		t.Fatalf("ListDeployKeys after delete failed: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected 0 deploy keys after delete, got %d", len(keys))
	}
}

// TestCreateDeployKey_DuplicateKey verifies duplicate deploy keys are allowed for a repo.
func TestCreateDeployKey_DuplicateKey(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user and repo
	svc.DB.Create(&db.User{Login: "dupdeployuser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "dupdeployuser")

	repoName := "dup-deploy-repo"
	fullName := u.Login + "/" + repoName
	svc.DB.Create(&db.Repository{
		OwnerID:       u.ID,
		Owner:         u,
		Name:          repoName,
		FullName:      fullName,
		DefaultBranch: "main",
	})

	sshKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDTest1234567890 dupdeploy@example.com"
	_, err := svc.CreateDeployKey(ctx, fullName, "dup-deploy-key-1", sshKey, false)
	if err != nil {
		t.Fatalf("CreateDeployKey first duplicate failed: %v", err)
	}
	_, err = svc.CreateDeployKey(ctx, fullName, "dup-deploy-key-2", sshKey, false)
	if err != nil {
		t.Fatalf("CreateDeployKey second duplicate failed: %v", err)
	}

	keys, err := svc.ListDeployKeys(ctx, fullName)
	if err != nil {
		t.Fatalf("ListDeployKeys failed: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 deploy keys, got %d", len(keys))
	}
}

// TestSSHKeyCRUD tests the CRUD lifecycle for SSH keys.
func TestSSHKeyCRUD(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user
	svc.DB.Create(&db.User{Login: "sshkeyuser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "sshkeyuser")

	// Create SSH key
	sshKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDTest1234567890 test@example.com"
	created, err := svc.CreateSSHKey(ctx, u.ID, "test-ssh-key", sshKey)
	if err != nil {
		t.Fatalf("CreateSSHKey failed: %v", err)
	}

	// Get SSH key
	got, err := svc.GetSSHKey(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetSSHKey failed: %v", err)
	}
	if got.Title != "test-ssh-key" {
		t.Errorf("expected title 'test-ssh-key', got %s", got.Title)
	}

	// List SSH keys
	keys, err := svc.ListSSHKeys(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListSSHKeys failed: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("expected 1 SSH key, got %d", len(keys))
	}

	// Delete SSH key
	err = svc.DeleteSSHKey(ctx, created.ID)
	if err != nil {
		t.Fatalf("DeleteSSHKey failed: %v", err)
	}

	// Verify deletion
	_, err = svc.GetSSHKey(ctx, created.ID)
	if err == nil {
		t.Error("expected error when getting deleted SSH key")
	}
}

// TestCreateSSHKey_DuplicateKey verifies duplicate SSH keys are allowed for a user.
func TestCreateSSHKey_DuplicateKey(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user
	svc.DB.Create(&db.User{Login: "dupsshuser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "dupsshuser")

	sshKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDTest1234567890 dups@example.com"
	_, err := svc.CreateSSHKey(ctx, u.ID, "dup-ssh-key-1", sshKey)
	if err != nil {
		t.Fatalf("CreateSSHKey first duplicate failed: %v", err)
	}
	_, err = svc.CreateSSHKey(ctx, u.ID, "dup-ssh-key-2", sshKey)
	if err != nil {
		t.Fatalf("CreateSSHKey second duplicate failed: %v", err)
	}

	keys, err := svc.ListSSHKeys(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListSSHKeys failed: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 SSH keys, got %d", len(keys))
	}
}

// TestCreateSSHKey_InvalidFormat verifies malformed SSH keys are stored as-is.
func TestCreateSSHKey_InvalidFormat(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user
	svc.DB.Create(&db.User{Login: "badsshuser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "badsshuser")

	invalidKey := "not-a-valid-ssh-key"
	created, err := svc.CreateSSHKey(ctx, u.ID, "invalid-ssh-key", invalidKey)
	if err != nil {
		t.Fatalf("CreateSSHKey with invalid key should not fail at service level, got: %v", err)
	}
	if created.Key != invalidKey {
		t.Errorf("expected stored key to match invalid input, got %s", created.Key)
	}
}

// TestSSHSigningKeyCRUD tests the CRUD lifecycle for SSH signing keys.
func TestSSHSigningKeyCRUD(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user
	svc.DB.Create(&db.User{Login: "sshsignuser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "sshsignuser")

	// Create SSH signing key
	sshKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDTest1234567890 signing@example.com"
	created, err := svc.CreateSSHSigningKey(ctx, u.ID, "test-signing-key", sshKey)
	if err != nil {
		t.Fatalf("CreateSSHSigningKey failed: %v", err)
	}

	// Get SSH signing key
	got, err := svc.GetSSHSigningKey(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetSSHSigningKey failed: %v", err)
	}
	if got.Title != "test-signing-key" {
		t.Errorf("expected title 'test-signing-key', got %s", got.Title)
	}

	// List SSH signing keys
	keys, err := svc.ListSSHSigningKeys(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListSSHSigningKeys failed: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("expected 1 SSH signing key, got %d", len(keys))
	}

	// Delete SSH signing key
	err = svc.DeleteSSHSigningKey(ctx, created.ID)
	if err != nil {
		t.Fatalf("DeleteSSHSigningKey failed: %v", err)
	}

	// Verify deletion
	_, err = svc.GetSSHSigningKey(ctx, created.ID)
	if err == nil {
		t.Error("expected error when getting deleted SSH signing key")
	}
}

// TestGPGKeyCRUD tests the CRUD lifecycle for GPG keys.
func TestGPGKeyCRUD(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user
	svc.DB.Create(&db.User{Login: "gpgkeyuser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "gpgkeyuser")

	// Sample armored GPG public key
	armoredKey := validArmoredGPGKey

	// Create GPG key
	created, err := svc.CreateGPGKey(ctx, u.ID, armoredKey)
	if err != nil {
		t.Fatalf("CreateGPGKey failed: %v", err)
	}
	if created.KeyID == "" {
		t.Error("expected non-empty KeyID for valid armored key")
	}

	// List GPG keys
	keys, err := svc.ListGPGKeys(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListGPGKeys failed: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("expected 1 GPG key, got %d", len(keys))
	}

	// Delete GPG key
	err = svc.DeleteGPGKey(ctx, created.ID)
	if err != nil {
		t.Fatalf("DeleteGPGKey failed: %v", err)
	}

	// Verify deletion
	keys, err = svc.ListGPGKeys(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListGPGKeys after delete failed: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected 0 GPG keys after delete, got %d", len(keys))
	}
}

// TestCreateGPGKey_InvalidArmoredKey tests edge case with invalid armored key.
func TestCreateGPGKey_InvalidArmoredKey(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user
	svc.DB.Create(&db.User{Login: "badgpguser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "badgpguser")

	// Invalid armored key; service stores raw input and leaves KeyID empty.
	invalidKey := "not a valid armored key"
	created, err := svc.CreateGPGKey(ctx, u.ID, invalidKey)
	if err != nil {
		t.Fatalf("CreateGPGKey with invalid key should not fail at service level, got: %v", err)
	}
	if created.PublicKey != invalidKey {
		t.Errorf("expected stored public key to match invalid input, got %s", created.PublicKey)
	}
	// KeyID should be empty for invalid keys
	if created.KeyID != "" {
		t.Errorf("expected empty KeyID for invalid key, got %s", created.KeyID)
	}
}

// TestCreateDeployKey_EmptyTitle tests that empty title falls back to SSH key comment.
func TestCreateDeployKey_EmptyTitle(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user and repo
	svc.DB.Create(&db.User{Login: "emptytitleuser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "emptytitleuser")

	repoName := "test-repo2"
	fullName := u.Login + "/" + repoName
	svc.DB.Create(&db.Repository{
		OwnerID:       u.ID,
		Owner:         u,
		Name:          repoName,
		FullName:      fullName,
		DefaultBranch: "main",
	})

	// Create deploy key with empty title
	sshKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDTest1234567890 my-key-comment"
	created, err := svc.CreateDeployKey(ctx, fullName, "", sshKey, true)
	if err != nil {
		t.Fatalf("CreateDeployKey failed: %v", err)
	}
	if created.Title != "my-key-comment" {
		t.Errorf("expected title 'my-key-comment', got %s", created.Title)
	}
	if !created.ReadOnly {
		t.Error("expected ReadOnly to be true")
	}
}

// TestCreateDeployKey_NoComment tests fallback when SSH key has no comment.
func TestCreateDeployKey_NoComment(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user and repo
	svc.DB.Create(&db.User{Login: "nocommentuser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "nocommentuser")

	repoName := "test-repo3"
	fullName := u.Login + "/" + repoName
	svc.DB.Create(&db.Repository{
		OwnerID:       u.ID,
		Owner:         u,
		Name:          repoName,
		FullName:      fullName,
		DefaultBranch: "main",
	})

	// Create deploy key with no comment
	sshKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDTest1234567890"
	created, err := svc.CreateDeployKey(ctx, fullName, "", sshKey, false)
	if err != nil {
		t.Fatalf("CreateDeployKey failed: %v", err)
	}
	if created.Title != "key" {
		t.Errorf("expected title 'key', got %s", created.Title)
	}
}

// TestGetSSHKey_NotFound tests error handling for non-existent SSH key.
func TestGetSSHKey_NotFound(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	_, err := svc.GetSSHKey(ctx, 99999)
	if err == nil {
		t.Error("expected error for non-existent SSH key")
	}
}

// TestGetSSHSigningKey_NotFound tests error handling for non-existent SSH signing key.
func TestGetSSHSigningKey_NotFound(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	_, err := svc.GetSSHSigningKey(ctx, 99999)
	if err == nil {
		t.Error("expected error for non-existent SSH signing key")
	}
}

// TestDeleteDeployKey_NotFound tests error handling for non-existent deploy key.
func TestDeleteDeployKey_NotFound(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	err := svc.DeleteDeployKey(ctx, 99999)
	if err == nil {
		t.Error("expected error for non-existent deploy key")
	}
}

// TestDeleteSSHKey_NotFound tests error handling for non-existent SSH key.
func TestDeleteSSHKey_NotFound(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	err := svc.DeleteSSHKey(ctx, 99999)
	if err == nil {
		t.Error("expected error for non-existent SSH key")
	}
}

// TestDeleteSSHSigningKey_NotFound tests error handling for non-existent SSH signing key.
func TestDeleteSSHSigningKey_NotFound(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	err := svc.DeleteSSHSigningKey(ctx, 99999)
	if err == nil {
		t.Error("expected error for non-existent SSH signing key")
	}
}

// TestDeleteGPGKey_NotFound tests error handling for non-existent GPG key.
func TestDeleteGPGKey_NotFound(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	err := svc.DeleteGPGKey(ctx, 99999)
	if err == nil {
		t.Error("expected error for non-existent GPG key")
	}
}

// TestListDeployKeys_NonExistentRepo tests listing deploy keys for non-existent repo.
func TestListDeployKeys_NonExistentRepo(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	_, err := svc.ListDeployKeys(ctx, "nonexistent/repo")
	if err == nil {
		t.Error("expected error for non-existent repository")
	}
}

// TestCreateDeployKey_NonExistentRepo tests creating deploy key for non-existent repo.
func TestCreateDeployKey_NonExistentRepo(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	_, err := svc.CreateDeployKey(ctx, "nonexistent/repo", "test", "ssh-rsa AAAAB3", false)
	if err == nil {
		t.Error("expected error for non-existent repository")
	}
}

// TestSSHKey_MultipleKeys tests listing multiple SSH keys for a user.
func TestSSHKey_MultipleKeys(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user
	svc.DB.Create(&db.User{Login: "multikeyuser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "multikeyuser")

	// Create multiple SSH keys
	for i := 1; i <= 3; i++ {
		_, err := svc.CreateSSHKey(ctx, u.ID, "key-"+string(rune('0'+i)), "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDTest1234567890 key"+string(rune('0'+i))+"@example.com")
		if err != nil {
			t.Fatalf("CreateSSHKey %d failed: %v", i, err)
		}
	}

	// List should return all 3 keys
	keys, err := svc.ListSSHKeys(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListSSHKeys failed: %v", err)
	}
	if len(keys) != 3 {
		t.Errorf("expected 3 SSH keys, got %d", len(keys))
	}
}

// TestGPGKey_MultipleKeys tests listing multiple GPG keys for a user.
func TestGPGKey_MultipleKeys(t *testing.T) {
	t.Parallel()
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user
	svc.DB.Create(&db.User{Login: "multigpguser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "multigpguser")

	// Create multiple GPG keys
	armoredKey := validArmoredGPGKey

	for i := 1; i <= 2; i++ {
		_, err := svc.CreateGPGKey(ctx, u.ID, armoredKey)
		if err != nil {
			t.Fatalf("CreateGPGKey %d failed: %v", i, err)
		}
	}

	// List should return all keys
	keys, err := svc.ListGPGKeys(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListGPGKeys failed: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 GPG keys, got %d", len(keys))
	}
}

// TestGPGKeyIDExtraction tests the GPG key ID extraction logic.
func TestGPGKeyIDExtraction(t *testing.T) {
	// Test extractGPGKeyID returns empty for invalid/empty keys
	t.Run("EmptyKeyReturnsEmpty", func(t *testing.T) {
		// The service accepts empty keys but KeyID will be empty
		svc, cleanup := setupTestService(t)
		defer cleanup()
		ctx := context.Background()

		if err := svc.DB.Create(&db.User{Login: "gpgemptyuser", Type: db.TypeUser}).Error; err != nil {
			t.Fatalf("Create user failed: %v", err)
		}

		user, err := svc.GetUser(ctx, "gpgemptyuser")
		if err != nil {
			t.Fatalf("GetUser failed: %v", err)
		}

		gk, err := svc.CreateGPGKey(ctx, user.ID, "")
		if err != nil {
			t.Fatalf("CreateGPGKey failed: %v", err)
		}
		if gk.KeyID != "" {
			t.Errorf("Expected empty KeyID for empty key, got %q", gk.KeyID)
		}
	})

	// Test invalid/malformed GPG armored keys (addresses QG gate review for PR #445)
	t.Run("InvalidArmoredKeyHandled", func(t *testing.T) {
		cases := []struct {
			name string
			key  string
		}{
			{
				name: "wrong headers",
				key: `-----BEGIN WRONG KEY-----

AAAAB3NzaC1yc2EAAAADAQABAAABAQ
-----END WRONG KEY-----`,
			},
			{
				name: "garbage input",
				key:  "not-a-key",
			},
			{
				name: "truncated armor",
				key: `-----BEGIN PGP PUBLIC KEY BLOCK-----

mQINBGQxO4oBEADKz
`,
			},
			{
				name: "invalid base64",
				key: `-----BEGIN PGP PUBLIC KEY BLOCK-----

$$$%
-----END PGP PUBLIC KEY BLOCK-----`,
			},
		}

		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("CreateGPGKey panicked: %v", r)
					}
				}()

				svc, cleanup := setupTestService(t)
				defer cleanup()
				ctx := context.Background()

				if err := svc.DB.Create(&db.User{Login: "gpgbaduser", Type: db.TypeUser}).Error; err != nil {
					t.Fatalf("Create user failed: %v", err)
				}

				user, err := svc.GetUser(ctx, "gpgbaduser")
				if err != nil {
					t.Fatalf("GetUser failed: %v", err)
				}

				gk, err := svc.CreateGPGKey(ctx, user.ID, tc.key)
				if err != nil {
					if strings.TrimSpace(err.Error()) == "" {
						t.Fatalf("expected meaningful error message, got empty error")
					}
					return
				}
				// KeyID should be empty for invalid keys
				if gk.KeyID != "" {
					t.Errorf("expected empty KeyID for invalid key, got %s", gk.KeyID)
				}
			})
		}
	})
}
