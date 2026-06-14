# Test Strategy

This document is the execution plan for increasing confidence in `agent-git-service`.
The current direction is bottom-up:

1. strengthen package-level and domain-level tests first
2. add real HTTP integration tests through router and middleware
3. keep gh CLI compatibility tests and shell E2E tests as the compatibility and end-to-end layer

The first goal is not broad coverage for every endpoint.
The first goal is confidence in the main user paths and the highest-risk behavior.

For the current dependency seams and acceptable concrete couplings, see `docs/module-contracts.md`.

## Priorities

The major paths to protect first are:

- discovery and auth bootstrap flows (`/api/v3/`, `/api/v3/meta`, `/api/v3/rate_limit`, token login, `gh auth login --with-token`, and `gh auth setup-git` / Git credential setup)
- token auth, embedded auth, and context-scoped DB correctness
- organization collaboration-governance flows (explicit org creation, org invitations, team-repo grants, outside collaborator lifecycle, effective permission precedence)
- repository create, clone, fork, transfer, and delete
- issue create, edit, comment, label, assignee, and search flows
- pull request create, view, diff, review, and merge flows
- release create plus asset upload and download
- workflow dispatch, rerun, cancel, logs, and artifact flows

These priorities are intentionally GitHub-compatible API-server centric. gh CLI compatibility is still a first-class compatibility target, and Git transport tests matter because repository and PR flows depend on real clone/fetch/push behavior.

## Principles

- prefer fast, deterministic tests close to the logic
- use real DB and Git behavior for merge, diff, and lifecycle flows
- test current architecture honestly instead of designing around an interface boundary that does not exist yet
- use gh CLI acceptance tests to validate CLI compatibility, not as the first line of defense for every edge case
- add breadth after core-path confidence is in place

## CI Layers

The repository now follows a layered merge gate adapted to what is stable today.

### Layer 1: Regression Gate

Run with:

```bash
bash scripts/regression_gate.sh
```

This gate is intentionally small and deterministic.
It is the merge-blocking pack for fast feedback, not the place to dump every existing suite.

Its responsibilities are:

- compile the backend tree with `go test ./... -run '^$'`
- compile the vendored `cli/` tree and `cli/_go-gh-local` without turning the known-flaky full CLI suite into a blocking behavior gate
- run the stable package tests listed in `scripts/regression_gate.list`
- run `scripts/wiki_regression_gate.sh`, which protects the stale wiki tree/search/backlink projection class without promoting the full service package into the fast gate

Promotion rule: when a bug fix or repaired area needs recurring protection, add a stable test to the package tree and then promote that package or script into `scripts/regression_gate.list`.

Remote wiki console checks are not part of the deterministic local gate. Use `scripts/wiki_remote_integrity.sh` during deploy verification to crawl tree-emitted page URLs and validate search/backlink page URLs against the running service.

### Layer 2: Integration Tests

Run with:

```bash
bash scripts/integration_tests.sh
```

This gate makes the stable real-router integration pack merge-blocking.
It is intentionally package-oriented: the same command should run locally and in CI without requiring a pre-started server.

Its responsibilities are:

- run `./internal/testharness`
- run `./internal/router`
- run `./internal/githttp/...`
- run the stable integration subset inside `./internal/rest`
- run the stable integration subset inside `./internal/graphql`

### Layer 3: Runtime Smoke Gates

Run with:

```bash
bash scripts/backend_smoke.sh
make test-migrate-tidb
```

These gates prove the built server still works as a real process, not just as isolated packages.

- `scripts/backend_smoke.sh` is the low-dependency startup smoke:
  - boot the server binary with a fresh TiDB playground database
  - verify `/readyz`, `/api/v3/`, `/api/v3/meta`, and `/api/v3/rate_limit`
  - create a repository through the REST API
  - verify authenticated Git Smart HTTP against the live server
- `make test-migrate-tidb` is the TiDB migration smoke:
  - start the test-only TiDB playground
  - create a fresh temporary TiDB database
  - boot the server against that TiDB DSN and wait for `/readyz`
  - fail early on TiDB-incompatible migration or bootstrap DDL

The smoke layer should stay small enough to run on every PR, but realistic enough to catch wiring and startup regressions the regression gate cannot see.

### Layer 4: Compatibility And E2E Gates

Run with:

```bash
make test
make test-e2e
```

These gates cover the higher-fidelity compatibility and shell E2E paths that are stable enough to be merge-blocking today.

