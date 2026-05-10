# Contributing

Thanks for helping improve `agent-git-service`.

## Development Setup

1. Install the prerequisites listed in `README.md`.
2. Copy `.env.example` to `.env` and set `DB_DSN` to a TiDB Cloud Starter database for production or persistent development.
3. Run `make setup` for the full GitHub-compatible API environment, `make test-setup` for the test-only TiDB playground, or `make build` for a quick compile.

## Before Opening A Pull Request

Run the fastest relevant checks first:

```bash
make check
```

For behavior changes, add focused tests beside the code you changed and run the matching package tests. For wider changes, use:

```bash
make test-unit
bash scripts/integration_tests.sh
```

Changes that affect API compatibility, Git transport, routing, auth, CI, or test expectations should also update the matching docs under `docs/`.

## Pull Request Expectations

- Keep each PR scoped to one behavior change or cleanup.
- Include a clear description of why the change is needed.
- Link a tracking issue or plan when available.
- List the exact commands you ran.
- Do not commit local databases, generated binaries, credentials, logs, or scan outputs.

## Vendored CLI

The `cli/` directory contains a vendored GitHub CLI tree used for gh CLI compatibility testing. Avoid broad edits there unless the change is specifically about that harness, gh CLI compatibility, or GitHub-compatible client behavior.
