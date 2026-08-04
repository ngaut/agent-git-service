package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
)

func TestRepoCacheIndexesCanonicalAndLookupNames(t *testing.T) {
	ctx := ContextWithRepoCache(context.Background())
	repo := db.Repository{ID: 42, FullName: "new-owner/wiki"}

	repoCacheSetForLookup(ctx, "old-owner/wiki", repo)

	for _, name := range []string{"new-owner/wiki", "old-owner/wiki"} {
		got, ok := repoCacheGet(ctx, name)
		if !ok {
			t.Fatalf("repoCacheGet(%q) missed", name)
		}
		if got.ID != repo.ID || got.FullName != repo.FullName {
			t.Fatalf("repoCacheGet(%q) = %+v, want %+v", name, got, repo)
		}
	}

	RepoCacheInvalidate(ctx, repo.ID)
	for _, name := range []string{"new-owner/wiki", "old-owner/wiki"} {
		if _, ok := repoCacheGet(ctx, name); ok {
			t.Fatalf("repoCacheGet(%q) hit after invalidation", name)
		}
	}
}

func TestRepoIdentityCacheDoesNotReplaceCompleteRepo(t *testing.T) {
	ctx := ContextWithRepoCache(context.Background())
	identity := db.Repository{
		ID:       42,
		FullName: "new-owner/wiki",
		OwnerID:  9,
		Private:  true,
	}

	repoIdentityCacheSetForLookup(ctx, "old-owner/wiki", identity)
	for _, name := range []string{"new-owner/wiki", "old-owner/wiki"} {
		got, ok := repoIdentityCacheGet(ctx, name)
		if !ok || got.ID != identity.ID {
			t.Fatalf("repoIdentityCacheGet(%q) = %+v, %v; want repo %d", name, got, ok, identity.ID)
		}
		if _, ok := repoCacheGet(ctx, name); ok {
			t.Fatalf("repoCacheGet(%q) accepted an identity-only entry", name)
		}
	}

	complete := identity
	complete.Owner = db.User{ID: identity.OwnerID, Login: "new-owner"}
	repoCacheSetForLookup(ctx, "old-owner/wiki", complete)

	// A later identity-only lookup must not downgrade the complete entry.
	repoIdentityCacheSetForLookup(ctx, "new-owner/wiki", identity)
	got, ok := repoCacheGet(ctx, "old-owner/wiki")
	if !ok {
		t.Fatal("repoCacheGet missed the complete entry")
	}
	if got.Owner.Login != complete.Owner.Login {
		t.Fatalf("repoCacheGet owner = %q, want %q", got.Owner.Login, complete.Owner.Login)
	}

	RepoCacheInvalidate(ctx, identity.ID)
	if _, ok := repoIdentityCacheGet(ctx, identity.FullName); ok {
		t.Fatal("repoIdentityCacheGet hit after invalidation")
	}
}

func TestGetRepoBaseUsesAuthorizedRequestCache(t *testing.T) {
	ctx := ContextWithRepoCache(context.Background())
	ctx = ContextWithUser(ctx, db.User{ID: 7, Login: "writer"})
	repo := db.Repository{ID: 42, FullName: "new-owner/wiki", OwnerID: 9}
	repoCacheSetForLookup(ctx, "old-owner/wiki", repo)
	repoPermissionCacheSet(ctx, repo.ID, RepoPermissionWrite)

	// A nil DB makes any fallback query fail immediately, proving the alias
	// resolves entirely through the already-authorized request cache.
	svc := &Service{}
	got, err := svc.getRepoBase(ctx, "old-owner/wiki")
	if err != nil {
		t.Fatalf("getRepoBase: %v", err)
	}
	if got.ID != repo.ID || got.FullName != repo.FullName {
		t.Fatalf("getRepoBase = %+v, want %+v", got, repo)
	}
}

func TestRepoPermissionCachePreservesInsufficientPermission(t *testing.T) {
	ctx := ContextWithRepoCache(context.Background())
	repoPermissionCacheSet(ctx, 42, RepoPermissionRead)

	got, ok := repoPermissionCacheGet(ctx, 42)
	if !ok {
		t.Fatal("repoPermissionCacheGet missed")
	}
	if got.AtLeast(RepoPermissionWrite) {
		t.Fatalf("cached permission %q unexpectedly grants write", got)
	}
}

func TestWikiCatalogFreshnessCheckRunsOncePerRequest(t *testing.T) {
	ctx := ContextWithRepoCache(context.Background())
	calls := 0
	check := func() error {
		calls++
		return nil
	}

	if err := wikiCatalogFreshnessCacheDo(ctx, "Acme/Widgets", check); err != nil {
		t.Fatalf("first freshness check: %v", err)
	}
	if err := wikiCatalogFreshnessCacheDo(ctx, "acme/widgets", check); err != nil {
		t.Fatalf("cached freshness check: %v", err)
	}
	if calls != 1 {
		t.Fatalf("freshness check calls = %d, want 1", calls)
	}

	if err := wikiCatalogFreshnessCacheDo(ContextWithRepoCache(context.Background()), "acme/widgets", check); err != nil {
		t.Fatalf("new request freshness check: %v", err)
	}
	if calls != 2 {
		t.Fatalf("freshness check calls after new request = %d, want 2", calls)
	}
}

func TestWikiCatalogFreshnessCheckCoalescesConcurrentCalls(t *testing.T) {
	ctx := ContextWithRepoCache(context.Background())
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	check := func() error {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil
	}

	const callers = 16
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- wikiCatalogFreshnessCacheDo(ctx, "acme/widgets", check)
		}()
	}
	<-started
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("freshness check: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("freshness check calls = %d, want 1", got)
	}
}
