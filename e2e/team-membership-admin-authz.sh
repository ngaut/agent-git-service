#!/usr/bin/env bash
set -euo pipefail

# E2E Test: Team Membership Admin Authorization Boundaries
# This test validates that only admin users can modify team membership.
# Non-admin org members and outsiders must be denied.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=./lib.sh
source "$ROOT/e2e/lib.sh"
# shellcheck source=./helpers.sh
source "$ROOT/e2e/helpers.sh"

require_cmd curl
require_cmd jq

BASE_URL="$(strip_trailing_slash "${E2E_BASE_URL:-http://github.localhost}")"
ADMIN_TOKEN="${ADMIN_TOKEN:-${GH_TOKEN:-mytoken}}"
MEMBER_TOKEN="${TEAM_MEMBER_TOKEN:-${MEMBER_TOKEN:-}}"
OUTSIDER_TOKEN="${TEAM_OUTSIDER_TOKEN:-${OUTSIDER_TOKEN:-}}"

RANDOM_SUFFIX="$(date +%s)-$RANDOM"
ORG="${TEAM_REPO_ORG:-e2e-team-org-$RANDOM_SUFFIX}"
TEAM_NAME="team-$RANDOM_SUFFIX"
TEAM_DESC="team membership authz e2e"

TEST_RESULTS=()
TESTS_PASSED=0
TESTS_FAILED=0
TESTS_SKIPPED=0
CURRENT_STEP="init"
RESULT_RECORDED="false"

on_exit() {
  local status=$?
  if [[ "$RESULT_RECORDED" != "true" && "$status" -ne 0 ]]; then
    record_result "team-membership-admin-authz" "FAIL" "failed at: $CURRENT_STEP"
    RESULT_RECORDED="true"
  fi
  print_summary
  exit "$status"
}

trap on_exit EXIT

note "BASE_URL=$BASE_URL"

if ! curl -sf "$BASE_URL/api/v3/" >/dev/null 2>&1; then
  note "Skipping team membership authz e2e: server not reachable at $BASE_URL"
  record_result "team-membership-admin-authz" "SKIP" "server not reachable"
  RESULT_RECORDED="true"
  exit 0
fi

if [[ -z "$ADMIN_TOKEN" ]]; then
  echo "ADMIN_TOKEN is required" >&2
  exit 1
fi

if [[ -z "$MEMBER_TOKEN" || -z "$OUTSIDER_TOKEN" ]]; then
  note "Skipping team membership authz e2e: TEAM_MEMBER_TOKEN and TEAM_OUTSIDER_TOKEN required"
  record_result "team-membership-admin-authz" "SKIP" "member/outsider tokens not set"
  RESULT_RECORDED="true"
  exit 0
fi

# Resolve logins for all users
CURRENT_STEP="resolve-logins"
admin_login="$(curl_json 200 -H "Authorization: token $ADMIN_TOKEN" "$BASE_URL/api/v3/user" | json_get login)"
member_login="$(curl_json 200 -H "Authorization: token $MEMBER_TOKEN" "$BASE_URL/api/v3/user" | json_get login)"
outsider_login="$(curl_json 200 -H "Authorization: token $OUTSIDER_TOKEN" "$BASE_URL/api/v3/user" | json_get login)"

if [[ "$admin_login" == "$member_login" || "$admin_login" == "$outsider_login" || "$member_login" == "$outsider_login" ]]; then
  echo "ADMIN_TOKEN, TEAM_MEMBER_TOKEN, and TEAM_OUTSIDER_TOKEN must be distinct users" >&2
  exit 1
fi

# Ensure org exists
CURRENT_STEP="org-create"
note "Ensuring org exists: $ORG"
ensure_org_exists "$BASE_URL" "$ADMIN_TOKEN" "$ORG"

# Create team
CURRENT_STEP="create-team"
note "Creating team: $TEAM_NAME"
team_resp="$(curl_json 201 \
  -X POST "$BASE_URL/api/v3/orgs/$ORG/teams" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$TEAM_NAME\",\"description\":\"$TEAM_DESC\",\"privacy\":\"closed\"}")"
team_slug="$(json_get slug <<<"$team_resp")"
assert_eq "$team_slug" "$TEAM_NAME"

# Admin adds themselves to the team first (baseline membership)
CURRENT_STEP="admin-baseline-membership"
note "Admin adding themselves to team (baseline)"
membership_resp="$(curl_json 200 \
  -X PUT "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$admin_login" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"role\":\"maintainer\"}")"
assert_eq "$(json_get role <<<"$membership_resp")" "maintainer"

membership_get="$(curl_json 200 \
  -H "Authorization: token $ADMIN_TOKEN" \
  "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$admin_login")"
assert_eq "$(json_get role <<<"$membership_get")" "maintainer"
ok "Admin baseline membership established"

# Test 1: Non-admin org member attempts to add membership (should be rejected)
CURRENT_STEP="member-add-denied"
note "Test 1: Non-admin org member attempts to add membership"
code="$(http_code -X PUT "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$member_login" \
  -H "Authorization: token $MEMBER_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"role\":\"member\"}")"
