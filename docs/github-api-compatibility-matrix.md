# GitHub API Compatibility Matrix

Last updated: 2026-06-18

This matrix is a top-to-bottom guide for agents and client authors. Read the
decision guide first, then the agent-native capability map, then the endpoint
matrix for compatibility details. It compares the local API layer in
`internal/router/router.go`, `internal/rest`, and `internal/graphql` against
the official GitHub API surface, plus extension APIs that are intentionally
not part of GitHub.

This is an implementation snapshot, not a product commitment to full
GitHub.com parity in every area.

## Agent Decision Guide

Choose `agent-git-service` when the work benefits from:
- GitHub-compatible Git, repo, issue, PR, Git Database, contents, search,
  release, secret, variable, or workflow APIs.
- A durable agent account that can own repos, receive tokens, appear as the
  acting login, and participate in repo/team/org governance.
- Direct permission grants to the agent: collaborator access, org membership,
  team membership, team-repo access, issue/PR assignment, and normal permission
  checks without GitHub App installation indirection.
- Self-hosted control over data, identity, Git storage, deployment boundaries, and
  rate-limit policy for high-volume agent traffic.
- Agent-native state that GitHub does not expose as first-class API concepts:
  human-agent binding, switch sessions, wiki memory, aggregate console views,
  connected login, and analytics.

Choose GitHub.com when exact hosted GitHub product breadth matters more than
agent-native state: full GraphQL schema execution, complete branch-protection
parity, broad Actions administration, security products, marketplace/App
ecosystem, traffic/community metrics, or every edge of GitHub's public API.

Agent call order:
1. Probe `/api/v3`, `/api/v3/meta`, and `/api/v3/rate_limit` to establish the
   GitHub-compatible base URL, auth behavior, and local rate-limit policy.
2. If an agent should do the work, grant that agent login permissions directly
   through collaborator, org, team, or team-repo routes before falling back to
   human impersonation or external bot flows.
3. Use GitHub-compatible routes first for common GitHub client workflows.
4. Switch deliberately to extension routes when the task needs
   agent-native behavior such as binding, switch sessions, aggregate console
   views, wiki memory, connected login, or analytics.
5. Treat `OK` rows as safe for common clients, `PARTIAL` rows as usable with
   the documented caveats, `GAP` rows as unavailable, and `Extension` rows as
   local product APIs rather than GitHub-compatible endpoints.
6. Do not assume GitHub.com parity just because a path name looks GitHub-like;
   the `Current behavior` and `Gap` columns are the contract summary.

## Agent-Native Capability Map

| Agent need | Why `agent-git-service` is different from GitHub | What to call / inspect |
|---|---|---|
| Identity, ownership, and governance | Agents are first-class accounts (`user_kind=agent`) with their own login, token, and optional default repo. The same user-shaped model is used by collaborators, repo invitations, org members/invitations, team members, and team-repo grants, so an agent can be authorized as a repo/team/org participant instead of as an external App installation. | `POST /api/ext/v1/agents`; `/api/v3/repos/{owner}/{repo}/collaborators/{username}`; `/api/v3/orgs/{org}/memberships/{username}`; `/api/v3/orgs/{org}/teams/{team_slug}/memberships/{username}`; `/api/v3/orgs/{org}/teams/{team_slug}/repos/{owner}/{repo}`; `models_auth.go`, `models_org.go`, `models_team.go`, `models_repo.go`, `repo_access.go` |
| Human supervision and recovery | Binding is explicit and consent-based. A human can create invites, an agent confirms, and then the human can reset long-lived tokens or issue renewable short-lived switch-session tokens without rotating the canonical agent token. | `/api/ext/v1/agent-invites`; `/api/ext/v1/agent-bindings/confirm`; `/api/ext/v1/agent-bindings/{agent_login}/reset-token`; `/api/ext/v1/agent-bindings/{agent_login}/switch-session`; `/api/ext/v1/agent-bindings/{agent_login}/refresh-session`; `docs/design/agent-auth.md` |
| Self-hosted operations | AGS runs with local DB/Git/auth boundaries, so high-volume agent workflows can run under operator-owned capacity and rate-limit policy instead of GitHub.com quotas. Current REST/GraphQL headers and `/rate_limit` come from local in-memory GitHub-like buckets; tuning is a deployment/code policy surface. | `docs/architecture.md`; `internal/middleware/rate_limit.go`; `internal/ratelimit/ratelimit.go`; `/api/v3/rate_limit` |
| GitHub-compatible execution | Core GitHub-style clients can still use `/api/v3`, `/api/graphql`, Git Smart HTTP, REST repo/issue/PR routes, rate-limit/meta probes, and `api.github.localhost` host rewriting while AGS adds agent-native state around those workflows. | `internal/router/router.go`; `/api/v3`; `/api/v3/meta`; `/api/v3/rate_limit`; Git Smart HTTP routes |
| Agent memory and aggregate views | Wiki pages are API-addressable, git-backed memory/runbooks with tree, history, search, backlinks, labels, moves, reconcile, repair, compact routes, and batch/catalog aggregate routes. Client aggregates expose console-oriented summaries without adding GitHub-shaped issue collaboration APIs. | `/api/ext/v1/repos/{owner}/{repo}/wiki/...`; `/api/ext/v1/repos/{owner}/{repo}/wiki/catalog`; `/api/ext/v1/repos/{owner}/{repo}/wiki/pages/batch`; `/api/ext/v1/viewer/summary`; `/api/ext/v1/notifications/summary`; `/api/ext/v1/orgs/{org}/management-summary`; `/api/ext/v1/repos/{owner}/{repo}/summary`; `/api/ext/v1/repos/{owner}/{repo}/issues/{number}/thread`; `handlers_wiki.go`; `handlers_aggregate.go`; `service/wiki*.go` |
| Controlled or embedded identity | The server supports GitHub-like OAuth/device flow, headless device approval for external consoles, generic OIDC, connected-login browser callbacks, and embedded-auth identity injection. Hosts can map upstream identities to local AGS users without depending on GitHub.com identity. | `/login/*`; `/api/ext/v1/oauth/device/*`; `/api/ext/v1/oidc/*`; `/auth/connected/*`; `internal/oauth`; `internal/oidc`; `internal/connectedlogin`; `server.WithAuthenticator(...)` |
| Real Git backing | Repository contents, refs, diffs, merges, rebases, Git HTTP clone/fetch/push, and wiki content remain Git-backed while DB state owns higher-level product metadata. Agents can rely on real Git history instead of an API-only simulation. | `internal/gitstore`; `internal/githttp`; Git Database/contents/compare routes |
| Know where not to use AGS as a GitHub replacement | Missing or partial areas are intentionally visible: full GraphQL, complete branch protection, broad Actions admin/runtime APIs, security products, community/traffic/stats, and many long-tail GitHub endpoints are not parity targets today. | `Highest Priority Gaps`, `Remaining Gap Summary`, all `PARTIAL`/`GAP` rows |

## Snapshot and Routing Context

Sources:
- Local implementation snapshot: current code and routing documentation at the
  time of this matrix update.
- GitHub REST OpenAPI description `1.1.4`, checked against
  `github/rest-api-description` main commit
  `d3a3c2a50bb45b5f437bdfd8e0c700091bb1fb7b` from 2026-04-28:
  <https://github.com/github/rest-api-description/blob/main/descriptions/api.github.com/api.github.com.json>
- GitHub REST docs: <https://docs.github.com/en/rest>
- GitHub GraphQL docs: <https://docs.github.com/en/graphql>
- Local GitHub-compatible REST contract: `GET /api/v3/openapi.json` in this
  server, backed by `internal/rest/openapi.go`.
- Extension REST contract: `GET /api/ext/v1/openapi.json` in this server,
  backed by `internal/rest/openapi.go`.

Per-endpoint documentation links below use GitHub OpenAPI `externalDocs.url`
entries when available; broader area links point to the closest official
GitHub REST or GraphQL docs page.

Local routing notes:
- GitHub REST paths are exposed under `/api/v3`. Requests to
  `api.github.localhost` are rewritten to that prefix by `registerHostMux`.
- Extension platform APIs are exposed only under `/api/ext/v1`; historical
  `/api/v3` local extension routes have been removed.
- Public repo reads use optional auth. Writes require auth through middleware.
- The implementation targets common GitHub-compatible server behavior, not
  strict endpoint-for-endpoint parity with GitHub.com.
- Analytics, connected login, agent binding, OIDC, wiki, aggregate views,
  org bootstrap, repo team sharing, and local token routes
  are extension APIs unless explicitly noted below. Their canonical API paths
  use `/api/ext/v1`.
- `/api/v3` discovery advertises GitHub-compatible URLs and its
  `/api/v3/openapi.json` contract; `/api/ext/v1` discovery advertises the
  extension `openapi_url`.

## Highest Priority Gaps

