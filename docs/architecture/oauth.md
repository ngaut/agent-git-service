# OAuth — Component Reference

## Purpose

`internal/oauth` implements the GitHub OAuth device-flow and authorization-code endpoints needed for `gh auth login --git-protocol https`.
It is a headless backend surface: AGS manages device code creation, user approval decisions, token exchange, and browser authorization with PKCE and state validation, while external console components may provide the user-facing verification page.

For the full system overview see [docs/architecture.md](../architecture.md).
Core invariant: `agent-git-service` is Git-backed; see [architecture.md § Purpose](../architecture.md#purpose).

## Scope

Owns:

- OAuth device-flow HTTP endpoints and response format
- Device code generation with authenticated user approval
- Headless approve/reject APIs for external consoles
- Authorization code flow with PKCE (S256) and state parameter validation
- Redirect URI validation (same-origin policy)

Does not own:

- token persistence or validation logic (belongs to `service`)
- auth middleware or context injection (belongs to `middleware`)
- user management (belongs to `service`)
- console UI, session UX, or browser rendering beyond the built-in fallback page

## Key Entry Points

| File | Responsibility |
|---|---|
| `handler.go` | `Handler` struct with device-code request, token exchange, approval, rejection, and authorization-code endpoint methods |

The `Handler` struct holds:

| Field | Type | Role |
|---|---|---|
| `Svc` | `*service.Service` | `CreateDeviceCode` for persistence, `ExchangeDeviceCode` for token retrieval |
| `DeviceVerificationURL` | `string` | Optional external console URL returned as the device-flow `verification_uri` |

## Main Flows

### Device Code Request

```
POST /login/device/code
  → RequestDeviceCode handler
  → generate device code (32 hex), user code (8 hex, XXXX-XXXX)
  → store device code in "pending" state
  → Svc.CreateDeviceCode(ctx, deviceCode)
  → respond with { device_code, user_code, verification_uri, verification_uri_complete, expires_in: 900, interval: 5 }
```

If `OAUTH_DEVICE_VERIFICATION_URL` is configured, `verification_uri` points to that external console URL.
Otherwise it falls back to the built-in `/login/device` page on the AGS host.
`verification_uri_complete` appends `user_code` as a query parameter while preserving any existing console URL query parameters.

### Token Exchange (Polling)

```
POST /login/oauth/access_token
  → AccessToken handler
  → accept either:
      → device_code from JSON body or form-encoded body
      → authorization code + code_verifier for PKCE exchange
  → device flow:
      → Svc.ExchangeDeviceCode(ctx, deviceCode)
      → if found and token set: respond { access_token, token_type: "bearer", scope }
      → if found but pending: respond { error: "authorization_pending" }
  → authorization-code flow:
      → Svc.ExchangeAuthorizationCode(ctx, code, code_verifier)
      → validate code_verifier against stored PKCE code_challenge (S256)
      → reject missing/mismatched verifier with { error: "bad_verification_code" }
```

### Device Code Approval (Authenticated)

```
POST /api/ext/v1/oauth/device/approve (requires authentication)
POST /api/ext/v1/oauth/device/reject (requires authentication)
  → ApproveDeviceCode / RejectDeviceCode handler
  → extract user from request context (TokenAuth middleware)
  → if no authenticated user: return 401 Unauthorized
  → accept user_code from JSON or form body
  → lookup device code by user_code
  → Svc.ApproveDeviceCode(ctx, deviceCode, userID, userLogin)
    → validates user exists and is of Type "User"
    → generates access token bound to approving user
    → updates device code state to "approved"
    → creates audit log entry
  → or Svc.RejectDeviceCode(ctx, deviceCode, userID, userLogin, reason)
    → validates user exists, is active, and is of Type "User"
    → updates device code state to "rejected"
    → creates audit log entry
  → respond with { status: "approved" } or { status: "rejected" }
```

The legacy built-in fallback page remains available:

```
GET /login/device (requires authentication)
  → render minimal form
POST /login/device (requires authentication)
  → accept user_code from form body
  → approve the device code
  → respond with success page
```

### Browser Authorization (PKCE Required)

```
GET /login/oauth/authorize?redirect_uri=...&state=...&code_challenge=...&code_challenge_method=S256
  → Authorize handler
  → validate required parameters:
      → state: must be non-empty (CSRF protection)
      → code_challenge: must match PKCE S256 pattern (43-128 chars, URL-safe base64)
      → code_challenge_method: must be "S256"
  → validate redirect_uri is same-origin:
      → parse redirect hostname, compare to request Host
      → allow localhost and 127.0.0.1 as exceptions
      → reject if different host (returns 400)
  → if no redirect_uri: return 200 (no-op)
  → generate authorization code (32 hex)
  → persist authorization code with redirect_uri, expiry, code_challenge, code_challenge_method
     and authenticated user ID when present
  → redirect to redirect_uri?code=...&state=... (preserving state for CSRF validation)
```

### Full gh auth login Flow (Updated Security Model)

```
gh auth login
  → POST /login/device/code → receives device_code + user_code + verification_uri
  → user opens verification_uri in browser (external console when configured, built-in fallback otherwise)
  → authenticated console calls /api/ext/v1/oauth/device/approve or user submits /login/device
  → server validates user is authenticated, approves or rejects device code
  → device code state changes from "pending" to "approved"
  → gh polls POST /login/oauth/access_token with device_code
  → once approved, poll succeeds with access_token
  → gh stores access token for future API calls
```

## Invariants and Design Constraints

- **Authenticated device approval.** The built-in `/login/device` POST endpoint and headless approve/reject APIs require an authenticated user. Device codes are never pre-approved by the request endpoint.
- **Headless console support.** `OAUTH_DEVICE_VERIFICATION_URL` lets deployments point device prompts at an external console. The console calls `/api/ext/v1/oauth/device/approve` or `/api/ext/v1/oauth/device/reject` with an authenticated human token or embedded identity.
- **Token bound to approver.** Access tokens generated via device code approval are bound to the approving user's identity (`DeviceCode.ApprovedBy`, `Token.UserID`). Token exchange validates that the approver exists and is of type "User", preventing privilege escalation via hardcoded admin lookup.
- **PKCE and state required.** The `/login/oauth/authorize` endpoint requires `state` (CSRF protection) and PKCE `code_challenge` with `code_challenge_method=S256`, and `/login/oauth/access_token` validates `code_verifier` against the stored challenge before issuing a token.
- **State echoed in redirect.** The authorization redirect includes the original `state` parameter, enabling the client to validate the response matches the request.
- **Same-origin redirect validation.** The `Authorize` endpoint validates that `redirect_uri` points to the same host as the incoming request, with exceptions for `localhost` and `127.0.0.1`. This prevents open-redirect attacks while supporting local client callbacks.
- **Selective authentication.** `/login/device/code` and `/login/oauth/access_token` remain unauthenticated (they implement the authentication flow itself). `/login/device`, `/api/ext/v1/oauth/device/approve`, `/api/ext/v1/oauth/device/reject`, and `/login/oauth/authorize` require authentication and/or security parameters.
- **Approval only for active users.** Device-code approval and rejection reject users whose status is `banned`, `suspended`, or `deleted`.
- **Device verification is throttled.** `/login/device` and the headless device approve/reject APIs are rate-limited to 5 requests per minute per client.
- **Concrete service dependency.** The handler depends on `*service.Service` directly. This is acceptable because the package is small and the service contract is narrow (`CreateDeviceCode`, `ApproveDeviceCode`, `RejectDeviceCode`, `ExchangeDeviceCode`).
- **Device codes expire after 15 minutes.** The `expires_in` field is set to 900 seconds. Expired codes are not cleaned up proactively; stale records remain in the database.
- **Audit logging.** All device code approval events are logged to `DeviceCodeAuditLog` with the approving user's ID and login for security auditing.

For the full dependency-boundary rules see [module-contracts.md § oauth](../module-contracts.md#oauth).

## Extension and Change Guidance

**Modifying the device flow:**

- The device flow now requires authenticated approval. To modify approval logic, update the OAuth decision handlers in `handler.go` and `ApproveDeviceCode` / `RejectDeviceCode` in `service/auth.go`.
- To add new OAuth grant types, add handler methods to the `Handler` struct and register routes in `internal/router/router.go`. Ensure new endpoints follow the security model (PKCE, state, authentication as appropriate).

**Common patterns:**

- Random hex generation uses `randutil.Hex(n)`.
- Response format follows GitHub's OAuth conventions (not the standard REST error envelope).
- JSON and form-encoded request bodies are both supported for `AccessToken`.
- PKCE code challenge validation uses regex pattern `^[A-Za-z0-9_-]{43,128}$` for S256.

## Related Tests

- `internal/oauth/handler_test.go` — covers device code generation, token exchange (JSON and form body), authorization with redirect validation, and error cases
- `internal/router/router_test.go` — covers the authenticated headless approve/reject routes through real router wiring

For the phased test roadmap see [docs/test-strategy.md](../test-strategy.md).

## Related Docs

- [docs/architecture.md](../architecture.md) — system overview, authentication model
- [docs/module-contracts.md](../module-contracts.md) § oauth — dependency rules
- [Service Layer](service.md) — token validation and device code persistence
