#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=./lib.sh
source "$ROOT/e2e/lib.sh"

require_cmd curl
require_cmd jq

BASE_URL="$(strip_trailing_slash "${E2E_BASE_URL:-http://github.localhost}")"
ADMIN_TOKEN="${ADMIN_TOKEN:-${GH_TOKEN:-mytoken}}"
MEMBER_TOKEN="${TEAM_MEMBER_TOKEN:-${MEMBER_TOKEN:-}}"
OUTSIDER_TOKEN="${TEAM_OUTSIDER_TOKEN:-${OUTSIDER_TOKEN:-}}"

RANDOM_SUFFIX="$(date +%s)-$RANDOM"
ORG="e2e-governance-org-$RANDOM_SUFFIX"
TEAM_NAME="governance-$RANDOM_SUFFIX"
TRANSFER_REPO="transfer-$RANDOM_SUFFIX"
TEAM_REPO="maintain-$RANDOM_SUFFIX"
OUTSIDE_REPO="outside-$RANDOM_SUFFIX"
MISSING_ORG="missing-org-$RANDOM_SUFFIX"

team_slug=""
cleanup_repos=()

cleanup() {
  set +e
  if [[ "${cleanup_repos+x}" == "x" ]]; then
    for repo_name in "${cleanup_repos[@]}"; do
      http_code -X DELETE "$BASE_URL/api/v3/repos/$ORG/$repo_name" \
        -H "Authorization: token $ADMIN_TOKEN" >/dev/null 2>&1
    done
  fi
  if [[ -n "$team_slug" ]]; then
    http_code -X DELETE "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug" \
      -H "Authorization: token $ADMIN_TOKEN" >/dev/null 2>&1
  fi
}
trap cleanup EXIT

note "BASE_URL=$BASE_URL"

if ! curl -sf "$BASE_URL/api/v3/" >/dev/null 2>&1; then
  note "Skipping org collaboration governance e2e: server not reachable at $BASE_URL"
  exit 0
fi

if [[ -z "$ADMIN_TOKEN" || -z "$MEMBER_TOKEN" || -z "$OUTSIDER_TOKEN" ]]; then
  note "Skipping org collaboration governance e2e: ADMIN_TOKEN, TEAM_MEMBER_TOKEN/MEMBER_TOKEN, and TEAM_OUTSIDER_TOKEN/OUTSIDER_TOKEN are required"
  exit 0
fi

admin_json="$(curl_json 200 -H "Authorization: token $ADMIN_TOKEN" "$BASE_URL/api/v3/user")"
admin_login="$(json_get login <<<"$admin_json")"
member_json="$(curl_json 200 -H "Authorization: token $MEMBER_TOKEN" "$BASE_URL/api/v3/user")"
member_login="$(json_get login <<<"$member_json")"
outsider_json="$(curl_json 200 -H "Authorization: token $OUTSIDER_TOKEN" "$BASE_URL/api/v3/user")"
outsider_login="$(json_get login <<<"$outsider_json")"

if [[ "$admin_login" == "$member_login" || "$admin_login" == "$outsider_login" || "$member_login" == "$outsider_login" ]]; then
  echo "ADMIN_TOKEN, MEMBER_TOKEN, and OUTSIDER_TOKEN must resolve to distinct users" >&2
  exit 1
fi

note "org-owned repo creation before explicit org creation must fail"
code="$(http_code -X POST "$BASE_URL/api/v3/orgs/$ORG/repos" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$TEAM_REPO\",\"private\":true}")"
assert_eq "$code" "404"
ok "org repo creation is gated on explicit org creation"

note "creating organization with default triage alias"
org_resp="$(curl_json 201 \
  -X POST "$BASE_URL/api/ext/v1/user/orgs" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"login\":\"$ORG\",\"default_repository_permission\":\"triage\"}")"
assert_eq "$(json_get login <<<"$org_resp")" "$ORG"
assert_eq "$(json_get default_repository_permission <<<"$org_resp")" "read"

orgs_resp="$(curl_json 200 -H "Authorization: token $ADMIN_TOKEN" "$BASE_URL/api/v3/user/orgs")"
jq -e --arg login "$ORG" '.[] | select(.login == $login and .default_repository_permission == "read")' <<<"$orgs_resp" >/dev/null
org_get="$(curl_json 200 -H "Authorization: token $ADMIN_TOKEN" "$BASE_URL/api/v3/orgs/$ORG")"
assert_eq "$(json_get default_repository_permission <<<"$org_get")" "read"
ok "explicit organization creation is visible via user/org APIs"

