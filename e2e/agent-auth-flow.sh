#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=./lib.sh
source "$ROOT/e2e/lib.sh"

require_cmd curl
require_cmd jq
require_cmd openssl

BASE_URL="$(strip_trailing_slash "${E2E_BASE_URL:-http://github.localhost}")"
MOCK_OIDC_BASE_URL="$(strip_trailing_slash "${MOCK_OIDC_BASE_URL:-http://localhost:8891}")"
MOCK_OIDC_CLIENT_ID="${MOCK_OIDC_CLIENT_ID:-test-client-id}"

HUMAN_TOKEN="${HUMAN_TOKEN:-}"
AGENT_PREFIX="${AGENT_PREFIX:-e2e-agent}"
DEFAULT_REPO_NAME="${DEFAULT_REPO_NAME:-memory}"
ORG_REPO_NAME="${ORG_REPO_NAME:-core}"

note "BASE_URL=$BASE_URL"

code="$(http_code "$BASE_URL/api/v3/")"
assert_eq "$code" "200"
ok "Server is responding"

check_mock_oidc_available() {
  if ! curl -sS "$MOCK_OIDC_BASE_URL/__admin/state" >/dev/null 2>&1; then
    echo "WARNING: Mock OIDC server not available at $MOCK_OIDC_BASE_URL" >&2
    echo "To run OIDC login flow, start the mock server:" >&2
    echo "  go run ./e2e/cmd/mock-oidc-server/main.go :8891" >&2
    echo "And configure gh-server with:" >&2
    echo "  OIDC_PROVIDER=mock-oidc OIDC_ISSUER=http://localhost:8891/ OIDC_CLIENT_ID=test-client-id OIDC_ALLOW_INSECURE_HTTP=1" >&2
    return 1
  fi
  return 0
}

set_oidc_mode() {
  local mode="$1"
  local fail_count="${2:-0}"
  local success_once="${3:-false}"

  curl -sS -X POST "$MOCK_OIDC_BASE_URL/__admin/mode?mode=$mode&fail_count=$fail_count&success_once=$success_once" >/dev/null
}

reset_oidc_mock() {
  curl -sS -X POST "$MOCK_OIDC_BASE_URL/__admin/reset" >/dev/null
}

mint_mock_id_token() {
  local subject="$1"
  local subject_uri
  local token_response

  subject_uri="$(jq -nr --arg v "$subject" '$v|@uri')"
  token_response="$(curl_json 200 \
    -X POST "$MOCK_OIDC_BASE_URL/oauth/token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "grant_type=authorization_code&code=mock-browser-code&client_id=$MOCK_OIDC_CLIENT_ID&subject=$subject_uri&redirect_uri=http://localhost/mock-callback")"
  json_get id_token <<<"$token_response"
}

register_agent() {
  note "register agent account"
  local resp
  resp="$(curl_json 201 \
    -X POST "$BASE_URL/api/ext/v1/agents" \
    -H "Content-Type: application/json" \
    -d "{\"prefix_login\":\"$AGENT_PREFIX\",\"default_repo_name\":\"$DEFAULT_REPO_NAME\"}")"

  agent_login="$(json_get login <<<"$resp")"
  agent_token="$(json_get token <<<"$resp")"
  agent_repo_full="$(json_get repo_full_name <<<"$resp")"

  assert_re "$agent_login" '^[a-z0-9][a-z0-9-]+-[a-f0-9]{6}$'
  assert_re "$agent_token" '^.+$'
  assert_eq "$agent_repo_full" "$agent_login/$DEFAULT_REPO_NAME"
  ok "agent registered: $agent_login"
}

verify_agent_repo() {
  note "verify default repo created for agent"
  local repo
  repo="$(curl_json 200 \
    -H "Authorization: token $agent_token" \
    "$BASE_URL/api/v3/repos/$agent_repo_full")"
  assert_eq "$(json_get full_name <<<"$repo")" "$agent_repo_full"
  assert_eq "$(json_get private <<<"$repo")" "true"
  ok "agent default repo ready: $agent_repo_full"
}

create_org_as_agent() {
  org_login="e2e-org-$(openssl rand -hex 3)"
  note "agent creates org $org_login"
  local org
  org="$(curl_json 201 \
    -X POST "$BASE_URL/api/ext/v1/user/orgs" \
    -H "Authorization: token $agent_token" \
    -H "Content-Type: application/json" \
    -d "{\"login\":\"$org_login\"}")"
  assert_eq "$(json_get login <<<"$org")" "$org_login"
  ok "org created: $org_login"
}

create_org_repo_as_agent() {
  note "agent creates org repo under $org_login"
  local repo
  repo="$(curl_json 201 \
    -X POST "$BASE_URL/api/v3/orgs/$org_login/repos" \
    -H "Authorization: token $agent_token" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"$ORG_REPO_NAME\",\"private\":true}")"
  org_repo_full="$(json_get full_name <<<"$repo")"
  assert_eq "$org_repo_full" "$org_login/$ORG_REPO_NAME"
  ok "org repo created: $org_repo_full"
}

