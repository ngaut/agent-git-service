# Collaboration Framework

This document records the current implemented collaboration framework for `agent-git-service`.
It is the source of truth for organization membership, teams, repository permissions, organization invitations, and outside collaborators.

This is a current-state document, not a rollout plan.
When older design notes differ from the implementation, this document and the current code win.

This document consolidates the collaboration state that is currently spread across:

- [architecture.md](../architecture.md)
- [module-contracts.md](../module-contracts.md)
- [test-strategy.md](../test-strategy.md)
- the implementation anchors listed at the end of this document

## Purpose and Scope

The collaboration model is intentionally narrow and agent-first:

- organizations are explicit governance boundaries
- teams are authorization groups inside an organization
- repository access resolves from a small canonical permission model
- org ingress is explicit through org creation or invitation acceptance
- GitHub-compatible API shapes are preserved where useful, but the runtime model does not expand into broader human-collaboration features

Out of scope for this framework:

- nested teams
- team visibility semantics beyond compatibility serialization
- review-routing, notification, or social-collaboration policy
- introducing `triage` or `maintain` as first-class runtime permissions

## Current Model Overview

The implemented collaboration model is built from these rules:

1. An organization is an explicit `User` row with `Type="Organization"`.
2. Organization membership is explicit and is stored separately from team membership.
3. Teams are org-scoped authorization groups. They are always persisted as `closed`.
4. Repository access is the maximum of org base permission, direct collaborator permission, and team-granted permission, after site-admin and owner shortcuts.
5. Runtime authorization decisions use the minimal set `read`, `write`, and `admin`.
6. GitHub-compatible aliases such as `pull`, `triage`, `push`, and `maintain` are accepted at the API boundary, then normalized onto the runtime set.

## Core Entities

### Organization

- Implemented as `User{Type="Organization"}`.
- Created explicitly through `CreateOrg` and `POST /api/v3/user/orgs`.
- Carries `DefaultRepositoryPermission`, which is the org-wide base repository permission for explicit org members.
- `GET /api/v3/orgs/{org}` only resolves existing org accounts; it does not auto-create them.

### OrganizationMember

- `OrganizationMember` is the authoritative org membership table.
- Role is `owner` or `member`.
- User org listing is driven by `OrganizationMember`, not by inferred team joins.
- Team membership depends on this table; joining a team requires existing org membership.

### Team

- `Team` is an org-scoped authorization group.
- `Privacy` is kept only for GitHub REST compatibility and is normalized to `closed`.
- Teams are not the ownership primitive for organizations and are not used to infer org membership.
- The implementation also uses a system-managed `admins` team to keep org-owner/admin repo governance aligned, but that is a support mechanism rather than a separate product concept.

### TeamMember

- `TeamMember` is the authoritative per-team membership table.
- Role is `member` or `maintainer`.
- Team role is independent from org role.
- Adding a team member fails unless the user is already an org member.

### TeamRepository

- `TeamRepository` grants a repository to a team.
- Stored permission is canonicalized to `read`, `write`, or `admin`.
- Team grants participate in the same effective-permission resolution as direct collaborators and org base grants.

### OrganizationInvitation

- `OrganizationInvitation` stores a pending invite for one user into one organization.
- It includes:
  - invited org role (`direct_member` or `admin`)
  - invited team IDs
  - inviter
  - expiry
- Team IDs are stored as normalized JSON in the invitation row.

### OutsideCollaborator

- `OutsideCollaborator` tracks a non-member user who still has direct collaborator access to at least one org-owned repository.
- It is an org-level derived record, not an independent grant source.
- It exists only for direct repo collaborator access; org membership and team access do not create outside-collaborator state.

## Effective Repository Permission

`service.HasRepoAccess` is the canonical permission decision point.
It is reused by REST authorization, GraphQL viewer permission, viewer-repository listing, and Git Smart HTTP.

Permission precedence is:

1. site admin -> `admin`
2. repo owner -> `admin`
3. otherwise `max(org default permission, direct collaborator grant, team grant)`

Runtime decision levels are:

- `none`
- `read`
- `write`
- `admin`

Compatibility handling at the API boundary is:

- `pull` and `triage` -> `read`
- `push` and `maintain` -> `write`
- `admin` -> `admin`
- org base permission also accepts `none`

The important constraint is that `triage` and `maintain` remain transport-compatibility aliases.
They are not separate governance concepts in the runtime model.

## Organization, Team, and Repo Semantics

### Explicit Org Ingress

