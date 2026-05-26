# Wiki Storage V2 Cutover Checklist

Status: Draft

This runbook defines the operator evidence required before the Wiki V2 route
cutover from issue #1488. It complements the target design in
[`../architecture/wiki-storage-v2.md`](../architecture/wiki-storage-v2.md) and
the implementation plan in
[`../design/wiki-storage-rearchitecture.md`](../design/wiki-storage-rearchitecture.md).

## Preconditions

- The git-backed wiki implementation and derived-index reconciler have already
  landed behind a feature flag or provisional route.
- Migration tooling can import a wiki from the legacy catalog state into git and
  rebuild all required derived indexes.
- Router/service/integration tests for wiki CRUD, history, tree, labels,
  backlinks, search, rename, and prefix move are green.
- Acceptance coverage (`make test` or an equivalent acceptance suite) is green
  for wiki CRUD, rename, prefix move, search, labels, backlinks, history, and
  compaction behaviors exposed through the GitHub-compatible surfaces.
- End-to-end coverage (`make test-e2e` or an equivalent focused e2e suite) is
  green for the same cutover-critical wiki flows.

## Pre-Cutover Evidence

1. Measure production-like latency for:
   - `git cat-file`
   - `git ls-tree`
   - `git log -- <path>`
2. Record the exact automated test evidence that gates cutover:
   - package/service/router test commands
   - acceptance command and result summary
   - e2e command and result summary
3. Run migration verification on at least one representative wiki:
   - current page content parity
   - page list parity
   - label parity
   - backlink parity
   - search parity
4. Confirm index rebuild from git completes successfully and document:
   - total duration
   - failure handling
   - resulting `indexed_commit_sha`
5. Confirm reconciler lag metrics and alerts exist for:
   - indexed commit lag
   - failed reconciliation attempts
   - rebuild failures
6. Confirm direct git write policy is enforced:
   - rejected at transport boundary, or
   - validated through an approved hook path

## Cutover Steps

1. Freeze the planned cutover window and identify the rollback owner.
2. Run the migration tool for the target wiki set.
3. Verify parity results and reconciler catch-up before route changes.
4. Flip the feature flag or route wiring to the git-backed wiki path.
5. Run focused smoke checks against:
   - page read at `HEAD`
   - page read at explicit `ref`
   - page list
   - tree endpoint
   - page history
   - search
   - labels
   - single-page rename
   - prefix move
6. Record the acceptance and e2e evidence bundle with the cutover ticket so the
   verification window has a fixed baseline.
7. Monitor reconciler lag and error logs during the verification window.

## Rollback Conditions

Rollback immediately if any of the following occurs:

- page content differs from pre-cutover expectations
- history or tree reads fail for existing pages
- reconciler lag exceeds the accepted threshold without recovery
- label/backlink/search parity fails in a user-visible way
- direct git writes can bypass the intended validation path

## Rollback Steps

1. Flip route wiring or the feature flag back to the previous wiki path.
2. Preserve the migrated git and derived-index state for debugging.
3. Capture the failing repo, commit, and reconciler state in the incident log.
4. Do not delete catalog-authority code or tables until the failure is
   understood and replay-tested.

## Post-Cutover Exit

The old catalog-authority path can only be deleted after:

- the verification window completes without rollback
- index rebuild drills succeed from git
- operator documentation is updated to the now-current architecture
