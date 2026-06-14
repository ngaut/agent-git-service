#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=/dev/null
source "$ROOT_DIR/e2e/lib.sh"

BASE_URL="${BASE_URL:-}"
REPO_FULL_NAME="${REPO_FULL_NAME:-}"
AUTH_TOKEN="${WIKI_TOKEN:-${TOKEN:-}}"
REF=""
MAX_PAGES=0
TREE_PATHS=("")
SEARCH_QUERIES=()
BACKLINK_SLUGS=()
SEARCH_QUERY_COUNT=0
BACKLINK_SLUG_COUNT=0

usage() {
  cat <<'EOF'
Usage:
  bash scripts/wiki_remote_integrity.sh --base-url <url> --repo <owner/repo> [options]

Checks the remote wiki surfaces that emit page links:
  - recursively crawls /wiki/tree and verifies every page URL resolves
  - verifies /wiki/search result URLs for each supplied query
  - verifies /wiki/pages/{slug}/backlinks source URLs for each supplied slug
  - when --ref is supplied, verifies tree-emitted page URLs preserve that ref

Options:
  --base-url <url>       Service base URL, for example https://github.example.com
  --repo <owner/repo>    Repository full name
  --token <token>        Optional API token. Prefer WIKI_TOKEN or TOKEN env vars.
  --path <path>          Initial tree path to crawl. May be repeated.
  --search-query <q>     Search query whose result URLs should resolve. Repeatable.
  --backlink-slug <s>    Page slug whose backlink source URLs should resolve. Repeatable.
  --ref <sha>            Optional explicit tree ref. Use a full commit SHA.
  --max-pages <n>        Stop after checking n tree page URLs. 0 means unlimited.
  -h, --help             Show this help.

Example:
  WIKI_TOKEN=... bash scripts/wiki_remote_integrity.sh \
    --base-url https://github.example.com \
    --repo octo/wiki-repo \
    --path calls \
    --search-query Airbnb \
    --backlink-slug home
EOF
}

urlencode() {
  python3 - "$1" <<'PY'
import sys
from urllib.parse import quote

print(quote(sys.argv[1], safe=""))
PY
}

append_query() {
  local url="$1"
  local key="$2"
  local value="$3"
  local sep="?"
  if [[ "$url" == *\?* ]]; then
    sep="&"
  fi
  printf '%s%s%s=%s' "$url" "$sep" "$key" "$(urlencode "$value")"
}

curl_json_body() {
  local url="$1"
  local tmp
  tmp="$(mktemp)"
  local code
  if [[ -n "$AUTH_TOKEN" ]]; then
    code="$(curl -ksS -o "$tmp" -w "%{http_code}" -H "Authorization: token $AUTH_TOKEN" "$url")"
  else
    code="$(curl -ksS -o "$tmp" -w "%{http_code}" "$url")"
  fi
  if [[ "$code" != "200" ]]; then
    echo "FAIL fetch json: status=$code url=$url" >&2
    echo "response body:" >&2
    cat "$tmp" >&2
    rm -f "$tmp"
    exit 1
  fi
  cat "$tmp"
  rm -f "$tmp"
}

check_resolves() {
  local label="$1"
  local url="$2"
  local tmp
  tmp="$(mktemp)"
  local code
  if [[ -n "$AUTH_TOKEN" ]]; then
    code="$(curl -ksS -o "$tmp" -w "%{http_code}" -H "Authorization: token $AUTH_TOKEN" "$url")"
  else
    code="$(curl -ksS -o "$tmp" -w "%{http_code}" "$url")"
  fi
  rm -f "$tmp"
  if [[ "$code" != "200" ]]; then
    echo "FAIL $label: status=$code url=$url" >&2
    return 1
  fi
  return 0
}

contains_ref_query() {
  local url="$1"
  local encoded_ref
  encoded_ref="$(urlencode "$REF")"
  [[ "$url" == *"?ref=$encoded_ref"* || "$url" == *"&ref=$encoded_ref"* ]]
}

add_unique_line() {
  local file="$1"
  local value="$2"
  if ! grep -Fxq "$value" "$file" 2>/dev/null; then
    printf '%s\n' "$value" >>"$file"
  fi
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --base-url)
      BASE_URL="${2:-}"
      shift 2
      ;;
    --repo)
      REPO_FULL_NAME="${2:-}"
      shift 2
      ;;
    --token)
      AUTH_TOKEN="${2:-}"
      shift 2
      ;;
    --path)
      if [[ "${#TREE_PATHS[@]}" -eq 1 && "${TREE_PATHS[0]}" == "" ]]; then
        TREE_PATHS=()
      fi
      TREE_PATHS+=("${2:-}")
      shift 2
      ;;
    --search-query)
      SEARCH_QUERIES+=("${2:-}")
      SEARCH_QUERY_COUNT=$((SEARCH_QUERY_COUNT + 1))
      shift 2
      ;;
    --backlink-slug)
      BACKLINK_SLUGS+=("${2:-}")
      BACKLINK_SLUG_COUNT=$((BACKLINK_SLUG_COUNT + 1))
      shift 2
      ;;
    --ref)
      REF="${2:-}"
      shift 2
      ;;
    --max-pages)
      MAX_PAGES="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

