# Design: Wiki Storage Re-Architecture

Status: Draft

This RFC proposes replacing the wiki subsystem's git-as-source-of-truth
storage model with a relational catalog backed by a content-addressed blob
store, while preserving the current REST contract on the existing wiki
endpoints.

## 1. Summary

The wiki subsystem stores every page as a file in a sibling bare git repo
(`<owner>/<repo>.wiki`) and writes one commit per page. Reads reconstruct
page metadata from `git log` / `ls-tree` on every request; writes serialize
through a per-repo in-process mutex and fork ≥13 `git` subprocesses per
page. At 3,000 pages the navigation list takes ~55s and writes take
~1.5s/page with super-linear degradation. The architecture cannot reach the
project's 10⁹ pages target.

This RFC proposes treating the wiki as a **catalog-backed system with git
only at the protocol boundary**:

1. A relational catalog (`wiki_pages`, `wiki_page_revisions`,
   `wiki_changesets`, `wiki_dir_index`, `wiki_page_links`, `wiki_blob_refs`)
   becomes the source of truth.
2. Page bodies live in a content-addressed blob store (object storage)
   referenced by git's SHA-1 blob hash, preserving the existing REST
   `If-Match` ETag contract.
3. A single write primitive — `ApplyChangeSet` — replaces all current write
   paths and supports REST single-page, batch upsert, rename, prefix-move,
   and future `git push` ingestion.
4. The current per-repo `sync.Mutex` is removed in favor of SQL-level
   optimistic concurrency control (`wiki_changesets.parent_id` CAS).
5. Reading the page list becomes a single indexed SQL query, independent
   of git history length, while keeping current rename, prefix-move,
   rewrite-reporting, and label behaviors intact.
6. A `git clone` / `git push` protocol façade is **future, optional** work
   (deferred RFC) because no production client uses it today.

Expected impact: navigation list ~55s → <100ms P99, single-page PUT ~1.5s
→ <200ms P99, batch 1,000-page write ~25min → <5s, and write concurrency
unbounded per wiki.

## 2. Motivation

### 2.1 Observed performance

- 3,000-page wiki: navigation list 55s, page write 1.5s, degrades
  super-linearly.
- Code path responsible for read regression:
  `internal/service/wiki.go:735` `ListWikiPages` →
  `internal/gitstore/search.go:356` `LatestCommitsForPathsAtRef` with
  N-element pathspec walking N commits.
- Code path responsible for write regression:
  `internal/gitstore/content.go:586` `writeFile` (read-tree of N entries,
  write-tree of N entries, multiple `git` forks per write), serialized
  through `internal/gitstore/store.go:36-60` per-repo mutex.

### 2.2 Why incremental tuning is insufficient

- `git log -- <N paths>` is O(commits × pathspec). With one commit per
  page write, this is O(N²). No git flag avoids it; we cannot patch our
  way out.
- `read-tree` / `write-tree` scale linearly with tree size. Any per-page
  write must touch them.
- Per-repo write serialization is in-process and cannot scale
  horizontally.
- These are property-of-storage problems, not parameter-tuning problems.

### 2.3 Target scale

- Total pages across all wikis: 10⁹ order of magnitude.
- Per-wiki: 10¹–10⁴ typical, 10⁷ long tail.
- Per-page body: ~10 KB average, 1 MB P99, 25 MB hard cap.

## 3. Goals and Non-goals

### Goals

- Navigation list `GET /repos/{o}/{r}/wiki/pages`: < 100 ms P99
  independent of wiki size up to 10⁶ pages.
- Single-page PUT: < 200 ms P99 independent of wiki size.
- Batch 1,000 page upsert: < 5 s.
- Linear write throughput within a wiki (no per-repo lock bottleneck).
- REST API compatibility: every existing endpoint at
  `/api/v3/repos/{owner}/{repo}/wiki/...` keeps URL, JSON shape, status
  codes, rename/move side effects, and **blob-SHA-based `If-Match`
  semantics**.
- Preserve per-page version history, prefix collision detection,
  backlinks, labels, batch move semantics.

### Non-goals (this RFC)

- `git clone` / `git push` of `<repo>.wiki.git` — currently not exposed
  (see §10.A); deferred to a follow-up "Wiki Git Protocol Façade" RFC.
- Multi-region active-active wiki writes.
- Rich-text / non-markdown content.
- Branches, merges, force-push, submodules, LFS (none of which GitHub
  Wiki itself supports).
