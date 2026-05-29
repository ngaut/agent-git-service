# Module Contracts

This document records the current architectural contracts between the main server modules.
It is meant to reduce drift between the intended layering and the real dependency graph in code.

This is a contract document, not a "refactor everything to interfaces" proposal.
When the code intentionally violates or relaxes one of these contracts, update this document.
This document records the current implemented contracts. Planned multi-agent changes live in `docs/design/multi-agent.md` until the code lands.

## Status

The contracts below describe the current implemented codebase, including the
control-plane DB router and tenant-aware service DB selection.
Some boundaries are already clean.
Some are still concrete couplings by design or by technical debt.
Both cases are documented here explicitly.

## Core Invariant and Authority Split

The most important architectural invariant in this repo is that `agent-git-service` is Git-backed.
Repository content, history, refs, diffs, merges, rebases, and Git transport semantics should stay grounded in real Git behavior.

Authority is split by concern:

- `gitstore` owns Git-native repository state and operations.
- `db` owns higher-level relational metadata such as users, auth, issues, pull requests, reviews, labels, workflow records, and similar product state.
- `service` is the coordination layer when a flow needs both Git-backed and DB-backed state.
- surface packages should respect that ownership instead of inventing parallel storage rules.

These contracts do not forbid repository- or pull-request-related metadata in the database.
The narrower rule is that Git-native behavior must stay Git-backed, while relational metadata stays DB-backed.

Deployment topology is intentionally out of scope for this document.
The same module contracts should hold for a local all-in-one setup or a split, stateless deployment with external services.

## Core Layers

The main runtime layers are:

- `router`
- `middleware`
- `rest`
- `graphql`
- `controlplane`
- `service`
- `db`
- `gitstore`

Supporting packages such as `config`, `oauth`, `authn`, `githttp`, `oidc`,
`connectedlogin`, `rest/respond`, `rest/transform`, `tenant`, `ratelimit`,
`metrics`, `logging`, `httputil`, `testharness`,
`apperrors`, `crypto`, `embedding`, and `randutil` are included where they
materially affect the contracts.

The public import surface is intentionally small:

- `config` exposes environment-backed startup configuration.
- `server` exposes the embeddable composition-root APIs (`New`, `Run`, `RunWikiReindex`, `Start`, `Shutdown`, and mountable handlers).
- Everything else in the root module remains internal-only unless documented otherwise.

## Top-Level Internal Package Inventory

This is the top-level contract inventory for `internal/*`.
When a new top-level package is introduced, add it to this table and then
document the relevant contract below in the same change.

| Package | Primary responsibility |
|---|---|
| `apperrors` | shared sentinel error catalog and helpers |
| `authn` | low-layer token-resolver interface and auth sentinel errors |
| `controlplane` | control-plane schema plus token-to-tenant DB routing |
| `crypto` | NaCl-based secret encryption helpers |
| `db` | relational schema, migrations, seed data, and model types |
| `embedding` | outbound embedding-provider integration |
| `githttp` | Git Smart HTTP transport bridge |
| `gitstore` | on-disk bare repository operations |
| `graphql` | GitHub GraphQL API surface |
| `httputil` | bounded outbound HTTP helpers |
| `logging` | structured logging and request-scoped log attributes |
| `metrics` | Prometheus collectors and metric-recording helpers |
| `mentions` | GitHub-style mention token parsing helpers |
| `middleware` | auth, logging, rate-limit, and request guards |
| `oidc` | generic OIDC discovery, device flow, and JWKS-backed ID token verification |
| `oauth` | OAuth device-flow HTTP endpoints |
| `randutil` | shared random helper functions |
| `ratelimit` | GitHub-compatible rate-limit snapshot helpers |
| `rest` | GitHub REST API surface |
| `router` | route registration and host rewrite |
| `service` | business logic and cross-store orchestration |
| `connectedlogin` | configurable OAuth-style browser-login code exchange and userinfo client |
| `tenant` | gitstore tenant context helpers for physical repo scoping |
| `testharness` | production-wired service and router test fixtures |
| `wikicatalog` | legacy wiki catalog primitives, slug canonicalization, and transitional blob/CAS helpers |
| `wikiv2` | git-authoritative wiki write planning, derived index contracts, and reconcile primitives |