require_cmd curl
require_cmd jq
require_cmd python3

if [[ -z "$BASE_URL" || -z "$REPO_FULL_NAME" ]]; then
  echo "--base-url and --repo are required" >&2
  usage >&2
  exit 2
fi
if ! [[ "$MAX_PAGES" =~ ^[0-9]+$ ]]; then
  echo "--max-pages must be a non-negative integer" >&2
  exit 2
fi

BASE_URL="$(strip_trailing_slash "$BASE_URL")"
API_BASE="$BASE_URL/api/v3"

queue_file="$(mktemp)"
visited_file="$(mktemp)"
checked_urls_file="$(mktemp)"
cleanup() {
  rm -f "$queue_file" "$visited_file" "$checked_urls_file"
}
trap cleanup EXIT

for path in "${TREE_PATHS[@]}"; do
  add_unique_line "$queue_file" "$path"
done

failures=0
tree_pages=0
tree_dirs=0
offset=1

while [[ "$offset" -le "$(wc -l <"$queue_file" | tr -d ' ')" ]]; do
  path="$(sed -n "${offset}p" "$queue_file")"
  offset=$((offset + 1))

  if grep -Fxq "$path" "$visited_file" 2>/dev/null; then
    continue
  fi
  printf '%s\n' "$path" >>"$visited_file"

  tree_url="$API_BASE/repos/$REPO_FULL_NAME/wiki/tree"
  tree_url="$(append_query "$tree_url" "path" "$path")"
  if [[ -n "$REF" ]]; then
    tree_url="$(append_query "$tree_url" "ref" "$REF")"
  fi

  tree_json="$(curl_json_body "$tree_url")"
  while IFS= read -r child_path; do
    [[ -z "$child_path" ]] && continue
    tree_dirs=$((tree_dirs + 1))
    add_unique_line "$queue_file" "$child_path"
  done < <(printf '%s' "$tree_json" | jq -r '.[] | select(.kind == "directory") | .path')

  while IFS=$'\t' read -r slug page_url; do
    [[ -z "$slug" || -z "$page_url" ]] && continue
    if [[ "$MAX_PAGES" -gt 0 && "$tree_pages" -ge "$MAX_PAGES" ]]; then
      continue
    fi
    tree_pages=$((tree_pages + 1))
    if [[ -n "$REF" ]] && ! contains_ref_query "$page_url"; then
      echo "FAIL tree page missing ref query: slug=$slug url=$page_url" >&2
      failures=$((failures + 1))
      continue
    fi
    if ! grep -Fxq "$page_url" "$checked_urls_file" 2>/dev/null; then
      printf '%s\n' "$page_url" >>"$checked_urls_file"
      if ! check_resolves "tree page slug=$slug" "$page_url"; then
        failures=$((failures + 1))
      fi
    fi
  done < <(printf '%s' "$tree_json" | jq -r '.[] | select(.kind == "page") | [.slug, .url] | @tsv')
done

for ((i = 0; i < SEARCH_QUERY_COUNT; i++)); do
  query="${SEARCH_QUERIES[$i]}"
  search_url="$API_BASE/repos/$REPO_FULL_NAME/wiki/search"
  search_url="$(append_query "$search_url" "q" "$query")"
  search_url="$(append_query "$search_url" "limit" "50")"
  search_json="$(curl_json_body "$search_url")"
  while IFS=$'\t' read -r slug page_url; do
    [[ -z "$slug" || -z "$page_url" ]] && continue
    if ! check_resolves "search result query=$query slug=$slug" "$page_url"; then
      failures=$((failures + 1))
    fi
  done < <(printf '%s' "$search_json" | jq -r '.results[] | [.slug, .url] | @tsv')
done

for ((i = 0; i < BACKLINK_SLUG_COUNT; i++)); do
  slug="${BACKLINK_SLUGS[$i]}"
  backlink_url="$API_BASE/repos/$REPO_FULL_NAME/wiki/pages/$(urlencode "$slug")/backlinks"
  backlinks_json="$(curl_json_body "$backlink_url")"
  while IFS=$'\t' read -r source_slug page_url; do
    [[ -z "$source_slug" || -z "$page_url" ]] && continue
    if ! check_resolves "backlink source target=$slug source=$source_slug" "$page_url"; then
      failures=$((failures + 1))
    fi
  done < <(printf '%s' "$backlinks_json" | jq -r '.[] | [.slug, .url] | @tsv')
done

if [[ "$failures" -ne 0 ]]; then
  echo "Wiki remote integrity failed: failures=$failures tree_pages_checked=$tree_pages tree_dirs_seen=$tree_dirs" >&2
  exit 1
fi

echo "Wiki remote integrity passed: tree_pages_checked=$tree_pages tree_dirs_seen=$tree_dirs search_queries=$SEARCH_QUERY_COUNT backlink_slugs=$BACKLINK_SLUG_COUNT"