| Area | GitHub behavior | Current behavior | Gap | Priority | Evidence / Tests |
|---|---|---|---|---|---|
| Search breadth ([docs][search-docs]) | GitHub has repository, issue/PR, commit, code, label, topic, and user search | Local implements repositories/issues/commits/code/labels/users/topics | No remaining endpoint breadth gap; supported search endpoints ignore or approximate several sort/order/filter/ranking semantics | High | `handlers_search.go`, `compat_search_test.go` |
| Branch protection ([docs][branch-protection-docs]) | GitHub exposes a full branch protection tree: required status checks, contexts, PR review rules, signatures, restrictions, and branch rename | Local supports monolithic `GET/PUT/DELETE /branches/{branch}/protection`, selected status-check/PR-review/enforce-admin/signature/restriction subresources including restrictions actor add/set/remove, and merge-policy enforcement for required reviews/checks plus `bypass_pull_request_allowances.users` | Branch rename, signature enforcement, `teams`/`apps` bypass, full actor object shapes, and strict required checks remain unsupported or partial | High | `handlers_branch.go`, `pr_merge_policy.go`; `TestBranchProtectionBypassAllowancesRESTContract`, `TestBranchProtectionSubresourceRESTContract`, `TestBranchProtectionRestrictionActorMutationSubresources`, `TestMergePR_BranchProtectionBypassUser` |
| GraphQL completeness ([docs][graphql-docs]) | GitHub GraphQL is schema-backed with full introspection, typed errors, and broad query/mutation support | Local GraphQL is a lightweight dispatcher with selected fields/mutations and filtered responses | Unknown or unsupported operations often return empty data with HTTP 200; introspection is selective | High | `internal/graphql/handler.go`, `gql_queries.go`, `gql_mutations.go` |

## Issues

| Endpoint | Expected GitHub behavior | Current behavior | Gap | Priority | Status / Tests |
|---|---|---|---|---|---|
| `GET /repos/{owner}/{repo}/issues` ([docs][issues-list-for-repo]) | Returns issues and pull requests by default; supports state, labels, assignee, creator, mentioned, milestone, since, sort, direction | Returns issues plus PRs; supports most listed filters, including `mentioned` across issue and PR title/body/comment text with GitHub-style mention-token boundaries | Sort/filter semantics are simpler than GitHub for edge cases | High | OK/PARTIAL: `TestCompat_IssueList_IncludesPullRequests`, `TestCompat_IssueList_MentionedIncludesPullRequests`, `TestCompat_IssueList_MentionedUsesMentionTokenBoundaries` |
| `GET /repos/{owner}/{repo}/issues/{number}` ([docs][issues-get]) | Returns an issue; if the number is a PR, returns an issue-shaped PR | Falls back to PR when issue lookup fails | None for common clients | High | OK: existing tests |
| `POST /repos/{owner}/{repo}/issues` ([docs][issues-create]) | Create issue with title/body/labels/assignees/milestone | Supported, including deprecated singular `assignee` | Labels that fail to resolve may be logged and skipped in service layer rather than always producing GitHub-identical validation | High | OK/PARTIAL: `TestCompat_IssueCREATE_SingularAssignee` |
| `PATCH /repos/{owner}/{repo}/issues/{number}` ([docs][issues-update]) | Partial update; labels and assignees replace when provided | Supported for issues and issue-shaped PR fallback | Error bodies/statuses may differ for invalid labels, assignees, and state transitions | High | OK/PARTIAL: `TestCompat_IssuePATCH_*` |
| `GET /repos/{owner}/{repo}/issues/comments` ([docs][issues-list-comments-for-repo]) | Lists all issue comments in repo with pagination/since/sort/direction | Supported | None known for common CLI usage | Medium | OK: `TestListRepoIssueComments` |
| `GET/PATCH/DELETE /repos/{owner}/{repo}/issues/comments/{comment_id}` ([get][issues-get-comment], [update][issues-update-comment], [delete][issues-delete-comment]) | Get, update, delete issue comment | Supported | Fine-grained permissions and exact error payloads may differ | Medium | OK/PARTIAL: existing comment tests |
| `PUT/DELETE /repos/{owner}/{repo}/issues/comments/{comment_id}/pin` ([pin][issues-pin-comment], [unpin][issues-unpin-comment]) | `PUT` pins; `DELETE` unpins | `PUT` pins; `DELETE` unpins | None known for common clients | High | OK: `TestIssueCommentPinEndpoints` |
| `GET /repos/{owner}/{repo}/issues/events` and `GET /repos/{owner}/{repo}/issues/events/{event_id}` ([list][issues-list-events-for-repo], [get][issues-get-event]) | Repo-wide issue event listing and event lookup | Not routed | Missing repo-wide event APIs | Medium | GAP |
| `GET /repos/{owner}/{repo}/issues/{number}/events` ([docs][issues-list-events]) | Issue event listing | Supported | Event types are limited to modeled issue lifecycle | Medium | PARTIAL |
| `GET /repos/{owner}/{repo}/issues/{number}/timeline` ([docs][issues-list-timeline]) | Timeline feed with many event/item types | Supported | Timeline item coverage is narrower than GitHub | Medium | PARTIAL |
| `GET /repos/{owner}/{repo}/issues/{number}/assignees/{assignee}` ([docs][issues-check-user-can-be-assigned-to-issue]) | Check whether a user can be assigned | Not routed | Missing check endpoint | Low | GAP |
| Issue dependencies, sub-issues, parent, issue fields ([dependencies][issue-dependencies-docs], [sub-issues][sub-issues-docs], [field values][issue-field-values-docs]) | GitHub exposes issue hierarchy/dependencies/issue-field endpoints | Not routed | Missing modern GitHub issue planning APIs | Low | GAP |
| Reactions on issues and issue comments ([issues][reactions-list-for-issue], [comments][reactions-list-for-issue-comment]) | List/create/delete reactions, with media-type semantics on GitHub | Supported for issues and issue comments | GraphQL reaction mutations are minimal; REST media-type previews are not enforced | Low | PARTIAL |
| Local extensions | None on GitHub | No extension issue presence, typing, read-state, unread-count, or attachment REST APIs are routed | Historical local collaboration and attachment issue APIs have been removed from the published API surface | N/A | Removed |

## Pull Requests

| Endpoint | Expected GitHub behavior | Current behavior | Gap | Priority | Status / Tests |
|---|---|---|---|---|---|
| `GET /repos/{owner}/{repo}/pulls` ([docs][pulls-list]) | List PRs by state, head, base, sort, direction | Supported for state/head/base/sort/direction | `popularity` and `long-running` sort are approximated by created timestamp | High | OK/PARTIAL: `TestPRHandlers_ListPRs_BaseHeadFilters` |
| `GET /repos/{owner}/{repo}/pulls/{number}` ([docs][pulls-get]) | Full PR object; supports diff media type | Supported; `Accept: diff` supported | Exact mergeability/check fields are local approximations | High | OK/PARTIAL: `TestCompat_PRGet_*` |
| `POST /repos/{owner}/{repo}/pulls` ([docs][pulls-create]) | Create PR with draft and cross-repo head support | Supported | Permission and validation errors do not exactly match GitHub | High | OK/PARTIAL: existing tests |
| `PATCH /repos/{owner}/{repo}/pulls/{number}` ([docs][pulls-update]) | Partial update | Supported | Some fields remain unmodeled | High | OK: existing tests |
| `PUT /repos/{owner}/{repo}/pulls/{number}/update-branch` ([docs][pulls-update-branch]) | Merge base into head with optional `expected_head_sha` | Supported with merge update | Rebase is GraphQL-only locally, matching REST's merge-only endpoint | Medium | OK: `TestPRHandlers_UpdateBranch` |
| `GET/PUT /repos/{owner}/{repo}/pulls/{number}/merge` ([check][pulls-check-merged], [merge][pulls-merge]) | Check merge state and merge PR | Supported | Merge conflict/invalid-state status mapping may differ | Medium | PARTIAL: `TestCompat_PRMerge_ResponseShape` |
| `GET /repos/{owner}/{repo}/pulls/{number}/commits` and `/files` ([commits][pulls-list-commits], [files][pulls-list-files]) | List commits and changed files on a PR | Supported | Commit/file response shapes and pagination are simpler than GitHub | Medium | PARTIAL |
| GraphQL auto-merge ([enable][graphql-enable-auto-merge], [disable][graphql-disable-auto-merge]) | GitHub can queue or cancel auto-merge when the repository allows it; queued merge should honor branch protection and optional `expectedHeadOid` | Local supports `enablePullRequestAutoMerge` and `disablePullRequestAutoMerge`; queued merges run asynchronously when workflow runs or commit statuses satisfy the same merge policy as manual merges | Auto-merge settings are configured through REST `allow_auto_merge`; branch-protection bypass is configured only through REST branch protection, not GraphQL | High | OK/PARTIAL: `TestGraphQL_EnablePullRequestAutoMerge_*`, `TestAutoMerge_BranchProtectionWithoutBypassKeepsPROpen`, `TestAutoMerge_ExpectedHeadSHAMismatchBlocksMerge` |
| PR review lifecycle ([list][pulls-list-reviews], [create][pulls-create-review], [submit][pulls-submit-review], [update][pulls-update-review], [dismiss][pulls-dismiss-review]) | List/create/submit/get/dismiss/delete reviews; update pending review via `PUT` | Most routes exist, including `PUT` review update | No known method mismatch for update; lifecycle edge semantics may still differ | High | OK/PARTIAL: `TestCompat_PRReviewUpdate_UsesPut` |
| PR review comments ([repo list][pulls-list-review-comments-for-repo], [PR list][pulls-list-review-comments], [create][pulls-create-review-comment], [reply][pulls-create-reply-for-review-comment], [update][pulls-update-review-comment], [reactions][reactions-list-for-pr-review-comment]) | GitHub supports repo-wide list, per-PR list, create/reply/update/delete, and reactions | Local supports per-PR list, create/reply/update/delete, single comment get | Missing repo-wide `GET /pulls/comments`; missing review comment reactions | Medium | PARTIAL |
| Requested reviewers ([docs][pulls-requested-reviewers]) | Add/remove/list requested reviewers and teams | Supported, including no-prefix compatibility routes | Team/user validation is simpler than GitHub | Medium | OK/PARTIAL |
| Draft conversion ([ready][graphql-mark-pull-request-ready], [draft][graphql-convert-pull-request-draft]) | GitHub exposes ready/draft conversion through GraphQL mutations for this compatibility target | Local exposes GraphQL ready/draft mutations and no REST `ready_for_review` shim | REST clients should not assume a local GitHub-shaped ready-for-review endpoint exists | Low | PARTIAL |
| PR archive/codespaces ([archive][pulls-archive], [codespaces][codespaces-create-with-pr]) | GitHub has PR archive and PR codespaces routes | Not routed | Missing low-priority modern endpoints | Low | GAP |
| Local extensions ([GraphQL thread docs][graphql-review-thread]) | None on GitHub REST | REST `resolve`/`unresolve` review comment routes | Thread resolution is a GitHub GraphQL concept; REST routes are local extensions | N/A | Extension |

