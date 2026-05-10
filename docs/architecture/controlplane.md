# Control Plane Architecture

This document describes the control plane subsystem for multi-agent support in `agent-git-service`.

## Purpose

The control plane provides a separate database-backed registry for managing agent users, authentication tokens, and tenant database mappings. It enables multi-tenant deployments where each agent has an isolated tenant database while sharing a common control plane for authentication and routing.

## Authority Split

- **Control Plane DB**: Authoritative for agent identity, token validation, and tenant DSN mappings
- **Tenant DB**: Authoritative for application data (users, repositories, issues, PRs, workflows)
- **DBRouter**: Coordinates flows that need both control plane and tenant database access

This matches the multi-agent design in [design/multi-agent.md](../design/multi-agent.md): the control plane answers "who is this agent?" and "which tenant DB should serve this request?", while service-layer business state remains tenant-local.

## Components

### Models (`internal/controlplane/models.go`)

| Model | Purpose |
|---|---|
| `CPUser` | Global registry entry for an agent; stores encrypted tenant DSN and lifecycle state |
| `CPToken` | Maps auth tokens to control plane users for authentication |

### `CPUser` State Machine

`CPUser.State` represents whether the agent may serve traffic in multi-tenant mode:

```text
pending --> active
pending --> failed
failed --> pending
failed --> active
active --> failed
```

Transitions are validated by `TransitionTo()` to prevent invalid state changes.

### DBRouter (`internal/controlplane/router.go`)

The `DBRouter` resolves authentication tokens to tenant databases:

1. **Token Lookup**: Query control plane DB for `CPToken` and preload `CPUser`
2. **Agent State Check**: In multi-tenant mode, require `CPUser.State == active`
3. **Tenant DB Resolution**: Return cached tenant DB connection or open a new one from the agent DSN
4. **Tenant User Sync**: Ensure the matching `db.User` exists in the tenant DB

#### Caching Strategy

- Tenant DB connections are cached by `CPUser.ID`
- `singleflight.Group` serializes concurrent connection opens for the same agent
- Capacity-limited cache (`MaxAgents`, default `100`) counts both cached and in-flight opens
- Pool configuration per tenant defaults to `MaxOpenConns=5`, `MaxIdleConns=2`, `ConnMaxLifetime=30m`
- `Close()` drains all cached tenant DB handles during shutdown

#### Multi-Tenant Mode

When `multiTenantMode=true`:

- Token resolution requires `CPUser.State == AgentStateActive`
- Inactive agents (`pending` or `failed`) are rejected with `ErrInactiveUser`

When `multiTenantMode=false` (single-DB development fallback):

- State checks are bypassed for local development convenience
- The broader system still falls back to the base service DB as described in [design/multi-agent.md](../design/multi-agent.md)

## API Contracts

### `RouterConfig`

```go
type RouterConfig struct {
    MaxOpenConns    int           // per-tenant; default 5
    MaxIdleConns    int           // per-tenant; default 2
    ConnMaxLifetime time.Duration // per-tenant; default 30m
    MaxAgents       int           // cache cap; default 100
}
```

### Public Methods

| Method | Purpose |
|---|---|
| `ResolveToken(ctx, token)` | Resolve token and return tenant-scoped `db.User` plus `*gorm.DB` |
| `CreateUser(ctx, login, email)` | Create `CPUser` with placeholder encrypted DSN and pending agent state |
| `CreateToken(ctx, cpUserID, tokenValue)` | Create auth token for agent |
| `GetUserByLogin(ctx, login)` | Retrieve `CPUser` by login |
| `GetUserByID(ctx, id)` | Retrieve `CPUser` by ID |
| `DeleteUser(ctx, cpUserID)` | Delete `CPUser` and associated tokens |
| `DeleteToken(ctx, cpUserID)` | Delete all tokens for agent |
| `PingCP(ctx)` | Verify control plane DB connectivity |
| `Close()` | Drain all cached tenant DB connections |

## Dependency Rules

| Layer | May Call | Must Not Call |
|---|---|---|
| `controlplane` | `db`, `crypto`, GORM | `rest`, `graphql`, `gitstore`, HTTP helpers |
| `middleware` | `controlplane.DBRouter` (via service wiring) | Direct GORM queries on control plane tables |
| `service` | Request-scoped tenant DB from context | Direct control plane table access for request routing |

These boundaries align with [module-contracts.md](../module-contracts.md): request routing and auth extraction stay outside business logic, while `controlplane` owns token-to-tenant resolution.

## Current State

- Control plane schema and `DBRouter` exist in the codebase today
- Router and middleware wiring already accept a `*controlplane.DBRouter`
- Token validation still supports single-DB fallback for development compatibility
- The repo design baseline remains that multi-agent routing is still being integrated incrementally rather than fully replacing all production paths

## Testing Implications

- Unit tests should use SQLite for the control plane DB
- Router tests should verify token lookup, inactive-agent rejection, and tenant user auto-creation
- Integration tests should verify token-to-tenant DB resolution and cross-tenant isolation
- State machine tests should cover valid and invalid `CPUser` transitions
## Related Documents

- [Multi-Agent Architecture](../design/multi-agent.md) - tenant routing design
- [Module Contracts](../module-contracts.md) - layer boundaries and ownership
- [Service Component Reference](service.md) - request-scoped DB access expectations
