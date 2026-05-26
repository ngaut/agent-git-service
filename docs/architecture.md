# Architecture

This document is the design baseline and single source of truth for `agent-git-service`.
Update it when behavior, package boundaries, or the local development workflow change.
Avoid putting transient status here such as exact passing test counts; the codebase and CI are the truth for fast-moving inventory.
This document records the current implemented architecture. Planned multi-agent changes live in [design/multi-agent.md](design/multi-agent.md) until the corresponding code lands.

## Purpose

`agent-git-service` is a self-hosted Git-backed server for standard GitHub-compatible API and Git transport workflows. The development binary is currently named `gh-server`.
The compatibility target is common GitHub API and Git behavior for automation clients, not strict endpoint-for-endpoint parity with GitHub.com.
It exposes four primary surfaces:

- GitHub REST API v3
- GitHub GraphQL API v4
- Git Smart HTTP
- OAuth device flow

It also exposes additive repo-specific endpoints such as Auth0-backed human-login
helpers under `/api/v3/auth0/*` when Auth0 is configured, plus admin-only wiki
maintenance endpoints such as `/api/v3/admin/wiki/repos/{owner}/{repo}/repair-locks`
for stale wiki ref-lock recovery.

From a user-facing perspective, the main entry points are GitHub-compatible clients, including `gh` CLI, plus the REST discovery/auth endpoints `/api/v3/`, `/api/v3/meta`, and `/api/v3/rate_limit`. Git Smart HTTP is typically exercised after that setup path, when a Git client or credential helper crosses into clone, fetch, or push.

The key architectural invariant is that `agent-git-service` is Git-backed.
Repository content, history, refs, diffs, merges, rebases, and Git transport must preserve real Git semantics.

Authority is split by concern:

- Git is authoritative for Git-native repository state and behavior.
- The relational database is authoritative for higher-level metadata such as users, auth, issues, pull requests, reviews, labels, workflow records, and related product state.
- `service` coordinates flows that need both Git-backed and DB-backed state.

Current wiki contract:

- The sibling bare `*.wiki.git` repository is the durable authority for wiki page content, path layout, commit history, ref-pinned reads, rename semantics, and prefix moves.
- TiDB-backed wiki tables still serve current-page and search/list read paths through catalog-backed projections while the final #1488 cutover and cleanup work remains in progress.
- Remaining wiki re-architecture work for issue #1488 is about removing those transitional read paths so current-page and indexed metadata reads become obviously rebuildable from git without reintroducing catalog-first writes.

This does not prohibit repository- or pull-request-related metadata in the database.
The rule is about authority: Git-native behavior stays Git-backed, while relational metadata stays DB-backed.

Deployment topology is intentionally not part of the architectural contract.
The system may run as a single local process for development or as stateless/distributed components with external services, as long as the Git-backed invariant and authority split above remain true.

The current reference setup stores relational state in TiDB through GORM and stores Git objects in bare repositories under `GIT_REPO_DIR`. Plain MySQL-compatible databases can support core metadata storage, but TiDB is the reference database for multilingual full-text and vector-backed search behavior.
The vendored `cli/` module is the gh CLI compatibility harness, not the product boundary.

## Repository Structure