if [[ "$code" != "403" && "$code" != "404" ]]; then
  echo "ERROR: Expected 403 or 404, got $code" >&2
  exit 1
fi
ok "Non-admin org member denied adding membership (HTTP $code)"

# Verify membership unchanged
membership_after="$(curl_json 200 \
  -H "Authorization: token $ADMIN_TOKEN" \
  "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$member_login" 2>/dev/null || echo "")"
if [[ -n "$membership_after" ]]; then
  echo "ERROR: Membership should not exist for member after denied request" >&2
  exit 1
fi
ok "Membership unchanged after denied add attempt"

# Test 2: Outsider attempts to add membership (should be rejected)
CURRENT_STEP="outsider-add-denied"
note "Test 2: Outsider attempts to add membership"
code="$(http_code -X PUT "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$outsider_login" \
  -H "Authorization: token $OUTSIDER_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"role\":\"member\"}")"
if [[ "$code" != "403" && "$code" != "404" ]]; then
  echo "ERROR: Expected 403 or 404, got $code" >&2
  exit 1
fi
ok "Outsider denied adding membership (HTTP $code)"

# Verify membership unchanged
membership_after="$(curl_json 200 \
  -H "Authorization: token $ADMIN_TOKEN" \
  "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$outsider_login" 2>/dev/null || echo "")"
if [[ -n "$membership_after" ]]; then
  echo "ERROR: Membership should not exist for outsider after denied request" >&2
  exit 1
fi
ok "Membership unchanged after outsider denied add attempt"

# Test 3: Admin successfully adds member (to test delete denial)
CURRENT_STEP="admin-adds-member"
note "Admin adds member to team (for delete test)"
membership_resp="$(curl_json 200 \
  -X PUT "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$member_login" \
  -H "Authorization: token $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"role\":\"member\"}")"
assert_eq "$(json_get role <<<"$membership_resp")" "member"
ok "Admin successfully added member"

# Test 4: Non-admin org member attempts to remove membership (should be rejected)
CURRENT_STEP="member-remove-denied"
note "Test 4: Non-admin org member attempts to remove membership"
code="$(http_code -X DELETE "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$member_login" \
  -H "Authorization: token $MEMBER_TOKEN")"
if [[ "$code" != "403" && "$code" != "404" ]]; then
  echo "ERROR: Expected 403 or 404, got $code" >&2
  exit 1
fi
ok "Non-admin org member denied removing membership (HTTP $code)"

# Verify membership still exists
membership_get="$(curl_json 200 \
  -H "Authorization: token $ADMIN_TOKEN" \
  "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$member_login")"
assert_eq "$(json_get role <<<"$membership_get")" "member"
ok "Membership unchanged after denied remove attempt"

# Test 5: Outsider attempts to remove membership (should be rejected)
CURRENT_STEP="outsider-remove-denied"
note "Test 5: Outsider attempts to remove membership"
code="$(http_code -X DELETE "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$member_login" \
  -H "Authorization: token $OUTSIDER_TOKEN")"
if [[ "$code" != "403" && "$code" != "404" ]]; then
  echo "ERROR: Expected 403 or 404, got $code" >&2
  exit 1
fi
ok "Outsider denied removing membership (HTTP $code)"

# Verify membership still exists
membership_get="$(curl_json 200 \
  -H "Authorization: token $ADMIN_TOKEN" \
  "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$member_login")"
assert_eq "$(json_get role <<<"$membership_get")" "member"
ok "Membership unchanged after outsider denied remove attempt"

# Test 6: Admin successfully removes member (cleanup)
CURRENT_STEP="admin-removes-member"
note "Admin removes member from team (cleanup)"
code="$(http_code -X DELETE "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$member_login" \
  -H "Authorization: token $ADMIN_TOKEN")"
assert_eq "$code" "204"
ok "Admin successfully removed member"

# Verify membership removed
membership_after="$(curl_json 200 \
  -H "Authorization: token $ADMIN_TOKEN" \
  "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$member_login" 2>/dev/null || echo "")"
if [[ -n "$membership_after" ]]; then
  echo "ERROR: Membership should be removed after admin delete" >&2
  exit 1
fi
ok "Membership removed after admin delete"

# Test 7: Final verification - admin membership unchanged throughout
CURRENT_STEP="final-verification"
note "Test 7: Final verification - admin membership unchanged"
membership_get="$(curl_json 200 \
  -H "Authorization: token $ADMIN_TOKEN" \
  "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug/memberships/$admin_login")"
assert_eq "$(json_get role <<<"$membership_get")" "maintainer"
ok "Admin membership unchanged (maintainer)"

# Cleanup
CURRENT_STEP="cleanup"
note "Deleting team"
code="$(http_code -X DELETE "$BASE_URL/api/v3/orgs/$ORG/teams/$team_slug" \
  -H "Authorization: token $ADMIN_TOKEN")"
assert_eq "$code" "204"

record_result "team-membership-admin-authz" "PASS" "all authorization boundaries verified"
RESULT_RECORDED="true"
ok "team membership admin authorization boundaries e2e completed"
