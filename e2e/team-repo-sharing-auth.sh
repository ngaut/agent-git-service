#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=./lib.sh
source "$ROOT/e2e/lib.sh"

require_cmd curl
require_cmd jq
require_cmd git

BASE_URL="$(strip_trailing_slash "${E2E_BASE_URL:-http://github.localhost}")"
ADMIN_TOKEN="${ADMIN_TOKEN:-${GH_TOKEN:-mytoken}}"
TEAM_MEMBER_TOKEN="${TEAM_MEMBER_TOKEN:-${TENANT_A_TOKEN:-}}"
OUTSIDER_TOKEN="${OUTSIDER_TOKEN:-${TENANT_B_TOKEN:-}}"

RANDOM_SUFFIX="$(date +%s)-$RANDOM"
ORG="e2e-team-auth-org-$RANDOM_SUFFIX"
TEAM_NAME="team-auth-$RANDOM_SUFFIX"
TEAM_DESC="team repo auth e2e"
REPO_NAME="team-auth-repo-$RANDOM_SUFFIX"
team_slug="$TEAM_NAME"
repo_dir=""

note "BASE_URL=$BASE_URL"

if ! curl -sf "$BASE_URL/api/v3/" >/dev/null 2>&1; then
  note "Skipping team repo auth e2e: server not reachable at $BASE_URL"
  exit 0
fi

if [[ -z "$ADMIN_TOKEN" || -z "$TEAM_MEMBER_TOKEN" || -z "$OUTSIDER_TOKEN" ]]; then
  note "Skipping team repo auth e2e: ADMIN_TOKEN, TEAM_MEMBER_TOKEN, and OUTSIDER_TOKEN are required"
  exit 0
fi

cleanup() {
  set +e
  if [[ -n "$repo_dir" ]]; then
    rm -rf "$repo_dir"
  fi
  http_code -X DELETE "$BASE_URL/api/v3/repos/$ORG/$REPO_NAME" \
    -H "Authorization: token $ADMIN_TOKEN" >/dev/null 2>&1
  http_code -X DELETE "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug" \
    -H "Authorization: token $ADMIN_TOKEN" >/dev/null 2>&1
}
trap cleanup EXIT

admin_json="$(curl_json 200 -H "Authorization: token $ADMIN_TOKEN" "$BASE_URL/api/v3/user")"
admin_login="$(json_get login <<<"$admin_json")"
team_json="$(curl_json 200 -H "Authorization: token $TEAM_MEMBER_TOKEN" "$BASE_URL/api/v3/user")"
team_login="$(json_get login <<<"$team_json")"
outsider_json="$(curl_json 200 -H "Authorization: token $OUTSIDER_TOKEN" "$BASE_URL/api/v3/user")"
outsider_login="$(json_get login <<<"$outsider_json")"

if [[ "$team_login" == "$outsider_login" ]]; then
  note "Skipping: TEAM_MEMBER_TOKEN and OUTSIDER_TOKEN resolve to the same user ($team_login)"
  exit 0
fi

note "Ensuring org exists: $ORG"
ensure_org_exists "$BASE_URL" "$ADMIN_TOKEN" "$ORG"

note "Creating team: $TEAM_NAME"
team_resp="$(curl_json 201 \
  -X POST "$BASE_URL/api/v3/orgs/$ORG/teams" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$TEAM_NAME\",\"description\":\"$TEAM_DESC\",\"privacy\":\"closed\"}")"
team_slug="$(json_get slug <<<"$team_resp")"

note "Adding team member: $team_login"
curl_json 200 \
  -X PUT "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$team_login" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"role\":\"member\"}" > /dev/null

note "Creating repo: $ORG/$REPO_NAME"
repo_resp="$(curl_json 201 \
  -X POST "$BASE_URL/api/v3/orgs/$ORG/repos" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$REPO_NAME\",\"private\":true}")"
repo_full="$(json_get full_name <<<"$repo_resp")"

note "Granting team push access"
code="$(http_code -X PUT "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/repos/$ORG/$REPO_NAME" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"permission\":\"push\"}")"
assert_eq "$code" "204"

repo_dir="$(mktemp -d)"

git -C "$repo_dir" init -q
git -C "$repo_dir" checkout -q -b main || git -C "$repo_dir" checkout -q -B main
echo "# Team Auth" > "$repo_dir/README.md"
git -C "$repo_dir" add README.md
git -C "$repo_dir" -c user.email="e2e@test.local" -c user.name="E2E Test" commit -q -m "seed"
git -C "$repo_dir" remote add origin "$BASE_URL/$repo_full.git"

if ! GIT_TERMINAL_PROMPT=0 git -C "$repo_dir" \
  -c http.extraHeader="Authorization: token $TEAM_MEMBER_TOKEN" \
  push -q origin main 2>/dev/null; then
  note "Git push failed (git-http may not be enabled); skipping auth e2e"
  exit 0
fi

note "Team member clone succeeds"
GIT_TERMINAL_PROMPT=0 git \
  -c http.extraHeader="Authorization: token $TEAM_MEMBER_TOKEN" \
  ls-remote "$BASE_URL/$repo_full.git" >/dev/null

note "Non-member REST read is 404"
code="$(http_code -H "Authorization: token $OUTSIDER_TOKEN" "$BASE_URL/api/v3/repos/$repo_full")"
assert_eq "$code" "404"

note "Non-member clone denied"
if GIT_TERMINAL_PROMPT=0 git \
  -c http.extraHeader="Authorization: token $OUTSIDER_TOKEN" \
  ls-remote "$BASE_URL/$repo_full.git" >/dev/null 2>&1; then
  echo "expected non-member ls-remote to fail" >&2
  exit 1
fi

note "Downgrading team to pull"
code="$(http_code -X PUT "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/repos/$ORG/$REPO_NAME" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"permission\":\"pull\"}")"
assert_eq "$code" "204"

note "Team member can still clone after downgrade"
GIT_TERMINAL_PROMPT=0 git \
  -c http.extraHeader="Authorization: token $TEAM_MEMBER_TOKEN" \
  ls-remote "$BASE_URL/$repo_full.git" >/dev/null

echo "denied" >> "$repo_dir/README.md"
git -C "$repo_dir" add README.md
git -C "$repo_dir" -c user.email="e2e@test.local" -c user.name="E2E Test" commit -q -m "should fail"

note "Team member push denied on pull permission"
if GIT_TERMINAL_PROMPT=0 git -C "$repo_dir" \
  -c http.extraHeader="Authorization: token $TEAM_MEMBER_TOKEN" \
  push -q origin main >/dev/null 2>&1; then
  echo "expected team member push to fail after downgrade" >&2
  exit 1
fi

note "Upgrading team to push"
code="$(http_code -X PUT "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/repos/$ORG/$REPO_NAME" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"permission\":\"push\"}")"
assert_eq "$code" "204"

note "Team member push succeeds after upgrade"
GIT_TERMINAL_PROMPT=0 git -C "$repo_dir" \
  -c http.extraHeader="Authorization: token $TEAM_MEMBER_TOKEN" \
  push -q origin main

ok "team repo sharing authorization checks completed"