login_human() {
  if [[ -n "$HUMAN_TOKEN" ]]; then
    note "use provided HUMAN_TOKEN"
    human_token="$HUMAN_TOKEN"
    local me
    me="$(curl_json 200 -H "Authorization: token $human_token" "$BASE_URL/api/v3/user")"
    human_login="$(json_get login <<<"$me")"
    ok "human token valid: $human_login"
    return 0
  fi

  if ! check_mock_oidc_available; then
    note "use seeded human token fallback"
    human_token="${ADMIN_TOKEN:-${GH_TOKEN:-mytoken}}"
    local me
    me="$(curl_json 200 -H "Authorization: token $human_token" "$BASE_URL/api/v3/user")"
    human_login="$(json_get login <<<"$me")"
    ok "human token valid: $human_login"
    return 0
  fi

  reset_oidc_mock
  set_oidc_mode "success"

  local subject
  subject="oidc|human-$(openssl rand -hex 4)"

  local id_token
  id_token="$(mint_mock_id_token "$subject")"
  assert_re "$id_token" '^.+$'

  note "login human via OIDC id_token"
  local resp
  resp="$(curl_json 200 \
    -X POST "$BASE_URL/api/ext/v1/oidc/callback" \
    -H "Content-Type: application/json" \
    -d "{\"id_token\":\"$id_token\"}")"
  human_token="$(json_get token <<<"$resp")"
  human_login="$(json_get login <<<"$resp")"
  assert_re "$human_login" '^[a-z0-9][a-z0-9_-]{0,38}$'
  ok "human login created: $human_login"
}

create_invite_and_bind() {
  note "human creates agent invite"
  local invite
  invite="$(curl_json 201 \
    -X POST "$BASE_URL/api/ext/v1/agent-invites" \
    -H "Authorization: token $human_token")"
  invite_token="$(json_get invite_token <<<"$invite")"
  assert_re "$invite_token" '^[a-f0-9]{32}$'
  ok "invite created"

  note "human token is rejected for confirm"
  local human_confirm_code
  human_confirm_code="$(http_code \
    -X POST "$BASE_URL/api/ext/v1/agent-bindings/confirm" \
    -H "Authorization: token $human_token" \
    -H "Content-Type: application/json" \
    -d "{\"invite_token\":\"$invite_token\"}")"
  assert_eq "$human_confirm_code" "403"
  ok "human confirm rejected"

  note "invalid invite token is rejected"
  local invalid_invite_code
  invalid_invite_code="$(http_code \
    -X POST "$BASE_URL/api/ext/v1/agent-bindings/confirm" \
    -H "Authorization: token $agent_token" \
    -H "Content-Type: application/json" \
    -d '{"invite_token":"missing-invite-token"}')"
  assert_eq "$invalid_invite_code" "422"
  ok "invalid invite rejected"

  note "agent confirms binding"
  local binding
  binding="$(curl_json 201 \
    -X POST "$BASE_URL/api/ext/v1/agent-bindings/confirm" \
    -H "Authorization: token $agent_token" \
    -H "Content-Type: application/json" \
    -d "{\"invite_token\":\"$invite_token\"}")"
  assert_re "$(json_get bound_at <<<"$binding")" '^.+$'
  ok "binding confirmed"

  note "consumed invite token returns conflict"
  local consumed_invite_code
  consumed_invite_code="$(http_code \
    -X POST "$BASE_URL/api/ext/v1/agent-bindings/confirm" \
    -H "Authorization: token $agent_token" \
    -H "Content-Type: application/json" \
    -d "{\"invite_token\":\"$invite_token\"}")"
  assert_eq "$consumed_invite_code" "409"
  ok "consumed invite rejected"
}