note "transfer to a missing organization must fail"
personal_resp="$(curl_json 201 \
  -X POST "$BASE_URL/api/v3/user/repos" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$TRANSFER_REPO\",\"private\":true,\"auto_init\":true}")"
assert_eq "$(json_get full_name <<<"$personal_resp")" "$admin_login/$TRANSFER_REPO"

code="$(http_code -X POST "$BASE_URL/api/v3/repos/$admin_login/$TRANSFER_REPO/transfer" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"new_owner\":\"$MISSING_ORG\"}")"
assert_eq "$code" "404"

note "transfer to an existing organization must succeed"
transfer_resp="$(curl -ksS -w "\n%{http_code}" \
  -X POST "$BASE_URL/api/v3/repos/$admin_login/$TRANSFER_REPO/transfer" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"new_owner\":\"$ORG\"}")"
transfer_body="$(printf '%s\n' "$transfer_resp" | sed '$d')"
transfer_code="$(echo "$transfer_resp" | tail -n 1)"
assert_eq "$transfer_code" "202"
assert_eq "$(json_get full_name <<<"$transfer_body")" "$ORG/$TRANSFER_REPO"
assert_eq "$(json_get permissions.admin <<<"$transfer_body")" "true"
cleanup_repos+=("$TRANSFER_REPO")
ok "explicit transfer gating is correct"

note "creating team and org-owned repos"
team_resp="$(curl_json 201 \
  -X POST "$BASE_URL/api/v3/orgs/$ORG/teams" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$TEAM_NAME\",\"description\":\"org governance e2e\",\"privacy\":\"closed\"}")"
team_slug="$(json_get slug <<<"$team_resp")"
team_id="$(json_get id <<<"$team_resp")"

team_repo_resp="$(curl_json 201 \
  -X POST "$BASE_URL/api/v3/orgs/$ORG/repos" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$TEAM_REPO\",\"private\":true,\"auto_init\":true}")"
assert_eq "$(json_get full_name <<<"$team_repo_resp")" "$ORG/$TEAM_REPO"
cleanup_repos+=("$TEAM_REPO")

outside_repo_resp="$(curl_json 201 \
  -X POST "$BASE_URL/api/v3/orgs/$ORG/repos" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$OUTSIDE_REPO\",\"private\":true,\"auto_init\":true}")"
assert_eq "$(json_get full_name <<<"$outside_repo_resp")" "$ORG/$OUTSIDE_REPO"
cleanup_repos+=("$OUTSIDE_REPO")

code="$(http_code -X PUT "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/repos/$ORG/$TEAM_REPO" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"permission\":\"maintain\"}")"
assert_eq "$code" "204"

team_repos="$(curl_json 200 -H "Authorization: token $ADMIN_TOKEN" "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/repos")"
jq -e --arg repo "$TEAM_REPO" '.[] | select(.name == $repo and .role_name == "write" and .permissions.maintain == false and .permissions.push == true and .permissions.triage == false and .permissions.pull == true and .permissions.admin == false)' <<<"$team_repos" >/dev/null
ok "compatibility team grant is canonicalized to write"

note "inviting member into organization with team assignment"
org_inv_resp="$(curl_json 201 \
  -X POST "$BASE_URL/api/v3/orgs/$ORG/invitations" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"invitee_login\":\"$member_login\",\"role\":\"direct_member\",\"team_ids\":[$team_id]}")"
org_inv_id="$(json_get id <<<"$org_inv_resp")"
assert_eq "$(json_get role <<<"$org_inv_resp")" "direct_member"

member_org_invs="$(curl_json 200 -H "Authorization: token $MEMBER_TOKEN" "$BASE_URL/api/v3/user/organization_invitations")"
jq -e --arg org "$ORG" --argjson id "$org_inv_id" '.[] | select(.organization.login == $org and .id == $id)' <<<"$member_org_invs" >/dev/null

code="$(http_code -X PATCH "$BASE_URL/api/v3/user/organization_invitations/$org_inv_id" \
  -H "Authorization: token $MEMBER_TOKEN")"
assert_eq "$code" "204"

