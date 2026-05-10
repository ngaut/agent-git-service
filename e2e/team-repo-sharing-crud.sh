#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=./lib.sh
source "$ROOT/e2e/lib.sh"

require_cmd curl
require_cmd jq

BASE_URL="$(strip_trailing_slash "${E2E_BASE_URL:-http://github.localhost}")"
ADMIN_TOKEN="${ADMIN_TOKEN:-${GH_TOKEN:-mytoken}}"
RANDOM_SUFFIX="$(date +%s)-$RANDOM"
ORG="${TEAM_REPO_ORG:-e2e-team-org-$RANDOM_SUFFIX}"
TEAM_NAME="team-$RANDOM_SUFFIX"
TEAM_DESC="team repo sharing e2e"
REPO_NAME="team-repo-$RANDOM_SUFFIX"

note "BASE_URL=$BASE_URL"

if ! curl -sf "$BASE_URL/api/v3/" >/dev/null 2>&1; then
  note "Skipping team repo sharing e2e: server not reachable at $BASE_URL"
  exit 0
fi

if [[ -z "$ADMIN_TOKEN" ]]; then
  echo "ADMIN_TOKEN is required" >&2
  exit 1
fi

note "Ensuring org exists: $ORG"
ensure_org_exists "$BASE_URL" "$ADMIN_TOKEN" "$ORG"

me="$(curl_json 200 -H "Authorization: token $ADMIN_TOKEN" "$BASE_URL/api/v3/user")"
login="$(json_get login <<<"$me")"

note "Creating team: $TEAM_NAME"
team_resp="$(curl_json 201 \
  -X POST "$BASE_URL/api/v3/orgs/$ORG/teams" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$TEAM_NAME\",\"description\":\"$TEAM_DESC\",\"privacy\":\"closed\"}")"
team_slug="$(json_get slug <<<"$team_resp")"
assert_eq "$team_slug" "$TEAM_NAME"

note "Adding team member role=member"
membership_resp="$(curl_json 200 \
  -X PUT "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$login" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"role\":\"member\"}")"
assert_eq "$(json_get role <<<"$membership_resp")" "member"

note "Verifying membership"
membership_get="$(curl_json 200 \
  -H "Authorization: token $ADMIN_TOKEN" \
  "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$login")"
assert_eq "$(json_get role <<<"$membership_get")" "member"

note "Updating membership role=maintainer"
membership_update="$(curl_json 200 \
  -X PUT "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$login" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"role\":\"maintainer\"}")"
assert_eq "$(json_get role <<<"$membership_update")" "maintainer"

note "Verifying membership update"
membership_get2="$(curl_json 200 \
  -H "Authorization: token $ADMIN_TOKEN" \
  "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$login")"
assert_eq "$(json_get role <<<"$membership_get2")" "maintainer"

note "Creating repo: $ORG/$REPO_NAME"
repo_resp="$(curl_json 201 \
  -X POST "$BASE_URL/api/v3/orgs/$ORG/repos" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$REPO_NAME\",\"private\":true}")"
assert_eq "$(json_get full_name <<<"$repo_resp")" "$ORG/$REPO_NAME"

note "Granting team access to repo"
code="$(http_code -X PUT "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/repos/$ORG/$REPO_NAME" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"permission\":\"push\"}")"
assert_eq "$code" "204"

note "Listing team repos"
repos="$(curl_json 200 \
  -H "Authorization: token $ADMIN_TOKEN" \
  "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/repos")"
if ! jq -e --arg name "$REPO_NAME" '.[] | select(.name == $name)' <<<"$repos" >/dev/null; then
  echo "expected repo $REPO_NAME in team list" >&2
  exit 1
fi

note "Revoking team access"
code="$(http_code -X DELETE "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/repos/$ORG/$REPO_NAME" \
  -H "Authorization: token $ADMIN_TOKEN")"
assert_eq "$code" "204"

note "Verifying repo removal"
repos_after="$(curl_json 200 \
  -H "Authorization: token $ADMIN_TOKEN" \
  "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/repos")"
if jq -e --arg name "$REPO_NAME" '.[] | select(.name == $name)' <<<"$repos_after" >/dev/null; then
  echo "expected repo $REPO_NAME to be removed from team list" >&2
  exit 1
fi

note "Deleting team"
code="$(http_code -X DELETE "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug" \
  -H "Authorization: token $ADMIN_TOKEN")"
assert_eq "$code" "204"

note "Deleting repo"
code="$(http_code -X DELETE "$BASE_URL/api/v3/repos/$ORG/$REPO_NAME" \
  -H "Authorization: token $ADMIN_TOKEN")"
assert_eq "$code" "204"

ok "team repo sharing CRUD checks completed"
