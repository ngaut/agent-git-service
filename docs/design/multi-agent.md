# Multi-Agent agent-git-service Architecture

`agent-git-service` runs as a shared, stateless service that supports multiple independent agents. Each agent has its own TiDB Cloud Starter database for metadata isolation and accesses git repositories through a shared POSIX filesystem. Public repository identity stays GitHub-compatible (`owner/repo`), while Git authentication and physical repository storage become tenant-aware.

This document describes the multi-agent target architecture. Until control-plane routing is configured, `agent-git-service` must continue to support the current single-DB development mode as a backward-compatible fallback.

## Product Focus

Multi-agent mode remains **GitHub-compatible API-server centric**:

- The primary product surface is GitHub-compatible auth, repo, issue, PR, release, workflow, and related API operations.
- The primary user-visible entry points are GitHub-compatible clients, including `gh`, and the REST discovery/auth endpoints `/api/v3/`, `/api/v3/meta`, and `/api/v3/rate_limit`.
- Git Smart HTTP remains necessary for repository content sync, branch advancement, and other Git-native steps behind those API workflows.
- Multi-agent mode does **not** expand the product goal into a general-purpose Git hosting platform. Git transport should be implemented and hardened only as far as needed to support the target GitHub-compatible workflows safely in multi-agent deployments.

## User-Facing Entry Points

Users should primarily experience the system through GitHub-compatible clients, including `gh`, and REST discovery flows:

- token login, credential setup, repo, issue, PR, release, and workflow operations, including `gh auth`, `gh repo`, `gh issue`, and `gh pr`
- `GET /api/v3/` for top-level API discovery
- `GET /api/v3/meta` for Git credential capability discovery
- `GET /api/v3/rate_limit` for GitHub-compatible API probing and smoke validation

Git Smart HTTP is still exposed, but in the intended product story it is usually reached indirectly after the user has already started with an API client or the `/api/v3/*` discovery endpoints.

## Design Principles

1. **Stateless compute.** Server instances hold no local state. Any instance can serve any agent. The cluster scales horizontally behind a load balancer.

2. **One agent = one user.** Each agent is a User with its own token and its own TiDB Cloud Starter database.

3. **Shared token resolution.** All authenticated surfaces resolve the caller through the control plane. REST and GraphQL accept `Authorization: token ...` or `Bearer ...`; Git Smart HTTP accepts HTTPS Basic Auth and treats the Basic password as the token so standard Git clients work without a custom protocol.

4. **Per-request DB routing.** After token resolution, the server looks up the caller's TiDB DSN from the control plane, opens or reuses that agent's `*gorm.DB`, and injects it into the request context via `service.ContextWithDB(ctx, db)`. Service code reads the active database through `s.DBForCtx(ctx)` / `service.DBFromContext(ctx)` instead of calling `s.DB.WithContext(ctx)` directly.

5. **Physical data isolation.** Each agent's queries go to their own TiDB database. Agent A cannot see Agent B's metadata — isolation is at the database level. Git content must also be isolated physically: multi-agent deployments must store bare repos under a tenant-scoped path such as `GIT_REPO_DIR/{tenant}/{owner}/{repo}.git`.

6. **POSIX git storage.** Git repositories are bare repos on a POSIX filesystem (`GIT_REPO_DIR`). In production, a distributed POSIX-compatible filesystem (JuiceFS backed by S3) is mounted at `GIT_REPO_DIR`, providing shared access across all server instances. JuiceFS is transparent to the application; the application still sees ordinary local paths.

## Architecture Overview

```
                        ┌─────────────────────────────────────────┐
                        │    agent-git-service cluster            │
                        │        (stateless compute)              │
                        │                                         │
Agent A ── token/basic ─>│  auth: token/basic → control plane DB  │
Agent B ── token/basic ─>│    → resolve tenant + user + TiDB DSN  │
Agent C ── token/basic ─>│    → inject per-request DB connection  │
                        │                                         │
                        │  service layer: DB from context         │
                        │    → queries go to that agent's TiDB    │
                        │                                         │
                        │  gitstore: read/write GIT_REPO_DIR      │
                        │    → {tenant}/{owner}/{repo}.git        │
                        └──────┬──────────────┬───────────────────┘
                               │              │
              ┌────────────────┘              └──────────────┐
              v                                              v
   ┌─────────────────────┐                      ┌────────────────────┐
   │  Control Plane DB   │                      │  Shared Filesystem │
   │  (shared TiDB)      │                      │  (JuiceFS / S3)    │
   │                     │                      │                    │
   │  - users            │                      │  tenant-a/acme/api.git │
   │  - tokens           │                      │  tenant-b/acme/api.git │
   │  - user → DSN map   │                      │  tenant-c/acme/api.git │
   └─────────────────────┘                      └────────────────────┘
              │
   ┌──────────┼──────────┐
   v          v          v
┌───────┐ ┌───────┐ ┌───────┐
│TiDB-A │ │TiDB-B │ │TiDB-C │  one TiDB Cloud Starter per agent
│Agent A│ │Agent B│ │Agent C│
│ data  │ │ data  │ │ data  │
└───────┘ └───────┘ └───────┘
```

## Three Layers of State

| Layer | Storage | Content | Scope |
|---|---|---|---|
| Control plane | Shared TiDB | Users, tokens, user → TiDB DSN mapping | Global (all agents) |
| Agent metadata | Per-agent TiDB Starter | Repositories, issues, PRs, labels, workflows, reviews, etc. | Per agent (isolated) |
| Git data | Shared POSIX filesystem (JuiceFS) | Bare repos: commits, trees, blobs, refs | Per agent by physical path (for example `{tenant}/{owner}/{repo}.git`) |

