// Package gitstore provides git repository storage.
package gitstore

import (
	"os"
	"testing"
)

// NewTestStore creates a test gitstore with a temporary directory that has
// predictable permissions (0750). Returns the store and a cleanup function.
// The cleanup function must be called to remove the temporary directory.
func NewTestStore(t *testing.T) (*Store, func()) {
	t.Helper()
	return NewTestStoreWithMode(t, 0o750)
}

// NewTestStoreWithMode creates a test gitstore with custom directory permissions.
// The cleanup function must be called to remove the temporary directory.
func NewTestStoreWithMode(t *testing.T, mode os.FileMode) (*Store, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "gitstore-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	// Set predictable permissions
	if err := os.Chmod(tmpDir, mode); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to set permissions on temp dir: %v", err)
	}

	store, err := New(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to create gitstore: %v", err)
	}

	cleanup := func() {
		os.RemoveAll(tmpDir)
	}

	return store, cleanup
}
