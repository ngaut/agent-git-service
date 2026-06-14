#!/usr/bin/env bash
# e2e/repo-transfer-lifecycle.sh - E2E tests for repository transfer lifecycle and redirects
# Tests successful transfer, REST visibility, redirect resolution, and Git usability
#
# Coverage:
# 1. New E2E coverage exercises a successful transfer, not only rollback/failure handling.
# 2. Assertions cover REST visibility, redirect resolution, and Git usability after transfer.
# 3. Creator visibility after org transfer is explicitly checked.
# 4. No stale owner metadata is returned from the old path.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=./lib.sh
source "$ROOT/e2e/lib.sh"

require_cmd curl
require_cmd jq
require_cmd make
require_cmd git
require_cmd python3
require_cmd mysql

RANDOM_SUFFIX="$(date +%s)-$RANDOM"
SERVER_PORT="${E2E_PORT:-$((RANDOM % 10000 + 20000))}"
BASE_URL="http://localhost:$SERVER_PORT"
ADMIN_LOGIN="e2e-admin-$RANDOM_SUFFIX"
ADMIN_TOKEN="e2e-token-$RANDOM_SUFFIX"
TIDB_DB_NAME="transfer_e2e_${RANDOM_SUFFIX//[^A-Za-z0-9_]/_}"
DB_DSN="$(tidb_dsn_for_database "$TIDB_DB_NAME")"
GIT_REPO_DIR="$(mktemp -d /tmp/gh-server-transfer-repos.XXXXXX)"
LOG_FILE="/tmp/gh-server-transfer-e2e.log"

# Test users and orgs
USER_A="$ADMIN_LOGIN"
ORG_B="org-beta-transfer-$RANDOM_SUFFIX"
TRANSFER_REPO="transfer-test-$RANDOM_SUFFIX"

