# Mock Auth0 Server for E2E Tests

This is a mock Auth0 server for testing Auth0 error-state contracts and browser
`id_token` claim flows in E2E tests.

## Usage

Start the mock server:

```bash
go run ./e2e/cmd/mock-auth0-server/main.go :8891
```

## Admin Endpoints

### GET /__admin/state

Returns the current mock state:

```bash
curl http://localhost:8891/__admin/state
```

### POST /__admin/reset

Resets the mock to default success mode:

```bash
curl -X POST http://localhost:8891/__admin/reset
```

### POST /__admin/mode

Configures the mock to return specific error responses:

```bash
# Set mode to authorization_pending
curl -X POST "http://localhost:8891/__admin/mode?mode=authorization_pending&fail_count=3&success_once=true"

# Set mode to slow_down
curl -X POST "http://localhost:8891/__admin/mode?mode=slow_down"

# Set mode to expired_token
curl -X POST "http://localhost:8891/__admin/mode?mode=expired_token"

# Set mode to access_denied
curl -X POST "http://localhost:8891/__admin/mode?mode=access_denied"

# Set mode to generic OAuth error
curl -X POST "http://localhost:8891/__admin/mode?mode=oauth_error"

# Set mode to success (default)
curl -X POST "http://localhost:8891/__admin/mode?mode=success"
```

Query parameters:
- `mode`: One of `authorization_pending`, `slow_down`, `expired_token`, `access_denied`, `oauth_error`, `success`
- `fail_count`: (optional) Number of times to return error before success (for `authorization_pending`)
- `success_once`: (optional) If `true`, switch to success mode after first successful exchange

## OAuth Endpoints

### POST /oauth/device/code

Returns a mock device code:

```bash
curl -X POST http://localhost:8891/oauth/device/code
```

Response:
```json
{
  "device_code": "mock-device-code-123",
  "user_code": "MOCK-123",
  "verification_uri": "https://mock.auth0.example.com/activate",
  "verification_uri_complete": "https://mock.auth0.example.com/activate?code=MOCK-123",
  "expires_in": 900,
  "interval": 5
}
```

### POST /oauth/token

Exchanges device code for tokens. Response depends on configured mode:

- `authorization_pending`: Returns 400 with `{"error": "authorization_pending"}`
- `slow_down`: Returns 400 with `{"error": "slow_down"}`
- `expired_token`: Returns 400 with `{"error": "expired_token"}`
- `access_denied`: Returns 400 with `{"error": "access_denied"}`
- `oauth_error`: Returns 400 with `{"error": "invalid_grant"}`
- `success`: Returns 200 with mock tokens, including an `RS256`-signed `id_token`
  whose `iss` matches the request host and whose `aud` matches the submitted
  `client_id` (or `test-client-id` if omitted)

### GET /.well-known/jwks.json

Returns the JWKS that matches the mock server's signing key so `gh-server` can
verify the signed `id_token`.

## Running E2E Tests with Mock Auth0

1. Start the mock Auth0 server:
   ```bash
   go run ./e2e/cmd/mock-auth0-server/main.go :8891
   ```

2. Start gh-server with mock Auth0 configuration:
   ```bash
   AUTH0_ISSUER=http://localhost:8891/ \
   AUTH0_CLIENT_ID=test-client-id \
   make run-bg
   ```

3. Run the E2E tests:
   ```bash
   make test-e2e
   ```

Use this mock server for Auth0-related manual validation as needed.
