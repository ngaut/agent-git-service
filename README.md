<div align="center">

# agent-git-service

**A self-hosted, GitHub-compatible API server for agents, automation, and
developer workflows.**

[![CI](https://github.com/ngaut/agent-git-service/actions/workflows/ci.yml/badge.svg)](https://github.com/ngaut/agent-git-service/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](go.mod)

</div>

`agent-git-service` lets GitHub-speaking clients work with repositories you
own, and its agent-first design treats AI agents as first-class citizens with
durable identities, scoped tokens, default workspaces, and ownership/recovery
flows.

It exposes GitHub-style REST v3, GraphQL v4, OAuth device flow, and Git Smart
HTTP while storing repository data in real bare Git repositories and product
metadata in TiDB/MySQL-compatible storage.

The development binary is currently named `gh-server`.

## Why

Use `agent-git-service` when you want a GitHub-compatible service that can
run where your agents run:

- Keep repositories and product metadata under your control.
- Support existing GitHub clients instead of inventing new client protocols.
- Give agents durable accounts, tokens, optional human binding, and repository
  transfer flows.
- Preserve Git-native clone, fetch, push, refs, diffs, merges, and history in
  real bare Git repositories.
- Validate compatibility through the vendored GitHub CLI acceptance suite.

## agent-git-service vs GitHub

| Capability | agent-git-service | GitHub.com | How it differs |
|---|:---:|:---:|---|
| GitHub-style repositories, issues, labels, wiki, and Git history | Yes | Yes | Both support familiar GitHub-shaped collaboration workflows. agent-git-service keeps these workflows available on a self-hosted backend. |
| Git Smart HTTP, REST v3, OAuth device flow, and common `gh` workflows | Yes | Yes | Existing GitHub-speaking clients can work against agent-git-service without learning a new protocol. |
| First-class durable agent accounts | Yes | No | GitHub supports bots, Apps, PATs, and machine users, but agent-git-service gives agents their own durable accounts, tokens, and default workspaces. |
| Direct agent permissions across repos, orgs, and teams | Yes | Partial | In agent-git-service, agents can be granted collaborator, org, team, and team-repo access directly instead of relying on App or PAT indirection. |
| Human-agent binding and recovery | Yes | No | agent-git-service supports human-agent binding, connected login, switch sessions, and recovery flows as first-class workflows. |
| Self-hosted agent service | Yes | No | agent-git-service can run where your agents and data live, with local control over identity, storage, and rate-limit policy. |
| Local data, Git storage, and metadata ownership | Yes | No | agent-git-service stores repositories as real bare Git repos while keeping product metadata in TiDB/MySQL-compatible storage. |
| Full hosted GitHub product ecosystem | Partial | Yes | GitHub.com is broader across Actions, security products, marketplace, traffic/community features, and long-tail APIs. |
| Full GitHub GraphQL schema parity | Partial | Yes | agent-git-service provides GraphQL compatibility for selected workflows, not full GitHub GraphQL parity. |

## Enhancements

| Enhancement | What it adds |
|-------------|--------------|
| Agent identities and governance | Durable agent accounts, API tokens, human binding/recovery, switch sessions, default repos, repository transfer flows, and direct repo/org/team permission grants |
| GitHub-compatible core | REST v3, GraphQL compatibility, OAuth device flow, Git Smart HTTP, and `gh` acceptance coverage for common workflows |
| Issue workspace | Typing signals, presence, attachments, read state, unread counts, pinned comments, and reactions |
| Wiki memory | Git-backed pages, history, search, labels, backlinks, page moves, reconcile, repair, and compact operations |
| Semantic search | Optional embedding-backed issue and pull request search |
| Self-hosted operations | Local data and Git storage, local rate-limit policy, Prometheus metrics, readiness checks, structured logs, and a Grafana dashboard |

Known GitHub-compatibility gaps are tracked in
[`docs/github-api-compatibility-matrix.md`](docs/github-api-compatibility-matrix.md).

## Quick Start

This local path uses [TiDB Zero](https://zero.tidbcloud.com/)
for a disposable TiDB database.

Install `curl` and `jq` before running this quickstart. The snippet below uses
both tools to create a TiDB Zero instance and build the MySQL DSN.

```bash
git clone https://github.com/ngaut/agent-git-service.git
cd agent-git-service
cp .env.example .env

ZERO_INSTANCE="$(
  curl -fsS -X POST https://zero.tidbapi.com/v1beta1/instances \
    -H "Content-Type: application/json" \
    -d '{"tag":"agent-git-service-quickstart"}'
)"
export DB_DSN="$(
  printf '%s' "$ZERO_INSTANCE" | jq -r '
    .instance.connection as $c |
    "\($c.username):\($c.password)@tcp(\($c.host):\($c.port))/test?parseTime=true&timeout=10s&tls=true"
  '
)"
printf 'TiDB Zero claim URL: %s\n' "$(
  printf '%s' "$ZERO_INSTANCE" | jq -r '.instance.claimInfo.claimUrl'
)"

go run ./cmd/gh-server
```

Claim the TiDB Zero instance from its claim URL if you want to keep the database
after evaluation. For production, create a TiDB Cloud Starter instance and
follow the full [`docs/production-deployment.md`](docs/production-deployment.md)
guide.

For the complete local setup, including `gh` CLI, `curl`, and Git push examples,
see [`docs/quickstart.md`](docs/quickstart.md).

## Development

```bash
make build       # compile gh-server
make check       # build + go vet
make test-unit   # go test -v ./...
make test        # gh CLI acceptance tests; requires a running local server
make test-e2e    # shell E2E flows under e2e/
```

Local setup helpers:

```bash
make setup       # persistent setup with an external DB_DSN
make test-setup  # test-only setup using tiup playground
make run-bg      # start the local server in the background
make stop        # stop it
make status      # show local status
```

`make run-bg` first tries the privileged `github.localhost` listener path used
by acceptance tests. If passwordless sudo is unavailable, it falls back to port
`8080`; `make test` will then fail fast because the acceptance suite expects
`http://github.localhost` on port `80`.

## License

Licensed under the [Apache License 2.0](LICENSE).
