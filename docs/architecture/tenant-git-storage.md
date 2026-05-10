# Tenant-Aware Git Storage Contract

## Overview

When control-plane mode is enabled (`CONTROL_PLANE_DSN` is set), Git Smart HTTP and on-disk git storage become tenant-aware. This provides isolation between tenants at the physical storage level.

## Storage Layout

### Control-Plane Mode (Multi-Tenant)

```
GIT_REPO_DIR/
├── {tenant1}/
│   ├── {owner1}/
│   │   ├── {repo1}.git/
│   │   └── {repo2}.git/
│   └── {owner2}/
│       └── {repo1}.git/
├── {tenant2}/
│   ├── {owner1}/
│   │   └── {repo1}.git/  # Physically separate from tenant1's owner1/repo1
│   └── ...
└── default/               # Fallback for unauthenticated/background contexts
    └── ...
```

### Single-DB Mode (Legacy)

```
GIT_REPO_DIR/
├── {owner1}/
│   ├── {repo1}.git/
│   └── {repo2}.git/
└── {owner2}/
    └── {repo1}.git/
```

## Tenant Resolution Flow

1. **Authentication**: When a request arrives with an auth token, the `TokenAuth` middleware:
   - In control-plane mode: calls `DBRouter.ResolveToken()` which returns the tenant user and tenant-scoped DB
   - Injects the tenant identifier (`user.Login`) into the request context via `ContextWithTenant()`

2. **Git Operations**: When Git Smart HTTP handlers (`InfoRefs`, `UploadPack`, `ReceivePack`) are invoked:
   - The tenant is already present in the request context
   - `gitstore.Store.repoPath()` resolves the physical path as `{GIT_REPO_DIR}/{tenant}/{owner}/{repo}.git`

3. **Fallback**: If no tenant is in the context (background jobs, unauthenticated requests):
   - The `default` tenant is used (configurable via `WithDefaultTenant()`)

## API Contract

### Git Store

```go
// New creates a Store with optional tenant isolation.
func New(dir string, opts ...Option) (*Store, error)

// Options:
// - WithTenantIsolation(): Enable tenant-aware paths
// - WithDefaultTenant(tenant): Set fallback tenant for unauthenticated contexts

// Methods accept context with optional tenant:
func (s *Store) Init(ctx context.Context, fullName, defaultBranch string, seed bool) error
func (s *Store) Exists(ctx context.Context, fullName string) bool
func (s *Store) GetRepoPath(ctx context.Context, fullName string) (string, error)
func (s *Store) RepoRoot(ctx context.Context) (string, error)
```

### Context Helpers

```go
// Inject tenant into context (called by auth middleware)
func ContextWithTenant(ctx context.Context, tenant string) context.Context

// Extract tenant from context
func TenantFromContext(ctx context.Context) (string, bool)
```

## Security Guarantees

1. **Tenant Isolation**: Two tenants can use the same logical `owner/repo` name without sharing the same bare repo on disk.

2. **Auth Required**: Git clone/fetch/push in control-plane mode require valid authentication. The tenant is resolved from the auth token.

3. **No Cross-Tenant Access**: Unauthenticated or wrong-tenant git requests cannot reach another tenant's repository data because:
   - The auth middleware rejects invalid tokens
   - The gitstore resolves paths based on the authenticated tenant

4. **Background Jobs**: Post-push hooks and other background operations propagate the tenant context to ensure they operate on the correct tenant's data.

## Migration Notes

- Existing repos in flat layout (`{owner}/{repo}.git`) are NOT automatically migrated
- New repos created in control-plane mode use tenant-aware layout
- To migrate existing repos, manually move them to `{tenant}/{owner}/{repo}.git` structure

## Configuration

### Environment Variables

- `CONTROL_PLANE_DSN`: When set, enables control-plane mode with tenant isolation
- `GIT_REPO_DIR`: Root directory for git repositories

### Code Configuration

```go
// In main.go:
var gitOpts []gitstore.Option
if cfg.ControlPlaneDSN != "" {
    gitOpts = append(gitOpts, 
        gitstore.WithTenantIsolation(), 
        gitstore.WithDefaultTenant("default"))
}
store, err := gitstore.New(cfg.GitRepoDir, gitOpts...)
```