| Path | Responsibility |
|---|---|
| `cmd/gh-server` | CLI entrypoint, signal handling, `.env` loading, and logging init |
| `server` | Public startup/shutdown API, embeddable constructor/handlers, dependency wiring, TLS setup, and listeners |
| `config` | Environment-backed configuration exposed for external consumers |
| `internal/db` | GORM models, migrations, seed data, shared state constants |
| `internal/service` | Business logic over DB and Git storage (includes `Embedder` and `AllowAnyToken` fields) |
| `internal/controlplane` | Shared control-plane schema and token-to-tenant DB routing |
| `internal/tenant` | Shared tenant-context key and helpers for tenant-scoped Git routing |
| `internal/rest` | REST handlers plus response and transform helpers |
| `internal/graphql` | Query parsing, resolver dispatch, field filtering |
| `internal/gitstore` | Bare-repository operations on disk |
| `internal/githttp` | Smart HTTP bridge to `git-http-backend` |
| `internal/middleware` | Auth and request-size middleware |
| `internal/oauth` | OAuth device-flow endpoints |
| `internal/auth0` | Auth0 device-flow/JWKS client for human login |
| `internal/authn` | Shared token-resolver interfaces and auth sentinel errors |
| `internal/embedding` | Optional embedding-backed search support |
| `internal/crypto` | NaCl-based encryption primitives for secrets |
| `internal/ratelimit` | GitHub-compatible rate-limit snapshot helpers |
| `internal/metrics` | Prometheus collectors and recorder facade |
| `internal/logging` | Structured logging and request-scoped log attributes |
| `internal/httputil` | Safe bounded helpers for outbound HTTP clients |
| `internal/randutil` | Shared random-hex generation utility |
| `internal/apperrors` | Sentinel application errors and error helpers |
| `internal/testharness` | Reusable HTTP integration test harness (SQLite, real router) |
| `internal/router` | Chi router registration and middleware wiring |
| `cli/` | Vendored GitHub CLI plus compatibility tests |
| `docs/module-contracts.md` | Dependency boundaries, ownership, and coupling audit |
| `docs/architecture/tenant-db-correctness.md` | Tenant-aware DB routing guardrails for service code |
| `docs/test-strategy.md` | Testing roadmap and execution order |

## Startup and Runtime

`cmd/gh-server` is the binary entrypoint and `server` is the composition root. External embedders can either keep using `server.Run` or construct a reusable instance with `server.New(config.Config)`, mount `Handler()` or the protocol-specific handler accessors, and manage listeners through `Start()` / `Shutdown(ctx)`. The startup sequence is:

1. Load `.env` for local development via `godotenv`.
2. Initialize structured logging via `internal/logging`.
3. Load typed configuration via `config.New()` from environment variables.
4. Initialize the main application database, run migrations, and seed default records.
5. Initialize embeddings if `EMBEDDING_API_KEY` is present.
6. Initialize the Git store rooted at `GIT_REPO_DIR`; when `CONTROL_PLANE_DSN` is set, enable tenant-isolated repo roots with a default-tenant fallback.
7. Build the shared `service.Service`, wiring DB, Git store, base URL, embeddings, Auth0, and local-dev auth conveniences.
8. If `CONTROL_PLANE_DSN` is set, initialize the control-plane database and `controlplane.DBRouter`.
9. Initialize REST transforms, GraphQL server, REST deps, Git HTTP handler, OAuth handler, metrics, and readiness endpoints.
10. Register routes and start listeners.

### Listeners

The server starts five listeners from the same handler tree:

| Address | Protocol | Notes |
|---|---|---|
| `:443` | HTTPS | Conventional HTTPS endpoint |
| `:$PORT` | HTTPS | Configurable primary development port |
| `:8081` | HTTPS | Non-privileged HTTPS alternative |
| `:80` | HTTP | Conventional HTTP endpoint |
| `:4003` | HTTP | Non-privileged HTTP alternative |

TLS uses `cert.pem` and `key.pem`.
Shutdown is graceful with a 10-second timeout.

## Routing Model

Route wiring lives in `internal/router/router.go`.
That file is the executable truth for concrete endpoints.
This document records the stable structure around those routes.
The default REST prefix is `/api/v3`, and embedders can additionally expose the same REST surface under a custom prefix through `config.Config.RESTPrefix`.

### Request Families

- OAuth endpoints are unauthenticated.
- Auth0 helper endpoints under `/api/v3/auth0/*` are unauthenticated but service-backed.
- Git Smart HTTP endpoints are routed separately from the REST/GraphQL API tree, but they still use the same auth middleware (`TokenAuth` in control-plane mode, `OptionalTokenAuth` in single-DB mode).
- Discovery endpoints under `/api/v3`, `/api/v3/meta`, and `/api/v3/rate_limit` use optional auth and are the main user-visible discovery/auth bootstrap routes for GitHub-compatible clients, including `gh`.
- The authenticated API contains REST and GraphQL endpoints, including the current organization-governance surfaces for explicit org creation, org invitations, teams, and outside-collaborator inspection.
- Unknown `/api/*` paths return GitHub-style JSON 404 responses.

