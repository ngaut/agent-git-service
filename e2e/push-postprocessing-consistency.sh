#!/usr/bin/env bash
set -euo pipefail

# E2E: Git push post-processing consistency (#875, #996)
# Covers: fixHEAD ordering, workflow sync after push, rename/delete cleanup, failure handling

ROOT=""
BASE_URL=""
SERVER_PORT=""
ADMIN_LOGIN=""
ADMIN_TOKEN=""
TIDB_DB_NAME=""
DB_DSN=""
GIT_REPO_DIR=""
WORK_DIR=""
LOG_FILE=""
SERVER_PID=""
RANDOM_SUFFIX=""

TESTS_PASSED=0
TESTS_FAILED=0
TESTS_SKIPPED=0
TEST_RESULTS=()

record_result() {
  local name="$1"
  local status="$2"
  local details="${3:-}"
  TEST_RESULTS+=("$name|$status|$details")
  case "$status" in
    PASS) ((TESTS_PASSED++)) || true ;;
    FAIL) ((TESTS_FAILED++)) || true ;;
    SKIP) ((TESTS_SKIPPED++)) || true ;;
  esac
}

print_summary() {
  note "=== Test Summary ==="
  echo ""
  echo "Tests Passed:  $TESTS_PASSED"
  echo "Tests Failed:  $TESTS_FAILED"
  echo "Tests Skipped: $TESTS_SKIPPED"
  echo ""
  echo "Detailed Results:"
  echo "----------------"
  local result name status details
  for result in "${TEST_RESULTS[@]}"; do
    IFS='|' read -r name status details <<< "$result"
    case "$status" in
      PASS) echo "✓ $name" ;;
      FAIL) echo "✗ $name: $details" ;;
      SKIP) echo "○ $name: $details" ;;
    esac
  done
}

curl_json_expect() {
  local expect="$1"
  shift
  local tmp
  tmp="$(mktemp)"
  local code
  code="$(curl -ksS -o "$tmp" -w "%{http_code}" "$@" || echo "000")"
  if [[ "$code" != "$expect" ]]; then
    echo "unexpected status: got=$code expect=$expect url=${*: -1}" >&2
    echo "response body:" >&2
    cat "$tmp" >&2
    rm -f "$tmp"
    return 1
  fi
  cat "$tmp"
  rm -f "$tmp"
}

wait_for() {
  local timeout="$1"
  local sleep_secs="$2"
  shift 2
  local start=$SECONDS
  while (( SECONDS - start < timeout )); do
    if "$@"; then
      return 0
    fi
    sleep "$sleep_secs"
  done
  return 1
}

repo_path() {
  local full_name="$1"
  echo "$GIT_REPO_DIR/$full_name.git"
}

