#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=./lib.sh
source "$ROOT/e2e/lib.sh"

require_cmd curl
require_cmd jq

BASE_URL="$(strip_trailing_slash "${E2E_BASE_URL:-http://github.localhost}")"
TOKEN="${E2E_TOKEN:-${ADMIN_TOKEN:-mytoken}}"

note "BASE_URL=$BASE_URL"

if [[ -z "$TOKEN" ]]; then
  echo "E2E_TOKEN or ADMIN_TOKEN must be set" >&2
  exit 1
fi

code="$(http_code "$BASE_URL/api/v3/")"
assert_eq "$code" "200"
ok "Server is responding"

name="e2e-token-$(date +%s)"

note "Creating token: $name"
created="$(curl_json 201 \
  -X POST "$BASE_URL/api/v3/user/tokens" \
  -H "Authorization: token $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$name\"}")"

new_token="$(json_get token <<<"$created")"
new_id="$(json_get id <<<"$created")"
assert_re "$new_id" '^[0-9]+$'
assert_re "$new_token" '^.+$'

note "Using newly minted token"
me="$(curl_json 200 -H "Authorization: token $new_token" "$BASE_URL/api/v3/user")"
assert_re "$(json_get login <<<"$me")" '^.+$'
ok "New token works"

note "Listing tokens"
list="$(curl_json 200 -H "Authorization: token $TOKEN" "$BASE_URL/api/v3/user/tokens")"
list_id="$(jq -r --arg name "$name" '.[] | select(.name==$name) | .id' <<<"$list" | head -n1)"
assert_eq "$list_id" "$new_id"
ok "Token appears in list"

note "Deleting token"
code="$(http_code \
  -X DELETE "$BASE_URL/api/v3/user/tokens" \
  -H "Authorization: token $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"id\":$new_id}")"
assert_eq "$code" "204"
ok "Token deleted"

note "Deleted token no longer valid"
code="$(http_code -H "Authorization: token $new_token" "$BASE_URL/api/v3/user")"
assert_eq "$code" "401"
ok "Deleted token rejected"

note "Token removed from list"
list="$(curl_json 200 -H "Authorization: token $TOKEN" "$BASE_URL/api/v3/user/tokens")"
remaining="$(jq -r --arg name "$name" '[.[] | select(.name==$name)] | length' <<<"$list")"
assert_eq "$remaining" "0"
ok "Token list clean"
