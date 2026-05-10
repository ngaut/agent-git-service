#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=./lib.sh
source "$ROOT/e2e/lib.sh"

require_cmd curl
require_cmd jq
require_cmd make
require_cmd python3

RANDOM_SUFFIX="$(date +%s)-$RANDOM"
SERVER_PORT="${E2E_PORT:-$((RANDOM % 10000 + 20000))}"
BASE_URL="http://localhost:$SERVER_PORT"
ADMIN_LOGIN="e2e-admin-$RANDOM_SUFFIX"
ADMIN_TOKEN="e2e-token-$RANDOM_SUFFIX"
SQLITE_DB="/tmp/gh-server-rollback-e2e-$RANDOM_SUFFIX.db"
DB_DSN="sqlite:$SQLITE_DB"
GIT_REPO_DIR="$(mktemp -d /tmp/gh-server-rollback-repos.XXXXXX)"
LOG_FILE="/tmp/gh-server-rollback-e2e.log"

OWNER_A="alice-$RANDOM_SUFFIX"
OWNER_B="bob-$RANDOM_SUFFIX"
OWNER_SRC="src-$RANDOM_SUFFIX"
OWNER_FORK="forker-$RANDOM_SUFFIX"
TRANSFER_REPO="transfer-$RANDOM_SUFFIX"
FORK_REPO="fork-$RANDOM_SUFFIX"