## Dependency Rules

| Layer | Owns | May call directly | Must not call directly |
|---|---|---|---|
| `router` | route registration, host rewrite, top-level HTTP composition | `middleware`, `rest`, `graphql`, `githttp`, `oauth` | `db`, `gitstore`, GORM queries, business logic |
| `middleware` | auth extraction, request guards, context injection | `controlplane`, `service` auth methods, `rest/respond`, `logging`, `metrics`, `ratelimit` | `db`, `gitstore`, REST handlers, GraphQL resolvers |
| `rest` | HTTP request decode, REST response codes, REST JSON shapes | `service`, `controlplane`, `rest/respond`, `rest/transform`, `ratelimit`, `db` model types, `Svc.Git` via `*service.Service` | GORM queries, GraphQL helpers |
| `graphql` | GraphQL request parse, resolver dispatch, GraphQL response shapes, field filtering | `service`, `db` model types, `rest/respond` for HTTP JSON writeout, selected `Svc.Git` and `Svc.DB` access via `*service.Service` | `rest/transform` |
| `controlplane` | control-plane schema, token-to-tenant DB routing, tenant-user bootstrap | `db`, `crypto`, GORM, standard library | `router`, `rest`, `graphql`, `gitstore`, transport rendering |
| `service` | business rules, persistence orchestration, Git orchestration, domain side effects | `db`, `gitstore`, `embedding`, `oidc`, `connectedlogin` | `router`, `middleware`, `rest`, `graphql`, HTTP response helpers |
| `db` | schema, migrations, seed data, relational model types, shared state constants | GORM and standard library only | `service`, `rest`, `graphql`, `gitstore` |
| `gitstore` | Git-native repo lifecycle, refs, merge/rebase/diff/content/archive operations | system `git`, go-git, filesystem, `tenant` | `db`, `rest`, `graphql` |

## Layer Contracts

### `router`

Ownership:

- builds the HTTP tree in one place
- decides which endpoints are authenticated, optionally authenticated, or unauthenticated
- performs host rewrite for `api.github.localhost`

Rules:

- `router` is composition only
- it may wire handlers together, but it should not implement business logic
- it should not inspect the database or Git state directly

Current state:

- clean boundary overall
- `router` depends on surface handlers and middleware, not on `db` or `gitstore`

### `middleware`

Ownership:

- parse auth headers
- validate tokens
- inject current user into request context
- enforce request body limits
- preserve optional-auth behavior on discovery endpoints that `gh` uses for server discovery and auth bootstrap

Rules:

- middleware may reject requests before they reach surface handlers
- middleware owns API auth handling for REST and GraphQL routes
- discovery endpoints under `/api/v3`, `/api/v3/meta`, and `/api/v3/rate_limit` are intentionally optional-auth because clients probe them before or during auth setup
- middleware must not issue GORM queries or inspect Git repositories directly

Current state:

- `TokenAuth` and `OptionalTokenAuth` depend on a concrete `*service.Service`
- this is a real coupling, not just documentation

Assessment:

- acceptable for now
- highest-value candidate for a narrow interface later because the required contract is small:
  - `ValidateToken`
  - `ResolveUserByToken`

### `rest`

Component reference: [architecture/rest.md](architecture/rest.md)

Ownership:

- decode REST path, query, and JSON body inputs
- perform transport-level validation such as required fields and type parsing
- call service methods
- transform service and DB objects into GitHub REST JSON shapes
- serialize collaboration-governance responses such as org invitations, team-repo permission maps, and org member versus outside-collaborator annotations
- map service errors to HTTP status codes through `rest/respond`

Rules:

- REST handlers should stay thin
- REST may use `db` structs as wire types and input containers
- REST must not run GORM queries directly
- target boundary: REST should not call `gitstore` directly

Current state:

- the package is wired through `rest.Deps{Svc: *service.Service}`
- handlers consistently use `respond.ServiceError`, `respond.ValidationFailed`, and `transform.*`
- REST now owns the transport layer for explicit org creation, org invitations, team-repo grants, and outside-collaborator listing, while delegating the underlying policy and persistence to `service`
- many Git-centric handlers still call `d.Svc.Git.*` directly for branch, tag, diff, archive, search, and ref operations
- repo JSON shapes and collaborator lists now expose canonical `read`/`write`/`admin` authorization decisions through `service.HasRepoAccess`, while still serializing GitHub-compatible permission flags for transport compatibility
- REST does not run GORM queries directly today
- this is a real current coupling to Git infrastructure, not just a future concern

Assessment:

- acceptable for now
- the current package boundary is "thin transport plus direct Git access through `Svc`", not "service-only"
- interface extraction is optional, not mandatory
- the main reason to introduce narrower interfaces later would be easier handler-only tests, not runtime flexibility

### `graphql`

Component reference: [architecture/graphql.md](architecture/graphql.md)

Ownership:

- parse GraphQL requests
- route queries and mutations
- build GraphQL-specific response shapes
- filter response fields to the requested selection set

Rules:

- GraphQL owns GraphQL response assembly
- GraphQL should not reuse REST transform code because REST and GraphQL contracts differ
- target boundary: GraphQL should not bypass the service layer for persistence or Git logic

Current state:

- `graphql.Server` depends on a concrete `*service.Service`
- GraphQL writes HTTP JSON through `rest/respond`, but it builds its own GraphQL payloads
- GraphQL contains important mutation flows such as `revertPullRequest` and ProjectV2 operations
- repository GraphQL shapes compute `viewerPermission` through `service.HasRepoAccess`, exposing `READ`, `TRIAGE`, `WRITE`, `MAINTAIN`, or `ADMIN`
- resolvers still reach into `s.Svc.Git` for bare read operations (HeadSHA, ListTags, CreateBranchFromOid); the business-rule paths — compare, mergeability, merge-simulation, branch update, revert — now flow through typed service methods

Assessment:

- acceptable for now because GraphQL has a large surface and a broad service dependency set
- GraphQL business-rule Git operations are now service-backed; the remaining `Svc.Git` calls are narrow Git reads, acceptable as an internal coupling
- worth formalizing by tests before attempting interface extraction
- if narrower interfaces are introduced here, they should be domain-grouped and incremental, not package-wide for its own sake

### `service`

Component reference: [architecture/service.md](architecture/service.md)

Ownership:

- all business rules
- GORM persistence orchestration
- Git orchestration through `gitstore`
- cross-entity lifecycle changes
- organization governance flows such as explicit org creation, org membership, org invitations, team-repo grants, and outside-collaborator reconciliation
- effective repository permission resolution across org base permission, direct collaborators, and team grants
- domain side effects such as workflow sync and embedding follow-up
- translating persistence errors into stable sentinel errors

Rules:

- `service` is the only layer that should coordinate both relational state and Git state
- `service` returns Go values and errors, not HTTP responses
- `service` may launch domain background work when that work belongs to domain consistency rather than transport

Current state:

- `service.Service` owns:
  - `*gorm.DB`
  - `*gitstore.Store`
  - `BaseURL`
  - `embedding.Embedder` (optional, activated when embedding API key is configured)
  - `AllowAnyToken bool` (local-development convenience to accept any non-empty token)
- the concrete service now also owns the collaboration policy helpers that normalize permission vocabularies, resolve effective repo access, and reconcile org membership versus outside-collaborator state
- no authoritative service-interface catalog exists in the current tree; concrete service methods are the implemented contract

Assessment:

- strong boundary conceptually
- future interfaces should be introduced only where a specific surface needs a narrower seam

