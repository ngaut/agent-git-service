#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GITLEAKS_BIN="${GITLEAKS_BIN:-gitleaks}"
RUN_WORKTREE=1
RUN_HISTORY=1

usage() {
  cat <<'EOF'
Usage: scripts/secret_scan.sh [--worktree-only|--history-only]

Runs gitleaks against the current worktree and, by default, full git history.

Options:
  --worktree-only  Scan checked-out files only.
  --history-only   Scan git history only.
  -h, --help       Show this help.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --worktree-only)
      RUN_WORKTREE=1
      RUN_HISTORY=0
      ;;
    --history-only)
      RUN_WORKTREE=0
      RUN_HISTORY=1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
  shift
done

if ! command -v "$GITLEAKS_BIN" >/dev/null 2>&1; then
  cat >&2 <<'EOF'
gitleaks is required.

Install it from https://github.com/gitleaks/gitleaks, or set GITLEAKS_BIN to a
local binary path.
EOF
  exit 127
fi

cd "$ROOT"

if [[ "$RUN_WORKTREE" == "1" ]]; then
  echo "==> gitleaks worktree scan"
  "$GITLEAKS_BIN" detect \
    --no-git \
    --source . \
    --redact \
    --no-banner
fi

if [[ "$RUN_HISTORY" == "1" ]]; then
  echo "==> gitleaks full-history scan"
  "$GITLEAKS_BIN" detect \
    --source . \
    --redact \
    --no-banner
fi
