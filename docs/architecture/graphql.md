# GraphQL API — Component Reference

## Purpose

`internal/graphql` implements the GitHub GraphQL API v4 surface.
It parses incoming GraphQL requests, routes them to query or mutation handlers, builds GraphQL-specific response shapes, and filters responses to the requested field selection set.

The field-filtering behavior is critical for strict GraphQL clients, including `gh` CLI: unexpected fields can break strict unmarshaling.

For the full system overview see [docs/architecture.md](../architecture.md).
Core invariant: `agent-git-service` is Git-backed; see [architecture.md § Purpose](../architecture.md#purpose).

## Scope

Owns:

- GraphQL request parsing (query string, operation name, variables)
- query and mutation routing via AST field detection
- building GraphQL-specific response shapes (distinct from REST transforms)
- field filtering against the requested selection set
- fragment expansion for nested filtering

Does not own:

- business rules or domain validation (belongs to `service`)
- REST response shapes (belongs to `rest/transform`)
- route registration (belongs to `router`)

## Key Entry Points

| File | Responsibility |
|---|---|
| `handler.go` | HTTP entry point (`Handler`), request parse, mutation vs query detection, response filtering via `filterMap` |
| `handler_query.go` | Query routing: AST field checks, regex pattern matching for aliased queries, operation name fallback |
| `gql_mutations.go` | Mutation handler implementations (repo, issue, PR, project, etc.) |
| `filter.go` | AST parsing (`parseQuery` via gqlparser), `filterMap` for recursive response pruning, fragment expansion |
| `gql_helpers.go` | Utility functions: repo resolution, input extraction, author shapes, N+1 prevention helpers |

**Query handlers:**

| File | Covers |
|---|---|
| `gql_query_repo.go` | Node resolution (Issue/PR by ID), resource URL resolution, releases, labels, milestones, assignable users |
| `gql_query_repo_detail.go` | Full repository details, refs, tag existence, issue/PR unified lookup, rulesets, templates, forks |
| `gql_query_issue.go` | Issue list with alias-based filterBy (assignee, mentioned, createdBy, `$viewer` resolution) |
| `gql_query_pr.go` | PR list with headRefName filtering, `baseRef.compare()` for branch comparison, search with aliased queries |
| `gql_query_project.go` | Project by owner/number, project list for org/user/viewer |

**Mutation handlers:**

| File | Covers |
|---|---|
| `gql_mut_repo.go` | Create, clone template, update, archive/unarchive repository |
| `gql_mut_issue.go` | Create, close, reopen, update, delete, lock/unlock issue |
| `gql_mut_pr.go` | Create PR (with cross-repo support), merge (with panic recovery), close, reopen, draft toggle, update, revert |
| `gql_mut_issue_comment.go` | Add/update/delete comment, add/remove labels, replace assignees |
| `gql_mut_pr_review.go` | Request reviews, add review (approve/request changes/comment), queued auto-merge enable/disable, resolve/unresolve review threads |
| `gql_mut_project.go` | Project CRUD, batched item add/delete with aliases, update, close, copy |
| `gql_mut_project_field.go` | Project field CRUD, item field value update/clear |
| `gql_mut_dependabot.go` | Dismiss and resolve vulnerability alerts |

**Shape builders:**

| File | Converts |
|---|---|
| `gql_shapes.go` | Generic helper structs |
| `gql_shapes_repo.go` | Repository, User |
| `gql_shapes_issue.go` | Issue with lazy comment loading, state reason, linked branches |
| `gql_shapes_pr.go` | PR with lazy diff stats, review loading, merge simulation, status check rollup |
| `gql_shapes_project.go` | Project, fields, items (including draft issues) |
| `gql_shapes_team.go` | Team with member nodes |
| `gql_shapes_pr_status.go` | PR status shapes |
| `gql_shapes_dependabot.go` | Dependabot alert shapes |

## Main Flows

### Query Pipeline

```
POST /api/graphql { query, variables, operationName }
  → handler.go: Handler()
  → parseQuery(query)
      → gqlparser AST → field map + fragment definitions + __type introspections
  → routeQuery(ctx, req, ast)
      → AST field checks (astHas, astChild) to determine query type
      → regex fallback for batched aliased repository queries
      → operation name fallback for CLI-specific queries
      → dispatch to doRepository, doIssues, doPRs, doSearch, doNode, etc.
  → resolver builds full response map
  → filterMap(response, ast, fragments)
      → recursively prune to only requested fields
      → expand fragment spreads
  → respond.JSON(w, 200, {"data": filtered})
```

### Mutation Pipeline

```
POST /api/graphql { query: "mutation { createIssue(...) { issue { id } } }", variables }
  → Handler() detects "mutation" keyword
  → parseQuery → AST
  → routeMutation(ctx, req, ast)
      → compound conditions for XOR mutations (archive/unarchive)
      → lookup table: mutation name → handler function
  → handler: extract input vars → call service → build shape → wrap("createIssue", result)
  → filterMap → respond
```

### Field Filtering

```
Request: repository { issues { nodes { id title } } }
  → AST: {repository: {issues: {nodes: {id: true, title: true}}}}
  → resolver returns full issue objects (id, title, body, author, labels, ...)
  → filterMap strips everything except id and title at each nesting level
  → clients such as gh CLI receive only requested fields, strict unmarshaling succeeds
```

Without filtering, strict GraphQL clients such as `gh` CLI can reject the response.

### PR with Branch Comparison

```
query { repository { pullRequest(number: N) { ... baseRef { compare(headRef: ...) { ... } } } } }
  → doPRSingle()
  → detect baseRef.compare in AST
  → Svc.Git.HeadSHA(ctx, repo, base) + Svc.Git.HeadSHA(ctx, repo, head)
  → Svc.Git.Compare(ctx, repo, baseSHA, headSHA)
  → embed comparison data in PR response shape
```

## Invariants and Design Constraints

- **Field filtering is mandatory.** Every GraphQL response passes through `filterMap` before being sent to the client. This ensures compatibility with strict GraphQL client libraries.
- **GraphQL builds its own response shapes.** The `gql_shapes_*.go` files are independent of `rest/transform`. REST and GraphQL contracts differ, and sharing shape code would create coupling.
- **GraphQL uses `rest/respond` for HTTP writing only.** This is acceptable because GraphQL builds its own data payloads; `rest/respond` only handles the HTTP JSON serialization.
- **Repository authorization flows through `service.HasRepoAccess`.** `viewerPermission` now reports the richer `READ`/`TRIAGE`/`WRITE`/`MAINTAIN`/`ADMIN` vocabulary rather than a three-level model.
- **Accepted `Svc.Git` coupling.** Several resolvers call `s.Svc.Git.*` directly for compare, mergeability, branch SHA lookup, repo detail, and revert operations. This is documented as accepted current coupling.
- **Accepted `Svc.DB` coupling in Dependabot mutations.** `gql_mut_dependabot.go` queries `s.Svc.DB` directly to resolve alert records by ID. This is current technical debt that bypasses the intended persistence ownership.
- **`Server` depends on concrete `*service.Service`.** The GraphQL server receives the full service struct, not a narrow interface. This is acceptable for now given the large resolver surface.

For the full dependency-boundary rules see [module-contracts.md § graphql](../module-contracts.md#graphql).

## Extension and Change Guidance

**Adding a new query:**

1. Add a `do*` handler method to `*Server` in the appropriate `gql_query_*.go` file.
2. Add AST detection logic in `handler_query.go` `routeQuery()` (use `astHas` or `astChild`).
3. Build the response shape in the corresponding `gql_shapes_*.go` file.
4. The handler should call service methods, build the shape, and return via `wrap("fieldName", data)`.
5. Field filtering is automatic — no additional work needed.

**Adding a new mutation:**

1. Add a `do*` handler method in the appropriate `gql_mut_*.go` file.
2. Register it in the mutation dispatch table in `handler.go` `routeMutation()`.
3. Extract input variables via `inputMap(req)` and type-safe helpers (`strFrom`, `intVar`).
4. Call service methods, build the response shape, and return via `wrap`.

**Common patterns:**

- `resolveRepo(req)` extracts owner/name from variables.
- `parseNodeID(id)` converts Base64 Relay-style node IDs to DB IDs.
- `queryHasAny(req.Query, "fieldName")` is a fast heuristic to prevent N+1 queries (checks raw query string before doing expensive lookups).
- Batched alias mutations (e.g., `add_000`, `delete_001`) are handled by iterating over request aliases.

## Auto-Merge Contract

- `enablePullRequestAutoMerge` and `disablePullRequestAutoMerge` call `service.SetPRAutoMerge`, so GraphQL enforces authentication, repository write access, and the repository-level `autoMergeAllowed` flag.
- The enable mutation persists `mergeMethod`, `commitHeadline`, `commitBody`, `authorEmail`, and `expectedHeadOid` for later execution.
- Queued auto-merge is executed asynchronously when workflow runs or commit statuses update the PR head SHA and the shared service merge policy passes.
- GraphQL does not expose a branch-protection mutation or `bypassPullRequestAllowances` equivalent today. Branch-protection bypass configuration is available only through the REST branch-protection payload.

## Related Tests

- `internal/graphql/filter_test.go` — field filtering and fragment expansion
- `internal/graphql/graphql_test.go` — end-to-end query and mutation tests
- `internal/graphql/dependabot_test.go` — Dependabot mutation tests
- Acceptance tests in `cli/acceptance/` exercise GraphQL through the vendored GitHub CLI

For the phased test roadmap see [docs/test-strategy.md](../test-strategy.md).

## Related Docs

- [docs/architecture.md](../architecture.md) — system overview, canonical request flows
- [docs/module-contracts.md](../module-contracts.md) § graphql — dependency rules, accepted couplings
- [Service Layer](service.md) — business logic called by resolvers
- [REST API](rest.md) — parallel surface with its own response shapes
