# Architecture

This document is the design baseline and single source of truth for the current
`agent-git-service` implementation. Update it when behavior, package
boundaries, or local development workflow change. Avoid putting transient status
here such as exact passing test counts; the codebase and CI are the truth for
fast-moving inventory.

## Purpose

`agent-git-service` is a self-hosted Git-backed server for standard
GitHub-compatible API and Git transport workflows. The development binary is
currently named `gh-server`.

The compatibility target is common GitHub API and Git behavior for automation
clients, not strict endpoint-for-endpoint parity with GitHub.com. The primary
surfaces are:

- GitHub REST API v3
- GitHub GraphQL API v4
- Git Smart HTTP
- OAuth device flow

The service also exposes repo-specific helper endpoints such as OIDC-backed
human login under `/api/v3/oidc/*`, connected-login browser helpers under
`/auth/connected/*`, and admin-only wiki maintenance endpoints.

The key architectural invariant is that `agent-git-service` is Git-backed:

- Git is authoritative for repository content, history, refs, diffs, merges,
  rebases, and Git transport behavior.
- The relational database is authoritative for users, auth, issues, pull
  requests, reviews, labels, workflow records, and other product metadata.
- `service` coordinates flows that need both Git-backed and DB-backed state.

The reference setup stores relational state in one application metadata
database, normally TiDB through GORM, and stores Git objects in bare
repositories under `GIT_REPO_DIR`. Plain MySQL-compatible databases can support
core metadata storage, but TiDB is the reference database for multilingual
full-text and vector-backed search behavior.

## Repository Structure

| Path | Responsibility |
|---|---|
| `auth` | Public embedding identity types for external consumers |
| `cmd/gh-server` | CLI entrypoint, signal handling, `.env` loading, and logging init |
| `server` | Public startup/shutdown API, embeddable constructor/handlers, dependency wiring, TLS setup, and listeners |
| `config` | Environment-backed configuration exposed for external consumers |
| `internal/db` | GORM models, migrations, seed data, shared state constants |
| `internal/service` | Business logic over DB and Git storage |
| `internal/rest` | REST handlers plus response and transform helpers |
| `internal/graphql` | Query parsing, resolver dispatch, field filtering |
| `internal/gitstore` | Bare-repository operations on disk |
| `internal/githttp` | Smart HTTP bridge to `git-http-backend` |
| `internal/middleware` | Auth and request-size middleware |
| `internal/oauth` | OAuth device-flow endpoints |
| `internal/oidc` | Generic OIDC discovery, device flow, and ID token verification client |
| `internal/connectedlogin` | Configurable OAuth-style browser-login client for code exchange and userinfo |
| `internal/embedding` | Optional embedding-backed search support |
| `internal/crypto` | NaCl-based encryption primitives for secrets |
| `internal/ratelimit` | GitHub-compatible rate-limit snapshot helpers |
| `internal/metrics` | Prometheus collectors and recorder facade |
| `internal/logging` | Structured logging and request-scoped log attributes |
| `internal/httputil` | Safe bounded helpers for outbound HTTP clients |
| `internal/randutil` | Shared random-hex generation utility |
| `internal/apperrors` | Sentinel application errors and error helpers |
| `internal/testharness` | Reusable HTTP integration test harness (TiDB, real router) |
| `internal/router` | Chi router registration and middleware wiring |
| `cli/` | Vendored GitHub CLI plus compatibility tests |
| `docs/module-contracts.md` | Dependency boundaries, ownership, and accepted couplings |
| `docs/test-strategy.md` | Testing roadmap and execution order |

## Startup and Runtime

`cmd/gh-server` is the binary entrypoint and `server` is the composition root.
External embedders can use `server.Run` or construct a reusable instance with
`server.New(config.Config, ...)`, mount `Handler()` or protocol-specific
handler accessors, and manage listeners through `Start()` / `Shutdown(ctx)`.

Embedded hosts may install `server.WithAuthenticator(...)` to inject a trusted
request identity without minting AGS tokens first. The shared identity shape is
exported from the top-level `auth` package.

The embedded-auth contract is:

- The host authenticator returns a trusted `auth.Identity` with non-empty
  `Provider`, `Subject`, and `Login`; `Name`, `Email`, `Groups`, and
  `SiteAdmin` are optional metadata that AGS persists onto its internal user
  record.
- When the authenticator returns `ok=false`, AGS falls back to token auth.
- When the authenticator returns `ok=true`, embedded identity takes precedence
  over any `Authorization` header on the request.

A minimal host implementation looks like:

```go
import (
    "github.com/ngaut/agent-git-service/auth"
    "github.com/ngaut/agent-git-service/server"
)

srv, err := server.New(cfg, server.WithAuthenticator(myAuthenticator{}))
```

where `myAuthenticator.Authenticate(*http.Request)` returns a stable upstream
subject such as:

```go
auth.Identity{
    Provider: "meshx",
    Subject:  "user-123",
    Login:    "alice",
    Name:     "Alice",
    Email:    "alice@example.com",
}
```

The startup sequence is:

