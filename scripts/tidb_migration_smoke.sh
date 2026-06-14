#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=/dev/null
source "$ROOT_DIR/e2e/lib.sh"

require_cmd curl
require_cmd go
require_cmd mysql
require_cmd python3

pick_port() {
  python3 - <<'PY'
import socket

sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
sock.bind(("127.0.0.1", 0))
print(sock.getsockname()[1])
sock.close()
PY
}

RANDOM_SUFFIX="$(date +%s)_$RANDOM"
DB_NAME="${MIGRATE_SMOKE_DB:-gh_server_migrate_smoke_$RANDOM_SUFFIX}"
if ! [[ "$DB_NAME" =~ ^[A-Za-z0-9_]+$ ]]; then
  echo "MIGRATE_SMOKE_DB must contain only letters, numbers, and underscores: $DB_NAME" >&2
  exit 1
fi

TIDB_HOST="${TIDB_HOST:-127.0.0.1}"
TIDB_PORT="${TIDB_PORT:-4000}"
TIDB_USER="${TIDB_USER:-root}"
TIDB_PASSWORD="${TIDB_PASSWORD:-}"
SERVER_PORT="${MIGRATE_SMOKE_PORT:-}"
if [[ -z "$SERVER_PORT" ]]; then
  SERVER_PORT="$(pick_port 2>/dev/null || echo 18081)"
fi
BASE_URL="${BASE_URL:-http://127.0.0.1:$SERVER_PORT}"
ADMIN_LOGIN="${ADMIN_LOGIN:-migrate-admin-$RANDOM_SUFFIX}"
ADMIN_TOKEN="${ADMIN_TOKEN:-migrate-token-$RANDOM_SUFFIX}"
GIT_REPO_DIR="$(mktemp -d /tmp/gh-server-migrate-repos.XXXXXX)"
SERVER_BIN="$(mktemp /tmp/gh-server-migrate-bin.XXXXXX)"
LOG_FILE="${LOG_FILE:-/tmp/gh-server-migrate-$RANDOM_SUFFIX.log}"
WAIT_SECONDS="${MIGRATE_SMOKE_WAIT_SECONDS:-90}"

mysql_args=(-h "$TIDB_HOST" -P "$TIDB_PORT" -u "$TIDB_USER")
if [[ -n "$TIDB_PASSWORD" ]]; then
  mysql_args+=("-p$TIDB_PASSWORD")
fi

db_auth="$TIDB_USER:"
if [[ -n "$TIDB_PASSWORD" ]]; then
  db_auth="$TIDB_USER:$TIDB_PASSWORD"
fi
DB_DSN="$db_auth@tcp($TIDB_HOST:$TIDB_PORT)/$DB_NAME?parseTime=true&timeout=10s"

cleanup() {
  local status=$?
  if [[ -n "${SERVER_PID:-}" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  if [[ $status -ne 0 && -f "$LOG_FILE" ]]; then
    echo "--- gh-server TiDB migration smoke log tail ---" >&2
    tail -n 80 "$LOG_FILE" >&2 || true
  fi
  mysql "${mysql_args[@]}" -e "DROP DATABASE IF EXISTS \`$DB_NAME\`" >/dev/null 2>&1 || true
  rm -rf "$GIT_REPO_DIR"
  rm -f "$SERVER_BIN"
}
trap cleanup EXIT

note "Preparing TiDB migration smoke database $DB_NAME"
mysql "${mysql_args[@]}" -e "DROP DATABASE IF EXISTS \`$DB_NAME\`; CREATE DATABASE \`$DB_NAME\`;"

note "Building gh-server"
(
  cd "$ROOT_DIR"
  go build -o "$SERVER_BIN" ./cmd/gh-server
)

note "Starting gh-server against TiDB on $BASE_URL"
ENVIRONMENT=development \
LISTEN_MODE=production \
PORT="$SERVER_PORT" \
BASE_URL="$BASE_URL" \
ADMIN_LOGIN="$ADMIN_LOGIN" \
ADMIN_TOKEN="$ADMIN_TOKEN" \
DB_DSN="$DB_DSN" \
GIT_REPO_DIR="$GIT_REPO_DIR" \
"$SERVER_BIN" >"$LOG_FILE" 2>&1 &
SERVER_PID=$!

wait_for_http_ready \
  "$BASE_URL/readyz" \
  "Waiting for TiDB-backed gh-server to finish bootstrap and migrations..." \
  "TiDB-backed gh-server is ready" \
  "TiDB migration smoke failed before readiness (see $LOG_FILE)" \
  "$WAIT_SECONDS"

note "Checking migrated TiDB schema"
mysql "${mysql_args[@]}" "$DB_NAME" -e "SHOW TABLES LIKE 'users';" | grep -q '^users$'

echo "TiDB migration smoke passed."