### Discovery Endpoints

- `GET /api/v3/` advertises core REST URLs and password-auth capability to clients.
- `GET /api/v3/meta` is part of the `gh auth setup-git` and Git credential capability check path.
- `GET /api/v3/rate_limit` is a GitHub-compatible probe used by clients and smoke tests.

### Collaboration and Governance Endpoints

The current collaboration model adds explicit organization-governance endpoints alongside repo and team routes:

- `POST /api/v3/user/orgs` explicitly creates an organization account. `GET /api/v3/orgs/{org}` only returns existing organizations and does not auto-create missing orgs.
- `GET /api/v3/user/orgs` lists organizations through explicit `OrganizationMember` rows, not by inferring membership from team joins.
- `GET/POST/DELETE /api/v3/orgs/{org}/invitations...` and `GET/PATCH/DELETE /api/v3/user/organization_invitations...` implement the org invitation lifecycle.
- `GET /api/v3/orgs/{org}/outside_collaborators` lists non-member users who currently have direct collaborator access to repos owned by that organization.

### Host Rewrite

Requests sent to `api.github.localhost` are rewritten before routing:

- `/graphql` becomes `/api/graphql`
- any non-`/api/*` path becomes `/api/v3/*`

This keeps the local server compatible with the way `gh`, `go-gh`, and other GitHub-style clients construct API URLs for enterprise hosts.

## Layer Boundaries

| Layer | Responsibility | Notes |
|---|---|---|
| `router` | Endpoint registration and host rewrite | Depends on REST, GraphQL, Git HTTP, OAuth, and middleware |
| `middleware` | Auth extraction and request guards | Auth middleware currently receives a concrete `*service.Service` plus optional `authn.TokenResolver` (normally `controlplane.DBRouter`) |
| `rest` | Decode request, call service, encode REST response | Handlers are expected to avoid direct DB access |
| `rest/respond` | Status code and JSON helpers | REST-only concern |
| `rest/transform` | Convert DB models into GitHub REST shapes | REST-only concern |
| `graphql` | Parse query, route resolvers, filter fields | Uses service methods directly |
| `controlplane` | Token-to-tenant DB routing and global agent state | Active only when `CONTROL_PLANE_DSN` is configured |
| `service` | Business rules and orchestration | Owns DB and GitStore interaction |
| `db` | Relational schema and persistence | Auto-migrated on startup |
| `gitstore` | Bare-repository operations and Git command execution | Handles repo-level locking for writes |

### Important Current Constraint

The current service layer is wired as a concrete `*service.Service`; there is no generated or authoritative service-interface catalog.
That matters for future refactors and for test design: today the shortest reliable path is real-service integration testing, not handler-level mocks.

## Persistence Model

Persistence is split by authority rather than by deployment topology:

- the control-plane database is authoritative for global agent identity, auth tokens, and tenant DSN mappings.
- tenant-local `db` state is authoritative for higher-level relational metadata and workflow/product records.
- `gitstore` is authoritative for Git-native repository state and operations.
- `service` coordinates flows that need both stores.

### Relational State

All GORM models live in `internal/db/models_*.go`.
The main data groups are:

| Area | Main Models |
|---|---|
| Auth and identity | `User` (including organization accounts and `DefaultRepositoryPermission`), `Token`, `DeviceCode`, `SSHKey`, `SSHSigningKey`, `GPGKey` |
| Organization governance | `OrganizationMember`, `OrganizationInvitation`, `OutsideCollaborator` |
| Repositories and collaboration | `Repository`, `Collaborator`, `RepositoryInvitation`, `DeployKey`, `Ruleset`, `BranchProtection`, `Webhook`, `HookDelivery`, `Autolink`, `Star` |
| Issues and pull requests | `Issue`, `PullRequest`, `IssueComment`, `Milestone`, `ReviewRequest`, `PullRequestReview`, `PRReviewComment`, `LinkedBranch`, `Reaction` |
| Releases | `Release`, `ReleaseAsset` |
| Actions and workflows | `Workflow`, `WorkflowRun`, `WorkflowRunJob`, `Artifact`, `ActionCache`, `Secret`, `Variable` |
| Projects and teams | `Project`, `ProjectField`, `ProjectItem`, `ProjectRepoLink`, `Team`, `TeamMember`, `TeamRepository` |
| Other user content | `Gist`, `DependabotAlert`, `Deployment`, `DeploymentStatus`, `CommitStatus` |