member_membership="$(curl_json 200 -H "Authorization: token $ADMIN_TOKEN" "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$member_login")"
assert_eq "$(json_get role <<<"$member_membership")" "member"

member_repos="$(curl_json 200 -H "Authorization: token $MEMBER_TOKEN" "$BASE_URL/api/v3/user/repos")"
jq -e --arg full "$ORG/$TRANSFER_REPO" '.[] | select(.full_name == $full and .permissions.pull == true and .permissions.triage == false and .permissions.push == false and .permissions.maintain == false and .permissions.admin == false)' <<<"$member_repos" >/dev/null
jq -e --arg full "$ORG/$TEAM_REPO" '.[] | select(.full_name == $full and .permissions.pull == true and .permissions.triage == false and .permissions.push == true and .permissions.maintain == false and .permissions.admin == false)' <<<"$member_repos" >/dev/null
ok "org base triage and team maintain aliases resolve to read/write permissions"

note "creating an outside collaborator with maintain alias"
repo_inv_resp="$(curl_json 201 \
  -X PUT "$BASE_URL/api/v3/repos/$ORG/$OUTSIDE_REPO/collaborators/$outsider_login" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"permission\":\"maintain\"}")"
repo_inv_id="$(json_get id <<<"$repo_inv_resp")"
assert_eq "$(json_get permissions <<<"$repo_inv_resp")" "write"

outsider_repo_invs="$(curl_json 200 -H "Authorization: token $OUTSIDER_TOKEN" "$BASE_URL/api/v3/user/repository_invitations")"
jq -e --arg full "$ORG/$OUTSIDE_REPO" --argjson id "$repo_inv_id" '.[] | select(.repository.full_name == $full and .id == $id and .permissions == "write")' <<<"$outsider_repo_invs" >/dev/null

code="$(http_code -X PATCH "$BASE_URL/api/v3/user/repository_invitations/$repo_inv_id" \
  -H "Authorization: token $OUTSIDER_TOKEN")"
assert_eq "$code" "204"

outside_rows="$(curl_json 200 -H "Authorization: token $ADMIN_TOKEN" "$BASE_URL/api/v3/orgs/$ORG/outside_collaborators")"
jq -e --arg login "$outsider_login" '.[] | select(.login == $login and .outside_collaborator == true and .organization_member == false)' <<<"$outside_rows" >/dev/null

collabs_before="$(curl_json 200 -H "Authorization: token $ADMIN_TOKEN" "$BASE_URL/api/v3/repos/$ORG/$OUTSIDE_REPO/collaborators")"
jq -e --arg login "$outsider_login" '.[] | select(.login == $login and .organization_member == false and .outside_collaborator == true and .permissions.maintain == false and .permissions.push == true and .permissions.triage == false and .permissions.pull == true and .permissions.admin == false)' <<<"$collabs_before" >/dev/null
ok "outside collaborator is separated from org membership"

note "promoting outside collaborator into the organization"
outsider_org_inv="$(curl_json 201 \
  -X POST "$BASE_URL/api/v3/orgs/$ORG/invitations" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"invitee_login\":\"$outsider_login\",\"role\":\"direct_member\"}")"
outsider_org_inv_id="$(json_get id <<<"$outsider_org_inv")"

code="$(http_code -X PATCH "$BASE_URL/api/v3/user/organization_invitations/$outsider_org_inv_id" \
  -H "Authorization: token $OUTSIDER_TOKEN")"
assert_eq "$code" "204"

outside_rows_after="$(curl_json 200 -H "Authorization: token $ADMIN_TOKEN" "$BASE_URL/api/v3/orgs/$ORG/outside_collaborators")"
if jq -e --arg login "$outsider_login" '.[] | select(.login == $login)' <<<"$outside_rows_after" >/dev/null; then
  echo "expected outsider to be removed from outside collaborator list after joining org" >&2
  exit 1
fi

collabs_after="$(curl_json 200 -H "Authorization: token $ADMIN_TOKEN" "$BASE_URL/api/v3/repos/$ORG/$OUTSIDE_REPO/collaborators")"
jq -e --arg login "$outsider_login" '.[] | select(.login == $login and .organization_member == true and .outside_collaborator == false and .permissions.push == true and .permissions.maintain == false and .permissions.triage == false)' <<<"$collabs_after" >/dev/null
ok "outside collaborator transitions cleanly into an organization member"

ok "org collaboration governance checks completed"