verify_bound_agents_list() {
  note "human lists bound agents"
  local list
  list="$(curl_json 200 \
    -H "Authorization: token $human_token" \
    "$BASE_URL/api/ext/v1/user/agents")"
  if ! jq -e --arg login "$agent_login" '.[] | select(.agent.login == $login)' >/dev/null <<<"$list"; then
    echo "expected bound agent $agent_login in list" >&2
    echo "$list" >&2
    exit 1
  fi
  ok "bound agent listed"
}

verify_admins_team_membership() {
  note "verify admins team includes human for org"
  local members
  members="$(curl_json 200 \
    -H "Authorization: token $agent_token" \
    "$BASE_URL/api/v3/orgs/$org_login/teams/admins/members")"
  if ! jq -e --arg login "$human_login" '.[] | select(.login == $login)' >/dev/null <<<"$members"; then
    echo "expected human $human_login in admins team for $org_login" >&2
    echo "$members" >&2
    exit 1
  fi
  ok "admins team membership ok"
}

verify_progressive_backfill() {
  note "human accesses org repo (progressive backfill)"
  local repo
  repo="$(curl_json 200 \
    -H "Authorization: token $human_token" \
    "$BASE_URL/api/v3/repos/$org_repo_full")"
  assert_eq "$(jq -r '.permissions.admin // "false"' <<<"$repo")" "true"
  ok "human has admin permission via backfill"
}

reset_agent_token() {
  note "human resets agent token"
  local resp
  resp="$(curl_json 200 \
    -X POST "$BASE_URL/api/ext/v1/agent-bindings/$agent_login/reset-token" \
    -H "Authorization: token $human_token")"
  new_agent_token="$(jq -r '.token.token // ""' <<<"$resp")"
  if [[ -z "$new_agent_token" ]]; then
    echo "expected new agent token in response" >&2
    echo "$resp" >&2
    exit 1
  fi
  if [[ "$new_agent_token" == "$agent_token" ]]; then
    echo "expected new token to differ from old token" >&2
    exit 1
  fi
  ok "agent token reset"

  note "old agent token is revoked"
  local old_code
  old_code="$(http_code -H "Authorization: token $agent_token" "$BASE_URL/api/v3/user")"
  if [[ "$old_code" == "200" ]]; then
    echo "expected old token to be revoked, got 200" >&2
    exit 1
  fi
  ok "old token rejected (status=$old_code)"

  note "new agent token works"
  local me
  me="$(curl_json 200 -H "Authorization: token $new_agent_token" "$BASE_URL/api/v3/user")"
  assert_eq "$(json_get login <<<"$me")" "$agent_login"
  ok "new token validated"
}

verify_anonymous_removed() {
  note "anonymous endpoints are removed"
  local code
  code="$(http_code -X POST "$BASE_URL/api/v3/anonymous/session")"
  assert_eq "$code" "404"
  ok "anonymous session endpoint returns 404"
}

register_agent
verify_agent_repo
create_org_as_agent
create_org_repo_as_agent
login_human
create_invite_and_bind
verify_bound_agents_list
verify_admins_team_membership
verify_progressive_backfill
reset_agent_token
verify_anonymous_removed

ok "agent auth flow completed"
