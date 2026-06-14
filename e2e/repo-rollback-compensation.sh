#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=./lib.sh
source "$ROOT/e2e/lib.sh"

require_cmd go
require_cmd make
require_cmd mysql

note "=== Rollback compensation checks (TiDB-backed Go integration tests) ==="
make -C "$ROOT" test-db-start

(
  cd "$ROOT"
  TEST_DB_DSN="${TEST_DB_DSN:-root:@tcp(127.0.0.1:4000)/gh-server?parseTime=true&timeout=10s}" \
    go test ./internal/service \
    -run 'TestRepoLifecycle_TransferRepo_RollbackOnUpdateFailure|TestForkRepoDBFinalizeFailureCompensatesDBAndGit' \
    -count=1 -v
)

ok "rollback compensation checks passed"
