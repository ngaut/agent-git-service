#!/bin/bash
# E2E smoke test for GitHub-compatible code search in single-DB mode.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./lib.sh
source "$SCRIPT_DIR/lib.sh"

require_cmd curl
require_cmd jq
require_cmd make
require_cmd mysql

RANDOM_STRING="$(head /dev/urandom | tr -dc 'a-z0-9' | head -c 6)"
ORG="${GH_ORG:-testadmin}"
SERVER_PORT="${E2E_PORT:-$((RANDOM % 10000 + 20000))}"
BASE_URL="http://localhost:$SERVER_PORT"
ADMIN_LOGIN="$ORG"
ADMIN_TOKEN="e2e-token-$RANDOM_STRING"
export GH_TOKEN="$ADMIN_TOKEN"

TIDB_DB_NAME=""
DROP_TIDB_DB_ON_CLEANUP="false"
if [ -n "${DB_DSN:-}" ]; then
  DEFAULT_DB_DSN="$DB_DSN"
else
  TIDB_DB_NAME="code_search_e2e_${RANDOM_STRING}"
  DEFAULT_DB_DSN="root:@tcp(127.0.0.1:4000)/${TIDB_DB_NAME}?parseTime=true&timeout=10s"
  DROP_TIDB_DB_ON_CLEANUP="true"
fi

GIT_REPO_DIR="$(mktemp -d /tmp/gh-server-code-search-repos.XXXXXX)"
REPO_NAME="code-search-$RANDOM_STRING"
MARKER="CODE_SEARCH_MARKER_${RANDOM_STRING}"

MYSQL_ARGS=(-h 127.0.0.1 -P 4000 -u root)

mysql_exec() {
  local stmt="$1"
  mysql "${MYSQL_ARGS[@]}" -e "$stmt"
}

cleanup() {
  note "Cleaning up..."
  if [ -n "${SERVER_PID:-}" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$GIT_REPO_DIR"
  if [ "$DROP_TIDB_DB_ON_CLEANUP" = "true" ] && [ -n "$TIDB_DB_NAME" ]; then
    mysql_exec "DROP DATABASE IF EXISTS \`$TIDB_DB_NAME\`" 2>/dev/null || true
  fi
}
trap cleanup EXIT

create_repo() {
  curl_json 201 \
    -X POST "$BASE_URL/api/v3/user/repos" \
    -H "Authorization: token $GH_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"$REPO_NAME\",\"private\":true,\"add_readme\":true}"
}

create_file() {
  local encoded
  encoded="$(printf "%s" "package main
// $MARKER
func codeSearchMarker() {}" | base64 | tr -d '\n')"
  curl_json 201 \
    -X PUT "$BASE_URL/api/v3/repos/$ORG/$REPO_NAME/contents/src/search_marker.go" \
    -H "Authorization: token $GH_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"message\":\"Add code search marker\",\"content\":\"$encoded\"}"
}

note "=== Code Search E2E Test ==="
note "Random suffix: $RANDOM_STRING"

if [ "$DROP_TIDB_DB_ON_CLEANUP" = "true" ]; then
  note "Creating isolated TiDB database: $TIDB_DB_NAME"
  mysql_exec "CREATE DATABASE IF NOT EXISTS \`$TIDB_DB_NAME\`"
fi

note "Building gh-server..."
make build

note "Starting gh-server..."
ENVIRONMENT=development \
LISTEN_MODE=production \
PORT="$SERVER_PORT" \
BASE_URL="$BASE_URL" \
ADMIN_LOGIN="$ADMIN_LOGIN" \
ADMIN_TOKEN="$ADMIN_TOKEN" \
DB_DSN="$DEFAULT_DB_DSN" \
GIT_REPO_DIR="$GIT_REPO_DIR" \
./gh-server > /tmp/gh-server-code-search-e2e.log 2>&1 &
SERVER_PID=$!

wait_for_http_ready \
  "$BASE_URL/api/v3/" \
  "Waiting for gh-server to start..." \
  "server running on port $SERVER_PORT" \
  "gh-server failed to start"

note "Creating searchable repository..."
create_repo > /dev/null
create_file > /dev/null
sleep 1

RESP="$(curl_json 200 \
  -H "Authorization: token $GH_TOKEN" \
  --get "$BASE_URL/api/v3/search/code" \
  --data-urlencode "q=$MARKER")"

assert_eq "$(jq -r '.total_count' <<<"$RESP")" "1"
assert_eq "$(jq -r '.items | length' <<<"$RESP")" "1"
assert_eq "$(jq -r '.items[0].path' <<<"$RESP")" "src/search_marker.go"
assert_eq "$(jq -r '.items[0].repository.full_name' <<<"$RESP")" "$ORG/$REPO_NAME"

ok "code search returns the indexed marker"
