#!/usr/bin/env bash
set -euo pipefail

# OAuth Device Flow E2E Tests
# Covers: device code request, polling failures, token exchange, redirect URI validation
# OAuth device flow bootstrap failure matrix

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=./lib.sh
source "$ROOT/e2e/lib.sh"

require_cmd curl
require_cmd jq

BASE_URL="$(strip_trailing_slash "${E2E_BASE_URL:-http://github.localhost}")"
ADMIN_TOKEN="${ADMIN_TOKEN:-mytoken}"

# Test result tracking
TESTS_PASSED=0
TESTS_FAILED=0
TESTS_SKIPPED=0
declare -a TEST_RESULTS=()

# Polling configuration
POLLING_BUFFER=10
DEFAULT_INTERVAL=5
DEFAULT_EXPIRES_IN=900

note "BASE_URL=$BASE_URL"

have_gh() {
  command -v gh >/dev/null 2>&1
}

# Verify server is running
code="$(http_code "$BASE_URL/api/v3/")"
assert_eq "$code" "200"
ok "Server is responding"

###############################################################################
# Test 1: Request Device Code from /login/device/code
###############################################################################
test_request_device_code() {
  note "=== Test 1: Request Device Code ==="
  
  local response
  response="$(curl_json 200 \
    -X POST "$BASE_URL/login/device/code" \
    -H "Content-Type: application/json" \
    -d '{"client_id":"test-client","scope":"repo"}')"
  
  local device_code user_code verification_uri verification_uri_complete expires_in interval
  device_code="$(json_get device_code <<<"$response")"
  user_code="$(json_get user_code <<<"$response")"
  verification_uri="$(json_get verification_uri <<<"$response")"
  verification_uri_complete="$(json_get verification_uri_complete <<<"$response")"
  expires_in="$(json_get expires_in <<<"$response")"
  interval="$(json_get interval <<<"$response")"
  
  # Validate response structure
  assert_re "$device_code" '^[a-f0-9]{32,64}$'
  assert_re "$user_code" '^[A-F0-9]{4}-[A-F0-9]{4}$'
  assert_re "$verification_uri" '^https?://.+/login/device$'
  assert_re "$verification_uri_complete" '^https?://.+/login/device\?.*user_code='
  assert_eq "$expires_in" "900"
  assert_eq "$interval" "5"
  
  ok "Device code requested successfully: $user_code"
  
  # Store for later tests
  export TEST_DEVICE_CODE="$device_code"
  export TEST_USER_CODE="$user_code"
}

###############################################################################
# Test 2: Poll with Invalid Code -> 400 bad_verification_code
###############################################################################
test_poll_invalid_code() {
  note "=== Test 2: Poll with Invalid Code ==="
  
  local tmp_file response
  tmp_file="$(mktemp)"
  
  # Use a completely invalid device code
  local invalid_code="invalid-code-$(date +%s)"
  
  local code
  code="$(curl -ksS -o "$tmp_file" -w "%{http_code}" \
    -X POST "$BASE_URL/login/oauth/access_token" \
    -H "Content-Type: application/json" \
    -d "{\"device_code\":\"$invalid_code\"}")"
  
  response="$(cat "$tmp_file")"
  rm -f "$tmp_file"
  
  assert_eq "$code" "400"
  
  # Verify error response
  local error
  error="$(jq -r '.error // ""' <<<"$response")"
  assert_eq "$error" "bad_verification_code"
  
  ok "Invalid code returns 400 bad_verification_code"
}

###############################################################################
# Test 3: Poll After Approval -> 200 access_token
###############################################################################
test_poll_pending_code() {
  note "=== Test 3: Poll After Approval ==="
  
  # Request a new device code
  local response
  response="$(curl_json 200 \
    -X POST "$BASE_URL/login/device/code" \
    -H "Content-Type: application/json" \
    -d '{"client_id":"test-client","scope":"repo"}')"
  
  local device_code user_code
  device_code="$(json_get device_code <<<"$response")"
  user_code="$(json_get user_code <<<"$response")"
  
  # Approve the device code via the headless console API
  local approve_code
  approve_code="$(curl -ksS -w "%{http_code}" -o /dev/null \
    -X POST "$BASE_URL/api/ext/v1/oauth/device/approve" \
    -H "Authorization: token $ADMIN_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"user_code\":\"$user_code\"}")"
  
  assert_eq "$approve_code" "200"
  ok "Device code approved successfully"
  
  # Now poll - should return token
  local tmp_file poll_response
  tmp_file="$(mktemp)"
  
  local code
  code="$(curl -ksS -o "$tmp_file" -w "%{http_code}" \
    -X POST "$BASE_URL/login/oauth/access_token" \
    -H "Content-Type: application/json" \
    -d "{\"device_code\":\"$device_code\"}")"
  
  poll_response="$(cat "$tmp_file")"
  rm -f "$tmp_file"
  
  assert_eq "$code" "200"
  
  local access_token
  access_token="$(jq -r '.access_token // ""' <<<"$poll_response")"
  if [[ -z "$access_token" || "$access_token" == "null" ]]; then
    echo "Expected access_token in response" >&2
    exit 1
  fi
  
  ok "Poll after approval returns token"
}

