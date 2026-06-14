#!/bin/bash
# E2E test for vector search with mock embedding server.
# Tests semantic search end-to-end plus failure handling.
#
# Prerequisites:
# - TiDB running (SQLite doesn't support VECTOR operations for the success case)
# - Go installed (for mock server and gh-server build)
# - gh CLI configured
#
# Usage: ./e2e/vector-search-e2e.sh

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./lib.sh
source "$SCRIPT_DIR/lib.sh"

require_cmd curl
require_cmd jq
require_cmd go
require_cmd make
require_cmd mysql

RANDOM_STRING=$(head /dev/urandom | tr -dc 'a-z0-9' | head -c 6)
ORG="${GH_ORG:-testadmin}"
EMBEDDING_PORT=":8888"
SERVER_PORT="${E2E_PORT:-$((RANDOM % 10000 + 20000))}"
BASE_URL="http://localhost:$SERVER_PORT"
export GH_HOST="${GH_HOST:-localhost:$SERVER_PORT}"
ADMIN_LOGIN="$ORG"
ADMIN_TOKEN="e2e-token-$RANDOM_STRING"
export GH_TOKEN="$ADMIN_TOKEN"

TIDB_DB_NAME=""
DROP_TIDB_DB_ON_CLEANUP="false"
if [ -n "${DB_DSN:-}" ]; then
  DEFAULT_DB_DSN="$DB_DSN"
else
  TIDB_DB_NAME="vector_e2e_${RANDOM_STRING}"
  DEFAULT_DB_DSN="root:@tcp(127.0.0.1:4000)/${TIDB_DB_NAME}?parseTime=true&timeout=10s"
  DROP_TIDB_DB_ON_CLEANUP="true"
fi
SQLITE_DB="/tmp/gh-server-vector-e2e-$RANDOM_STRING.db"
SQLITE_DSN="sqlite:$SQLITE_DB"
GIT_REPO_DIR="$(mktemp -d /tmp/gh-server-e2e-repos.XXXXXX)"

REPOS_TO_DELETE=()

note "=== Vector Search E2E Test ==="
note "Random suffix: $RANDOM_STRING"

MYSQL_HOST="127.0.0.1"
MYSQL_PORT="4000"
MYSQL_USER="root"
MYSQL_PASS=""
MYSQL_ARGS=()

init_mysql_args() {
  local dsn="$1"
  local base="${dsn%%\?*}"
  local prefix="${base%%/*}"
  if [[ "$prefix" == *"@tcp("*")"* ]]; then
    local userpass="${prefix%@tcp(*}"
    local hostport="${prefix##*@tcp(}"
    hostport="${hostport%)}"
    if [[ "$userpass" == *":"* ]]; then
      MYSQL_USER="${userpass%%:*}"
      MYSQL_PASS="${userpass#*:}"
    else
      MYSQL_USER="$userpass"
      MYSQL_PASS=""
    fi
    if [[ "$hostport" == *":"* ]]; then
      MYSQL_HOST="${hostport%%:*}"
      MYSQL_PORT="${hostport##*:}"
    else
      MYSQL_HOST="$hostport"
      MYSQL_PORT="3306"
    fi
  fi
  MYSQL_ARGS=(-h "$MYSQL_HOST" -P "$MYSQL_PORT" -u "$MYSQL_USER")
  if [ -n "$MYSQL_PASS" ]; then
    MYSQL_ARGS+=(-p"$MYSQL_PASS")
  fi
}

mysql_exec() {
  local stmt="$1"
  mysql "${MYSQL_ARGS[@]}" -e "$stmt"
}

init_mysql_args "$DEFAULT_DB_DSN"

