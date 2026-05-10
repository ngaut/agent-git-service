#!/bin/bash
# E2E test for code search tenant/principal isolation.
#
# Coverage:
# - Single-tenant smoke search
# - Multi-principal isolation without repo qualifiers
# - Concurrent multi-principal searches against the same shared term
# - Boundary/result-filter verification with filename/path/repo qualifiers
# - Unauthorized repo qualifiers do not leak file paths or snippets

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./lib.sh
source "$SCRIPT_DIR/lib.sh"

require_cmd curl
require_cmd jq
require_cmd go
require_cmd make
require_cmd mysql

RANDOM_STRING="$(head /dev/urandom | tr -dc 'a-z0-9' | head -c 6)"
ORG="${GH_ORG:-testadmin}"
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
  TIDB_DB_NAME="code_search_e2e_${RANDOM_STRING}"
  DEFAULT_DB_DSN="root:@tcp(127.0.0.1:4000)/${TIDB_DB_NAME}?parseTime=true&timeout=10s"
  DROP_TIDB_DB_ON_CLEANUP="true"
fi

CONTROL_PLANE_DB_NAME=""
TENANT_A_DB_NAME=""
TENANT_B_DB_NAME=""
CONTROL_PLANE_DSN=""
TENANT_A_DSN=""
TENANT_B_DSN=""
SQLITE_DB="/tmp/gh-server-code-search-e2e-$RANDOM_STRING.db"
GIT_REPO_DIR="$(mktemp -d /tmp/gh-server-e2e-repos.XXXXXX)"
SEARCH_TMP_DIR="$(mktemp -d /tmp/gh-server-code-search-results.XXXXXX)"

REPOS_TO_DELETE=()

note "=== Code Search Tenant Isolation E2E Test ==="
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

