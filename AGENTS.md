# Repository Guidelines

## Project Structure & Module Organization
- `main.go` is the composition root (config, DB/Git wiring, listeners).
- `internal/` contains backend modules by concern: `rest`, `graphql`, `service`, `db`, `gitstore`, `githttp`, `oauth`, `middleware`, `router`, and `testharness`.
- `cli/` is the vendored GitHub CLI plus acceptance tests in `cli/acceptance/`.
- `e2e/` contains shell-based end-to-end scripts (`bash` + `curl` + `jq`).
- `docs/` is the architecture SSOT, especially `architecture.md`, `module-contracts.md`, and `test-strategy.md`.

## Build, Test, and Development Commands
- `make setup`: production/persistent bootstrap with an external TiDB Cloud Starter `DB_DSN`.
- `make test-setup`: test-only bootstrap that starts TiDB via `tiup playground`.
- `make build`: compile `gh-server`.
- `make run-bg` / `make stop`: start or stop local server.
- `make check`: fast pre-commit check (`make build` + `make vet`).
- `make test-unit`: run all Go unit tests (`go test -v ./...`).
- `make test`: run `cli` acceptance tests (requires running server).
- `make test-e2e [SCRIPT=name]`: run all or one `e2e/*.sh` flow.

## Coding Style & Naming Conventions
- Target Go `1.25.0` (see `go.mod`).
- Indentation: rely on formatters (`gofmt`/`goimports` for Go tabs; follow existing style for non-Go files).
- Format Go code with `make fmt` (`goimports` on root and `internal/`).
- Keep transport layers thin: REST/GraphQL handlers should delegate business logic to `internal/service`.
- Follow standard Go naming: exported `CamelCase`, unexported `camelCase`, package names lowercase.
- Place tests beside code as `*_test.go`; prefer table-driven tests for business rules and handlers.

## API Surface Boundary
- Use `/api/v3` and `/api/graphql` only for GitHub-compatible APIs. These routes primarily serve existing GitHub-speaking clients, including `gh`, GitHub REST/GraphQL SDKs, and Git-compatible automation.
- A route may stay under `/api/v3` as a GitHub-shaped local shim only when it intentionally uses a GitHub-like path, request/response shape, or client behavior for compatibility. If behavior differs from GitHub.com, document it clearly as partial compatibility, a local shim, or an extension. Do not imply strict GitHub.com parity for local semantics.
- Use `/api/ext/v1` for extension APIs that are not part of GitHub's API contract. New primitives such as agent runs, run-scoped token management, context packs, leases, agent policies, agent queues, scorecards, local wiki page APIs, aggregate views, and other platform control-plane features belong under `/api/ext/v1`.
- Do not place extension APIs under `/api/v3` just because the resource is repo-scoped. Repo-scoped extension routes should use `/api/ext/v1/repos/{owner}/{repo}/...`.
- Do not introduce new `/api/ags/...` routes. The extension namespace is `/api/ext/v1`; keep code constants, OpenAPI output, tests, docs, and compatibility matrices aligned with that path.
- Discovery and OpenAPI output must keep the boundary explicit: `/api/v3` and `/api/v3/openapi.json` describe GitHub-compatible REST only, while `/api/ext/v1` and `/api/ext/v1/openapi.json` describe extension APIs only.

## Testing Guidelines
- Follow the test pyramid in `docs/test-strategy.md`: package/service tests first, then router/integration, then acceptance.
- Use `internal/testharness` for integration tests with real router wiring.
- Use `make test-run SUITE=TestName` for focused acceptance debugging.
- Keep E2E scripts executable and descriptive (for example, `repo-transfer-lifecycle.sh`).

## Commit & Pull Request Guidelines
- Keep each commit scoped to one issue or behavior change.
- Preferred commit subject patterns match repo history: `fix: ...`, `feat: ...`, `test: ...`, `docs: ...` (optionally with `(#123)` or `Fix #123:`).
- PRs should include a clear description, exact test commands run, and updates to SSOT docs when boundaries or testing expectations change.
- Complete the checklist in `.github/pull_request_template.md` for architecture/test-strategy drift.