### Control-Plane State and Tenant Routing

When `CONTROL_PLANE_DSN` is configured, the server adds a second relational store
for global control-plane state:

| Store | Main Models | Responsibility |
|---|---|---|
| Control plane DB | `CPUser`, `CPToken` | agent identity, token resolution, tenant DSN mapping |
| Tenant DB | the normal `internal/db` model set above | per-tenant GitHub-compatible product metadata |

The request-time routing path in control-plane mode is:

```text
client
  -> router
  -> middleware.TokenAuth / OptionalTokenAuth
  -> controlplane.DBRouter.ResolveToken(...)
  -> service.ContextWithDB(...) + ContextWithUser(...)
  -> REST/GraphQL handler
  -> service.DBForCtx(ctx)
  -> tenant DB
```

`controlplane.DBRouter` caches tenant `*gorm.DB` handles and runs
`db.Migrate(...)` on first open.
`docs/architecture/tenant-db-correctness.md` records the guardrail that service code must
always use `DBForCtx(ctx)` rather than reaching for `s.DB` directly.

### Current Collaboration Model

- Organizations are explicit `User{Type="Organization"}` accounts. `service.CreateOrg` is the current product entry point and records the creator as an org owner; `EnsureOrg` remains as a legacy/test helper.
- Organization membership is independent from team membership. `OrganizationMember` controls org-level membership and owner/member role, while `TeamMember` controls per-team member/maintainer role.
- Teams remain authorization-group objects. The legacy `privacy` field is retained for REST compatibility, but the service persists and serializes teams as `closed`.
- Organization invitations store GitHub-compatible invitation roles (`direct_member`, `admin`) plus invited team IDs. Accepting an invitation maps the invitation role to org membership (`direct_member` -> `member`, `admin` -> `owner`), joins the invited teams, and removes the pending invitation row.
- Outside collaborators are tracked explicitly for org-owned repos. The row is reconciled from direct repo collaborator state and removed when the user becomes an org member or loses all direct repo access in that org.
- Effective repository permission for REST, GraphQL, and viewer-repo listing is `max(org default permission, direct collaborator grant, team grant)` over the minimal runtime set `read`, `write`, `admin`. GitHub-style `triage` and `maintain` remain accepted as compatibility aliases for `read` and `write`.
- Git Smart HTTP now consults the same repository permission model for read/write authorization after auth middleware has populated the request context.

### Git State

`internal/gitstore` manages bare repositories on disk.

Single-DB layout:

```text
GIT_REPO_DIR/{owner}/{repo}.git
```

When control-plane mode is enabled, `main` constructs `gitstore` with
tenant isolation enabled, so the intended physical layout becomes:

```text
GIT_REPO_DIR/{tenant}/{owner}/{repo}.git
```

The tenant-scoping contract for git storage is documented in
`docs/module-contracts.md`; future multi-agent storage work is documented in
[design/multi-agent.md](design/multi-agent.md).

The package mixes:

- go-git for some native reference operations
- `git` CLI commands for merge, rebase, diff, archive, log, and content operations

Write-sensitive operations use a per-repository mutex.

### Other Stored Data

- Release asset binaries are stored in the database.
- Actions, Dependabot, and Codespaces secrets use the NaCl-based encryption primitives in `internal/crypto`.
- Semantic search data is optional and only activated when embeddings are configured and the active database supports the required vector-distance search capability.

## Authentication Model

### Token Auth

