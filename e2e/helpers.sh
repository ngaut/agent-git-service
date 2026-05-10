#!/usr/bin/env bash
# helpers.sh - Shared utility functions for multi-tenant control-plane integration tests

# Record test result and update counters
# Usage: record_result <name> <status> [details]
record_result() {
  local name="$1"
  local status="$2"
  local details="${3:-}"
  TEST_RESULTS+=("$name|$status|$details")
  case "$status" in
    PASS) ((TESTS_PASSED++)) || true ;;
    FAIL) ((TESTS_FAILED++)) || true ;;
    SKIP) ((TESTS_SKIPPED++)) || true ;;
  esac
}

# Check if HTTP status code is in accepted set
# Usage: is_accepted_status <code> <space-separated accepted codes>
is_accepted_status() {
  local code="$1"
  shift
  for accepted in "$@"; do
    [[ "$code" == "$accepted" ]] && return 0
  done
  return 1
}

# Print summary of test results
print_summary() {
  note "=== Test Summary ==="
  echo ""
  echo "Tests Passed:  $TESTS_PASSED"
  echo "Tests Failed:  $TESTS_FAILED"
  echo "Tests Skipped: $TESTS_SKIPPED"
  echo ""
  echo "Detailed Results:"
  echo "----------------"
  local result name status details
  for result in "${TEST_RESULTS[@]}"; do
    IFS='|' read -r name status details <<< "$result"
    case "$status" in
      PASS) echo "✓ $name" ;;
      FAIL) echo "✗ $name: $details" ;;
      SKIP) echo "○ $name: $details" ;;
    esac
  done
}

# Generate detailed test report in Markdown format.
generate_report() {
  local SUMMARY_FILE="/tmp/codex-b-e2e-test-summary.md"

  cat > "$SUMMARY_FILE" << EOF
# E2E Test Summary

**Generated**: $(date -Iseconds)
**Base URL**: $BASE_URL

## Test Results Summary

| Status | Count |
|--------|-------|
| Passed | $TESTS_PASSED |
| Failed | $TESTS_FAILED |
| Skipped | $TESTS_SKIPPED |
| Total | $((TESTS_PASSED + TESTS_FAILED + TESTS_SKIPPED)) |

## Detailed Test Results

| Test ID | Status | Details |
|---------|--------|---------|
EOF

  local result name status details
  for result in "${TEST_RESULTS[@]}"; do
    IFS='|' read -r name status details <<< "$result"
    echo "| $name | $status | $details |" >> "$SUMMARY_FILE"
  done

  cat >> "$SUMMARY_FILE" << EOF

## Failed Test Details

EOF

  local has_failures=false
  for result in "${TEST_RESULTS[@]}"; do
    IFS='|' read -r name status details <<< "$result"
    if [[ "$status" == "FAIL" ]]; then
      has_failures=true
      echo "### $name" >> "$SUMMARY_FILE"
      echo "**Error**: $details" >> "$SUMMARY_FILE"
      echo "" >> "$SUMMARY_FILE"
    fi
  done

  if [[ "$has_failures" == "false" ]]; then
    echo "No test failures." >> "$SUMMARY_FILE"
  fi

  note "Test summary written to: $SUMMARY_FILE"
}
