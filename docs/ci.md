# CI Documentation

This document describes the GitHub Actions workflows carried by the public
`agent-git-service` repository.

## Overview

The repository uses a layered CI gate:

- `ci.yml` runs formatting, vet, regression, unit, integration, compatibility,
  E2E, and backend smoke jobs.
- `cli-pr-check.yml` keeps the vendored GitHub CLI test inventory visible on
  pull requests.
- `doc-lint.yml` verifies documentation links, workflow inventory, and module
  contract coverage.
- `secret-scan.yml` runs gitleaks against checked-out files on pull requests and
  pushes to `main`.
- `license-check.yml` verifies root Go module dependency license metadata.

Publishing, deployment, and benchmark-history workflows are intentionally not
part of the default public CI set.

## Workflow Files

- `ci.yml` - main layered CI gate for pull requests, pushes to `main`, and
  manual runs
- `cli-pr-check.yml` - full vendored `cli/` unit-test workflow for pull
  requests and manual runs
- `doc-lint.yml` - verifies core CI/testing docs and workflow inventory stay in
  sync
- `secret-scan.yml` - scans checked-out files for secrets; manual runs can also
  scan full git history
- `license-check.yml` - verifies dependency licenses and reviewed third-party
  NOTICE files

## Main CI Workflow

Trigger:

- pull requests targeting `main`
- pushes to `main`
- manual `workflow_dispatch`

Jobs:

1. `lint`
   - checks Go formatting outside the nested `cli/` module
   - runs `go vet ./...`
2. `regression-gate`
   - runs `bash scripts/regression_gate.sh`
   - compiles the backend tree with `go test ./... -run '^$'`
   - compiles the vendored `cli/` tree and `cli/_go-gh-local`
   - runs the stable package pack defined in `scripts/regression_gate.list`
   - runs the tenant DB audit through the regression manifest
3. `unit-tests`
   - runs `make test-unit`
   - executes the backend Go unit-test inventory with `go test -v ./...`
4. `integration-tests`
   - runs `bash scripts/integration_tests.sh`
   - executes stable real-router and Git HTTP integration coverage
5. `compatibility-tests`
   - provisions the test-only TiDB playground with `make test-setup`
   - starts `gh-server` against TiDB
   - runs the vendored GitHub CLI acceptance inventory with `make test`
   - cleans up with `make test-clean-all`
6. `e2e-tests`
   - provisions the test-only TiDB playground with `make test-setup`
   - starts `gh-server` against TiDB
   - runs the shell E2E inventory with `make test-e2e`
   - cleans up with `make test-clean-all`
7. `backend-smoke`
   - runs `bash scripts/backend_smoke.sh`
   - builds and starts `gh-server` against SQLite
   - probes readiness and basic API endpoints
   - validates repository creation and Git Smart HTTP clone behavior
8. `ci-success`
   - fails if any required CI job fails

This gives the repository one explicit merge gate with a clear layer split:

- `Regression Gate` protects fast, deterministic regressions.
- `Unit Tests` protect backend package behavior.
- `Integration Tests` protect stable real-router and Git HTTP paths.
- `Compatibility Tests` protect the vendored GitHub CLI compatibility inventory.
- `E2E Tests` and `Backend Smoke` prove runtime behavior across real process
  boundaries.
- `doc-lint.yml` keeps gate definitions and documentation expectations in sync.

## Secret Scan Workflow

`secret-scan.yml` runs `scripts/secret_scan.sh --worktree-only` on pull requests
targeting `main` and pushes to `main`. The workflow installs a pinned gitleaks
version through Go so scan behavior stays deterministic.

Manual `workflow_dispatch` runs include a `full_history` input. When enabled,
the workflow also runs `scripts/secret_scan.sh --history-only` against the full
git history. The default PR path scans only checked-out files so the gate stays
fast and focused on newly introduced leaks.

## Dependency License Workflow

`license-check.yml` runs `python3 scripts/license_check.py --report
artifacts/dependency-licenses.tsv` on pull requests, pushes to `main`, and
manual dispatch. The script scans the root Go module build list, checks each
module's top-level license files against the reviewed allowlist, and fails on
unknown or disallowed licenses.

The script also reports third-party NOTICE files. Currently reviewed NOTICE
entries must remain present in the root `NOTICE`; newly introduced NOTICE files
fail the workflow until they are reviewed and documented in
[`docs/governance/dependency-licensing.md`](governance/dependency-licensing.md).

## Documentation Workflow

`doc-lint.yml` runs `python3 scripts/doc_lint.py` to verify that:

- required CI/testing docs and workflow files exist
- relative links inside the core CI/test docs resolve
- `docs/ci.md` mentions the active core workflows
- `.github/pull_request_template.md` points contributors at current CI/test docs
- CI helper scripts used by the main gate still exist
- every top-level `internal/*` package has a contract entry in
  `docs/module-contracts.md` via `scripts/check-module-contracts.sh`

## Workflow Execution Sandbox

The repository supports sandboxed workflow execution for GitHub Actions-style
workflow runs. This capability is fail-closed by default and must be explicitly
enabled via the `ENABLE_WORKFLOW_EXEC` environment variable.

| Variable | Default | Description |
|----------|---------|-------------|
| `ENABLE_WORKFLOW_EXEC` | `false` | Enable workflow execution sandbox (set to `true` or `1`) |
| `WORKFLOW_EXEC_IMAGE` | `bash:5.2` | Docker image used for workflow step execution |
| `WORKFLOW_EXEC_TIMEOUT` | `2m` | Maximum duration for workflow execution |
| `WORKFLOW_EXEC_CPUS` | `1.0` | CPU limit for workflow containers |
| `WORKFLOW_EXEC_MEMORY` | `256m` | Memory limit for workflow containers |
| `WORKFLOW_EXEC_PIDS_LIMIT` | `128` | Maximum number of processes in workflow containers |
| `WORKFLOW_EXEC_NOFILE` | `1024` | Maximum number of open file descriptors |
| `WORKFLOW_EXEC_TMPFS_SIZE` | `64m` | Size of tmpfs mount for `/tmp` |

Workflow steps run in isolated Docker containers with no network access, a
read-only root filesystem, dropped Linux capabilities, no-new-privileges, and
resource limits. Tests cover both the disabled state and enabled execution path.

## Known Gaps

The following capabilities are intentionally outside the default public CI set:

- scheduled release automation
- image publication and deployment
- coverage reporting
- benchmark-history publication
- supply-chain extras such as SBOM generation, signing, or provenance
  attestations

As additional suites become reliable, promote the smallest stable regressions
into `scripts/regression_gate.list` first, then consider widening the required
smoke or compatibility lanes.
