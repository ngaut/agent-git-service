#!/usr/bin/env bash
set -euo pipefail

require_cmd() {
  local cmd="$1"
  command -v "$cmd" >/dev/null 2>&1 || {
    echo "missing dependency: $cmd" >&2
    exit 1
  }
}

strip_trailing_slash() {
  local s="$1"
  s="${s%/}"
  echo "$s"
}

http_code() {
  # Usage: http_code <curl args...> <url>
  curl -ksS -o /dev/null -w "%{http_code}" "$@"
}

curl_json() {
  # Usage: curl_json <expected_code> <curl args...>
  local expect="$1"
  shift
  local tmp
  tmp="$(mktemp)"
  local code
  code="$(curl -ksS -o "$tmp" -w "%{http_code}" "$@")"
  if [[ "$code" != "$expect" ]]; then
    echo "unexpected status: got=$code expect=$expect url=${*: -1}" >&2
    echo "response body:" >&2
    cat "$tmp" >&2
    rm -f "$tmp"
    exit 1
  fi
  cat "$tmp"
  rm -f "$tmp"
}

json_get() {
  # Usage: json_get <dot.path>
  local path="$1"
  local v
  v="$(jq -r ".$path" 2>/dev/null)" || {
    echo "json_get failed: .$path" >&2
    exit 1
  }
  if [[ "$v" == "null" ]]; then
    echo "json_get missing: .$path" >&2
    exit 1
  fi
  echo "$v"
}

assert_re() {
  local value="$1"
  local pattern="$2"
  if ! [[ "$value" =~ $pattern ]]; then
    echo "assert_re failed: value=$value pattern=$pattern" >&2
    exit 1
  fi
}

assert_eq() {
  local got="$1"
  local expect="$2"
  if [[ "$got" != "$expect" ]]; then
    echo "assert_eq failed: got=$got expect=$expect" >&2
    exit 1
  fi
}

note() { echo "==> $*"; }
ok() { echo "OK: $*"; }

ensure_org_exists() {
  # Usage: ensure_org_exists <base_url> <token> <org_login> [default_repository_permission]
  local base_url="$1"
  local token="$2"
  local org_login="$3"
  local default_permission="${4:-}"
  local code
  local payload

  code="$(http_code -H "Authorization: token $token" "$base_url/api/v3/orgs/$org_login")"
  case "$code" in
    200)
      return 0
      ;;
    404)
      payload="{\"login\":\"$org_login\""
      if [[ -n "$default_permission" ]]; then
        payload+=",\"default_repository_permission\":\"$default_permission\""
      fi
      payload+="}"
      curl_json 201 \
        -X POST "$base_url/api/v3/user/orgs" \
        -H "Authorization: token $token" \
        -H "Content-Type: application/json" \
        -d "$payload" >/dev/null
      return 0
      ;;
    *)
      echo "ensure_org_exists unexpected status: org=$org_login code=$code" >&2
      exit 1
      ;;
  esac
}

wait_for_http_ready() {
  local url="$1"
  local wait_msg="${2:-Waiting for service to start...}"
  local ok_msg="${3:-service is running}"
  local fail_msg="${4:-service failed to start}"
  local max_attempts="${5:-30}"
  local sleep_secs="${6:-1}"

  note "$wait_msg"
  for _ in $(seq 1 "$max_attempts"); do
    if curl -sf "$url" > /dev/null 2>&1; then
      ok "$ok_msg"
      return 0
    fi
    sleep "$sleep_secs"
  done
  echo "$fail_msg" >&2
  return 1
}
