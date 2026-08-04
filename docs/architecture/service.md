# Service Layer — Component Reference

## Purpose

`internal/service` is the business-logic and persistence-orchestration layer.
It owns all domain rules, coordinates GORM-backed relational state with Git-backed repository state through `gitstore`, and translates persistence errors into stable sentinel errors for surfaces to map.

Surface packages (REST, GraphQL, OAuth, Git HTTP) call service methods and receive Go values and errors, never HTTP responses.

For the full system overview see [docs/architecture.md](../architecture.md).
Core invariant: `agent-git-service` is Git-backed; see [architecture.md § Purpose](../architecture.md#purpose).

## Scope

Owns:

- all business rules and domain validation
- GORM persistence reads and writes
- Git orchestration through `gitstore`
- cross-entity lifecycle changes (e.g., PR merge updates both Git history and DB state)
- domain side effects such as workflow sync and embedding follow-up
- sentinel error definitions consumed by surface layers

Does not own:

- HTTP routing, request decoding, or response formatting (belongs to surfaces)
- schema definition and migrations (belongs to `db`)
- raw Git operations (belongs to `gitstore`)

## Key Entry Points

### Service Struct

Defined in `repo.go`. Fields:

| Field | Type | Role |
|---|---|---|
| `DB` | `*gorm.DB` | Primary database connection (context-aware) |
| `Git` | `*gitstore.Store` | Git repository storage layer |
| `BaseURL` | `string` | HTTP base URL for generated links |
| `Embedder` | `embedding.Embedder` | Optional semantic search provider |
| `AllowAnyToken` | `bool` | Dev convenience: accept any token when no tokens exist in DB |

### Service Contract Shape

There is no authoritative `internal/service/iface.go` catalog in the current tree.
Production wiring passes the concrete `*Service` to REST, GraphQL, OAuth, and Git HTTP.
Narrower interfaces can still be introduced later where they remove real test or coupling pain, but the current contract source is the concrete service methods and the domain files listed below.

Milestone vs label semantics:
- Use a milestone for the single primary category/topic on an issue or conversation channel.
- Use labels for cross-cutting attributes and tags that can be multi-valued.

### Implementation Files by Domain

| Area | Files |
|---|---|
| Repository lifecycle | `repo.go`, `repo_fork.go`, `repo_query.go`, `branch.go` |
| Repository authorization | `permission.go`, `repo_access.go` |
| Issues | `issue.go` |
| Pull requests | `pr.go`, `pr_merge.go` |
| Comments and timeline | `comment.go`, `timeline.go`, `review_comment.go` |
| Labels and milestones | `label.go`, `milestone.go` |
| Reviews | `review.go` |
| Reactions | `reaction.go` |
| Search | `search.go` |
| Releases | `release.go` |
| Workflows and actions | `workflow.go`, `workflow_dispatch.go`, `workflow_exec.go` |
| Users, orgs, teams, and collaboration governance | `user.go`, `team.go`, `org_membership.go`, `org_invitation.go`, `outside_collaborator.go` |
| Auth and keys | `auth.go`, `keys.go` |
| Projects | `project.go` |
| Wiki | `wiki.go`, `wiki_label.go`, `wiki_search.go`, `wiki_rewrite.go` |
| Other | `gist.go`, `star.go`, `dependabot.go`, `deployment.go`, `invitation.go`, `ruleset.go`, `webhook.go`, `webhook_push.go`, `actions.go`, `status.go` |
| Infrastructure | `errors.go`, `crud.go`, `preload.go`, `embedding_hook.go` |

## Main Flows

### PR Merge

```
MergePR(ctx, repoFullName, prNumber, method, message)
  → authenticate current user
  → load PR, validate open + not already merged
  → enforce merge policy:
      → require repository write access
      → load branch protection for the base branch
      → enforce required approvals unless the actor is in `bypass_pull_request_allowances.users`
      → enforce required status-check contexts (strict mode is rejected)
  → MergePRRecord():
      → resolve merge method (merge / squash / rebase)
      → call Git.Merge, Git.SquashMerge, or Git.Rebase
      → on success: single GORM `Updates` call to set merged=true, state=closed, merged_commit_sha, and clear any queued auto-merge request
      → reload PR with full preloads
```

This is one of the highest-risk flows because it crosses DB state and real Git history.

### Auto-Merge Queue

```
SetPRAutoMerge(ctx, prID, input)
  → authenticate current user
  → require repository write access
  → require repo.AllowAutoMerge when enabling
  → optionally validate expectedHeadOid against the current PR head SHA
  → persist merge method plus optional commit metadata and expected head SHA

CreateCommitStatus / completeRun
  → reevaluate open PRs whose head SHA matches the updated check/status SHA
  → reuse the same merge policy as manual merges
  → merge with the queued actor identity when policy passes
```

Current contract:
- `required_pull_request_reviews.bypass_pull_request_allowances.users` is enforced in the merge policy.
- `teams` and `apps` bypass actors are not supported.
- `required_status_checks.strict` is rejected rather than being silently ignored.

### Repository Creation with Fork

```
ForkRepo(ctx, srcFullName, targetOwner)
  → resolve source repo
  → CreateRepo under target owner (DB record + Git.Init)
  → Git.Fork (cp -a source bare repo)
  → DB transaction: set fork=true, parent_id
  → on any failure: compensating cleanup (delete DB record + Git directory)
```

### Issue/PR Number Allocation

```
CreateIssue(ctx, repoFullName, fields)
  → retry loop (max 5 attempts):
      → DB transaction:
          → lockRepoForNumbering (SELECT ... FOR UPDATE on repo row)
          → nextIssueOrPRNumberTx (MAX(number) across issues and PRs + 1)
          → INSERT issue with allocated number
      → on duplicate key: sleep(retryDelay) and retry
  → reload with preloads
```

PRs use the same number sequence to match GitHub behavior.

### Timeline Synthesis

```
GetIssueTimeline(ctx, repoFullName, number)
  → fetch issue or PR
  → load IssueComments → wrap as TimelineEvent
  → if PR: load PullRequestReviews → wrap as TimelineEvent
  → sort all events by CreatedAt
  → return chronological timeline
```

## Invariants and Design Constraints

- **Service is the only layer that coordinates both relational and Git state.** Surfaces should call service methods, not orchestrate GORM + gitstore themselves.
- **Wiki path and backlink rules live in service.** `service/wiki.go` owns the single writable slug grammar, prefix-collision checks, atomic move preconditions, markdown-aware inbound-link rewrites during page moves, link parsing, and the wiki-HEAD-keyed in-memory backlink cache so REST stays transport-thin.
- **Wiki catalog freshness lives in service.** `service/wiki_migrate.go` decides when catalog-backed reads are stale, schedules at most one background migration replay per repository, and keeps read handlers non-blocking while the catalog catches up to git-backed wiki pushes or historical imports.
- **Wiki catalog CAS GC is automatic housekeeping.** The server runtime starts an hourly background worker that calls `wikicatalog.GCRun` with one-hour pending/refcount TTLs, removing legacy inline-body ref metadata and reclaiming orphaned pending blobs and zero-refcount CAS blobs without exposing a REST or CLI trigger.
- **Wiki labels live in service.** `service/wiki_label.go` attaches the existing repo-scoped `labels` catalog to git-backed wiki slugs through `wiki_page_labels`, validates that the target page exists, keeps label links in sync across wiki delete/move/prefix move operations, and exposes label-filter helpers for list/search. Label mutations persist their lexical projection task in the same database transaction because they change search content without changing the page revision.
- **Wiki writes preserve one linear Git commit per API mutation.** REST page mutations capture the catalog head and touched-page conflict state in one preflight snapshot, validate that snapshot, and compute the exact Git commit SHA in memory. Git object persistence then overlaps the catalog transaction; a catalog pre-commit barrier waits for object durability and rolls the SQL transaction back if persistence fails. The original catalog transaction stores that durable Git SHA, so successful ref publication needs no second catalog marker update. Only after both durable operations succeed does the service publish the branch with a parent CAS while holding the repository write lock. For a single-page upsert, the preflight snapshot also carries prefix-directory state and live outbound-link targets, so the transaction can skip known directory rows and avoid resolving the same links twice. A changed head fails with `ErrCASLost`, forcing Git preparation and catalog validation to restart from one parent. This preserves synchronous failure semantics, the single-page CRUD API, and one linear Git commit per mutation while avoiding serial Git/catalog latency.
- **Wiki writes fail closed until catalog and Git agree.** Under the shared catalog/Git lock, every REST mutation compares the durable catalog SHA with Git HEAD: a catalog-ahead prepared commit is republished, while Git-ahead commits left by direct push are synchronously ingested. A new mutation cannot advance either head while an older projection remains unresolved. Read-path freshness checks are non-destructive for catalog-originated state because a lagging or missing Git ref can represent interrupted publication; explicit Git ingest and receive-pack remain authoritative for force-push rewrites and content-branch deletion. Before Git HTTP receive-pack can mutate a wiki ref, the service claims a `wiki_git_repair_obligations` row with the pre-receive-pack Git snapshot, an owner token, and an owner expiration; this claim is insert-only so a concurrent receive-pack cannot overwrite another active owner after both instances have reconciled. The Git HTTP handler refreshes that owner expiration while the receive-pack critical section is active. Rejected pushes, no-op pushes, and successful synchronous ingest can clear only the row owned by that receive-pack token. A later serialized writer must consume any remaining row before the healthy REST fast path: an unexpired in-progress owner makes the writer fail closed, an expired unchanged pre-receive-pack snapshot is cleared as abandoned, and an expired changed snapshot is honored as authoritative receive-pack state before any REST recovery path can republish catalog-ahead content. The supported mutation boundary is the REST API or Git HTTP receive-pack; direct filesystem edits to a server-side bare repository are an internal-storage violation and are not interpreted by ordinary reads.
- **Wiki post-commit work is ordered without extending the Git lock.** Repository lookup data and changed bodies travel in the changeset result. Git ref publication remains inside the critical section; issue-reference synchronization runs through a per-tenant, per-repository FIFO after the lock is released, and the API still waits for it synchronously. The existing `wiki_repo_heads` CAS update also advances a durable, coalescing reference-recovery cursor when a changeset may add or remove wiki issue references; normal completion clears it conditionally, and a runtime recovery worker rebuilds current references for any repository left pending by a process interruption. Plain new pages without issue-reference syntax leave the cursor unchanged and add no transaction query. Search mutations persist a coalescing TiDB outbox task after Git publication, so catalog/Git writes never wait for embedding.
- **Wiki search lifecycle also lives in service.** `service/wiki_search.go` owns repo-scoped candidate indexes, lexical recall, label-name/description boosting, semantic ranking, stale-row filtering, live result hydration, and explicit reindexing. `service/wiki_search_projection.go` owns durable put/move/delete/label projection: repository-bound tasks coalesce by repository, slug, and kind; lexical projection always lands before a separate embedding task; generation checks reject stale completions; leases permit safe multi-instance recovery; and startup repair recreates tasks for missing, stale, or deleted documents.
- **Service owns collaboration policy.** Org membership, org invitations, outside-collaborator reconciliation, and effective repository permission resolution all live in `service`, not in REST or GraphQL handlers.
- **Sentinel errors for surface mapping.** `errors.go` defines `ErrNotFound`, `ErrConflict`, `ErrInvalidState`, `ErrValidation`, `ErrUnauthorized`, `ErrDuplicate`, `ErrInvalidRequest`, and `ErrAlreadyCollaborator`. REST maps these to HTTP status codes via `respond.ServiceError`; GraphQL maps them to error payloads.
- **`wrapErr` normalizes GORM errors.** GORM's `ErrRecordNotFound` is converted to `ErrNotFound` for consistent HTTP 404 mapping.
- **Retry with backoff for concurrent number allocation.** Issue and PR creation retry up to 5 times on duplicate key errors, with exponential backoff via `retryDelay`.
- **Preload chains for consistent association loading.** `preload.go` defines reusable GORM preload helpers (`preloadIssue`, `preloadPRFull`, etc.) to prevent N+1 queries and ensure surfaces receive fully-loaded objects.
- **The concrete service is the primary wiring seam.** Production code passes `*Service`; introduce narrower interfaces only for a specific, tested boundary.

For the full dependency-boundary rules see [module-contracts.md § service](../module-contracts.md#service).

## Extension and Change Guidance

**Adding a new domain operation:**

1. Add the method to `Service` in the appropriate domain file.
2. Use the active request-scoped DB accessor for all GORM queries to respect request cancellation and context-scoped DB overrides. In today's code that is `s.DBForCtx(ctx)`.
3. Return sentinel errors for failures that surfaces need to distinguish (e.g., `ErrNotFound` for missing entities).
4. If the operation touches Git state, coordinate through `s.Git` and handle the case where `s.Git` is nil.
5. If the operation creates sequentially-numbered entities, follow the retry + `lockRepoForNumbering` pattern from `issue.go`.

**Common patterns:**

- Public methods accept `repoFullName` (e.g., `"owner/repo"`); internal helpers resolve to numeric IDs for efficiency.
- Upsert operations use `isDuplicateErr` for idempotency.
- Compensating cleanup on multi-step failures (see `repo_fork.go`).
- SQL injection prevention via `escapeLike` for LIKE queries.

## Related Tests

Test files in `internal/service/`:

| File | Coverage |
|---|---|
| `repo_lifecycle_test.go`, `repo_test.go`, `repo_fork_test.go` | Repository CRUD, fork, transfer |
| `pr_test.go`, `pr_lifecycle_test.go`, `pr_merge_test.go` | PR creation, merging, state transitions |
| `issue_test.go` | Issue creation, update, list |
| `comment_test.go` | Comment operations |
| `review_test.go` | Review creation and submission |
| `label_test.go` | Label CRUD and issue attachment |
| `milestone_test.go` | Milestone numbering and CRUD |
| `team_test.go` | Team management |
| `auth_test.go` | Token validation and device code exchange |
| `gist_test.go` | Gist CRUD |
| `release_test.go` | Release operations |
| `workflow_test.go` | Workflow dispatch and runs |
| `search_test.go`, `search_db_test.go` | Search qualifier parsing and DB queries |
| `service_test.go` | General service helpers |
| `numbering_concurrency_test.go` | Concurrent number allocation |

For the phased test roadmap see [docs/test-strategy.md](../test-strategy.md).

## Related Docs

- [docs/architecture.md](../architecture.md) — system overview
- [docs/module-contracts.md](../module-contracts.md) § service — dependency rules and ownership
- [Git Store](gitstore.md) — bare-repository operations that service orchestrates
- [REST API](rest.md) — primary consumer of service methods
- [GraphQL API](graphql.md) — primary consumer of service methods