start_server() {
  note "Building gh-server..."
  make -C "$ROOT" build >/dev/null
  note "Starting gh-server (DB_DSN=$DB_DSN, GIT_REPO_DIR=$GIT_REPO_DIR)"
  ENVIRONMENT=development \
  LISTEN_MODE=production \
  PORT="$SERVER_PORT" \
  BASE_URL="$BASE_URL" \
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

create_repo() {
  local name="$1"
  curl_json_expect 201 \
    -X POST "$BASE_URL/api/v3/user/repos" \
    -H "Authorization: token $ADMIN_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"$name\",\"private\":true,\"auto_init\":false,\"default_branch\":\"main\"}"
}

prepare_local_repo() {
  local repo_dir="$1"
  local branch="$2"
  local workflow_name="$3"
  local workflow_file="$4"

  mkdir -p "$repo_dir"
  git init "$repo_dir" >/dev/null
  git -C "$repo_dir" checkout -b "$branch" >/dev/null
  git -C "$repo_dir" config user.email "e2e@example.test"
  git -C "$repo_dir" config user.name "E2E Test"
  write_workflow_file "$repo_dir" "$workflow_file" "$workflow_name"
  echo "post-processing" > "$repo_dir/README.md"
  commit_repo_changes "$repo_dir" "add workflow"
}

write_workflow_file() {
  local repo_dir="$1"
  local workflow_file="$2"
  local workflow_name="$3"

  mkdir -p "$repo_dir/.github/workflows"
  cat > "$repo_dir/.github/workflows/$workflow_file" <<EOF
name: $workflow_name
on:
  push:
  workflow_dispatch:
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: echo
        run: echo '$workflow_name'
EOF
}

commit_repo_changes() {
  local repo_dir="$1"
  local message="$2"
  git -C "$repo_dir" add -A
  git -C "$repo_dir" commit -m "$message" >/dev/null
}

git_push_with_auth() {
  local repo_dir="$1"
  local branch="$2"
  git -C "$repo_dir" -c http.extraHeader="Authorization: token $ADMIN_TOKEN" push -q origin "$branch"
}

check_head_ref() {
  local bare_repo="$1"
  local expect="$2"
  local head
  head="$(git -C "$bare_repo" symbolic-ref HEAD 2>/dev/null || true)"
  [[ "$head" == "$expect" ]]
}

fetch_ref_sha() {
  local repo_full="$1"
  local branch="$2"
  local tmp
  tmp="$(mktemp)"
  local code
  code="$(curl -ksS -o "$tmp" -w "%{http_code}" \
    -H "Authorization: token $ADMIN_TOKEN" \
    "$BASE_URL/api/v3/repos/$repo_full/git/refs/heads/$branch" || echo "000")"
  if [[ "$code" != "200" ]]; then
    rm -f "$tmp"
    return 1
  fi
  local sha
  sha="$(jq -r '.object.sha // empty' "$tmp")"
  rm -f "$tmp"
  if [[ -z "$sha" || "$sha" == "null" ]]; then
    return 1
  fi
  echo "$sha"
}

workflow_present() {
  local repo_full="$1"
  local workflow_path="$2"
  local workflow_name="$3"
  local json
  if ! json="$(workflow_list_json "$repo_full")"; then
    return 1
  fi
  if jq -e --arg path "$workflow_path" --arg name "$workflow_name" \
    '.workflows[]? | select(.path==$path and .name==$name) | .id' \
    <<<"$json" >/dev/null 2>&1; then
    return 0
  fi
  return 1
}

workflow_list_json() {
  local repo_full="$1"
  curl_json_expect 200 \
    -H "Authorization: token $ADMIN_TOKEN" \
    "$BASE_URL/api/v3/repos/$repo_full/actions/workflows"
}

workflow_absent() {
  local repo_full="$1"
  local workflow_path="$2"
  local json
  if ! json="$(workflow_list_json "$repo_full")"; then
    return 1
  fi
  ! jq -e --arg path "$workflow_path" \
    '.workflows[]? | select(.path==$path)' \
    <<<"$json" >/dev/null 2>&1
}

workflow_count_is() {
  local repo_full="$1"
  local expect="$2"
  local json
  if ! json="$(workflow_list_json "$repo_full")"; then
    return 1
  fi
  local count
  count="$(jq -r '.total_count // -1' <<<"$json")"
  [[ "$count" == "$expect" ]]
}

workflow_id_by_path() {
  local repo_full="$1"
  local workflow_path="$2"
  local json
  if ! json="$(workflow_list_json "$repo_full")"; then
    return 1
  fi
  jq -er --arg path "$workflow_path" \
    '.workflows[]? | select(.path==$path) | .id' \
    <<<"$json" | head -n1
}

workflow_runs_json() {
  local repo_full="$1"
  curl_json_expect 200 \
    -H "Authorization: token $ADMIN_TOKEN" \
    "$BASE_URL/api/v3/repos/$repo_full/actions/runs"
}

workflow_runs_count_is() {
  local repo_full="$1"
  local expect="$2"
  local json
  if ! json="$(workflow_runs_json "$repo_full")"; then
    return 1
  fi
  local count
  count="$(jq -r '.total_count // -1' <<<"$json")"
  [[ "$count" == "$expect" ]]
}

dispatch_workflow_expect() {
  local expect="$1"
  local repo_full="$2"
  local workflow_id="$3"
  local ref="$4"
  curl_json_expect "$expect" \
    -X POST "$BASE_URL/api/v3/repos/$repo_full/actions/workflows/$workflow_id/dispatches" \
    -H "Authorization: token $ADMIN_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"ref\":\"$ref\"}" >/dev/null
}

readyz_ok() {
  local tmp
  tmp="$(mktemp)"
  local code
  code="$(curl -ksS -o "$tmp" -w "%{http_code}" "$BASE_URL/readyz" || echo "000")"
  if [[ "$code" != "200" ]]; then
    rm -f "$tmp"
    return 1
  fi
  local status
  status="$(jq -r '.status // empty' "$tmp")"
  rm -f "$tmp"
  [[ "$status" == "ready" ]]
}

test_success_postprocessing() {
  local test_name="postprocessing-success"
  note "--- Success case: push workflows and sync ---"

  local repo_name="pp-success-$RANDOM_SUFFIX"
  local repo_json
  if ! repo_json="$(create_repo "$repo_name")"; then
    record_result "$test_name" "FAIL" "repo creation failed"
    return
  fi

  local repo_full repo_id
  repo_full="$(jq -r '.full_name // empty' <<<"$repo_json")"
  repo_id="$(jq -r '.id // empty' <<<"$repo_json")"
  if [[ -z "$repo_full" || -z "$repo_id" || "$repo_id" == "null" ]]; then
    record_result "$test_name" "FAIL" "missing repo identifiers"
    return
  fi

  local repo_dir="$WORK_DIR/$repo_name"
  local workflow_file="ci-success.yml"
  local workflow_name="CI Success"
  prepare_local_repo "$repo_dir" "master" "$workflow_name" "$workflow_file"
  git -C "$repo_dir" remote add origin "$BASE_URL/$repo_full.git"

  if ! git_push_with_auth "$repo_dir" "master"; then
    record_result "$test_name" "FAIL" "git push failed"
    return
  fi

  local commit_sha
  commit_sha="$(git -C "$repo_dir" rev-parse HEAD)"

  local bare_repo
  bare_repo="$(repo_path "$repo_full")"
  if ! wait_for 10 0.2 check_head_ref "$bare_repo" "refs/heads/master"; then
    record_result "$test_name" "FAIL" "HEAD did not update to refs/heads/master"
    return
  fi

  local ref_sha
  if ! ref_sha="$(fetch_ref_sha "$repo_full" "master")"; then
    record_result "$test_name" "FAIL" "failed to resolve ref via API"
    return
  fi
  if [[ "$ref_sha" != "$commit_sha" ]]; then
    record_result "$test_name" "FAIL" "ref sha mismatch (api=$ref_sha local=$commit_sha)"
    return
  fi

  local workflow_path=".github/workflows/$workflow_file"
  if ! wait_for 15 0.5 workflow_present "$repo_full" "$workflow_path" "$workflow_name"; then
    record_result "$test_name" "FAIL" "workflow not synced to API"
    return
  fi

  record_result "$test_name" "PASS" "refs valid and workflows synced"
}

test_workflow_sync_cleanup_after_push() {
  local test_name="workflow-sync-cleanup-after-push"
  note "--- Cleanup case: rename and delete workflows after push ---"

  local repo_name="pp-workflow-cleanup-$RANDOM_SUFFIX"
  local repo_json
  if ! repo_json="$(create_repo "$repo_name")"; then
    record_result "$test_name" "FAIL" "repo creation failed"
    return
  fi

  local repo_full
  repo_full="$(jq -r '.full_name // empty' <<<"$repo_json")"
  if [[ -z "$repo_full" ]]; then
    record_result "$test_name" "FAIL" "missing repo full name"
    return
  fi

  local repo_dir="$WORK_DIR/$repo_name"
  local initial_file="cleanup-a.yml"
  local initial_name="Workflow Cleanup A"
  local initial_path=".github/workflows/$initial_file"
  prepare_local_repo "$repo_dir" "master" "$initial_name" "$initial_file"
  git -C "$repo_dir" remote add origin "$BASE_URL/$repo_full.git"

  if ! git_push_with_auth "$repo_dir" "master"; then
    record_result "$test_name" "FAIL" "initial git push failed"
    return
  fi

  if ! wait_for 15 0.5 workflow_present "$repo_full" "$initial_path" "$initial_name"; then
    record_result "$test_name" "FAIL" "initial workflow not synced"
    return
  fi

  if ! wait_for 15 0.5 workflow_count_is "$repo_full" 1; then
    record_result "$test_name" "FAIL" "initial workflow count did not settle to 1"
    return
  fi

  local initial_id
  if ! initial_id="$(workflow_id_by_path "$repo_full" "$initial_path")"; then
    record_result "$test_name" "FAIL" "failed to resolve initial workflow id"
    return
  fi

  if ! dispatch_workflow_expect 204 "$repo_full" "$initial_id" "master"; then
    record_result "$test_name" "FAIL" "initial workflow dispatch failed"
    return
  fi

  if ! wait_for 10 0.2 workflow_runs_count_is "$repo_full" 1; then
    record_result "$test_name" "FAIL" "initial workflow dispatch did not create a run"
    return
  fi

  local renamed_file="cleanup-renamed.yml"
  local renamed_name="Workflow Cleanup Renamed"
  local renamed_path=".github/workflows/$renamed_file"

  mv "$repo_dir/.github/workflows/$initial_file" "$repo_dir/.github/workflows/$renamed_file"
  write_workflow_file "$repo_dir" "$renamed_file" "$renamed_name"
  commit_repo_changes "$repo_dir" "rename workflow"

  if ! git_push_with_auth "$repo_dir" "master"; then
    record_result "$test_name" "FAIL" "rename git push failed"
    return
  fi

  if ! wait_for 15 0.5 workflow_present "$repo_full" "$renamed_path" "$renamed_name"; then
    record_result "$test_name" "FAIL" "renamed workflow not synced"
    return
  fi

  if ! wait_for 15 0.5 workflow_count_is "$repo_full" 1; then
    record_result "$test_name" "FAIL" "workflow count after rename did not settle to 1"
    return
  fi

  if ! wait_for 15 0.5 workflow_absent "$repo_full" "$initial_path"; then
    record_result "$test_name" "FAIL" "stale workflow path remained after rename"
    return
  fi

  local renamed_id
  if ! renamed_id="$(workflow_id_by_path "$repo_full" "$renamed_path")"; then
    record_result "$test_name" "FAIL" "failed to resolve renamed workflow id"
    return
  fi
  if [[ "$renamed_id" == "$initial_id" ]]; then
    record_result "$test_name" "FAIL" "workflow id was reused after rename"
    return
  fi

  if ! dispatch_workflow_expect 404 "$repo_full" "$initial_id" "master"; then
    record_result "$test_name" "FAIL" "stale workflow id remained dispatchable after rename"
    return
  fi

  if ! wait_for 5 0.2 workflow_runs_count_is "$repo_full" 1; then
    record_result "$test_name" "FAIL" "stale dispatch after rename created a workflow run"
    return
  fi

  if ! dispatch_workflow_expect 204 "$repo_full" "$renamed_id" "master"; then
    record_result "$test_name" "FAIL" "renamed workflow dispatch failed"
    return
  fi

  if ! wait_for 10 0.2 workflow_runs_count_is "$repo_full" 2; then
    record_result "$test_name" "FAIL" "renamed workflow dispatch did not create a run"
    return
  fi

  rm -f "$repo_dir/.github/workflows/$renamed_file"
  commit_repo_changes "$repo_dir" "delete workflow"

  if ! git_push_with_auth "$repo_dir" "master"; then
    record_result "$test_name" "FAIL" "delete git push failed"
    return
  fi

  if ! wait_for 15 0.5 workflow_count_is "$repo_full" 0; then
    record_result "$test_name" "FAIL" "workflow list did not empty after delete"
    return
  fi

  if ! dispatch_workflow_expect 404 "$repo_full" "$renamed_id" "master"; then
    record_result "$test_name" "FAIL" "deleted workflow remained dispatchable"
    return
  fi

  if ! wait_for 5 0.2 workflow_runs_count_is "$repo_full" 2; then
    record_result "$test_name" "FAIL" "stale dispatch after delete created a workflow run"
    return
  fi

  record_result "$test_name" "PASS" "rename/delete cleanup and stale dispatch rejection verified"
}

cleanup() {
  note "Cleaning up..."
  if [[ -n "${SERVER_PID:-}" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  if [[ -n "${WORK_DIR:-}" && -d "$WORK_DIR" ]]; then
    rm -rf "$WORK_DIR"
  fi
  if [[ -n "${GIT_REPO_DIR:-}" && -d "$GIT_REPO_DIR" ]]; then
    rm -rf "$GIT_REPO_DIR"
  fi
  tidb_drop_database "$TIDB_DB_NAME"
}
trap cleanup EXIT

setup() {
  ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  # shellcheck source=./lib.sh
  source "$ROOT/e2e/lib.sh"

  require_cmd curl
  require_cmd jq
  require_cmd git
  require_cmd make
  require_cmd mysql
  require_cmd python3

  RANDOM_SUFFIX="$(date +%s)-$RANDOM"
  SERVER_PORT="${E2E_PORT:-$((RANDOM % 10000 + 20000))}"
  BASE_URL="http://localhost:$SERVER_PORT"
  ADMIN_LOGIN="e2e-admin-$RANDOM_SUFFIX"
  ADMIN_TOKEN="e2e-token-$RANDOM_SUFFIX"
  TIDB_DB_NAME="postprocess_e2e_${RANDOM_SUFFIX//[^A-Za-z0-9_]/_}"
  DB_DSN="$(tidb_dsn_for_database "$TIDB_DB_NAME")"
  GIT_REPO_DIR="$(mktemp -d /tmp/gh-server-postprocess-repos.XXXXXX)"
  WORK_DIR="$(mktemp -d /tmp/gh-server-postprocess-work.XXXXXX)"
  LOG_FILE="/tmp/gh-server-postprocess-e2e-$RANDOM_SUFFIX.log"

  note "=== Push Post-Processing Consistency E2E ==="
  tidb_create_database "$TIDB_DB_NAME"
  start_server
}

setup

test_success_postprocessing
test_workflow_sync_cleanup_after_push

print_summary

if [[ "$TESTS_FAILED" -gt 0 ]]; then
  echo ""
  note "WARNING: $TESTS_FAILED test(s) failed"
  exit 1
fi
