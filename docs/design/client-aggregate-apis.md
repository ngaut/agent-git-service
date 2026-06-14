# Design: Client Aggregate APIs

Status: Draft

## Summary

Console and Agent Team Service currently build product views by composing many GitHub-compatible REST calls in the browser or in the local ATS process. That keeps the API surface familiar, but it pushes fan-out, pagination, role normalization, and N+1 joins into clients. This document proposes a small set of generic read aggregation APIs for AGS. The goal is not to create console-only or ATS-only shortcuts; the goal is to expose reusable snapshots that future products can also use.

The current conclusion is that one aggregate endpoint is not enough. `GET /api/v3/orgs/{org}/management-summary` is useful for the console organization page, but the larger wins are viewer bootstrap, issue thread loading, issue list filtering, and explicit wiki page batch reads.

## Goals

- Reduce client-side fan-out for common first-render paths.
- Keep mutations on the existing GitHub-compatible endpoints.
- Keep aggregate responses bounded, paginated, and explicit about included sections.
- Make viewer permissions and row-level capabilities explicit instead of forcing clients to infer them.
- Reuse existing GitHub-compatible response transforms where possible, while allowing custom aggregate envelopes.
- Support console, ATS, and future AGS-backed products with the same endpoints.

## Non-goals

- Replacing the GitHub-compatible REST API.
- Adding new mutation semantics.
- Returning unbounded repository, organization, wiki, or issue history.
- Adding product-specific endpoints such as `/console/...` or `/agent-team/...`.
- Hiding authorization differences. Each section must still obey the same authorization rules as the underlying resource.
- Adding recursive "return every wiki body" APIs. Full wiki body scans should stay outside the default client path.

## Reuse Criteria

An aggregate API should be added only when it is reusable by more than one current product, or when the resource shape is generic enough that the next AGS-backed product is likely to need it. Console-specific and ATS-specific flows should be expressed as generic AGS resource reads: viewer summaries, repository summaries, issue threads, issue list filters, wiki catalogs, and explicit wiki page batches.

This means the API should not expose concepts like "agent profiles", "chat spaces", or "runs" directly. Those are product conventions layered on top of issues and wiki pages. The API can make issue and wiki access efficient, but product interpretation stays in the client.

## Current Client Hotspots

### Console

Console bootstrap currently needs `/user`, `/user/agents`, `/user/orgs`, `/user/repos`, and then `/orgs/{org}/repos` for each organization. This is the broadest fan-out path and affects every signed-in session.

Repository selection currently starts with `/repos/{owner}/{repo}` and then individual pages load labels, issues, wiki data, collaborators, teams, invitations, or organization context later. This is correct but makes page transitions show multiple loading phases.

The organization page composes `/orgs/{org}`, `/orgs/{org}/repos`, `/orgs/{org}/teams`, `/orgs/{org}/members`, `/orgs/{org}/outside_collaborators`, and `/orgs/{org}/invitations`. Team drill-down can add more calls for members, repos, and invitations.

The memories page pages through `/repos/{owner}/{repo}/issues`, then filters pull requests client-side. It often needs labels as a separate request.

The wiki page composes page list/tree/search/detail/history/backlinks/labels depending on the view. The first render needs a catalog-like snapshot more than it needs each detail endpoint.

### Agent Team Service

ATS stores metadata in AGS. `list_agents` calls wiki page list under `agents/`, then reads each agent profile page one by one. That is a clear N+1 pattern and will get worse as teams grow.

ATS chat uses AGS issues as durable chat spaces and issue comments as messages. Opening a chat needs the issue plus comments. Worker processing also repeatedly loads issue plus comments when it sees a mention.

ATS runs are represented as AGS issues. `list_runs` lists all issues and filters `[run]` titles client-side. If a listed issue does not include the body, ATS fetches each run issue again to parse `agent` and `source`.

ATS worker polling uses notifications, then loads issue and comments for each mention notification. It also scans chat issues and comments with a human token to detect mentions. That gives durable behavior, but it is call-heavy.

## Call Chain Notation

Each proposed API below lists current and future call chains for console and ATS:

- "Current console" means the call pattern visible in the current console client.
- "Current ATS" means the call pattern visible in the current Agent Team Service backend or worker.
- "Future console" and "Future ATS" describe how the client should change after the aggregate exists.
- "No first-milestone use" means the endpoint is still generic, but that product should not adopt it until it has a real page or workflow that benefits from it.

## Proposed API Set

### 1. Viewer Summary

```text
GET /api/v3/viewer/summary
```