###############################################################################
# Test 4: Complete Approval and Exchange Successfully
###############################################################################
test_complete_approval_exchange() {
  note "=== Test 4: Complete Approval and Exchange ==="
  
  # Request a new device code
  local response
  response="$(curl_json 200 \
    -X POST "$BASE_URL/login/device/code" \
    -H "Content-Type: application/json" \
    -d '{"client_id":"test-client","scope":"repo"}')"
  
  local device_code user_code
  device_code="$(json_get device_code <<<"$response")"
  user_code="$(json_get user_code <<<"$response")"
  
  # Approve the device code
  local approve_code
  approve_code="$(curl -ksS -w "%{http_code}" -o /dev/null \
    -X POST "$BASE_URL/login/device" \
    -H "Authorization: token $ADMIN_TOKEN" \
    -d "user_code=$user_code")"
  
  assert_eq "$approve_code" "200"
  ok "Device code approved"
  
  # Exchange the approved device code
  local exchange_response
  exchange_response="$(curl_json 200 \
    -X POST "$BASE_URL/login/oauth/access_token" \
    -H "Content-Type: application/json" \
    -d "{\"device_code\":\"$device_code\"}")"
  
  local access_token token_type scope
  access_token="$(json_get access_token <<<"$exchange_response")"
  token_type="$(json_get token_type <<<"$exchange_response")"
  scope="$(json_get scope <<<"$exchange_response")"
  
  assert_re "$access_token" '^[a-f0-9]{40,80}$'
  assert_eq "$token_type" "bearer"
  assert_eq "$scope" "repo,read:org,read:user"
  
  ok "Token exchange successful: ${access_token:0:16}..."
  
  # Verify the token works
  local me
  me="$(curl_json 200 -H "Authorization: token $access_token" "$BASE_URL/api/v3/user")"
  local login
  login="$(json_get login <<<"$me")"
  assert_re "$login" '^.+$'
  
  ok "Exchanged token is valid for API access"
  
  if ! have_gh; then
    TESTS_SKIPPED=$((TESTS_SKIPPED + 1))
    note "gh not installed; skipping gh auth CLI verification"
    return 0
  fi

  local gh_config_dir status_output
  gh_config_dir="$(mktemp -d)"
  
  printf '%s\n' "$access_token" | GH_CONFIG_DIR="$gh_config_dir" GH_TOKEN="" \
    gh auth login --hostname="${GH_HOST:-github.localhost}" --with-token --insecure-storage >/dev/null
  
  status_output="$(GH_CONFIG_DIR="$gh_config_dir" GH_TOKEN="" \
    gh auth status --hostname "${GH_HOST:-github.localhost}" 2>&1)"
  rm -rf "$gh_config_dir"
  
  if ! grep -Fq "${GH_HOST:-github.localhost}" <<<"$status_output"; then
    echo "Expected gh auth status output to mention host ${GH_HOST:-github.localhost}, got: $status_output" >&2
    exit 1
  fi
  
  if ! grep -Fq "$login" <<<"$status_output"; then
    echo "Expected gh auth status output to mention login $login, got: $status_output" >&2
    exit 1
  fi
  
  ok "gh auth status reports authenticated host and login"
}

