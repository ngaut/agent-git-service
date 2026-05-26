# Wiki Storage V2

Status: Proposed

This document records the approved target architecture for the wiki storage
rewrite tracked by issue #1488. It is a design baseline for implementation
planning; the current production architecture remains documented in
[`../architecture.md`](../architecture.md).

## Summary

Wiki V2 makes the sibling bare `*.wiki.git` repository the only durable source
of truth for wiki page content, directory shape, commit history, rename
semantics, and compaction. TiDB remains in the architecture, but only for
rebuildable derived indexes such as page listings, labels, backlinks, search,
history acceleration, and reconciler progress.

The current catalog-first model stores authoritative wiki state in relational
tables and then projects that state back into git. Wiki V2 removes that
dual-authority boundary so page writes become real git commits and all git-like
wiki reads come directly from git.

## Goals

- Make git authoritative for wiki content, tree shape, commit history, and
  compaction.
- Keep TiDB indexes derivable from git so they can be dropped and rebuilt
  without data loss.
- Remove catalog-to-git materialization drift, synthetic commit reconciliation,
  and duplicate concurrency control paths.
- Preserve existing repo permission checks and endpoint auth rules.
- Define migration, verification, rollback, and observability before deleting
  catalog code.

## Non-Goals

- Supporting multi-region active-active wiki writes.
- Preserving catalog-internal page identities after cutover when they are not
  required by the public API.
- Solving wiki content quality problems from upstream content pipelines.
- Redesigning unrelated repository, issue, or pull-request storage flows.

## Authority Model

After cutover:

- Git owns wiki page bytes, path layout, commit history, rename behavior, and
  compacted history.
- TiDB owns rebuildable indexes used for list/search/filter/read-optimization
  paths.
- Service code owns orchestration, permission checks, ref-CAS retries,
  migration, and reconciliation scheduling.

No relational table remains authoritative for live wiki page content after the
rewrite.

## Storage Layout

- One bare git repo per wiki remains at `/data/repos/{owner}/{repo}.wiki.git`.
- Slugs map to repository paths with a stable translation rule:
  `slug = path without the .md suffix`.
- The canonical page path format is `path/to/page.md`.
- A page delete removes the file from `HEAD`; historical content remains in git
  history.

## Derived Indexes

The git repository is authoritative; TiDB indexes are derived projections. The
expected index families are:

- `wiki_page_index`: current live page rows keyed by `(repository_id, slug)`.
- `wiki_page_labels`: derived page labels for filtering.
- `wiki_backlinks`: resolved and dangling wiki links derived from page content.
- `wiki_page_fts`: full-text search rows built from page title/body.
- `wiki_page_history` if history endpoint latency requires acceleration.
- `wiki_index_state`: the last fully indexed commit and reconciler lease state.

All of these tables must be rebuildable from git history and current trees.

## Write Path

1. Validate repo permissions, slug/path rules, and request payloads.
2. Translate the requested page mutation into git index mutations.
3. Create one git commit for the logical wiki change.
4. Advance the wiki ref with single-writer/ref-CAS protection.
5. Enqueue or trigger reconciliation for the new commit.
6. Return success after git durability, optionally waiting for index catch-up on
   endpoints that require read-your-writes behavior.

Write correctness relies on git ref atomicity. The service layer may add a
process-local guard, but git ref CAS is the durable concurrency primitive across
multiple pods.

## Read Path

- Page content at `HEAD` or a specific commit comes from git objects.
- Tree listings come from `git ls-tree`.
- Page history comes from git history, optionally accelerated by a derived
  history index.
- Flat lists, label filters, backlinks, and search come from TiDB-derived
  indexes.

The key rule is simple: if a read is fundamentally about git content or history,
git is the authority; if it is an indexed query over current wiki metadata,
TiDB may answer it.

## Migration

The cutover is a deliberate migration, not a forever dual-write architecture.

1. Land design and route-contract updates.
2. Build the new git-backed wiki package, schema, and reconciler behind
   provisional handlers and feature flags.
3. Ship a one-shot migration tool that imports catalog state into git and builds
   indexes from git.
4. Verify content/list/search parity and production latency on real wiki repos.
5. Cut traffic to the new handlers.
6. Remove catalog-authority code only after a verification window and rollback
   plan are in place.

The migration must explicitly decide and document:

- whether pre-cutover history is replayed revision-by-revision or imported as a
  bounded history baseline,
- whether a dedicated `wiki_page_history` index is required,
- how direct git pushes are rejected or validated,
- how rename-history fidelity and soft-delete semantics change across cutover.

## Operational Requirements

- Measure `git cat-file`, `git ls-tree`, and `git log -- <path>` latency on the
  production wiki filesystem before cutover claims are accepted.
- Export reconciler lag metrics and alert when `indexed_commit_sha` falls behind.
- Keep an index rebuild procedure that can reconstruct TiDB wiki indexes from
  git without human data repair.
- Block or validate direct pushes to the bare wiki repo so API invariants cannot
  be bypassed.

## Testing Requirements

- Package tests for git-backed wiki write planning, ref-CAS retries, and
  reconciler idempotence.
- Router/service integration tests for wiki read/write/history/tree flows.
- Acceptance and e2e coverage for page CRUD, rename, prefix move, search,
  labels, backlinks, history, and compaction.
- Migration verification tests that compare imported git/index state with the
  legacy catalog before cutover.

## Open Decisions

- Whether history queries need a dedicated derived index table.
- Whether the migration preserves historical revisions or establishes a new git
  history boundary at cutover.
- How to model direct-git-write policy: hard reject, hook-based validation, or
  controlled ingestion.
- The exact rollback window before catalog tables and code are deleted.
