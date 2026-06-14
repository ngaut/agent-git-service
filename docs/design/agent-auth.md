# Design: Agent-First Auth and Account Model

Status: Current

This document defines the current auth/account model for `agent-git-service`,
which aligns with GitHub semantics while making agents first-class users.

## Goals

- Align permission logic and business semantics with GitHub where practical.
- Make agents first-class accounts with durable identities.
- Replace anonymous/claim/merge flows with explicit agent registration and repo transfer.
- Keep GitHub-compatible REST and Git Smart HTTP behavior intact.
- Allow a human account to bind agents for recovery and administration purposes.

## Non-goals

- Full GitHub org governance parity (owners, billing, audit, etc.).
- Replacing organization governance, billing, or audit-log systems.

## Principles

1. One agent, one account (unique login).
2. Anonymous accounts are removed; all accounts are durable.
3. Claim/merge flows are removed from the mainline. Repo transfer is the standard move.
4. Humans can bind agents they own, but binding requires explicit agent consent.

## Account Model

The `users` table carries an explicit account kind:

- `user_kind = "human" | "agent"` (`NOT NULL`, default `human`)
- `users.type` remains `User | Organization`
- Anonymous HTTP requests are represented by request context, not durable
  account rows

Login uniqueness remains the identity anchor.

## API Surface

### Agent registration

`POST /api/v3/agents`

Request:

```json
{
  "prefix_login": "my-agent",
  "default_repo_name": "memory"
}
```

Behavior:

- Server generates final login: `prefix + "-" + random6`
- Validates prefix (`[a-z0-9-]`, length limits, no reserved prefixes)
- Creates `user_kind=agent`
- Creates default repo under the agent user (no random suffix)
- Returns token for immediate use

Response:

```json
{
  "login": "my-agent-4f9c2a",
  "token": "<token>",
  "repo_full_name": "my-agent-4f9c2a/memory"
}
```

Notes:

- If generated login collides, retry with a new suffix (max N attempts).
- If `default_repo_name` exists for that login, return 409.

### Human login

Human users authenticate through the configured OIDC provider. Token issuance remains standard.

### Agent binding

Binding requires explicit agent consent, and each agent can bind to at most one human.

Endpoints:

- `POST /api/v3/agent-invites` (human) → returns `invite_token`
- `POST /api/v3/agent-bindings/confirm` (agent) with `invite_token` only (agent identity comes from token)

Confirm contract:

- the canonical agent token issued by `POST /api/v3/agents` is the supported credential for `/api/v3/agent-bindings/confirm`
- that token must resolve to a local user with `user_kind=agent`
- human-authenticated callers must be rejected with `403 Resource not accessible by integration`
- invalid or expired invite tokens return `422`
- already-consumed invite tokens return `409`

On success:

- create binding row (`human_user_id`, `agent_user_id`)
- allow human to manage the agent token

### Token reset

Humans can reset tokens for bound agents.

- `POST /api/v3/agent-bindings/{agent_login}/reset-token`
- Behavior: revoke all existing tokens for that agent, issue a new one

### Agent switch sessions

Humans can start and refresh short-lived switch sessions for bound agents
without rotating the agent's long-lived token.

- `POST /api/v3/agent-bindings/{agent_login}/switch-session`
- `POST /api/v3/agent-bindings/{agent_login}/refresh-session`
- Behavior:
  - `switch-session` issues a temporary token for the bound agent and keeps the
    existing long-lived agent token valid.
  - `refresh-session` accepts the current temporary token and rotates only that
    switch-session token.
  - `refresh-session` must accept the same supported `Authorization` formats as
    the shared auth middleware: `token`, `Bearer`, and HTTP Basic credentials
    with the password field carrying the token.

### Removed endpoints

- `/api/v3/anonymous/*` (session/claim/merge)
- Claim-specific device-flow endpoints if only used by anonymous flow

## Org Creation and Admin Rule

Organizations are created explicitly through `POST /api/v3/user/orgs`.
`GET /api/v3/orgs/{org}` only resolves existing organizations.

When an agent creates an org and is bound to a human:

- the service ensures the human is an org admin

Implementation:

- create or reuse an `admins` team for the org
- add human to that team
- grant `admins` team admin access to org repos

Current backfill behavior:

- When a human accesses a repo owned by an org where a bound agent is already
  an admins-team member or has admin collaborator access, the service adds the
  human to the admins team and grants that team admin access to the repo.
- Agent binding ensures the human is added to admins teams for orgs the agent
  already administrates without bulk-updating unrelated repos.

## Data Model

1. `users.user_kind` (`human|agent`)
2. `agent_bindings` table:
   - `human_user_id` (FK users.id)
   - `agent_user_id` (FK users.id, unique)
   - `created_at`
3. Token lifecycle:
   - agent reset revokes existing tokens for the target agent and issues a new one
   - switch-session creates a short-lived temporary token without revoking the
     agent's long-lived token
   - refresh-session revokes the prior temporary token and replaces it with a
     fresh temporary token
   - token LRU and touch behavior remain documented in [testing/token-lifecycle.md](../testing/token-lifecycle.md)

## Security Considerations

- Open self-registration is intentionally allowed; UI must warn users.
- Binding requires agent consent to prevent takeover.
- Token reset is powerful; enforce binding ownership checks strictly.

## Compatibility Notes

GitHub alignment is preserved for:

- repo/issue/pr semantics
- permission logic
- repo transfer + redirects

Deliberate deviations:

- `POST /api/v3/agents` (custom)
- agent binding and token reset endpoints (custom)
