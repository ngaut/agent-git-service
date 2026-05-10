#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

require_cmd() {
  local cmd="$1"
  command -v "$cmd" >/dev/null 2>&1 || {
    echo "missing dependency: $cmd" >&2
    exit 1
  }
}

require_git_http_backend() {
  local backend
  backend="$(git --exec-path)/git-http-backend"
  if [[ ! -x "$backend" ]]; then
    echo "git-http-backend not found at $backend" >&2
    exit 1
  fi
}

require_cmd git
require_cmd go
require_git_http_backend

cd "$ROOT_DIR"

REST_RUN='^(TestHostRewrite_|TestOAuth_|TestAuth_|TestRepoLifecycle|TestIssueLifecycle|TestPRLifecycle|TestGraphQLWriteRESTRead_StateParity|TestRESTWriteGraphQLRead_StateParity|TestOrganizationInvitationHandlers_FullFlow|TestTeamShare_)'
GRAPHQL_RUN='^(TestGraphQLAuth_|TestGraphQL_RepositoryQuery|TestGraphQL_IssueQuery|TestGraphQL_PRQuery|TestGraphQL_CreateIssueMutation|TestGraphQL_MergePRMutation|TestGraphQL_MergePRMutation_FailureCases)$'

echo "==> go test -count=1 ./internal/testharness"
go test -count=1 ./internal/testharness

echo "==> go test -count=1 ./internal/router"
go test -count=1 ./internal/router

echo "==> go test -count=1 ./internal/githttp/..."
go test -count=1 ./internal/githttp/...

echo "==> go test -count=1 ./internal/rest -run ${REST_RUN}"
go test -count=1 ./internal/rest -run "$REST_RUN"

echo "==> go test -count=1 ./internal/graphql -run ${GRAPHQL_RUN}"
go test -count=1 ./internal/graphql -run "$GRAPHQL_RUN"

echo "Integration tests passed."
