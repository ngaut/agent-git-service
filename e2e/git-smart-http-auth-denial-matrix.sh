#!/usr/bin/env bash
set -euo pipefail

# E2E: Git Smart HTTP control-plane auth denial matrix
# Covers: info/refs, git-upload-pack, git-receive-pack
# Matrix: no token, malformed token, invalid token, wrong-tenant token

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=./lib.sh
source "$ROOT/e2e/lib.sh"

require_cmd curl
require_cmd jq
require_cmd git

BASE_URL="$(strip_trailing_slash "${E2E_BASE_URL:-http://localhost:8080}")"
CONTROL_PLANE_MODE="${CONTROL_PLANE_MODE:-true}"
TENANT_A_TOKEN="${TENANT_A_TOKEN:-}"
TENANT_B_TOKEN="${TENANT_B_TOKEN:-}"

note "BASE_URL=$BASE_URL CONTROL_PLANE_MODE=$CONTROL_PLANE_MODE"

if [[ "$CONTROL_PLANE_MODE" != "true" ]]; then
  note "Control-plane mode disabled; skipping Git Smart HTTP auth matrix"
  exit 0
fi

if [[ -z "$TENANT_A_TOKEN" || -z "$TENANT_B_TOKEN" ]]; then
  note "TENANT_A_TOKEN/TENANT_B_TOKEN not set; skipping Git Smart HTTP auth matrix"
  exit 0
fi

code="$(http_code "$BASE_URL/api/v3/")"
assert_eq "$code" "200"
ok "Server is responding"

repo_name="githttp-auth-matrix-$(date +%s)"
repo_json="$(curl_json 201 \
  -X POST "$BASE_URL/api/v3/user/repos" \
  -H "Authorization: token $TENANT_A_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$repo_name\",\"private\":true}")"
repo_full="$(json_get full_name <<<"$repo_json")"

note "Created repo: $repo_full"

work_dir="$(mktemp -d)"
trap "rm -rf '$work_dir'" RETURN

git -C "$work_dir" init -q
# Ensure deterministic branch name
if git -C "$work_dir" checkout -q -b main 2>/dev/null; then
  :
else
  git -C "$work_dir" checkout -q -B main
fi

echo "# Auth Matrix" > "$work_dir/README.md"
git -C "$work_dir" add README.md
git -C "$work_dir" -c user.email="e2e@test.local" -c user.name="E2E Test" commit -q -m "seed"
git -C "$work_dir" remote add origin "$BASE_URL/$repo_full.git"

if ! GIT_TERMINAL_PROMPT=0 git -C "$work_dir" \
  -c http.extraHeader="Authorization: token $TENANT_A_TOKEN" \
  push -q origin main 2>/dev/null; then
  note "Git push failed (git-http may not be enabled); skipping auth matrix"
  exit 0
fi

get_remote_refs() {
  git -c http.extraHeader="Authorization: token $TENANT_A_TOKEN" \
    ls-remote --heads "$BASE_URL/$repo_full.git" 2>/dev/null | sort
}

baseline_refs="$(get_remote_refs)"
if [[ -z "$baseline_refs" ]]; then
  note "No refs detected after seed push; skipping auth matrix"
  exit 0
fi

note "Baseline refs captured"

request_code() {
  local method="$1"
  local url="$2"
  local auth_header="${3:-}"

  local args=(-ksS -o /dev/null -w "%{http_code}" -X "$method")
  if [[ -n "$auth_header" ]]; then
    args+=(-H "Authorization: $auth_header")
  fi
  if [[ "$method" == "POST" ]]; then
    args+=(--data-binary "")
  fi

  curl "${args[@]}" "$url" 2>/dev/null || echo "000"
}

assert_refs_unchanged() {
  local label="$1"
  local current
  current="$(get_remote_refs)"
  if [[ "$current" != "$baseline_refs" ]]; then
    echo "ERROR: refs changed after denied receive-pack ($label)" >&2
    echo "Baseline refs:" >&2
    echo "$baseline_refs" >&2
    echo "Current refs:" >&2
    echo "$current" >&2
    exit 1
  fi
}

run_matrix_case() {
  local label="$1"
  local auth_header="$2"
  local expected_code="$3"

  note "Case: $label"

  local first_code=""
  local endpoint
  local name method url code

  for endpoint in "${ENDPOINTS[@]}"; do
    IFS='|' read -r name method url <<< "$endpoint"
    code="$(request_code "$method" "$url" "$auth_header")"

    if [[ "$code" == 2* ]]; then
      echo "ERROR: $label $name returned success status $code" >&2
      exit 1
    fi

    if [[ "$code" != "$expected_code" ]]; then
      echo "ERROR: $label $name expected $expected_code, got $code" >&2
      exit 1
    fi

    if [[ -z "$first_code" ]]; then
      first_code="$code"
    elif [[ "$code" != "$first_code" ]]; then
      echo "ERROR: $label response codes are inconsistent ($first_code vs $code)" >&2
      exit 1
    fi

    if [[ "$name" == "receive-pack" ]]; then
      assert_refs_unchanged "$label"
    fi

    ok "$label $name denied ($code)"
  done
}

ENDPOINTS=(
  "info-refs|GET|$BASE_URL/$repo_full.git/info/refs?service=git-upload-pack"
  "upload-pack|POST|$BASE_URL/$repo_full.git/git-upload-pack"
  "receive-pack|POST|$BASE_URL/$repo_full.git/git-receive-pack"
)

AUTH_NONE=""
AUTH_MALFORMED="Basic not-base64"
AUTH_INVALID="token invalid-$(date +%s)"
AUTH_WRONG_TENANT="token $TENANT_B_TOKEN"

run_matrix_case "no-token" "$AUTH_NONE" "401"
run_matrix_case "malformed-token" "$AUTH_MALFORMED" "401"
run_matrix_case "invalid-token" "$AUTH_INVALID" "401"
run_matrix_case "wrong-tenant-token" "$AUTH_WRONG_TENANT" "404"

note "=== Git Smart HTTP control-plane auth denial matrix passed ==="
ok "All endpoints denied consistently with no receive-pack side effects"