cleanup() {
  note "Cleaning up..."
  if [ "${#REPOS_TO_DELETE[@]}" -gt 0 ]; then
    for entry in "${REPOS_TO_DELETE[@]}"; do
      local repo token code
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
  if [ -n "${SERVER_PID:-}" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$GIT_REPO_DIR" "$SEARCH_TMP_DIR"
  rm -f "$SQLITE_DB"
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

create_file_in_repo() {
  local repo_full="$1"
  local path="$2"
  local content="$3"
  local message="$4"
  local token="${5:-$GH_TOKEN}"
  local encoded_content
  encoded_content="$(printf "%s" "$content" | base64 -w 0)"
  curl_json 201 \
    -X PUT "$BASE_URL/api/v3/repos/$repo_full/contents/$path" \
    -H "Authorization: token $token" \
    -H "Content-Type: application/json" \
    -d "{\"path\":\"$path\",\"message\":\"$message\",\"content\":\"$encoded_content\"}"
}

search_code() {
  local query="$1"
  local token="${2:-$GH_TOKEN}"
  local accept_header="${3:-}"
  local curl_args=(-H "Authorization: token $token" --get "$BASE_URL/api/v3/search/code" --data-urlencode "q=$query")
  if [ -n "$accept_header" ]; then
    curl_args+=(-H "Accept: $accept_header")
  fi
  curl_json 200 "${curl_args[@]}"
}

run_search_to_file() {
  local query="$1"
  local token="$2"
  local output="$3"
  local accept_header="${4:-}"
  search_code "$query" "$token" "$accept_header" >"$output"
}

assert_search_contract() {
  local resp="$1"
  local total incomplete items_len
  total="$(json_get total_count <<<"$resp")"
  assert_re "$total" '^[0-9]+$'
  incomplete="$(json_get incomplete_results <<<"$resp")"
  if [[ "$incomplete" != "true" && "$incomplete" != "false" ]]; then
    echo "unexpected incomplete_results: $incomplete" >&2
    exit 1
  fi
  items_len="$(jq -r '.items | length' <<<"$resp")"
  assert_re "$items_len" '^[0-9]+$'
}

assert_total_count_eq() {
  local resp="$1"
  local expected="$2"
  local got
  got="$(json_get total_count <<<"$resp")"
  assert_eq "$got" "$expected"
}

assert_items_len_eq() {
  local resp="$1"
  local expected="$2"
  local got
  got="$(jq -r '.items | length' <<<"$resp")"
  assert_eq "$got" "$expected"
}

assert_path_present() {
  local resp="$1"
  local path="$2"
  if ! jq -e --arg path "$path" '.items[]? | select(.path == $path)' >/dev/null <<<"$resp"; then
    echo "expected path not found: $path" >&2
    echo "Results: $resp" >&2
    exit 1
  fi
}

assert_path_absent() {
  local resp="$1"
  local path="$2"
  if jq -e --arg path "$path" '.items[]? | select(.path == $path)' >/dev/null <<<"$resp"; then
    echo "unexpected path found: $path" >&2
    echo "Results: $resp" >&2
    exit 1
  fi
}

assert_path_prefix_present() {
  local resp="$1"
  local prefix="$2"
  if ! jq -e --arg prefix "$prefix" '.items[]? | select(.path | startswith($prefix))' >/dev/null <<<"$resp"; then
    echo "expected path prefix not found: $prefix" >&2
    echo "Results: $resp" >&2
    exit 1
  fi
}

assert_path_prefix_absent() {
  local resp="$1"
  local prefix="$2"
  if jq -e --arg prefix "$prefix" '.items[]? | select(.path | startswith($prefix))' >/dev/null <<<"$resp"; then
    echo "unexpected path prefix found: $prefix" >&2
    echo "Results: $resp" >&2
    exit 1
  fi
}

assert_only_repo() {
  local resp="$1"
  local repo_full="$2"
  if ! jq -e --arg repo "$repo_full" 'all(.items[]?; .repository.full_name == $repo)' >/dev/null <<<"$resp"; then
    echo "unexpected repository in response; expected only $repo_full" >&2
    echo "Results: $resp" >&2
    exit 1
  fi
}

assert_repo_absent() {
  local resp="$1"
  local repo_full="$2"
  if jq -e --arg repo "$repo_full" '.items[]? | select(.repository.full_name == $repo)' >/dev/null <<<"$resp"; then
    echo "unexpected repository found: $repo_full" >&2
    echo "Results: $resp" >&2
    exit 1
  fi
}

assert_fragment_present() {
  local resp="$1"
  local snippet="$2"
  if ! jq -e --arg snippet "$snippet" '.items[]?.text_matches[]?.fragment | select(contains($snippet))' >/dev/null <<<"$resp"; then
    echo "expected snippet not found: $snippet" >&2
    echo "Results: $resp" >&2
    exit 1
  fi
}

assert_fragment_absent() {
  local resp="$1"
  local snippet="$2"
  if jq -e --arg snippet "$snippet" '.items[]?.text_matches[]?.fragment | select(contains($snippet))' >/dev/null <<<"$resp"; then
    echo "unexpected snippet found: $snippet" >&2
    echo "Results: $resp" >&2
    exit 1
  fi
}

assert_text_matches_present() {
  local resp="$1"
  if ! jq -e '.items[]? | select(has("text_matches"))' >/dev/null <<<"$resp"; then
    echo "expected text_matches in response" >&2
    echo "Results: $resp" >&2
    exit 1
  fi
}

assert_text_matches_absent() {
  local resp="$1"
  if jq -e '.items[]? | select(has("text_matches"))' >/dev/null <<<"$resp"; then
    echo "unexpected text_matches found in response" >&2
    echo "Results: $resp" >&2
    exit 1
  fi
}

assert_response_excludes_literal() {
  local resp="$1"
  local literal="$2"
  if grep -Fq "$literal" <<<"$resp"; then
    echo "unexpected literal leaked into response: $literal" >&2
    echo "Results: $resp" >&2
    exit 1
  fi
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
  DB_DSN="$dsn" \
  GIT_REPO_DIR="$GIT_REPO_DIR" \
  ./gh-server > /tmp/gh-server-code-search-e2e.log 2>&1 &
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

seed_repo_content() {
  local repo_full="$1"
  local token="$2"
  local principal="$3"
  local unique_marker="$4"
  local snippet_marker="$5"

  create_file_in_repo "$repo_full" "src/shared_scope.txt" \
    "shared-scope-$principal $SHARED_SCOPE_MARKER" \
    "Add shared scope marker for $principal" "$token" > /dev/null

  create_file_in_repo "$repo_full" "src/${principal}_secret.go" \
    "package main
// $unique_marker
func ${principal//-/_}Secret() {}" \
    "Add unique code marker for $principal" "$token" > /dev/null

  create_file_in_repo "$repo_full" "config/${principal}_private.yaml" \
    "private_key: $snippet_marker" \
    "Add private snippet marker for $principal" "$token" > /dev/null
}

init_mysql_args "$DEFAULT_DB_DSN"

SINGLE_TENANT_MARKER="CODE_SEARCH_SINGLE_TENANT_${RANDOM_STRING}"
SHARED_SCOPE_MARKER="CODE_SEARCH_SHARED_SCOPE_${RANDOM_STRING}"
TENANT_A_UNIQUE_MARKER="CODE_SEARCH_TENANT_A_ONLY_${RANDOM_STRING}"
TENANT_B_UNIQUE_MARKER="CODE_SEARCH_TENANT_B_ONLY_${RANDOM_STRING}"
TENANT_C_UNIQUE_MARKER="CODE_SEARCH_TENANT_C_ONLY_${RANDOM_STRING}"
TENANT_A_SNIPPET="TENANT_A_SNIPPET_${RANDOM_STRING}"
TENANT_B_SNIPPET="TENANT_B_SNIPPET_${RANDOM_STRING}"
TENANT_C_SNIPPET="TENANT_C_SNIPPET_${RANDOM_STRING}"

note "Building gh-server..."
make build

if [ "$DROP_TIDB_DB_ON_CLEANUP" = "true" ] && [ -n "$TIDB_DB_NAME" ]; then
  note "Creating isolated TiDB database: $TIDB_DB_NAME"
  mysql_exec "CREATE DATABASE IF NOT EXISTS \`$TIDB_DB_NAME\`"
fi

note "Running single-tenant code-search smoke test..."
start_gh_server "$DEFAULT_DB_DSN"

SINGLE_TENANT_REPO="code-search-single-$RANDOM_STRING"
create_repo "$SINGLE_TENANT_REPO" > /dev/null
create_file_in_repo "$ORG/$SINGLE_TENANT_REPO" "src/single_tenant.go" \
  "package main
// $SINGLE_TENANT_MARKER
func singleTenant() {}" \
  "Add single-tenant search marker" > /dev/null

sleep 1

SINGLE_TENANT_RESP="$(search_code "$SINGLE_TENANT_MARKER")"
assert_search_contract "$SINGLE_TENANT_RESP"
assert_total_count_eq "$SINGLE_TENANT_RESP" "1"
assert_items_len_eq "$SINGLE_TENANT_RESP" "1"
assert_path_present "$SINGLE_TENANT_RESP" "src/single_tenant.go"
assert_only_repo "$SINGLE_TENANT_RESP" "$ORG/$SINGLE_TENANT_REPO"
ok "single-tenant code search works"
assert_eq "$(http_code -X DELETE -H "Authorization: token $GH_TOKEN" "$BASE_URL/api/v3/repos/$ORG/$SINGLE_TENANT_REPO")" "204"

note "Switching to control-plane mode for tenant/principal isolation checks..."
stop_gh_server

CONTROL_PLANE_DB_NAME="code_search_cp_${RANDOM_STRING}"
TENANT_A_DB_NAME="code_search_tenant_a_${RANDOM_STRING}"
TENANT_B_DB_NAME="code_search_tenant_b_${RANDOM_STRING}"
CONTROL_PLANE_DSN="$(dsn_for_db "$CONTROL_PLANE_DB_NAME")"
TENANT_A_DSN="$(dsn_for_db "$TENANT_A_DB_NAME")"
TENANT_B_DSN="$(dsn_for_db "$TENANT_B_DB_NAME")"

mysql_exec "CREATE DATABASE IF NOT EXISTS \`$CONTROL_PLANE_DB_NAME\`"
mysql_exec "CREATE DATABASE IF NOT EXISTS \`$TENANT_A_DB_NAME\`"
mysql_exec "CREATE DATABASE IF NOT EXISTS \`$TENANT_B_DB_NAME\`"

start_gh_server "$DEFAULT_DB_DSN" "$CONTROL_PLANE_DSN"
init_control_plane_schema

TENANT_A_LOGIN="tenant-a-$RANDOM_STRING"
TENANT_B_LOGIN="tenant-b-$RANDOM_STRING"
TENANT_C_LOGIN="tenant-c-$RANDOM_STRING"
TENANT_A_TOKEN="tenant-a-token-$RANDOM_STRING"
TENANT_B_TOKEN="tenant-b-token-$RANDOM_STRING"
TENANT_C_TOKEN="tenant-c-token-$RANDOM_STRING"

seed_control_plane_user "$TENANT_A_LOGIN" "$TENANT_A_TOKEN" "$TENANT_A_DSN"
seed_control_plane_user "$TENANT_B_LOGIN" "$TENANT_B_TOKEN" "$TENANT_B_DSN"
# Keep the tenant C principal in tenant A's DB so the test covers both
# cross-tenant isolation and same-tenant principal discoverability rules.
seed_control_plane_user "$TENANT_C_LOGIN" "$TENANT_C_TOKEN" "$TENANT_A_DSN"

curl_json 200 -H "Authorization: token $TENANT_A_TOKEN" "$BASE_URL/api/v3/user" > /dev/null
curl_json 200 -H "Authorization: token $TENANT_B_TOKEN" "$BASE_URL/api/v3/user" > /dev/null
curl_json 200 -H "Authorization: token $TENANT_C_TOKEN" "$BASE_URL/api/v3/user" > /dev/null

TENANT_A_REPO_NAME="code-tenant-a-$RANDOM_STRING"
TENANT_B_REPO_NAME="code-tenant-b-$RANDOM_STRING"
TENANT_C_REPO_NAME="code-tenant-c-$RANDOM_STRING"

create_repo "$TENANT_A_REPO_NAME" "$TENANT_A_TOKEN" > /dev/null
TENANT_A_REPO_FULL="$TENANT_A_LOGIN/$TENANT_A_REPO_NAME"
add_repo_cleanup "$TENANT_A_REPO_FULL" "$TENANT_A_TOKEN"

create_repo "$TENANT_B_REPO_NAME" "$TENANT_B_TOKEN" > /dev/null
TENANT_B_REPO_FULL="$TENANT_B_LOGIN/$TENANT_B_REPO_NAME"
add_repo_cleanup "$TENANT_B_REPO_FULL" "$TENANT_B_TOKEN"

create_repo "$TENANT_C_REPO_NAME" "$TENANT_C_TOKEN" > /dev/null
TENANT_C_REPO_FULL="$TENANT_C_LOGIN/$TENANT_C_REPO_NAME"
add_repo_cleanup "$TENANT_C_REPO_FULL" "$TENANT_C_TOKEN"

note "Seeding tenant-specific searchable content..."
seed_repo_content "$TENANT_A_REPO_FULL" "$TENANT_A_TOKEN" "tenant-a" "$TENANT_A_UNIQUE_MARKER" "$TENANT_A_SNIPPET"
seed_repo_content "$TENANT_B_REPO_FULL" "$TENANT_B_TOKEN" "tenant-b" "$TENANT_B_UNIQUE_MARKER" "$TENANT_B_SNIPPET"
seed_repo_content "$TENANT_C_REPO_FULL" "$TENANT_C_TOKEN" "tenant-c" "$TENANT_C_UNIQUE_MARKER" "$TENANT_C_SNIPPET"

sleep 2

note "Test 1: Single-principal searches without repo qualifiers..."
TENANT_A_SEARCH="$(search_code "$TENANT_A_UNIQUE_MARKER" "$TENANT_A_TOKEN")"
assert_search_contract "$TENANT_A_SEARCH"
assert_total_count_eq "$TENANT_A_SEARCH" "1"
assert_items_len_eq "$TENANT_A_SEARCH" "1"
assert_path_present "$TENANT_A_SEARCH" "src/tenant-a_secret.go"
assert_only_repo "$TENANT_A_SEARCH" "$TENANT_A_REPO_FULL"
assert_path_absent "$TENANT_A_SEARCH" "src/tenant-b_secret.go"
assert_path_absent "$TENANT_A_SEARCH" "src/tenant-c_secret.go"
ok "tenant A sees only tenant A code"

TENANT_B_SEARCH="$(search_code "$TENANT_B_UNIQUE_MARKER" "$TENANT_B_TOKEN")"
assert_search_contract "$TENANT_B_SEARCH"
assert_total_count_eq "$TENANT_B_SEARCH" "1"
assert_items_len_eq "$TENANT_B_SEARCH" "1"
assert_path_present "$TENANT_B_SEARCH" "src/tenant-b_secret.go"
assert_only_repo "$TENANT_B_SEARCH" "$TENANT_B_REPO_FULL"
assert_path_absent "$TENANT_B_SEARCH" "src/tenant-a_secret.go"
assert_path_absent "$TENANT_B_SEARCH" "src/tenant-c_secret.go"
ok "tenant B sees only tenant B code"

TENANT_C_SEARCH="$(search_code "$TENANT_C_UNIQUE_MARKER" "$TENANT_C_TOKEN")"
assert_search_contract "$TENANT_C_SEARCH"
assert_total_count_eq "$TENANT_C_SEARCH" "1"
assert_items_len_eq "$TENANT_C_SEARCH" "1"
assert_path_present "$TENANT_C_SEARCH" "src/tenant-c_secret.go"
assert_only_repo "$TENANT_C_SEARCH" "$TENANT_C_REPO_FULL"
assert_path_absent "$TENANT_C_SEARCH" "src/tenant-a_secret.go"
assert_path_absent "$TENANT_C_SEARCH" "src/tenant-b_secret.go"
ok "tenant C principal sees only its own code"

note "Test 2: Text-match expansion returns only authorized snippets..."
TENANT_A_TEXT_MATCH="$(search_code "$TENANT_A_SNIPPET repo:$TENANT_A_REPO_FULL" "$TENANT_A_TOKEN" "application/vnd.github.v3.text-match+json")"
assert_search_contract "$TENANT_A_TEXT_MATCH"
assert_total_count_eq "$TENANT_A_TEXT_MATCH" "1"
assert_items_len_eq "$TENANT_A_TEXT_MATCH" "1"
assert_only_repo "$TENANT_A_TEXT_MATCH" "$TENANT_A_REPO_FULL"
assert_path_prefix_present "$TENANT_A_TEXT_MATCH" "config/tenant-a_private.yaml"
assert_text_matches_present "$TENANT_A_TEXT_MATCH"
assert_fragment_present "$TENANT_A_TEXT_MATCH" "$TENANT_A_SNIPPET"
assert_fragment_absent "$TENANT_A_TEXT_MATCH" "$TENANT_B_SNIPPET"
assert_fragment_absent "$TENANT_A_TEXT_MATCH" "$TENANT_C_SNIPPET"
ok "authorized text-match snippets are isolated"

note "Test 3: Boundary/result filtering with shared marker and filename qualifier..."
TENANT_A_SHARED_FILTERED="$(search_code "$SHARED_SCOPE_MARKER filename:shared_scope.txt" "$TENANT_A_TOKEN")"
assert_search_contract "$TENANT_A_SHARED_FILTERED"
assert_total_count_eq "$TENANT_A_SHARED_FILTERED" "1"
assert_items_len_eq "$TENANT_A_SHARED_FILTERED" "1"
assert_path_present "$TENANT_A_SHARED_FILTERED" "src/shared_scope.txt"
assert_only_repo "$TENANT_A_SHARED_FILTERED" "$TENANT_A_REPO_FULL"
assert_repo_absent "$TENANT_A_SHARED_FILTERED" "$TENANT_B_REPO_FULL"
ok "filename-based filtering preserves isolation"

note "Test 4: Mixed repo qualifiers include authorized repos and ignore unauthorized repos..."
TENANT_A_MIXED_REPOS="$(search_code "$SHARED_SCOPE_MARKER repo:$TENANT_A_REPO_FULL repo:$TENANT_B_REPO_FULL path:src" "$TENANT_A_TOKEN" "application/vnd.github.v3.text-match+json")"
assert_search_contract "$TENANT_A_MIXED_REPOS"
assert_total_count_eq "$TENANT_A_MIXED_REPOS" "1"
assert_items_len_eq "$TENANT_A_MIXED_REPOS" "1"
assert_only_repo "$TENANT_A_MIXED_REPOS" "$TENANT_A_REPO_FULL"
assert_repo_absent "$TENANT_A_MIXED_REPOS" "$TENANT_B_REPO_FULL"
assert_path_prefix_present "$TENANT_A_MIXED_REPOS" "src/shared_scope.txt"
assert_fragment_present "$TENANT_A_MIXED_REPOS" "$SHARED_SCOPE_MARKER"
ok "mixed repo qualifiers do not widen visibility"

note "Test 5: Concurrent searches for the same shared term stay isolated..."
TENANT_A_CONCURRENT_OUT="$SEARCH_TMP_DIR/tenant-a.json"
TENANT_B_CONCURRENT_OUT="$SEARCH_TMP_DIR/tenant-b.json"
TENANT_C_CONCURRENT_OUT="$SEARCH_TMP_DIR/tenant-c.json"

run_search_to_file "$SHARED_SCOPE_MARKER" "$TENANT_A_TOKEN" "$TENANT_A_CONCURRENT_OUT" &
PID_A=$!
run_search_to_file "$SHARED_SCOPE_MARKER" "$TENANT_B_TOKEN" "$TENANT_B_CONCURRENT_OUT" &
PID_B=$!
run_search_to_file "$SHARED_SCOPE_MARKER" "$TENANT_C_TOKEN" "$TENANT_C_CONCURRENT_OUT" &
PID_C=$!

wait "$PID_A"
wait "$PID_B"
wait "$PID_C"

TENANT_A_CONCURRENT_RESP="$(<"$TENANT_A_CONCURRENT_OUT")"
TENANT_B_CONCURRENT_RESP="$(<"$TENANT_B_CONCURRENT_OUT")"
TENANT_C_CONCURRENT_RESP="$(<"$TENANT_C_CONCURRENT_OUT")"

assert_search_contract "$TENANT_A_CONCURRENT_RESP"
assert_total_count_eq "$TENANT_A_CONCURRENT_RESP" "1"
assert_only_repo "$TENANT_A_CONCURRENT_RESP" "$TENANT_A_REPO_FULL"
assert_path_present "$TENANT_A_CONCURRENT_RESP" "src/shared_scope.txt"
assert_repo_absent "$TENANT_A_CONCURRENT_RESP" "$TENANT_B_REPO_FULL"
assert_repo_absent "$TENANT_A_CONCURRENT_RESP" "$TENANT_C_REPO_FULL"

assert_search_contract "$TENANT_B_CONCURRENT_RESP"
assert_total_count_eq "$TENANT_B_CONCURRENT_RESP" "1"
assert_only_repo "$TENANT_B_CONCURRENT_RESP" "$TENANT_B_REPO_FULL"
assert_path_present "$TENANT_B_CONCURRENT_RESP" "src/shared_scope.txt"
assert_repo_absent "$TENANT_B_CONCURRENT_RESP" "$TENANT_A_REPO_FULL"
assert_repo_absent "$TENANT_B_CONCURRENT_RESP" "$TENANT_C_REPO_FULL"

assert_search_contract "$TENANT_C_CONCURRENT_RESP"
assert_total_count_eq "$TENANT_C_CONCURRENT_RESP" "1"
assert_only_repo "$TENANT_C_CONCURRENT_RESP" "$TENANT_C_REPO_FULL"
assert_path_present "$TENANT_C_CONCURRENT_RESP" "src/shared_scope.txt"
assert_repo_absent "$TENANT_C_CONCURRENT_RESP" "$TENANT_A_REPO_FULL"
assert_repo_absent "$TENANT_C_CONCURRENT_RESP" "$TENANT_B_REPO_FULL"
ok "concurrent code searches remain isolated by principal"

note "Test 6: Unauthorized repo qualifier returns zero results and no path/snippet leaks..."
TENANT_A_UNAUTHORIZED_REPO="$(search_code "$TENANT_B_SNIPPET repo:$TENANT_B_REPO_FULL" "$TENANT_A_TOKEN" "application/vnd.github.v3.text-match+json")"
assert_search_contract "$TENANT_A_UNAUTHORIZED_REPO"
assert_total_count_eq "$TENANT_A_UNAUTHORIZED_REPO" "0"
assert_items_len_eq "$TENANT_A_UNAUTHORIZED_REPO" "0"
assert_path_prefix_absent "$TENANT_A_UNAUTHORIZED_REPO" "config/tenant-b_private.yaml"
assert_repo_absent "$TENANT_A_UNAUTHORIZED_REPO" "$TENANT_B_REPO_FULL"
assert_text_matches_absent "$TENANT_A_UNAUTHORIZED_REPO"
assert_response_excludes_literal "$TENANT_A_UNAUTHORIZED_REPO" "config/tenant-b_private.yaml"
assert_response_excludes_literal "$TENANT_A_UNAUTHORIZED_REPO" "$TENANT_B_SNIPPET"
ok "tenant A cannot leak tenant B file paths or snippets"

TENANT_C_UNAUTHORIZED_REPO="$(search_code "$TENANT_A_SNIPPET repo:$TENANT_A_REPO_FULL" "$TENANT_C_TOKEN" "application/vnd.github.v3.text-match+json")"
assert_search_contract "$TENANT_C_UNAUTHORIZED_REPO"
assert_total_count_eq "$TENANT_C_UNAUTHORIZED_REPO" "0"
assert_items_len_eq "$TENANT_C_UNAUTHORIZED_REPO" "0"
assert_path_prefix_absent "$TENANT_C_UNAUTHORIZED_REPO" "config/tenant-a_private.yaml"
assert_repo_absent "$TENANT_C_UNAUTHORIZED_REPO" "$TENANT_A_REPO_FULL"
assert_text_matches_absent "$TENANT_C_UNAUTHORIZED_REPO"
assert_response_excludes_literal "$TENANT_C_UNAUTHORIZED_REPO" "config/tenant-a_private.yaml"
assert_response_excludes_literal "$TENANT_C_UNAUTHORIZED_REPO" "$TENANT_A_SNIPPET"
ok "tenant C principal cannot leak normal-user file paths or snippets"

note "=== Code Search Tenant Isolation E2E Test PASSED ==="
ok "Code-search isolation coverage verified"
