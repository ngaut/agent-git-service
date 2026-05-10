package rest

import (
	"fmt"
	"net/http"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/rest/respond"
	"gh-server/internal/rest/transform"
	"gh-server/internal/service"

	"github.com/go-chi/chi/v5"
)

func repositoryInvitationJSON(inv db.RepositoryInvitation) map[string]any {
	var invitee any
	if inv.Invitee.ID != 0 {
		invitee = transform.User(inv.Invitee)
	}
	var inviter any
	if inv.Inviter.ID != 0 {
		inviter = transform.User(inv.Inviter)
	}

	return map[string]any{
		"id":          inv.ID,
		"repository":  transform.Repo(inv.Repository),
		"invitee":     invitee,
		"inviter":     inviter,
		"permissions": service.ParseRepoPermission(inv.Permissions).String(),
		"created_at":  inv.CreatedAt.Format(time.RFC3339),
		"url":         fmt.Sprintf("%s/api/v3/user/repository_invitations/%d", transform.Base(), inv.ID),
		"html_url":    fmt.Sprintf("%s/%s/invitations", transform.HTMLBase(), inv.Repository.FullName),
	}
}

func normalizeCollaboratorPermission(permission string) (string, bool) {
	return service.NormalizeGrantPermission(permission)
}

// GetRepoInvitations handles GET /api/v3/repos/{owner}/{repo}/invitations
func (d *Deps) GetRepoInvitations(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if _, err := d.Svc.GetCurrentUser(r.Context()); err != nil {
		respond.Unauthorized(w, "Bad credentials")
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionAdmin) {
		return
	}

	invs, err := d.Svc.ListRepoInvitations(r.Context(), repo.ID)
	if err != nil {
		respond.JSON(w, 200, []any{})
		return
	}

	var out []any
	for _, inv := range invs {
		// Repo JSON requires Owner preloaded typically
		out = append(out, repositoryInvitationJSON(inv))
	}
	if out == nil {
		out = []any{}
	}
	respond.JSON(w, 200, out)
}

// ListUserInvitations handles GET /api/v3/user/repository_invitations
func (d *Deps) ListUserInvitations(w http.ResponseWriter, r *http.Request) {
	user, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.Unauthorized(w, "Bad credentials")
		return
	}

	invs, err := d.Svc.ListUserInvitations(r.Context(), user.ID)
	if err != nil {
		respond.JSON(w, 200, []any{})
		return
	}

	var out []any
	for _, inv := range invs {
		out = append(out, repositoryInvitationJSON(inv))
	}
	if out == nil {
		out = []any{}
	}
	respond.JSON(w, 200, out)
}

// AcceptInvitation handles PATCH /api/v3/user/repository_invitations/{invitation_id}
func (d *Deps) AcceptInvitation(w http.ResponseWriter, r *http.Request) {
	user, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.Unauthorized(w, "Bad credentials")
		return
	}
	id, ok := mustIntParam(w, r, "invitation_id")
	if !ok {
		return
	}

	if err := d.Svc.AcceptInvitation(r.Context(), uint(id), user.ID); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DeclineInvitation handles DELETE /api/v3/user/repository_invitations/{invitation_id}
func (d *Deps) DeclineInvitation(w http.ResponseWriter, r *http.Request) {
	user, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.Unauthorized(w, "Bad credentials")
		return
	}
	id, ok := mustIntParam(w, r, "invitation_id")
	if !ok {
		return
	}

	if err := d.Svc.DeclineInvitation(r.Context(), uint(id), user.ID); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// AddCollaborator handles PUT /api/v3/repos/{owner}/{repo}/collaborators/{username}
func (d *Deps) AddCollaborator(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	user, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.Unauthorized(w, "Bad credentials")
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionAdmin) {
		return
	}

	username := chi.URLParam(r, "username")
	if username == "" {
		respond.NotFound(w)
		return
	}

	invitee, err := d.Svc.GetUser(r.Context(), username)
	if err != nil {
		respond.NotFound(w)
		return
	}

	var body struct {
		Permission string `json:"permission"`
	}
	if err := decodeBodyStrictOptional(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	permission, ok := normalizeCollaboratorPermission(body.Permission)
	if !ok {
		respond.ValidationFailed(w, service.GrantPermissionValidationMessage)
		return
	}

	if invitee.ID == repo.OwnerID {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	isCollaborator, err := d.Svc.IsCollaborator(r.Context(), repo.ID, invitee.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if isCollaborator {
		if err := d.Svc.AddCollaborator(r.Context(), repo.ID, invitee.ID, permission); err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	inv := db.RepositoryInvitation{
		RepositoryID: repo.ID,
		Repository:   *repo,
		InviteeID:    invitee.ID,
		Invitee:      invitee,
		InviterID:    user.ID,
		Inviter:      user,
		Permissions:  permission,
	}

	if err := d.Svc.CreateInvitation(r.Context(), &inv); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	// Format response as an invitation instead of direct collaborator
	respond.JSON(w, 201, repositoryInvitationJSON(inv))
}

// RemoveCollaborator handles DELETE /api/v3/repos/{owner}/{repo}/collaborators/{username}
func (d *Deps) RemoveCollaborator(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if _, err := d.Svc.GetCurrentUser(r.Context()); err != nil {
		respond.Unauthorized(w, "Bad credentials")
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionAdmin) {
		return
	}
	username := chi.URLParam(r, "username")
	if username == "" {
		respond.NotFound(w)
		return
	}

	targetUser, err := d.Svc.GetUser(r.Context(), username)
	if err != nil {
		respond.NotFound(w)
		return
	}

	if err := d.Svc.RemoveCollaborator(r.Context(), repo.ID, targetUser.ID); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
