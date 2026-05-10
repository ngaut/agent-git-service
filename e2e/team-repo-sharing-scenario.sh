#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=./lib.sh
source "$ROOT/e2e/lib.sh"
# shellcheck source=./helpers.sh
source "$ROOT/e2e/helpers.sh"

require_cmd curl
require_cmd jq
require_cmd git

BASE_URL="$(strip_trailing_slash "${E2E_BASE_URL:-http://github.localhost}")"
ADMIN_TOKEN="${ADMIN_TOKEN:-${GH_TOKEN:-mytoken}}"
MEMBER1_TOKEN="${TEAM_MEMBER1_TOKEN:-${MEMBER1_TOKEN:-}}"
MEMBER2_TOKEN="${TEAM_MEMBER2_TOKEN:-${MEMBER2_TOKEN:-}}"
NON_MEMBER_TOKEN="${TEAM_NON_MEMBER_TOKEN:-${NON_MEMBER_TOKEN:-}}"

RANDOM_SUFFIX="$(date +%s)-$RANDOM"
ORG="${TEAM_REPO_ORG:-e2e-team-org-$RANDOM_SUFFIX}"
TEAM_NAME="team-$RANDOM_SUFFIX"
TEAM_DESC="team repo sharing scenario e2e"
REPO_ONE="conventions-$RANDOM_SUFFIX"
REPO_TWO="tasks-$RANDOM_SUFFIX"

TEST_RESULTS=()
TESTS_PASSED=0
TESTS_FAILED=0
TESTS_SKIPPED=0
CURRENT_STEP="init"
work_dir=""
RESULT_RECORDED="false"

on_exit() {
  local status=$?
  if [[ "$RESULT_RECORDED" != "true" && "$status" -ne 0 ]]; then
    record_result "team-repo-sharing-scenario" "FAIL" "failed at: $CURRENT_STEP"
    RESULT_RECORDED="true"
  fi
  if [[ -n "$work_dir" && -d "$work_dir" ]]; then
    rm -rf "$work_dir"
  fi
  print_summary
  exit "$status"
}

trap on_exit EXIT

note "BASE_URL=$BASE_URL"

if ! curl -sf "$BASE_URL/api/v3/" >/dev/null 2>&1; then
  note "Skipping team repo sharing scenario: server not reachable at $BASE_URL"
  record_result "team-repo-sharing-scenario" "SKIP" "server not reachable"
  RESULT_RECORDED="true"
  exit 0
fi

if [[ -z "$ADMIN_TOKEN" ]]; then
  echo "ADMIN_TOKEN is required" >&2
  exit 1
fi

if [[ -z "$MEMBER1_TOKEN" || -z "$MEMBER2_TOKEN" || -z "$NON_MEMBER_TOKEN" ]]; then
  note "Skipping team repo sharing scenario: TEAM_MEMBER1_TOKEN, TEAM_MEMBER2_TOKEN, TEAM_NON_MEMBER_TOKEN required"
  record_result "team-repo-sharing-scenario" "SKIP" "member tokens not set"
  RESULT_RECORDED="true"
  exit 0
fi

CURRENT_STEP="resolve-logins"
admin_login="$(curl_json 200 -H "Authorization: token $ADMIN_TOKEN" "$BASE_URL/api/v3/user" | json_get login)"
member1_login="$(curl_json 200 -H "Authorization: token $MEMBER1_TOKEN" "$BASE_URL/api/v3/user" | json_get login)"
member2_login="$(curl_json 200 -H "Authorization: token $MEMBER2_TOKEN" "$BASE_URL/api/v3/user" | json_get login)"
non_member_login="$(curl_json 200 -H "Authorization: token $NON_MEMBER_TOKEN" "$BASE_URL/api/v3/user" | json_get login)"

if [[ "$admin_login" == "$member1_login" || "$admin_login" == "$member2_login" ]]; then
  echo "ADMIN_TOKEN must be distinct from TEAM_MEMBER1_TOKEN and TEAM_MEMBER2_TOKEN" >&2
  exit 1
fi
if [[ "$member1_login" == "$member2_login" ]]; then
  echo "TEAM_MEMBER1_TOKEN and TEAM_MEMBER2_TOKEN must be distinct users" >&2
  exit 1
fi
if [[ "$non_member_login" == "$admin_login" || "$non_member_login" == "$member1_login" || "$non_member_login" == "$member2_login" ]]; then
  echo "TEAM_NON_MEMBER_TOKEN must be a user outside the org/team" >&2
  exit 1
fi

CURRENT_STEP="org-auto-create"
note "Ensuring org exists: $ORG"
ensure_org_exists "$BASE_URL" "$ADMIN_TOKEN" "$ORG"

CURRENT_STEP="create-team"
note "Creating team: $TEAM_NAME"
team_resp="$(curl_json 201 \
  -X POST "$BASE_URL/api/v3/orgs/$ORG/teams" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$TEAM_NAME\",\"description\":\"$TEAM_DESC\",\"privacy\":\"closed\"}")"
team_slug="$(json_get slug <<<"$team_resp")"
assert_eq "$team_slug" "$TEAM_NAME"

CURRENT_STEP="add-members"
for login in "$member1_login" "$member2_login"; do
  note "Adding team member: $login"
  membership_resp="$(curl_json 200 \
    -X PUT "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$login" \
    -H "Authorization: token $ADMIN_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"role\":\"member\"}")"
  assert_eq "$(json_get role <<<"$membership_resp")" "member"

  membership_get="$(curl_json 200 \
    -H "Authorization: token $ADMIN_TOKEN" \
    "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$login")"
  assert_eq "$(json_get role <<<"$membership_get")" "member"
