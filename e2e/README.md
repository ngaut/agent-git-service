# E2E Tests

This folder contains lightweight end-to-end tests for `gh-server`.

E2E tests commonly use:
- shell scripts (`bash` + `curl`) for simple HTTP flows
- the repo's main language (Go here) for richer assertions and reuse
- Node/Python for API-driven integration suites

In this repo, we use **bash + curl + jq** for minimal dependencies and easy local debugging.

## Usage

Most scripts run against an existing server:

```bash
make run-bg
```

Some scripts are self-contained and build/start their own temporary server.

Run all e2e scripts:

```bash
make test-e2e
```

Run a single script:

```bash
make test-e2e SCRIPT=code-search-isolation-e2e
```

Notes:
- Scripts that require multiple users or tokens skip themselves when the needed
  token environment variables are absent.
- Self-contained TiDB scripts require a local TiDB-compatible endpoint, normally
  provided by `make test-setup`.

Override base URL (if not on port 80) or curl flags:

```bash
make test-e2e E2E_BASE_URL="https://github.localhost:8080"
```

## Test Scripts

| Script | Description | Mode |
|--------|-------------|------|
| `agent-auth-flow.sh` | Agent registration, human binding, and OIDC-backed claim flow | Existing server plus mock OIDC |
| `code-search-isolation-e2e.sh` | Code search tenant isolation, concurrent search, and no-leak checks | Self-contained TiDB |
| `git-smart-http-auth-denial-matrix.sh` | Git Smart HTTP auth denial matrix | Existing server |
| `multi-agent-isolation.sh` | Multi-agent control-plane tenant isolation | Self-contained TiDB |
| `oauth-device-flow.sh` | OAuth device-flow bootstrap and polling behavior | Existing server |
| `oidc-provider-flow.sh` | Generic OIDC callback, lookup, repeated-login, and token-validity flow using the mock discovery server | Running server with `OIDC_PROVIDER`, `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_ALLOW_INSECURE_HTTP=1`; mock OIDC server |
| `org-collaboration-governance.sh` | Org invitations, outside collaborators, and permission aliases | Existing server plus extra user tokens |
| `push-postprocessing-consistency.sh` | Post-push HEAD, workflow sync, and cleanup behavior | Self-contained SQLite |
| `repo-rollback-compensation.sh` | Repository create/fork rollback behavior | Self-contained SQLite |
| `repo-transfer-lifecycle.sh` | Repository transfer, redirects, and Git usability | Self-contained SQLite |
| `team-membership-admin-authz.sh` | Team membership admin authorization boundaries | Existing server plus extra user tokens |
| `team-repo-sharing-auth.sh` | Team repository sharing authorization | Existing server plus extra user tokens |
| `team-repo-sharing-crud.sh` | Team repository sharing CRUD | Existing server |
| `team-repo-sharing-scenario.sh` | Multi-user team sharing scenario | Existing server plus extra user tokens |
| `token-api.sh` | User token API smoke flow | Existing server |
| `token-lifecycle.sh` | User token lifecycle and revocation behavior | Existing server |
| `vector-search-e2e.sh` | Vector and semantic search behavior with a mock embedding server | Self-contained TiDB |