This endpoint returns the current viewer and the workspace navigation data needed for first render.

Default `include`:

```text
include=user,orgs,repositories,invitations,agent_bindings
```

Optional query parameters:

```text
repo_affiliation=owner,collaborator,organization_member
per_page=100
page=1
```

Response sketch:

```json
{
  "user": { "login": "alice", "type": "User", "user_kind": "human" },
  "organizations": [
    { "login": "acme", "role": "admin", "permissions": { "manage_members": true } }
  ],
  "repositories": [
    {
      "full_name": "acme/metadata",
      "owner": { "login": "acme", "type": "Organization" },
      "permission": "admin",
      "private": true
    }
  ],
  "invitations": {
    "repository_count": 1,
    "organization_count": 0,
    "repository_items": [],
    "organization_items": []
  },
  "agent_bindings": {
    "count": 2,
    "items": []
  },
  "pagination": { "next": null }
}
```

This replaces console bootstrap fan-out and is also useful for any future client that needs a repo picker, workspace switcher, or invitation badge. It should be one of the first endpoints implemented.

Call chain:

- Current console: `AppStore.bootstrap()` -> `GET /user` -> `GET /user/agents` -> `GET /user/orgs` -> `GET /user/repos` -> `GET /orgs/{org}/repos` once per organization. Inbox badges later call `GET /user/repository_invitations` and `GET /user/organization_invitations`.
- Current ATS: login/session creation calls `GET /user`; workspace setup separately probes `GET /repos/{human}/metadata`.
- Future console: `AppStore.bootstrap()` -> `GET /viewer/summary?include=user,orgs,repositories,invitations,agent_bindings`, then only drill down with existing endpoints when the user opens a specific repo, org, or invitation view.
- Future ATS: session bootstrap can use `GET /viewer/summary?include=user,repositories` if it needs a workspace picker or metadata repo status. The minimal token login flow can keep `GET /user` if it only needs identity.

### 2. Repository Summary

```text
GET /api/v3/repos/{owner}/{repo}/summary
```

This endpoint returns a bounded snapshot for entering a repository workspace.

Default `include`:

```text
include=repo,viewer,counts,labels,wiki,agents
```

Optional sections:

```text
include=access,invitations,teams,milestones
```

Response sketch:

```json
{
  "repository": { "full_name": "acme/metadata", "private": true },
  "viewer": {
    "permission": "admin",
    "capabilities": {
      "read": true,
      "write": true,
      "admin": true,
      "manage_access": true
    }
  },
  "counts": {
    "open_issues": 12,
    "wiki_pages": 8,
    "labels": 24,
    "pending_invitations": 1
  },
  "labels": [],
  "wiki": { "root_pages": [], "updated_at": "2026-06-12T00:00:00Z" },
  "agents": { "bound_count": 1, "items": [] }
}
```

This endpoint is useful but less urgent than viewer bootstrap and issue/wiki aggregates. It becomes valuable when console wants smoother repo transitions without loading every repo subview.

Call chain:

- Current console: `setRepo(owner, repo)` -> `GET /repos/{owner}/{repo}`. Individual pages then load labels, issues, wiki metadata, repo collaborators, repo invitations, and organization context through separate calls.
- Current ATS: `workspace()` and `bootstrap_workspace()` probe or create the metadata repository with `GET /repos/{human}/metadata` and `POST /user/repos`; other ATS reads go directly to issues and wiki pages.
- Future console: repo switch -> `GET /repos/{owner}/{repo}/summary?include=repo,viewer,counts,labels,wiki,agents`. Detail views still call dedicated issue, wiki, or access endpoints when opened.
- Future ATS: metadata workspace status can call `GET /repos/{human}/metadata/summary?include=repo,viewer,counts,wiki` if the UI wants a richer readiness check. No first-milestone use is required for worker execution.

### 3. Organization Management Summary

```text
GET /api/v3/orgs/{org}/management-summary
```

This endpoint returns the first-render state for organization management: organization profile, viewer role, repositories, members, pending invitations, teams, outside collaborators, and row-level capabilities.

Default `include`:

```text
include=repos,members,invitations,teams,outside_collaborators
```

Optional query parameters:

```text
per_page=50
team_detail_limit=20
include=audit
audit_limit=20
```

Role semantics should be GitHub-compatible at the REST boundary:

- Organization membership role: `admin` or `member`.
- Organization invitation role: `admin` or `member`.
- Team membership role: `maintainer` or `member`.
- Repository permission: `admin`, `maintain`, `write`, `triage`, `read`, or `none`.