### `db`

Ownership:

- schema and model definitions
- database initialization and migration
- seed data
- shared state constants such as issue and PR states

Rules:

- `db` should remain infrastructure-only
- it must not know about HTTP, handlers, or Git transport
- model types may be shared outward, but business rules must stay outside `db`

Current state:

- clean boundary overall
- models are imported widely as data types, which is acceptable

### `gitstore`

Component reference: [architecture/gitstore.md](architecture/gitstore.md)

Ownership:

- repository existence, init, fork, delete
- refs and branches
- merge, rebase, compare, diff, archive, file-content operations
- write serialization per repository

Rules:

- `gitstore` is infrastructure, not business policy
- it should not make database decisions
- it should not shape HTTP responses

Current state:

- clean boundary overall
- `githttp` uses `gitstore` directly for repository transport work, which is appropriate

## Supporting Runtime Packages

### `oauth`

Component reference: [architecture/oauth.md](architecture/oauth.md)

Ownership:

- OAuth device-flow HTTP endpoints and HTTP-specific response format

Rules:

- may call service auth methods
- must not persist directly through GORM

Current state:

- depends on concrete `*service.Service`
- acceptable for now because the package is small and already directly testable

### `githttp`

Component reference: [architecture/git-http.md](architecture/git-http.md)

Ownership:

- Git Smart HTTP transport
- delegation to `git-http-backend`
- transport-level repository existence bootstrap
- post-push follow-up triggering

Rules:

- may call `gitstore` directly for repository existence and transport support
- may trigger service follow-up work that belongs to repository consistency
- should not own business rules such as PR merge policy or workflow semantics
- must not treat `REMOTE_USER` or CGI environment variables as an authorization decision

Current state:

- depends on both `*gitstore.Store` and concrete `*service.Service`
- after push it runs `fixHEAD()` and `Svc.SyncWorkflowsFromRepo(...)`
- auth and authorization now run inline: `resolveRepoContext` reads the
  current user from the request context (populated by the standard API
  middleware) and calls `service.HasRepoAccess` to enforce read/write
  effective permission, falling back to anonymous read only for public repos
- still treats `owner/repo` as the logical repository identity, while
  `gitstore` may add tenant-scoped physical roots beneath that identity

Assessment:

- acceptable for now
- worth keeping visible as technical debt because one handler currently mixes:
  - Git transport
  - repo bootstrap
  - post-push follow-up
- product priority remains GitHub-compatible API and repo workflows, with gh CLI compatibility as a first-class compatibility target, so future hardening should treat this package as a support surface for repo workflows rather than as an independent feature area

### `rest/respond`

Ownership:

- GitHub-style HTTP JSON responses
- REST error mapping from service sentinel errors

Rules:

- surface packages may use it as an HTTP writer helper
- service code must not depend on it

Current state:

- used by REST, GraphQL, OAuth, and Git HTTP-adjacent paths for HTTP writing
- acceptable because the dependency points toward transport helpers, not back into business logic

### `rest/transform`

Ownership:

- REST-only JSON shape conversion

Rules:

- only REST should depend on this package
- GraphQL should keep building GraphQL-native shapes

Current state:

- this rule already holds and should be preserved

### `config`

Ownership:

- environment-backed process configuration
- validation of startup-only configuration invariants

Rules:

- `config` is a startup helper, not a runtime service locator
- it must not depend on `service`, transport packages, or persistence packages

Current state:

- `main` is the primary consumer
- the package now owns control-plane, OIDC, logging, and multi-listener configuration flags

### `controlplane`

Ownership:

- control-plane schema (`CPUser`, `CPToken`)
- token-to-tenant DB resolution
- cached tenant `*gorm.DB` handles and first-open tenant migration
- tenant-scoped `db.User` bootstrap so service-layer current-user lookup works

Rules:

- may read and write the control-plane database
- may open tenant databases and run `db.Migrate(...)` on first access
- must not render HTTP responses or own GitHub product business rules