`internal/middleware/auth.go` implements two modes:

- `TokenAuth` for authenticated REST and GraphQL endpoints
- `OptionalTokenAuth` for discovery endpoints

In single-DB mode, token validation goes through the service layer.
In control-plane mode, auth middleware resolves the token through
`controlplane.DBRouter`, injects the tenant DB into the request context, and
service code reads it back through `DBForCtx(ctx)`.

When there are no tokens in the database, any non-empty token is accepted as a local-development convenience.

### Git Smart HTTP Auth

Git clone, fetch, and push are routed outside the REST/GraphQL API tree but still
pass through the standard auth middleware on their route group.
The shared auth extraction path accepts GitHub-style `token ...`,
`Bearer ...`, and HTTPS Basic auth (`username:token`).

In control-plane mode, Git routes require authentication up front.
In single-DB mode, they preserve optional-auth behavior for public-repo reads.
After auth, `githttp` enforces repo read/write permission through
`service.HasRepoAccess(...)` before delegating to `git-http-backend`.

The handler still sets `REMOTE_USER=git` for CGI compatibility, but that value is
not treated as the authorization decision.

This is the current local/offline behavior, not the planned multi-agent model. The future Git transport auth design is documented in [design/multi-agent.md](design/multi-agent.md).

### Auth0 Human Login

When Auth0 is configured, REST exposes these unauthenticated helper endpoints:

- `POST /api/v3/auth0/device/code`
- `POST /api/v3/auth0/session`
- `POST /api/v3/auth0/callback`
- `POST /api/v3/auth0/lookup`

These endpoints stay transport-thin: `internal/auth0` owns the outbound Auth0
protocol work, while `service` owns mapping verified Auth0 identities onto local
application users and tokens.

### OAuth Device Flow (Secured)

`internal/oauth` implements:

- `POST /login/device/code` — request device code (unauthenticated)
- `POST /login/device` — approve device code (requires authentication)
- `POST /login/oauth/access_token` — exchange device code for access token (unauthenticated)
- `GET /login/oauth/authorize` — authorization code flow with PKCE (requires state + PKCE S256)

Device codes require explicit authenticated user approval before token exchange succeeds. The `/login/device` endpoint is protected by `TokenAuth` middleware and rejects unauthenticated requests. The `/login/oauth/authorize` endpoint requires `state` and PKCE `code_challenge` parameters to prevent CSRF and authorization code interception attacks. Redirect validation is limited to same-origin or localhost targets.

## Canonical Request Flows

### REST Request

```text
client
  -> router
  -> auth middleware
  -> single-DB: service.ValidateAndResolveTokenDetailed(...)
     or
     control-plane: controlplane.DBRouter.ResolveToken(...)
  -> REST handler
  -> service
  -> service.DBForCtx(ctx) / gitstore
  -> transform/respond
```

Handlers are expected to stay thin:

1. parse params and request body
2. call service methods
3. transform model data into GitHub JSON shape
4. write HTTP response

### GraphQL Request

```text
client -> router -> auth middleware -> GraphQL handler -> parse query -> resolver -> service -> DBForCtx(ctx)/gitstore -> field filter -> response
```

GraphQL builds response objects directly and then prunes them against the requested selection set.

### Git Push

```text
git client -> router -> auth middleware -> githttp -> git-http-backend -> repository update -> post-push housekeeping
```

After push, the server performs follow-up work such as fixing `HEAD`, dispatching repository and pull-request webhook events, and syncing workflow definitions from the repository.

### Pull Request Merge

```text
REST or GraphQL merge request -> service merge logic -> gitstore merge/rebase/squash path -> DB state update
```

This is one of the highest-risk flows in the system because it crosses REST or GraphQL, DB state, and real Git history updates.

### Workflow Execution

```text
workflow dispatch -> service background runner -> Docker sandbox per `run:` step -> job logs/artifacts -> workflow completion
```

Workflow execution is fail-closed by default. The server only executes workflow steps when `ENABLE_WORKFLOW_EXEC=1` is set.

When enabled:

- each workflow `run:` step executes in an isolated Docker container rather than as a host shell process
- the container is launched with `--network none`, `--read-only`, `--cap-drop ALL`, `--security-opt no-new-privileges`, a bind-mounted temporary workspace, and a dedicated `tmpfs` at `/tmp`
- step processes receive only explicit workflow env vars plus a minimal runtime env (`HOME`, `PATH`, `CI`, `GITHUB_ACTIONS`); host `os.Environ()` is never inherited
- the service enforces workflow-wide timeout and container quotas for CPU, memory, process count, and file descriptors; timed-out containers are force-removed as a kill-switch
- workflow execution emits structured audit logs for start, each step, artifact handling, and final completion, all keyed by repo and workflow run ID

`actions/upload-artifact` remains a built-in service-side action over the sandbox workspace. Artifact collection skips symlinks and resolves real paths before reading files so workflow-created links cannot exfiltrate host files outside the workspace.

## Primary Product Flows

These flows should stay central in future work:

- server discovery and auth bootstrap through `/api/v3/`, `/api/v3/meta`, `/api/v3/rate_limit`, token login, and `gh auth setup-git` / Git credential setup
- explicit organization creation and governance through `/api/v3/user/orgs`, org invitations, team membership, and outside-collaborator inspection
- Auth0-backed human login and identity lookup through `/api/v3/auth0/*`
- control-plane token routing when multi-tenant mode is enabled
- repository creation, fork, transfer, delete
- repository sharing and effective permission resolution across org base permission, direct collaborators, and team grants
- issue creation, update, search, label and assignee management
- pull request creation, review, diff inspection, merge, revert
- release creation plus asset upload and download
- workflow discovery, dispatch, rerun, cancel, logs, and artifacts
- search across repositories, issues, pull requests, commits, and code

## Configuration

The canonical configuration reference is
[`../.env.example`](../.env.example). The top section contains the required
quick-start settings; later sections document optional runtime capabilities.

Configuration is loaded from environment variables in `config/config.go`
and a small number of subsystem-local environment reads for CORS, logging,
secret encryption, Git HTTP upload limits, and embedding concurrency.

## Local Development and Test Entry Points

The main developer commands are:

```bash
make setup
make run-bg
make test-unit
make test
make test-run SUITE=TestPullRequests
make test-script SUITE=TestPullRequests SCRIPT=pr-create-basic.txtar
```

`make setup` is for production or persistent development environments and
expects `DB_DSN` to point at TiDB Cloud Starter. Use `make test-setup` when a
local or CI test run needs the test-only `tiup playground` database.

To inspect the current acceptance inventory instead of hard-coding counts:

```bash
(
  cd cli
  go test -tags acceptance ./acceptance -list '^Test'
  find acceptance/testdata -name '*.txtar'
)
```

## Related Documents

- `docs/module-contracts.md` records layer ownership, dependency rules, and current technical debt around concrete couplings.
- `docs/architecture/tenant-db-correctness.md` records the `DBForCtx(ctx)` tenant-routing rules for service code.
- `docs/test-strategy.md` describes the phased test roadmap.
- `internal/router/router.go` is the concrete route inventory.
- `docs/module-contracts.md` is the current domain-contract reference.

### Component References

- [REST API](architecture/rest.md) — v3 surface handlers, transform, respond
- [GraphQL API](architecture/graphql.md) — v4 query/mutation dispatch and field filtering
- [Git Smart HTTP](architecture/git-http.md) — transport bridge to git-http-backend
- [OAuth](architecture/oauth.md) — device flow endpoints
- [Service Layer](architecture/service.md) — domain interfaces and persistence orchestration
- [Control Plane](architecture/controlplane.md) — shared agent registry, token resolution, tenant DB routing
- [Tenant Context](architecture/tenant.md) — shared tenant key, middleware injection, and Git path scoping
- [Git Store](architecture/gitstore.md) — bare-repository operations

### Design Documents

- [Multi-Agent Architecture](design/multi-agent.md) — per-agent TiDB routing, stateless deployment, JuiceFS storage
