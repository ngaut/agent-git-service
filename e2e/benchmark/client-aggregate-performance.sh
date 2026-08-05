#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$ROOT/e2e/lib.sh"

require_cmd curl
require_cmd jq
require_cmd python3

BASE_URL="$(strip_trailing_slash "${E2E_BASE_URL:-http://github.localhost}")"
TOKEN="${E2E_TOKEN:-${ADMIN_TOKEN:-${TEST_TOKEN:-${GH_TOKEN:-}}}}"
ITERATIONS="${E2E_PERF_ITERATIONS:-5}"
WARMUP_ITERATIONS="${E2E_PERF_WARMUP_ITERATIONS:-1}"
PER_PAGE="${E2E_PERF_PER_PAGE:-100}"
COMMENT_PER_PAGE="${E2E_PERF_COMMENT_PER_PAGE:-100}"
WIKI_BATCH_LIMIT="${E2E_PERF_WIKI_BATCH_LIMIT:-10}"

if [[ -z "$TOKEN" ]]; then
  note "Skipping client aggregate performance e2e: set E2E_TOKEN, ADMIN_TOKEN, TEST_TOKEN, or GH_TOKEN."
  exit 0
fi

if ! [[ "$ITERATIONS" =~ ^[0-9]+$ ]] || [[ "$ITERATIONS" -lt 1 ]]; then
  echo "E2E_PERF_ITERATIONS must be a positive integer" >&2
  exit 1
fi

if ! [[ "$WARMUP_ITERATIONS" =~ ^[0-9]+$ ]]; then
  echo "E2E_PERF_WARMUP_ITERATIONS must be a non-negative integer" >&2
  exit 1
fi

RESULTS="$(mktemp)"
trap 'rm -f "$RESULTS"' EXIT
REQUESTS=0

now_ns() {
  python3 - <<'PY'
import time
print(time.perf_counter_ns())
PY
}

elapsed_ms() {
  local start="$1"
  local end="$2"
  python3 - "$start" "$end" <<'PY'
import sys
start = int(sys.argv[1])
end = int(sys.argv[2])
print(f"{(end - start) / 1_000_000:.3f}")
PY
}

url_path_escape() {
  python3 - "$1" <<'PY'
import sys
from urllib.parse import quote
print(quote(sys.argv[1], safe=""))
PY
}

auth_args() {
  printf '%s\0' -H "Authorization: token $TOKEN"
}

request() {
  local method="$1"
  local url="$2"
  local body="${3:-}"
  local tmp
  tmp="$(mktemp)"
  local code
  REQUESTS=$((REQUESTS + 1))
  if [[ -n "$body" ]]; then
    code="$(curl -ksS -o "$tmp" -w "%{http_code}" -X "$method" \
      -H "Authorization: token $TOKEN" \
      -H "Content-Type: application/json" \
      -d "$body" \
      "$url")"
  else
    code="$(curl -ksS -o "$tmp" -w "%{http_code}" -X "$method" \
      -H "Authorization: token $TOKEN" \
      "$url")"
  fi
  if [[ "$code" != 2* ]]; then
    echo "unexpected status: method=$method code=$code url=$url" >&2
    echo "response body:" >&2
    cat "$tmp" >&2
    rm -f "$tmp"
    return 1
  fi
  cat "$tmp"
  rm -f "$tmp"
}

request_optional() {
  local method="$1"
  local url="$2"
  local body="${3:-}"
  local tmp
  tmp="$(mktemp)"
  local code
  REQUESTS=$((REQUESTS + 1))
  if [[ -n "$body" ]]; then
    code="$(curl -ksS -o "$tmp" -w "%{http_code}" -X "$method" \
      -H "Authorization: token $TOKEN" \
      -H "Content-Type: application/json" \
      -d "$body" \
      "$url")"
  else
    code="$(curl -ksS -o "$tmp" -w "%{http_code}" -X "$method" \
      -H "Authorization: token $TOKEN" \
      "$url")"
  fi
  if [[ "$code" == 2* ]]; then
    cat "$tmp"
  fi
  rm -f "$tmp"
}

measure_once() {
  local case_name="$1"
  local mode="$2"
  local fn="$3"
  local iteration="$4"
  REQUESTS=0
  local start end ms
  start="$(now_ns)"
  "$fn" >/dev/null
  end="$(now_ns)"
  ms="$(elapsed_ms "$start" "$end")"
  printf '%s\t%s\t%s\t%s\t%s\n' "$case_name" "$mode" "$iteration" "$ms" "$REQUESTS" >> "$RESULTS"
}

benchmark_pair() {
  local case_name="$1"
  local old_fn="$2"
  local new_fn="$3"
  local reason="${4:-}"
  if [[ -n "$reason" ]]; then
    note "Skipping $case_name: $reason"
    return 0
  fi

  note "Benchmarking $case_name"
  for i in $(seq 1 "$WARMUP_ITERATIONS"); do
    measure_once "$case_name" old "$old_fn" "warmup-$i" >/dev/null
    measure_once "$case_name" new "$new_fn" "warmup-$i" >/dev/null
  done
  for i in $(seq 1 "$ITERATIONS"); do
    measure_once "$case_name" old "$old_fn" "$i"
    measure_once "$case_name" new "$new_fn" "$i"
  done
}

discover_repo() {
  if [[ -n "${E2E_PERF_REPO:-}" ]]; then
    echo "$E2E_PERF_REPO"
    return 0
  fi
  request GET "$BASE_URL/api/v3/user/repos?per_page=1" | jq -r '.[0].full_name // empty'
}

discover_org() {
  if [[ -n "${E2E_PERF_ORG:-}" ]]; then
    echo "$E2E_PERF_ORG"
    return 0
  fi
  request GET "$BASE_URL/api/v3/user/orgs" | jq -r '.[0].login // empty'
}

discover_issue_number() {
  local repo_full="$1"
  if [[ -n "${E2E_PERF_ISSUE_NUMBER:-}" ]]; then
    echo "$E2E_PERF_ISSUE_NUMBER"
    return 0
  fi
  request_optional GET "$BASE_URL/api/v3/repos/$repo_full/issues?state=all&kind=issue&fields=number&per_page=1" | jq -r '.[0].number // empty'
}

discover_wiki_slugs() {
  local repo_full="$1"
  if [[ -n "${E2E_PERF_WIKI_SLUGS:-}" ]]; then
    echo "$E2E_PERF_WIKI_SLUGS"
    return 0
  fi
  request_optional GET "$BASE_URL/api/ext/v1/repos/$repo_full/wiki/catalog?include=pages&recursive=true" | jq -r --argjson limit "$WIKI_BATCH_LIMIT" '[.pages[]?.slug][0:$limit] | join(",")'
}

viewer_old_chain() {
  request GET "$BASE_URL/api/v3/user"
  request GET "$BASE_URL/api/v3/user/repos?per_page=$PER_PAGE"
  request GET "$BASE_URL/api/v3/user/orgs"
  request GET "$BASE_URL/api/v3/user/repository_invitations"
  request GET "$BASE_URL/api/v3/user/organization_invitations"
  request GET "$BASE_URL/api/ext/v1/user/agents"
}

viewer_new_chain() {
  request GET "$BASE_URL/api/ext/v1/viewer/summary?include=user,orgs,repositories,invitations,agent_bindings&per_page=$PER_PAGE"
}

notifications_old_chain() {
  request GET "$BASE_URL/api/v3/notifications?per_page=$PER_PAGE"
}

notifications_new_chain() {
  request GET "$BASE_URL/api/ext/v1/notifications/summary?include=subject,repository&per_page=$PER_PAGE"
}

org_old_chain() {
  request GET "$BASE_URL/api/v3/orgs/$ORG"
  request GET "$BASE_URL/api/v3/orgs/$ORG/repos?per_page=$PER_PAGE"
  request GET "$BASE_URL/api/v3/orgs/$ORG/members?per_page=$PER_PAGE"
  request GET "$BASE_URL/api/v3/orgs/$ORG/invitations?per_page=$PER_PAGE"
  request GET "$BASE_URL/api/v3/orgs/$ORG/teams?per_page=$PER_PAGE"
}

org_new_chain() {
  request GET "$BASE_URL/api/ext/v1/orgs/$ORG/management-summary?include=org,viewer,repos,members,invitations,teams"
}

repo_old_chain() {
  request GET "$BASE_URL/api/v3/repos/$REPO_FULL"
  request GET "$BASE_URL/api/v3/repos/$REPO_FULL/labels"
  request_optional GET "$BASE_URL/api/ext/v1/repos/$REPO_FULL/wiki/pages?recursive=true&per_page=$PER_PAGE"
  request GET "$BASE_URL/api/ext/v1/user/agents"
}

repo_new_chain() {
  request GET "$BASE_URL/api/ext/v1/repos/$REPO_FULL/summary?include=repo,viewer,counts,labels,wiki,agents"
}

issue_list_old_chain() {
  request GET "$BASE_URL/api/v3/repos/$REPO_FULL/issues?state=all&per_page=$PER_PAGE" | jq --arg prefix "${E2E_PERF_TITLE_PREFIX:-}" '
    if $prefix == "" then . else map(select(.title | startswith($prefix))) end
  '
}

issue_list_new_chain() {
  local url="$BASE_URL/api/v3/repos/$REPO_FULL/issues?state=all&kind=issue&fields=number,title,state,updated_at&per_page=$PER_PAGE"
  if [[ -n "${E2E_PERF_TITLE_PREFIX:-}" ]]; then
    url="$url&title_prefix=$(python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1]))' "${E2E_PERF_TITLE_PREFIX}")"
  fi
  request GET "$url"
}

issue_thread_old_chain() {
  request GET "$BASE_URL/api/v3/repos/$REPO_FULL/issues/$ISSUE_NUMBER"
  request GET "$BASE_URL/api/v3/repos/$REPO_FULL/issues/$ISSUE_NUMBER/comments?per_page=$COMMENT_PER_PAGE"
}

issue_thread_new_chain() {
  request GET "$BASE_URL/api/ext/v1/repos/$REPO_FULL/issues/$ISSUE_NUMBER/thread?include=issue,comments&comments_per_page=$COMMENT_PER_PAGE"
}

wiki_catalog_old_chain() {
  request GET "$BASE_URL/api/ext/v1/repos/$REPO_FULL/wiki/tree"
  request GET "$BASE_URL/api/ext/v1/repos/$REPO_FULL/wiki/pages?recursive=true&per_page=$PER_PAGE"
}

wiki_catalog_new_chain() {
  request GET "$BASE_URL/api/ext/v1/repos/$REPO_FULL/wiki/catalog?include=tree,pages,labels&recursive=true"
}

wiki_batch_old_chain() {
  local slug
  for slug in "${WIKI_SLUGS[@]}"; do
    request GET "$BASE_URL/api/ext/v1/repos/$REPO_FULL/wiki/pages/$(url_path_escape "$slug")"
  done
}

wiki_batch_new_chain() {
  local slugs_json
  local body
  slugs_json="$(printf '%s\n' "${WIKI_SLUGS[@]}" | jq -R . | jq -s .)"
  body="$(jq -cn --argjson slugs "$slugs_json" '{slugs: $slugs, include: ["body", "labels"], body_limit: 20000}')"
  request POST "$BASE_URL/api/ext/v1/repos/$REPO_FULL/wiki/pages/batch" "$body"
}

note "Base URL: $BASE_URL"
note "Iterations: $ITERATIONS, warmup: $WARMUP_ITERATIONS"

VIEWER="$(request GET "$BASE_URL/api/v3/user" | jq -r '.login // empty')"
if [[ -z "$VIEWER" ]]; then
  echo "unable to resolve authenticated viewer" >&2
  exit 1
fi
note "Viewer: $VIEWER"

REPO_FULL="$(discover_repo)"
ORG="$(discover_org)"
ISSUE_NUMBER=""
WIKI_SLUGS_CSV=""
WIKI_SLUGS=()

if [[ -n "$REPO_FULL" ]]; then
  note "Repo target: $REPO_FULL"
  ISSUE_NUMBER="$(discover_issue_number "$REPO_FULL")"
  WIKI_SLUGS_CSV="$(discover_wiki_slugs "$REPO_FULL")"
  if [[ -n "$WIKI_SLUGS_CSV" ]]; then
    IFS=',' read -r -a WIKI_SLUGS <<< "$WIKI_SLUGS_CSV"
    note "Wiki page targets: ${#WIKI_SLUGS[@]}"
  fi
else
  note "No repository target found. Set E2E_PERF_REPO=owner/repo to enable repo benchmarks."
fi

if [[ -n "$ORG" ]]; then
  note "Org target: $ORG"
else
  note "No organization target found. Set E2E_PERF_ORG=org to enable org benchmarks."
fi

benchmark_pair "viewer_summary" viewer_old_chain viewer_new_chain
benchmark_pair "notifications_summary" notifications_old_chain notifications_new_chain
benchmark_pair "org_management_summary" org_old_chain org_new_chain "$([[ -z "$ORG" ]] && echo "no org target")"
benchmark_pair "repo_summary" repo_old_chain repo_new_chain "$([[ -z "$REPO_FULL" ]] && echo "no repo target")"
benchmark_pair "issue_list_filters" issue_list_old_chain issue_list_new_chain "$([[ -z "$REPO_FULL" ]] && echo "no repo target")"
benchmark_pair "issue_thread" issue_thread_old_chain issue_thread_new_chain "$([[ -z "$REPO_FULL" || -z "$ISSUE_NUMBER" ]] && echo "no issue target")"
benchmark_pair "wiki_catalog" wiki_catalog_old_chain wiki_catalog_new_chain "$([[ -z "$REPO_FULL" || "${#WIKI_SLUGS[@]}" -eq 0 ]] && echo "no wiki pages")"
benchmark_pair "wiki_batch_pages" wiki_batch_old_chain wiki_batch_new_chain "$([[ -z "$REPO_FULL" || "${#WIKI_SLUGS[@]}" -eq 0 ]] && echo "no wiki pages")"

echo
echo "Client aggregate performance results"
echo "case,old_requests,new_requests,old_avg_ms,new_avg_ms,delta_ms,speedup"
awk -F'\t' '
  $3 !~ /^warmup-/ {
    key=$1 SUBSEP $2
    sum[key]+=$4
    count[key]++
    req[key]+=$5
    cases[$1]=1
  }
  END {
    for (c in cases) {
      old_key=c SUBSEP "old"
      new_key=c SUBSEP "new"
      if (count[old_key] == 0 || count[new_key] == 0) {
        continue
      }
      old_avg=sum[old_key]/count[old_key]
      new_avg=sum[new_key]/count[new_key]
      old_req=req[old_key]/count[old_key]
      new_req=req[new_key]/count[new_key]
      delta=old_avg-new_avg
      speedup=(new_avg > 0 ? old_avg/new_avg : 0)
      printf "%s,%.1f,%.1f,%.3f,%.3f,%.3f,%.2fx\n", c, old_req, new_req, old_avg, new_avg, delta, speedup
    }
  }
' "$RESULTS" | sort

ok "client aggregate performance benchmark completed"