1. Load `.env` for local development via `godotenv`.
2. Initialize structured logging via `internal/logging`.
3. Load typed configuration via `config.New()` from environment variables.
4. Initialize the application database, run migrations, and seed default
   records.
5. Initialize embeddings if `EMBEDDING_API_KEY` is present.
6. Initialize the Git store rooted at `GIT_REPO_DIR`.
7. Build the shared `service.Service`, wiring DB, Git store, base URL,
   embeddings, generic OIDC, optional connected login, and local-dev auth
   conveniences.
8. Initialize REST transforms, GraphQL server, REST deps, Git HTTP handler,
   OAuth handler, metrics, and readiness endpoints.
9. Register routes and start listeners.

The server starts five listeners from the same handler tree:

| Address | Protocol | Notes |
|---|---|---|
| `:443` | HTTPS | Conventional HTTPS endpoint |
| `:$PORT` | HTTPS | Configurable primary development port |
| `:8081` | HTTPS | Non-privileged HTTPS alternative |
| `:80` | HTTP | Conventional HTTP endpoint |
| `:4003` | HTTP | Non-privileged HTTP alternative |

TLS uses `cert.pem` and `key.pem`. Shutdown is graceful with a 10-second
timeout.

## Routing Model

Route wiring lives in `internal/router/router.go`; that file is the executable
truth for concrete endpoints. The REST prefix is fixed at `/api/v3` to remain
compatible with GitHub-compatible clients.

Request families:

- OAuth endpoints are unauthenticated.
- OIDC helper endpoints under `/api/v3/oidc/*` are unauthenticated but
  service-backed.
- Connected login helper endpoints under `/auth/connected/*` are
  unauthenticated but service-backed.
- Git Smart HTTP endpoints are routed separately from the REST/GraphQL API
  tree, but still use the same auth middleware.
- Discovery endpoints under `/api/v3`, `/api/v3/meta`, and
  `/api/v3/rate_limit` use optional auth.
- The authenticated API contains REST and GraphQL endpoints, including
  organization-governance surfaces for explicit org creation, org invitations,
  teams, and outside-collaborator inspection.
- Unknown `/api/*` paths return GitHub-style JSON 404 responses.

Requests sent to `api.github.localhost` are rewritten before routing:

- `/graphql` becomes `/api/graphql`
- any non-`/api/*` path becomes `/api/v3/*`

## Layer Boundaries

| Layer | Responsibility | Notes |
|---|---|---|
| `router` | Endpoint registration and host rewrite | Depends on REST, GraphQL, Git HTTP, OAuth, and middleware |
| `middleware` | Auth extraction and request guards | Calls service auth methods and injects current-user context |
| `rest` | Decode request, call service, encode REST response | Handlers should avoid direct DB access |
| `rest/respond` | Status code and JSON helpers | REST-only concern |
| `rest/transform` | Convert DB models into GitHub REST shapes | REST-only concern |
| `graphql` | Parse query, route resolvers, filter fields | Uses service methods directly |
| `service` | Business rules and orchestration | Owns DB and GitStore interaction |
| `db` | Relational schema and persistence | Auto-migrated on startup |
| `gitstore` | Bare-repository operations and Git command execution | Handles repo-level locking for writes |

The service layer is wired as a concrete `*service.Service`; there is no
generated or authoritative service-interface catalog. Real-service integration
tests are usually more reliable than handler-level mocks for cross-layer flows.

## Persistence Model

Persistence is split by authority rather than by deployment topology:

- `db` state is authoritative for higher-level relational metadata and
  workflow/product records.
- `gitstore` is authoritative for Git-native repository state and operations.
- `service` coordinates flows that need both stores.

All GORM models live in `internal/db/models_*.go`. The main data groups are:

| Area | Main Models |
|---|---|
| Auth and identity | `User`, `Token`, `DeviceCode`, `SSHKey`, `SSHSigningKey`, `GPGKey` |
| Organization governance | `OrganizationMember`, `OrganizationInvitation`, `OutsideCollaborator` |
| Repositories and collaboration | `Repository`, `Collaborator`, `RepositoryInvitation`, `DeployKey`, `Ruleset`, `BranchProtection`, `Webhook`, `HookDelivery`, `Autolink`, `Star` |
| Issues and pull requests | `Issue`, `PullRequest`, `IssueComment`, `Milestone`, `ReviewRequest`, `PullRequestReview`, `PRReviewComment`, `LinkedBranch`, `Reaction` |
| Releases | `Release`, `ReleaseAsset` |
| Actions and workflows | `Workflow`, `WorkflowRun`, `WorkflowRunJob`, `Artifact`, `ActionCache`, `Secret`, `Variable` |
| Projects and teams | `Project`, `ProjectField`, `ProjectItem`, `ProjectRepoLink`, `Team`, `TeamMember`, `TeamRepository` |
| Other user content | `Gist`, `DependabotAlert`, `Deployment`, `DeploymentStatus`, `CommitStatus` |

`internal/gitstore` manages standard bare repositories on disk:

```text
GIT_REPO_DIR/{owner}/{repo}.git
```

The package mixes go-git for some native reference operations and `git` CLI
commands for merge, rebase, diff, archive, log, and content operations.
Write-sensitive operations use a per-repository mutex.