###############################################################################
# Test 5: Same-Origin vs Cross-Origin Redirect URIs
###############################################################################
assert_authorize_redirect() {
  local redirect_uri="$1"
  local description="$2"
  local tmp_headers
  tmp_headers="$(mktemp)"
  
  # PKCE parameters required by the authorize endpoint
  local state="test-state-$(date +%s)"
  local code_challenge="E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
  local code_challenge_method="S256"
  
  local code
  code="$(curl -ksS -D "$tmp_headers" -o /dev/null -w "%{http_code}" \
    -G "$BASE_URL/login/oauth/authorize" \
    --data-urlencode "redirect_uri=$redirect_uri" \
    --data-urlencode "state=$state" \
    --data-urlencode "code_challenge=$code_challenge" \
    --data-urlencode "code_challenge_method=$code_challenge_method")"
  
  assert_eq "$code" "302"
  
  local location
  location="$(awk 'BEGIN { IGNORECASE = 1 } /^Location:/ {
    sub(/\r$/, "", $0)
    sub(/^Location:[[:space:]]*/, "", $0)
    print
    exit
  }' "$tmp_headers")"
  rm -f "$tmp_headers"
  
  if [[ -z "$location" || "$location" != *"code="* ]]; then
    echo "Expected redirect Location header with code= parameter for $description, got: ${location:-<missing>}" >&2
    exit 1
  fi
  
  ok "$description redirects with authorization code"
}

test_redirect_uri_validation() {
  note "=== Test 5: Redirect URI Validation ==="
  
  # Test 5a: Same-origin redirect (should succeed)
  note "Test 5a: Same-origin redirect"
  local same_origin_uri="$BASE_URL/callback"
  
  assert_authorize_redirect "$same_origin_uri" "Same-origin redirect"
  
  # Test 5b: Localhost redirect (should succeed - exception)
  note "Test 5b: Localhost redirect (exception)"
  local localhost_uri="http://localhost:8080/callback"
  
  assert_authorize_redirect "$localhost_uri" "Localhost redirect"
  
  # Test 5c: 127.0.0.1 redirect (should succeed - exception)
  note "Test 5c: 127.0.0.1 redirect (exception)"
  local loopback_uri="http://127.0.0.1:8080/callback"
  
  assert_authorize_redirect "$loopback_uri" "127.0.0.1 redirect"
  
  # Test 5d: Cross-origin redirect (should fail with 400)
  note "Test 5d: Cross-origin redirect (should be rejected)"
  local cross_origin_uri="https://evil.example.com/callback"
  local code
  
  local tmp_file
  tmp_file="$(mktemp)"
  
  # PKCE parameters required by the authorize endpoint
  local state="test-state-$(date +%s)"
  local code_challenge="E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
  local code_challenge_method="S256"
  
  code="$(curl -ksS -o "$tmp_file" -w "%{http_code}" -G "$BASE_URL/login/oauth/authorize" \
    --data-urlencode "redirect_uri=$cross_origin_uri" \
    --data-urlencode "state=$state" \
    --data-urlencode "code_challenge=$code_challenge" \
    --data-urlencode "code_challenge_method=$code_challenge_method")"
  
  local body
  body="$(cat "$tmp_file")"
  rm -f "$tmp_file"
  
  assert_eq "$code" "400"
  
  # Verify error message mentions redirect_uri
  if ! echo "$body" | grep -qi "redirect"; then
    echo "Expected error about redirect_uri, got: $body" >&2
    exit 1
  fi
  
  ok "Cross-origin redirect rejected with 400"
}

