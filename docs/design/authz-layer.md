# Authorization Layer Design

## Overview

This document describes the authorization layer design for the agent-git-service, specifically focusing on repository-level permission checks for sensitive operations.

## Security Context

Repository-scoped mutations must verify both authentication and repository
permission. A caller who can name a repository or alert ID must not be able to
modify or infer protected resources without the required access level.

## Permission Model

The service uses a GitHub-compatible permission model with the following levels:

- **None**: No access
- **Read**: Can view repository content and issues
- **Triage**: Can manage issues and PRs (maps to Read for authorization decisions)
- **Write**: Can push code, manage issues/PRs, and modify repository settings
- **Maintain**: Can manage repository without push access (maps to Write for authorization decisions)
- **Admin**: Full control including deletion and permission management

### Permission Decision Mapping

For authorization decisions, permissions are collapsed:
- `triage` → `read`
- `maintain` → `write`

This simplifies authorization logic while maintaining GitHub compatibility.

## Authorization Check Pattern

### Service Layer

The `Service` type provides `HasRepoAccess(ctx, repoID, userID)` which returns the effective `RepoPermission` for a user on a repository. Permission precedence:

1. Site admin (always has admin access)
2. Repository owner (always has admin access)
3. Maximum of:
   - Organization base permission
   - Direct collaborator grant
   - Team-based grants

### Usage in Handlers

For mutations that modify repository state (e.g., dismissing Dependabot alerts, creating PRs, pinning comments), handlers MUST verify write permission:

```go
// Example: Dependabot alert dismissal
u, err := s.Svc.GetCurrentUser(ctx)
if err != nil {
    return errResp("unauthorized")
}

perm, err := s.Svc.HasRepoAccess(ctx, alert.RepositoryID, u.ID)
if err != nil || !perm.AtLeast(service.RepoPermissionWrite) {
    return errResp("vulnerability alert not found")
}

// Proceed with mutation...
```

This "not found" response is intentional. The helper hides whether the alert exists from callers who do not have write access, avoiding information leakage.

### Read Operations

For read-only operations, the `requireRepoPermission` helper in the REST handlers still requires authentication and returns 404 when the viewer is anonymous or lacks the required repository permission:

```go
if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionRead) {
    return // Handler returns 404 for unauthorized access
}
```

This allows:
- Authenticated users with sufficient access to read the repository
- Proper 404 responses (vs 403) to avoid leaking repository existence

## Security Considerations

### Defense in Depth

1. **Always check permissions at the service layer**: Even if middleware provides some authorization, service methods should verify permissions for sensitive operations.

2. **Fail closed**: When permission checks fail or error, deny access rather than allowing the operation.

3. **Use AtLeast() for comparisons**: The `RepoPermission.AtLeast()` method correctly handles the permission hierarchy and decision mapping.

### Common Pitfalls

1. **Checking only authentication**: Verifying a user is logged in is not sufficient; their permission level on the specific repository must be verified.

2. **Assuming repo ID implies access**: Just because a user can reference a repository ID doesn't mean they have permission to modify it.

3. **Skipping checks for "internal" operations**: All user-triggered operations should verify permissions, regardless of how they're invoked.

## Implementation Locations

- Permission model: `internal/service/permission.go`
- Access checking: `internal/service/repo_access.go`
- REST helper: `internal/rest/handlers.go`
- Example usage: `internal/graphql/gql_mut_dependabot.go`, `internal/service/comment.go`

## Testing

Permission checks should be tested with:
- Repository owner (should succeed)
- Collaborator with write permission (should succeed)
- Collaborator with read permission (should fail for write operations)
- Organization member with base permission (should succeed/fail based on org settings)
- User with no relationship to repository (should fail)
- Anonymous user on public repo (should fail)
- Anonymous user on private repo (should fail)

## Future Improvements

1. **Audit logging**: Permission-denied responses should be logged with enough
   context for security monitoring without leaking protected resource details.

2. **Fine-grained permissions**: Consider adding support for more granular
   permissions, such as alert-specific mutation rights, if a future API surface
   needs them.

## References

- `internal/service/permission.go`: Permission model implementation
- `internal/service/repo_access.go`: Access checking implementation
