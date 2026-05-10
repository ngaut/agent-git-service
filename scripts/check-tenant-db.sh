#!/bin/bash
# scripts/check-tenant-db.sh
# 
# This script checks for violations of tenant DB correctness rules.
# It should be run in CI to prevent regressions.
#
# Exit codes:
#   0 - No violations found
#   1 - Violations found

set -euo pipefail

echo "🔍 Checking for tenant DB correctness violations..."

# Allowlist of files/patterns exempt from direct DB access checks
# These files are allowed to have s.DB.* calls for legitimate reasons
ALLOWLIST_PATTERNS=(
    "repo.go"  # DBForCtx implementation lives here
)

# Check if a file is in the allowlist
is_allowlisted() {
    local file="$1"
    for pattern in "${ALLOWLIST_PATTERNS[@]}"; do
        if [[ "$file" == *"$pattern" ]]; then
            return 0
        fi
    done
    return 1
}

# Check a file for direct s.DB access (excluding DBForCtx and comments)
# Returns matching lines (or empty if none).
get_direct_db_matches() {
    local file="$1"
    grep -n "s\.DB\." "$file" | grep -v "DBForCtx" | grep -v "^[[:space:]]*//" || true
}

# Check a file for direct s.DB access (excluding DBForCtx and comments)
# Returns 0 if violations found, 1 if clean
check_direct_db_access() {
    local file="$1"
    local matches
    matches=$(get_direct_db_matches "$file")
    [[ -n "$matches" ]]
}

# Find all Go files in internal/service/ excluding test files
VIOLATIONS=0

# Check for direct s.DB access patterns
echo "Checking for direct s.DB.* calls..."
while IFS= read -r -d '' file; do
    if check_direct_db_access "$file"; then
        if is_allowlisted "$file"; then
            echo "  ⚠️  Found s.DB.* in allowlisted file $file (verify it's in DBForCtx)"
        else
            matches=$(get_direct_db_matches "$file")
            echo "  ❌ VIOLATION in $file:"
            echo "$matches" | sed 's/^/      /'
            VIOLATIONS=$((VIOLATIONS + 1))
        fi
    fi
done < <(find internal/service -name "*.go" -not -name "*_test.go" -type f -print0)

# Helper: Check for missing ctx parameter in service methods
check_missing_ctx() {
    local file="$1"
    local matches
    matches=$(awk '
        BEGIN {
            method_pattern = "^func \\(s \\*Service\\) [A-Z]"
            context_pattern = "context\\.Context"
            exception_pattern = "(ServerCtx|DBForCtx|HTMLBaseURL)"
        }
        $0 ~ method_pattern {
            if ($0 ~ context_pattern) { next }
            if ($0 ~ exception_pattern) { next }
            printf "%d:%s\n", NR, $0
        }
    ' "$file")
    if [[ -n "$matches" ]]; then
        echo "$matches"
        echo "  ⚠️  Method in $file might be missing context.Context parameter"
    fi
}

# Helper: Check that background goroutines propagate tenant DB
check_goroutine_db_propagation() {
    local file="$1"
    if grep -q "go func()" "$file"; then
        # Check if the file has ContextWithDB pattern anywhere (not just nearby)
        if ! grep -q "ContextWithDB\|DBFromContext" "$file"; then
            echo "  ⚠️  Background goroutine in $file might not propagate tenant DB"
            echo "      Consider adding:"
            echo "        bgCtx := s.ServerCtx()"
            echo "        if tenantDB, ok := DBFromContext(ctx); ok {"
            echo "            bgCtx = ContextWithDB(bgCtx, tenantDB)"
            echo "        }"
        fi
    fi
}

# Single loop over all service files with helper checks per rule
while IFS= read -r -d '' file; do
    check_missing_ctx "$file"
    check_goroutine_db_propagation "$file"
done < <(find internal/service -name "*.go" -not -name "*_test.go" -type f -print0)

if [ $VIOLATIONS -eq 0 ]; then
    echo "✅ No tenant DB correctness violations found!"
    exit 0
else
    echo ""
    echo "❌ Found $VIOLATIONS file(s) with tenant DB violations"
    echo ""
    echo "See docs/architecture/tenant-db-correctness.md for guidelines."
    exit 1
fi