## Repositories, Branches, Commits, Contents

| Endpoint / Area | Expected GitHub behavior | Current behavior | Gap | Priority | Status / Tests |
|---|---|---|---|---|---|
| `GET /repos/{owner}/{repo}` ([docs][repos-get]) | Rich repo metadata shape with permissions, counts, feature flags, security fields | Supported with computed stats, merge defaults, local feature flags, and disabled security/code-security analysis status fields for admin viewers | Optional fields for security products are status-only; exact GitHub Enterprise feature availability and admin-only visibility semantics are approximated | High | OK/PARTIAL: `TestCompat_RepoGet_ResponseFields` |
| `PATCH /repos/{owner}/{repo}` ([docs][repos-update]) | Update repo and return full repo shape | Supported, including `homepage`, `has_projects`, `has_downloads`, `has_discussions`, and `allow_auto_merge` | Some fields unmodeled; exact validation differs | Medium | OK/PARTIAL: `TestCompat_RepoPATCH_ResponseIncludesStats`, `TestCompat_RepoPATCH_AllowAutoMergeRoundTrip`, `TestCompat_RepoPATCH_HomepageAndFeatureTogglesRoundTrip` |
| `DELETE /repos/{owner}/{repo}` ([docs][repos-delete]) | Delete repo | Supported | None known for local use | Medium | OK |
| `POST /user/repos`, `POST /orgs/{org}/repos` ([user][repos-create-for-authenticated-user], [org][repos-create-in-org]) | Create user/org repo with many optional settings | Supported for core fields plus modeled GitHub options: `homepage`, `has_issues`, `has_projects`, `has_wiki`, `has_downloads`, `has_discussions`, `is_template`, `auto_init`, `license_template`, merge flags, delete-branch-on-merge, and org `visibility` (`public`/`private`) | `team_id`, `custom_properties`, `.gitignore`/license file materialization, and merge commit message defaults remain ignored or unmodeled; validation is simpler than GitHub | High | OK/PARTIAL: `TestCompat_RepoCREATE_ModeledOptionsRoundTrip`, `TestCompat_OrgRepoCREATE_VisibilityRoundTrip` |
| Forks and transfer ([forks][repos-list-forks], [create fork][repos-create-fork], [transfer][repos-transfer]) | Create/list forks; transfer repo | Supported | Async behavior and invitation workflow are simplified | Medium | PARTIAL |
| `POST /repos/{owner}/{repo}/merge-upstream` ([docs][branches-sync-fork]) | Sync a fork branch from upstream | Supported for fast-forwarding from the fork parent when present; non-forks return a no-op success | Merge conflict handling and upstream branch discovery are simplified | Medium | PARTIAL |
| `GET/PUT /repos/{owner}/{repo}/topics` ([get][repos-get-topics], [replace][repos-replace-topics]) | Topic list/replace | Supported | None known | Medium | OK |
| Repository autolinks ([docs][repo-autolinks-docs]) | List/create/get/delete repository autolinks | Supported | Validation and storage are local | Low | OK/PARTIAL |
| `GET /repos/{owner}/{repo}/languages` ([docs][repos-list-languages]) | Byte-count map by language | Returns stored primary language with value `1` | Not a real language byte analysis | Low | PARTIAL |
| `GET /repos/{owner}/{repo}/branches` and `GET /branches/{branch}` ([list][branches-list], [get][branches-get]) | List and fetch real branch refs | Supported from Git store, fallback to default branch with zero SHA | Missing protection details in branch object beyond local transform; fallback can mask git-store failures | Medium | PARTIAL |
| Branch protection tree ([protection][branch-protection-docs], [rename][branches-rename]) | Full branch-protection REST subresource tree | Monolithic `/protection` plus selected subresources for required status checks, contexts, enforce admins, required signatures, required PR reviews, restrictions, and restrictions actor add/set/remove for users/teams/apps | Missing branch rename, signature enforcement, full actor object response shapes, and exact validation/error semantics | High | PARTIAL: `TestBranchProtectionSubresourceRESTContract`, `TestBranchProtectionRestrictionActorMutationSubresources` |
| `GET /repos/{owner}/{repo}/commits` ([docs][commits-list]) | Supports `sha`, `path`, `author`, `committer`, `since`, `until`, pagination | Supports `path` and fixed git limit before pagination | Most filters are missing/ignored | Medium | PARTIAL |
| `GET /repos/{owner}/{repo}/commits/{ref}` ([commit][commits-get], [comments][commit-comments-list], [PRs][commits-list-prs], [branches][commits-list-branches]) | Commit details or diff | Supported with file stats/patches when available | Missing commit comments, branches-where-head, commit-to-PR lookup | Medium | PARTIAL |
| `GET /repos/{owner}/{repo}/compare/{basehead}` ([docs][commits-compare]) | Compare refs; missing refs should error | Supported with real git diff when possible | Missing refs can return a 200 empty compare response instead of GitHub error | Medium | PARTIAL |
| `GET /repos/{owner}/{repo}/contributors` ([docs][repos-list-contributors]) | Contributor summary from git history with anonymous filtering options | Supported from git contributor data | Query options and exact aggregation differ | Low | PARTIAL |
| `GET/PUT/DELETE /repos/{owner}/{repo}/contents/{path}` ([get][contents-get], [put][contents-create-or-update], [delete][contents-delete]) | Full contents API, links/download URLs, symlink/submodule handling, optimistic SHA concurrency | File/dir read, create/update/delete with SHA validation and GitHub-style `url`/`git_url`/`html_url`/`download_url`/`_links` fields for common file and directory responses | Symlink/submodule typing and full commit response payload details remain partial | High | OK/PARTIAL: `ContentsSHAValidation`, `ContentsDirectoryListing` |
| `GET /repos/{owner}/{repo}/readme` ([repo root][contents-get-readme], [directory][contents-get-readme-in-directory]) | README contents object, optional `ref` | Supported for common README names | `/readme/{dir}` is missing; response shape is minimal | Medium | PARTIAL |
| Tags and archives ([tags][repos-list-tags], [zipball][contents-download-zipball], [tarball][contents-download-tarball], [git tags][git-create-tag]) | GitHub has `GET /tags`, `/zipball/{ref}`, `/tarball/{ref}`, plus Git Database tag object APIs | `GET /tags`, local `POST /tags`, release archive helpers, and Git Database tags are implemented | Official `/zipball/{ref}` and `/tarball/{ref}` are missing; local `POST /tags` is not GitHub REST | Medium | PARTIAL/Extension |
| Collaborators, invitations, and assignees ([collaborators][collaborators-list], [permission][collaborators-permission], [invitations][repo-invitations-docs], [assignees][assignees-list]) | List/check collaborators, get permission, add/remove collaborators, list invitations, list/check assignees | List/add/remove collaborators, list repository invitations, and list assignees | Missing `GET /collaborators/{username}`, `/permission`, and `GET /assignees/{assignee}` | Medium | GAP/PARTIAL |
| Deploy keys ([list][deploy-keys-list], [create][deploy-keys-create], [get][deploy-keys-get], [delete][deploy-keys-delete]) | List/create/get/delete deploy keys | List/create/delete only | `GET /repos/{owner}/{repo}/keys/{key_id}` is missing | Low | GAP |
| Community, traffic, stats, subscribers, subscriptions, vulnerability-alert toggles ([community][community-profile], [traffic][traffic-docs], [stats][stats-docs], [watching][watching-docs], [alerts][vulnerability-alerts-docs]) | GitHub exposes many repo metadata/admin APIs | Not routed | Missing broad non-CLI repository surface | Low | GAP |

## Git Database

| Endpoint | Expected GitHub behavior | Current behavior | Gap | Priority | Status / Tests |
|---|---|---|---|---|---|
| `GET/POST /repos/{owner}/{repo}/git/blobs` ([get][git-get-blob], [create][git-create-blob]) | Base64/UTF-8 blob create and blob fetch | Supported | Exact error payloads differ | High | OK: `TestGitHandlers/GitDatabaseCreateBlobAndTree_Issue1292` |
| `GET/POST /repos/{owner}/{repo}/git/trees` ([get][git-get-tree], [create][git-create-tree]) | Tree fetch/create with `base_tree`, inline content, and deletion | Supported | None known for core behavior | High | OK |
| `GET/POST /repos/{owner}/{repo}/git/tags` ([get][git-get-tag], [create][git-create-tag]) | Annotated tag object fetch/create; ref creation is separate | Supported | None known for core behavior | High | OK |
| `GET/POST /repos/{owner}/{repo}/git/commits` ([get][git-get-commit], [create][git-create-commit]) | Commit object fetch/create | Supported | Commit verification is local and limited | High | PARTIAL |
| `GET/POST/PATCH/DELETE /repos/{owner}/{repo}/git/ref(s)` ([get][git-get-ref], [create][git-create-ref], [update][git-update-ref], [delete][git-delete-ref]) | Ref create/fetch/update/delete, including namespaced refs | Supported, including singular `ref` compatibility | Exact fast-forward and error semantics may differ | High | OK/PARTIAL |
| `GET /repos/{owner}/{repo}/git/matching-refs/{ref}` ([docs][git-list-matching-refs]) | List matching refs | Supported | None known for common use | Medium | OK |

