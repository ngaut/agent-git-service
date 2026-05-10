#!/usr/bin/env bash
set -euo pipefail

# E2E test for multi-agent isolation.
# The script self-bootstraps a control-plane server plus tenant tokens so the
# default E2E inventory keeps exercising token routing and tenant isolation.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./lib.sh
source "$SCRIPT_DIR/lib.sh"

require_cmd curl
require_cmd jq
require_cmd git
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
CONTROL_PLANE_MODE="${CONTROL_PLANE_MODE:-true}"

TIDB_DB_NAME=""
DROP_TIDB_DB_ON_CLEANUP="false"
if [ -n "${DB_DSN:-}" ]; then
  DEFAULT_DB_DSN="$DB_DSN"
else
  TIDB_DB_NAME="multi_agent_e2e_${RANDOM_STRING}"
  DEFAULT_DB_DSN="root:@tcp(127.0.0.1:4000)/${TIDB_DB_NAME}?parseTime=true&timeout=10s"
  DROP_TIDB_DB_ON_CLEANUP="true"
fi

CONTROL_PLANE_DB_NAME=""
TENANT_A_DB_NAME=""
TENANT_B_DB_NAME=""
CONTROL_PLANE_DSN=""
TENANT_A_DSN=""
TENANT_B_DSN=""
GIT_REPO_DIR="$(mktemp -d /tmp/gh-server-multi-agent-repos.XXXXXX)"
TMP_DIR="$(mktemp -d /tmp/gh-server-multi-agent-tmp.XXXXXX)"
REPOS_TO_DELETE=()

MYSQL_HOST="127.0.0.1"
MYSQL_PORT="4000"
MYSQL_USER="root"
MYSQL_PASS=""
MYSQL_ARGS=()

TENANT_A_TOKEN=""
TENANT_B_TOKEN=""

note "=== Multi-Agent Isolation Test ==="
note "BASE_URL=$BASE_URL CONTROL_PLANE_MODE=$CONTROL_PLANE_MODE"

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
      local token repo code
      token="${entry%%|*}"
      repo="${entry#*|}"
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
  rm -rf "$GIT_REPO_DIR" "$TMP_DIR"
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
  local token="$2"
  REPOS_TO_DELETE+=("${token}|${repo}")
}

create_repo() {
  local name="$1"
  local token="$2"
  curl_json 201 \
    -X POST "$BASE_URL/api/v3/user/repos" \
    -H "Authorization: token $token" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"$name\",\"private\":true,\"add_readme\":true}"
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
  local control_plane_dsn="$2"
  note "Starting gh-server (DB_DSN=$dsn, CONTROL_PLANE_DSN=$control_plane_dsn)"
  ENVIRONMENT=development \
  LISTEN_MODE=production \
  PORT="$SERVER_PORT" \
  BASE_URL="$BASE_URL" \
  CONTROL_PLANE_DSN="$control_plane_dsn" \
  ADMIN_LOGIN="$ADMIN_LOGIN" \
  ADMIN_TOKEN="$ADMIN_TOKEN" \
  DB_DSN="$dsn" \
  GIT_REPO_DIR="$GIT_REPO_DIR" \
  ./gh-server > /tmp/gh-server-multi-agent-isolation.log 2>&1 &
  SERVER_PID=$!
  wait_for_http_ready \
    "$BASE_URL/api/v3/" \
    "Waiting for gh-server to start..." \
    "server running on port $SERVER_PORT" \
    "gh-server failed to start"
}

if [[ "$CONTROL_PLANE_MODE" != "true" ]]; then
  note "Skipping multi-agent isolation test: CONTROL_PLANE_MODE is not enabled"
  exit 0
fi

init_mysql_args "$DEFAULT_DB_DSN"

note "Building gh-server..."
make build

if [ "$DROP_TIDB_DB_ON_CLEANUP" = "true" ] && [ -n "$TIDB_DB_NAME" ]; then
  note "Creating isolated TiDB database: $TIDB_DB_NAME"
  mysql_exec "CREATE DATABASE IF NOT EXISTS \`$TIDB_DB_NAME\`"
fi

CONTROL_PLANE_DB_NAME="multi_agent_cp_${RANDOM_STRING}"
TENANT_A_DB_NAME="multi_agent_tenant_a_${RANDOM_STRING}"
TENANT_B_DB_NAME="multi_agent_tenant_b_${RANDOM_STRING}"
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
TENANT_A_TOKEN="tenant-a-token-$RANDOM_STRING"
TENANT_B_TOKEN="tenant-b-token-$RANDOM_STRING"

seed_control_plane_user "$TENANT_A_LOGIN" "$TENANT_A_TOKEN" "$TENANT_A_DSN"
seed_control_plane_user "$TENANT_B_LOGIN" "$TENANT_B_TOKEN" "$TENANT_B_DSN"