Current state:

- `middleware.TokenAuth` and `OptionalTokenAuth` delegate to `DBRouter.ResolveToken(...)` when control-plane mode is enabled
- `rest.Deps` carries a concrete `*controlplane.DBRouter` for agent and control-plane-aware routes
- `main` owns control-plane process lifecycle and shutdown

Assessment:

- runtime-critical in multi-tenant mode
- belongs in the explicit contract surface rather than being treated as incidental glue

### `oidc`

Ownership:

- generic OIDC discovery document loading
- generic device-authorization and token exchange helpers
- JWKS-backed ID token verification and claim decoding for provider-neutral login

Rules:

- may perform outbound HTTP and JWT validation
- must stay transport-agnostic and must not persist application users or tokens directly
- owns low-level discovery and verification helpers, while provider-to-local-user mapping remains in `service`

Current state:

- `main` constructs the client and injects it into `service.Service.OIDC`
- REST handlers under `/api/v3/oidc/*` call service methods, not the client directly

### `connectedlogin`

Ownership:

- configurable OAuth-style browser login URL generation
- authorization-code token exchange against the configured token path
- bearer-token userinfo lookup and claim extraction for non-OIDC providers

Rules:

- may perform outbound HTTP and provider response validation
- must stay transport-agnostic and must not persist application users or tokens directly
- must remain provider-neutral; provider-specific behavior belongs in deployment configuration such as endpoint paths and claim names

Current state:

- `main` constructs the client and injects it into `service.Service.ConnectedLogin`
- REST handlers under `/auth/connected/*` call service methods, not the client directly

### `authn`

Ownership:

- low-layer authentication interfaces and shared auth sentinel errors

Rules:

- `authn` must stay dependency-light so `middleware`, `controlplane`, and `rest`
  can share token-resolution contracts without import cycles
- it must not depend on transport rendering or business logic

Current state:

- `authn.TokenResolver` is the abstraction for token -> tenant user + tenant DB resolution
- `middleware` re-exports the type for convenience, while `controlplane.DBRouter` is the main production implementation

### `tenant`

Ownership:

- gitstore tenant context keys and extraction helpers for physical repository scoping

Rules:

- infrastructure-only package
- must stay small, transport-agnostic, and free of business rules

Current state:

- `gitstore` depends on `tenant.FromContext(...)` for per-tenant filesystem roots and lock keys
- `service.ContextWithTenant(...)` and `service.TenantFromContext(...)` now delegate to the shared `tenant` package for compatibility, so middleware and gitstore use one tenant-context contract

### `wikiv2`

Component reference: [architecture/wiki-storage-v2.md](architecture/wiki-storage-v2.md)

Ownership:

- git-authoritative wiki path and slug translation helpers
- durable ref compare-and-swap primitives for wiki writes
- derived index contracts for reconcile progress and live page projections
- manual reconcile request and result types shared by service orchestration

Rules:

- `wikiv2` defines storage and reconcile primitives, not HTTP handlers or route contracts
- it may depend on low-level git and wiki catalog validation helpers, but it must not issue GORM queries or shape transport responses
- service owns permission checks, orchestration, and lifecycle policy around these primitives

Current state:

- `service` uses `wikiv2` for slug/path parity, write-plan creation, and manual reconcile entrypoints
- `db` owns the concrete `wiki_page_index`, `wiki_index_state`, `wiki_backlinks`, and optional `wiki_page_history` tables, while `wikiv2` owns the domain contracts those tables implement
- the package is additive and does not yet replace the existing routed wiki handlers or all catalog-derived projections

### `ratelimit`

Ownership:

- GitHub-compatible rate-limit snapshots and headers

Rules:

- transport helper only
- must not own quota enforcement or persistence

Current state:

- REST uses it for `/api/v3/rate_limit`
- middleware and responders may reuse the same request-scoped snapshot so headers and JSON payloads stay consistent

### `metrics`

Ownership:

- Prometheus collector registration
- package-level recorder facade used by middleware and background jobs

