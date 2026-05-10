#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=/dev/null
source "$ROOT_DIR/e2e/lib.sh"

require_cmd curl
require_cmd git
require_cmd go
require_cmd jq
require_cmd python3

backend="$(git --exec-path)/git-http-backend"
if [[ ! -x "$backend" ]]; then
  echo "git-http-backend not found at $backend" >&2
  exit 1
fi

pick_port() {
  python3 - <<'PY'
import socket

sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
sock.bind(("127.0.0.1", 0))
print(sock.getsockname()[1])
sock.close()
PY
}

RANDOM_SUFFIX="$(date +%s)-$RANDOM"
SERVER_PORT="${SMOKE_PORT:-}"
if [[ -z "$SERVER_PORT" ]]; then
  SERVER_PORT="$(pick_port 2>/dev/null || echo 18080)"
fi
BASE_URL="${BASE_URL:-http://127.0.0.1:$SERVER_PORT}"
ADMIN_LOGIN="${ADMIN_LOGIN:-smoke-admin-$RANDOM_SUFFIX}"
ADMIN_TOKEN="${ADMIN_TOKEN:-smoke-token-$RANDOM_SUFFIX}"
REPO_NAME="${REPO_NAME:-smoke-repo-$RANDOM_SUFFIX}"
SQLITE_DB="$(mktemp /tmp/gh-server-smoke.XXXXXX.db)"
GIT_REPO_DIR="$(mktemp -d /tmp/gh-server-smoke-repos.XXXXXX)"
CLONE_DIR="$(mktemp -d /tmp/gh-server-smoke-clone.XXXXXX)"
SERVER_BIN="$(mktemp /tmp/gh-server-smoke-bin.XXXXXX)"
LOG_FILE="${LOG_FILE:-/tmp/gh-server-smoke-$RANDOM_SUFFIX.log}"

cleanup() {
  local status=$?
  if [[ -n "${SERVER_PID:-}" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  if [[ $status -ne 0 && -f "$LOG_FILE" ]]; then
    echo "--- gh-server smoke log tail ---" >&2
    tail -n 40 "$LOG_FILE" >&2 || true
  fi
  rm -rf "$CLONE_DIR" "$GIT_REPO_DIR"
  rm -f "$SERVER_BIN"
  rm -f "$SQLITE_DB"
}
trap cleanup EXIT

note "Building gh-server"
(
  cd "$ROOT_DIR"
  go build -o "$SERVER_BIN" .
)

note "Starting gh-server on $BASE_URL"
ENVIRONMENT=development \
LISTEN_MODE=production \
PORT="$SERVER_PORT" \
BASE_URL="$BASE_URL" \
ADMIN_LOGIN="$ADMIN_LOGIN" \
ADMIN_TOKEN="$ADMIN_TOKEN" \
DB_DSN="sqlite:$SQLITE_DB" \
GIT_REPO_DIR="$GIT_REPO_DIR" \
"$SERVER_BIN" >"$LOG_FILE" 2>&1 &
SERVER_PID=$!

wait_for_http_ready \
  "$BASE_URL/readyz" \
  "Waiting for gh-server to become ready..." \
  "gh-server is ready" \
  "gh-server failed to become ready (see $LOG_FILE)"

note "Checking discovery and metadata endpoints"
curl -fsS "$BASE_URL/readyz" >/dev/null
curl_json 200 "$BASE_URL/api/v3/" >/dev/null
curl_json 200 "$BASE_URL/api/v3/meta" >/dev/null
curl_json 200 "$BASE_URL/api/v3/rate_limit" >/dev/null
curl_json 200 -H "Authorization: token $ADMIN_TOKEN" "$BASE_URL/api/v3/user" >/dev/null

note "Creating a repository through the REST API"
repo_json="$(curl_json 201 \
  -X POST "$BASE_URL/api/v3/user/repos" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$REPO_NAME\",\"add_readme\":true}")"
repo_full_name="$(printf '%s' "$repo_json" | jq -r '.full_name')"
assert_eq "$repo_full_name" "$ADMIN_LOGIN/$REPO_NAME"
ok "created $repo_full_name"

note "Verifying the repository is readable via REST"
curl_json 200 \
  -H "Authorization: token $ADMIN_TOKEN" \
  "$BASE_URL/api/v3/repos/$ADMIN_LOGIN/$REPO_NAME" >/dev/null

note "Verifying Git Smart HTTP clone"
git -c http.extraHeader="Authorization: token $ADMIN_TOKEN" \
  clone -q "$BASE_URL/$ADMIN_LOGIN/$REPO_NAME.git" "$CLONE_DIR/repo"

test -d "$CLONE_DIR/repo/.git"
git -C "$CLONE_DIR/repo" rev-parse --is-inside-work-tree >/dev/null
ok "git clone succeeded"

echo "Backend smoke passed."