- `make test` boots against a live TiDB-backed `github.localhost` server and runs the vendored gh CLI compatibility inventory
- CI mirrors the local acceptance path with the test-only TiDB playground: `make test-setup`, a Docker access verification step (`docker --version` plus `docker run --rm hello-world`), `ENABLE_WORKFLOW_EXEC=1 make run-bg`, `make test`, then `make test-clean-all`
- `make test-e2e` runs the full shell E2E inventory under `e2e/`
- the full E2E inventory includes `e2e/repo-transfer-lifecycle.sh`, so repo transfer remains merge-blocking without a separate standalone smoke job
- the full E2E inventory also includes `e2e/agent-auth-flow.sh`, which now asserts the agent-binding confirm contract for canonical agent tokens plus the human-token `403`, invalid-invite `422`, and consumed-invite `409` failure paths
- CI mirrors the local full E2E path too with the test-only TiDB playground: `make test-setup`, `ENABLE_WORKFLOW_EXEC=1 make run-bg`, `make test-e2e`, then `make test-clean-all`
- `make run-bg` waits up to `STARTUP_WAIT_SECONDS` seconds for `/readyz`; the default intentionally leaves enough room for first-start TiDB schema migration in the compatibility and E2E test lanes

#### OAuth Device Flow E2E

`e2e/oauth-device-flow.sh` is the merge-blocking shell regression for the live OAuth device bootstrap path.
It keeps the runtime contract executable across the full device flow:

- `POST /login/device/code` returns a device code, user code, verification URI, and the expected `expires_in` and `interval` polling metadata
- `POST /login/device` approval plus `POST /login/oauth/access_token` exchange yields a usable bearer token, with an optional `gh auth` verification step when the CLI is installed
- `GET /login/oauth/authorize` accepts same-origin and loopback redirect URIs while rejecting cross-origin callbacks
- malformed, missing, and form-encoded device-code exchange requests fail or recover in the expected way, including repeated polling after approval
- success and error payloads do not leak issued access tokens or device codes

### Layer 5: Documentation Gates

Run with:

```bash
python3 scripts/doc_lint.py
bash scripts/check-module-contracts.sh
```

The documentation gate is enforced in GitHub Actions and can also be run
locally. Its purpose is to stop process drift:

- doc-lint catches stale workflow inventories, broken internal links, and missing CI/test docs
- doc-lint also runs `scripts/check-module-contracts.sh` so every top-level `internal/*` package must have a contract entry in `docs/module-contracts.md`

### Workflow Execution Testing

Workflow execution is tested at multiple layers:

**Unit tests** (`internal/service/workflow_exec_internal_test.go`):
- `writeWorkflowSandboxFiles` - validates sandbox script and launcher creation
- `buildWorkflowLauncherScript` - validates environment variable injection and script generation
- `parseWorkflowEnv` - validates environment variable parsing and validation
- `shellQuote` - validates shell escaping for environment values

**Integration tests** (`internal/service/workflow_dispatch_exec_test.go`):
- Full workflow dispatch and execution flow
- Docker sandbox isolation verification
- Artifact creation from workflow steps
- Secret injection and environment resolution
- Timeout and cancellation behavior
- Artifact path traversal rejection (security hardening)

**Coverage tracking** (`internal/service/workflow_coverage_test.go`):
- Tracks coverage across workflow-related code paths
- Ensures workflow execution surface changes are visible in coverage reports
- Deadline-exceeded artifact collection handling

The workflow execution sandbox is **fail-closed by default** and requires `ENABLE_WORKFLOW_EXEC=1` to activate.
Tests verify both the disabled state (workflow runs fail immediately with audit logging) and the enabled state (steps execute in isolated containers).

## Current Reality

Today the repository already has useful tests in:

- `internal/service` (including `auth_test.go` for auth service flows)
- `internal/gitstore`
- `internal/graphql`
- `internal/middleware` (including `auth_test.go` for middleware auth)
- `internal/oauth`
- `internal/rest/respond`
- `internal/rest/transform`
- `internal/rest` (dedicated handler tests: `handlers_branch_test.go`, `handlers_dependabot_test.go`, `handlers_deployment_test.go`, `handlers_gist_test.go`, `handlers_webhook_test.go`, `handlers_webhook_delivery_test.go`, `pagination_test.go`)
- `internal/router` (router-level integration tests in `router_test.go`)
- `config`
- `internal/oidc`
- `internal/embedding` (including `embedder_test.go`)
- `internal/apperrors`
- `internal/testharness` (reusable HTTP integration test harness with smoke tests)