Rules:

- instrumentation only
- must not own business logic or request routing decisions

Current state:

- `main` registers `/metrics`
- `middleware` records HTTP/request-operation metrics

### `mentions`

Ownership:

- exact GitHub-style `@login` token extraction and lookup helpers
- shared mention parsing semantics reused across notification and search flows

Rules:

- string parsing only
- must not depend on service, storage, or transport packages

Current state:

- `service/notification` uses it to expand user mentions into notification recipients
- `service/pr` and `service/search` use it to enforce mention-token boundaries instead of raw substring matches

### `logging`

Ownership:

- process-wide `slog` initialization
- request-scoped structured log attributes
- GORM logger integration

Rules:

- infrastructure-only package
- may enrich logs with context, but must not influence business decisions

Current state:

- `main` initializes logging before other startup work
- middleware and services add request attributes and clone them into background work

### `httputil`

Ownership:

- bounded helper utilities for outbound HTTP clients

Rules:

- only for server-to-server HTTP helpers
- must not become a generic transport abstraction layer

Current state:

- `embedding` uses it to cap error-body reads on upstream failures

### `testharness`

Ownership:

- production-wired service and router test fixtures
- reusable HTTP integration setup for package tests and benchmarks

Rules:

- testing-only package
- runtime packages must not depend on it

Current state:

- the integration strategy relies on `testharness.New(...)` and `testharness.NewService(...)` rather than mock-only seams

## Cross-Cutting Responsibility Contracts

### Authentication

Ownership split:

- `middleware`: extract API auth headers, reject malformed or missing credentials, and inject request-scoped auth context
- `server`: optional public embedding seam that can accept a trusted host authenticator, then adapt it into the shared middleware pipeline
- `auth`: public identity shape for embedded hosts using the server package
- `controlplane`: in multi-tenant mode, resolve token -> `CPUser` -> tenant `*gorm.DB`, and ensure the tenant-local `db.User` exists
- `service`: validate API tokens and resolve user-by-token in single-DB mode; persist application users and tokens for OIDC-backed human login; map trusted embedded identities onto internal `db.User` + `UserIdentity` rows
- `oidc`: perform provider-neutral discovery, device-flow requests, and ID token verification
- `connectedlogin`: perform configurable OAuth-style code exchange and userinfo lookup for providers without standard OIDC discovery
- `githttp`: uses the same auth middleware on Git routes, with `TokenAuth` in control-plane mode and `OptionalTokenAuth` in single-DB mode
- `rest` and `graphql`: consume `GetCurrentUser(ctx)` and assume middleware has prepared the context

Rule:

- surface handlers must not parse auth headers themselves
- control-plane routing and single-DB validation are both first-class current auth paths
- embedded single-DB hosts may inject a trusted identity through `server.WithAuthenticator`; middleware must still be the single place that turns that identity into request context
- the trusted identity contract requires non-empty `Provider`, `Subject`, and `Login`; AGS owns the mapping from that tuple onto `db.User` + `db.UserIdentity`
- when embedded identity is present in single-DB mode, it takes precedence over `Authorization` headers and must flow through every REST/GraphQL/Git route family that already depends on optional or required auth context, including `/api/v3/rate_limit` and `/api/v3/users/{username}/starred`
- outbound identity-provider clients such as `oidc` and `connectedlogin` must not write application state directly
- control-plane mode currently stays fail-closed for embedded identities until a tenant-aware resolver contract is added

### Collaboration Authorization

Ownership split:

- `service`: normalize repository permission vocabularies, resolve effective repo access across org base permission, collaborator grants, and team grants, and reconcile org membership with outside-collaborator rows
- `rest` and `graphql`: call the shared service policy and expose transport-specific permission shapes and authz failures
- `githttp`: calls the same shared service policy for Git read/write authorization

Rule:

- only `service` should combine collaborator, team, and org-membership state into an effective repository permission decision
- transport layers may map that decision into their own response shapes, but they must not fork the policy

