# Module Contracts

This document records the current implemented package boundaries for
`agent-git-service`. The runtime architecture uses one application metadata
database plus a Git store rooted at `GIT_REPO_DIR`.

## Core Invariant and Authority Split

The most important architectural invariant in this repo is that
`agent-git-service` is Git-backed. Repository content, history, refs, diffs,
merges, rebases, and Git transport semantics should stay grounded in real Git
behavior.

Authority is split by concern:

- `gitstore` owns Git-native repository state and operations.
- `db` owns higher-level relational metadata such as users, auth, issues, pull
  requests, reviews, labels, workflow records, and similar product state.
- `service` coordinates flows that need both Git-backed and DB-backed state.
- surface packages should respect that ownership instead of inventing parallel
  storage rules.

## High-Level Rules

- Transport packages decode requests, call `service`, and render responses.
- Business rules live in `internal/service`.
- GORM model definitions, migrations, and seed data live in `internal/db`.
- Git-native repository operations live in `internal/gitstore`.
- Git Smart HTTP protocol bridging lives in `internal/githttp`.
- `server` is the composition root; package constructors should not open global
  process resources behind the caller's back.

The public import surface is intentionally small:

- `config` exposes environment-backed startup configuration.
- `server` exposes the embeddable composition-root APIs (`New`, `Run`,
  `RunWikiReindex`, `Handler`, and `Shutdown`).
- Everything else in the root module remains internal-only unless documented
  otherwise.

## Package Ownership

| Package | Owns | May Depend On | Must Not Depend On |
|---|---|---|---|
| `apperrors` | sentinel application errors and helpers | stdlib | transport rendering |
| `connectedlogin` | configurable login URL, token exchange, userinfo claims | HTTP helpers, stdlib | service persistence |
| `config` | environment-backed configuration | stdlib | internal runtime packages |
| `crypto` | secret encryption primitives | NaCl dependencies, stdlib | service or DB policy |
| `db` | models, migrations, seed data | GORM, stdlib | service, router, transports |
| `embedding` | embedding client and vector search support | HTTP helpers, stdlib | transport packages |
| `githttp` | Git Smart HTTP CGI bridge | `service`, `gitstore`, HTTP stdlib | REST/GraphQL handlers |
| `gitstore` | bare repository filesystem layout and Git commands | go-git, `git` CLI, stdlib | service, DB, auth |
| `graphql` | GraphQL parsing, resolver dispatch, field filtering | `service`, DB model types | REST response helpers |
| `httputil` | bounded outbound HTTP helpers | stdlib | service policy |
| `logging` | structured logging helpers | stdlib/logging dependencies | DB or Git behavior |
| `mentions` | mention parsing and extraction helpers | stdlib | persistence or auth |
| `metrics` | Prometheus collectors and recorder facade | Prometheus client, stdlib | business-rule ownership |
| `middleware` | auth extraction, current-user context, request guards | `service`, `db` model types, logging/metrics helpers | transport handlers |
| `oauth` | OAuth device-flow endpoints and headless device approval API | `service`, middleware-compatible auth context | REST transform helpers |
| `oidc` | OIDC discovery, device flow, token verification | HTTP helpers, stdlib | service persistence |
| `randutil` | shared random-hex generation | crypto/rand, stdlib | callers' persistence rules |
| `ratelimit` | GitHub-compatible rate-limit snapshots | stdlib | transport handlers |
| `rest` | REST decoding and response orchestration | `service`, `rest/respond`, `rest/transform`, DB model types for shapes | direct GORM queries for business behavior |
| `router` | route registration and host rewrite | REST, GraphQL, Git HTTP, OAuth, middleware | direct DB/Git operations |
| `service` | business rules and orchestration | `db`, `gitstore`, embedding/OIDC helpers, metrics/logging | transport response packages |
| `testharness` | real-router integration harness | server-adjacent packages, TiDB test schemas | production-only process setup |
| `wikicatalog` | wiki catalog indexing support | DB/Git-adjacent helpers | transport routing |
| `wikiv2` | wiki V2 storage helpers | Git/storage helpers | transport routing |

