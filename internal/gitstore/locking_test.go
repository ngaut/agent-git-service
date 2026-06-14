package gitstore

import (
	"context"
	"testing"
	"time"
)

func TestStoreRepoLock_SerializesSameRepo(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New store failed: %v", err)
	}

	ctx := context.Background()
	mu := store.repoLock(ctx, "owner/repo")
	mu.Lock()

	acquired := make(chan struct{})
	go func() {
		mu2 := store.repoLock(ctx, "owner/repo")
		mu2.Lock()
		close(acquired)
		mu2.Unlock()
	}()

	select {
	case <-acquired:
		mu.Unlock()
		t.Fatal("expected lock to block for same repo")
	case <-time.After(200 * time.Millisecond):
	}

	mu.Unlock()

	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("expected lock to be acquired after unlock")
	}
}

func TestStoreRepoLock_IsolatesDifferentRepos(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New store failed: %v", err)
	}

	ctx := context.Background()
	mu := store.repoLock(ctx, "owner/repo-a")
	mu.Lock()
	defer mu.Unlock()

	acquired := make(chan struct{})
	go func() {
		mu2 := store.repoLock(ctx, "owner/repo-b")
		mu2.Lock()
		close(acquired)
		mu2.Unlock()
	}()

	select {
	case <-acquired:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected different repo lock not to block")
	}
}