- Organizations are created explicitly.
- Org-targeted governance does not rely on implicit org creation.
- Team membership does not auto-bootstrap org membership.
- Org-targeted repo create, fork, transfer, and team-sharing enablement require pre-existing org governance.
- These flows do not auto-bootstrap org membership or ownership.

### Org Membership Versus Team Membership

- Org membership answers “is this user part of the organization?”
- Team membership answers “which authorization group inside the organization is this user in?”
- Org admin checks are based on org ownership (`OrganizationMember.Role == owner`) or site-admin privilege.
- Team maintainer is scoped to team management and does not replace org ownership.

### Repo Governance Inside Orgs

- Org members can receive a base repo permission through the org account’s `DefaultRepositoryPermission`.
- Teams can raise access on selected repos through `TeamRepository`.
- Direct collaborators still exist and can coexist with org and team grants.
- Current org-governance flows require existing org admin privileges instead of auto-creating org owners or members as side effects.

### System-Managed Admin Alignment

The implementation keeps org-level admin governance aligned through a system-managed `admins` team:

- org creation bootstraps the creator as org owner and `admins` team maintainer
- when the acting principal is an agent with a bound human, bootstrap includes the bound human as well
- org-owned repositories are reconciled so the `admins` team has `admin` access

This is an implementation mechanism that keeps agent-first governance consistent.
It should not be treated as a broader team-product feature.

## Invitation Lifecycle

Pending org invitations are the explicit path for adding a user into an organization without immediate membership.

Current behavior:

- an org admin creates or refreshes an invitation for a specific invitee
- the invitation stores the target invitation role, invited team IDs, inviter, and expiry
- invitation role mapping on accept:
  - `direct_member` -> org membership role `member`
  - `admin` -> org membership role `owner` (membership API role `admin`)
- expiry defaults to 7 days when omitted
- accepting the invitation:
  - creates or promotes the org membership
  - joins the invited teams as `member`
  - deletes the pending invitation row
- declining removes the pending invitation for the invitee
- revoking removes the pending invitation from the org side

Invitation acceptance is also a membership transition point:

- once a user becomes an org member, any `OutsideCollaborator` row for that org is removed

## Outside Collaborators

Outside collaborators are deliberately narrow:

- they are users who are not org members
- they have direct collaborator access to at least one org-owned repo
- they are tracked at org scope for listing and annotation

The service reconciles this derived state when direct collaborator access changes, including:

- collaborator invitation acceptance
- collaborator removal
- org invitation acceptance or other org-membership creation
- repo deletion
- repo transfer into or out of an organization

If a user loses all direct repo access in the org, or becomes an org member, the outside-collaborator row is removed.

## Agent-First Simplifications

The current framework intentionally converges on a smaller model than the earlier design exploration:

- teams are authorization groups, not social or visibility objects
- team privacy is fixed to `closed` for runtime behavior
- runtime repo permission is only `read`, `write`, or `admin`
- org membership is explicit and never inferred from team joins
- org ingress is explicit rather than side-effect driven
- compatibility aliases remain accepted so GitHub-facing clients keep working

This keeps the implementation aligned with the actual product need: controlled repo sharing and governance for org-owned repositories, especially in agent-driven workflows.

## Surface Mapping

The framework is reflected across the main server surfaces:

- REST:
  - explicit org create/list
  - org invitation create/list/accept/decline/revoke
  - team CRUD, team membership, and team-repo grants
  - org outside collaborator listing
- GraphQL:
  - repository viewer permission resolves through the same service-level access model
- Git Smart HTTP:
  - clone/fetch requires read access
  - push and receive-pack advertisement require write access

The transport layers keep GitHub-compatible response shapes where needed, but permission truth stays in `internal/service`.

## Relationship to Other Docs

- [architecture.md](../architecture.md) records the system-wide architecture, routing model, and current collaboration endpoints.
- [module-contracts.md](../module-contracts.md) records that `service` owns collaboration policy and effective permission resolution, while REST and GraphQL stay transport-focused.
- [test-strategy.md](../test-strategy.md) records the expected regression coverage for org creation, invitations, team grants, effective-permission precedence, and outside-collaborator reconciliation.

## Implementation Anchors

The main implementation anchors for this document are:

- `internal/service/permission.go`
- `internal/service/repo_access.go`
- `internal/service/user.go`
- `internal/service/team.go`
- `internal/service/org_membership.go`
- `internal/service/org_invitation.go`
- `internal/service/outside_collaborator.go`