This endpoint is still worth implementing for console, but it is not the only aggregation surface. It should be treated as one P0 item because the organization page is currently slow and role inference has caused correctness bugs.

Call chain:

- Current console: `loadOrgContext(ownerLogin)` -> parallel `GET /orgs/{org}`, `GET /orgs/{org}/repos`, `GET /orgs/{org}/teams`, `GET /orgs/{org}/members`, `GET /orgs/{org}/outside_collaborators`, and `GET /orgs/{org}/invitations`. Team detail pages may add `GET /orgs/{org}/teams/{team_slug}`, `/members`, `/repos`, and `/invitations`.
- Current ATS: no first-class organization management view. Agent binding uses repo grants against the human metadata repo and does not need the organization management snapshot.
- Future console: organization page first render -> `GET /orgs/{org}/management-summary`. Mutations still use existing membership, invitation, team, and repo grant endpoints, then refetch the summary or patch local state.
- Future ATS: no first-milestone use. A future team-admin product can reuse this endpoint when it needs an AGS organization management screen.

### 4. Issue Thread

```text
GET /api/v3/repos/{owner}/{repo}/issues/{issue_number}/thread
```

This endpoint returns an issue and its comments in one authorization-checked response.

Default `include`:

```text
include=issue,comments,viewer
```

Optional query parameters:

```text
comments_per_page=100
comments_page=1
comment_sort=created
comment_direction=asc
```

Response sketch:

```json
{
  "issue": { "number": 1, "title": "[chat] general", "state": "open" },
  "comments": [
    { "id": 101, "body": "hello", "user": { "login": "alice" } }
  ],
  "viewer": {
    "capabilities": {
      "comment": true,
      "edit_own_comments": true,
      "delete_own_comments": true
    }
  },
  "pagination": { "comments_next": null }
}
```

This is generic and high value. Console memory detail and ATS chat both need issue plus comments. The ATS worker also needs this when handling notifications or chat mentions.

Call chain:

- Current console: issue or memory detail views load `GET /repos/{owner}/{repo}/issues/{number}` and `GET /repos/{owner}/{repo}/issues/{number}/comments` separately when they need full thread context.
- Current ATS: opening chat messages uses `GET /repos/{owner}/{repo}/issues/{number}/comments`. Worker notification handling calls `GET /repos/{owner}/{repo}/issues/{number}` and then `GET /repos/{owner}/{repo}/issues/{number}/comments`. Chat mention scanning also loads issue plus comments for each chat issue.
- Future console: memory detail -> `GET /repos/{owner}/{repo}/issues/{number}/thread?include=issue,comments,viewer`.
- Future ATS: chat open and worker source loading -> `GET /repos/{owner}/{repo}/issues/{number}/thread?include=issue,comments,viewer`. The worker can then build prompts without two separate network round trips.

### 5. Issue List Filters And Lightweight Views

This can be implemented as extensions to the existing list endpoint rather than a new endpoint:

```text
GET /api/v3/repos/{owner}/{repo}/issues?kind=issue&title_prefix=%5Bchat%5D&include=body&fields=number,title,state,body,updated_at
```

Useful additions:

- `kind=issue|pull|all` so clients do not fetch PRs only to filter them out.
- `title_prefix=` for clients that encode typed records in issue titles, such as `[chat]`, `[run]`, `[incident]`, or `[decision]`.
- `include=body` so clients can parse lightweight issue metadata without fetching every issue.
- `fields=` to keep list responses small when only metadata is needed.

This is more general than an ATS-specific `/chats` or `/runs` API. It benefits console memory lists, ATS chat lists, ATS run lists, and any future product that stores typed records as issues.

Call chain:

- Current console: `loadRepoMemories()` repeatedly calls `GET /repos/{owner}/{repo}/issues?state=all&per_page=100&page=N`, then filters out pull requests client-side. Labels are loaded through `GET /repos/{owner}/{repo}/labels` when needed.
- Current ATS: `list_chats()` calls `GET /repos/{human}/metadata/issues?state=all` and filters titles starting with `[chat]`. `list_runs()` calls the same all-issues list, filters `[run]`, and may call `GET /repos/{human}/metadata/issues/{number}` per run if the body is absent.
- Future console: memories list -> `GET /repos/{owner}/{repo}/issues?kind=issue&fields=number,title,state,user,labels,updated_at&state=all`. Detail still uses issue thread.
- Future ATS: chat list -> `GET /repos/{human}/metadata/issues?kind=issue&title_prefix=%5Bchat%5D&fields=number,title,state,updated_at`. Run list -> `GET /repos/{human}/metadata/issues?kind=issue&title_prefix=%5Brun%5D&include=body&fields=number,title,state,body,updated_at`.