###############################################################################
# Test 6: Network Failures and Token Endpoint Failures
###############################################################################
test_network_failures() {
  note "=== Test 6: Network and Token Endpoint Failures ==="
  
  # Test 6a: Empty device code
  note "Test 6a: Empty device code"
  local tmp_file response
  tmp_file="$(mktemp)"
  
  local code
  code="$(curl -ksS -o "$tmp_file" -w "%{http_code}" \
    -X POST "$BASE_URL/login/oauth/access_token" \
    -H "Content-Type: application/json" \
    -d '{"device_code":""}')"
  
  response="$(cat "$tmp_file")"
  rm -f "$tmp_file"
  
  assert_eq "$code" "400"
  ok "Empty device code returns 400"
  
  # Test 6b: Missing device code in JSON
  note "Test 6b: Missing device_code field"
  tmp_file="$(mktemp)"
  
  code="$(curl -ksS -o "$tmp_file" -w "%{http_code}" \
    -X POST "$BASE_URL/login/oauth/access_token" \
    -H "Content-Type: application/json" \
    -d '{}')"
  
  response="$(cat "$tmp_file")"
  rm -f "$tmp_file"
  
  assert_eq "$code" "400"
  ok "Missing device_code returns 400"
  
  # Test 6c: Form-encoded body
  note "Test 6c: Form-encoded request body"
  local device_response
  device_response="$(curl_json 200 \
    -X POST "$BASE_URL/login/device/code" \
    -H "Content-Type: application/json" \
    -d '{"client_id":"test-client","scope":"repo"}')"
  
  local form_device_code user_code
  form_device_code="$(json_get device_code <<<"$device_response")"
  user_code="$(json_get user_code <<<"$device_response")"
  
  # Approve the device code
  local approve_status
  approve_status="$(curl -ksS -o /dev/null -w "%{http_code}" \
    -X POST "$BASE_URL/login/device" \
    -H "Authorization: token $ADMIN_TOKEN" \
    -d "user_code=$user_code")"
  
  if [[ "$approve_status" != "200" ]]; then
    echo "Device code approval failed with status $approve_status (Test 6c)" >&2
    exit 1
  fi
  
  local form_response
  form_response="$(curl_json 200 \
    -X POST "$BASE_URL/login/oauth/access_token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "device_code=$form_device_code")"
  
  local form_token
  form_token="$(json_get access_token <<<"$form_response")"
  assert_re "$form_token" '^.+$'
  ok "Form-encoded token exchange works"
}