## Search

| Endpoint | Expected GitHub behavior | Current behavior | Gap | Priority | Status / Tests |
|---|---|---|---|---|---|
| `GET /search/repositories` ([docs][search-repositories]) | `q` required; rich qualifiers; sort/order for stars/forks/help-wanted-updated; items include `score` | `q` required; many qualifiers parsed; `score` present; REST `sort`/`order` query params work for local star/fork/update/create/pushed sorts | Help-wanted sort and some qualifier coverage remain incomplete | High | OK/PARTIAL: `TestCompat_SearchRepos_ResponseShape`, `TestSearchReposStarsForksLanguageLicenseSort` |
| `GET /search/issues` ([docs][search-issues]) | Searches issues and PRs with rich qualifiers/sort/order | Supported with hybrid issue/PR search and many qualifiers | Some qualifiers are parsed but not enforced; sort/order differs for edge cases | High | PARTIAL: `TestCompat_SearchIssues_ResponseShape` |
| `GET /search/commits` ([docs][search-commits]) | Commit search with commit-specific qualifiers; preview media type historically required | Supported | Preview media type not enforced; qualifier coverage is partial | Medium | PARTIAL |
| `GET /search/code` ([docs][search-code]) | Code search with repo/path/language/extension/filename qualifiers and text matching | Supported through git search across repos | Negated qualifiers are ignored; sort/order and advanced code search semantics are missing | Medium | PARTIAL |
| `GET /search/labels` ([docs][search-labels]) | Search labels by `q` within `repository_id`; returns search envelope with scored label items | Supported for `q`, `repository_id`, pagination, and basic created/updated sort | Text-match metadata and exact ranking differ | High | OK/PARTIAL: `TestCompat_SearchLabels_ResponseShape` |
| `GET /search/users` ([docs][search-users]) | Search users and organizations by `q`; supports text, `type`, `in`, count/date qualifiers, sort/order, and scored user items | Supported for text, `type:user`/`type:org`, `in`, pagination, `joined` sort, and scored user items | Followers/repositories/language/location/created qualifiers and exact ranking differ | High | OK/PARTIAL: `TestCompat_SearchUsers_ResponseShape` |
| `GET /search/topics` ([docs][search-topics]) | Search topics by `q`; returns topic items with metadata and score | Supported from repository topics for text, `repositories`, pagination, and scored topic items | Curated/featured metadata, aliases/related topics, exact ranking, and topic descriptions are repository-derived or empty | High | OK/PARTIAL: `TestCompat_SearchTopics_ResponseShape` |

## Releases

| Endpoint | Expected GitHub behavior | Current behavior | Gap | Priority | Status / Tests |
|---|---|---|---|---|---|
| `GET/POST /repos/{owner}/{repo}/releases` ([list][releases-list], [create][releases-create]) | List/create releases | Supported | `make_latest`, discussion category, immutable releases, and exact validation are unmodeled | High | OK/PARTIAL: `compat_release_test.go` |
| `GET /releases/latest`, `GET/HEAD /releases/tags/{tag}` ([latest][releases-latest], [tag][releases-by-tag]) | Fetch latest/by tag | Supported | Latest semantics depend on local service ordering | Medium | OK/PARTIAL |
| `GET/PATCH/DELETE /releases/{release_id}` ([get][releases-get], [update][releases-update], [delete][releases-delete]) | Fetch/update/delete release | Supported | Some fields unmodeled | Medium | OK/PARTIAL |
| `POST /releases/generate-notes` ([docs][releases-generate-notes]) | Generate release notes from tags/commits | Supported | Notes are simpler than GitHub's generated release notes | Medium | PARTIAL |
| Release assets ([list][release-assets-list], [upload][release-assets-upload], [get][release-assets-get], [update][release-assets-update], [delete][release-assets-delete]) | Upload/list/get/delete/update asset; browser download URL serves bytes | Upload/list/get/delete and local `/download` route | `PATCH /releases/assets/{asset_id}` is missing; `browser_download_url` points at API URL instead of a stable asset download URL | Medium | PARTIAL/GAP |
| Archives ([zipball][contents-download-zipball], [tarball][contents-download-tarball]) | `GET /zipball/{ref}` and `/tarball/{ref}` | Local archive-by-tag and release archive routes | Official zipball/tarball endpoints are missing | Medium | GAP/PARTIAL |
| Release reactions / immutable releases ([reactions][reactions-list-for-release], [immutable releases][releases-docs]) | Official endpoints exist | Not routed | Missing | Low | GAP |

## Actions, Checks, Statuses, Environments

| Endpoint / Area | Expected GitHub behavior | Current behavior | Gap | Priority | Status / Tests |
|---|---|---|---|---|---|
| Workflows ([list][actions-list-workflows], [get][actions-get-workflow], [dispatch][actions-workflow-dispatch]) | List/get/enable/disable/dispatch workflows; validate `workflow_dispatch` inputs/ref; list runs for a workflow | Supported from repository workflow records/files; `workflow_id` may be numeric or filename; workflow-scoped run listing is routed | Dispatch accepts payload but backend workflow engine is local/mock-oriented; validation is simpler | High | PARTIAL: workflow acceptance tests |
| Workflow runs ([list][actions-list-workflow-runs], [get][actions-get-workflow-run], [rerun][actions-rerun-workflow], [logs][actions-download-run-logs], [jobs][actions-list-jobs-for-run]) | List/get/cancel/delete/rerun/rerun-failed/force-cancel/logs/artifacts/jobs/attempts | Supported subset, including run attempts, job get/logs/rerun, and force-cancel | Attempts collapse to one stored attempt; job rerun/rerun-failed delegate to full-run rerun; filters and run metadata are partial | High | PARTIAL: `compat_workflow_test.go`, `handlers_workflow_jobs.go` |
| Missing Actions run APIs ([workflow runs][actions-workflow-runs-docs], [runners][actions-runners-docs], [permissions][actions-permissions-docs], [OIDC][actions-oidc-docs]) | Approvals, pending deployments, timing, delete logs, workflow timing, permissions, runners, runner groups, OIDC customization | Not routed | Missing broad Actions admin/runtime surface | Medium | GAP |
| Artifacts ([repo list][actions-list-artifacts], [run list][actions-list-run-artifacts], [get][actions-get-artifact], [download][actions-download-artifact], [delete][actions-delete-artifact]) | List repo/run artifacts and download zip | List repo/run artifacts and download zip at `/actions/artifacts/{artifact_id}/zip` | Missing `GET/DELETE /actions/artifacts/{artifact_id}` metadata/delete; download path differs from official archive-format path | Medium | PARTIAL |
| Actions cache ([list][actions-list-caches], [delete by key][actions-delete-cache-by-key], [delete by id][actions-delete-cache-by-id], [usage][actions-cache-usage]) | List/delete caches and usage | Supported | Cache usage is static; retention/storage-limit endpoints missing | Low | PARTIAL |
| Commit statuses ([create][statuses-create], [list][statuses-list], [combined][statuses-combined]) | Create/list/combined status | Supported | Status contexts are local; exact permissions/errors differ | Medium | OK/PARTIAL |
| Check runs/suites ([runs][checks-runs-docs], [suites][checks-suites-docs]) | GitHub supports create/update/rerequest check runs and suites | Local maps workflow jobs/runs to read-only check runs/suites | Create/update/rerequest check APIs are missing | Medium | GAP/PARTIAL |
| Deployments ([deployments][deployments-docs], [statuses][deployment-statuses-docs]) | Create/list deployments and create/list statuses | Supported | Create deployment returns `200` instead of GitHub-created/accepted status; get deployment and get single status endpoints are missing | Medium | PARTIAL |
| Environments ([environments][environments-docs], [branch policies][deployment-branch-policies-docs], [secrets][actions-secrets-docs], [variables][actions-variables-docs]) | GitHub supports list/get/create/update/delete, protection rules, deployment branch policies, env secrets/variables | Local supports list/get/create-update/delete plus env secrets/variables, both by `{owner}/{repo}` and numeric `{repo_id}` compatibility routes | Full parity for all protection rule flavors is still incomplete | Medium | PARTIAL |
| Repository dispatch ([docs][repos-create-dispatch]) | GitHub returns 204 and can trigger workflows | Returns 204 but does not fully feed a workflow engine | Behavior is mostly a no-op | Medium | PARTIAL |

## Secrets and Variables

