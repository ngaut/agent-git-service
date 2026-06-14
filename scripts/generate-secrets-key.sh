#!/bin/bash
# Generate a new SECRET_ENCRYPTION_KEY for deployments with multiple server instances.
# Usage: ./scripts/generate-secrets-key.sh
#
# This script generates a cryptographically secure 32-byte random key
# and outputs it as a base64-encoded string suitable for use with
# the SECRET_ENCRYPTION_KEY environment variable.

set -e

# Check for openssl
if ! command -v openssl &> /dev/null; then
    echo "Error: openssl is required but not installed." >&2
    echo "Install with: brew install openssl (macOS) or apt-get install openssl (Linux)" >&2
    exit 1
fi

# Generate 32 random bytes and encode as base64
KEY=$(openssl rand -base64 32)

echo "# Generated SECRET_ENCRYPTION_KEY for shared-instance deployment"
echo "# Store this securely and configure all instances with the same value"
echo "SECRET_ENCRYPTION_KEY=\"$KEY\""
echo
echo "# To use immediately:"
echo "export SECRET_ENCRYPTION_KEY=\"$KEY\""
