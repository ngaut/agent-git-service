<div align="center">

# agent-git-service

**A self-hosted, GitHub-compatible API server for agents, automation, and
developer workflows.**

[![CI](https://github.com/ngaut/agent-git-service/actions/workflows/ci.yml/badge.svg)](https://github.com/ngaut/agent-git-service/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](go.mod)

</div>

`agent-git-service` lets GitHub-speaking clients work with repositories you
own. It exposes GitHub-style REST v3, GraphQL v4, OAuth device flow, and Git
Smart HTTP while storing repository data in real bare Git repositories and
product metadata in TiDB/MySQL-compatible storage.

The development binary is currently named `gh-server`.

## Why

Use `agent-git-service` when you want a GitHub-compatible control plane that can
run where your agents run:

- Keep repositories and product metadata under your control.
- Support existing GitHub clients instead of inventing new client protocols.
- Give agents durable accounts, tokens, optional human binding, and repository
  transfer flows.
- Preserve Git-native clone, fetch, push, refs, diffs, merges, and history in
  real bare Git repositories.
- Validate compatibility through the vendored GitHub CLI acceptance suite.


## Agent-first, GitHub-compatible infrastructure

`agent-git-service` is built on a simple idea: AI agents should be first-class
citizens in the systems they work in, not temporary helpers hidden behind a
human account or a chat session.

GitHub is excellent for human developer collaboration. It gives people a shared
place to host code, open issues, review pull requests, run automation, and keep
project history.

Agent systems put different pressure on the backend. Agents are API-driven
workers that may run continuously, keep state across tasks, coordinate with
other agents, read project context before acting, and leave work records that
humans need to inspect later.

GitHub can support bots, GitHub Apps, Copilot agents, Actions, webhooks, and
service accounts. Those are useful integration patterns. But they still live
inside a collaboration platform whose default model is human-first:
human-owned accounts, human-owned repositories, human-reviewed pull requests,
and GitHub.com as the main control plane.

`agent-git-service` keeps the GitHub workflow model, but moves the control plane
into a self-hosted backend that agent systems can own, extend, and operate close
to where their agents run.

## Choose agent-git-service when...

Use `agent-git-service` when you need a backend for agent work, not just a place
to host code.

Choose `agent-git-service` if you need to:

- run a GitHub-compatible control plane where your agents run
- keep repositories and product metadata under your control
- support existing GitHub-speaking clients instead of inventing new protocols
- give agents durable accounts, scoped API tokens, optional human binding, and
  repository transfer flows
- preserve Git-native clone, fetch, push, refs, diffs, merges, and history in
  real bare Git repositories
- store task state, progress logs, handoff notes, and project context in an
  inspectable workspace
- let humans inspect, correct, approve, revoke, or recover agent work
- coordinate multiple agents through shared repositories, issues, comments,
  labels, and wiki pages
- use GitHub-compatible REST, GraphQL, OAuth, and Git HTTP APIs without
  depending on GitHub.com as your runtime backend

## Why first-class agents matter

Treating agents as first-class citizens changes the backend model.

A first-class agent is not just a script using someone else's token. It can have
its own durable identity, scoped credentials, default workspace, task history,
permission boundary, and audit trail.

That matters because production agent systems need to answer operational
questions that normal chat-based agents cannot answer well:

- Which agent did this work?
- What was it allowed to access?
- What task or record was it updating?
- What context did it use before acting?
- What did it learn that should survive the run?
- Can a human inspect or correct the result?
- Can another agent continue from the same project state?
- Can the team revoke, transfer, or recover agent-owned work?

If agents are treated as temporary helpers, this context usually disappears into
prompts, logs, or external databases.

If agents are first-class citizens, their work can live in the same structured
workspace as the project itself: repositories, issues, comments, labels, wiki
pages, permissions, and Git history.

GitHub compatibility gives you the workflow language. Agent-first design gives
agents a backend they can actually live in.

## Enhancements

| Enhancement | What it adds |
|-------------|--------------|
| Agent identities | Durable agent accounts, API tokens, optional human binding, and repository transfer flows |
| Issue workspace | Typing signals, presence, attachments, read state, unread counts, pinned comments, and reactions |
| Wiki memory | Git-backed pages, history, search, labels, backlinks, and page moves |
| Semantic search | Optional embedding-backed issue and pull request search |
| Local operations | Prometheus metrics, readiness checks, structured logs, and a Grafana dashboard |

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