The main gaps are:

- limited coverage for real Git merge, rebase, compare, and diff behavior
- real Docker-sandbox workflow execution still depends more on CI and smoke gates than on direct package tests
- too much reliance on end-to-end acceptance tests for HTTP-path confidence
- targeted collaboration-governance coverage now exists, but it needs to stay part of the routine regression path rather than living only in one-off branch work

Workflow execution sandbox coverage was added in 2026 with:
- unit tests for sandbox file writing, environment parsing, and shell escaping
- integration tests for full workflow dispatch and Docker-based step execution
- coverage tracking across workflow-related code paths
- security hardening tests for artifact path traversal and timeout handling

The repo also has focused shell end-to-end coverage under `e2e/`, including org collaboration governance and code search. Keep those flows in the normal regression toolbox when a change spans multiple endpoints and is awkward to express through one package test. The code search flow also creates repository contents through the GitHub-compatible Contents API, so it should keep asserting the GitHub create status contract while protecting repository-scoped search boundaries.

The main CI gate now makes the full backend `go test ./...` inventory merge-blocking via `make test-unit`.
CI shards that inventory with `GO_TEST_PACKAGES` so each shard has an isolated TiDB playground instead of forcing the slowest database-backed packages to contend for one local TiDB instance.
The vendored `cli/` inventory is still not merge-blocking, because that surface remains slower and is more environment-heavy than the backend unit-test lane.
The correct pattern is:

- keep `make test-unit` blocking for the backend unit-test inventory
- keep compile-all blocking
- keep stable repaired packages in `scripts/regression_gate.list`
- keep a live smoke layer for real process validation
- promote additional suites only after they become stable enough to be routine merge gates

To inspect the current test inventory instead of hard-coding counts:

```bash
find internal -name '*_test.go' | sort
(
  cd cli
  go test -tags acceptance ./acceptance -list '^Test'
  find acceptance/testdata -name '*.txtar'
)
```

## Phase 1: Package and Domain Tests

This is the highest-priority phase.
It gives the fastest feedback and protects the most critical business rules.

### Existing Building Blocks

The repo already has useful helpers such as:

- `internal/testharness/service_fixture.go:NewService` — the canonical
  service-layer fixture. Returns a bare `*service.Service` wired to an
  isolated TiDB playground database migrated via `db.Migrate` (production parity)
  and an isolated gitstore. Accepts `ServiceConfig` with knobs for
  connection caps and a custom `Embedder`.
- `internal/service/service_test.go:setupTestService` — 1-line wrapper
  over `testharness.NewService{}`, kept for call-site compatibility across
  hundreds of existing service-layer tests.
- `internal/service/service_test.go:setupRepoForTest`
- `internal/gitstore/store_test.go` temp-repo setup patterns

New tests should call `testharness.NewService` directly or go through
`setupTestService`; avoid re-rolling TiDB+migration bootstrap inline.

### First Batch

#### Git and Repository Foundation

Start with the parts that every PR, release, and workflow flow depends on:

- `internal/gitstore`: `Merge`, `Rebase`, `Compare`, `DiffNameStatus`, `DiffNumStat`, `ReadFile`, `ListTags`, `LogBetweenTags`, `PRCommitsLog`
- `internal/service/repo_test.go`: duplicate repo names, transfer, fork behavior, delete cascade, repo emptiness, disk usage

#### Pull Request Lifecycle

Add direct service and Git coverage for:

- create PR from valid branches
- reject invalid same-branch or invalid-state requests
- list PR commits and files
- request reviewers and prevent duplicate review requests
- merge with merge, squash, and rebase strategies
- conflict and already-merged paths

#### Issue Lifecycle

Add coverage for:

- create with labels, assignees, and milestone
- close and reopen behavior
- list and filter by state, label, and assignee
- search with multi-qualifier queries

#### Auth and OAuth

Add direct service tests for:

- dev-mode token behavior when the token table is empty
- valid and invalid token resolution
- device-code exchange paths
- user resolution by token
- generic OIDC discovery, device-code exchange, and local-user/token creation flows
- connected login code exchange, userinfo validation, local identity linking,
  and human versus agent user-kind mapping

#### Auth and Context-Scoped DB

Add direct package tests for:

- token resolution across missing, invalid, and valid token paths
- `service.DBForCtx(ctx)` request, transaction, and background propagation rules

#### Collaboration Governance and Authorization

Add direct service tests for:

- explicit org creation and org-owner bootstrap
- organization invitation create, accept, decline, revoke, and expiry paths with GitHub-compatible invitation roles (`direct_member`, `admin`)
- role-aware org membership and team membership behavior
- effective repository permission precedence across org base permission, direct collaborator grants, and team grants
- outside collaborator reconciliation after invite acceptance, collaborator removal, repo deletion, and repo transfer

#### Workflows and Releases

Protect the most common lifecycle paths:

- sync workflow definitions from repo contents
- dispatch, rerun, cancel, and list workflow runs
- create releases and upload or download assets

### Phase 1 Output

By the end of this phase, the service layer and Git layer should be trusted for the main CLI-facing behavior before any HTTP integration expansion.

## Phase 2: HTTP and Surface Integration Tests

This phase should use real service dependencies and exercise the real router and all primary HTTP-facing surfaces together:

- REST
- GraphQL
- OAuth
- Git Smart HTTP

### Why Real Integration First

REST handlers and auth middleware are currently wired to a concrete `*service.Service`.
That means mock-first handler tests are not the shortest or most honest path today.
Until REST and router are refactored to depend on injected interfaces, the recommended path is:

- isolated TiDB playground database
- temp gitstore
- real `service.Service`
- real `router.RegisterRoutes`
- `httptest` requests for REST, GraphQL, OAuth, and host-rewrite paths
- `httptest.NewServer` plus real `git` CLI calls for Git Smart HTTP paths

### Harness

The `internal/testharness` package provides a production-ready HTTP integration test harness. `testharness.New` builds on top of `testharness.NewService` (the shared service-layer fixture) and adds the full HTTP dispatch:

1. an isolated TiDB playground database migrated via `db.Migrate` (production parity)
2. a temp gitstore
3. a real `service.Service`
4. REST, GraphQL, Git HTTP, and OAuth handlers
5. the real router via `router.RegisterRoutes`

Service-only tests that do not need the HTTP surface should call
`testharness.NewService` directly.

The harness API is `testing.TB`-based rather than `*testing.T`-only, so the same wiring can be reused by package tests and benchmarks.
That allows hot-path benchmarks to exercise the real router with the same auth, DB, and gitstore setup used by the integration layer instead of maintaining a separate benchmark-only harness.

The harness supports both `httptest.NewRequest`-style tests and `httptest.NewServer` for real `git` CLI calls (clone, push, ls-remote) against an actual URL.
When a caller upgrades to `Server()`, cleanup is anchored to the root benchmark or test passed to `New()` so shared harness servers stay alive across sibling subtests or sub-benchmarks.

See `internal/testharness/smoke_test.go` for usage examples.

### First Integration Paths

Start with the highest-value flows for each primary surface.
Phase 2 is not complete until each surface has at least one core-path integration case.

#### REST

1. discovery endpoints with and without auth, explicitly covering `/api/v3/`, `/api/v3/meta`, and `/api/v3/rate_limit`
2. token auth failure and success paths
3. repository create and get
4. issue create, update, list, and comment
5. pull request create, view, list commits, list files, and merge
6. release create and asset upload or download
7. workflow dispatch and view
8. search basics
9. host rewrite behavior for `api.github.localhost`
10. explicit org creation and listing through `/api/v3/user/orgs`
11. organization invitation create/list/accept/decline/revoke flows, including pending-membership role rendering for `admin` invitations
12. outside collaborator listing and collaborator annotations on org-owned repos
13. team-repo permission alias compatibility, including canonical `read`/`write` decisions for `triage` and `maintain`
14. OIDC helper endpoints under `/api/v3/oidc/*`
15. connected login helper endpoints under `/auth/connected/*`
16. embedded identity routing through middleware into service DB access

#### GraphQL

Add integration tests for the GraphQL paths that are important and currently under-protected:

1. authenticated repository or issue query through `/api/graphql`
2. pull request query that verifies review or diff-adjacent fields
3. `revertPullRequest` mutation
4. one ProjectV2 lifecycle path such as `createProjectV2` or updating a project item field

These tests should hit the real GraphQL handler and field filtering path, not just resolver helpers.

#### OAuth

Add router-level integration tests for:

