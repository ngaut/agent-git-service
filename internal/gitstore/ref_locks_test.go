package gitstore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gh-server/internal/gitstore"
)

func TestRepairRefLock_ClearsStaleLock(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-ref-lock-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo := "user/ref-lock-stale"
	if err := store.Init(ctx, repo, "main", true); err != nil {
		t.Fatalf("Init: %v", err)
	}
	repoDir, err := store.GetRepoPath(ctx, repo)
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	lockPath := filepath.Join(repoDir, "refs", "heads", "main.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("lock"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	stale := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	result, err := store.RepairRefLock(ctx, repo, "refs/heads/main", 5*time.Minute, false)
	if err != nil {
		t.Fatalf("RepairRefLock: %v", err)
	}
	if !result.Present || !result.Cleared {
		t.Fatalf("result = %+v, want present+cleared", result)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock still present, stat err = %v", err)
	}
}

func TestRepairRefLock_RejectsFreshLockWithoutForce(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-ref-lock-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo := "user/ref-lock-fresh"
	if err := store.Init(ctx, repo, "main", true); err != nil {
		t.Fatalf("Init: %v", err)
	}
	repoDir, err := store.GetRepoPath(ctx, repo)
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	lockPath := filepath.Join(repoDir, "refs", "heads", "main.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("lock"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := store.RepairRefLock(ctx, repo, "refs/heads/main", 5*time.Minute, false)
	if !errors.Is(err, gitstore.ErrRefLockActive) {
		t.Fatalf("RepairRefLock err = %v, want ErrRefLockActive", err)
	}
	if !result.Present || result.Cleared {
		t.Fatalf("result = %+v, want present and not cleared", result)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock should remain, stat err = %v", err)
	}
}