| Endpoint / Area | Expected GitHub behavior | Current behavior | Gap | Priority | Status / Tests |
|---|---|---|---|---|---|
| Repo actions/dependabot/codespaces secrets ([actions][actions-repo-secrets], [dependabot][dependabot-repo-secrets], [codespaces][codespaces-repo-secrets]) | List, public key, get secret, create/update encrypted secret, delete | List/public-key/get/create-update/delete routed for repo namespaces | Public key is static/local; encryption semantics differ from GitHub libsodium flow | High | OK/PARTIAL: `TestCompat_RepoSecretGet_ByNamespace` |
| Org actions/dependabot/codespaces secrets ([actions][actions-org-secrets], [dependabot][dependabot-org-secrets], [codespaces][codespaces-org-secrets]) | List/public-key/get/create-update/delete; set/list/add/remove selected repositories | List/public-key/get/create-update/delete and bulk selected-repo set/list supported | Per-repository add/remove selected endpoints are missing | Medium | PARTIAL |
| Environment secrets ([docs][actions-environment-secrets]) | List/public-key/get/create-update/delete | List/public-key/create-update/delete by `{owner}/{repo}` and numeric `{repo_id}` compatibility routes | `GET /.../secrets/{secret_name}` is missing | Medium | PARTIAL |
| User codespaces secrets ([docs][codespaces-user-secrets]) | GitHub supports user-level codespaces secrets and selected repositories | List/public-key/get/create-update/delete and selected repository routes are user-scoped | Encryption semantics differ from GitHub libsodium flow; selected repo response shape is minimal | High | OK/PARTIAL: `TestCompat_UserCodespacesSecrets` |
| Repo/org/env variables ([repo][actions-repo-variables], [org][actions-org-variables], [environment][actions-environment-variables]) | List/create/get/update/delete | Supported; environment variables are also routed by numeric `{repo_id}` for `gh variable set --env` compatibility | Org selected-repository variable endpoints are missing | Medium | PARTIAL |
| Secret scanning and private registries ([secret scanning][secret-scanning-docs], [private registries][private-registries-docs]) | Official GitHub security APIs exist | Not routed | Missing | Low | GAP |

## Organizations, Teams, Invitations

| Endpoint / Area | Expected GitHub behavior | Current behavior | Gap | Priority | Status / Tests |
|---|---|---|---|---|---|
| `GET /orgs/{org}` and org repos ([org][orgs-get], [org repos][repos-list-for-org], [create repo][repos-create-in-org]) | Fetch org and list/create org repos | Supported | PATCH/DELETE org are missing | Medium | PARTIAL |
| Org members/memberships ([members][orgs-members-docs], [memberships][orgs-memberships-docs], [blocks][orgs-blocking-docs]) | List members; get/set/delete memberships; delete member; role filters | Supported subset with active/pending membership role normalization and `role=admin/member/all` member filtering | Member check `GET /orgs/{org}/members/{username}`, public members, org blocks, org membership listing are missing | Medium | PARTIAL/GAP |
| Outside collaborators ([docs][orgs-outside-collaborators-docs]) | List and remove outside collaborators; GitHub also add/convert routes | List implemented; remove via org member/collaborator flows | `PUT/DELETE /orgs/{org}/outside_collaborators/{username}` is missing | Low | GAP/PARTIAL |
| Org invitations ([pending][orgs-list-invitations], [create][orgs-create-invitation], [cancel][orgs-cancel-invitation], [user membership][orgs-authenticated-membership]) | List/create/revoke org invitations; user accepts/declines | Supported | Invitation teams lookup and failed invitations missing | Medium | PARTIAL |
| Teams ([teams][teams-docs], [members][teams-members-docs], [repos][teams-repos-docs], [legacy][teams-legacy-docs]) | Org team CRUD, members, invitations, repos | Supported under `/orgs/{org}/teams/{slug}` | Numeric `/teams/{team_id}` legacy routes and team child teams are missing | Medium | PARTIAL |
| Org audit log ([docs][org-audit-log-docs]) | GitHub audit log supports rich filtering and paging | Local audit log route exists | Coverage and event taxonomy are local | Low | PARTIAL |
| Org rulesets ([docs][org-rulesets-docs]) | GitHub supports list/create/get/update/delete/history | Local only supports `GET /orgs/{org}/rulesets/{ruleset_id}` | Org list/create/update/delete/history missing | Medium | GAP/PARTIAL |

## Users, Auth, Keys, Stars, Notifications, Gists

| Endpoint / Area | Expected GitHub behavior | Current behavior | Gap | Priority | Status / Tests |
|---|---|---|---|---|---|
| `GET /user`, `GET /users/{username}` ([authenticated][users-get-authenticated], [public][users-get-by-username]) | Authenticated/private user and public user shapes | Supported | Some optional fields absent | High | OK/PARTIAL: `compat_user_test.go` |
| User/org repo listing ([viewer][repos-list-for-authenticated-user], [user][repos-list-for-user], [org][repos-list-for-org]) | List viewer/user/org repos with filters/sort/type | Supported | Query filters are mostly ignored | Medium | PARTIAL |
| User orgs ([viewer][orgs-list-for-authenticated-user], [public][orgs-list-for-user]) | `GET /user/orgs`; public `GET /users/{username}/orgs` | Viewer orgs supported | Public user org listing missing | Low | GAP/PARTIAL |
| Stars ([viewer][stars-authenticated-docs], [public][stars-public-docs]) | List/check/star/unstar repos; list public stars for a user | Supported for current user and `GET /users/{username}/starred`, including anonymous reads of public starred repos and auth-aware private filtering | Public per-repo star check and sort/direction variants are missing | Medium | PARTIAL: `TestListUserStarredRepos*` |
| SSH keys ([viewer][ssh-keys-authenticated-docs], [public][ssh-keys-public-docs]) | User list/create/get/delete; public user keys | Supported | None known for common use | Medium | OK |
| SSH signing keys ([viewer][ssh-signing-keys-authenticated-docs], [public][ssh-signing-keys-public-docs]) | User list/create/get/delete; public user signing keys | Supported | None known for common use | Medium | OK |
| GPG keys ([viewer][gpg-keys-authenticated-docs], [public][gpg-keys-public-docs]) | List/create/delete and public user list | Supported | `GET /user/gpg_keys/{gpg_key_id}` is missing | Low | GAP/PARTIAL |
| Tokens | GitHub does not expose generic `/user/tokens` REST endpoints | Local list/create/delete token API | Local extension, not GitHub API | N/A | Extension |
| Notifications ([global][notifications-docs], [thread][notification-thread-docs], [repo][repo-notifications-docs]) | List notifications and mark all read | Global `GET/PUT /notifications` is implemented; `GET` supports conditional polling via ETag/`If-None-Match` | Thread get/patch/delete/subscription and repo notifications are missing; polling does not yet mirror GitHub's `Last-Modified`/`X-Poll-Interval` contract | Medium | PARTIAL |
| User events ([docs][events-docs]) | GitHub returns event feeds | Local user event endpoints intentionally return empty arrays | No event model | Low | PARTIAL |
| Gists ([gists][gists-docs], [comments][gist-comments-docs]) | Authenticated list/create/get/update/delete | Supported | Public/starred/user gists, comments, commits, forks, star/unstar, and revision fetch are missing | Medium | PARTIAL/GAP |
| GitHub App installations ([docs][apps-installations-docs]) | GitHub has full App/installation APIs | Local only returns empty `GET /app/installations` | Minimal compatibility stub only | Low | PARTIAL |
| API discovery/meta/rate limit ([root][meta-root], [meta][meta-get], [rate limit][rate-limit-get]) | Rich discovery/meta/rate-limit envelopes | Discovery/meta are minimal/static; rate limit headers/body are local | Static/minimal metadata | Medium | PARTIAL |
| OAuth/OIDC/connected login/agents ([OAuth apps][oauth-apps-docs], [device flow][oauth-device-flow-docs]) | GitHub OAuth/device flow uses GitHub identity; GitHub has no OIDC, connected-login, or agent binding routes | Local has GitHub-like OAuth/device flow with PKCE authorization-code exchange, generic OIDC, connected-login callbacks, agent registration, invite/bind grants, bound-agent rename, token rotation, and renewable switch sessions | Auth model intentionally diverges | N/A | Extension |

## Webhooks, Dependabot, Rulesets, Pages, Templates

| Endpoint / Area | Expected GitHub behavior | Current behavior | Gap | Priority | Status / Tests |
|---|---|---|---|---|---|
| Repository webhooks ([webhooks][webhooks-docs], [config][webhook-config-docs], [deliveries][webhook-deliveries-docs]) | CRUD hooks, config get/update, ping/test, deliveries/redelivery | CRUD and deliveries/redelivery supported | Config endpoints, ping, and test routes are missing; delivery/payload semantics are local | Medium | PARTIAL |
| Dependabot alerts ([repo][dependabot-alerts-repo], [org][dependabot-alerts-org], [enterprise][dependabot-alerts-enterprise]) | Repo/org/enterprise alerts with filters/sort and dismiss/reopen | Repo list/get/update supported | Query filters ignored; org/enterprise alert endpoints missing | Medium | PARTIAL |
| Dependabot secrets ([repo][dependabot-repo-secrets], [org][dependabot-org-secrets]) | Same caveats as secrets above | Repo/org namespaced secret routes exist | Selected repo single add/remove and exact encryption semantics missing | Medium | PARTIAL |
| Repository rulesets ([rulesets][repo-rulesets-docs], [branch rules][repo-branch-rules], [rule suites][repo-rule-suites]) | List/create/get/update/delete/history/rule suites | List/create/get plus branch rule check | Update/delete/history/rule suites missing; branch rule evaluation is basic | Medium | PARTIAL/GAP |
| Pages ([site][pages-site-docs], [builds][pages-builds-docs], [deployments][pages-deployments-docs], [health][pages-health-docs]) | Get/create/update/delete Pages, list/create builds, build details, deployments, health | Get/create/update/delete and list/create builds | Latest/single build, deployments, health endpoints missing | Medium | PARTIAL |
| Licenses ([list][licenses-list], [get][licenses-get]) | GitHub has full license catalog | Local embeds MIT, Apache-2.0, GPL-3.0 | Catalog is tiny | Low | PARTIAL |
| Gitignore templates ([list][gitignore-list], [get][gitignore-get]) | Official template catalog | Local embedded templates | Catalog depends on local embedded files | Low | PARTIAL |

## GraphQL