done

CURRENT_STEP="create-repos"
note "Creating repo: $ORG/$REPO_ONE"
repo_one_resp="$(curl_json 201 \
  -X POST "$BASE_URL/api/v3/orgs/$ORG/repos" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$REPO_ONE\",\"private\":true}")"
assert_eq "$(json_get full_name <<<"$repo_one_resp")" "$ORG/$REPO_ONE"

note "Creating repo: $ORG/$REPO_TWO"
repo_two_resp="$(curl_json 201 \
  -X POST "$BASE_URL/api/v3/orgs/$ORG/repos" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$REPO_TWO\",\"private\":true}")"
assert_eq "$(json_get full_name <<<"$repo_two_resp")" "$ORG/$REPO_TWO"

CURRENT_STEP="grant-team-access"
for repo_name in "$REPO_ONE" "$REPO_TWO"; do
  note "Granting team access to $ORG/$repo_name"
  code="$(http_code -X PUT "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/repos/$ORG/$repo_name" \
    -H "Authorization: token $ADMIN_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"permission\":\"push\"}")"
  assert_eq "$code" "204"
done

CURRENT_STEP="list-team-repos"
repos_list="$(curl_json 200 \
  -H "Authorization: token $ADMIN_TOKEN" \
  "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/repos")"
for repo_name in "$REPO_ONE" "$REPO_TWO"; do
  if ! jq -e --arg name "$repo_name" '.[] | select(.name == $name)' <<<"$repos_list" >/dev/null; then
    echo "expected repo $repo_name in team list" >&2
    exit 1
  fi
done

work_dir="$(mktemp -d)"

CURRENT_STEP="git-access-members"

git_clone_and_push() {
  local token="$1"
  local repo_full="$2"
  local dest="$3"
  local label="$4"

  GIT_TERMINAL_PROMPT=0 git -c http.extraHeader="Authorization: token $token" \
    clone -q "$BASE_URL/$repo_full.git" "$dest" 2>/dev/null || {
      echo "ERROR: git clone failed for $repo_full ($label)" >&2
      exit 1
    }

  if git -C "$dest" checkout -q -b main 2>/dev/null; then
    :
  else
    git -C "$dest" checkout -q -B main
  fi

  echo "# $label" > "$dest/README.md"
  git -C "$dest" add README.md
  git -C "$dest" -c user.email="$label@e2e.local" -c user.name="$label" commit -q -m "e2e: seed"

  GIT_TERMINAL_PROMPT=0 git -C "$dest" -c http.extraHeader="Authorization: token $token" \
    push -q origin main 2>/dev/null || {
      echo "ERROR: git push failed for $repo_full ($label)" >&2
      exit 1
    }

  ok "$label git clone/push succeeded for $repo_full"
}

note "Member access: $member1_login -> $ORG/$REPO_ONE"
git_clone_and_push "$MEMBER1_TOKEN" "$ORG/$REPO_ONE" "$work_dir/member1-repo1" "member1"

note "Member access: $member2_login -> $ORG/$REPO_TWO"
git_clone_and_push "$MEMBER2_TOKEN" "$ORG/$REPO_TWO" "$work_dir/member2-repo2" "member2"

CURRENT_STEP="non-member-deny"
note "Validating non-member Git HTTP 404"
code="$(http_code -H "Authorization: token $NON_MEMBER_TOKEN" \
  "$BASE_URL/$ORG/$REPO_ONE.git/info/refs?service=git-upload-pack")"
assert_eq "$code" "404"

if GIT_TERMINAL_PROMPT=0 git -c http.extraHeader="Authorization: token $NON_MEMBER_TOKEN" \
  clone -q "$BASE_URL/$ORG/$REPO_ONE.git" "$work_dir/non-member-fail" 2>/dev/null; then
  echo "ERROR: non-member clone unexpectedly succeeded" >&2
  exit 1
fi
ok "Non-member denied with 404"

CURRENT_STEP="revoke-access"
note "Revoking team access to $ORG/$REPO_TWO"
code="$(http_code -X DELETE "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/repos/$ORG/$REPO_TWO" \
  -H "Authorization: token $ADMIN_TOKEN")"
assert_eq "$code" "204"

CURRENT_STEP="verify-revocation"
code="$(http_code -H "Authorization: token $MEMBER1_TOKEN" \
  "$BASE_URL/$ORG/$REPO_TWO.git/info/refs?service=git-upload-pack")"
assert_eq "$code" "404"

if GIT_TERMINAL_PROMPT=0 git -c http.extraHeader="Authorization: token $MEMBER1_TOKEN" \
  clone -q "$BASE_URL/$ORG/$REPO_TWO.git" "$work_dir/revoked-fail" 2>/dev/null; then
  echo "ERROR: expected clone denied after revocation" >&2
  exit 1
fi
ok "Revocation denies access"

CURRENT_STEP="cleanup"
note "Deleting team"
code="$(http_code -X DELETE "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug" \
  -H "Authorization: token $ADMIN_TOKEN")"
assert_eq "$code" "204"

note "Deleting repos"
code="$(http_code -X DELETE "$BASE_URL/api/v3/repos/$ORG/$REPO_ONE" \
  -H "Authorization: token $ADMIN_TOKEN")"
assert_eq "$code" "204"
code="$(http_code -X DELETE "$BASE_URL/api/v3/repos/$ORG/$REPO_TWO" \
  -H "Authorization: token $ADMIN_TOKEN")"
assert_eq "$code" "204"

record_result "team-repo-sharing-scenario" "PASS" "complete flow verified"
RESULT_RECORDED="true"
ok "team repo sharing scenario completed"
