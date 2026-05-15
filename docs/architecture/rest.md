# REST API — Component Reference

## Purpose

`internal/rest` implements the GitHub REST API v3 surface.
Handlers decode HTTP requests, call service methods, transform results into GitHub-compatible JSON shapes, and write responses using standard status codes and error formats.

For the full system overview see [docs/architecture.md](../architecture.md).
Core invariant: `agent-git-service` is Git-backed; see [architecture.md § Purpose](../architecture.md#purpose).

## Scope

Owns:

- HTTP request decoding (path params, query params, JSON body)
- transport-level validation (required fields, type parsing)
- calling service methods
- transforming DB models into GitHub REST JSON shapes (`rest/transform`)
- mapping service errors to HTTP status codes (`rest/respond`)

Does not own:

- business rules or domain validation (belongs to `service`)
- route registration (belongs to `router`)
- auth extraction (belongs to `middleware`)
- GraphQL response shapes (belongs to `graphql`)

## Key Entry Points

### Deps Struct

All handlers are methods on `*Deps`, defined in `handlers.go`:

```go
type Deps struct {
    Svc *service.Service
}
```

Handlers access the service layer through `d.Svc` and Git operations through `d.Svc.Git`.

### Handler Files by Domain

**Repository and org lifecycle:**

| File | Covers |
|---|---|
| `handlers_repo.go` | Repo CRUD, org lookup for existing org accounts, topics, languages, fork, transfer |
| `handlers_user.go` | Authenticated user, user lookup, user repos, explicit org create/list, stars, collaborators, assignees |

**Issues and pull requests:**

| File | Covers |
|---|---|
| `handlers_issue.go` | Issue CRUD, list with filtering, comments, lock/unlock, assignees, timeline, events |
| `handlers_pr.go` | PR CRUD, list, merge, commits, files, requested reviewers, reviews, review comments, ready-for-review |
| `handlers_label.go` | Label CRUD, issue-label add/remove/set |
| `handlers_milestone.go` | Milestone CRUD, counts, milestone issue listing |

**Git operations (direct `Svc.Git` access):**

| File | Covers |
|---|---|
| `handlers_git.go` | Branches, commits, file content read/write/delete, tags, compare, contributors, refs |
| `handlers_search.go` | Repository search, issue/PR search, commit search (`Svc.Git.SearchCommits`), code search (`Svc.Git.SearchCode`) |

**Releases:**

| File | Covers |
|---|---|
| `handlers_release.go` | Release CRUD, tag lookup, latest release, release notes, assets upload/download, archive streaming (`Svc.Git.Archive`) |

**Actions and workflows:**

| File | Covers |
|---|---|
| `handlers_workflow.go` | Workflow list/get/enable/disable, workflow runs, dispatch, cancel, rerun |
| `handlers_workflow_jobs.go` | Workflow run jobs and artifacts |
| `handlers_cache.go` | Action cache list and delete |
| `handlers_actions.go` | Environments, environment variables and secrets |
| `handlers_secrets.go` | Repo and org secrets |
| `handlers_variables.go` | Repo and org variables |

**Auth and keys:**

| File | Covers |
|---|---|
| `handlers_keys.go` | Deploy keys, SSH keys, GPG keys, SSH signing keys |

**Other:**

| File | Covers |
|---|---|
| `handlers_gist.go` | Gist CRUD |
| `handlers_branch.go` | Branch protection rules, including user-based PR-review bypass actors |
| `handlers_webhook.go` | Webhook CRUD, delivery list/detail, redelivery |
| `handlers_team.go` | Team CRUD, members, team-repo grants |
| `handlers_invitation.go` | Repository invitations, accept/decline |
| `handlers_org_invitation.go` | Organization invitations, accept/decline/revoke |
| `handlers_outside_collaborator.go` | Outside collaborator listing for orgs |
| `handlers_dependabot.go` | Dependabot alerts |
| `handlers_deployment.go` | Deployments and deployment statuses |
| `handlers_ruleset.go` | Repository rulesets |
| `handlers_wiki.go` | Wiki page list/get/put/delete, per-page history, labels, atomic move, search, and backlink lookup |
| `handlers_misc.go` | Miscellaneous endpoints |
| `handlers_templates.go` | Issue and PR templates |
| `pagination.go` | Pagination helpers |

### respond Package

`rest/respond` provides GitHub-style HTTP response helpers.

**`ServiceError` mapping** (the primary error-to-HTTP bridge):

| Service Error | HTTP Status |
|---|---|
| `ErrNotFound` | 404 |
| `ErrConflict` | 409 |
| `ErrInvalidState` | 422 |
| `ErrValidation` | 422 |
| unrecognized | 500 (logged) |

Other helpers: `JSON(w, status, v)`, `NotFound(w)`, `Error(w, status, msg)`, `ValidationFailed(w, msg)`, `NoContent(w)`.

All error responses follow the GitHub format: `{"message": "...", "documentation_url": "https://docs.github.com/rest"}`.

### transform Package

`rest/transform` converts GORM DB models into GitHub-compatible JSON shapes.

| File | Converts |
|---|---|
| `transform.go` | `User`, `Repo` (with stats), `Branch`, `Commit`, `Gist`, `NodeID` helper, URL builders |
| `transform_issue_pr.go` | `Issue`, `PR` (with stats), `IssueComment`, `PRReview`, `PRReviewComment`, `Reactions`, `AuthorAssociation` |
| `transform_misc.go` | `Milestone`, `Label`, `Release`, `ReleaseAsset`, `DeployKey` |
| `transform_team.go` | `Team` |
| `transform_workflow.go` | Workflow-related shapes |

URL generation is centralized: `Init(baseURL)` must be called at startup, and all URL fields are derived from that base.

## Main Flows

### Standard REST Request

```
client → router → auth middleware → REST handler → service → DB/GitStore → transform → respond
```

Handlers follow a consistent pattern:

1. Extract path/query params via `chi.URLParam` and helper methods (`mustGetRepo`, `mustIntParam`)
2. Decode JSON body if needed via `decodeBody`
3. Call service methods: `d.Svc.GetRepo(ctx, fullName)`
4. On error: `respond.ServiceError(w, err)` and return
5. Transform result: `transform.Repo(rep, stats)`
6. Write response: `respond.JSON(w, http.StatusOK, result)`

### Wiki Backlinks

`GET /api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/backlinks` follows the standard REST pattern:

- resolve `{owner}`, `{repo}`, and `{slug}` from the path
- delegate backlink lookup and cache handling to `service.ListWikiBacklinks`
- transform each entry to `{ slug, title, snippet, html_url, url }`
- rely on the standard service error mapping so missing wiki pages stay `404`

Wiki path-slug hierarchy rules:

- page slugs are lowercase canonical paths such as `guides/setup`
- wiki page routes treat `{slug}` as one percent-encoded path parameter; clients must request nested slugs such as `guides/setup` as `guides%2Fsetup` when the slug is followed by a subresource, for example `/wiki/pages/guides%2Fsetup/history`
- `GET /api/v3/repos/{owner}/{repo}/wiki/pages/{slug}` accepts an optional `ref` query parameter to read the page body and blob SHA at a full commit SHA from that page's history; omitted `ref` still reads HEAD
- `GET /api/v3/repos/{owner}/{repo}/wiki/pages` accepts `path`, `recursive`, `label`/`labels`, and `exclude_label`/`exclude_labels` query parameters for prefix-scoped and label-scoped listing
- `GET /api/v3/repos/{owner}/{repo}/wiki/search` accepts `q`, `limit`, `offset`, `label`/`labels`, and `exclude_label`/`exclude_labels`, returns `{results, query, method, elapsed_ms}`, and caps `limit` server-side at 50
- `GET/POST/PUT/DELETE /api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/labels...` attaches repo-scoped labels to wiki pages; labels are metadata, not git-tracked page content
- `POST /api/v3/repos/{owner}/{repo}/wiki/move` atomically renames every page whose slug equals `from` or starts with `from/`, requires an `if_match` SHA map that covers the full source set, and returns one commit for the entire move
- `POST /api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/move` performs an atomic rename with `new_slug` and `if_match`, rewrites eligible inbound wiki references in the same commit, and returns `{ moved, rewrites, skipped }`
- wiki page get/list/search responses include `labels`, shaped with the existing repository label JSON contract
- wiki write endpoints reject `ref` because historical revision edits are out of scope for the current REST contract
- only the exact single-segment routes `/wiki/pages/{slug}/history`, `/wiki/pages/{slug}/backlinks`, `/wiki/pages/{slug}/move`, and `/wiki/pages/{slug}/labels...` bind the wiki subresources directly
- read/list/backlink operations also surface legacy on-disk wiki filenames that still contain uppercase letters, underscores, or dots
- wiki search indexing is asynchronous after successful put/move/delete/label writes, so clients must tolerate short freshness lag; when embeddings are unavailable or semantic ranking fails, the endpoint falls back to substring matching and reports `method: "substring"`

### Wiki Page History

`GET /api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/history` follows the standard REST pattern:

- resolve `{owner}`, `{repo}`, and `{slug}` from the path
- delegate path-filtered revision lookup to `service.ListWikiPageHistory`
- load the full path-filtered history before applying shared REST pagination so older wiki revisions remain reachable beyond 10,000 commits
- paginate with the shared `pagination.go` helpers so `page`, `per_page`, and RFC 5988 `Link` headers match the rest of the REST surface
- transform each entry to `{ sha, message, author, committer, date, body_size }`
- rely on the standard service error mapping so missing wiki pages stay `404`

### Git-Backed REST Request

```
client → router → auth middleware → REST handler → d.Svc.Git.* → respond
```

Git-centric handlers in `handlers_git.go` and `handlers_search.go` bypass the service layer and call `d.Svc.Git.*` directly for branch, tag, diff, content, search, and ref operations. This is an accepted current coupling documented in `module-contracts.md`.

### Workflow-Backed Checks Compatibility

`GET /api/v3/repos/{owner}/{repo}/commits/{ref}/check-runs` is implemented as a
GitHub Checks compatibility shim over stored workflow runs and workflow jobs.
It is not a general-purpose Checks API store for arbitrary external apps.

Current contract:

- successful responses enumerate workflow jobs for the resolved branch or commit SHA
- each returned check run includes the compatibility-critical fields used by downstream automation:
  `head_sha`, `details_url`, `html_url`, `check_suite.id`, `app.id`, `app.slug`, `app.name`, and a stable `external_id`
- `id` remains the workflow job-backed check-run identifier used by this server's
  `GET /check-runs/{check_run_id}` and annotations endpoints; clients must not
  assume it is interchangeable with a GitHub Actions workflow run ID
- the synthetic `app.slug` is `gh-server-actions`, which intentionally differs
  from GitHub's hosted integrations so clients can detect the compatibility layer
- `pull_requests` linkage is not currently synthesized for workflow-backed check runs

Failure semantics are intentionally loud:

- missing repositories return `404` instead of `200 { total_count: 0, check_runs: [] }`
- missing refs or unknown SHAs return `404`
- if the server cannot resolve a ref because the Git backend is unavailable, the
  endpoint returns `501` instead of pretending the repository has no checks
- a valid resolved ref with no matching workflow jobs still returns the normal
  empty success payload

### PR Diff via Accept Header

```
GET /repos/{owner}/{repo}/pulls/{number}
  Accept: application/vnd.github.v3.diff
  → handler detects diff Accept header
  → d.Svc.Git.DiffRaw(ctx, fullName, baseSHA, headSHA)
  → write raw diff text (not JSON)
```

## Invariants and Design Constraints

- **Thin handlers.** Handlers should parse input, call service, transform output, and write the response. Business logic belongs in `service`.
- **No direct GORM queries.** REST handlers do not run GORM queries directly today; all persistence flows through `service`.
- **Accepted `Svc.Git` coupling.** Git-centric handlers call `d.Svc.Git.*` directly for performance and simplicity. This is the current package boundary: "thin transport plus direct Git access through `Svc`."
- **Repository permission resolution stays in `service`.** REST serializes GitHub-compatible repo permission maps and org member/outside-collaborator annotations, but the effective permission decision comes from `service.HasRepoAccess` and now collapses runtime authorization to `read`/`write`/`admin`.
- **`rest/transform` is REST-only.** GraphQL builds its own response shapes and must not depend on `rest/transform`.
- **`rest/respond` is shared.** Other surface packages (GraphQL, OAuth, Git HTTP) use `rest/respond` for HTTP JSON writing. This is acceptable because the dependency points toward transport helpers, not back into business logic.

For the full dependency-boundary rules see [module-contracts.md § rest](../module-contracts.md#rest).

## Extension and Change Guidance

**Adding a new REST endpoint:**

1. Add the handler method to `*Deps` in the appropriate `handlers_*.go` file (or create a new file for a new domain area).
2. Register the route in `internal/router/router.go`.
3. Call service methods for business logic; do not add business rules in the handler.
4. Use `respond.ServiceError` for error mapping and `transform.*` for response shapes.
5. If the endpoint needs a new transform, add it to the appropriate `transform_*.go` file.

**Common patterns:**

- `repoFullName(r)` extracts `{owner}/{repo}` from the URL.
- `mustGetRepo(w, r)` loads the repo or writes a 404 and returns nil.
- `decodeBody(r, &target)` unmarshals JSON request body.
- Pagination is handled by helpers in `pagination.go`.

## Branch Protection Contract

- `PUT /repos/{owner}/{repo}/branches/{branch}/protection` persists the raw GitHub-style branch-protection JSON.
- Merge enforcement currently honors:
  - `required_pull_request_reviews.required_approving_review_count`
  - `required_pull_request_reviews.bypass_pull_request_allowances.users`
  - `required_status_checks.contexts`
- `required_pull_request_reviews.bypass_pull_request_allowances.teams` and `.apps` are rejected at the REST boundary.
- `required_status_checks.strict` is persisted but rejected by the merge policy, so callers do not get a false GitHub-parity signal.

## Related Tests

- `internal/rest/handlers_gist_test.go` — gist handler tests
- `internal/rest/pagination_test.go` — pagination helper tests
- Integration tests exercise REST endpoints through the real router (see `internal/router/` tests if present)
- Acceptance tests in `cli/acceptance/` exercise REST endpoints through the vendored GitHub CLI

For the phased test roadmap see [docs/test-strategy.md](../test-strategy.md).

## Related Docs

- [docs/architecture.md](../architecture.md) — system overview, routing model, canonical request flows
- [docs/module-contracts.md](../module-contracts.md) § rest — dependency rules, accepted couplings
- [Service Layer](service.md) — business logic called by handlers
- [GraphQL API](graphql.md) — parallel surface with its own response shapes
