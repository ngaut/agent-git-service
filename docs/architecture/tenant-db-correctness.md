# Tenant DB Correctness Rules

This document defines the rules for ensuring tenant-aware database access in the agent-git-service.

## Background

The service supports multi-tenant deployments where each tenant (user/organization) may have their own isolated database. To support this, all database operations must use the tenant-aware `DBForCtx(ctx)` method instead of directly accessing `s.DB`.

## Core Rules

### Rule 1: Always Use DBForCtx

**DO:**
```go
err := s.DBForCtx(ctx).Create(&record).Error
err := s.DBForCtx(ctx).Where("id = ?", id).First(&record).Error
err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error { ... })
```

**DON'T:**
```go
err := s.DB.WithContext(ctx).Create(&record).Error  // ❌ Bypasses tenant isolation
err := s.DB.Create(&record).Error                    // ❌ No context, no tenant
```

### Rule 2: Propagate Tenant DB to Background Goroutines

When spawning background work from a request handler, propagate the tenant DB context:

```go
func (s *Service) DoWork(ctx context.Context, data string) {
    bgCtx := s.ServerCtx()
    if tenantDB, ok := DBFromContext(ctx); ok {
        bgCtx = ContextWithDB(bgCtx, tenantDB)
    }
    
    s.Wg.Add(1)
    go func() {
        defer s.Wg.Done()
        s.backgroundWork(bgCtx, data)
    }()
}
```

**Pattern to follow:**
1. Start with `s.ServerCtx()` for the background context
2. Check if the request context has a tenant DB via `DBFromContext(ctx)`
3. If present, inject it into the background context via `ContextWithDB(bgCtx, tenantDB)`
4. Use the enriched `bgCtx` in the goroutine

### Rule 3: Tests May Use s.DB Directly

Test files (`*_test.go`) may use `s.DB` directly since they typically run with a single shared test database. However, integration tests that verify tenant isolation should use `DBForCtx(ctx)` with a properly configured context.

## Audit Checklist

Before merging any PR that touches service layer code, verify:

- [ ] All DB operations in non-test files use `s.DBForCtx(ctx)`
- [ ] Background goroutines propagate tenant DB context
- [ ] No direct `s.DB.*` calls except in `DBForCtx()` implementation itself
- [ ] New service methods accept `ctx context.Context` as first parameter

## Implementation Details

### DBForCtx Implementation

Located in `internal/service/repo.go`:

```go
func (s *Service) DBForCtx(ctx context.Context) *gorm.DB {
    if db, ok := DBFromContext(ctx); ok {
        return db.WithContext(ctx)
    }
    return s.DB.WithContext(ctx)
}
```

This method:
1. Checks if a tenant-specific DB was injected into the context
2. If yes, returns the tenant DB with the context attached
3. If no, falls back to the default shared DB with context attached

### Context Helpers

Located in `internal/service/context.go` (or similar):

- `DBFromContext(ctx)` - Extract tenant DB from context
- `ContextWithDB(ctx, db)` - Inject tenant DB into context
- `UserFromContext(ctx)` - Extract user from context
- `ContextWithUser(ctx, user)` - Inject user into context

## Known Exceptions

The following are acceptable uses of `s.DB` directly:

1. **DB initialization** (`internal/db/db.go`) - Setup code runs before tenant contexts exist
2. **DBForCtx implementation** - The fallback case in `DBForCtx()` itself
3. **Test files** - Unit tests with shared test DB (but integration tests should use tenant-aware patterns)

## Testing

To verify tenant DB correctness:

1. **Unit tests**: Verify `DBForCtx` returns the correct DB based on context
2. **Integration tests**: Create multiple tenant DBs, verify operations on one tenant don't affect another
3. **Background work tests**: Verify background goroutines use the correct tenant DB

See `internal/service/tenant_db_test.go` for example test patterns.

## Enforcement

- CI should run `grep` checks for forbidden patterns in non-test files
- Code review must verify tenant DB usage
- Static analysis tools can be configured to flag direct `s.DB` access

## Related Docs

- [tenant-git-storage.md](tenant-git-storage.md): tenant-aware Git repository storage rules
- [design/multi-agent.md](../design/multi-agent.md): multi-agent control-plane and tenant-routing model
- [service.md](service.md): service-layer DB access patterns