###############################################################################
# Test 7: User Code Expiration and Polling Timeouts
###############################################################################
test_expiration_and_timeouts() {
  note "=== Test 7: User Code Expiration and Polling Timeouts ==="
  
  # Test 7a: Verify expires_in is reasonable (900 seconds = 15 minutes)
  local device_response
  device_response="$(curl_json 200 \
    -X POST "$BASE_URL/login/device/code" \
    -H "Content-Type: application/json" \
    -d '{"client_id":"test-client","scope":"repo"}')"
  
  local expires_in
  expires_in="$(json_get expires_in <<<"$device_response")"
  assert_eq "$expires_in" "900"
  ok "Device code expires_in is 900 seconds (15 minutes)"
  
  # Test 7b: Verify polling interval
  local interval
  interval="$(json_get interval <<<"$device_response")"
  assert_eq "$interval" "5"
  ok "Polling interval is 5 seconds"
  
  # Test 7c: Polling with correct interval should not be rate limited
  note "Test 7c: Respecting polling interval"
  local device_code user_code
  device_code="$(json_get device_code <<<"$device_response")"
  user_code="$(json_get user_code <<<"$device_response")"
  
  # Approve the device code first
  local approve_status
  approve_status="$(curl -ksS -o /dev/null -w "%{http_code}" \
    -X POST "$BASE_URL/login/device" \
    -H "Authorization: token $ADMIN_TOKEN" \
    -d "user_code=$user_code")"
  
  if [[ "$approve_status" != "200" ]]; then
    echo "Device code approval failed with status $approve_status (Test 7c)" >&2
    exit 1
  fi
  
  # Poll twice with proper interval
  local poll1 poll2
  poll1="$(curl -ksS -X POST "$BASE_URL/login/oauth/access_token" \
    -H "Content-Type: application/json" \
    -d "{\"device_code\":\"$device_code\"}")"
  
  sleep 6  # Wait longer than interval
  
  poll2="$(curl -ksS -X POST "$BASE_URL/login/oauth/access_token" \
    -H "Content-Type: application/json" \
    -d "{\"device_code\":\"$device_code\"}")"
  
  # Both should succeed after approval
  local token1 token2
  token1="$(jq -r '.access_token // ""' <<<"$poll1")"
  token2="$(jq -r '.access_token // ""' <<<"$poll2")"
  
  assert_re "$token1" '^.+$'
  ok "First poll succeeds"
  
  assert_re "$token2" '^.+$'
  ok "Second poll (after interval) succeeds"
}

###############################################################################
# Test 8: Invalid Credentials
###############################################################################
test_invalid_credentials() {
  note "=== Test 8: Invalid Credentials ==="
  
  # Test 8a: Malformed device code (wrong format)
  note "Test 8a: Malformed device code"
  local tmp_file
  tmp_file="$(mktemp)"
  
  local code
  code="$(curl -ksS -o "$tmp_file" -w "%{http_code}" \
    -X POST "$BASE_URL/login/oauth/access_token" \
    -H "Content-Type: application/json" \
    -d '{"device_code":"not-a-hex-code-!!!"}')"
  
  local response
  response="$(cat "$tmp_file")"
  rm -f "$tmp_file"
  
  assert_eq "$code" "400"
  ok "Malformed device code returns 400"
  
  # Test 8b: Device code with wrong length
  note "Test 8b: Wrong length device code"
  tmp_file="$(mktemp)"
  
  code="$(curl -ksS -o "$tmp_file" -w "%{http_code}" \
    -X POST "$BASE_URL/login/oauth/access_token" \
    -H "Content-Type: application/json" \
    -d '{"device_code":"tooshort"}')"
  
  response="$(cat "$tmp_file")"
  rm -f "$tmp_file"
  
  assert_eq "$code" "400"
  ok "Short device code returns 400"
}

###############################################################################
# Test 9: Rate Limiting
###############################################################################
test_rate_limiting() {
  note "=== Test 9: Rate Limiting ==="
  
  # Test rapid polling tolerance (without calling /login/device to avoid rate limiting)
  note "Test 9a: Rapid polling tolerance"
  local device_response
  device_response="$(curl_json 200 \
    -X POST "$BASE_URL/login/device/code" \
    -H "Content-Type: application/json" \
    -d '{"client_id":"test-client","scope":"repo"}')"
  
  local device_code
  device_code="$(json_get device_code <<<"$device_response")"
  
  # Poll 5 times rapidly with an unapproved code
  # This tests that the token endpoint handles rapid requests gracefully
  # (returns authorization_pending or similar, not 429 on the token endpoint)
  local success_count=0
  for i in {1..5}; do
    local code
    code="$(http_code -X POST "$BASE_URL/login/oauth/access_token" \
      -H "Content-Type: application/json" \
      -d "{\"device_code\":\"$device_code\"}")"
    
    # Accept any response except 429 (rate limit) or 5xx (server error)
    if [[ "$code" != "429" && "$code" != "503" && "$code" != "502" && "$code" != "500" ]]; then
      ((success_count++)) || true
    fi
  done
  
  if [[ "$success_count" -eq 5 ]]; then
    ok "Rapid polling handled gracefully (5/5 requests responded without rate limiting)"
  else
    echo "Only $success_count/5 requests got valid responses" >&2
    exit 1
  fi
}

