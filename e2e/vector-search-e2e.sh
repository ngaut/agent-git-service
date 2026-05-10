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
CONTROL_PLANE_DB_NAME=""
TENANT_A_DB_NAME=""
TENANT_B_DB_NAME=""
TENANT_C_DB_NAME=""
CONTROL_PLANE_DSN=""
TENANT_A_DSN=""
TENANT_B_DSN=""
TENANT_C_DSN=""
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

mysql_exec_db() {
  local db="$1"
  local stmt="$2"
  mysql "${MYSQL_ARGS[@]}" "$db" -e "$stmt"
}

dsn_for_db() {
  local db_name="$1"
  local base="${DEFAULT_DB_DSN%%\?*}"
  local params=""
  if [[ "$DEFAULT_DB_DSN" == *"?"* ]]; then
    params="${DEFAULT_DB_DSN#*\?}"
  fi
  local prefix="${base%/*}"
  echo "${prefix}/${db_name}${params:+?${params}}"
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
  if [ "$DROP_TIDB_DB_ON_CLEANUP" = "true" ] && [ -n "$CONTROL_PLANE_DB_NAME" ]; then
    mysql_exec "DROP DATABASE IF EXISTS \`$CONTROL_PLANE_DB_NAME\`" 2>/dev/null || true
  fi
  if [ "$DROP_TIDB_DB_ON_CLEANUP" = "true" ] && [ -n "$TENANT_A_DB_NAME" ]; then
    mysql_exec "DROP DATABASE IF EXISTS \`$TENANT_A_DB_NAME\`" 2>/dev/null || true
  fi
  if [ "$DROP_TIDB_DB_ON_CLEANUP" = "true" ] && [ -n "$TENANT_B_DB_NAME" ]; then
    mysql_exec "DROP DATABASE IF EXISTS \`$TENANT_B_DB_NAME\`" 2>/dev/null || true
  fi
  if [ "$DROP_TIDB_DB_ON_CLEANUP" = "true" ] && [ -n "$TENANT_C_DB_NAME" ]; then
    mysql_exec "DROP DATABASE IF EXISTS \`$TENANT_C_DB_NAME\`" 2>/dev/null || true
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

init_control_plane_schema() {
  note "Creating control plane schema..."
  mysql_exec_db "$CONTROL_PLANE_DB_NAME" "CREATE TABLE IF NOT EXISTS cpusers (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    login VARCHAR(255) NOT NULL UNIQUE,
    email VARCHAR(255),
    dsn VARCHAR(2048),
    state VARCHAR(32) NOT NULL DEFAULT 'pending',
    failure_reason VARCHAR(1024),
    created_at DATETIME(3),
    updated_at DATETIME(3)
  )"
  mysql_exec_db "$CONTROL_PLANE_DB_NAME" "CREATE TABLE IF NOT EXISTS cp_tokens (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    value VARCHAR(255) NOT NULL UNIQUE,
    cpuser_id BIGINT UNSIGNED NOT NULL,
    created_at DATETIME(3),
    INDEX idx_cpuser_id (cpuser_id),
    CONSTRAINT fk_cp_tokens_cpuser FOREIGN KEY (cpuser_id) REFERENCES cpusers(id)
  )"
  ok "Control plane schema created"
}

seed_control_plane_user() {
  local login="$1"
  local token="$2"
  local dsn="$3"
  mysql_exec_db "$CONTROL_PLANE_DB_NAME" "INSERT INTO cpusers (login, email, dsn, state, created_at, updated_at) VALUES ('${login}', '${login}@e2e.local', '${dsn}', 'active', NOW(3), NOW(3))"
  mysql_exec_db "$CONTROL_PLANE_DB_NAME" "INSERT INTO cp_tokens (cpuser_id, value, created_at) SELECT id, '${token}', NOW(3) FROM cpusers WHERE login='${login}'"
}

