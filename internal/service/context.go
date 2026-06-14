package service

import (
	"context"
	"sync"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
)

// ── Per-request repository cache ────────────────────────────────
//
// GetRepo is called 2-4 times per request across handler → service →
// sub-method layers.  This cache deduplicates those calls within a
// single HTTP request.  The cache is NOT shared across requests.

type repoCacheKey struct{}

// RepoAggregates holds the repo-level counter fields used by REST responses.
type RepoAggregates struct {
	ForksCount      int
	OpenIssuesCount int
	StargazersCount int
}

type repoStatsCacheEntry struct {
	hasPermission bool
	permission    RepoPermission
	hasAggregates bool
	aggregates    RepoAggregates
	hasDiskUsage  bool
	diskUsageKB   int
}

type repoCache struct {
	mu sync.Mutex
	// Keyed by repo ID (not fullName) so renames don't leave stale
	// entries under old names.  A secondary name→ID index allows
	// lookup by fullName without scanning.
	repos   map[uint]db.Repository
	nameIdx map[string]uint
	stats   map[uint]repoStatsCacheEntry
}

// ContextWithRepoCache returns a context carrying a fresh per-request
// repo cache.  Call this once per HTTP request (e.g. in middleware).
func ContextWithRepoCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, repoCacheKey{}, &repoCache{
		repos:   make(map[uint]db.Repository),
		nameIdx: make(map[string]uint),
		stats:   make(map[uint]repoStatsCacheEntry),
	})
}

func getRepoCache(ctx context.Context) *repoCache {
	rc, _ := ctx.Value(repoCacheKey{}).(*repoCache)
	return rc
}

func repoCacheGet(ctx context.Context, fullName string) (db.Repository, bool) {
	rc := getRepoCache(ctx)
	if rc == nil {
		return db.Repository{}, false
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	id, ok := rc.nameIdx[fullName]
	if !ok {
		return db.Repository{}, false
	}
	r, ok := rc.repos[id]
	return r, ok
}

func repoCacheSet(ctx context.Context, repo db.Repository) {
	rc := getRepoCache(ctx)
	if rc == nil {
		return
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.repos[repo.ID] = repo
	rc.nameIdx[repo.FullName] = repo.ID
}

func repoPermissionCacheGet(ctx context.Context, repoID uint) (RepoPermission, bool) {
	rc := getRepoCache(ctx)
	if rc == nil || repoID == 0 {
		return RepoPermissionNone, false
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	entry, ok := rc.stats[repoID]
	if !ok || !entry.hasPermission {
		return RepoPermissionNone, false
	}
	return entry.permission, true
}

func repoPermissionCacheSet(ctx context.Context, repoID uint, permission RepoPermission) {
	rc := getRepoCache(ctx)
	if rc == nil || repoID == 0 {
		return
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	entry := rc.stats[repoID]
	entry.permission = permission
	entry.hasPermission = true
	rc.stats[repoID] = entry
}

func repoAggregatesCacheGet(ctx context.Context, repoID uint) (RepoAggregates, bool) {
	rc := getRepoCache(ctx)
	if rc == nil || repoID == 0 {
		return RepoAggregates{}, false
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	entry, ok := rc.stats[repoID]
	if !ok || !entry.hasAggregates {
		return RepoAggregates{}, false
	}
	return entry.aggregates, true
}

func repoAggregatesCacheSet(ctx context.Context, repoID uint, aggregates RepoAggregates) {
	rc := getRepoCache(ctx)
	if rc == nil || repoID == 0 {
		return
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	entry := rc.stats[repoID]
	entry.aggregates = aggregates
	entry.hasAggregates = true
	rc.stats[repoID] = entry
}

func repoDiskUsageCacheGet(ctx context.Context, repoID uint) (int, bool) {
	rc := getRepoCache(ctx)
	if rc == nil || repoID == 0 {
		return 0, false
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	entry, ok := rc.stats[repoID]
	if !ok || !entry.hasDiskUsage {
		return 0, false
	}
	return entry.diskUsageKB, true
}

func repoDiskUsageCacheSet(ctx context.Context, repoID uint, diskUsageKB int) {
	rc := getRepoCache(ctx)
	if rc == nil || repoID == 0 {
		return
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	entry := rc.stats[repoID]
	entry.diskUsageKB = diskUsageKB
	entry.hasDiskUsage = true
	rc.stats[repoID] = entry
}

// RepoCacheInvalidate removes a repo from the per-request cache
// by ID, clearing both the repo entry and ALL name index entries
// that pointed to it.  Safe for renames and deletes.
func RepoCacheInvalidate(ctx context.Context, repoID uint) {
	rc := getRepoCache(ctx)
	if rc == nil {
		return
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	delete(rc.repos, repoID)
	delete(rc.stats, repoID)
	for name, id := range rc.nameIdx {
		if id == repoID {
			delete(rc.nameIdx, name)
		}
	}
}

// ── Anonymous-request marker ───────────────────────────────────
//
// Set by OptionalTokenAuth when no Authorization header is present so that
// the service layer can distinguish "anonymous HTTP request" from "internal
// service call with no user context".

type anonRequestKey struct{}

// ContextWithAnonRequest returns a copy of ctx marked as an anonymous
// (unauthenticated) HTTP request.
func ContextWithAnonRequest(ctx context.Context) context.Context {
	return context.WithValue(ctx, anonRequestKey{}, true)
}

// IsAnonRequest reports whether ctx was marked as an anonymous HTTP request.
func IsAnonRequest(ctx context.Context) bool {
	v, _ := ctx.Value(anonRequestKey{}).(bool)
	return v
}

// dbContextKey is an unexported type for the per-request DB context key.
type dbContextKey struct{}

// ContextWithDB returns a copy of ctx carrying a scoped DB override.
func ContextWithDB(ctx context.Context, db *gorm.DB) context.Context {
	return context.WithValue(ctx, dbContextKey{}, db)
}

// DBFromContext extracts the scoped DB override set by callers or tests.
// Returns the DB and true if present, or nil and false otherwise.
func DBFromContext(ctx context.Context) (*gorm.DB, bool) {
	db, ok := ctx.Value(dbContextKey{}).(*gorm.DB)
	return db, ok
}