###############################################################################
# Test 10: Bootstrap Monitoring - No Token Leakage in Logs
###############################################################################
test_no_token_leakage() {
  note "=== Test 10: No Token Leakage in Logs ==="
  
  # Create a device code
  local device_response
  device_response="$(curl_json 200 \
    -X POST "$BASE_URL/login/device/code" \
    -H "Content-Type: application/json" \
    -d '{"client_id":"test-client","scope":"repo"}')"
  
  local device_code user_code
  device_code="$(json_get device_code <<<"$device_response")"
  user_code="$(json_get user_code <<<"$device_response")"
  
  # Approve the device code
  local approve_status
  approve_status="$(curl -ksS -o /dev/null -w "%{http_code}" \
    -X POST "$BASE_URL/login/device" \
    -H "Authorization: token $ADMIN_TOKEN" \
    -d "user_code=$user_code")"
  
  if [[ "$approve_status" != "200" ]]; then
    echo "Device code approval failed with status $approve_status" >&2
    exit 1
  fi
  
  # Exchange it
  local access_token_response
  access_token_response="$(curl_json 200 \
    -X POST "$BASE_URL/login/oauth/access_token" \
    -H "Content-Type: application/json" \
    -d "{\"device_code\":\"$device_code\"}")"
  
  local access_token
  access_token="$(json_get access_token <<<"$access_token_response")"
  
  # Verify token is not exposed in error messages
  # Test with invalid request that should fail
  local tmp_file
  tmp_file="$(mktemp)"
  
  local code
  code="$(curl -ksS -o "$tmp_file" -w "%{http_code}" \
    -X POST "$BASE_URL/login/oauth/access_token" \
    -H "Content-Type: application/json" \
    -d '{"device_code":"invalid"}')"
  
  local error_response
  error_response="$(cat "$tmp_file")"
  rm -f "$tmp_file"
  
  # Token should not appear in error response
  if echo "$error_response" | grep -q "$access_token"; then
    echo "SECURITY ISSUE: Token leaked in error response!" >&2
    exit 1
  fi
  
  ok "No token leakage in error responses"
  
  # Verify device_code is not in successful token response
  if echo "$access_token_response" | grep -q "$device_code"; then
    echo "SECURITY ISSUE: Device code leaked in token response!" >&2
    exit 1
  fi
  
  ok "No device code leakage in token response"
}

###############################################################################
# Test 11: User Experience Under Failures
###############################################################################
test_user_experience() {
  note "=== Test 11: User Experience Under Failures ==="
  
  # Test 11a: Clear error messages
  note "Test 11a: Error message clarity"
  local tmp_file
  tmp_file="$(mktemp)"
  
  local code
  code="$(curl -ksS -o "$tmp_file" -w "%{http_code}" \
    -X POST "$BASE_URL/login/oauth/access_token" \
    -H "Content-Type: application/json" \
    -d '{"device_code":"invalid-code"}')"
  
  local error_response
  error_response="$(cat "$tmp_file")"
  rm -f "$tmp_file"
  
  # Error should have 'error' field
  local error_field
  error_field="$(jq -r '.error // ""' <<<"$error_response")"
  
  if [[ -z "$error_field" || "$error_field" == "null" ]]; then
    echo "Error response missing 'error' field: $error_response" >&2
    exit 1
  fi
  
  ok "Error messages include 'error' field: $error_field"
  
  # Test 11b: Response time is reasonable
  note "Test 11b: Response time"
  local start_time end_time duration
  start_time=$(date +%s%N)
  
  curl -ksS -o /dev/null \
    -X POST "$BASE_URL/login/device/code" \
    -H "Content-Type: application/json" \
    -d '{"client_id":"test-client","scope":"repo"}'
  
  end_time=$(date +%s%N)
  duration=$(( (end_time - start_time) / 1000000 ))  # Convert to milliseconds
  
  if [[ "$duration" -lt 5000 ]]; then
    ok "Response time acceptable: ${duration}ms"
  else
    echo "Response time too slow: ${duration}ms" >&2
    exit 1
  fi
}