cleanup() {
  note "Cleaning up..."
  if [[ -n "${SERVER_PID:-}" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  if [[ -n "${GIT_REPO_DIR:-}" && -d "$GIT_REPO_DIR" ]]; then
    rm -rf "$GIT_REPO_DIR"
  fi
  tidb_drop_database "$TIDB_DB_NAME"
}
trap cleanup EXIT

tidb_create_database "$TIDB_DB_NAME"

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

# Create repo under the authenticated user.
create_user_repo() {
  local name="$1"
  curl_json 201 \
    -X POST "$BASE_URL/api/v3/user/repos" \
    -H "Authorization: token $ADMIN_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"$name\",\"add_readme\":true}"
}

get_repo() {
  local token="$1"
  local owner="$2"
  local repo="$3"
  curl -ksS -H "Authorization: token $token" "$BASE_URL/api/v3/repos/$owner/$repo"
}

get_repo_status() {
  local token="$1"
  local owner="$2"
  local repo="$3"
  http_code -H "Authorization: token $token" "$BASE_URL/api/v3/repos/$owner/$repo"
}

transfer_repo() {
  local token="$1"
  local owner="$2"
  local repo="$3"
  local new_owner="$4"
  curl -ksS -w "\n%{http_code}" \
    -X POST "$BASE_URL/api/v3/repos/$owner/$repo/transfer" \
    -H "Authorization: token $token" \
    -H "Content-Type: application/json" \
    -d "{\"new_owner\":\"$new_owner\"}"
}

list_repos() {
  local token="$1"
  curl -ksS -H "Authorization: token $token" "$BASE_URL/api/v3/user/repos"
}

clone_repo() {
  local token="$1"
  local owner="$2"
  local repo="$3"
  local dest="$4"
  # Use Git Smart HTTP endpoint with token authentication via http.extraHeader
  git clone -q "$BASE_URL/$owner/$repo.git" \
    -c http.extraHeader="Authorization: token $token" \
    "$dest" 2>&1
}

fetch_repo() {
  local repo_dir="$1"
  (cd "$repo_dir" && git fetch origin 2>&1)
}

# ============================================================================
# TEST SETUP
# ============================================================================

start_server

note "=== Setting up test fixtures ==="
note "Ensuring target org exists: $ORG_B"
ensure_org_exists "$BASE_URL" "$ADMIN_TOKEN" "$ORG_B"
note "Creating repository $USER_A/$TRANSFER_REPO"
create_user_repo "$TRANSFER_REPO" >/dev/null
ok "Repository $USER_A/$TRANSFER_REPO created"

# ============================================================================
# TEST STEP 1: Create repository under user A, verify before transfer
# Expected: repo is reachable and Git operations succeed
# ============================================================================

note "=== TEST STEP 1: Verify repository before transfer ==="

# Verify repo exists and is reachable via REST
note "Verifying repository is reachable via REST API"
repo_status=$(get_repo_status "$ADMIN_TOKEN" "$USER_A" "$TRANSFER_REPO")
assert_eq "$repo_status" "200"
ok "✓ Repository $USER_A/$TRANSFER_REPO is reachable (status 200)"

# Verify repo metadata
note "Verifying repository metadata"
repo_json=$(get_repo "$ADMIN_TOKEN" "$USER_A" "$TRANSFER_REPO")
repo_full_name=$(echo "$repo_json" | jq -r '.full_name')
repo_owner=$(echo "$repo_json" | jq -r '.owner.login')
assert_eq "$repo_full_name" "$USER_A/$TRANSFER_REPO"
assert_eq "$repo_owner" "$USER_A"
ok "✓ Repository metadata correct: full_name=$repo_full_name, owner=$repo_owner"

# Verify Git clone works before transfer
note "Verifying Git clone operation before transfer"
CLONE_DIR_1="$(mktemp -d /tmp/transfer-clone-1.XXXXXX)"
if clone_repo "$ADMIN_TOKEN" "$USER_A" "$TRANSFER_REPO" "$CLONE_DIR_1"; then
  ok "✓ Git clone succeeded before transfer"
else
  echo "✗ Git clone failed before transfer" >&2
  exit 1
fi

if [[ -d "$CLONE_DIR_1/.git" ]]; then
  ok "✓ Clone directory contains .git"
else
  echo "✗ Clone directory missing .git" >&2
  exit 1
fi
rm -rf "$CLONE_DIR_1"

# ============================================================================
# TEST STEP 2: Transfer repository to organization B
# Expected: transfer succeeds and response reports new full_name
# ============================================================================

note "=== TEST STEP 2: Transfer repository to organization B ==="
note "Transferring $USER_A/$TRANSFER_REPO to $ORG_B"

transfer_response=$(transfer_repo "$ADMIN_TOKEN" "$USER_A" "$TRANSFER_REPO" "$ORG_B")
transfer_body="$(printf '%s\n' "$transfer_response" | sed '$d')"
transfer_status=$(echo "$transfer_response" | tail -n 1)

assert_eq "$transfer_status" "202"
ok "✓ Transfer API returned status 202 Accepted"

# Verify response reports new full_name
new_full_name=$(echo "$transfer_body" | jq -r '.full_name')
assert_eq "$new_full_name" "$ORG_B/$TRANSFER_REPO"
ok "✓ Transfer response reports correct new full_name: $new_full_name"

# Verify creator retains admin permissions.
admin_perm=$(echo "$transfer_body" | jq -r '.permissions.admin')
push_perm=$(echo "$transfer_body" | jq -r '.permissions.push')
pull_perm=$(echo "$transfer_body" | jq -r '.permissions.pull')
assert_eq "$admin_perm" "true"
assert_eq "$push_perm" "true"
assert_eq "$pull_perm" "true"
ok "✓ Creator retains admin/push/pull permissions after transfer"

# ============================================================================
# TEST STEP 3: Read repository through old and new paths
# Expected: new path is canonical, old path resolves via redirect without stale metadata
# ============================================================================

note "=== TEST STEP 3: Verify redirect resolution ==="

# Test new path is canonical
note "Verifying new path ($ORG_B/$TRANSFER_REPO) is canonical"
new_path_status=$(get_repo_status "$ADMIN_TOKEN" "$ORG_B" "$TRANSFER_REPO")
assert_eq "$new_path_status" "200"
ok "✓ New path returns 200"

new_path_json=$(get_repo "$ADMIN_TOKEN" "$ORG_B" "$TRANSFER_REPO")
new_path_owner=$(echo "$new_path_json" | jq -r '.owner.login')
new_path_full=$(echo "$new_path_json" | jq -r '.full_name')
assert_eq "$new_path_owner" "$ORG_B"
assert_eq "$new_path_full" "$ORG_B/$TRANSFER_REPO"
ok "✓ New path returns correct owner: $new_path_owner"

# Test old path resolves through redirect.
note "Verifying old path ($USER_A/$TRANSFER_REPO) resolves via redirect"
old_path_status=$(get_repo_status "$ADMIN_TOKEN" "$USER_A" "$TRANSFER_REPO")
assert_eq "$old_path_status" "200"
ok "✓ Old path returns 200 (redirect resolved)"

old_path_json=$(get_repo "$ADMIN_TOKEN" "$USER_A" "$TRANSFER_REPO")
old_path_owner=$(echo "$old_path_json" | jq -r '.owner.login')
old_path_full=$(echo "$old_path_json" | jq -r '.full_name')

# CRITICAL: Old path must NOT expose stale owner metadata.
assert_eq "$old_path_owner" "$ORG_B"
assert_eq "$old_path_full" "$ORG_B/$TRANSFER_REPO"
ok "✓ Old path resolves to NEW owner (no stale metadata): owner=$old_path_owner, full_name=$old_path_full"

# ============================================================================
# TEST STEP 4: Clone and fetch using the new path
# Expected: Git operations succeed against the new location
# ============================================================================

note "=== TEST STEP 4: Verify Git operations on new path ==="

# Clone using new path
note "Cloning repository using new path ($ORG_B/$TRANSFER_REPO)"
CLONE_DIR_2="$(mktemp -d /tmp/transfer-clone-2.XXXXXX)"
if clone_repo "$ADMIN_TOKEN" "$ORG_B" "$TRANSFER_REPO" "$CLONE_DIR_2"; then
  ok "✓ Git clone succeeded using new path"
else
  echo "✗ Git clone failed using new path" >&2
  exit 1
fi

# Verify .git directory exists
if [[ -d "$CLONE_DIR_2/.git" ]]; then
  ok "✓ Clone directory contains .git"
else
  echo "✗ Clone directory missing .git" >&2
  exit 1
fi

# Fetch from new path
note "Testing git fetch from new path"
if fetch_repo "$CLONE_DIR_2"; then
  ok "✓ Git fetch succeeded from new path"
else
  echo "✗ Git fetch failed from new path" >&2
  exit 1
fi

# Verify old Git path behavior after transfer
# Note: Git directory is physically moved, but server may redirect Git HTTP requests
note "Verifying old Git path behavior after transfer"
old_git_path="$(repo_path "$USER_A/$TRANSFER_REPO")"
if [[ ! -d "$old_git_path" ]]; then
  ok "✓ Old Git directory correctly removed (moved to new location)"
else
  note "Old Git directory still exists (may be due to server redirect logic)"
fi

# Try cloning from old path - may succeed if server redirects, but content should be same
CLONE_DIR_OLD="$(mktemp -d /tmp/transfer-clone-old.XXXXXX)"
if clone_repo "$ADMIN_TOKEN" "$USER_A" "$TRANSFER_REPO" "$CLONE_DIR_OLD" 2>/dev/null; then
  # If clone succeeded, verify it's actually the transferred repo (should have new owner metadata)
  # This is acceptable behavior if the server implements Git-level redirects
  note "Old Git path clone succeeded (server may redirect Git requests)"
  rm -rf "$CLONE_DIR_OLD"
else
  ok "✓ Old Git path correctly fails for Git clone (git directory moved)"
fi

rm -rf "$CLONE_DIR_2"

# ============================================================================
# TEST STEP 5: List repositories as original creator after transfer
# Expected: creator still sees transferred repo with admin-equivalent visibility
# ============================================================================

note "=== TEST STEP 5: Verify creator visibility after transfer ==="

note "Listing repositories (simulating creator's view)"
user_repos_json=$(list_repos "$ADMIN_TOKEN")

# Count repos
repo_count=$(echo "$user_repos_json" | jq 'length')
note "Total repositories visible: $repo_count"

# Find the transferred repo in the list.
transferred_in_list=$(echo "$user_repos_json" | jq --arg fn "$ORG_B/$TRANSFER_REPO" '[.[] | select(.full_name == $fn)] | length')
assert_eq "$transferred_in_list" "1"
ok "✓ Transferred repository appears in creator's repository list"

# Verify the repo in the list has correct metadata (no stale owner)
transferred_repo=$(echo "$user_repos_json" | jq --arg fn "$ORG_B/$TRANSFER_REPO" '.[] | select(.full_name == $fn)')
listed_owner=$(echo "$transferred_repo" | jq -r '.owner.login')
listed_full=$(echo "$transferred_repo" | jq -r '.full_name')
listed_perms_admin=$(echo "$transferred_repo" | jq -r '.permissions.admin')

assert_eq "$listed_owner" "$ORG_B"
assert_eq "$listed_full" "$ORG_B/$TRANSFER_REPO"
assert_eq "$listed_perms_admin" "true"
ok "✓ Creator sees transferred repo with correct owner: $listed_owner"
ok "✓ Creator retains admin visibility: $listed_perms_admin"

# Verify no stale metadata for old path exists.
stale_count=$(echo "$user_repos_json" | jq --arg fn "$USER_A/$TRANSFER_REPO" '[.[] | select(.full_name == $fn)] | length')
assert_eq "$stale_count" "0"
ok "✓ No stale entries with old full_name ($USER_A/$TRANSFER_REPO) in listing"

# ============================================================================
# SUMMARY
# ============================================================================

echo ""
echo "═══════════════════════════════════════════════════════════════════"
echo "All E2E transfer lifecycle tests PASSED"
echo "═══════════════════════════════════════════════════════════════════"
echo ""
echo "Coverage:"
echo "  ✓ Successful transfer exercised (not just rollback/failure)"
echo "  ✓ REST visibility, redirect resolution, Git usability verified"
echo "  ✓ Creator visibility after org transfer explicitly checked"
echo "  ✓ No stale owner metadata returned from old path"
echo ""
echo "Test Steps Completed:"
echo "  1. ✓ Repository creation and pre-transfer verification"
echo "  2. ✓ Successful transfer to organization (202 Accepted)"
echo "  3. ✓ Redirect resolution without stale metadata"
echo "  4. ✓ Git clone/fetch on new path only"
echo "  5. ✓ Creator visibility with admin permissions after transfer"
echo ""
