# Secrets Encryption Key Management

This document describes the secrets encryption key management for GitHub Actions secrets in `agent-git-service`.

## Overview

`agent-git-service` uses NaCl sealed-box encryption (Curve25519) to encrypt and decrypt GitHub Actions secrets. Clients encrypt secret values with the server's public key using `box.SealAnonymous`, and the server decrypts them using the corresponding private key.

## Key Generation Modes

### Single-Node Mode (Default)

When `SECRET_ENCRYPTION_KEY` is not set, the server generates a new ephemeral keypair at startup:

```go
pub, priv, err := box.GenerateKey(rand.Reader)
```

**Characteristics:**
- Simple setup for local development
- Each server instance has a unique keypair
- Secrets encrypted against one instance cannot be decrypted by another
- **Not suitable for multi-tenant or load-balanced deployments**

### Multi-Tenant Mode

For stateless multi-tenant deployments, all server instances must share the same encryption keypair. Set the `SECRET_ENCRYPTION_KEY` environment variable:

```bash
export SECRET_ENCRYPTION_KEY="<base64-encoded-32-byte-private-key>"
```

**Characteristics:**
- All instances use the same keypair
- Secrets are portable across instances
- Required for load-balanced or horizontally scaled deployments

## Generating a Key for Multi-Tenant Mode

To generate a new key for multi-tenant deployments:

```bash
# Using Go (requires golang.org/x/crypto/nacl/box)
cat > /tmp/gen_key.go << 'EOF'
package main

import (
    "crypto/rand"
    "encoding/base64"
    "fmt"
    "golang.org/x/crypto/nacl/box"
)

func main() {
    pub, priv, err := box.GenerateKey(rand.Reader)
    if err != nil { panic(err) }
    fmt.Println(base64.StdEncoding.EncodeToString(priv[:]))
}
EOF
go run /tmp/gen_key.go
rm /tmp/gen_key.go
```

Or using OpenSSL and a secure random source:

```bash
# Generate 32 random bytes and encode as base64
openssl rand -base64 32
```

**Important:** Store the generated key securely. All server instances in the deployment must use the exact same key. The decoded key must be exactly 32 bytes.

## Configuration

### Environment Variable

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `SECRET_ENCRYPTION_KEY` | No (multi-tenant: **yes**) | (generated) | Base64-encoded 32-byte private key |

### Example Configuration

```env
# Multi-tenant deployment
DB_DSN="root:@tcp(tidb-cluster:4000)/gh-server?parseTime=true"
SECRET_ENCRYPTION_KEY="<base64-encoded-32-byte-key>"
```

## Failure Behavior

### Invalid Key Format

If `SECRET_ENCRYPTION_KEY` is set but invalid, the server panics at startup with a descriptive error:

- **Invalid base64:** `crypto: failed to initialize keypair: SECRET_ENCRYPTION_KEY is not valid base64: ...`
- **Wrong length:** `crypto: failed to initialize keypair: SECRET_ENCRYPTION_KEY must be 32 bytes, got N`

### Key Mismatch in Multi-Tenant Mode

If different instances have different keys:
- Secrets encrypted against instance A cannot be decrypted by instance B
- CLI operations will fail with "failed to open sealed box" errors
- **All instances must be configured with the same key**

## Security Considerations

1. **Key Storage:** In production, the key should be stored in a secure secrets manager (e.g., HashiCorp Vault, AWS Secrets Manager) and injected at runtime.

2. **Key Rotation:** Currently, key rotation is not supported. Rotating the key would invalidate all existing encrypted secrets. If key rotation is needed, all secrets must be re-encrypted with the new key.

3. **Key Uniqueness:** Each deployment should use a unique key. Never share keys between different deployments or environments.

4. **Access Control:** Limit access to the key to only those services that require it. The key should not be logged or exposed in error messages.

## Implementation Details

The key management is implemented in `internal/crypto/nacl.go`:

- On startup, the package checks for `SECRET_ENCRYPTION_KEY`
- If set, it decodes the base64 key and derives the public key using Curve25519 scalar base multiplication
- If unset, it generates a new ephemeral keypair
- The public key is exposed via `/api/v3/repos/{owner}/{repo}/actions/secrets/public-key` endpoints
- The private key is used internally to decrypt sealed boxes

## Testing

To test multi-tenant key configuration locally:

```bash
# Generate a key
KEY=$(openssl rand -base64 32)

# Cleanup function to stop background processes
cleanup() {
  echo "Stopping test servers..."
  kill $PID1 $PID2 2>/dev/null
  wait $PID1 $PID2 2>/dev/null
  echo "Cleanup complete"
}
trap cleanup EXIT

# Start two instances with the same key (capture PIDs)
SECRET_ENCRYPTION_KEY="$KEY" ./gh-server &
PID1=$!
SECRET_ENCRYPTION_KEY="$KEY" ./gh-server --port 8081 &
PID2=$!

# Wait for servers to start
sleep 2

# Both instances should report the same public key
curl http://localhost:8080/api/v3/repos/test/test/actions/secrets/public-key
curl http://localhost:8081/api/v3/repos/test/test/actions/secrets/public-key

# Cleanup is automatic via trap, or run manually:
# kill $PID1 $PID2
```