Current state:

- REST, GraphQL, and Git Smart HTTP all now rely on `service.HasRepoAccess`
- Git transport still remains a separate surface and route family, but its permission decision no longer forks the core repo-access policy

### Request Validation

Ownership split:

- `rest` and `oauth`: syntactic and transport validation
- `graphql`: GraphQL request parsing and argument presence checks
- `service`: domain validation and state-machine validation

Rule:

- reject malformed transport input in the surface layer
- reject invalid domain transitions in `service`

### Response Transformation

Ownership split:

- REST: `rest/transform`
- GraphQL: GraphQL resolver and shape helpers
- OAuth and Git HTTP: package-local response formatting

Rule:

- service returns domain data, never GitHub REST or GraphQL payload maps

### Persistence

Ownership split:

- `controlplane`: global agent identity, auth tokens, tenant DSN mappings, and tenant DB selection
- `db`: schema, migration, and tenant-local relational metadata
- `service`: all tenant-local GORM-backed reads and writes plus cross-store coordination through `DBForCtx(ctx)`
- `gitstore`: all Git-backed reads and writes for repository content, history, refs, diffs, merges, rebases, and related Git-native state
- `tenant`: gitstore tenant context used for physical repository scoping when tenant isolation is enabled

Rule:

- target boundary: REST, GraphQL, middleware, OAuth, and router should not talk to GORM directly
- only `service` coordinates tenant-local GORM state and Git state together
- in multi-tenant mode, request-scoped tenant DB selection must happen before service methods run, through `controlplane.DBRouter` + `service.ContextWithDB(...)`
- database-backed metadata is allowed even for repository or pull-request domains, but it must not replace Git as the authority for Git-native behavior
- current wiki rule: the sibling `*.wiki.git` repo is authoritative for wiki page content, path layout, commit history, and lexical search recall, while TiDB-backed wiki tables remain rebuildable derived indexes and transitional compatibility surfaces until the final `#1488` cleanup lands
- `wikicatalog` remains in the tree only as transitional logic that still backs some routed handlers and migration paths; it must not be treated as the long-term durable authority
- issue `#1488` tracks the remaining cleanup toward a fully git-authoritative wiki stack; see `docs/architecture/wiki-storage-v2.md` for the approved target design

Current state:

- the target rule holds for REST, middleware, OAuth, router, Git HTTP, and GraphQL
- `service.DBForCtx(ctx)` is the tenant-aware DB entrypoint in production code, while `controlplane.DBRouter` chooses the concrete tenant DB in multi-tenant mode

### Side Effects and Background Follow-Up Work

Ownership split:

- `main`: process lifecycle and listener goroutines
- `githttp`: transport-triggered post-push follow-up kickoff
- `service`: domain-owned async work such as embeddings and workflow sync implementation

Rule:

- background work should live where ownership is clear
- transport layers may trigger domain follow-up, but they should not silently absorb large amounts of business logic

### Error Mapping Across Boundaries

Ownership split:

- `service` and `internal/apperrors`: define stable sentinel errors such as `ErrNotFound`, `ErrConflict`, `ErrInvalidState`, `ErrValidation`, and prefer returning them for transport-visible failures
- REST: map sentinel errors to HTTP via `respond.ServiceError`
- GraphQL: return GraphQL error payloads
- OAuth: map service auth errors to OAuth-specific error responses
- Git HTTP: map missing repos to 404 and internal transport failures to 500

Rule:

- `service` owns semantic error categories, but current implementation is mixed between sentinel-based errors and plain wrapped errors
- each surface owns its own transport-specific rendering

Current state:

- many state and persistence paths already return sentinel-wrapped errors
- some transport-visible service failures are still plain `fmt.Errorf(...)` values, including merge-path failures in `service/pr_merge.go`
- REST only maps known sentinel categories specially; non-sentinel service errors can still collapse to HTTP 500

## Current Concrete Couplings Audit