cleanup() {
  note "Cleaning up..."
  if [[ -n "${SERVER_PID:-}" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  if [[ -n "${GIT_REPO_DIR:-}" && -d "$GIT_REPO_DIR" ]]; then
    rm -rf "$GIT_REPO_DIR"
  fi
  if [[ -f "$SQLITE_DB" ]]; then
    rm -f "$SQLITE_DB"
  fi
}
trap cleanup EXIT

start_server() {
  note "Building gh-server..."
  make -C "$ROOT" build
  note "Starting gh-server (DB_DSN=$DB_DSN, GIT_REPO_DIR=$GIT_REPO_DIR)"
  ENVIRONMENT=development \
  LISTEN_MODE=production \
  PORT="$SERVER_PORT" \
  ADMIN_LOGIN="$ADMIN_LOGIN" \
  ADMIN_TOKEN="$ADMIN_TOKEN" \
  DB_DSN="$DB_DSN" \
  GIT_REPO_DIR="$GIT_REPO_DIR" \
  "$ROOT/gh-server" > "$LOG_FILE" 2>&1 &
  SERVER_PID=$!
  wait_for_http_ready \
    "$BASE_URL/api/v3/" \
    "Waiting for gh-server to start on $BASE_URL..." \
    "server running on port $SERVER_PORT" \
    "gh-server failed to start (see $LOG_FILE)"
}

repo_path() {
  local full_name="$1"
  echo "$GIT_REPO_DIR/$full_name.git"
}

create_org_repo() {
  local owner="$1"
  local name="$2"
  ensure_org_exists "$BASE_URL" "$ADMIN_TOKEN" "$owner"
  curl_json 201 \
    -X POST "$BASE_URL/api/v3/orgs/$owner/repos" \
    -H "Authorization: token $ADMIN_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"$name\",\"add_readme\":true}"
}

assert_repo_code() {
  local owner="$1"
  local repo="$2"
  local expect="$3"
  local code
  code="$(http_code -H "Authorization: token $ADMIN_TOKEN" "$BASE_URL/api/v3/repos/$owner/$repo")"
  assert_eq "$code" "$expect"
}

sqlite_exec() {
  local sql="$1"
  SQLITE_DB="$SQLITE_DB" SQLITE_SQL="$sql" python3 - <<'PY'
import os
import sqlite3

db_path = os.environ["SQLITE_DB"]
sql = os.environ["SQLITE_SQL"]
conn = sqlite3.connect(db_path, timeout=5)
conn.executescript(sql)
conn.commit()
conn.close()
PY
}

start_server

note "=== Transfer rollback compensation ==="
note "Ensuring transfer target org exists: $OWNER_B"
ensure_org_exists "$BASE_URL" "$ADMIN_TOKEN" "$OWNER_B"
note "Creating repo $OWNER_A/$TRANSFER_REPO"
create_org_repo "$OWNER_A" "$TRANSFER_REPO" >/dev/null

transfer_src_path="$(repo_path "$OWNER_A/$TRANSFER_REPO")"
transfer_target_path="$(repo_path "$OWNER_B/$TRANSFER_REPO")"
if [[ ! -d "$transfer_src_path" ]]; then
  echo "expected git path to exist: $transfer_src_path" >&2
  exit 1
fi
if [[ -d "$transfer_target_path" ]]; then
  echo "unexpected git path already exists: $transfer_target_path" >&2
  exit 1
fi

transfer_trigger_sql=$(cat <<SQL
DROP TRIGGER IF EXISTS e2e_transfer_fail;
CREATE TRIGGER e2e_transfer_fail
BEFORE UPDATE ON repositories
WHEN NEW.full_name = '$OWNER_B/$TRANSFER_REPO'
  AND OLD.full_name = '$OWNER_A/$TRANSFER_REPO'
BEGIN
  SELECT RAISE(FAIL, 'e2e transfer update failure');
END;
SQL
)
sqlite_exec "$transfer_trigger_sql"

note "Attempting transfer with injected DB failure"
code="$(http_code -X POST "$BASE_URL/api/v3/repos/$OWNER_A/$TRANSFER_REPO/transfer" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"new_owner\":\"$OWNER_B\"}")"
assert_eq "$code" "500"

assert_repo_code "$OWNER_A" "$TRANSFER_REPO" "200"
assert_repo_code "$OWNER_B" "$TRANSFER_REPO" "404"
if [[ ! -d "$transfer_src_path" ]]; then
  echo "expected source git path to remain after rollback: $transfer_src_path" >&2
  exit 1
fi
if [[ -d "$transfer_target_path" ]]; then
  echo "expected target git path to be absent after rollback: $transfer_target_path" >&2
  exit 1
fi
ok "transfer rollback restored DB + git consistency"

sqlite_exec "DROP TRIGGER IF EXISTS e2e_transfer_fail;"

note "=== Fork rollback compensation ==="
note "Ensuring fork target org exists: $OWNER_FORK"
ensure_org_exists "$BASE_URL" "$ADMIN_TOKEN" "$OWNER_FORK"
note "Creating source repo $OWNER_SRC/$FORK_REPO"
create_org_repo "$OWNER_SRC" "$FORK_REPO" >/dev/null

fork_src_path="$(repo_path "$OWNER_SRC/$FORK_REPO")"
fork_target_path="$(repo_path "$OWNER_FORK/$FORK_REPO")"
if [[ ! -d "$fork_src_path" ]]; then
  echo "expected fork source git path to exist: $fork_src_path" >&2
  exit 1
fi
if [[ -d "$fork_target_path" ]]; then
  echo "unexpected fork target git path already exists: $fork_target_path" >&2
  exit 1
fi

fork_trigger_sql=$(cat <<SQL
DROP TRIGGER IF EXISTS e2e_fork_finalize_fail;
CREATE TRIGGER e2e_fork_finalize_fail
BEFORE UPDATE ON repositories
WHEN NEW.full_name = '$OWNER_FORK/$FORK_REPO'
  AND NEW.fork = 1
BEGIN
  SELECT RAISE(FAIL, 'e2e fork finalize failure');
END;
SQL
)
sqlite_exec "$fork_trigger_sql"

note "Attempting fork with injected DB finalize failure"
code="$(http_code -X POST "$BASE_URL/api/v3/repos/$OWNER_SRC/$FORK_REPO/forks" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"organization\":\"$OWNER_FORK\"}")"
assert_eq "$code" "422"

assert_repo_code "$OWNER_FORK" "$FORK_REPO" "404"
if [[ -d "$fork_target_path" ]]; then
  echo "expected fork git path to be removed after compensation: $fork_target_path" >&2
  exit 1
fi
assert_repo_code "$OWNER_SRC" "$FORK_REPO" "200"
if [[ ! -d "$fork_src_path" ]]; then
  echo "expected source git path to remain after fork failure: $fork_src_path" >&2
  exit 1
fi
ok "fork rollback cleaned DB + git state"

sqlite_exec "DROP TRIGGER IF EXISTS e2e_fork_finalize_fail;"

ok "rollback compensation e2e checks completed"