Git is authoritative for Git-native state (content, history, refs, diffs, merges). The relational database is authoritative for metadata (users, issues, PRs, reviews, labels, workflows). The service layer coordinates both.

## Control Plane

The control plane is a shared TiDB instance that stores the global user registry:

- **Users** — agent identity: login, email, TiDB DSN
- **Tokens** — authentication credentials mapped to users

The control plane is the single source of truth for "which agent exists" and "where is their data." It is queried on every authenticated request to resolve the token to a user and their database.

## Authentication Surfaces

- **REST and GraphQL** use the existing GitHub-style API auth headers: `Authorization: token ...` or `Bearer ...`.
- **Git Smart HTTP** uses HTTPS Basic Auth. The Basic password is treated as the same token used for API auth. The Basic username is transport-only and may be either the real login or `x-access-token`, matching common GitHub tooling behavior and `gh auth setup-git`.
- **Shared resolver** means both surfaces end at the same control-plane lookup: token → tenant + user + TiDB DSN.

## Request Flow

```
Discovery/bootstrap request
1. Agent or operator verifies the server through `GET /api/v3/`
2. `gh auth setup-git` and Git credential setup probe `GET /api/v3/meta`
3. Other clients may probe `GET /api/v3/rate_limit`

API request
1. Agent sends HTTP request with Bearer/token auth
2. Middleware extracts the token and queries control plane: token → tenant + user + TiDB DSN
3. `agent-git-service` obtains the cached `*gorm.DB` for that tenant and injects user + DB into context
4. Service layer retrieves the DB via `s.DBForCtx(ctx)` / `service.DBFromContext(ctx)`
5. All GORM queries execute against that tenant's TiDB

Git request
1. `git` sends HTTPS Basic Auth during clone/fetch/push
2. Git auth wrapper extracts the Basic password as the token
3. `agent-git-service` queries control plane: token → tenant + user + TiDB DSN
4. The logical repo path from the URL (`owner/repo`) is combined with the authenticated tenant to resolve the physical bare repo path
5. `git-http-backend` serves the tenant-scoped repo
```

## Repository Identity vs Storage Key

- **Public API and clone URLs remain GitHub-compatible.** Repositories are addressed as `owner/repo` and cloned from `https://host/{owner}/{repo}.git`.
- **Physical storage must include tenant.** In multi-agent mode, bare repos must not be stored at a flat `GIT_REPO_DIR/{owner}/{repo}.git` path because two tenants may independently own the same logical `owner/repo`.
- **Recommended MVP storage layout:** `GIT_REPO_DIR/{tenant}/{owner}/{repo}.git`
- **Longer-term option:** use an immutable repo storage key (for example `{tenantID}/{repoID}.git`) to avoid directory moves on rename or transfer, but this is not required for the current multi-agent target.

## Backward Compatibility

Multi-agent support must preserve the current development path when no control plane is configured:

- `Service.DB` remains the default single database in dev mode.
- `s.DBForCtx(ctx)` falls back to `s.DB.WithContext(ctx)` when no per-request DB is present in context.
- Existing tests and local workflows continue to run without configuring a control-plane DB.
- Single-DB local mode may continue to read the legacy flat repo layout, but multi-agent mode must resolve a tenant-scoped physical repo path.

This fallback is part of the multi-agent design contract, not an implementation detail to remove later in the refactor.

## Agent Registration

To onboard a new agent:

1. Create or choose the agent's tenant database.
2. Insert a user record in the control-plane DB with the agent's login and tenant DSN.
3. Insert a token record for that user.
4. On the agent's first request, `agent-git-service` connects to the tenant DB and initializes the schema.

The agent then uses a standard GitHub-compatible workflow. The commands below use GitHub CLI as an important compatibility client:

```bash
export GH_HOST=gh.example.com
curl -sf https://gh.example.com/api/v3/
curl -sf https://gh.example.com/api/v3/meta
echo "$TOKEN" | gh auth login --with-token
gh auth status
gh auth setup-git

gh repo create my-project --private
gh issue create --title "feature X"
git clone https://gh.example.com/agent-42/my-project.git
git push
gh pr create --title "feature X"
```

This sequence matches the intended product framing: users first see API discovery and client workflows such as `gh`; raw Git transport is mostly an implementation-support surface behind repo workflows.

## Configuration

| Variable | Purpose |
|---|---|
| `CONTROL_PLANE_DSN` | DSN for the shared control plane TiDB |
| `ADMIN_LOGIN` | Bootstrap: initial agent login |
| `ADMIN_TOKEN` | Bootstrap: initial agent token |
| `LISTEN_MODE` | `production`: single HTTP port, TLS terminated at LB |
| `DB_MAX_OPEN_CONNS` | Connection pool size per agent TiDB |
| `DB_CONN_MAX_LIFETIME` | Connection lifetime for TiDB Cloud |

## Deployment

`agent-git-service` runs as a container. Configuration is entirely through environment variables. The container image includes `git` for gitstore operations.

In production:
- Multiple `agent-git-service` instances behind a load balancer
- TLS terminated at the load balancer
- JuiceFS mounted at `GIT_REPO_DIR` for shared git storage
- Health check endpoints at `/healthz` and `/readyz`

## Related Documents

- [architecture.md](../architecture.md) — system overview, authority split, persistence model
- [module-contracts.md](../module-contracts.md) — layer ownership and dependency rules
- [architecture/service.md](../architecture/service.md) — Service struct and DB access patterns
- [architecture/gitstore.md](../architecture/gitstore.md) — bare-repository operations
