#!/bin/bash

# Performs spam detection on an issue and labels it if it's spam.
#
# Regardless of the spam detection result, the script always exits with a zero
# exit code, unless there's a runtime error.
#
# This script must be run from the root directory of the repository.

set -euo pipefail

# Determine absolute path to script directory based on where it is called from.
# This allows the script to be run from any directory.
SPAM_DIR="$(dirname "$(realpath "$0")")"

_issue_url="$1"
if [[ -z "$_issue_url" ]]; then
    echo "error: issue URL is empty" >&2
    exit 1
fi

_result="$("$SPAM_DIR/check-issue.sh" "$_issue_url")"

if [[ "$_result" == "PASS" ]]; then
    echo "detected as not-spam: $_issue_url"
    exit 0
fi

echo "detected as spam: $_issue_url"

cat << EOF | gh issue comment "$_issue_url" --body-file -
Thank you for taking the time to create this issue.

We've automatically reviewed this issue and suspect it as potentially inauthentic or spam-like content. As a result, we're closing this issue.

**If this was closed by mistake**, please don't hesitate to reach out to us by commenting on this issue with additional context.

We appreciate your understanding and apologize if this action was taken in error. Our automated systems help us manage the large volume of issues we receive, but we know they're not perfect.
EOF

gh issue edit --add-label "suspected-spam" --add-label "invalid" "$_issue_url"
gh issue close --reason 'not planned' "$_issue_url"

echo "issue processed as suspected spam: commented, closed, and labeled"
