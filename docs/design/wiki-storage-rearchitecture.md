# Design: Wiki Storage Re-Architecture

Status: Approved direction, implementation planning in progress

This document turns the approved Wiki V2 direction from issue `#1488` into an
implementation plan for the repo. The target architecture baseline lives in
[`../architecture/wiki-storage-v2.md`](../architecture/wiki-storage-v2.md).
The current production implementation remains documented in
[`../architecture.md`](../architecture.md) and the component references under
[`../architecture/`](../architecture/).

## Summary

The repo has already approved the architectural direction: the sibling bare
wiki git repository becomes the only durable source of truth, while TiDB keeps
rebuildable derived indexes for listing, labels, backlinks, search, and
reconciler progress. In the target state, wiki label assignments must also come
from git-tracked wiki metadata rather than standalone relational writes so the
label index can be rebuilt from git alone.

What remains open is execution discipline. This document defines the delivery
slices, repo touch points, open decisions, and acceptance gates for landing the
rewrite without drifting away from the current service and REST contracts.

## Delivery Principles

- Keep the current wiki APIs stable until a cutover step explicitly changes a
  route contract.
- Treat git as the durable authority for page content and history as soon as
  the new path exists; do not add new catalog-authoritative features.
- Land small, reviewable slices that preserve the current test pyramid:
  package/service first, router integration second, acceptance/e2e last.
- Keep every TiDB wiki index rebuildable from git history and current trees.
- Define a git-tracked source for wiki labels before cutover; do not leave
  label assignments as standalone catalog-only state.
- Prefer explicit feature flags and provisional handlers over partial in-place
  rewrites of the current wiki service.

## Planned Delivery Slices

### Slice 0: Design and Contract Baseline

Goal: make the approved direction explicit in repo docs before implementation.

Expected changes:

- `docs/architecture/wiki-storage-v2.md` as the target architecture baseline.
- This implementation-plan document.
- Contract notes in `docs/architecture.md` and `docs/module-contracts.md` that
  explain which current wiki behaviors are transitional and which must survive
  cutover.

Acceptance:

- The target authority split is documented once and referenced consistently.
- No current component doc implies that the old catalog-first direction is the
  future implementation baseline.

### Slice 1: Storage and Reconciler Skeleton

Goal: create the new internal seams without cutting production traffic.

Expected code areas:

- New git-backed wiki package or subpackage for path mapping, write planning,
  ref-CAS, and reconciler contracts.
- New TiDB models/migrations for derived indexes and reconciler state.
- Worker loop or service entrypoints for index catch-up.

Acceptance:

- `db.Migrate` can create the new derived tables safely.
- Service/package tests cover path mapping, slug validation, ref-CAS retry, and
  reconciler idempotence.
- No existing `/wiki/*` route changes behavior yet.

### Slice 2: Provisional V2 Service and Routes

Goal: expose the new git-backed flow behind provisional handlers and feature
flags so it can be tested without replacing the current API surface.

Expected code areas:

- New service entrypoints for git-backed read/write/list/tree/history flows.
- Git-tracked label metadata support so label assignment writes become part of
  the durable wiki history before cutover.
- Provisional REST routes, for example `/wiki2/*` or gated `/wiki/*` variants.
- Focused integration tests through `internal/testharness`.

Acceptance:

- Git-backed CRUD, history, tree, labels, backlinks, and search integration
  tests pass under the provisional path.
- Current `/wiki/*` clients remain unaffected when the feature flag is off.
- Label assignment rebuilds from git-tracked metadata with no dependency on the
  legacy `wiki_page_labels` rows as a source of truth.

### Slice 3: Migration Tooling and Verification

Goal: make cutover operable and measurable before traffic moves.

Expected code areas:

- One-shot migration command for importing current catalog state into git and
  building derived indexes from git.
- Verification helpers that compare page content, flat list results, labels,
  backlinks, and search parity.
- Metrics and logs for reconciler lag, rebuild duration, and migration
  failures.

Acceptance:

- A wiki can be migrated and verified end-to-end in a test environment.
- Failures are observable and rollback steps are documented.

### Slice 4: Route Cutover

Goal: switch production wiki traffic to the git-backed path.

Expected code areas:

- Route wiring from the old handlers to the new service implementation.
- Removal of obsolete migration/projection logic from the hot path.
- Updated acceptance and e2e coverage for the final route contract.

Acceptance:

- Existing REST wiki workflows still pass unless a separately approved route
  contract change says otherwise.
- `go test ./...`, relevant router/service suites, and wiki e2e coverage pass.

### Slice 5: Cleanup

Goal: delete the old catalog-authority implementation after a verification
window.

Expected code areas:

- Remove superseded wiki catalog code and stale repair/materialization paths.
- Keep only the derived index schema and rebuild tooling that the new design
  still requires.

Acceptance:

- No dead wiki catalog-authority code remains in `internal/service` or
  `internal/db`.
- Docs describe the current implementation rather than the migration state.

## Repo Touch Points

The rewrite will span these primary areas:

- `internal/service/wiki*.go`: current wiki read/write/list/history/move/search
  logic and its eventual replacement.
- `internal/rest/handlers_wiki.go`: route contract, transport validation, and
  response-shape preservation or controlled redesign.
- `internal/router/router.go`: provisional routes, cutover wiring, and any new
  tree endpoints.
- `internal/db/models_wiki_*.go` and migration wiring in startup.
- `internal/testharness` plus `internal/rest/*wiki*` and
  `internal/service/*wiki*` tests.
- `docs/architecture*.md`, `docs/module-contracts.md`, and operations docs.

## Open Decisions Before Cutover

- Whether `wiki_page_history` is required for acceptable history endpoint
  latency or whether raw git history is sufficient.
- Whether migration preserves historical revisions or establishes a clean git
  history boundary at cutover.
- Which git-tracked metadata format becomes the durable source for wiki labels
  and how it remains compatible with the existing label REST contract.
- Whether direct pushes to the bare wiki repo are rejected outright or
  validated through hooks.
- Whether `/wiki/pages` and `/wiki/tree` keep the current compatible shapes or
  intentionally adopt a cleaner V2 contract in the same cutover.
- How read-your-writes behavior is guaranteed for endpoints that currently
  assume synchronous visibility.

## Required Verification

Every implementation slice that changes code must follow the repo self-review
standard and explicitly check:

- docs alignment against `docs/architecture.md`,
  `docs/module-contracts.md`, and `docs/test-strategy.md`
- invalid input, permission failure, not-found/conflict, and retry behavior
- targeted tests for the touched wiki packages plus `go test ./...`

Cutover-capable slices additionally require:

- production-like latency measurements for `git cat-file`, `git ls-tree`, and
  `git log -- <path>`
- verification that git-derived indexes can be rebuilt without data loss
- explicit acceptance and e2e coverage for CRUD, rename, prefix move, search,
  labels, backlinks, history, and compaction before cutover
- rollback steps and operator evidence documented in the same change set

## Exit Criteria

Issue `#1488` is complete only when all of the following are true:

- git is the only durable wiki content authority
- list/search/label/backlink/history acceleration data in TiDB is rebuildable
  from git
- current or intentionally redesigned REST contracts are documented and tested
- migration, rebuild, rollback, and lag-monitoring procedures exist in `docs/`
- obsolete catalog-authority code has been removed after the verification
  window
