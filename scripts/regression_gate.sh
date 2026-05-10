#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
MANIFEST_PATH="${MANIFEST_PATH:-$SCRIPT_DIR/regression_gate.list}"

usage() {
  cat <<'EOF'
Usage:
  bash scripts/regression_gate.sh [--manifest <path>]

Runs the fast regression gate:
  1) compile the full backend tree with go test ./... -run '^$'
  2) compile the CLI trees with go test ./... -run '^$'
  3) run the stable package tests listed in scripts/regression_gate.list
  4) run shell audits listed in scripts/regression_gate.list
EOF
}

trim_manifest_line() {
  local line="$1"
  line="${line%%#*}"
  line="${line#"${line%%[![:space:]]*}"}"
  line="${line%"${line##*[![:space:]]}"}"
  printf '%s\n' "$line"
}

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

parse_manifest() {
  local manifest="$1"
  local section=""

  GO_PACKAGES=()
  SHELL_SCRIPTS=()

  while IFS= read -r raw_line || [[ -n "$raw_line" ]]; do
    local line
    line="$(trim_manifest_line "$raw_line")"
    [[ -z "$line" ]] && continue

    if [[ "$line" =~ ^\[([a-z]+)\]$ ]]; then
      section="${BASH_REMATCH[1]}"
      continue
    fi

    case "$section" in
      go) GO_PACKAGES+=("$line") ;;
      shell) SHELL_SCRIPTS+=("$line") ;;
      *)
        echo "invalid manifest entry outside [go]/[shell]: $line" >&2
        exit 2
        ;;
    esac
  done <"$manifest"

  if [[ "${#GO_PACKAGES[@]}" -eq 0 ]]; then
    echo "manifest has no [go] entries: $manifest" >&2
    exit 2
  fi
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --manifest)
      MANIFEST_PATH="${2:-}"
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

if [[ ! -f "$MANIFEST_PATH" ]]; then
  echo "manifest not found: $MANIFEST_PATH" >&2
  exit 2
fi

require_cmd bash
require_cmd git
require_cmd go
require_git_http_backend
parse_manifest "$MANIFEST_PATH"

cd "$ROOT_DIR"

echo "==> Compile backend tree"
go test ./... -run '^$'

echo "==> Compile CLI tree"
(
  cd "$ROOT_DIR/cli"
  env -u NO_COLOR -u CLICOLOR -u PAGER -u GH_PAGER go test ./... -run '^$'
)

echo "==> Compile go-gh-local tree"
(
  cd "$ROOT_DIR/cli/_go-gh-local"
  go test ./... -run '^$'
)

for pkg in "${GO_PACKAGES[@]}"; do
  echo "==> go test $pkg -count=1"
  go test "$pkg" -count=1
done

for script_path in "${SHELL_SCRIPTS[@]}"; do
  if [[ ! -f "$ROOT_DIR/$script_path" ]]; then
    echo "missing shell script from manifest: $script_path" >&2
    exit 2
  fi
  echo "==> bash $script_path"
  bash "$ROOT_DIR/$script_path"
done

echo "Regression gate passed."
