#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DOC_PATH="$ROOT/docs/module-contracts.md"

packages="$(
  cd "$ROOT"
  find internal -type f -name '*.go' -print |
    awk -F/ 'NF >= 3 { print $2 }' |
    LC_ALL=C sort -u
)"

missing=()
while IFS= read -r pkg; do
  [[ -n "$pkg" ]] || continue
  if ! grep -Fq "| \`$pkg\` |" "$DOC_PATH"; then
    missing+=("$pkg")
  fi
done <<EOF
$packages
EOF

if ((${#missing[@]} > 0)); then
  echo "module-contracts: docs/module-contracts.md is missing top-level package entries for:" >&2
  for pkg in "${missing[@]}"; do
    echo "  - internal/$pkg" >&2
  done
  exit 1
fi

echo "module-contracts: PASS"
