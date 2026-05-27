#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
E2E_DIR="$ROOT/e2e"

script_arg="${1:-}"
script_name="${SCRIPT:-$script_arg}"

scripts=()
if [[ -z "${script_name}" ]]; then
  while IFS= read -r -d '' f; do
    scripts+=("$f")
  done < <(find "$E2E_DIR" -maxdepth 1 -type f -name "*.sh" \
    ! -name "run.sh" \
    ! -name "lib.sh" \
    ! -name "helpers.sh" \
    -print0 | sort -z)
else
  if [[ "$script_name" != *.sh ]]; then
    script_name="${script_name}.sh"
  fi
  scripts+=("$E2E_DIR/$script_name")
fi

if [[ "${#scripts[@]}" -eq 0 ]]; then
  echo "no e2e scripts found" >&2
  exit 1
fi

for s in "${scripts[@]}"; do
  if [[ ! -f "$s" ]]; then
    echo "e2e script not found: $s" >&2
    exit 1
  fi
  echo
  echo "═══════════════════════════════════════════════════"
  echo "Running: $(basename "$s")"
  echo "═══════════════════════════════════════════════════"
  bash "$s"
done
