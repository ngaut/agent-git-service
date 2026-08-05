# Token Lifecycle Test Coverage

This document describes the test coverage for user token lifecycle management in `agent-git-service`.

## Overview

Token lifecycle management covers the complete lifecycle of user authentication tokens from creation through revocation, including:

- Token provisioning
- Token expiration handling
- Token refresh
- Token revocation
- LRU-based token cap enforcement

## Test Files

### E2E Tests

**Location:** `e2e/token-lifecycle.sh`

Comprehensive end-to-end tests that run against a live server instance. These tests validate the complete token lifecycle through the HTTP API.

**Run:** `make test-e2e SCRIPT=token-lifecycle`

### Unit Tests

**Location:** `internal/service/tokens_test.go`

Unit tests for the token service layer covering business logic.

### Integration Tests

**Location:** `internal/rest/handlers_tokens_test.go`

HTTP integration tests for token endpoints.

**Run:** `go test ./internal/rest/... -run TestTokens`

## Test Scenarios

### 1. Initial Token Provisioning

**Test:** `Test 1: Initial Token Provisioning`

**Coverage:**
- Create token without `expires_at`
- Token is immediately usable
- Token appears in user's token list
- Token has valid ID and value

**Expected Behavior:**
- POST `/api/ext/v1/user/tokens` with name returns 201
- Response includes `id`, `name`, `token`, `created_at`
- Token can authenticate immediately

### 2. Valid expires_at Handling

**Test:** `Test 2: Valid expires_at Handling`

**Coverage:**
- Create token with future `expires_at` in RFC3339 format
- Token works before expiration
- Expiration is stored correctly

**Expected Behavior:**
- POST with valid future `expires_at` returns 201
- Token authenticates successfully before expiration
- `expires_at` is visible in token list

### 3. Invalid expires_at Handling (Past Time)

**Test:** `Test 3: Invalid expires_at Handling (past time)`

**Coverage:**
- Attempt to create token with past `expires_at`
- Server rejects with validation error

**Expected Behavior:**
- POST with past `expires_at` returns 422
- Error message indicates validation failure
- No token is created

### 4. Invalid expires_at Handling (Malformed)

**Test:** `Test 4: Invalid expires_at Handling (malformed format)`

**Coverage:**
- Attempt to create token with non-RFC3339 `expires_at`
- Server rejects with format error

**Expected Behavior:**
- POST with malformed `expires_at` returns 422
- Error mentions RFC3339 format requirement
- No token is created

### 5. Delete Token by ID

**Test:** `Test 5: Delete Token by ID`

**Coverage:**
- Create token
- Delete using `id` field
- Token immediately becomes invalid
- Token removed from list

**Expected Behavior:**
- DELETE with `{"id": N}` returns 204
- Subsequent auth with deleted token returns 401
- Token no longer appears in list

### 6. Delete Token by Value

**Test:** `Test 6: Delete Token by Value`

**Coverage:**
- Create token
- Delete using `token` field (raw token value)
- Token immediately becomes invalid
- Token removed from list

**Expected Behavior:**
- DELETE with `{"token": "..."}` returns 204
- Subsequent auth with deleted token returns 401
- Token no longer appears in list

### 7. Token Revocation and Immediate Invalidation

**Test:** `Test 7: Token Revocation and Immediate Invalidation`

**Coverage:**
- Create token
- Verify token works
- Revoke token
- Immediately attempt to use revoked token
- Verify token removed from list

**Expected Behavior:**
- Revocation is immediate (no propagation delay)
- Revoked token returns 401 on next use
- Token removed from list immediately

### 8. LRU Token Cap

**Test:** `Test 8: LRU Token Cap - Does Not Evict Wrong Token`

**Coverage:**
- Create more tokens than `maxTokensPerUser` (20)
- Verify oldest/least-used tokens are evicted
- Verify recently-used tokens are preserved
- LRU ordering based on `last_used_at` and `created_at`

**Expected Behavior:**
- When cap exceeded, oldest tokens evicted first
- Recently touched tokens preserved
- Eviction order: `ORDER BY COALESCE(last_used_at, created_at) ASC, id ASC`
- Cap enforcement happens during token creation

**LRU Algorithm:**
```sql
ORDER BY COALESCE(last_used_at, created_at) ASC, id ASC
LIMIT extra
```

### 9. Concurrent Token Operations

**Test:** `Test 9: Concurrent Token Operations`