| Area | Expected GitHub behavior | Current behavior | Gap | Priority | Status / Tests |
|---|---|---|---|---|---|
| Transport ([docs][graphql-docs]) | `/graphql` endpoint with schema-backed execution and standard GraphQL errors | `/api/graphql` and `/graphql` endpoints with token auth; route by parsed fields/operation names | Not a general GraphQL executor | High | PARTIAL |
| Introspection ([schema][graphql-schema-docs], [introspection][graphql-introspection-docs]) | Full schema introspection | `__type` returns selected field/enum lists for feature detection | Full introspection queries are unsupported | High | PARTIAL |
| Queries ([queries][graphql-queries-docs], [objects][graphql-objects-docs], [search][graphql-search-query]) | Viewer, repository owner, repo, issue, PR, labels, milestones, releases, search, projects, rulesets, Dependabot subset | Supported subset for `gh` workflows | Unsupported fields can be omitted or return empty data | High | PARTIAL |
| Mutations ([docs][graphql-mutations-docs]) | Broad GitHub mutation surface | Supports selected issue, PR, repo, project, milestone, git database, labels, assignees, Dependabot, reaction, and lock mutations | Many GitHub mutations are missing; some reaction mutations are minimal; project team linking explicitly unsupported | High | PARTIAL |
| Error semantics ([errors][graphql-errors-docs], [validation][graphql-validation-docs]) | GraphQL errors with paths/types and schema validation | Mixed: some `errResp`, some graceful empty payloads | Clients that rely on schema validation or exact error classes may misbehave | Medium | PARTIAL |

## Extension Route Index

This is the canonical route index for the extension product APIs summarized in
the agent-native capability map. Historical `/api/v3` variants are no longer
routed; extension-aware callers must use `/api/ext/v1`:

| Area | Routes |
|---|---|
| Agents | `/api/ext/v1/agents`, `/api/ext/v1/agent-invites`, `/api/ext/v1/agent-bindings/confirm`, `PATCH /api/ext/v1/agent-bindings/{agent_login}`, `/api/ext/v1/agent-bindings/{agent_login}/reset-token`, `/api/ext/v1/agent-bindings/{agent_login}/switch-session`, `/api/ext/v1/agent-bindings/{agent_login}/refresh-session`, `/api/ext/v1/user/agents` |
| OAuth / OIDC / connected login | `/api/ext/v1/oauth/device/approve`, `/api/ext/v1/oauth/device/reject`, `/api/ext/v1/oidc/device/code`, `/api/ext/v1/oidc/session`, `/api/ext/v1/oidc/callback`, `/api/ext/v1/oidc/lookup`, `/auth/connected/login`, `/auth/connected/callback` |
| Analytics | `/api/ext/v1/analytics/events` |
| Wiki | `/api/ext/v1/repos/{owner}/{repo}/wiki/state`, `/api/ext/v1/repos/{owner}/{repo}/wiki/tree`, `/api/ext/v1/repos/{owner}/{repo}/wiki/catalog`, `/api/ext/v1/repos/{owner}/{repo}/wiki/reconcile/request`, `/api/ext/v1/repos/{owner}/{repo}/wiki/reconcile`, `/api/ext/v1/repos/{owner}/{repo}/wiki/compact`, `/api/ext/v1/repos/{owner}/{repo}/wiki/compact/{jobID}`, `/api/ext/v1/repos/{owner}/{repo}/wiki/pages...`, `/api/ext/v1/repos/{owner}/{repo}/wiki/search`, `/api/ext/v1/repos/{owner}/{repo}/wiki/move`, `/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}/move`, `/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}/backlinks`, `/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}/history`, `/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}/labels...`, `/api/ext/v1/admin/wiki/repos/{owner}/{repo}/repair-locks` |
| Aggregates | `/api/ext/v1/viewer/summary`, `/api/ext/v1/orgs/{org}/management-summary`, `/api/ext/v1/repos/{owner}/{repo}/summary`, `/api/ext/v1/repos/{owner}/{repo}/issues/{number}/thread`, `/api/ext/v1/repos/{owner}/{repo}/wiki/pages/batch`, `/api/ext/v1/notifications/summary` |
| Org bootstrap | `POST /api/ext/v1/user/orgs` |
| Repo team sharing | `/api/ext/v1/repos/{owner}/{repo}/team-sharing/enable` |
| Local token management | `/api/ext/v1/user/tokens` |

## Remaining Gap Summary

- Tighten search qualifier, ordering, and ranking parity across the implemented search endpoints.
- Continue branch protection endpoint parity beyond the current monolithic route and selected subresources.
- Expand deeper contents edge cases, commits, and release-asset response shapes for broader GitHub-compatible clients.
- Revisit Actions, secrets, variables, and environments: many routes exist, but several are thin or locally mocked.
- Treat GraphQL as a lightweight compatibility layer unless/until it is backed by a real schema/executor.
- Keep extension APIs clearly namespaced under `/api/ext/v1` and out of
  GitHub compatibility claims.
- Keep the generated `/api/v3/openapi.json` and `/api/ext/v1/openapi.json`
  contracts synchronized with this matrix when API behavior changes.