| Coupling | Where | Status | Rationale |
|---|---|---|---|
| `router -> rest/graphql/githttp/oauth/middleware` | route composition | intended | router is the composition root for HTTP |
| `middleware -> *service.Service` | auth middleware | technical debt worth tracking | very small contract, likely worth narrowing later |
| `middleware -> authn.TokenResolver` | auth middleware in multi-tenant mode | intended | middleware depends on a low-layer token-resolution contract, typically implemented by `controlplane.DBRouter` |
| `rest -> *service.Service` | `rest.Deps` | acceptable for now | broad service surface; real-router integration tests are higher value than mock seams right now |
| `rest -> authn.TokenResolver` | agent + control-plane-aware handlers | acceptable for now | REST owns the transport layer for agent registration and control-plane-backed identity flows while keeping the resolver contract narrow |
| `rest -> Svc.Git` | Git handlers, PR diff, release archive, search, deployment helpers | accepted current coupling | multiple REST paths still reach Git operations through the concrete service dependency |
| `graphql -> *service.Service` | `graphql.Server` | acceptable for now | large resolver surface; stabilize tests first |
| `graphql -> Svc.Git` | repo detail reads (HeadSHA, ListTags, CreateBranchFromOid) | accepted current coupling | bare-read paths; compare/mergeability/merge-simulation/revert/update-branch now flow through `Svc` service methods (`ComparePR`, `CanMergePR`, `SimulatePRMerge`, `UpdatePRBranch`, `RevertPRMerge`) |
| `oauth -> *service.Service` | OAuth handler | acceptable for now | small package; current direct wiring is simple |
| `githttp -> *gitstore.Store` | Git transport | intended | transport handler needs direct repo access |
| `githttp -> *service.Service` | ensure repo exists, post-push follow-up | acceptable but visible debt | transport + follow-up logic are coupled in one package |
| `service -> oidc.Client` | generic human-login flows | acceptable for now | keeps provider-neutral OIDC protocol work outside business-state orchestration |
| `service -> connectedlogin.Client` | non-OIDC connected-login flows | acceptable for now | keeps configurable OAuth-style protocol work outside business-state orchestration |
| `gitstore -> tenant` | per-tenant repo roots and lock keys | intended | physical repo scoping is an infrastructure concern, not a service concern |

## Refactors Worth Doing

These are the follow-ups most likely to improve maintainability or testability:

1. Introduce a tiny auth interface for middleware instead of depending on the full `*service.Service`.
2. Build router-level integration tests on the real handler tree before adding more mock-heavy handler tests.
3. Keep GraphQL on the concrete service for now, but group its future seams by domain if integration tests prove a specific split is valuable.
4. Consider isolating post-push follow-up from `githttp` if Git transport tests or workflow sync logic become harder to reason about.

## Refactors Not Worth Doing By Default

These should not be treated as mandatory:

- replacing every surface dependency with interfaces immediately
- abstracting `db` behind another repository layer
- abstracting `gitstore` behind a generic storage interface without a concrete testing need
- forcing REST and GraphQL to share the same response transformation package

## Testing Implications

The testing strategy should match the contracts above:

- package and domain tests should focus on `service`, `gitstore`, `controlplane`, and GraphQL internals
- HTTP integration tests should exercise the real router plus middleware plus surface handlers
- `testharness` should remain the default production-wired integration fixture
- gh CLI compatibility tests should remain an end-to-end compatibility net within the broader GitHub-compatible API target

Practical consequence:

- because middleware, REST, GraphQL, OAuth, and parts of Git HTTP still depend on the concrete service, real integration tests are currently a better investment than a full mock-based surface test architecture

## Decision Rule For Future Changes

Before introducing a new interface or moving logic across packages, answer:

1. Does this change improve testability at the layer where bugs actually occur?
2. Does it clarify ownership, or only add indirection?
3. Does it remove a concrete coupling that is causing real friction today?

If the answer is "no" to all three, prefer documentation and tests over abstraction.