## Auth Contract

`internal/middleware/auth.go` is the only package that extracts bearer, token,
and Basic-auth credentials from HTTP requests.

Authenticated routes use `TokenAuth`; discovery or public-capable routes use
`OptionalTokenAuth`. Both paths validate tokens through `service` and inject the
current `db.User` into request context for downstream code.

Embedded hosts may provide `server.WithAuthenticator(...)`. When the embedded
authenticator returns a trusted identity, middleware maps that identity into an
internal user through `service` and uses it as the current user for REST,
GraphQL, Git HTTP, OAuth approval, and optional-auth discovery paths.

When the token table is empty, any non-empty token is accepted as a
local-development convenience. Production deployments should seed explicit
tokens.

## Database Contract

`service.Service` owns the primary `*gorm.DB` handle. Production service code
should call `s.DBForCtx(ctx)` instead of directly reaching for
`s.DB.WithContext(ctx)` when there is a helper available. This keeps
transaction-scoped and test-scoped DB overrides working for background jobs and
integration harnesses.

`service.ContextWithDB(ctx, db)` and `service.DBFromContext(ctx)` are neutral
context-scoped DB override helpers. They are not a routing boundary.

Transport handlers should not query GORM directly for business decisions. If a
handler needs data, add or reuse a service method.

## Git Store Contract

`internal/gitstore` owns the physical bare-repository layout:

```text
GIT_REPO_DIR/{owner}/{repo}.git
```

Repository full names are logical GitHub-compatible identities. Git store code
must validate full names before mapping them to filesystem paths, and
write-sensitive operations must take the per-repository mutex.

Multi-file plumbing writes create blob/tree/commit objects separately from ref
publication. Callers that coordinate Git with another system of record persist
the prepared SHA first and then publish it with a parent CAS. Any process-local
tree cache must be bounded, transfer mutable ownership to only one preparer,
and validate the cached commit against the current branch parent.

`service` is responsible for deciding whether a caller may access a repository.
`gitstore` should not know about auth or DB permissions.

## Git HTTP Contract

`internal/githttp` bridges HTTP requests to `git-http-backend`.

- Auth middleware populates the current user before protected Git operations.
- `githttp` asks `service.HasRepoAccess(...)` for read/write authorization.
- `git-http-backend` remains responsible for protocol-level pack and ref
  negotiation.
- `git-receive-pack` holds the same concrete-store repository mutex used by
  service writes while it snapshots and updates refs.
- Post-push side effects, such as workflow sync and webhook dispatch, are
  coordinated through service-level helpers.

## REST Contract

REST handlers should:

1. parse route params, query params, and JSON request bodies
2. call `service`
3. convert models through `rest/transform`
4. write responses through `rest/respond`

Handlers may reference DB model types for request/response shape compatibility,
but business queries and mutations belong in `service`.

## GraphQL Contract

GraphQL resolvers use service methods directly and then prune response fields
according to the requested selection set. GraphQL should not call REST
transform helpers.

## Background Work Contract

Background goroutines started from service methods must preserve the pieces of
context that affect correctness, especially transaction-scoped DB overrides
when a test or caller deliberately installs one with `ContextWithDB`.

Long-running or asynchronous jobs should log enough repository/user/workflow IDs
to debug failures without relying on transport-level request state.

## Testing Contract

- Package and service tests should cover business rules first.
- Router and integration tests should use `internal/testharness` when they need
  real middleware and route wiring.
- Git behavior tests should exercise real bare repositories where practical.
- E2E scripts should remain shell-based, executable, and focused on externally
  observable behavior.

The regression gate manifest is `scripts/regression_gate.list`; keep it aligned
with this document when packages move or disappear.