[actions-cache-usage]: https://docs.github.com/rest/actions/cache#get-github-actions-cache-usage-for-a-repository
[actions-delete-artifact]: https://docs.github.com/rest/actions/artifacts#delete-an-artifact
[actions-delete-cache-by-id]: https://docs.github.com/rest/actions/cache#delete-a-github-actions-cache-for-a-repository-using-a-cache-id
[actions-delete-cache-by-key]: https://docs.github.com/rest/actions/cache#delete-github-actions-caches-for-a-repository-using-a-cache-key
[actions-download-artifact]: https://docs.github.com/rest/actions/artifacts#download-an-artifact
[actions-download-run-logs]: https://docs.github.com/rest/actions/workflow-runs#download-workflow-run-logs
[actions-environment-secrets]: https://docs.github.com/rest/actions/secrets#list-environment-secrets
[actions-environment-variables]: https://docs.github.com/rest/actions/variables#list-environment-variables
[actions-get-artifact]: https://docs.github.com/rest/actions/artifacts#get-an-artifact
[actions-get-workflow]: https://docs.github.com/rest/actions/workflows#get-a-workflow
[actions-get-workflow-run]: https://docs.github.com/rest/actions/workflow-runs#get-a-workflow-run
[actions-list-artifacts]: https://docs.github.com/rest/actions/artifacts#list-artifacts-for-a-repository
[actions-list-caches]: https://docs.github.com/rest/actions/cache#list-github-actions-caches-for-a-repository
[actions-list-jobs-for-run]: https://docs.github.com/rest/actions/workflow-jobs#list-jobs-for-a-workflow-run
[actions-list-run-artifacts]: https://docs.github.com/rest/actions/artifacts#list-workflow-run-artifacts
[actions-list-workflow-runs]: https://docs.github.com/rest/actions/workflow-runs#list-workflow-runs-for-a-repository
[actions-list-workflows]: https://docs.github.com/rest/actions/workflows#list-repository-workflows
[actions-oidc-docs]: https://docs.github.com/rest/actions/oidc
[actions-org-secrets]: https://docs.github.com/rest/actions/secrets#list-organization-secrets
[actions-org-variables]: https://docs.github.com/rest/actions/variables#list-organization-variables
[actions-permissions-docs]: https://docs.github.com/rest/actions/permissions
[actions-repo-secrets]: https://docs.github.com/rest/actions/secrets#list-repository-secrets
[actions-repo-variables]: https://docs.github.com/rest/actions/variables#list-repository-variables
[actions-rerun-workflow]: https://docs.github.com/rest/actions/workflow-runs#re-run-a-workflow
[actions-runners-docs]: https://docs.github.com/rest/actions/self-hosted-runners
[actions-secrets-docs]: https://docs.github.com/rest/actions/secrets
[actions-variables-docs]: https://docs.github.com/rest/actions/variables
[actions-workflow-dispatch]: https://docs.github.com/rest/actions/workflows#create-a-workflow-dispatch-event
[actions-workflow-runs-docs]: https://docs.github.com/rest/actions/workflow-runs
[apps-installations-docs]: https://docs.github.com/rest/apps/apps#list-installations-for-the-authenticated-app
[assignees-list]: https://docs.github.com/rest/issues/assignees#list-assignees
[branch-protection-docs]: https://docs.github.com/rest/branches/branch-protection
[branches-get]: https://docs.github.com/rest/branches/branches#get-a-branch
[branches-list]: https://docs.github.com/rest/branches/branches#list-branches
[branches-rename]: https://docs.github.com/rest/branches/branches#rename-a-branch
[branches-sync-fork]: https://docs.github.com/rest/branches/branches#sync-a-fork-branch-with-the-upstream-repository
[checks-runs-docs]: https://docs.github.com/rest/checks/runs
[checks-suites-docs]: https://docs.github.com/rest/checks/suites
[codespaces-create-with-pr]: https://docs.github.com/rest/codespaces/codespaces#create-a-codespace-from-a-pull-request
[codespaces-org-secrets]: https://docs.github.com/rest/codespaces/organization-secrets#list-organization-secrets
[codespaces-repo-secrets]: https://docs.github.com/rest/codespaces/repository-secrets#list-repository-secrets
[codespaces-user-secrets]: https://docs.github.com/rest/codespaces/secrets#list-secrets-for-the-authenticated-user
[collaborators-list]: https://docs.github.com/rest/collaborators/collaborators#list-repository-collaborators
[collaborators-permission]: https://docs.github.com/rest/collaborators/collaborators#get-repository-permissions-for-a-user
[commit-comments-list]: https://docs.github.com/rest/commits/comments#list-commit-comments
[commits-compare]: https://docs.github.com/rest/commits/commits#compare-two-commits
[commits-get]: https://docs.github.com/rest/commits/commits#get-a-commit
[commits-list]: https://docs.github.com/rest/commits/commits#list-commits
[commits-list-branches]: https://docs.github.com/rest/commits/commits#list-branches-for-head-commit
[commits-list-prs]: https://docs.github.com/rest/commits/commits#list-pull-requests-associated-with-a-commit
[community-profile]: https://docs.github.com/rest/metrics/community#get-community-profile-metrics
[contents-create-or-update]: https://docs.github.com/rest/repos/contents#create-or-update-file-contents
[contents-delete]: https://docs.github.com/rest/repos/contents#delete-a-file
[contents-download-tarball]: https://docs.github.com/rest/repos/contents#download-a-repository-archive-tar
[contents-download-zipball]: https://docs.github.com/rest/repos/contents#download-a-repository-archive-zip
[contents-get]: https://docs.github.com/rest/repos/contents#get-repository-content
[contents-get-readme]: https://docs.github.com/rest/repos/contents#get-a-repository-readme
[contents-get-readme-in-directory]: https://docs.github.com/rest/repos/contents#get-a-repository-readme-for-a-directory
[dependabot-alerts-enterprise]: https://docs.github.com/rest/dependabot/alerts#list-dependabot-alerts-for-an-enterprise
[dependabot-alerts-org]: https://docs.github.com/rest/dependabot/alerts#list-dependabot-alerts-for-an-organization
[dependabot-alerts-repo]: https://docs.github.com/rest/dependabot/alerts#list-dependabot-alerts-for-a-repository
[dependabot-org-secrets]: https://docs.github.com/rest/dependabot/secrets#list-organization-secrets
[dependabot-repo-secrets]: https://docs.github.com/rest/dependabot/secrets#list-repository-secrets
[deploy-keys-create]: https://docs.github.com/rest/deploy-keys/deploy-keys#create-a-deploy-key
[deploy-keys-delete]: https://docs.github.com/rest/deploy-keys/deploy-keys#delete-a-deploy-key
[deploy-keys-get]: https://docs.github.com/rest/deploy-keys/deploy-keys#get-a-deploy-key
[deploy-keys-list]: https://docs.github.com/rest/deploy-keys/deploy-keys#list-deploy-keys
[deployment-branch-policies-docs]: https://docs.github.com/rest/deployments/branch-policies
[deployment-statuses-docs]: https://docs.github.com/rest/deployments/statuses
[deployments-docs]: https://docs.github.com/rest/deployments/deployments
[environments-docs]: https://docs.github.com/rest/deployments/environments
[events-docs]: https://docs.github.com/rest/activity/events
[gist-comments-docs]: https://docs.github.com/rest/gists/comments
[gists-docs]: https://docs.github.com/rest/gists/gists
[git-create-blob]: https://docs.github.com/rest/git/blobs#create-a-blob
[git-create-commit]: https://docs.github.com/rest/git/commits#create-a-commit
[git-create-ref]: https://docs.github.com/rest/git/refs#create-a-reference
[git-create-tag]: https://docs.github.com/rest/git/tags#create-a-tag-object
[git-create-tree]: https://docs.github.com/rest/git/trees#create-a-tree
[git-delete-ref]: https://docs.github.com/rest/git/refs#delete-a-reference
[git-get-blob]: https://docs.github.com/rest/git/blobs#get-a-blob
[git-get-commit]: https://docs.github.com/rest/git/commits#get-a-commit-object
[git-get-ref]: https://docs.github.com/rest/git/refs#get-a-reference
[git-get-tag]: https://docs.github.com/rest/git/tags#get-a-tag
[git-get-tree]: https://docs.github.com/rest/git/trees#get-a-tree
[git-list-matching-refs]: https://docs.github.com/rest/git/refs#list-matching-references
[git-update-ref]: https://docs.github.com/rest/git/refs#update-a-reference
[gitignore-get]: https://docs.github.com/rest/gitignore/gitignore#get-a-gitignore-template
[gitignore-list]: https://docs.github.com/rest/gitignore/gitignore#get-all-gitignore-templates
[gpg-keys-authenticated-docs]: https://docs.github.com/rest/users/gpg-keys#list-gpg-keys-for-the-authenticated-user
[gpg-keys-public-docs]: https://docs.github.com/rest/users/gpg-keys#list-gpg-keys-for-a-user
[graphql-convert-pull-request-draft]: https://docs.github.com/graphql/reference/mutations#converttopullrequestdraft
[graphql-disable-auto-merge]: https://docs.github.com/graphql/reference/mutations#disablepullrequestautomerge
[graphql-docs]: https://docs.github.com/graphql
[graphql-enable-auto-merge]: https://docs.github.com/graphql/reference/mutations#enablepullrequestautomerge
[graphql-errors-docs]: https://docs.github.com/graphql/guides/forming-calls-with-graphql#communicating-with-graphql
[graphql-introspection-docs]: https://docs.github.com/graphql/guides/introduction-to-graphql#discovering-the-graphql-api
[graphql-mark-pull-request-ready]: https://docs.github.com/graphql/reference/mutations#markpullrequestreadyforreview
[graphql-mutations-docs]: https://docs.github.com/graphql/reference/mutations
[graphql-objects-docs]: https://docs.github.com/graphql/reference/objects
[graphql-queries-docs]: https://docs.github.com/graphql/reference/queries
[graphql-review-thread]: https://docs.github.com/graphql/reference/objects#pullrequestreviewthread
[graphql-schema-docs]: https://docs.github.com/graphql/overview/public-schema
[graphql-search-query]: https://docs.github.com/graphql/reference/queries#search
[graphql-validation-docs]: https://docs.github.com/graphql/overview/public-schema
[issue-dependencies-docs]: https://docs.github.com/rest/issues/issue-dependencies
[issue-field-values-docs]: https://docs.github.com/rest/issues/issue-field-values
[issues-check-user-can-be-assigned-to-issue]: https://docs.github.com/rest/issues/assignees#check-if-a-user-can-be-assigned-to-a-issue
[issues-create]: https://docs.github.com/rest/issues/issues#create-an-issue
[issues-delete-comment]: https://docs.github.com/rest/issues/comments#delete-an-issue-comment
[issues-get]: https://docs.github.com/rest/issues/issues#get-an-issue
[issues-get-comment]: https://docs.github.com/rest/issues/comments#get-an-issue-comment
[issues-get-event]: https://docs.github.com/rest/issues/events#get-an-issue-event
[issues-list-comments-for-repo]: https://docs.github.com/rest/issues/comments#list-issue-comments-for-a-repository
[issues-list-events]: https://docs.github.com/rest/issues/events#list-issue-events
[issues-list-events-for-repo]: https://docs.github.com/rest/issues/events#list-issue-events-for-a-repository
[issues-list-for-repo]: https://docs.github.com/rest/issues/issues#list-repository-issues
[issues-list-timeline]: https://docs.github.com/rest/issues/timeline#list-timeline-events-for-an-issue
[issues-pin-comment]: https://docs.github.com/rest/issues/comments#pin-an-issue-comment
[issues-unpin-comment]: https://docs.github.com/rest/issues/comments#unpin-an-issue-comment
[issues-update]: https://docs.github.com/rest/issues/issues#update-an-issue
[issues-update-comment]: https://docs.github.com/rest/issues/comments#update-an-issue-comment
[licenses-get]: https://docs.github.com/rest/licenses/licenses#get-a-license
[licenses-list]: https://docs.github.com/rest/licenses/licenses#get-all-commonly-used-licenses
[meta-get]: https://docs.github.com/rest/meta/meta#get-apiname-meta-information
[meta-root]: https://docs.github.com/rest/meta/meta#github-api-root
[notification-thread-docs]: https://docs.github.com/rest/activity/notifications#get-a-thread
[notifications-docs]: https://docs.github.com/rest/activity/notifications
[oauth-apps-docs]: https://docs.github.com/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps
[oauth-device-flow-docs]: https://docs.github.com/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow
[org-audit-log-docs]: https://docs.github.com/enterprise-cloud@latest/rest/orgs/orgs
[org-rulesets-docs]: https://docs.github.com/rest/orgs/rules
[orgs-authenticated-membership]: https://docs.github.com/rest/orgs/members#get-an-organization-membership-for-the-authenticated-user
[orgs-blocking-docs]: https://docs.github.com/rest/orgs/blocking
[orgs-cancel-invitation]: https://docs.github.com/rest/orgs/members#cancel-an-organization-invitation
[orgs-create-invitation]: https://docs.github.com/rest/orgs/members#create-an-organization-invitation
[orgs-get]: https://docs.github.com/rest/orgs/orgs#get-an-organization
[orgs-list-for-authenticated-user]: https://docs.github.com/rest/orgs/orgs#list-organizations-for-the-authenticated-user
[orgs-list-for-user]: https://docs.github.com/rest/orgs/orgs#list-organizations-for-a-user
[orgs-list-invitations]: https://docs.github.com/rest/orgs/members#list-pending-organization-invitations
[orgs-members-docs]: https://docs.github.com/rest/orgs/members
[orgs-memberships-docs]: https://docs.github.com/rest/orgs/members#list-organization-memberships-for-the-authenticated-user
[orgs-outside-collaborators-docs]: https://docs.github.com/rest/orgs/outside-collaborators
[pages-builds-docs]: https://docs.github.com/rest/pages/pages#list-apiname-pages-builds
[pages-deployments-docs]: https://docs.github.com/rest/pages/pages#create-a-github-pages-deployment
[pages-health-docs]: https://docs.github.com/rest/pages/pages#get-a-dns-health-check-for-github-pages
[pages-site-docs]: https://docs.github.com/rest/pages/pages#get-a-apiname-pages-site
[private-registries-docs]: https://docs.github.com/rest/private-registries/organization-configurations
[pulls-archive]: https://docs.github.com/rest/pulls/pulls#archive-a-pull-request
[pulls-check-merged]: https://docs.github.com/rest/pulls/pulls#check-if-a-pull-request-has-been-merged
[pulls-create]: https://docs.github.com/rest/pulls/pulls#create-a-pull-request
[pulls-create-reply-for-review-comment]: https://docs.github.com/rest/pulls/comments#create-a-reply-for-a-review-comment
[pulls-create-review]: https://docs.github.com/rest/pulls/reviews#create-a-review-for-a-pull-request
[pulls-create-review-comment]: https://docs.github.com/rest/pulls/comments#create-a-review-comment-for-a-pull-request
[pulls-dismiss-review]: https://docs.github.com/rest/pulls/reviews#dismiss-a-review-for-a-pull-request
[pulls-get]: https://docs.github.com/rest/pulls/pulls#get-a-pull-request
[pulls-list]: https://docs.github.com/rest/pulls/pulls#list-pull-requests
[pulls-list-commits]: https://docs.github.com/rest/pulls/pulls#list-commits-on-a-pull-request
[pulls-list-files]: https://docs.github.com/rest/pulls/pulls#list-pull-requests-files
[pulls-list-review-comments]: https://docs.github.com/rest/pulls/comments#list-review-comments-on-a-pull-request
[pulls-list-review-comments-for-repo]: https://docs.github.com/rest/pulls/comments#list-review-comments-in-a-repository
[pulls-list-reviews]: https://docs.github.com/rest/pulls/reviews#list-reviews-for-a-pull-request
[pulls-merge]: https://docs.github.com/rest/pulls/pulls#merge-a-pull-request
[pulls-requested-reviewers]: https://docs.github.com/rest/pulls/review-requests#get-all-requested-reviewers-for-a-pull-request
[pulls-submit-review]: https://docs.github.com/rest/pulls/reviews#submit-a-review-for-a-pull-request
[pulls-update]: https://docs.github.com/rest/pulls/pulls#update-a-pull-request
[pulls-update-branch]: https://docs.github.com/rest/pulls/pulls#update-a-pull-request-branch
[pulls-update-review]: https://docs.github.com/rest/pulls/reviews#update-a-review-for-a-pull-request
[pulls-update-review-comment]: https://docs.github.com/rest/pulls/comments#update-a-review-comment-for-a-pull-request
[rate-limit-get]: https://docs.github.com/rest/rate-limit/rate-limit#get-rate-limit-status-for-the-authenticated-user
[reactions-list-for-issue]: https://docs.github.com/rest/reactions/reactions#list-reactions-for-an-issue
[reactions-list-for-issue-comment]: https://docs.github.com/rest/reactions/reactions#list-reactions-for-an-issue-comment
[reactions-list-for-pr-review-comment]: https://docs.github.com/rest/reactions/reactions#list-reactions-for-a-pull-request-review-comment
[reactions-list-for-release]: https://docs.github.com/rest/reactions/reactions#list-reactions-for-a-release
[release-assets-delete]: https://docs.github.com/rest/releases/assets#delete-a-release-asset
[release-assets-get]: https://docs.github.com/rest/releases/assets#get-a-release-asset
[release-assets-list]: https://docs.github.com/rest/releases/assets#list-release-assets
[release-assets-update]: https://docs.github.com/rest/releases/assets#update-a-release-asset
[release-assets-upload]: https://docs.github.com/rest/releases/assets#upload-a-release-asset
[releases-by-tag]: https://docs.github.com/rest/releases/releases#get-a-release-by-tag-name
[releases-create]: https://docs.github.com/rest/releases/releases#create-a-release
[releases-delete]: https://docs.github.com/rest/releases/releases#delete-a-release
[releases-docs]: https://docs.github.com/rest/releases/releases
[releases-generate-notes]: https://docs.github.com/rest/releases/releases#generate-release-notes-content-for-a-release
[releases-get]: https://docs.github.com/rest/releases/releases#get-a-release
[releases-latest]: https://docs.github.com/rest/releases/releases#get-the-latest-release
[releases-list]: https://docs.github.com/rest/releases/releases#list-releases
[releases-update]: https://docs.github.com/rest/releases/releases#update-a-release
[repo-autolinks-docs]: https://docs.github.com/rest/repos/autolinks#list-all-autolinks-of-a-repository
[repo-branch-rules]: https://docs.github.com/rest/repos/rules#get-rules-for-a-branch
[repo-invitations-docs]: https://docs.github.com/rest/collaborators/invitations#list-repository-invitations
[repo-notifications-docs]: https://docs.github.com/rest/activity/notifications#list-repository-notifications-for-the-authenticated-user
[repo-rule-suites]: https://docs.github.com/rest/repos/rule-suites#list-repository-rule-suites
[repo-rulesets-docs]: https://docs.github.com/rest/repos/rules
[repos-create-dispatch]: https://docs.github.com/rest/repos/repos#create-a-repository-dispatch-event
[repos-create-for-authenticated-user]: https://docs.github.com/rest/repos/repos#create-a-repository-for-the-authenticated-user
[repos-create-fork]: https://docs.github.com/rest/repos/forks#create-a-fork
[repos-create-in-org]: https://docs.github.com/rest/repos/repos#create-an-organization-repository
[repos-delete]: https://docs.github.com/rest/repos/repos#delete-a-repository
[repos-get]: https://docs.github.com/rest/repos/repos#get-a-repository
[repos-get-topics]: https://docs.github.com/rest/repos/repos#get-all-repository-topics
[repos-list-for-authenticated-user]: https://docs.github.com/rest/repos/repos#list-repositories-for-the-authenticated-user
[repos-list-for-org]: https://docs.github.com/rest/repos/repos#list-organization-repositories
[repos-list-for-user]: https://docs.github.com/rest/repos/repos#list-repositories-for-a-user
[repos-list-contributors]: https://docs.github.com/rest/repos/repos#list-repository-contributors
[repos-list-forks]: https://docs.github.com/rest/repos/forks#list-forks
[repos-list-languages]: https://docs.github.com/rest/repos/repos#list-repository-languages
[repos-list-tags]: https://docs.github.com/rest/repos/repos#list-repository-tags
[repos-replace-topics]: https://docs.github.com/rest/repos/repos#replace-all-repository-topics
[repos-transfer]: https://docs.github.com/rest/repos/repos#transfer-a-repository
[repos-update]: https://docs.github.com/rest/repos/repos#update-a-repository
[search-code]: https://docs.github.com/rest/search/search#search-code
[search-commits]: https://docs.github.com/rest/search/search#search-commits
[search-docs]: https://docs.github.com/rest/search/search
[search-issues]: https://docs.github.com/rest/search/search#search-issues-and-pull-requests
[search-labels]: https://docs.github.com/rest/search/search#search-labels
[search-repositories]: https://docs.github.com/rest/search/search#search-repositories
[search-topics]: https://docs.github.com/rest/search/search#search-topics
[search-users]: https://docs.github.com/rest/search/search#search-users
[secret-scanning-docs]: https://docs.github.com/rest/secret-scanning/secret-scanning
[ssh-keys-authenticated-docs]: https://docs.github.com/rest/users/keys#list-public-ssh-keys-for-the-authenticated-user
[ssh-keys-public-docs]: https://docs.github.com/rest/users/keys#list-public-keys-for-a-user
[ssh-signing-keys-authenticated-docs]: https://docs.github.com/rest/users/ssh-signing-keys#list-ssh-signing-keys-for-the-authenticated-user
[ssh-signing-keys-public-docs]: https://docs.github.com/rest/users/ssh-signing-keys#list-ssh-signing-keys-for-a-user
[stars-authenticated-docs]: https://docs.github.com/rest/activity/starring#list-repositories-starred-by-the-authenticated-user
[stars-public-docs]: https://docs.github.com/rest/activity/starring#list-repositories-starred-by-a-user
[stats-docs]: https://docs.github.com/rest/metrics/statistics
[statuses-combined]: https://docs.github.com/rest/commits/statuses#get-the-combined-status-for-a-specific-reference
[statuses-create]: https://docs.github.com/rest/commits/statuses#create-a-commit-status
[statuses-list]: https://docs.github.com/rest/commits/statuses#list-commit-statuses-for-a-reference
[sub-issues-docs]: https://docs.github.com/rest/issues/sub-issues
[teams-docs]: https://docs.github.com/rest/teams/teams
[teams-legacy-docs]: https://docs.github.com/rest/teams/teams#get-a-team-legacy
[teams-members-docs]: https://docs.github.com/rest/teams/members
[teams-repos-docs]: https://docs.github.com/rest/teams/teams#list-team-repositories
[traffic-docs]: https://docs.github.com/rest/metrics/traffic
[users-get-authenticated]: https://docs.github.com/rest/users/users#get-the-authenticated-user
[users-get-by-username]: https://docs.github.com/rest/users/users#get-a-user
[vulnerability-alerts-docs]: https://docs.github.com/rest/repos/repos#check-if-vulnerability-alerts-are-enabled-for-a-repository
[watching-docs]: https://docs.github.com/rest/activity/watching
[webhook-config-docs]: https://docs.github.com/rest/repos/webhooks#get-a-webhook-configuration-for-a-repository
[webhook-deliveries-docs]: https://docs.github.com/rest/repos/webhooks#list-deliveries-for-a-repository-webhook
[webhooks-docs]: https://docs.github.com/rest/repos/webhooks
