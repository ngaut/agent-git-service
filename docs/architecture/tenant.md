# Tenant Architecture

This document describes the tenant-context subsystem for multi-tenant `agent-git-service` deployments.

This document records the current implementation. Broader multi-agent design lives in [design/multi-agent.md](../design/multi-agent.md).

## Purpose

`internal/tenant` defines the shared request-context contract for tenant identity.
It allows control-plane-authenticated requests to keep a logical repository name such as `owner/repo` while resolving tenant-scoped physical resources such as:

- tenant-local database handles injected by middleware
- tenant-local Git repository roots under `GIT_REPO_DIR/{tenant}/...`

The tenant string is an infrastructure routing hint.
It is not the authority for relational DB access; the request-scoped `*gorm.DB` injected by middleware is.

## Authority Split

- **`tenant` package**: authoritative for the tenant context key and low-level read/write helpers
- **`service` package**: exposes compatibility wrappers that delegate to `tenant` so all callers share one key
- **`controlplane.DBRouter`**: authoritative for token-to-tenant user and tenant DB resolution
- **`middleware/auth`**: authoritative writer for request-scoped tenant values in control-plane mode
- **`gitstore`**: authoritative consumer for tenant-scoped Git roots and per-tenant repository locks

## Components

### Shared Context Key (`internal/tenant/tenant.go`)

```go
type tenantKey struct{}

func ContextWithTenant(ctx context.Context, tenant string) context.Context
func FromContext(ctx context.Context) (string, bool)
```

The key type is unexported so only the `tenant` package can define the storage slot.
`FromContext` treats empty strings as missing tenants.

### Service Compatibility Wrappers (`internal/service/context.go`)

```go
func ContextWithTenant(ctx context.Context, t string) context.Context
func TenantFromContext(ctx context.Context) (string, bool)
```

These wrappers delegate directly to `internal/tenant` so middleware, service, and gitstore all observe the same value.
`internal/service/context_test.go` guards this single-key contract.

### Middleware Injection (`internal/middleware/auth.go`)

In control-plane mode, `resolveTokenAndInjectContext()` currently does:

1. `router.ResolveToken(ctx, token)` to resolve the tenant user and tenant DB
2. `service.ContextWithDB(...)`
3. `service.ContextWithUser(...)`
4. `service.ContextWithTenant(..., user.Login)`
5. `service.ContextWithRepoCache(...)`

In single-DB mode, middleware injects user and repo cache but not tenant context.

### Git Store Consumption (`internal/gitstore/store.go`)

`gitstore.Store` supports tenant-aware pathing via:

- `WithTenantIsolation()`
- `WithDefaultTenant(tenant)`
- `RepoRoot(ctx)`
- `GetRepoPath(ctx, fullName)`

When tenant isolation is enabled, `RepoRoot` resolves to `GIT_REPO_DIR/{tenant}`.
`repoLock()` also folds tenant into its lock key so two tenants do not serialize writes against the same logical `owner/repo`.

## Context Propagation Flow

1. A control-plane-authenticated request enters `TokenAuth` or `OptionalTokenAuth`.
2. `DBRouter.ResolveToken()` returns the tenant-scoped `db.User` and `*gorm.DB`.
3. Auth middleware injects the tenant DB, current user, and tenant string (`user.Login`) into the request context.
4. REST, GraphQL, and Git HTTP handlers pass that context into service and gitstore calls.
5. `gitstore` reads tenant from context to resolve the physical repository root and repo-level lock key.

The same request context must be preserved across any downstream call that needs tenant-aware Git access.
Database access uses the request-scoped `*gorm.DB`, not the tenant string.

## Multi-Tenant Resource Scoping

### Git Storage

Single-DB mode:

```text
GIT_REPO_DIR/{owner}/{repo}.git
```

Control-plane mode:

```text
GIT_REPO_DIR/{tenant}/{owner}/{repo}.git
```

`server` enables this mode by constructing the store with `gitstore.WithTenantIsolation()` and `gitstore.WithDefaultTenant("default")` when `CONTROL_PLANE_DSN` is set.

If tenant isolation is enabled and no tenant is present in the context:

- `gitstore` uses the configured default tenant when one exists
- otherwise path resolution fails with `missing tenant in context`

### Database Routing

Tenant DB selection is handled by `controlplane.DBRouter`, not by `internal/tenant`.

- `DBRouter.ResolveToken()` returns a tenant-scoped `*gorm.DB`
- middleware injects that DB with `service.ContextWithDB`
- service code reads it via `DBFromContext` and `DBForCtx`

This keeps DB routing and Git path routing separate: the DB handle is authoritative for relational data, while the tenant string is authoritative for tenant-scoped Git storage.

## API Contracts

| Function | Purpose |
|---|---|
| `tenant.ContextWithTenant(ctx, tenant)` | Write tenant identity into context |
| `tenant.FromContext(ctx)` | Read tenant identity from context |
| `service.ContextWithTenant(ctx, tenant)` | Compatibility wrapper over `tenant.ContextWithTenant` |
| `service.TenantFromContext(ctx)` | Compatibility wrapper over `tenant.FromContext` |

`tenant.FromContext()` returns `(tenant, ok)`:

- `ok=true`: tenant present and non-empty
- `ok=false`: tenant missing, empty, or wrong type

## Dependency Rules

| Layer | May Call | Must Not Call |
|---|---|---|
| `tenant` | stdlib `context` only | other internal packages |
| `service/context` | `tenant` | define its own tenant key |
| `middleware` | `service.ContextWithTenant`, `service.ContextWithDB`, `controlplane.DBRouter` | direct tenant-key manipulation |
| `gitstore` | `tenant.FromContext` | control-plane DB queries or auth logic |

## Current State

- `internal/tenant/tenant.go` exists and owns the shared context key today
- `internal/service/context.go` already delegates tenant helpers to `internal/tenant`
- `internal/middleware/auth.go` already injects tenant context for control-plane requests using `user.Login`
- `server` already enables tenant-isolated git storage when `CONTROL_PLANE_DSN` is configured
- `internal/gitstore/store.go` already enforces tenant-aware repo roots, path validation, and per-tenant lock isolation
- single-DB mode still uses the flat `GIT_REPO_DIR/{owner}/{repo}.git` layout and does not require tenant context

## Testing Implications

- unit tests should verify `ContextWithTenant` / `FromContext` round-trip and empty-string rejection
- service tests should keep guarding the single shared key contract between `service` and `tenant`
- auth middleware tests should verify control-plane requests receive tenant DB, current user, and tenant context
- gitstore tests should cover explicit tenant, default tenant fallback, missing tenant failure, invalid tenant values, tenant-scoped paths, and per-tenant lock isolation
- integration tests should verify two tenants can reuse the same logical `owner/repo` name without sharing physical Git storage or relational state

## Related Documents

- [Control Plane Architecture](controlplane.md) - token-to-tenant user and DB resolution
- [Git Store Architecture](gitstore.md) - tenant-scoped repository roots and bare-repo operations
- [Service Component Reference](service.md) - request-scoped DB access and service context helpers
- [Tenant-Aware Git Storage Contract](tenant-git-storage.md) - lower-level Git storage rules and migration notes
- [Tenant DB Correctness](tenant-db-correctness.md) - DB access rules for request and background contexts
- [Multi-Agent Architecture](../design/multi-agent.md) - broader tenant routing and isolation design
- [Module Contracts](../module-contracts.md) - layer boundaries and ownership