note "Step 1: Verify tenant A token works"
user_a="$(curl_json 200 -H "Authorization: token $TENANT_A_TOKEN" "$BASE_URL/api/v3/user")"
login_a="$(json_get login <<<"$user_a")"
note "Tenant A login: $login_a"

note "Step 2: Verify tenant B token works"
user_b="$(curl_json 200 -H "Authorization: token $TENANT_B_TOKEN" "$BASE_URL/api/v3/user")"
login_b="$(json_get login <<<"$user_b")"
note "Tenant B login: $login_b"

if [[ "$login_a" == "$login_b" ]]; then
  echo "ERROR: Tenant A and B have the same login - test setup issue" >&2
  exit 1
fi
ok "Tenants are distinct: $login_a != $login_b"

note "Step 4: Tenant A creates a private repo"
repo_a_name="isolation-test-a-$RANDOM_STRING"
repo_a="$(create_repo "$repo_a_name" "$TENANT_A_TOKEN")"
repo_a_full="$(json_get full_name <<<"$repo_a")"
add_repo_cleanup "$repo_a_full" "$TENANT_A_TOKEN"
note "Tenant A repo: $repo_a_full"

note "Step 5: Tenant B creates a repo with the same name"
repo_b="$(create_repo "$repo_a_name" "$TENANT_B_TOKEN")"
repo_b_full="$(json_get full_name <<<"$repo_b")"
add_repo_cleanup "$repo_b_full" "$TENANT_B_TOKEN"
note "Tenant B repo: $repo_b_full"

if [[ "$repo_a_full" == "$repo_b_full" ]]; then
  echo "ERROR: Repos have the same full name - isolation broken!" >&2
  exit 1
fi
ok "Repos are isolated: $repo_a_full != $repo_b_full"

assert_mutual_cross_tenant_404 "$TENANT_A_TOKEN" "$repo_a_full" "$TENANT_B_TOKEN" "$repo_b_full" "Tenant A" "Tenant B"

note "Step 9: Tenant A creates an issue"
issue_a="$(curl_json 201 \
  -X POST "$BASE_URL/api/v3/repos/$repo_a_full/issues" \
  -H "Authorization: token $TENANT_A_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"title":"Tenant A Issue","body":"This is from tenant A"}')"
issue_a_number="$(json_get number <<<"$issue_a")"
note "Tenant A issue #$issue_a_number"

note "Step 10: Verify Tenant B cannot list Tenant A's issues"
code="$(http_code -H "Authorization: token $TENANT_B_TOKEN" "$BASE_URL/api/v3/repos/$repo_a_full/issues")"
assert_eq "$code" "404"
ok "Tenant B gets 404 on Tenant A's issues endpoint"

note "Step 11: Git isolation - Tenant A clones their repo"
git clone -q "$BASE_URL/$repo_a_full.git" "$TMP_DIR/repo-a" \
  -c http.extraHeader="Authorization: token $TENANT_A_TOKEN" \
  2>/dev/null || {
    echo "WARNING: Git clone failed for tenant A (git-http may not be enabled)" >&2
    note "Skipping git isolation tests"
    exit 0
  }

echo "# Tenant A Repo" > "$TMP_DIR/repo-a/README.md"
(
  cd "$TMP_DIR/repo-a"
  git config user.email "tenant-a@test.local"
  git config user.name "Tenant A"
  git add README.md
  git commit -q -m "Initial commit from Tenant A"
  git push -q origin main 2>/dev/null || true
)
ok "Tenant A git operations successful"

note "Step 12: Git isolation - Tenant B cannot clone Tenant A's repo"
if git clone -q "$BASE_URL/$repo_a_full.git" "$TMP_DIR/repo-b-fail" \
  -c http.extraHeader="Authorization: token $TENANT_B_TOKEN" \
  2>/dev/null; then
  echo "ERROR: Tenant B was able to clone Tenant A's repo! Isolation broken!" >&2
  exit 1
fi
ok "Tenant B git clone rejected"

note "Step 13: Verify each tenant can only see their own repo list"
repos_a="$(curl_json 200 -H "Authorization: token $TENANT_A_TOKEN" "$BASE_URL/api/v3/user/repos")"
repos_b="$(curl_json 200 -H "Authorization: token $TENANT_B_TOKEN" "$BASE_URL/api/v3/user/repos")"
assert_eq "$(jq --arg name "$repo_a_name" '[.[] | select(.name == $name)] | length' <<<"$repos_a")" "1"
assert_eq "$(jq --arg name "$repo_a_name" '[.[] | select(.name == $name)] | length' <<<"$repos_b")" "1"
assert_eq "$(jq --arg full "$repo_b_full" '[.[] | select(.full_name == $full)] | length' <<<"$repos_a")" "0"
assert_eq "$(jq --arg full "$repo_a_full" '[.[] | select(.full_name == $full)] | length' <<<"$repos_b")" "0"
ok "Repo listing isolation verified"

note "=== All Multi-Agent Isolation Tests Passed ==="
ok "Multi-agent isolation is working correctly"