1. `POST /login/device/code`
2. `POST /login/oauth/access_token`
3. `GET /login/oauth/authorize` success and redirect validation failures

This ensures the device flow is covered at the integration layer rather than only through handler-local tests.

#### Git Smart HTTP

Add integration tests that use the real `git` CLI against a test server:

These tests should follow the real user path: discovery/auth setup first, then Git transport.

1. `info/refs` advertisement for an existing repository
2. clone or fetch via `git ls-remote` or `git clone`
3. push via `git push`, including post-push effects such as fixing `HEAD`, dispatching webhook deliveries, and syncing workflows

This is the integration layer for Git transport behavior; it should not be left exclusively to acceptance tests.

### Deferred Work

If we later refactor REST and router to consume narrow service interfaces, we can add smaller handler-unit tests on top.
That refactor is optional and should come after the real integration layer exists.

## Phase 3: Acceptance and End-to-End

The high-fidelity end-to-end layer is split across:

- `cli/acceptance/` for vendored gh CLI compatibility coverage
- `e2e/` shell flows for API and governance regressions that are easier to drive with `curl`, `git`, and `jq`

OIDC-specific end-to-end coverage should stay deterministic. Prefer the existing
mock-provider pattern and add provider-shaped discovery and ID token fixtures
under `e2e/cmd` rather than depending on a live third-party identity provider
in CI.

### Role of the End-to-End Layer

- verify CLI behavior against the running server
- protect cross-package flows that unit tests cannot express cleanly
- catch regressions in URL shape, auth behavior, host handling, and serialized responses
- keep organization-governance and collaboration-permission flows executable outside of ad hoc manual testing

### CI Coverage

For normal CI, run the full gh CLI compatibility inventory and a focused E2E subset around the major paths.

The acceptance lane should still prove the visible entry sequence around `gh`: `/api/v3/`, `/api/v3/meta`, `gh auth login --with-token`, then `gh auth setup-git`, while E2E scripts cover API flows that are easier to drive with `curl`, `git`, and `jq`.

- `TestAPI` with `basic-rest.txtar`
- `TestAPI` with `basic-graphql.txtar`
- `TestAuth` with `auth-setup-git.txtar`
- `TestRepo` with `repo-create-view.txtar`
- `TestRepo` with `repo-clone.txtar`
- `TestIssues` with `issue-create-basic.txtar`
- `TestPullRequests` with `pr-create-basic.txtar`
- `TestPullRequests` with `pr-merge-merge-strategy.txtar`
- `TestReleases` with `release-upload-download.txtar`
- `TestWorkflows` with `workflow-run.txtar`
- `TestSearches` with `search-issues.txtar`
- `TestSearches` with `search-code.txtar`
- `e2e/repo-rollback-compensation.sh`
- `e2e/push-postprocessing-consistency.sh`
- `e2e/oauth-device-flow.sh`

The current blocking merge gate includes:

- `make test-unit`
- `bash scripts/integration_tests.sh`
- `make test`
- `make test-e2e`
- `bash scripts/backend_smoke.sh`

Optional quick local sampling can still use `make test-e2e SCRIPT=...`, but the main gate now runs the full shell inventory.

### Full Compatibility Runs

Run the full acceptance suite in slower gates such as:

- nightly or scheduled validation
- pre-release validation
- large compatibility refactors
- changes touching router wiring, auth, serialization, or CLI patch behavior

## Recommended Execution Order

1. strengthen `gitstore` plus repo, PR, issue, and auth package tests
2. add workflow and release package tests for the main lifecycle paths
3. build a reusable HTTP integration harness on top of the real router
4. add API integration coverage for the main user paths
5. establish a small acceptance smoke gate
6. keep the full acceptance suite as the broader regression net

## Commands

Useful commands for this roadmap:

```bash
python3 scripts/doc_lint.py
bash scripts/check-module-contracts.sh
bash scripts/regression_gate.sh
bash scripts/integration_tests.sh
bash scripts/backend_smoke.sh
go test ./...
make test-unit
make test-integration
make test
make test-e2e
make test-e2e SCRIPT=org-collaboration-governance.sh
make test-run SUITE=TestPullRequests
make test-script SUITE=TestPullRequests SCRIPT=pr-create-basic.txtar
```

## Not the Current Focus

These are lower priority until the main path coverage exists:

- exhaustive handler-mock unit tests
- performance and load testing
- fuzzing every parser and endpoint
- full endpoint-by-endpoint API matrix coverage