- Migrating to SHA-256 blob identifiers (kept on git's timeline).

## 4. Design Principles

1. **Catalog is the source of truth.** Bytes go to content-addressed
   storage; structure goes to SQL.
2. **Page identity is a stable `page_id`, not a slug.** Renames preserve
   identity, history continuity, and backlink anchoring.
3. **Single write primitive.** All REST endpoints, batch operations, and
   any future push ingestion call `ApplyChangeSet`. There is exactly one
   path from request to durable state.
4. **No request-path `fork+exec git`.** Git subprocess invocations are
   confined to migration tooling and the (future) protocol façade.
5. **Per-page version chains, not a global DAG.** Cross-page atomicity
   comes from a SQL transaction tagged with a `changeset_id`; a global
   linear "git log"-equivalent is derived from `wiki_changesets`, not
   stored as a DAG.
6. **ETag = git blob SHA-1.** Catalog computes the same hash git would
   compute, so existing REST clients continue to send and receive the
   same `If-Match` / `ETag` values.

## 5. Architecture Overview

```
                ┌────────────────────────────────────────────┐
   REST API     │ PutWikiPage / ListWikiPages / Move / ...   │
                └──────────────────────┬─────────────────────┘
                                       │
                                       ▼
                ┌────────────────────────────────────────────┐
   Service      │ WikiCatalog.ApplyChangeSet                 │
                │  (validate → CAS-upload blobs → SQL txn    │
                │   → outbox enqueue)                        │
                └─────┬───────────────────────┬──────────────┘
                      │                       │
            ┌─────────▼─────────┐  ┌──────────▼──────────────┐
   Storage  │ TiDB Catalog      │  │ Blob CAS (S3-compatible)│
            │ - wiki_pages      │  │ key = "blob/aa/bb/<sha>"│
            │ - revisions       │  │ git SHA-1 blob hashes   │
            │ - changesets      │  └─────────────────────────┘
            │ - dir_index       │
            │ - page_links      │
            │ - blob_refs       │
            └────────┬──────────┘
                     │ outbox / CDC
                     ▼
            ┌────────────────────┐  ┌────────────────────────┐
   Async    │ Search indexer    │  │ Backlink resolver       │
            │ (existing TiDB    │  │ (slug → page_id refill) │
            │  hybrid search)   │  └────────────────────────┘
            └────────────────────┘

   (Future RFC) git façade: lazy-materialized protocol-cache
   bare repo per wiki; serves git-upload-pack / git-receive-pack
   over the existing /info/refs route.
```

## 6. Data Model

All wiki tables are `PARTITION BY HASH(repo_id) PARTITIONS 256` unless
otherwise stated. Same-wiki rows live in a single partition; cross-wiki
workload spreads naturally.

### 6.1 `wiki_pages`

```sql
CREATE TABLE wiki_pages (
  page_id           BIGINT     NOT NULL,           -- snowflake
  repo_id           BIGINT     NOT NULL,
  slug              VARBINARY(1024) NOT NULL,      -- readable, case-preserving
  slug_ci_v1        VARBINARY(384)  NOT NULL,      -- canonical form (see 6.8)
  title             VARCHAR(1024),
  head_blob_sha     BINARY(20) NOT NULL,           -- git SHA-1 blob hash
  body_size         INT        NOT NULL,
  body_inline       VARBINARY(4096) NULL,          -- inline if body_size <= 4096
  head_revision_id  BIGINT     NOT NULL,
  head_changeset_id BIGINT     NOT NULL,           -- ETag basis
  last_author_id    BIGINT,
  created_at        DATETIME(6) NOT NULL,
  updated_at        DATETIME(6) NOT NULL,
  deleted_at        DATETIME(6) NULL,
  PRIMARY KEY (page_id),
  UNIQUE KEY uk_repo_slug   (repo_id, slug_ci_v1),
  KEY idx_repo_updated      (repo_id, updated_at DESC, page_id),
  KEY idx_repo_prefix       (repo_id, slug_ci_v1)
) PARTITION BY HASH(repo_id) PARTITIONS 256;
```

Why `page_id` PK and not `(repo_id, slug_ci_v1)`:

- 10⁹-row tables suffer with wide clustered keys.
- Renames preserve `page_id`; backlinks resolved to `page_id` stay stable
  across slug changes.
- Secondary indexes become narrower.

`body_inline` short-circuits the common case (page bodies < 4 KB), saving
an S3 round-trip on hot reads. Determined at write time.

### 6.2 `wiki_page_revisions`

```sql
CREATE TABLE wiki_page_revisions (
  page_id       BIGINT     NOT NULL,
  revision_id   BIGINT     NOT NULL,            -- monotonic per page
  changeset_id  BIGINT     NOT NULL,
  blob_sha      BINARY(20) NULL,                -- NULL for delete rows
  body_size     INT        NULL,
  body_inline   VARBINARY(4096) NULL,           -- inline if body_size <= 4096
  slug_at_rev   VARBINARY(1024) NOT NULL,
  commit_sha    BINARY(20) NOT NULL,
  op            ENUM('create','update','rename','delete','restore') NOT NULL,
  author_id     BIGINT,
  committed_at  DATETIME(6) NOT NULL,
  PRIMARY KEY (page_id, revision_id DESC),
  KEY idx_changeset (changeset_id),
  KEY idx_page_commit (page_id, commit_sha)
) PARTITION BY HASH(page_id) PARTITIONS 256;
```

Per-page descending PK makes "list history" / "get latest" a prefix scan.
`body_inline` preserves small historical revisions even after later
updates or deletes. `commit_sha` records the immutable commit identity
that current REST history rows and move responses already expose on
`main`, while `idx_page_commit` keeps `GetWikiPage?ref=<sha>` lookups
indexable. Cross-page grouping uses `idx_changeset`.

### 6.3 `wiki_changesets`, `wiki_repo_heads`

```sql
CREATE TABLE wiki_changesets (
  changeset_id     BIGINT     NOT NULL,
  repo_id          BIGINT     NOT NULL,
  parent_id        BIGINT     NULL,            -- ff-only chain head
  message          TEXT,
  author_id        BIGINT,
  committed_at     DATETIME(6) NOT NULL,
  page_count       INT        NOT NULL,
  source           ENUM('rest','batch','push','migration') NOT NULL,
  synth_commit_sha BINARY(20) NOT NULL,        -- immutable REST-visible commit id
  synth_format_ver SMALLINT   NULL,
  PRIMARY KEY (changeset_id),
  KEY idx_repo (repo_id, changeset_id DESC),
  KEY idx_parent (repo_id, parent_id)
) PARTITION BY HASH(repo_id) PARTITIONS 256;

CREATE TABLE wiki_repo_heads (
  repo_id            BIGINT PRIMARY KEY,
  head_changeset_id  BIGINT NOT NULL,
  updated_at         DATETIME(6) NOT NULL
);
```

`wiki_repo_heads` is the single row whose CAS update serializes concurrent
writers to the same wiki (replaces the in-process `sync.Mutex`).

### 6.4 `wiki_dir_index`

```sql
CREATE TABLE wiki_dir_index (
  repo_id    BIGINT NOT NULL,
  parent_dir VARBINARY(1024) NOT NULL,      -- "" = root
  child_name VARBINARY(255)  NOT NULL,
  child_kind ENUM('blob','tree') NOT NULL,
  page_id    BIGINT NULL,                   -- present when child_kind='blob'
  PRIMARY KEY (repo_id, parent_dir, child_name)
) PARTITION BY HASH(repo_id) PARTITIONS 256;
```

Maintained incrementally inside `ApplyChangeSet`. Supports:

- `ListWikiPages(path, recursive=false)`: one indexed range scan.
- Prefix collision: O(depth) lookups, no full-tree scan.
- Tree-object synthesis for the future façade.

`_sidebar` and any other top-level reserved slugs (see §6.8) are stored as
ordinary `blob` entries under `parent_dir=""`.

### 6.5 `wiki_page_links`

```sql
CREATE TABLE wiki_page_links (
  repo_id      BIGINT NOT NULL,
  src_page_id  BIGINT NOT NULL,
  dst_slug_ci  VARBINARY(384) NOT NULL,     -- canonical (slug_ci_v1)
  dst_page_id  BIGINT NULL,                 -- filled by async resolver
  PRIMARY KEY  (src_page_id, dst_slug_ci),
  KEY idx_dst_resolved (repo_id, dst_page_id),
  KEY idx_dst_string   (repo_id, dst_slug_ci)
);
```

Write path: delete-all-and-insert per `src_page_id` inside the same txn.
Rename only mutates `wiki_pages.slug_ci_v1`; `wiki_page_links` does not
need rewriting because `idx_dst_resolved` is anchored on `dst_page_id`.

### 6.6 `wiki_blob_refs`, `wiki_pending_blobs`

```sql
CREATE TABLE wiki_blob_refs (
  blob_sha    BINARY(20) PRIMARY KEY,
  refcount    BIGINT NOT NULL,
  size        INT NOT NULL,
  first_seen  DATETIME(6),
  last_seen   DATETIME(6)
);

CREATE TABLE wiki_pending_blobs (
  blob_sha    BINARY(20) PRIMARY KEY,
  written_at  DATETIME(6) NOT NULL,
  size        INT NOT NULL
);
```

Reference-count-based garbage collection. `wiki_pending_blobs` holds the
WAL row asserted before blob upload and deleted inside the txn that takes
the first reference. GC: any row in `wiki_pending_blobs` older than 1h
with no matching `wiki_blob_refs` row → physical delete. Object-storage
lifecycle rules act as a belt-and-suspenders backup.

### 6.7 `wiki_slug_aliases` (internal migration aid)

```sql
CREATE TABLE wiki_slug_aliases (
  repo_id      BIGINT NOT NULL,
  old_slug_ci  VARBINARY(384) NOT NULL,
  page_id      BIGINT NOT NULL,
  created_at   DATETIME(6) NOT NULL,
  expires_at   DATETIME(6) NOT NULL,
  PRIMARY KEY (repo_id, old_slug_ci)
);
```

When a page is renamed, the prior `slug_ci_v1` may be inserted with a
TTL as an **internal migration aid only**. The current REST contract does
not expose renamed slugs as redirects or alias hits, so request-path
lookup continues to behave exactly as it does on `main`: callers must use
the new slug returned by the move endpoint. If alias storage is kept, it
is for rollback tooling, auditability, and future product discussion, not
for changing `GetWikiPage` semantics in this RFC.

### 6.8 Slug canonicalization (`slug_ci_v1`)

The canonical-form function is **frozen as v1** and corresponds to the
current `canonicalWikiLookupSlug` (`internal/service/wiki.go:321-337`):

```
slug_ci_v1(s) =
   1. split on '/'
   2. for each segment:
        trim spaces
        replace '_' → '-'
        collapse internal whitespace → '-'
        lowercase
   3. rejoin with '/'
   4. validate against validateReadableWikiSlug; reject if invalid
```

Reserved tokens (`_sidebar`, `.`, `..`) keep current handling: `_sidebar`
is allowed as a literal segment, dot segments rejected. A golden-test
suite locks input→output pairs; any future change requires introducing
`slug_ci_v2` and dual-maintaining columns during migration.

Write-time validation remains a separate concern from lookup
canonicalization. This RFC preserves the current split behavior: request
lookups canonicalize via `canonicalWikiLookupSlug`, while create/update
paths still run the stricter readable-slug validators before any catalog
write.

### 6.9 Label compatibility

Current wiki labels are part of the public API surface, so the catalog
design must preserve them during cutover rather than treating them as an
adjacent concern. The implementation plan is:

1. Keep the existing label endpoints and response fields unchanged.
2. Continue serving label-filtered list operations during M3+.
3. During rename and prefix-move operations, remap label ownership from
   old slug to new slug in the same transaction that applies the page
   move, matching today's `moveWikiPageLabels` behavior.
4. During dual-write phases, continuously audit that label reads from the
   catalog-backed path match label reads from the legacy git-backed path.

The RFC intentionally does not redesign labels around `page_id` in this
document. Whether label storage remains slug-keyed or gains an internal
`page_id` mapping is an implementation detail, but externally visible
behavior must stay compatible with `main`.

## 7. Blob CAS

- Object store: S3 / S3-compatible. Key layout: `blob/aa/bb/<full-sha1>`
  (2×2 hex prefix shards).
- Content stored zstd-compressed (`Content-Encoding: zstd`); compression
  performed by the writer.
- Identifier: **git blob SHA-1** of the raw content
  (`sha1("blob " + len + "\0" + content)`). This matches what
  `git hash-object` would compute and preserves wire compatibility with
  the existing REST `If-Match` value.
- Reference counting in `wiki_blob_refs` (§6.6) gives cross-wiki dedup
  for free (identical bodies share blobs).
- Bodies ≤ 4 KB inline into both `wiki_pages.body_inline` and
  `wiki_page_revisions.body_inline`; they may skip S3 storage because
  every historical revision still has a durable in-catalog copy.

Implementation note: the SHA-1 blob hash can be computed in-process (no
`git hash-object` fork) using `crypto/sha1` with the standard git blob
framing.

## 8. The `ApplyChangeSet` Primitive

Single write entry point. REST handlers, batch APIs, and (eventual) push
ingestion all funnel through it.

### 8.1 Interface

```go
type Op uint8
const (
  OpUpsert Op = iota
  OpRename
  OpDelete
  OpRestore
)

type Change struct {
  Op       Op
  Slug     string           // src slug
  NewSlug  string           // OpRename only
  Body     []byte           // OpUpsert only
  IfMatch  string           // optional blob SHA hex (per-page CAS)
}

type ChangeSetRequest struct {
  RepoID         uint64
  Author         UserRef
  Message        string
  ExpectedParent *uint64    // optional; ff-only if set
  IdempotencyKey *string    // optional
  Source         Source     // rest|batch|push|migration
  Changes        []Change
}

type ChangeSetResult struct {
  ChangesetID uint64
  Parent      uint64
  PerChange   []PerChangeResult   // new blob_sha, revision_id, etag, status
}
```

### 8.2 Execution

1. **Validate & canonicalize.** Per `Change`, derive `slug_ci_v1`,
   `new_slug_ci_v1`. Reject duplicates within the changeset. Validate
   slug grammar.
2. **Quota gate.** See §11.
3. **Pre-read.** One indexed
   `SELECT page_id, slug_ci_v1, head_blob_sha, head_changeset_id,
   deleted_at FROM wiki_pages WHERE repo_id = ? AND slug_ci_v1 IN (...)`
   covering every touched slug (sources + rename destinations).
4. **In-memory conflict checks** against pre-read:
   - `IfMatch` mismatch → 409 with current SHA.
   - Prefix collision via `wiki_dir_index` range query → 409.
   - Rename destination occupied → 409.
   - Delete on missing → 404.
   - Move and prefix-move plan generation must also precompute the
     current rewrite set and per-page skips so the response contract
     matches `main`.
5. **Blob uploads.** For each upsert with non-inline body:
   - Compute SHA-1 in-process.
   - If `wiki_blob_refs` already contains the SHA (dedup hit), skip
     upload.
   - Otherwise: `INSERT wiki_pending_blobs`, upload to S3 (parallel).
     Failures here roll back the whole changeset before touching SQL.
6. **SQL transaction:**

   ```
   BEGIN;
     INSERT wiki_changesets (parent_id = ExpectedParent or current head, ...);
     UPDATE wiki_repo_heads
        SET head_changeset_id = new
        WHERE repo_id = ? AND head_changeset_id = parent;   -- CAS
     -- if rowcount=0 → ROLLBACK, retry with refreshed parent or fail
     derive immutable `commit_sha` for the new changeset before writing
     revision rows or response payloads
     For each change:
       allocate page_id if create
       INSERT wiki_page_revisions
       UPSERT wiki_pages   (slug, slug_ci_v1, head_blob_sha, head_*_id, updated_at)
       UPDATE wiki_dir_index (add/remove leaves; materialize intermediate dirs)
       DELETE wiki_page_links WHERE src_page_id=?; INSERT new out-links
       For rename/prefix-move:
         rewrite inbound wiki links in affected pages using the same
         readable-slug rules as `main`
         collect `rewrites` and `skipped` result payloads
         move label ownership from old slug to new slug
       INSERT wiki_blob_refs ON DUPLICATE KEY UPDATE refcount=refcount+1
       UPDATE wiki_blob_refs SET refcount=refcount-1 WHERE blob_sha=old_blob
       DELETE wiki_pending_blobs WHERE blob_sha=new_blob
       optional internal alias rows may be written only for rollback or
       audit tooling; request-path reads do not consult them
     INSERT wiki_outbox (changeset_id, repo_id)
   COMMIT;
   ```

7. **Post-commit async (best-effort):**
   - Outbox consumer → search reindex (existing
     `queueWikiSearchUpsert` path).
   - Backlink resolver → fill `dst_page_id` where it can.
   - Optional: invalidate any per-repo cache.

### 8.3 Rename and prefix-move contract

The move endpoints keep their current observable behavior:

- The renamed page is returned under the destination slug.
- Inbound wiki references in other pages are rewritten when they can be
  updated safely using the same rewrite rules as `main`.
- The response still includes `rewrites` and `skipped`, with `skipped`
  sorted deterministically.
- Labels attached to moved pages remain attached after the move.

The catalog model changes *how* the service persists those effects, not
*whether* clients observe them.

### 8.4 Concurrency

The in-process `repoLock` (`internal/gitstore/store.go:36-60`) is removed
for wiki writes. Serialization is purely SQL-level on the
`wiki_repo_heads` row. On CAS failure the service retries from a fresh
planning snapshot: reload touched pages plus candidate rewrite targets,
recompute prefix-collision checks, recompute `rewrites` and `skipped`,
and then re-validate `IfMatch` on each change. Bounded retry (default 5).

### 8.5 Cost budget

| Step | Single page | 1,000 pages |
|---|---|---|
| Validate + folding | < 1 ms | < 50 ms |
| Pre-read SELECT | ~5 ms | ~15 ms |
| Blob upload (parallel) | ~10 ms | ~200 ms |
| SQL txn (incl. outbox) | ~10–15 ms | ~300–500 ms |
| **Total** | **~25 ms** | **~1 s** |

## 9. Read Paths

| Operation | Implementation | Complexity |
|---|---|---|
| `ListWikiPages` | `wiki_pages` ∪ `wiki_dir_index` range scan; label join | O(returned rows) |
| `GetWikiPage` (HEAD) | `wiki_pages` PK; inline body or 1 S3 GET | O(1) |
| `GetWikiPage` @rev | `wiki_page_revisions` via `(page_id, commit_sha)`; inline body or 1 S3 GET | O(1) |
| `ListWikiPageHistory` | `wiki_page_revisions(page_id, ...)` range | O(returned rows) |
| `ListWikiBacklinks` | `wiki_page_links` via `idx_dst_resolved` | O(returned rows) |
| Search | Existing `wiki_search_documents` (no change) | unchanged |
| Wiki "git log" view | `wiki_changesets(repo_id, ...)` range | O(returned rows) |
| Prefix collision check | `wiki_dir_index` range | O(depth) |

ETag = hex(`wiki_pages.head_blob_sha`) for page reads; `If-None-Match`
short-circuits at the catalog lookup. `head_changeset_id` remains an
internal change detector; this RFC does not claim new collection-level
HTTP ETags on routes that do not emit them today.

Renamed slugs do not gain new redirect or alias-read semantics in this
RFC. Compatibility means the post-move read contract stays aligned with
`main`, while the move endpoints continue to surface the destination slug
and rewrite results explicitly.

No request path forks `git`.

## 10. Open Architectural Decisions

### 10.A Git protocol façade

Out of scope for this RFC; deferred to "Wiki Git Protocol Façade" RFC.
Verified that today the route `/{owner}/{repo}.git/info/refs`
(`internal/router/router.go:212`) handles a hypothetical
`clone owner/repo.wiki.git`, but `internal/githttp/handler.go:100`
requires a `db.Repository` row that wiki repos don't have. This scope
decision is based on source inspection, not production telemetry; if
later evidence shows active clients on that path, the deferred façade RFC
must move ahead of read/write cutover.

When that follow-up RFC lands, the design will:

- Materialize a per-wiki bare repo lazily as a protocol cache (not SOT).
- Reuse `git-upload-pack` / `git-receive-pack` rather than re-implementing
  the wire protocol.
- Route push ingestion through `ApplyChangeSet` (parse pack →
  `[]Change`).
- Use immutable per-changeset `synth_commit_sha` once a clone observes it.

### 10.B SHA-1 vs SHA-256

This RFC stays on SHA-1 blob hashes to preserve REST `If-Match`
compatibility (`internal/service/wiki.go:1230`). Migration to SHA-256
follows git's own timeline and would be a separate RFC.

### 10.C Soft-delete vs hard-delete

`wiki_pages.deleted_at` retains the row; revision history retained.
`wiki_blob_refs` decrements on delete; blobs GC normally. Default UI/API
behavior excludes deleted pages. Hard-delete is an admin operation that
purges revisions and aliases as well.

## 11. Quotas, ACLs, Abuse

| Limit | Default | Enforcement point |
|---|---|---|
| `MAX_BLOB_BYTES` | 25 MB | step 5 (blob upload) |
| `MAX_BODY_INLINE_BYTES` | 4 KB | step 5 |
| `MAX_CHANGES_PER_CHANGESET` | 10,000 | step 2 |
| `MAX_BYTES_PER_CHANGESET` | 200 MB | step 2 |
| `MAX_PAGES_PER_WIKI` (soft) | 10⁷ | nightly job + ingress warn |
| `MAX_OUTLINKS_PER_PAGE` | 5,000 | step 1 (markdown parse) |
| `WIKI_WRITE_QPS_PER_REPO` | 100/s | service-layer token bucket |

All operations require resolution through `repo_id` and the existing repo
permission check (`service.HasRepoAccess`); the catalog never bypasses it.
The current `getRepoBase` / `requireRepoPermission` gating in the service
layer stays unchanged in shape, only its body changes from "ensure git
repo exists" to "ensure wiki catalog row exists."

Pack-parsing safety lives in the future façade RFC.

## 12. Storage Capacity

| Table | Row count (10⁹ pages) | Approx. size incl. indexes |
|---|---|---|
| `wiki_pages` | 1 × 10⁹ | 250–400 GB |
| `wiki_page_revisions` | ~10 × 10⁹ | ~1.2 TB |
| `wiki_dir_index` | ≤ 1.5 × 10⁹ | 150–250 GB |
| `wiki_page_links` | ~10 × 10⁹ | ~500 GB |
| `wiki_blob_refs` | 0.5–0.8 × 10⁹ (post-dedup) | < 100 GB |
| Blob bytes (S3, zstd) | — | ~3 PB |

Catalog total dominated by `wiki_page_revisions`; partition pruning by
`page_id` keeps individual partitions on the order of 5 × 10⁷ rows. Blob
storage is the dominant cost regardless of architecture.

## 13. Migration Plan

Each phase is independently shippable, independently revertible via a
per-repo feature flag, and gated on SLO + correctness metrics before
promotion.

| Phase | Change | Revert mechanism | Validation gate |
|---|---|---|---|
| M0 | DDL: create all tables. Freeze `slug_ci_v1` function + golden test (`internal/service/wiki.go:321` baseline). No traffic changes. | DROP tables | DDL applied to all environments; golden tests green |
| M1 | Dual-write: after each existing wiki git write, async upsert catalog (best-effort, alarms only), including immutable revision rows and commit identities for new traffic. | Flip flag | < 0.01% catalog/git drift over 24h on shadow audit job |
| M2 | One-shot backfill: per-repo job replays full git history, not just `HEAD`, into `wiki_changesets` and `wiki_page_revisions`, then rebuilds current-page state. Resumable, rate-limited. | Drop partitions | Per-repo current rows match git tree; historical commit count and sampled `?ref=` reads match git for pre-cutover content |
| M3 | Switch reads to catalog (`ListWikiPages`, `GetWikiPage`, `GetWikiPage?ref=`, history, search, labels, backlinks, move/prefix-move parity checks, prefix-collision), per-repo flag. Shadow read for first 10% repos, compare responses byte-by-byte (timestamps normalized). | Flag flip per repo | List P99 < 100 ms; diff rate < 0.001% |
| M4 | Switch writes to `ApplyChangeSet` (still dual-writing the legacy git repo). | Flag flip | PUT P99 < 200 ms; no missing data on audit; move/search/label parity checks stay green |
| M5 | Stop dual-writing the legacy git repo. Wiki bare repos move to a "frozen" state, retained for one quarter for forensics. | Re-enable dual-write (requires re-sync) | One quarter of clean operation, no rollback events |
| M6 | Decommission legacy bare repos. | — | — |

Two safety properties hold across all phases:

1. **`head_blob_sha` byte-equality**: catalog and (during dual-write) git
   always agree on the SHA-1 of each page's body. The audit job compares
   them continuously during M1–M4.
2. **REST API contract is invariant**: every URL/JSON shape/status
   code/`If-Match` header semantics works identically before and after
   each phase, including `GET ?ref=<sha>`, history commit SHAs, search,
   labels, single-page move, and bulk prefix-move responses. Existing
   acceptance tests in `cli/acceptance/` and `e2e/` must continue to
   pass.

## 14. Rollback Strategy

- M0–M2 are additive; revert by dropping tables / flipping flags.
- M3 read-side rollback: per-repo flag flip restores git-backed reads in
  seconds.
- M4 write-side rollback: per-repo flag flip restores git-backed writes;
  catalog writes continue in dual-write mode and may be replayed back to
  git via a one-time tooling script (drift-since-cutover bounded by
  changesets timestamped after the flag flip).
- M5 is the irreversible point. Decision gate: at least one quarter of
  stable operation in M4 with no rollback events, plus migration team
  approval.

## 15. Observability and SLOs

Service-level objectives (per-repo P99):

- `ListWikiPages` < 100 ms (independent of page count up to 10⁶).
- `GetWikiPage` < 50 ms.
- `PutWikiPage` < 200 ms.
- Batch 1,000-page `ApplyChangeSet` < 5 s.
- Catalog/git drift during M1–M4: < 0.01% pages with mismatched
  `head_blob_sha`.

Required metrics (Prometheus / TiDB dashboards):

- `wiki_apply_changeset_latency_seconds{op, source}`
- `wiki_apply_changeset_cas_retries_total`
- `wiki_blob_upload_latency_seconds{outcome}`
- `wiki_dual_write_drift_total`
- `wiki_pages_per_repo` (distribution; alert on growth approaching 10⁷)
- `wiki_blob_refs_orphan_total` (GC backlog)
- `wiki_outbox_lag_seconds`

Required logs (slog, structured):

- Every `ApplyChangeSet` invocation: `changeset_id`, `repo_id`,
  `page_count`, `source`, latency, CAS retries.
- Every blob upload failure with sha and size.
- Every prefix-collision rejection (to detect bad client patterns).

## 16. Testing Strategy

Per `docs/test-strategy.md`, with these specifics:

1. **`slug_ci_v1` golden tests.** Pure-function inputs/outputs; runs in
   every CI. Locks behavior forever.
2. **Catalog write semantics**: per-package tests on
   `WikiCatalog.ApplyChangeSet` for each conflict case (`IfMatch`
   mismatch, prefix collision, rename dest taken, delete missing) and OCC
   retry behavior under concurrent CAS losers.
3. **Cross-store invariant tests** (M1–M4 phase): a test harness that
   performs random wiki operations against both stores and asserts
   `head_blob_sha` agreement after each.
4. **Performance regression tests**: a synthetic 10⁴-page wiki where
   `ListWikiPages` must complete in < 50 ms; failure breaks CI.
5. **REST acceptance** (`cli/acceptance/`): all existing wiki cases run
   untouched; any diff in headers/status/payload is a blocker.
   Explicit parity coverage must include `GetWikiPage?ref=<sha>`,
   `ListWikiPageHistory`, wiki search, wiki labels, `MoveWikiPage`, and
   `MoveWikiPagePrefix` response fields (`moved`, `rewrites`, `skipped`,
   `commit`).
6. **E2E** (`e2e/*.sh`): add `wiki-batch-upsert.sh` and
   `wiki-rename-prefix-large.sh`.
7. **Chaos**: kill the service between blob upload and SQL commit;
   verify no dangling pages and GC reclaims the orphan blob within the
   configured TTL.

## 17. Alternatives Considered

**A. Keep git as SOT, add only a denormalized catalog projection.**
Rejected. The page-write fork count and `read-tree` / `write-tree` cost
remain; only reads improve. Cannot meet the write SLO at 10⁶+ pages.

**B. Replace per-repo mutex with a sharded lock; keep git writes.**
Rejected. Per-write fork count and tree-IO costs dominate; lock removal
does not change them.

**C. Migrate to a different git implementation (libgit2 / go-git
in-process).** Rejected as primary solution. Removes fork overhead
(~30%) but not the algorithmic O(N) per write and O(N²) for the metadata
join. Useful inside the future façade but not on the request path.

**D. Move only blobs to object storage, keep git tree as catalog.**
Rejected. The tree itself is the slow part. Decoupling blobs without
decoupling the tree saves IO but not CPU.

**E. SQLite-per-wiki instead of TiDB.** Rejected. Operational complexity
at 10⁹ pages across many tenants; cross-wiki search and dedup become
hard.

The chosen approach (SOT = TiDB catalog, blobs = object CAS, optional git
façade) is the only design surveyed that satisfies all four scaling axes
(per-wiki size, per-wiki write QPS, total system size, REST/Git
compatibility).

## 18. Open Questions

1. **Object storage choice** (S3 vs MinIO vs internal blob service): not
   blocking the RFC; resolved during M0 implementation.
2. **`MAX_PAGES_PER_WIKI`** soft limit value (currently proposed 10⁷):
   needs product input before M3 ramp.
3. **Alias retention TTL**: if internal alias rows are kept for rollback
   or audit, 90 days is proposed; product-facing redirect behavior is out
   of scope for this RFC.
4. **`Idempotency-Key` header**: introduce now or in a later iteration?
   RFC assumes optional; safe to defer.
5. **Cross-wiki link semantics**: currently links are intra-wiki only; no
   schema change here, but future extension would add `dst_repo_id` to
   `wiki_page_links`.

## 19. Code References (verification trail)

The verification round that informed this RFC inspected the following
code locations. Reviewers can audit each claim against the cited line.

| Claim | File:Line |
|---|---|
| 1 commit per page, ≥13 git forks per PUT | `internal/gitstore/content.go:586` `writeFile` |
| `ListWikiPages` triple-walks the tree | `internal/service/wiki.go:735` |
| `LatestCommitsForPathsAtRef` is O(commits × paths) | `internal/gitstore/search.go:356` |
| Full-tree scan on every write for prefix collision | `internal/service/wiki.go:1712` |
| Per-repo `sync.Mutex` serializes writes | `internal/gitstore/store.go:36-60` |
| Wiki repos are sibling bare repos, not DB rows | `internal/service/wiki.go:200, 725-730`; verified absence in `internal/githttp/handler.go:100` |
| Search/backlinks/labels already have non-git persistence pieces today | `internal/service/wiki_search.go:527` (goroutine); `internal/service/wiki.go:460` (in-memory cache); `internal/db/models_wiki_label.go` (DB table) |
| `If-Match` checks against blob SHA (not commit SHA) | `internal/service/wiki.go:1230` |
| Write-time readable-slug validation and lookup canonicalization are separate behaviors | `internal/service/wiki.go:205-337`, `wiki.go:26-30` |
| Move/prefix-move rewrites and label remap are part of today's contract | `internal/service/wiki.go:1344-1455`, `internal/service/wiki.go:1490-1708` |
| Canonical lookup function (`slug_ci_v1` baseline) | `internal/service/wiki.go:321-337` |
| Existing multi-mutation primitive available for transitional reuse | `internal/gitstore/commit_files.go:19` |

## 20. Out-of-scope Adjacent Work

- Wiki Git Protocol Façade — separate RFC.
- Multi-region replication of wiki catalog — separate RFC.
- Wiki content security policy / sanitization improvements — separate
  RFC.
- Migration tooling productization (UX, dashboards, throttling) —
  engineering work item, not part of this design.

## 21. Acceptance Checklist for This RFC

- [ ] DDL reviewed by DBA / TiDB team for partition layout and online-DDL
      feasibility.
- [ ] REST contract diff confirmed empty by API owners.
- [ ] Slug canonicalization golden tests landed before M0.
- [ ] Per-phase SLO dashboards exist and are alarmed before M1.
- [ ] Capacity model reviewed against current TiDB cluster headroom.
- [ ] Migration rollback rehearsal performed in staging at the end of
      each phase.