### 6. Wiki Page Batch Read

```text
POST /api/v3/repos/{owner}/{repo}/wiki/pages/batch
```

This endpoint fetches explicitly selected wiki pages. It does not recursively expand a path and it does not mean "return every wiki page body". Callers must first choose a bounded set of slugs from existing list, tree, or search endpoints.

Request sketch:

```json
{
  "slugs": ["docs/getting-started", "agents/python-backend"],
  "include": ["body", "labels", "backlink_count"],
  "body_limit": 20000,
  "ref": "main"
}
```

Response sketch:

```json
{
  "items": [
    {
      "slug": "docs/getting-started",
      "title": "Getting Started",
      "updated_at": "2026-06-12T00:00:00Z",
      "body": "# Getting Started\n",
      "body_truncated": false,
      "labels": []
    }
  ],
  "missing": ["agents/python-backend"],
  "limits": {
    "max_slugs": 50,
    "body_limit": 20000
  }
}
```

Bounds:

- Reject empty `slugs`.
- Default `max_slugs` should be 50, with a hard maximum of 100.
- Default `body_limit` should be conservative. Large bodies must return `body_truncated: true`.
- No path recursion inside this endpoint. Path recursion stays in `GET /wiki/pages` and `GET /wiki/tree`, which return metadata by default.

This is reusable across products. Console can batch-load selected wiki pages from a tree or search result. ATS can batch-load selected metadata records after listing their slugs. Future products can use the same shape for structured wiki-backed records.

Call chain:

- Current console: wiki views use `GET /wiki/pages`, `GET /wiki/tree`, or `GET /wiki/search` to discover pages, then call `GET /wiki/pages/{slug}`, `/history`, `/backlinks`, and `/labels` separately for detail data.
- Current ATS: `list_agents()` calls `GET /wiki/pages?path=agents&recursive=true`, then calls `GET /wiki/pages/{slug}` once per returned page to parse each profile.
- Future console: wiki tree/search returns selected slugs -> `POST /wiki/pages/batch` with `slugs` and `include=["body","labels","backlink_count"]` for the visible or selected pages only.
- Future ATS: `list_agents()` keeps using `GET /wiki/pages?path=agents&recursive=true` for metadata-only slug discovery, then calls `POST /wiki/pages/batch` with those explicit slugs and `include=["body"]`. It never asks AGS to recursively return every body.

### 7. Wiki Catalog

```text
GET /api/v3/repos/{owner}/{repo}/wiki/catalog
```

This endpoint returns the wiki navigation catalog for first render: tree, page metadata, labels, backlink counts, and latest update metadata. It should not return full bodies by default.

Default `include`:

```text
include=tree,pages,labels,backlink_counts
```

This is mainly a first-render wiki improvement. It is lower priority than explicit wiki page batch read because batch read removes a broader class of N+1 problems without encouraging full wiki body scans.

Call chain:

- Current console: wiki first render composes `GET /wiki/tree`, `GET /wiki/pages`, optional `GET /wiki/search`, and detail-only calls for selected pages.
- Current ATS: no first-milestone use. ATS treats wiki pages as metadata records and benefits more from explicit batch reads than from a navigation catalog.
- Future console: wiki first render -> `GET /wiki/catalog?include=tree,pages,labels,backlink_counts`, then selected page details use `POST /wiki/pages/batch` or existing detail endpoints.
- Future ATS: no first-milestone use. A future metadata browser could use `GET /wiki/catalog` for navigation while still using `POST /wiki/pages/batch` for selected record bodies.

### 8. Notification Summary

```text
GET /api/v3/notifications/summary
```

This endpoint returns notifications with enough subject context to avoid follow-up calls for the common worker case.

Default `include`:

```text
include=subject,latest_comments
```

Optional query parameters:

```text
all=false
reason=mention
latest_comments_limit=10
```

Response sketch:

```json
[
  {
    "id": "notification-1",
    "reason": "mention",
    "repository": { "full_name": "acme/metadata" },
    "subject": {
      "type": "Issue",
      "number": 1,
      "title": "[chat] general",
      "body": "type: agent-team-chat"
    },
    "latest_comments": []
  }
]
```

This is useful for notification-driven clients. The current ATS worker is one example, but the endpoint shape should stay generic: notifications plus bounded subject context. It should be implemented carefully so it does not make notification reads expensive for users with many notifications.

Call chain:

- Current console: no first-milestone use. Console currently uses invitation-specific endpoints for inbox-like UI, not the generic notifications endpoint.
- Current ATS: worker calls `GET /notifications?all=false`, filters mention notifications, then calls `GET /repos/{owner}/{repo}/issues/{number}` and `GET /repos/{owner}/{repo}/issues/{number}/comments` for each relevant notification.
- Future console: no first-milestone use. A future notification inbox can call `GET /notifications/summary?include=subject`.
- Future ATS: worker calls `GET /notifications/summary?reason=mention&include=subject,latest_comments&latest_comments_limit=10`; if the returned context is insufficient, it falls back to `GET /issues/{number}/thread`.

## Priority Recommendation

P0 endpoints/extensions:

- `GET /api/v3/viewer/summary`
- `GET /api/v3/orgs/{org}/management-summary`
- `GET /api/v3/repos/{owner}/{repo}/issues/{issue_number}/thread`
- issue list filters: `kind`, `title_prefix`, `include=body`, and `fields`
- `POST /api/v3/repos/{owner}/{repo}/wiki/pages/batch`

P1 endpoints/extensions:

- `GET /api/v3/repos/{owner}/{repo}/summary`
- `GET /api/v3/repos/{owner}/{repo}/wiki/catalog`
- `GET /api/v3/notifications/summary`

The org management endpoint is worth implementing, but it should not be the only aggregate API. If only one endpoint can be built first, `viewer/summary` has the broadest product reuse. If the immediate pain is console organization management, build `orgs/{org}/management-summary` first. If the immediate pain is AGS-backed collaboration clients, build issue thread, issue list filters, and wiki page batch read first.

## Authorization Model

Each aggregate section must use the same authorization rules as the underlying resource. A viewer who cannot read an underlying list must not receive that list through an aggregate. For optional sections, the preferred behavior is to omit unauthorized sections and include a capability flag that explains the viewer cannot manage or view that section. Return `403` only when the entire aggregate endpoint is forbidden.

Aggregate responses should include viewer capabilities where the UI would otherwise infer them. Examples:

```json
{
  "viewer": {
    "login": "alice",
    "role": "admin",
    "capabilities": {
      "manage_members": true,
      "manage_invitations": true,
      "comment": true
    }
  }
}
```

## Data Bounds

Every aggregate endpoint must have explicit bounds:

- `per_page` and `page` for top-level lists.
- Section-specific limits such as `team_detail_limit`, `comments_per_page`, `latest_comments_limit`, and `body_limit`.
- A default `include` set that is useful but not explosive.
- A documented fallback path to the existing detailed endpoints for drill-down.
- Explicit selection for body-heavy batch reads. For wiki bodies, clients must provide `slugs`; the backend must not infer "all pages under this path" and return all bodies.

Aggregates should not silently return all data for large organizations, repositories, or wikis.

## Implementation Notes

Prefer grouped service methods over calling REST handlers internally. REST handlers should remain thin wrappers around service APIs.

Use grouped queries for nested data. For example, organization summary should load team member counts and team repository counts for all returned teams in grouped queries, not one query per team.

Keep response structs separate from GitHub-compatible endpoint structs when the aggregate needs capability flags, counts, or section envelopes. Reuse transformation helpers for embedded GitHub-like resources.

Use short TTL caching only after measuring. Correctness and bounded query count are more important for the first implementation.

## Rollout Plan

1. Add backend service DTOs and tests for `viewer/summary`.
2. Update console bootstrap to use `viewer/summary`, with fallback to current endpoints while staging verifies the response.
3. Add `orgs/{org}/management-summary` and move the organization page to it.
4. Add issue thread, issue list filters, and wiki page batch read for console and AGS-backed collaboration clients.
5. Add repo summary, wiki catalog, and notification summary as follow-up optimizations.

## Test Plan

Backend tests should cover authorization, omitted sections, role normalization, pagination bounds, and large-list truncation for every aggregate.

Console tests should cover bootstrap with one summary request, organization page first render with management summary, and fallback behavior when an aggregate endpoint is missing.

ATS tests should cover metadata record loading through wiki page batch read, chat loading through issue thread, and worker notification processing through notification summary if that endpoint is implemented.

## Open Questions

- Should `viewer/summary` include full invitation records by default, or only counts unless `include=invitations:items` is requested?
- Should issue list `title_prefix` be limited to exact prefix matching, or should it support a more general server-side query language?
- Should wiki page batch read use `POST` for request body size, or support a `GET` variant for small slug lists?
- Should `notifications/summary` mark notifications as read, or should it preserve the existing notification read semantics exactly?
