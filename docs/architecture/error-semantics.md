# API Error Semantics Contract

This document defines the GitHub-compatible error semantics contract for `agent-git-service`.
All REST, GraphQL, and bootstrap/discovery endpoints MUST follow this contract to ensure
client compatibility and predictable error handling.

## Overview

The error semantics contract ensures that:
- Clients can reliably distinguish between different error classes
- Domain errors never fall through to generic 500 responses
- Error response shapes are consistent with GitHub's API
- Bootstrap and discovery paths are resilient to missing resources

## Canonical Error Mapping

All errors MUST be mapped to the following HTTP status codes:

| Status | Error Class | Sentinel | GitHub Message | When to Use |
|--------|-------------|----------|----------------|-------------|
| 401 | Unauthorized | `ErrUnauthorized` | "Bad credentials" | Missing or invalid authentication token |
| 403 | Forbidden | `ErrForbidden` | "Resource not accessible by integration" | Authenticated user lacks permission |
| 404 | Not Found | `ErrNotFound` | "Not Found" | Requested resource does not exist |
| 409 | Conflict | `ErrConflict` | "conflict" | Duplicate resource or conflicting state |
| 422 | Unprocessable Entity | `ErrValidation`, `ErrInvalidState` | "validation failed" / "invalid state" | Input validation failure or invalid operation for current state |
| 429 | Too Many Requests | `ErrRateLimited` | "You have exceeded a secondary rate limit" | Rate limit exceeded |
| 500 | Internal Server Error | (any unmapped error) | "Internal Server Error" | Unexpected backend errors only |

## Error Response Shape

### REST API (v3)

All REST error responses MUST follow this shape:

```json
{
  "message": "Error message string",
  "documentation_url": "https://docs.github.com/rest"
}
```

**Requirements:**
- Content-Type MUST be `application/json; charset=utf-8`
- X-GitHub-Media-Type header MUST be `github.v3; format=json`
- No extra fields beyond `message` and `documentation_url`
- Message text MUST match GitHub's conventions (see table above)

### GraphQL API (v4)

GraphQL errors MUST follow the GraphQL specification:

```json
{
  "errors": [
    {
      "message": "Error message string"
    }
  ],
  "data": null
}
```

**Requirements:**
- Errors array MUST contain at least one error object
- Each error MUST have a `message` field
- For domain errors (not-found, validation), return errors array rather than null data where possible
- Authentication/authorization errors should be surfaced in the errors array

### Bootstrap/Discovery Endpoints

Discovery endpoints (`/api/v3/`, `/api/v3/meta`, `/api/v3/rate_limit`) MUST:
- Always return 200 OK for valid requests
- Never require authentication
- Return GitHub-compatible JSON shapes
- Handle missing optional data gracefully (return empty arrays/objects, not errors)

## Implementation Guidelines

### Service Layer

1. **Use sentinel errors**: Always wrap domain errors with appropriate sentinels from `internal/apperrors`
   ```go
   if err != nil {
       return fmt.Errorf("get repo: %w", apperrors.ErrNotFound)
   }
   ```

2. **Convert ORM errors**: Convert GORM's `ErrRecordNotFound` to `apperrors.ErrNotFound`
   ```go
   if errors.Is(err, gorm.ErrRecordNotFound) {
       return apperrors.ErrNotFound
   }
   ```

3. **Preserve context**: Wrap sentinels with context, don't replace them
   ```go
   // Good
   return fmt.Errorf("create issue: %w", apperrors.ErrValidation)
   
   // Bad - loses sentinel
   return errors.New("create issue failed")
   ```

### Handler Layer

1. **Use respond.ServiceError**: Never manually map errors to status codes
   ```go
   if err != nil {
       respond.ServiceError(w, err)
       return
   }
   ```

2. **Validate early**: Check required parameters before calling service layer
   ```go
   if input.Title == "" {
       respond.ValidationFailed(w, "title is required")
       return
   }
   ```

3. **Don't swallow errors**: Log unexpected errors before returning 500
   ```go
   slog.Error("unexpected error", "error", err)
   respond.Error(w, http.StatusInternalServerError, "Internal Server Error")
   ```

### GraphQL Mutations

1. **Use errResp helper**: For mutation errors, use the `errResp` helper
   ```go
   if err != nil {
       return errResp(err.Error())
   }
   ```

2. **Check auth in mutations**: Validate authentication before service calls
   ```go
   u, err := s.Svc.GetCurrentUser(ctx)
   if err != nil {
       return errResp("authentication required")
   }
   ```

3. **Return meaningful messages**: Error messages should help clients understand the failure
   ```go
   if fullName == "" {
       return errResp("repo not found")
   }
   ```

## Anti-Patterns to Avoid

### ❌ Domain errors falling through to 500

```go
// Bad - generic error loses domain context
repo, err := s.Svc.GetRepo(ctx, fullName)
if err != nil {
    return err // Could be 500 if not wrapped properly
}
```

```go
// Good - explicit error mapping
repo, err := s.Svc.GetRepo(ctx, fullName)
if err != nil {
    return fmt.Errorf("get repo: %w", apperrors.ErrNotFound)
}
```

### ❌ Inconsistent error messages

```go
// Bad - varies by handler
respond.Error(w, 404, "repo not found")
respond.Error(w, 404, "Repository does not exist")
respond.Error(w, 404, "Not Found") // Correct
```

### ❌ Manual status code mapping

```go
// Bad - duplicates logic, error-prone
if errors.Is(err, apperrors.ErrNotFound) {
    w.WriteHeader(404)
} else if errors.Is(err, apperrors.ErrUnauthorized) {
    w.WriteHeader(401)
}
```

```go
// Good - single source of truth
respond.ServiceError(w, err)
```

### ❌ Extra fields in error responses

```go
// Bad - breaks strict client unmarshaling
JSON(w, 404, map[string]any{
    "message": "Not Found",
    "error": "not_found",  // Extra field
    "path": "/api/v3/...", // Extra field
})
```

## Testing Requirements

All new endpoints MUST include regression tests for:

1. **Not-found path**: Request non-existent resource, verify 404
2. **Validation path**: Send invalid input, verify 422
3. **Auth path**: Request without token, verify 401
4. **Rate-limit path**: (if applicable) Verify 429 under load

Example test structure:

```go
func TestHandler_NotFound(t *testing.T) {
    // Setup
    req := httptest.NewRequest("GET", "/api/v3/repos/nonexistent/repo", nil)
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
    
    // Assert
    if w.Code != http.StatusNotFound {
        t.Errorf("expected 404, got %d", w.Code)
    }
    // Verify error shape
    var body map[string]any
    json.NewDecoder(w.Body).Decode(&body)
    if body["message"] != "Not Found" {
        t.Errorf("expected 'Not Found', got %v", body["message"])
    }
}
```

## Related Documents

- [Architecture Overview](../architecture.md)
- [REST API Design](rest.md)
- [GraphQL API Design](graphql.md)
- [Service Layer Design](service.md)
