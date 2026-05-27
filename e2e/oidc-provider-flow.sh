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

note "BASE_URL=$BASE_URL"

code="$(http_code "$BASE_URL/api/v3/")"
assert_eq "$code" "200"
ok "Server is responding"

check_mock_oidc_available() {
  if ! curl -sS "$MOCK_OIDC_BASE_URL/.well-known/openid-configuration" >/dev/null 2>&1; then
    echo "mock OIDC server not available at $MOCK_OIDC_BASE_URL" >&2
    echo "Start it with:" >&2
    echo "  go run ./e2e/cmd/mock-oidc-server/main.go :8891" >&2
    echo "Configure gh-server with:" >&2
    echo "  OIDC_PROVIDER=casdoor OIDC_ISSUER=http://localhost:8891/ OIDC_CLIENT_ID=test-client-id OIDC_ALLOW_INSECURE_HTTP=1" >&2
    exit 1
  fi
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

mutate_token_payload() {
  local token="$1"
  local payload
  payload="$(printf '{"sub":"tampered|user","iss":"%s/","aud":"wrong-client","exp":1}' "$MOCK_OIDC_BASE_URL" | openssl base64 -A | tr '+/' '-_' | tr -d '=')"
  IFS='.' read -r header _ signature <<<"$token"
  printf '%s.%s.%s\n' "$header" "$payload" "$signature"
}

check_mock_oidc_available

subject="casdoor|user-$(openssl rand -hex 4)"
id_token="$(mint_mock_id_token "$subject")"
assert_re "$id_token" '^.+$'

note "Create user through OIDC callback"
first_login="$(curl_json 200 \
  -X POST "$BASE_URL/api/v3/oidc/callback" \
  -H "Content-Type: application/json" \
  -d "{\"id_token\":\"$id_token\"}")"
first_token="$(json_get token <<<"$first_login")"
first_user_id="$(json_get user_id <<<"$first_login")"
first_login_name="$(json_get login <<<"$first_login")"
assert_re "$first_token" '^.+$'
assert_re "$first_login_name" '^[a-z0-9][a-z0-9_-]{0,38}$'
ok "first OIDC callback created user $first_login_name"

note "OIDC lookup returns linked user"
lookup="$(curl_json 200 \
  -X POST "$BASE_URL/api/v3/oidc/lookup" \
  -H "Content-Type: application/json" \
  -d "{\"id_token\":\"$id_token\"}")"
assert_eq "$(json_get linked <<<"$lookup")" "true"
assert_eq "$(json_get user.id <<<"$lookup")" "$first_user_id"
assert_eq "$(json_get user.login <<<"$lookup")" "$first_login_name"
ok "OIDC lookup resolved linked user"

note "Repeated callback reuses the same identity"
repeat_login="$(curl_json 200 \
  -X POST "$BASE_URL/api/v3/oidc/callback" \
  -H "Content-Type: application/json" \
  -d "{\"id_token\":\"$id_token\"}")"
assert_eq "$(json_get user_id <<<"$repeat_login")" "$first_user_id"
assert_eq "$(json_get login <<<"$repeat_login")" "$first_login_name"
ok "repeated OIDC callback reused the same user"

note "Issued token is usable against /api/v3/user"
me="$(curl_json 200 -H "Authorization: token $first_token" "$BASE_URL/api/v3/user")"
assert_eq "$(json_get login <<<"$me")" "$first_login_name"
ok "OIDC-issued token authenticates user API"

note "Invalid token is rejected"
invalid_id_token="$(mutate_token_payload "$id_token")"
invalid_callback_code="$(http_code \
  -X POST "$BASE_URL/api/v3/oidc/callback" \
  -H "Content-Type: application/json" \
  -d "{\"id_token\":\"$invalid_id_token\"}")"
assert_eq "$invalid_callback_code" "401"
invalid_lookup_code="$(http_code \
  -X POST "$BASE_URL/api/v3/oidc/lookup" \
  -H "Content-Type: application/json" \
  -d "{\"id_token\":\"$invalid_id_token\"}")"
assert_eq "$invalid_lookup_code" "401"
ok "invalid OIDC id_token is rejected by callback and lookup"