cleanup() {
  note "Cleaning up..."
  if [ "${#REPOS_TO_DELETE[@]}" -gt 0 ]; then
    for entry in "${REPOS_TO_DELETE[@]}"; do
      local repo token
      token="$GH_TOKEN"
      repo="$entry"
      if [[ "$entry" == *"|"* ]]; then
        token="${entry%%|*}"
        repo="${entry#*|}"
      fi
      code="$(http_code -X DELETE -H "Authorization: token $token" "$BASE_URL/api/v3/repos/$repo" || echo "000")"
      if [ "$code" != "204" ] && [ "$code" != "404" ]; then
        echo "warning: cleanup delete repo $repo returned $code" >&2
      fi
    done
  fi
  if [ -n "${MOCK_PID:-}" ]; then
    kill "$MOCK_PID" 2>/dev/null || true
  fi
  if [ -n "${SERVER_PID:-}" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  if [ -n "${GIT_REPO_DIR:-}" ]; then
    rm -rf "$GIT_REPO_DIR"
  fi
  if [ -f "$SQLITE_DB" ]; then
    rm -f "$SQLITE_DB"
  fi
  if [ "$DROP_TIDB_DB_ON_CLEANUP" = "true" ] && [ -n "$TIDB_DB_NAME" ]; then
    mysql_exec "DROP DATABASE IF EXISTS \`$TIDB_DB_NAME\`" 2>/dev/null || true
  fi
}
trap cleanup EXIT

add_repo_cleanup() {
  local repo="$1"
  local token="${2:-}"
  if [ -n "$token" ]; then
    REPOS_TO_DELETE+=("${token}|${repo}")
  else
    REPOS_TO_DELETE+=("$repo")
  fi
}

create_repo() {
  local name="$1"
  local token="${2:-$GH_TOKEN}"
  curl_json 201 \
    -X POST "$BASE_URL/api/v3/user/repos" \
    -H "Authorization: token $token" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"$name\",\"private\":true,\"add_readme\":true}"
}

create_issue() {
  local repo_full="$1"
  local title="$2"
  local body="$3"
  local token="${4:-$GH_TOKEN}"
  curl_json 201 \
    -X POST "$BASE_URL/api/v3/repos/$repo_full/issues" \
    -H "Authorization: token $token" \
    -H "Content-Type: application/json" \
    -d "{\"title\":\"$title\",\"body\":\"$body\"}"
}

create_pr() {
  local repo_full="$1"
  local title="$2"
  local body="$3"
  local head="$4"
  local base="$5"
  local token="${6:-$GH_TOKEN}"
  curl_json 201 \
    -X POST "$BASE_URL/api/v3/repos/$repo_full/pulls" \
    -H "Authorization: token $token" \
    -H "Content-Type: application/json" \
    -d "{\"title\":\"$title\",\"body\":\"$body\",\"head\":\"$head\",\"base\":\"$base\"}"
}

start_gh_server() {
  local dsn="$1"
  note "Starting gh-server (DB_DSN=$dsn)"
  ENVIRONMENT=development \
  LISTEN_MODE=production \
  PORT="$SERVER_PORT" \
  BASE_URL="$BASE_URL" \
  ADMIN_LOGIN="$ADMIN_LOGIN" \
  ADMIN_TOKEN="$ADMIN_TOKEN" \
  EMBEDDING_BASE_URL="http://localhost${EMBEDDING_PORT#/}" \
  EMBEDDING_API_KEY="test-key" \
  EMBEDDING_MODEL="test-model" \
  EMBEDDING_DIMENSIONS="1536" \
  DB_DSN="$dsn" \
  GIT_REPO_DIR="$GIT_REPO_DIR" \
  ./gh-server > /tmp/gh-server-e2e.log 2>&1 &
  SERVER_PID=$!
  wait_for_http_ready \
    "$BASE_URL/api/v3/" \
    "Waiting for gh-server to start..." \
    "server running on port $SERVER_PORT" \
    "✗ gh-server failed to start"
}

stop_gh_server() {
  if [ -n "${SERVER_PID:-}" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
    unset SERVER_PID
  fi
}

search_issues() {
  local query="$1"
  local max_time="${2:-15}"
  local token="${3:-$GH_TOKEN}"
  curl_json 200 --max-time "$max_time" --get "$BASE_URL/api/v3/search/issues" \
    -H "Authorization: token $token" \
    --data-urlencode "q=$query"
}

assert_search_contract() {
  local resp="$1"
  local total incomplete items_len
  total="$(json_get total_count <<<"$resp")"
  assert_re "$total" '^[0-9]+$'
  if [ "$total" -lt 1 ]; then
    echo "expected total_count >= 1, got $total" >&2
    exit 1
  fi
  incomplete="$(json_get incomplete_results <<<"$resp")"
  if [[ "$incomplete" != "true" && "$incomplete" != "false" ]]; then
    echo "unexpected incomplete_results: $incomplete" >&2
    exit 1
  fi
  items_len="$(jq '.items | length' <<<"$resp")"
  assert_re "$items_len" '^[0-9]+$'
  if [ "$items_len" -lt 1 ]; then
    echo "expected items length >= 1, got $items_len" >&2
    exit 1
  fi
}

assert_issue_present() {
  local resp="$1"
  local title="$2"
  if ! jq -e --arg title "$title" '.items[]? | select(.title == $title)' >/dev/null <<<"$resp"; then
    echo "expected issue not found: $title" >&2
    echo "Results: $resp" >&2
    exit 1
  fi
}

search_until_issue_present() {
  local query="$1"
  local title="$2"
  local max_time="${3:-15}"
  local token="${4:-$GH_TOKEN}"
  local attempts="${5:-30}"
  local delay="${6:-2}"
  local resp=""

  for _ in $(seq 1 "$attempts"); do
    if resp="$(search_issues "$query" "$max_time" "$token")"; then
      if jq -e --arg title "$title" '.items[]? | select(.title == $title)' >/dev/null <<<"$resp"; then
        printf '%s\n' "$resp"
        return 0
      fi
    fi
    sleep "$delay"
  done

  echo "expected issue not found after polling: $title" >&2
  if [ -n "$resp" ]; then
    echo "Last results: $resp" >&2
  fi
  exit 1
}

# Step 1: Start mock embedding server
note "Starting mock embedding server on $EMBEDDING_PORT..."
go run "$SCRIPT_DIR/cmd/mock-embedding-server" "$EMBEDDING_PORT" > /tmp/mock-embedding-e2e.log 2>&1 &
MOCK_PID=$!
mock_ready="false"
for _ in $(seq 1 20); do
  if ! kill -0 "$MOCK_PID" 2>/dev/null; then
    break
  fi
  if curl -sf -X POST "http://localhost${EMBEDDING_PORT#/}/v1/embeddings" \
    -H "Content-Type: application/json" \
    -d '{"input":"healthcheck","model":"test-model"}' > /dev/null 2>&1; then
    mock_ready="true"
    break
  fi
  sleep 1
done

if [ "$mock_ready" != "true" ]; then
  echo "✗ Mock embedding server failed to start" >&2
  if [ -f /tmp/mock-embedding-e2e.log ]; then
    tail -n 20 /tmp/mock-embedding-e2e.log >&2 || true
  fi
  exit 1
fi
ok "mock embedding server running"

# Step 2: Build gh-server
note "Building gh-server..."
make build

if [ "$DROP_TIDB_DB_ON_CLEANUP" = "true" ] && [ -n "$TIDB_DB_NAME" ]; then
  note "Creating isolated TiDB database: $TIDB_DB_NAME"
  mysql_exec "CREATE DATABASE IF NOT EXISTS \`$TIDB_DB_NAME\`"
fi

# Step 3: Start gh-server against TiDB
start_gh_server "$DEFAULT_DB_DSN"

# Step 4: Create test repository
REPO_NAME="vector-e2e-$RANDOM_STRING"
note "Creating repository $ORG/$REPO_NAME..."
create_repo "$REPO_NAME" > /dev/null
add_repo_cleanup "$ORG/$REPO_NAME"

# Step 5: Create issues with semantically related content
note "Creating test issues..."

create_issue "$ORG/$REPO_NAME" "Bug fix in login" "Fixes login authentication bug" > /dev/null
create_issue "$ORG/$REPO_NAME" "Defect repair authentication" "Repairs authentication defect" > /dev/null
create_issue "$ORG/$REPO_NAME" "Feature request dashboard" "New dashboard feature" > /dev/null

# Step 6: Wait for async embedding
note "Waiting for async embedding..."

# Step 7: Search with semantic query
note "Searching for 'authentication defect' (hybrid match to Issue 2)..."
RESULTS="$(search_until_issue_present "authentication defect repo:$ORG/$REPO_NAME" "Defect repair authentication" 15)"
assert_search_contract "$RESULTS"
assert_issue_present "$RESULTS" "Defect repair authentication"
ok "hybrid search working: Issue 2 found"

# Step 8: Embedder timeout/outage handling
TIMEOUT_TITLE="Embedder timeout case $RANDOM_STRING"
OUTAGE_TITLE="Embedder outage case $RANDOM_STRING"

note "Creating timeout/outage issues..."
create_issue "$ORG/$REPO_NAME" "$TIMEOUT_TITLE" "trigger __e2e_timeout__" > /dev/null
create_issue "$ORG/$REPO_NAME" "$OUTAGE_TITLE" "trigger __e2e_outage__" > /dev/null

note "Case: embedder timeout (slow response)"
TIMEOUT_RESP="$(search_issues "__e2e_timeout__ repo:$ORG/$REPO_NAME" 25)"
assert_search_contract "$TIMEOUT_RESP"
assert_issue_present "$TIMEOUT_RESP" "$TIMEOUT_TITLE"
code="$(http_code --max-time 5 "$BASE_URL/api/v3/")"
assert_eq "$code" "200"
ok "timeout handled without crash/hang"

note "Case: embedder outage (503)"
OUTAGE_RESP="$(search_issues "__e2e_outage__ repo:$ORG/$REPO_NAME" 15)"
assert_search_contract "$OUTAGE_RESP"
assert_issue_present "$OUTAGE_RESP" "$OUTAGE_TITLE"
code="$(http_code --max-time 5 "$BASE_URL/api/v3/")"
assert_eq "$code" "200"
ok "outage handled without crash/hang"

# Step 9: Embedder rate limit handling
RATE_LIMIT_TITLE="Embedder rate limit case $RANDOM_STRING"

note "Creating rate-limit issue..."
create_issue "$ORG/$REPO_NAME" "$RATE_LIMIT_TITLE" "trigger __e2e_rate_limit__" > /dev/null

note "Case: embedder rate limit (429)"
RATE_LIMIT_RESP="$(search_issues "__e2e_rate_limit__ repo:$ORG/$REPO_NAME" 15)"
assert_search_contract "$RATE_LIMIT_RESP"
assert_issue_present "$RATE_LIMIT_RESP" "$RATE_LIMIT_TITLE"
code="$(http_code --max-time 5 "$BASE_URL/api/v3/")"
assert_eq "$code" "200"
ok "rate limit handled without crash/hang"

# Step 10: Vector DB query failure handling (SQLite has no VEC_COSINE_DISTANCE)
note "Restarting gh-server with SQLite to force vector query failure..."
stop_gh_server
start_gh_server "$SQLITE_DSN"

REPO_NAME_SQLITE="vector-e2e-sqlite-$RANDOM_STRING"
VEC_FAIL_TITLE="Vector DB failure case $RANDOM_STRING"

note "Creating SQLite repository $ORG/$REPO_NAME_SQLITE..."
create_repo "$REPO_NAME_SQLITE" > /dev/null
add_repo_cleanup "$ORG/$REPO_NAME_SQLITE"

create_issue "$ORG/$REPO_NAME_SQLITE" "$VEC_FAIL_TITLE" "vector-db-failure" > /dev/null

note "Case: vector DB query failure"
VEC_FAIL_RESP="$(search_issues "vector-db-failure repo:$ORG/$REPO_NAME_SQLITE" 15)"
assert_search_contract "$VEC_FAIL_RESP"
assert_issue_present "$VEC_FAIL_RESP" "$VEC_FAIL_TITLE"
code="$(http_code --max-time 5 "$BASE_URL/api/v3/")"
assert_eq "$code" "200"
ok "vector query failure handled without crash/hang"

note "=== Vector Search E2E Test PASSED ==="