Other stored data:

- Release asset binaries are stored in the database.
- Actions, Dependabot, and Codespaces secrets use the NaCl-based encryption
  primitives in `internal/crypto`.
- Semantic search data is optional and only activated when embeddings are
  configured and the active database supports the required vector-distance
  search capability.

## Authentication Model

`internal/middleware/auth.go` implements two token paths:

- `TokenAuth` for authenticated REST and GraphQL endpoints
- `OptionalTokenAuth` for discovery endpoints and routes where public access is
  possible

Token validation goes through the service layer. When there are no tokens in
the database, any non-empty token is accepted as a local-development
convenience.

Git clone, fetch, and push are routed outside the REST/GraphQL API tree but pass
through the standard auth middleware on their route group. The shared auth
extraction path accepts GitHub-style `token ...`, `Bearer ...`, and HTTPS Basic
auth (`username:token`). After auth, `githttp` enforces repo read/write
permission through `service.HasRepoAccess(...)` before delegating to
`git-http-backend`.

The handler still sets `REMOTE_USER=git` for CGI compatibility, but that value
is not treated as the authorization decision.

## OIDC and Connected Login

When generic OIDC is configured, REST exposes these unauthenticated helper
endpoints:

- `POST /api/v3/oidc/device/code`
- `POST /api/v3/oidc/session`
- `POST /api/v3/oidc/callback`
- `POST /api/v3/oidc/lookup`

These endpoints stay transport-thin: `internal/oidc` owns discovery, optional
device-authorization exchange, and ID token verification, while `service` owns
mapping verified external identities onto local application users and tokens.

When connected login is configured, REST also exposes:

- `GET /auth/connected/login`
- `GET /auth/connected/callback`

`internal/connectedlogin` owns the configurable browser login URL, token
exchange path, userinfo path, and claim extraction. `service` maps verified
external userinfo into the same local identity/session path as OIDC.

## Canonical Request Flows

REST request:

```text
client -> router -> auth middleware -> REST handler -> service -> DBForCtx(ctx)/gitstore -> transform/respond
```

Handlers are expected to stay thin:

1. parse params and request body
2. call service methods
3. transform model data into GitHub JSON shape
4. write HTTP response

GraphQL request:

```text
client -> router -> auth middleware -> GraphQL handler -> parse query -> resolver -> service -> DBForCtx(ctx)/gitstore -> field filter -> response
```

Git push:

```text
git client -> router -> auth middleware -> githttp -> git-http-backend -> repository update -> post-push housekeeping
```

After push, the server performs follow-up work such as fixing `HEAD`,
dispatching repository and pull-request webhook events, and syncing workflow
definitions from the repository.

Pull request merge:

```text
REST or GraphQL merge request -> service merge logic -> gitstore merge/rebase/squash path -> DB state update
```

Workflow execution:

```text
workflow dispatch -> service background runner -> Docker sandbox per `run:` step -> job logs/artifacts -> workflow completion
```

Workflow execution is fail-closed by default. The server only executes workflow
steps when `ENABLE_WORKFLOW_EXEC=1` is set.

## Primary Product Flows

These flows should stay central in future work:

- server discovery and auth bootstrap through `/api/v3/`, `/api/v3/meta`,
  `/api/v3/rate_limit`, token login, and Git credential setup
- explicit organization creation and governance through `/api/v3/user/orgs`,
  org invitations, team membership, and outside-collaborator inspection
- OIDC-backed human login and identity lookup through `/api/v3/oidc/*`
- repository creation, fork, transfer, delete
- repository sharing and effective permission resolution across org base
  permission, direct collaborators, and team grants
- issue creation, update, search, label and assignee management
- pull request creation, review, diff inspection, merge, revert
- release creation plus asset upload and download
- workflow discovery, dispatch, rerun, cancel, logs, and artifacts
- search across repositories, issues, pull requests, commits, and code

## Configuration

The canonical configuration reference is [`../.env.example`](../.env.example).
The top section contains the required quick-start settings; later sections
document optional runtime capabilities.

Configuration is loaded from environment variables in `config/config.go` and a
small number of subsystem-local environment reads for CORS, logging, secret
encryption, Git HTTP upload limits, and embedding concurrency.

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

## Related Documents

- [Module Contracts](module-contracts.md) records layer ownership, dependency
  rules, and current technical debt around concrete couplings.
- [Test Strategy](test-strategy.md) describes the phased test roadmap.
- [REST API](architecture/rest.md) documents v3 surface handlers, transform,
  and respond helpers.
- [GraphQL API](architecture/graphql.md) documents v4 query and mutation
  dispatch.
- [Git Smart HTTP](architecture/git-http.md) documents the transport bridge to
  `git-http-backend`.
- [OAuth](architecture/oauth.md) documents device flow endpoints.
- [Service Layer](architecture/service.md) documents domain interfaces and
  persistence orchestration.
- [Git Store](architecture/gitstore.md) documents bare-repository operations.
- [Wiki Storage Re-Architecture](design/wiki-storage-rearchitecture.md)
  documents the wiki storage roadmap.
