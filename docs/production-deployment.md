# Production Deployment

This guide describes a production-oriented `agent-git-service` deployment. It
assumes a persistent TiDB Cloud Starter database, durable Git repository
storage, explicit production configuration, and operational probes.

For local evaluation, use [Quick Start](quickstart.md). TiDB Zero is useful for
trying the service quickly and can be claimed from its claim URL for longer
experiments, but production deployments should use a persistent TiDB Cloud
Starter instance.

## Deployment Shape

The production baseline is:

- one or more `gh-server` processes or containers
- TLS terminated by a load balancer or reverse proxy
- `LISTEN_MODE=production`, which exposes one HTTP listener on `PORT`
- TiDB Cloud Starter for relational metadata
- a durable POSIX filesystem mounted at `GIT_REPO_DIR`
- `/readyz` for readiness and `/metrics` for Prometheus scraping

Single-node deployments may use a local persistent disk for `GIT_REPO_DIR`.
Horizontally scaled deployments need shared POSIX-compatible storage for
`GIT_REPO_DIR` so every instance can read and write the same bare repositories.

## Prerequisites

- Go 1.25+ if building from source
- Git available in the runtime image or host
- A TiDB Cloud Starter instance
- A persistent filesystem for bare Git repositories
- A reverse proxy or load balancer for TLS
- A secrets manager for production environment variables

## Prepare TiDB Cloud Starter

Create or open a TiDB Cloud Starter instance, then create a dedicated metadata
database:

```sql
CREATE DATABASE IF NOT EXISTS `gh-server`;
```

Open the TiDB Cloud **Connect** dialog, choose the public MySQL connection type,
and build a Go MySQL DSN. Keep `tls=true`:

```env
DB_DSN=<prefix>.root:<password>@tcp(<tidb-cloud-host>:4000)/gh-server?parseTime=true&timeout=10s&tls=true
```

For multi-agent control-plane deployments, create one additional control-plane
database and one tenant database per isolated agent. Set `CONTROL_PLANE_DSN` to
the control-plane database DSN and store each tenant DSN in the control plane.
See [Control Plane Architecture](architecture/controlplane.md) and
[Multi-Agent Architecture](design/multi-agent.md) for the current routing model.

## Configure Runtime

Set production values through environment variables rather than committing
`.env` files with real credentials.

```env
ENVIRONMENT=production
LISTEN_MODE=production
PORT=8080
BASE_URL=https://gh.example.com

DB_DSN=<prefix>.root:<password>@tcp(<tidb-cloud-host>:4000)/gh-server?parseTime=true&timeout=10s&tls=true
GIT_REPO_DIR=/data/repos

SECRET_ENCRYPTION_KEY=<base64-encoded-32-byte-key>

LOG_FORMAT=json
LOG_LEVEL=info
```

Important production notes:

- `ENVIRONMENT=production` skips local seed data. `ADMIN_LOGIN` and
  `ADMIN_TOKEN` are local-development seed settings and do not bootstrap users
  in production.
- `BASE_URL` must be the external URL clients use. REST responses and Git clone
  URLs are built from it.
- `SECRET_ENCRYPTION_KEY` should be set for any production deployment that
  stores Actions, Dependabot, or Codespaces secrets. If it is unset, the server
  generates an ephemeral key at startup and previously encrypted secrets may not
  decrypt after a restart or on another instance.
- `GIT_REPO_DIR` must survive restarts. Do not point it at an ephemeral
  container filesystem.

## Bootstrap Access

Production does not seed `octocat` or `local-dev-token`. Use one of these
supported access paths:

- Configure Auth0 for human login with `AUTH0_ISSUER`, `AUTH0_CLIENT_ID`, and
  `AUTH0_AUDIENCE`.
- Register agent accounts through `POST /api/v3/agents`, which returns an agent
  login, token, and default repository.
- In control-plane mode, provision control-plane users and tokens, then activate
  the users once their tenant databases are ready.

Open agent self-registration is intentional in the current product model. If
that is not appropriate for your environment, restrict access at the reverse
proxy or network layer until an admission flow is in place.

## Optional Capabilities

### Browser Console And CORS

Set the console URL and allowed browser origins:

```env
CONSOLE_BASE_URL=https://console.example.com
CORS_ALLOWED_ORIGINS=https://console.example.com
```

When `CORS_ALLOWED_ORIGINS` is empty, the server derives local development
origins from `CONSOLE_BASE_URL`.

### Workflow Execution

Workflow execution is fail-closed by default. Enable it only when the container
runtime and sandbox limits are ready:

```env
ENABLE_WORKFLOW_EXEC=true
WORKFLOW_EXEC_IMAGE=bash:5.2
WORKFLOW_EXEC_TIMEOUT=2m
WORKFLOW_EXEC_CPUS=1.0
WORKFLOW_EXEC_MEMORY=256m
WORKFLOW_EXEC_PIDS_LIMIT=128
WORKFLOW_EXEC_NOFILE=1024
WORKFLOW_EXEC_TMPFS_SIZE=64m
```

### Semantic Search

Lexical search works without an embedding provider. Set these variables only
when semantic/vector-backed issue and pull request search should be enabled:

```env
EMBEDDING_API_KEY=<provider-api-key>
EMBEDDING_BASE_URL=https://api.openai.com
EMBEDDING_MODEL=text-embedding-3-small
EMBEDDING_DIMENSIONS=
EMBEDDING_CONCURRENCY=50
```

### Git HTTP Push Limits

The normal API body limit is not used for Git pushes. Tune Git push spooling
when large repositories or memory-constrained hosts need it:

```env
GITHTTP_MAX_PUSH_BYTES=
GITHTTP_SPOOL_DIR=/data/spool
```

An empty `GITHTTP_MAX_PUSH_BYTES` uses the default 2 GiB push limit. An empty
`GITHTTP_SPOOL_DIR` uses the system temp directory.

## Run From Source

Build and run the binary with the production environment loaded by your process
manager:

```bash
make build
./gh-server
```

For a simple systemd-style deployment, configure the environment in a protected
environment file and run the binary as an unprivileged user with write access to
`GIT_REPO_DIR`.

## Run As A Container

The Dockerfile builds a static `gh-server` binary and includes Git in the
runtime image:

```bash
docker build -t gh-server:local .
docker run --rm \
  -p 8080:8080 \
  -e ENVIRONMENT=production \
  -e LISTEN_MODE=production \
  -e PORT=8080 \
  -e BASE_URL=https://gh.example.com \
  -e DB_DSN="$DB_DSN" \
  -e SECRET_ENCRYPTION_KEY="$SECRET_ENCRYPTION_KEY" \
  -v /srv/agent-git-service/repos:/data/repos \
  gh-server:local
```

Terminate TLS at the load balancer or reverse proxy and forward HTTP to the
container port.

## Verify The Deployment

Wait for readiness. The first start can take a few minutes while schema
migrations and TiDB full-text indexes are created:

```bash
curl -fsS https://gh.example.com/readyz | jq .
```

Expected ready response:

```json
{
  "status": "ready",
  "version": "unknown",
  "checks": {
    "main_db": {
      "status": "ok"
    }
  }
}
```

In control-plane mode, `/readyz` also reports `control_plane_db`.

Probe the GitHub-compatible discovery endpoints:

```bash
curl -fsS https://gh.example.com/api/v3/ | jq .
curl -fsS https://gh.example.com/api/v3/meta | jq .
curl -fsS https://gh.example.com/api/v3/rate_limit | jq .
```

Scrape Prometheus metrics from:

```text
https://gh.example.com/metrics
```

The Grafana dashboard JSON lives in
[monitoring/grafana/gh-server-operations-dashboard.json](monitoring/grafana/gh-server-operations-dashboard.json).

## Backups And Upgrades

Back up both persistence layers:

- TiDB Cloud Starter database backups for metadata
- `GIT_REPO_DIR` snapshots for bare Git repositories

Keep these backups consistent enough that repository rows and bare repositories
can be restored together. Before upgrading:

1. Back up TiDB metadata and Git repository storage.
2. Deploy the new binary or image to one instance first.
3. Wait for `/readyz` to return `ready`.
4. Run API and Git smoke tests.
5. Roll the remaining instances.

Schema migrations run at startup. During the first deployment of a new version,
allow enough startup time for migrations and full-text index changes.

## Production Checklist

- `ENVIRONMENT=production`
- `LISTEN_MODE=production`
- `BASE_URL` set to the public HTTPS URL
- `DB_DSN` points to TiDB Cloud Starter with `tls=true`
- `GIT_REPO_DIR` is durable and shared across instances when scaled out
- `SECRET_ENCRYPTION_KEY` stored in a secrets manager
- TLS terminated before traffic reaches `gh-server`
- `/readyz` wired as the load balancer readiness check
- `/metrics` scraped by Prometheus
- database and Git storage backups configured
- Auth0, agent registration controls, or control-plane provisioning chosen for
  account bootstrap
