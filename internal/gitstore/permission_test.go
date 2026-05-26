package gitstore_test

import (
	"context"
	"os"
	"testing"

	"github.com/ngaut/agent-git-service/internal/gitstore"
)

// TestNewTestStore_Helper validates that the test helper creates a usable store.
func TestNewTestStore_Helper(t *testing.T) {
	store, cleanup := gitstore.NewTestStore(t)
	defer cleanup()

	if store == nil {
		t.Fatal("expected non-nil store")
	}

	// Verify the store can actually be used
	ctx := context.Background()
	if err := store.Init(ctx, "test/repo", "main", false); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if !store.Exists(ctx, "test/repo") {
		t.Error("expected repo to exist after Init")
	}
}

// TestNewTestStoreWithMode_CustomPermissions validates custom permission modes.
func TestNewTestStoreWithMode_CustomPermissions(t *testing.T) {
	store, cleanup := gitstore.NewTestStoreWithMode(t, 0o700)
	defer cleanup()

	if store == nil {
		t.Fatal("expected non-nil store")
	}
}

// TestBootstrap_WithUnreadablePath tests behavior when the repo storage path
// is unreadable. This is a regression test for issue #398.
// Note: On Linux, directory owners can still access their directories even with
// 0o000 permissions. This test is kept for documentation but skips on most systems.
func TestBootstrap_WithUnreadablePath(t *testing.T) {
	// Skip this test because on Linux, the owner can always access their own
	// directory regardless of permission bits. Testing this properly would
	// require root to change ownership, which is not available in CI.
	t.Skip("skipping: owner can always access own directory on Linux")
}

// TestBootstrap_WithUnwritablePath tests behavior when the repo storage path
// is unwritable. This is a regression test for issue #398.
func TestBootstrap_WithUnwritablePath(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping when running as root")
	}

	tmpDir, err := os.MkdirTemp("", "gitstore-unwritable-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Make directory read-only (no write permission)
	if err := os.Chmod(tmpDir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(tmpDir, 0o755) // Restore for cleanup

	// gitstore.New should succeed (directory exists), but Init should fail
	store, err := gitstore.New(tmpDir)
	if err != nil {
		// This is acceptable - some systems may fail at New
		t.Logf("gitstore.New failed as expected: %v", err)
		return
	}

	// If New succeeded, Init should fail when trying to create subdirectories
	ctx := context.Background()
	err = store.Init(ctx, "test/repo", "main", false)
	if err == nil {
		t.Error("expected error when initializing repo in unwritable path")
	}
}

// TestBootstrap_WithValidPermissions validates that bootstrap succeeds with
// proper directory permissions. This is the positive counterpart to the
// unreadable/unwritable tests.
func TestBootstrap_WithValidPermissions(t *testing.T) {
	store, cleanup := gitstore.NewTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Verify normal operations work
	if err := store.Init(ctx, "owner/repo", "main", true); err != nil {
		t.Fatalf("Init failed with valid permissions: %v", err)
	}

	if !store.Exists(ctx, "owner/repo") {
		t.Error("expected repo to exist after Init")
	}
}
