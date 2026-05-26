# Documentation

Start here when you need the shape of the repo rather than a specific source
file.

## Run Locally

- [Quick Start](quickstart.md) - run `agent-git-service` locally with TiDB Zero.
- [Configuration Reference](../.env.example) - canonical environment variable reference.

## Architecture

- [Architecture](architecture.md) - system overview and current implementation baseline.
- [Module Contracts](module-contracts.md) - ownership, dependency rules, and accepted couplings for `internal/*`.
- [GitHub API Compatibility Matrix](github-api-compatibility-matrix.md) - supported surfaces and known gaps.
- [Test Strategy](test-strategy.md) - test pyramid, CI layers, and execution commands.

Component and cross-cutting references live in [architecture/](architecture/):

- [REST API](architecture/rest.md)
- [GraphQL API](architecture/graphql.md)
- [Service Layer](architecture/service.md)
- [Git Store](architecture/gitstore.md)
- [Git Smart HTTP](architecture/git-http.md)
- [OAuth](architecture/oauth.md)
- [Control Plane](architecture/controlplane.md)
- [Tenant Context](architecture/tenant.md)
- [Tenant DB Correctness](architecture/tenant-db-correctness.md)
- [Tenant Git Storage](architecture/tenant-git-storage.md)
- [Collaboration Framework](architecture/collaboration-framework.md)
- [Error Semantics](architecture/error-semantics.md)
- [Secrets Encryption](architecture/secrets-encryption.md)
- [Wiki Storage V2](architecture/wiki-storage-v2.md)

## Design Records

Design records live in [design/](design/). They may describe current behavior,
accepted direction, or incremental work that has not fully landed yet.

- [Agent Auth and Account Model](design/agent-auth.md)
- [Authorization Layer](design/authz-layer.md)
- [Multi-Agent Architecture](design/multi-agent.md)
- [Wiki Storage Re-Architecture](design/wiki-storage-rearchitecture.md)

## Testing And Operations

- [Production Deployment](production-deployment.md)
- [CI](ci.md)
- [Token Lifecycle Test Coverage](testing/token-lifecycle.md)
- [Dependency Licensing](governance/dependency-licensing.md)
- [Monitoring Assets](monitoring/README.md)