**Coverage:**
- Create multiple tokens simultaneously
- Verify all tokens created successfully
- No race conditions or data corruption

**Expected Behavior:**
- Concurrent creations all succeed
- Each token has unique ID and value
- No duplicate or missing tokens

### 10. Token Refresh Before Expiration

**Test:** `Test 10: Token Refresh Before Expiration`

**Coverage:**
- Create token with future expiration
- Use token multiple times (triggers `TouchToken`)
- Token remains valid
- `last_used_at` updated to prevent LRU eviction

**Expected Behavior:**
- Token remains valid through its lifetime
- `TouchToken` updates `last_used_at` (throttled to 1-minute window)
- Active tokens not evicted by LRU cap

### 11. Token List and Pagination

**Test:** `Test 11: Token List and Pagination`

**Coverage:**
- List all tokens for user
- Verify required fields present
- Pagination works correctly

**Expected Behavior:**
- GET `/api/ext/v1/user/tokens` returns array
- Each token has `id`, `name`, `token`, `created_at`, `expires_at` (optional)
- Results ordered by `created_at DESC`

## API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/ext/v1/user/tokens` | List user's tokens |
| POST | `/api/ext/v1/user/tokens` | Create new token |
| DELETE | `/api/ext/v1/user/tokens` | Delete token (by ID or value) |

## Request/Response Examples

### Create Token

**Request:**
```json
POST /api/ext/v1/user/tokens
Authorization: token <admin_token>
Content-Type: application/json

{
  "name": "my-token",
  "expires_at": "2026-12-31T23:59:59Z"
}
```

**Response:**
```json
{
  "id": 123,
  "name": "my-token",
  "token": "abc123...",
  "created_at": "2026-03-23T18:00:00Z",
  "expires_at": "2026-12-31T23:59:59Z"
}
```

### Delete Token by ID

**Request:**
```json
DELETE /api/ext/v1/user/tokens
Authorization: token <admin_token>
Content-Type: application/json

{
  "id": 123
}
```

**Response:** `204 No Content`

### Delete Token by Value

**Request:**
```json
DELETE /api/ext/v1/user/tokens
Authorization: token <admin_token>
Content-Type: application/json

{
  "token": "abc123..."
}
```

**Response:** `204 No Content`

## Configuration

### Token Limits

| Setting | Value | Description |
|---------|-------|-------------|
| `maxTokensPerUser` | 20 | Maximum tokens per user |
| `tokenHexLen` | 40 | Length of token hex string |
| `tokenTouchMinWindow` | 1 minute | Minimum interval between `last_used_at` updates |

### Validation Rules

| Field | Rule |
|-------|------|
| `name` | Required, non-empty after trim |
| `expires_at` | Optional, must be RFC3339 format, must be in future |
| `token` (delete) | Required if `id`/`token_id` not provided |
| `id`/`token_id` (delete) | Required if `token` not provided |

## Edge Cases

### Covered

- ✓ Token creation with no expiration
- ✓ Token creation with future expiration
- ✓ Token creation with past expiration (rejected)
- ✓ Token creation with malformed expiration (rejected)
- ✓ Delete by numeric ID
- ✓ Delete by token value
- ✓ Immediate invalidation after deletion
- ✓ LRU eviction preserves recently-used tokens
- ✓ Concurrent token creation
- ✓ Token usage updates `last_used_at`

### Future Coverage

- Token expiration at exact boundary (token expires mid-request)
- Token cap behavior with exactly `maxTokensPerUser` tokens
- Empty token name handling
- Unicode/special characters in token names
- Token value collision handling (extremely rare)

## Monitoring Hooks

Token lifecycle events can be monitored via:

1. **Token Creation:** Log entry in token creation path
2. **Token Usage:** `TouchToken` updates `last_used_at`
3. **Token Deletion:** Log entry in deletion path
4. **LRU Eviction:** Implicit in token creation with cap enforcement

## SLA Requirements

| Metric | Target | Measurement |
|--------|--------|-------------|
| Token refresh latency | < 100ms | Time from refresh request to valid new token |
| Revocation propagation | Immediate | Time from delete to 401 response |
| Token creation | < 200ms | Time from POST to 201 response |

## Related Files

- `internal/rest/handlers_tokens.go` - HTTP handlers
- `internal/service/tokens.go` - Service layer logic
- `internal/db/models_auth.go` - Token database model
- `e2e/token-lifecycle.sh` - E2E test suite
- `e2e/token-api.sh` - Original token API smoke test
