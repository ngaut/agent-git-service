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