start_gh_server() {
  local dsn="$1"
  local control_plane_dsn="${2:-}"
  if [ -n "$control_plane_dsn" ]; then
    note "Starting gh-server (DB_DSN=$dsn, CONTROL_PLANE_DSN=$control_plane_dsn)"
  else
    note "Starting gh-server (DB_DSN=$dsn)"
  fi
  ENVIRONMENT=development \
  LISTEN_MODE=production \
  PORT="$SERVER_PORT" \
  BASE_URL="$BASE_URL" \
  CONTROL_PLANE_DSN="$control_plane_dsn" \
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

assert_issue_absent() {
  local resp="$1"
  local title="$2"
  if jq -e --arg title "$title" '.items[]? | select(.title == $title)' >/dev/null <<<"$resp"; then
    echo "unexpected issue found: $title" >&2
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

# Step 10: Multi-tenant vector isolation and discoverability (control-plane mode)
note "Restarting gh-server with control plane for multi-tenant vector isolation..."
stop_gh_server

CONTROL_PLANE_DB_NAME="vector_cp_${RANDOM_STRING}"
TENANT_A_DB_NAME="vector_tenant_a_${RANDOM_STRING}"
TENANT_B_DB_NAME="vector_tenant_b_${RANDOM_STRING}"
TENANT_C_DB_NAME="vector_tenant_c_${RANDOM_STRING}"
CONTROL_PLANE_DSN="$(dsn_for_db "$CONTROL_PLANE_DB_NAME")"
TENANT_A_DSN="$(dsn_for_db "$TENANT_A_DB_NAME")"
TENANT_B_DSN="$(dsn_for_db "$TENANT_B_DB_NAME")"
TENANT_C_DSN="$(dsn_for_db "$TENANT_C_DB_NAME")"

note "Creating control plane and tenant databases..."
mysql_exec "CREATE DATABASE IF NOT EXISTS \`$CONTROL_PLANE_DB_NAME\`"
mysql_exec "CREATE DATABASE IF NOT EXISTS \`$TENANT_A_DB_NAME\`"
mysql_exec "CREATE DATABASE IF NOT EXISTS \`$TENANT_B_DB_NAME\`"
mysql_exec "CREATE DATABASE IF NOT EXISTS \`$TENANT_C_DB_NAME\`"

start_gh_server "$DEFAULT_DB_DSN" "$CONTROL_PLANE_DSN"

TENANT_A_LOGIN="tenant-a-$RANDOM_STRING"
TENANT_B_LOGIN="tenant-b-$RANDOM_STRING"
TENANT_C_LOGIN="tenant-c-$RANDOM_STRING"
TENANT_A_TOKEN="tenant-a-token-$RANDOM_STRING"
TENANT_B_TOKEN="tenant-b-token-$RANDOM_STRING"
TENANT_C_TOKEN="tenant-c-token-$RANDOM_STRING"

note "Initializing control plane schema..."
init_control_plane_schema
note "Seeding control plane tenants..."
seed_control_plane_user "$TENANT_A_LOGIN" "$TENANT_A_TOKEN" "$TENANT_A_DSN"
seed_control_plane_user "$TENANT_B_LOGIN" "$TENANT_B_TOKEN" "$TENANT_B_DSN"
seed_control_plane_user "$TENANT_C_LOGIN" "$TENANT_C_TOKEN" "$TENANT_C_DSN"

curl_json 200 -H "Authorization: token $TENANT_A_TOKEN" "$BASE_URL/api/v3/user" > /dev/null
curl_json 200 -H "Authorization: token $TENANT_B_TOKEN" "$BASE_URL/api/v3/user" > /dev/null
curl_json 200 -H "Authorization: token $TENANT_C_TOKEN" "$BASE_URL/api/v3/user" > /dev/null

TENANT_A_REPO_NAME="vector-tenant-a-$RANDOM_STRING"
TENANT_B_REPO_NAME="vector-tenant-b-$RANDOM_STRING"

note "Creating tenant A repository $TENANT_A_LOGIN/$TENANT_A_REPO_NAME..."
create_repo "$TENANT_A_REPO_NAME" "$TENANT_A_TOKEN" > /dev/null
TENANT_A_REPO_FULL="$TENANT_A_LOGIN/$TENANT_A_REPO_NAME"
add_repo_cleanup "$TENANT_A_REPO_FULL" "$TENANT_A_TOKEN"

note "Creating tenant B repository $TENANT_B_LOGIN/$TENANT_B_REPO_NAME..."
create_repo "$TENANT_B_REPO_NAME" "$TENANT_B_TOKEN" > /dev/null
TENANT_B_REPO_FULL="$TENANT_B_LOGIN/$TENANT_B_REPO_NAME"
add_repo_cleanup "$TENANT_B_REPO_FULL" "$TENANT_B_TOKEN"

note "Creating tenant C repository..."
TENANT_C_REPO_RESP="$(create_repo "tenant-c-repo-$RANDOM_STRING" "$TENANT_C_TOKEN")"
TENANT_C_REPO_FULL="$(json_get full_name <<<"$TENANT_C_REPO_RESP")"
add_repo_cleanup "$TENANT_C_REPO_FULL" "$TENANT_C_TOKEN"

VECTOR_QUERY="vector-isolation-probe-$RANDOM_STRING"
VECTOR_BODY="authentication regression in multi-tenant login $VECTOR_QUERY"
TENANT_C_BODY="tenant C workspace content $RANDOM_STRING $VECTOR_QUERY"

VEC_ISSUE_A_TITLE="Tenant A vector issue $RANDOM_STRING"
VEC_ISSUE_B_TITLE="Tenant B vector issue $RANDOM_STRING"
VEC_ISSUE_TENANT_C_TITLE="Tenant C vector issue $RANDOM_STRING"
VEC_PR_A_TITLE="Tenant A vector PR $RANDOM_STRING"
VEC_PR_B_TITLE="Tenant B vector PR $RANDOM_STRING"
VEC_PR_TENANT_C_TITLE="Tenant C vector PR $RANDOM_STRING"

note "Creating tenant issues and PRs for vector isolation..."
create_issue "$TENANT_A_REPO_FULL" "$VEC_ISSUE_A_TITLE" "$VECTOR_BODY" "$TENANT_A_TOKEN" > /dev/null
create_issue "$TENANT_B_REPO_FULL" "$VEC_ISSUE_B_TITLE" "$VECTOR_BODY" "$TENANT_B_TOKEN" > /dev/null
create_issue "$TENANT_C_REPO_FULL" "$VEC_ISSUE_TENANT_C_TITLE" "$TENANT_C_BODY" "$TENANT_C_TOKEN" > /dev/null

create_pr "$TENANT_A_REPO_FULL" "$VEC_PR_A_TITLE" "$VECTOR_BODY" "feature-a" "main" "$TENANT_A_TOKEN" > /dev/null
create_pr "$TENANT_B_REPO_FULL" "$VEC_PR_B_TITLE" "$VECTOR_BODY" "feature-b" "main" "$TENANT_B_TOKEN" > /dev/null
create_pr "$TENANT_C_REPO_FULL" "$VEC_PR_TENANT_C_TITLE" "$TENANT_C_BODY" "feature-tenant-c" "main" "$TENANT_C_TOKEN" > /dev/null

TIMEOUT_ISSUE_A_TITLE="Tenant A timeout issue $RANDOM_STRING"
TIMEOUT_ISSUE_B_TITLE="Tenant B timeout issue $RANDOM_STRING"
OUTAGE_PR_A_TITLE="Tenant A outage PR $RANDOM_STRING"
OUTAGE_PR_B_TITLE="Tenant B outage PR $RANDOM_STRING"

note "Creating tenant issues and PRs for fallback isolation..."
create_issue "$TENANT_A_REPO_FULL" "$TIMEOUT_ISSUE_A_TITLE" "trigger __e2e_timeout__" "$TENANT_A_TOKEN" > /dev/null
create_issue "$TENANT_B_REPO_FULL" "$TIMEOUT_ISSUE_B_TITLE" "trigger __e2e_timeout__" "$TENANT_B_TOKEN" > /dev/null
create_pr "$TENANT_A_REPO_FULL" "$OUTAGE_PR_A_TITLE" "trigger __e2e_outage__" "feature-timeout-a" "main" "$TENANT_A_TOKEN" > /dev/null
create_pr "$TENANT_B_REPO_FULL" "$OUTAGE_PR_B_TITLE" "trigger __e2e_outage__" "feature-timeout-b" "main" "$TENANT_B_TOKEN" > /dev/null

note "Waiting for async embedding (multi-tenant)..."
sleep 10

note "Tenant A vector issue search (no repo qualifier)..."
MT_ISSUE_VEC_RESP="$(search_issues "$VECTOR_QUERY" 15 "$TENANT_A_TOKEN")"
assert_search_contract "$MT_ISSUE_VEC_RESP"
assert_issue_present "$MT_ISSUE_VEC_RESP" "$VEC_ISSUE_A_TITLE"
assert_issue_absent "$MT_ISSUE_VEC_RESP" "$VEC_ISSUE_B_TITLE"
assert_issue_absent "$MT_ISSUE_VEC_RESP" "$VEC_ISSUE_TENANT_C_TITLE"
ok "tenant A issue search isolated in vector phase"

note "Tenant A vector PR search (no repo qualifier)..."
MT_PR_VEC_RESP="$(search_issues "$VECTOR_QUERY is:pr" 15 "$TENANT_A_TOKEN")"
assert_search_contract "$MT_PR_VEC_RESP"
assert_issue_present "$MT_PR_VEC_RESP" "$VEC_PR_A_TITLE"
assert_issue_absent "$MT_PR_VEC_RESP" "$VEC_PR_B_TITLE"
assert_issue_absent "$MT_PR_VEC_RESP" "$VEC_PR_TENANT_C_TITLE"
ok "tenant A PR search isolated in vector phase"

note "Tenant C vector issue search (discoverability)..."
TENANT_C_ISSUE_VEC_RESP="$(search_issues "$VECTOR_QUERY" 15 "$TENANT_C_TOKEN")"
assert_search_contract "$TENANT_C_ISSUE_VEC_RESP"
assert_issue_present "$TENANT_C_ISSUE_VEC_RESP" "$VEC_ISSUE_TENANT_C_TITLE"
assert_issue_absent "$TENANT_C_ISSUE_VEC_RESP" "$VEC_ISSUE_A_TITLE"
ok "tenant C issue search limited to owned repos"

note "Tenant C vector PR search (discoverability)..."
TENANT_C_PR_VEC_RESP="$(search_issues "$VECTOR_QUERY is:pr" 15 "$TENANT_C_TOKEN")"
assert_search_contract "$TENANT_C_PR_VEC_RESP"
assert_issue_present "$TENANT_C_PR_VEC_RESP" "$VEC_PR_TENANT_C_TITLE"
assert_issue_absent "$TENANT_C_PR_VEC_RESP" "$VEC_PR_A_TITLE"
ok "tenant C PR search limited to owned repos"

note "Case: embedder timeout (multi-tenant issue search)"
MT_TIMEOUT_RESP="$(search_issues "__e2e_timeout__" 25 "$TENANT_A_TOKEN")"
assert_search_contract "$MT_TIMEOUT_RESP"
assert_issue_present "$MT_TIMEOUT_RESP" "$TIMEOUT_ISSUE_A_TITLE"
assert_issue_absent "$MT_TIMEOUT_RESP" "$TIMEOUT_ISSUE_B_TITLE"
ok "tenant A issue search isolated during timeout fallback"

note "Case: embedder outage (multi-tenant PR search)"
MT_OUTAGE_RESP="$(search_issues "__e2e_outage__ is:pr" 15 "$TENANT_A_TOKEN")"
assert_search_contract "$MT_OUTAGE_RESP"
assert_issue_present "$MT_OUTAGE_RESP" "$OUTAGE_PR_A_TITLE"
assert_issue_absent "$MT_OUTAGE_RESP" "$OUTAGE_PR_B_TITLE"
ok "tenant A PR search isolated during outage fallback"

# Step 11: Vector DB query failure handling (SQLite has no VEC_COSINE_DISTANCE)
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
