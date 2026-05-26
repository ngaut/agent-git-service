package rest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/service"
)

// ListOrgTeams handles GET /api/v3/orgs/{org}/teams
func (d *Deps) ListOrgTeams(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}

	teams, err := d.Svc.ListOrgTeams(r.Context(), org.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	page, perPage := parsePagination(r)
	paged := paginate(w, r, d.Svc.BaseURL, teams, page, perPage)
	out := make([]any, len(paged))
	for i, t := range paged {
		t.Organization = *org // Hydrate the org for serialization
		out[i] = transform.Team(t)
	}

	if len(out) == 0 {
		out = []any{}
	}
	respond.JSON(w, 200, out)
}

// ListPendingTeamInvitations handles GET /api/v3/orgs/{org}/teams/{team_slug}/invitations
func (d *Deps) ListPendingTeamInvitations(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	slug := pathParam(r, "team_slug")

	team, err := d.Svc.GetTeam(r.Context(), org.ID, slug)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	viewer, ok := service.UserFromContext(r.Context())
	if !ok || viewer.ID == 0 {
		respond.Error(w, http.StatusUnauthorized, "Requires authentication")
		return
	}

	canManage, _, err := d.Svc.CanManageTeamMembership(r.Context(), org.ID, team.ID, viewer.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if !canManage {
		respond.ServiceErrorRequest(r, w, service.ErrForbidden)
		return
	}

	invitations, err := d.Svc.ListPendingTeamInvitations(r.Context(), org.ID, team.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	out := make([]any, 0, len(invitations))
	for _, inv := range invitations {
		out = append(out, teamPendingInvitationResponse(d.Svc.BaseURL, org.Login, inv))
	}
	if out == nil {
		out = []any{}
	}
	respond.JSON(w, http.StatusOK, out)
}

// CreateTeam handles POST /api/v3/orgs/{org}/teams
func (d *Deps) CreateTeam(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	if !d.requireOrgAdmin(w, r, org) {
		return
	}

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		// Privacy is accepted for GitHub API compatibility, but teams remain
		// authorization groups only and are normalized to closed server-side.
		Privacy string `json:"privacy"`
	}
	if err := decodeBodyStrict(r, &req); err != nil {
		respond.ValidationFailed(w, "invalid JSON")
		return
	}

	if req.Name == "" {
		respond.ValidationFailed(w, "name is required")
		return
	}

	// For simplicity, generate slug natively or use name if no spaces. Here we'll just use the name for the slug.
	slug := req.Name
	team, err := d.Svc.CreateTeam(r.Context(), org.ID, req.Name, slug, req.Description, req.Privacy)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	team.Organization = *org

	respond.JSON(w, 201, transform.Team(team))
}

// GetTeam handles GET /api/v3/orgs/{org}/teams/{team_slug}
func (d *Deps) GetTeam(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	slug := pathParam(r, "team_slug")

	team, err := d.Svc.GetTeam(r.Context(), org.ID, slug)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	team.Organization = *org

	respond.JSON(w, 200, transform.Team(team))
}

// UpdateTeam handles PATCH /api/v3/orgs/{org}/teams/{team_slug}
func (d *Deps) UpdateTeam(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	if !d.requireOrgAdmin(w, r, org) {
		return
	}
	slug := pathParam(r, "team_slug")

	team, err := d.Svc.GetTeam(r.Context(), org.ID, slug)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	team.Organization = *org

	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	decodeBody(r, &req)

	if req.Name != nil {
		team.Name = *req.Name
		// Service layer will update slug when name changes
	}
	if req.Description != nil {
		team.Description = *req.Description
	}

	if err := d.Svc.UpdateTeam(r.Context(), &team); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	respond.JSON(w, 200, transform.Team(team))
}

// DeleteTeam handles DELETE /api/v3/orgs/{org}/teams/{team_slug}
func (d *Deps) DeleteTeam(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	if !d.requireOrgAdmin(w, r, org) {
		return
	}
	slug := pathParam(r, "team_slug")

	team, err := d.Svc.GetTeam(r.Context(), org.ID, slug)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	if err := d.Svc.DeleteTeam(r.Context(), team.ID); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	respond.NoContent(w)
}

func (d *Deps) requireOrgAdmin(w http.ResponseWriter, r *http.Request, org *db.User) bool {
	if org == nil {
		respond.ServiceErrorRequest(r, w, service.ErrNotFound)
		return false
	}

	viewer, ok := service.UserFromContext(r.Context())
	if !ok || viewer.ID == 0 {
		respond.Forbidden(w, "org admin access required")
		return false
	}

	allowed, err := d.Svc.IsOrgAdmin(r.Context(), org.ID, viewer.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return false
	}
	if !allowed {
		respond.Forbidden(w, "org admin access required")
		return false
	}
	return true
}

func (d *Deps) canReadOrgMembers(r *http.Request, org *db.User) (bool, error) {
	if org == nil {
		return false, service.ErrNotFound
	}

	viewer, ok := service.UserFromContext(r.Context())
	if !ok || viewer.ID == 0 {
		return false, nil
	}
	if viewer.SiteAdmin {
		return true, nil
	}
	return d.Svc.IsOrgMember(r.Context(), org.ID, viewer.ID)
}

func organizationMembershipJSON(baseURL string, org db.User, membership service.OrganizationMembershipView) map[string]any {
	orgLogin := strings.TrimSpace(org.Login)
	userLogin := strings.TrimSpace(membership.User.Login)
	return map[string]any{
		"url":          fmt.Sprintf("%s/api/v3/orgs/%s/memberships/%s", baseURL, url.PathEscape(orgLogin), url.PathEscape(userLogin)),
		"state":        membership.State,
		"role":         membership.Role,
		"organization": transform.User(org),
		"user":         transform.User(membership.User),
	}
}

// ListOrgMembers handles GET /api/v3/orgs/{org}/members.
func (d *Deps) ListOrgMembers(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	if filter := strings.TrimSpace(r.URL.Query().Get("filter")); filter != "" && filter != "all" {
		respond.ValidationFailed(w, "filter must be one of: all")
		return
	}

	allowed, err := d.canReadOrgMembers(r, org)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if !allowed {
		respond.JSON(w, http.StatusOK, []any{})
		return
	}

	members, err := d.Svc.ListOrgMembers(r.Context(), org.ID, r.URL.Query().Get("role"))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	page, perPage := parsePagination(r)
	paged := paginate(w, r, d.Svc.BaseURL, members, page, perPage)

	out := make([]any, len(paged))
	for i, member := range paged {
		out[i] = transform.User(member)
	}
	if len(out) == 0 {
		out = []any{}
	}
	respond.JSON(w, http.StatusOK, out)
}

// DeleteOrgMember handles DELETE /api/v3/orgs/{org}/members/{username}.
func (d *Deps) DeleteOrgMember(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	if !d.requireOrgAdmin(w, r, org) {
		return
	}

	username := pathParam(r, "username")
	if err := d.Svc.RemoveOrgMember(r.Context(), org.ID, username); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// SetOrgMembership handles PUT /api/v3/orgs/{org}/memberships/{username}.
func (d *Deps) SetOrgMembership(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	if !d.requireOrgAdmin(w, r, org) {
		return
	}

	viewer, ok := service.UserFromContext(r.Context())
	if !ok || viewer.ID == 0 {
		respond.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	username := pathParam(r, "username")
	role := strings.TrimSpace(requestBodyValue(r, "role"))
	membership, err := d.Svc.SetOrgMembership(r.Context(), org.ID, username, role, viewer.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	respond.JSON(w, http.StatusOK, organizationMembershipJSON(d.Svc.BaseURL, *org, membership))
}

// GetOrgMembership handles GET /api/v3/orgs/{org}/memberships/{username}.
func (d *Deps) GetOrgMembership(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}

	allowed, err := d.canReadOrgMembers(r, org)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if !allowed {
		respond.Forbidden(w, "org membership access required")
		return
	}

	username := pathParam(r, "username")
	membership, err := d.Svc.GetOrgMembership(r.Context(), org.ID, username)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusOK, organizationMembershipJSON(d.Svc.BaseURL, *org, membership))
}

// DeleteOrgMembership handles DELETE /api/v3/orgs/{org}/memberships/{username}.
func (d *Deps) DeleteOrgMembership(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	if !d.requireOrgAdmin(w, r, org) {
		return
	}

	username := pathParam(r, "username")
	if err := d.Svc.RemoveOrgMembership(r.Context(), org.ID, username); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// ListTeamMembers handles GET /api/v3/orgs/{org}/teams/{team_slug}/members
func (d *Deps) ListTeamMembers(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	slug := pathParam(r, "team_slug")

	team, err := d.Svc.GetTeam(r.Context(), org.ID, slug)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	members, err := d.Svc.ListTeamMembers(r.Context(), team.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	page, perPage := parsePagination(r)
	paged := paginate(w, r, d.Svc.BaseURL, members, page, perPage)

	out := make([]any, len(paged))
	for i, u := range paged {
		out[i] = transform.User(u)
	}
	if len(out) == 0 {
		out = []any{}
	}

	respond.JSON(w, 200, out)
}

// AddTeamMember handles PUT /api/v3/orgs/{org}/teams/{team_slug}/memberships/{username}
func (d *Deps) AddTeamMember(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	slug := pathParam(r, "team_slug")
	username := pathParam(r, "username")

	role := strings.TrimSpace(requestBodyValue(r, "role"))
	role, ok := normalizeTeamMemberRole(role)
	if !ok {
		respond.ValidationFailed(w, "role must be member or maintainer")
		return
	}

	team, err := d.Svc.GetTeam(r.Context(), org.ID, slug)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	user, err := d.Svc.GetUser(r.Context(), username)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	viewer, ok := service.UserFromContext(r.Context())
	if !ok || viewer.ID == 0 {
		respond.Error(w, http.StatusUnauthorized, "Requires authentication")
		return
	}

	membership, err := d.Svc.AddOrInviteTeamMember(r.Context(), org.ID, team.ID, user.ID, viewer.ID, role)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	// GitHub API returns 200 with active or pending membership state.
	respond.JSON(w, 200, teamMembershipResponse(d.Svc.BaseURL, org.Login, slug, username, membership.Role, membership.State))
}

// RemoveTeamMember handles DELETE /api/v3/orgs/{org}/teams/{team_slug}/memberships/{username}
func (d *Deps) RemoveTeamMember(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	slug := pathParam(r, "team_slug")
	username := pathParam(r, "username")

	team, err := d.Svc.GetTeam(r.Context(), org.ID, slug)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	user, err := d.Svc.GetUser(r.Context(), username)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	viewer, ok := service.UserFromContext(r.Context())
	if !ok || viewer.ID == 0 {
		respond.Error(w, http.StatusUnauthorized, "Requires authentication")
		return
	}

	canManage, _, err := d.Svc.CanManageTeamMembership(r.Context(), org.ID, team.ID, viewer.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if !canManage {
		respond.ServiceErrorRequest(r, w, service.ErrForbidden)
		return
	}

	if err := d.Svc.RemoveTeamMember(r.Context(), team.ID, user.ID); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	respond.NoContent(w)
}

// GetTeamMembership handles GET /api/v3/orgs/{org}/teams/{team_slug}/memberships/{username}
func (d *Deps) GetTeamMembership(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	slug := pathParam(r, "team_slug")
	username := pathParam(r, "username")

	team, err := d.Svc.GetTeam(r.Context(), org.ID, slug)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	user, err := d.Svc.GetUser(r.Context(), username)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	member, err := d.Svc.GetTeamMembershipState(r.Context(), org.ID, team.ID, user.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	respond.JSON(w, 200, teamMembershipResponse(d.Svc.BaseURL, org.Login, slug, username, member.Role, member.State))
}

// AddTeamRepo handles PUT /api/v3/orgs/{org}/teams/{team_slug}/repos/{owner}/{repo}
func (d *Deps) AddTeamRepo(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	slug := pathParam(r, "team_slug")

	team, err := d.Svc.GetTeam(r.Context(), org.ID, slug)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	fullName := repoFullName(r)
	repo, err := d.Svc.GetRepo(r.Context(), fullName)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if !canMutateTeamRepo(w, r, org, &repo, d) {
		return
	}

	permission := strings.TrimSpace(requestBodyValue(r, "permission"))
	internalPerm, ok := normalizeTeamRepoPermission(permission)
	if !ok {
		respond.ValidationFailed(w, service.GrantPermissionValidationMessage)
		return
	}

	if err := d.Svc.AddTeamRepo(r.Context(), team.ID, repo.ID, internalPerm); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	respond.NoContent(w)
}

// ListTeamRepos handles GET /api/v3/orgs/{org}/teams/{team_slug}/repos
func (d *Deps) ListTeamRepos(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	slug := pathParam(r, "team_slug")

	team, err := d.Svc.GetTeam(r.Context(), org.ID, slug)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	repos, err := d.Svc.ListTeamRepos(r.Context(), team.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	page, perPage := parsePagination(r)
	paged := paginate(w, r, d.Svc.BaseURL, repos, page, perPage)

	out := make([]any, len(paged))
	for i, tr := range paged {
		repoObj := transform.Repo(tr.Repository)
		repoObj["permissions"] = teamRepoPermissions(tr.Permission)
		repoObj["role_name"] = teamRepoRoleName(tr.Permission)
		out[i] = repoObj
	}
	if len(out) == 0 {
		out = []any{}
	}

	respond.JSON(w, 200, out)
}

// RemoveTeamRepo handles DELETE /api/v3/orgs/{org}/teams/{team_slug}/repos/{owner}/{repo}
func (d *Deps) RemoveTeamRepo(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	slug := pathParam(r, "team_slug")

	team, err := d.Svc.GetTeam(r.Context(), org.ID, slug)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	fullName := repoFullName(r)
	repo, err := d.Svc.GetRepo(r.Context(), fullName)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if !canMutateTeamRepo(w, r, org, &repo, d) {
		return
	}

	if err := d.Svc.RemoveTeamRepo(r.Context(), team.ID, repo.ID); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	respond.NoContent(w)
}

// EnableRepoTeamSharing handles POST /api/v3/repos/{owner}/{repo}/team-sharing/enable
func (d *Deps) EnableRepoTeamSharing(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}

	viewer, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.Unauthorized(w, "Bad credentials")
		return
	}
	if !canMutateTeamRepo(w, r, &repo.Owner, repo, d) {
		return
	}

	team, err := d.Svc.EnableRepoTeamSharing(r.Context(), repo.ID, viewer.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if team.Organization.ID == 0 {
		team.Organization = repo.Owner
	}

	respond.JSON(w, 200, transform.Team(team))
}

func normalizeTeamMemberRole(role string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", "member":
		return "member", true
	case "maintainer":
		return "maintainer", true
	default:
		return "", false
	}
}

func normalizeTeamRepoPermission(permission string) (string, bool) {
	return service.NormalizeGrantPermission(permission)
}

func teamRepoPermissions(permission string) map[string]any {
	return repoPermissionMapFor(service.ParseRepoPermission(permission))
}

func teamRepoRoleName(permission string) string {
	return service.ParseRepoPermission(permission).String()
}

func teamMembershipResponse(baseURL, orgLogin, teamSlug, username, role, state string) map[string]any {
	if strings.TrimSpace(state) == "" {
		state = "active"
	}
	return map[string]any{
		"state": state,
		"role":  role,
		"url":   fmt.Sprintf("%s/api/v3/orgs/%s/teams/%s/memberships/%s", baseURL, orgLogin, teamSlug, username),
	}
}

func teamPendingInvitationResponse(baseURL, orgLogin string, inv db.OrganizationInvitation) map[string]any {
	invitee := transform.User(inv.Invitee)
	inviter := transform.User(inv.Inviter)
	teamIDs, err := service.DecodeOrganizationInvitationTeamIDs(inv.TeamIDsJSON)
	if err != nil {
		teamIDs = nil
	}

	return map[string]any{
		"id":                   inv.ID,
		"login":                invitee["login"],
		"node_id":              invitee["node_id"],
		"email":                invitee["email"],
		"role":                 inv.Role,
		"created_at":           inv.CreatedAt.Format(time.RFC3339),
		"inviter":              inviter,
		"team_count":           len(teamIDs),
		"invitation_teams_url": fmt.Sprintf("%s/api/v3/orgs/%s/invitations/%d/teams", baseURL, url.PathEscape(strings.TrimSpace(orgLogin)), inv.ID),
	}
}

func requestBodyValue(r *http.Request, key string) string {
	if r == nil || r.Body == nil {
		return ""
	}
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		return ""
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil {
		if v, ok := payload[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return ""
	}
	return values.Get(key)
}

func canMutateTeamRepo(w http.ResponseWriter, r *http.Request, org *db.User, repo *db.Repository, deps *Deps) bool {
	if org == nil || repo == nil {
		respond.ServiceErrorRequest(r, w, service.ErrNotFound)
		return false
	}
	if repo.Owner.Type != db.TypeOrganization || repo.OwnerID != org.ID {
		respond.ServiceErrorRequest(r, w, service.ErrNotFound)
		return false
	}
	return deps.requireRepoPermission(w, r, repo.ID, service.RepoPermissionAdmin)
}