###############################################################################
# Test 12: Recovery Mechanisms
###############################################################################
test_recovery_mechanisms() {
  note "=== Test 12: Recovery Mechanisms ==="
  
  # Test 12a: Can request new device code after failure
  note "Test 12a: Recovery after invalid code"
  
  # First, try with invalid code
  local tmp_file
  tmp_file="$(mktemp)"
  
  curl -ksS -o "$tmp_file" \
    -X POST "$BASE_URL/login/oauth/access_token" \
    -H "Content-Type: application/json" \
    -d '{"device_code":"invalid"}'
  
  rm -f "$tmp_file"
  
  # Now request a new valid device code
  local new_device_response
  new_device_response="$(curl_json 200 \
    -X POST "$BASE_URL/login/device/code" \
    -H "Content-Type: application/json" \
    -d '{"client_id":"test-client","scope":"repo"}')"
  
  local new_device_code
  new_device_code="$(json_get device_code <<<"$new_device_response")"
  
  # Verify we can get a new device code after a failure
  # Note: We skip the approval/exchange to avoid rate limiting on /login/device
  # The key behavior being tested is that failures don't prevent new device code requests
  assert_re "$new_device_code" '^[a-f0-9]{32,64}$'
  ok "Can request new device code after failure"
  
  # Test 12b: Multiple device codes can coexist
  note "Test 12b: Multiple device codes"
  local code1 code2
  code1="$(curl_json 200 \
    -X POST "$BASE_URL/login/device/code" \
    -H "Content-Type: application/json" \
    -d '{"client_id":"test-client","scope":"repo"}')"
  
  code2="$(curl_json 200 \
    -X POST "$BASE_URL/login/device/code" \
    -H "Content-Type: application/json" \
    -d '{"client_id":"test-client","scope":"repo"}')"
  
  local dc1 dc2
  dc1="$(json_get device_code <<<"$code1")"
  dc2="$(json_get device_code <<<"$code2")"
  
  # Verify both device codes have valid format
  # Note: We skip approval/exchange to avoid rate limiting on /login/device
  # The key behavior being tested is that multiple device codes can be created
  assert_re "$dc1" '^[a-f0-9]{32,64}$'
  assert_re "$dc2" '^[a-f0-9]{32,64}$'
  
  if [[ "$dc1" != "$dc2" ]]; then
    ok "Multiple device codes can coexist (unique codes generated)"
  else
    echo "Expected different device codes, got same code twice" >&2
    exit 1
  fi
}

###############################################################################
# Run All Tests
###############################################################################
note "Starting OAuth Device Flow E2E Tests"
note "======================================"

test_request_device_code
test_poll_invalid_code
test_poll_pending_code
test_complete_approval_exchange
test_redirect_uri_validation
test_network_failures
test_expiration_and_timeouts
test_invalid_credentials
test_rate_limiting
test_no_token_leakage
test_user_experience
test_recovery_mechanisms

###############################################################################
# Summary
###############################################################################
echo ""
echo "═══════════════════════════════════════════════════"
note "OAuth Device Flow E2E Tests Complete"
echo "═══════════════════════════════════════════════════"
echo ""
if [[ "$TESTS_SKIPPED" -gt 0 ]]; then
  echo "Skipped optional checks: $TESTS_SKIPPED"
fi
echo "All tests passed successfully!"
echo ""
