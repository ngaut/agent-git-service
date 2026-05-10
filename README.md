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

1. Create a free [TiDB Cloud Starter](https://www.pingcap.com/tidb-cloud-starter/)
   instance.
2. Open **SQL Editor** for the cluster and create the metadata database:

```sql
CREATE DATABASE IF NOT EXISTS `gh-server`;
```

3. Open the TiDB Cloud **Connect** dialog and copy the public MySQL connection
   string. Keep `tls=true` in the DSN.
4. Start the service:

```bash
git clone https://github.com/ngaut/agent-git-service.git
cd agent-git-service
cp .env.example .env
# Edit .env and set DB_DSN to the TiDB Cloud connection string.
go run .
```

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
