package rest_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

// TestInvitationHandlers covers AcceptInvitation and DeclineInvitation handlers.
func TestInvitationHandlers(t *testing.T) {
	t.Run("AcceptInvitation_Unauthorized", func(t *testing.T) {
		h := testharness.New(t)

		// No auth header
		w := h.DoRESTNoAuth(t, "PATCH", "/api/v3/user/repository_invitations/1")
		assertStatusCode(t, w, http.StatusUnauthorized)

		body := testharness.DecodeJSON(t, w)
		if body["message"] != "Requires authentication" {
			t.Fatalf("expected unauthorized message, got %v", body["message"])
		}
	})

	t.Run("AcceptInvitation_InvalidID", func(t *testing.T) {
		h := testharness.New(t)

		// Invalid (non-numeric) invitation ID
		w := h.DoREST(t, "PATCH", "/api/v3/user/repository_invitations/invalid", nil)
		assertStatusCode(t, w, http.StatusUnprocessableEntity)
		body := testharness.DecodeJSON(t, w)
		if body["message"] != "invitation_id must be a number" {
			t.Fatalf("expected validation message, got %v", body["message"])
		}
	})

	t.Run("AcceptInvitation_NotFound", func(t *testing.T) {
		h := testharness.New(t)

		// Non-existent invitation ID
		w := h.DoREST(t, "PATCH", "/api/v3/user/repository_invitations/99999", nil)
		assertStatusCode(t, w, http.StatusNotFound)
	})

	t.Run("AcceptInvitation_Success", func(t *testing.T) {
		h := testharness.New(t)
		ctx := h.Svc.Ctx

		// Create repo and invitation
		repo := db.Repository{
			Name:          "invitest",
			FullName:      "testuser/invitest",
			OwnerID:       h.User.ID,
			DefaultBranch: "main",
		}
		if err := h.Svc.DB.Create(&repo).Error; err != nil {
			t.Fatalf("failed to create repo: %v", err)
		}

		invitee := db.User{Login: "invitee", Name: "Invitee User", Type: db.TypeUser}
		if err := h.Svc.DB.Create(&invitee).Error; err != nil {
			t.Fatalf("failed to create invitee: %v", err)
		}

		inv := db.RepositoryInvitation{
			RepositoryID: repo.ID,
			InviteeID:    invitee.ID,
			InviterID:    h.User.ID,
			Permissions:  "write",
		}
		if err := h.Svc.DB.Create(&inv).Error; err != nil {
			t.Fatalf("failed to create invitation: %v", err)
		}

		// Accept as invitee - need to use invitee's token
		inviteeToken := "invitee-token"
		if err := h.Svc.DB.Create(&db.Token{UserID: invitee.ID, Value: inviteeToken}).Error; err != nil {
			t.Fatalf("failed to create invitee token: %v", err)
		}

		w := h.DoRESTWithToken(t, "PATCH", "/api/v3/user/repository_invitations/"+strconv.FormatUint(uint64(inv.ID), 10), inviteeToken)
		assertStatusCode(t, w, http.StatusNoContent)

		// Verify invitation was deleted
		_, err := h.Svc.GetInvitation(ctx, inv.ID)
		if err == nil {
			t.Fatal("expected invitation to be deleted after accept")
		}

		// Verify user is now a collaborator
		isCollab, err := h.Svc.IsCollaborator(ctx, repo.ID, invitee.ID)
		if err != nil {
			t.Fatalf("IsCollaborator failed: %v", err)
		}
		if !isCollab {
			t.Fatal("expected invitee to be collaborator after accepting")
		}
	})

	t.Run("DeclineInvitation_Unauthorized", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoRESTNoAuth(t, "DELETE", "/api/v3/user/repository_invitations/1")
		assertStatusCode(t, w, http.StatusUnauthorized)

		body := testharness.DecodeJSON(t, w)
		if body["message"] != "Requires authentication" {
			t.Fatalf("expected unauthorized message, got %v", body["message"])
		}
	})

	t.Run("DeclineInvitation_InvalidID", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoREST(t, "DELETE", "/api/v3/user/repository_invitations/invalid", nil)
		assertStatusCode(t, w, http.StatusUnprocessableEntity)
		body := testharness.DecodeJSON(t, w)
		if body["message"] != "invitation_id must be a number" {
			t.Fatalf("expected validation message, got %v", body["message"])
		}
	})

	t.Run("DeclineInvitation_NotFound", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoREST(t, "DELETE", "/api/v3/user/repository_invitations/99999", nil)
		assertStatusCode(t, w, http.StatusNotFound)
	})

	t.Run("DeclineInvitation_Success", func(t *testing.T) {
		h := testharness.New(t)
		ctx := h.Svc.Ctx

		// Create repo and invitation
		repo := db.Repository{
			Name:          "declinetest",
			FullName:      "testuser/declinetest",
			OwnerID:       h.User.ID,
			DefaultBranch: "main",
		}
		if err := h.Svc.DB.Create(&repo).Error; err != nil {
			t.Fatalf("failed to create repo: %v", err)
		}

		invitee := db.User{Login: "declinee", Name: "Decline User", Type: db.TypeUser}
		if err := h.Svc.DB.Create(&invitee).Error; err != nil {
			t.Fatalf("failed to create invitee: %v", err)
		}

		inv := db.RepositoryInvitation{
			RepositoryID: repo.ID,
			InviteeID:    invitee.ID,
			InviterID:    h.User.ID,
			Permissions:  "read",
		}
		if err := h.Svc.DB.Create(&inv).Error; err != nil {
			t.Fatalf("failed to create invitation: %v", err)
		}

		// Decline as invitee
		inviteeToken := "declinee-token"
		if err := h.Svc.DB.Create(&db.Token{UserID: invitee.ID, Value: inviteeToken}).Error; err != nil {
			t.Fatalf("failed to create invitee token: %v", err)
		}

		w := h.DoRESTWithToken(t, "DELETE", "/api/v3/user/repository_invitations/"+strconv.FormatUint(uint64(inv.ID), 10), inviteeToken)
		assertStatusCode(t, w, http.StatusNoContent)

		// Verify invitation was deleted
		_, err := h.Svc.GetInvitation(ctx, inv.ID)
		if err == nil {
			t.Fatal("expected invitation to be deleted after decline")
		}

		// Verify user is NOT a collaborator
		isCollab, err := h.Svc.IsCollaborator(ctx, repo.ID, invitee.ID)
		if err != nil {
			t.Fatalf("IsCollaborator failed: %v", err)
		}
		if isCollab {
			t.Fatal("expected invitee to NOT be collaborator after declining")
		}
	})
}

// TestCollaboratorHandlers covers AddCollaborator and RemoveCollaborator handlers.
func TestCollaboratorHandlers(t *testing.T) {
	t.Run("AddCollaborator_Unauthorized", func(t *testing.T) {
		h := testharness.New(t)

		// Create repo first
		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "collabtest",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)

		// Try to add collaborator without auth
		w = h.DoRESTNoAuth(t, "PUT", "/api/v3/repos/testuser/collabtest/collaborators/newuser")
		assertStatusCode(t, w, http.StatusUnauthorized)
	})

	t.Run("AddCollaborator_RepoNotFound", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoRESTJSON(t, "PUT", "/api/v3/repos/nonexistent/repo/collaborators/user", map[string]any{
			"permission": "read",
		})
		assertStatusCode(t, w, http.StatusNotFound)
	})

	t.Run("AddCollaborator_UserNotFound", func(t *testing.T) {
		h := testharness.New(t)

		// Create repo
		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "nousertest",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)

		// Try to add non-existent user
		w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/nousertest/collaborators/nonexistent", map[string]any{
			"permission": "read",
		})
		assertStatusCode(t, w, http.StatusNotFound)
	})

	t.Run("AddCollaborator_InvalidPermission", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "badpermtest",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)

		user := db.User{Login: "badpermuser", Name: "Bad Perm User", Type: db.TypeUser}
		if err := h.Svc.DB.Create(&user).Error; err != nil {
			t.Fatalf("failed to create user: %v", err)
		}

		w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/badpermtest/collaborators/badpermuser", map[string]any{
			"permission": "super-admin",
		})
		assertStatusCode(t, w, http.StatusUnprocessableEntity)
	})

	t.Run("AddCollaborator_TriagePermission", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "triagetest",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)
		repoID := mustHarnessRepoID(t, h, "testuser/triagetest")

		user := db.User{Login: "triageuser", Name: "Triage User", Type: db.TypeUser}
		if err := h.Svc.DB.Create(&user).Error; err != nil {
			t.Fatalf("failed to create user: %v", err)
		}

		w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/triagetest/collaborators/triageuser", map[string]any{
			"permission": "triage",
		})
		assertStatusCode(t, w, http.StatusCreated)
		body := testharness.DecodeJSON(t, w)
		if body["permissions"] != "read" {
			t.Fatalf("expected permissions 'read', got %v", body["permissions"])
		}

		invs, err := h.Svc.ListRepoInvitations(h.Svc.Ctx, repoID)
		if err != nil {
			t.Fatalf("list invitations: %v", err)
		}
		if len(invs) != 1 || invs[0].Permissions != "read" {
			t.Fatalf("expected canonical read invitation, got %#v", invs)
		}
	})

	t.Run("AddCollaborator_RequiresRepoAdmin", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "adminonlytest",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)

		targetUser := db.User{Login: "targetuser", Name: "Target User", Type: db.TypeUser}
		if err := h.Svc.DB.Create(&targetUser).Error; err != nil {
			t.Fatalf("failed to create target user: %v", err)
		}
		_, outsiderToken := seedHarnessUser(t, h, "repo-outsider", false)

		w = h.DoRESTJSONWithToken(t, "PUT", "/api/v3/repos/testuser/adminonlytest/collaborators/targetuser", outsiderToken, map[string]any{
			"permission": "write",
		})
		assertStatusCode(t, w, http.StatusNotFound)
	})

	t.Run("AddCollaborator_UpdatesExistingCollaborator", func(t *testing.T) {
		h := testharness.New(t)
		ctx := h.Svc.Ctx

		// Create repo
		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "alreadytest",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)
		repoID := mustHarnessRepoID(t, h, "testuser/alreadytest")

		// Create user to add as collaborator
		collabUser := db.User{Login: "collabuser", Name: "Collab User", Type: db.TypeUser}
		if err := h.Svc.DB.Create(&collabUser).Error; err != nil {
			t.Fatalf("failed to create collaborator: %v", err)
		}

		if err := h.Svc.AddCollaborator(ctx, repoID, collabUser.ID, "read"); err != nil {
			t.Fatalf("seed collaborator: %v", err)
		}

		w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/alreadytest/collaborators/collabuser", map[string]any{
			"permission": "write",
		})
		assertStatusCode(t, w, http.StatusNoContent)

		isCollab, err := h.Svc.IsCollaborator(ctx, repoID, collabUser.ID)
		if err != nil {
			t.Fatalf("IsCollaborator failed: %v", err)
		}
		if !isCollab {
			t.Fatal("expected user to still be collaborator")
		}

		var collab db.Collaborator
		if err := h.Svc.DB.First(&collab, "repository_id = ? AND user_id = ?", repoID, collabUser.ID).Error; err != nil {
			t.Fatalf("failed to load collaborator: %v", err)
		}
		if collab.Permission != "write" {
			t.Fatalf("expected collaborator permission to be updated to write, got %q", collab.Permission)
		}

		var invitationCount int64
		if err := h.Svc.DB.Model(&db.RepositoryInvitation{}).
			Where("repository_id = ? AND invitee_id = ?", repoID, collabUser.ID).
			Count(&invitationCount).Error; err != nil {
			t.Fatalf("count invitations: %v", err)
		}
		if invitationCount != 0 {
			t.Fatalf("expected no pending invitation for existing collaborator, got %d", invitationCount)
		}
	})

	t.Run("AddCollaborator_Success", func(t *testing.T) {
		h := testharness.New(t)
		ctx := h.Svc.Ctx

		// Create repo
		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "addcollabtest",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)
		repoID := mustHarnessRepoID(t, h, "testuser/addcollabtest")

		// Create user to add
		newCollab := db.User{Login: "newcollab", Name: "New Collab", Type: db.TypeUser}
		if err := h.Svc.DB.Create(&newCollab).Error; err != nil {
			t.Fatalf("failed to create collaborator: %v", err)
		}

		// Add collaborator with write permission
		w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/addcollabtest/collaborators/newcollab", map[string]any{
			"permission": "write",
		})
		assertStatusCode(t, w, http.StatusCreated)

		body := testharness.DecodeJSON(t, w)

		// Response schema compatibility checks
		assertFieldPresent(t, body, "id", "number")
		assertFieldPresent(t, body, "repository", "object")
		assertFieldPresent(t, body, "invitee", "object")
		assertFieldPresent(t, body, "inviter", "object")
		assertFieldPresent(t, body, "permissions", "string")
		assertFieldPresent(t, body, "created_at", "string")
		assertFieldPresent(t, body, "url", "string")
		assertFieldPresent(t, body, "html_url", "string")

		// Verify specific fields
		if body["permissions"] != "write" {
			t.Fatalf("expected permissions 'write', got %v", body["permissions"])
		}

		// Verify invitee
		invitee, ok := body["invitee"].(map[string]any)
		if !ok {
			t.Fatal("invitee should be an object")
		}
		if invitee["login"] != "newcollab" {
			t.Fatalf("expected invitee login 'newcollab', got %v", invitee["login"])
		}

		// Verify inviter
		inviter, ok := body["inviter"].(map[string]any)
		if !ok {
			t.Fatal("inviter should be an object")
		}
		if inviter["login"] != "testuser" {
			t.Fatalf("expected inviter login 'testuser', got %v", inviter["login"])
		}

		// Verify repository
		repo, ok := body["repository"].(map[string]any)
		if !ok {
			t.Fatal("repository should be an object")
		}
		if repo["name"] != "addcollabtest" {
			t.Fatalf("expected repo name 'addcollabtest', got %v", repo["name"])
		}

		// Verify invitation was created in DB
		invs, err := h.Svc.ListRepoInvitations(ctx, repoID)
		if err != nil {
			t.Fatalf("ListRepoInvitations failed: %v", err)
		}
		if len(invs) != 1 {
			t.Fatalf("expected 1 invitation, got %d", len(invs))
		}
		if invs[0].InviteeID != newCollab.ID {
			t.Fatalf("expected invitee ID %d, got %d", newCollab.ID, invs[0].InviteeID)
		}
		if invs[0].Permissions != "write" {
			t.Fatalf("expected permissions 'write', got %q", invs[0].Permissions)
		}
	})

	t.Run("AddCollaborator_ReinvitesPendingInvitation", func(t *testing.T) {
		h := testharness.New(t)
		ctx := h.Svc.Ctx

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "reinvitependingtest",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)
		repoID := mustHarnessRepoID(t, h, "testuser/reinvitependingtest")

		invitee := db.User{Login: "pendinginvitee", Name: "Pending Invitee", Type: db.TypeUser}
		if err := h.Svc.DB.Create(&invitee).Error; err != nil {
			t.Fatalf("failed to create invitee: %v", err)
		}

		w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/reinvitependingtest/collaborators/pendinginvitee", map[string]any{
			"permission": "read",
		})
		assertStatusCode(t, w, http.StatusCreated)
		firstBody := testharness.DecodeJSON(t, w)
		firstCreatedAt, _ := firstBody["created_at"].(string)
		firstID := firstBody["id"]
		if firstCreatedAt == "" || firstCreatedAt == "0001-01-01T00:00:00Z" {
			t.Fatalf("expected non-zero created_at on first invite, got %q", firstCreatedAt)
		}

		w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/reinvitependingtest/collaborators/pendinginvitee", map[string]any{
			"permission": "write",
		})
		assertStatusCode(t, w, http.StatusCreated)
		secondBody := testharness.DecodeJSON(t, w)
		secondCreatedAt, _ := secondBody["created_at"].(string)
		if secondBody["id"] != firstID {
			t.Fatalf("expected invitation ID %v to be preserved, got %v", firstID, secondBody["id"])
		}
		firstCreatedAtTime, err := time.Parse(time.RFC3339, firstCreatedAt)
		if err != nil {
			t.Fatalf("parse first created_at %q: %v", firstCreatedAt, err)
		}
		secondCreatedAtTime, err := time.Parse(time.RFC3339, secondCreatedAt)
		if err != nil {
			t.Fatalf("parse second created_at %q: %v", secondCreatedAt, err)
		}
		if !secondCreatedAtTime.Equal(firstCreatedAtTime) {
			t.Fatalf("expected created_at %q to be preserved, got %q", firstCreatedAt, secondCreatedAt)
		}
		if secondBody["permissions"] != "write" {
			t.Fatalf("expected permissions 'write', got %v", secondBody["permissions"])
		}

		invs, err := h.Svc.ListRepoInvitations(ctx, repoID)
		if err != nil {
			t.Fatalf("ListRepoInvitations failed: %v", err)
		}
		if len(invs) != 1 {
			t.Fatalf("expected 1 invitation after reinvite, got %d", len(invs))
		}
		if invs[0].Permissions != "write" {
			t.Fatalf("expected stored permissions 'write', got %q", invs[0].Permissions)
		}
		if invs[0].CreatedAt.IsZero() {
			t.Fatal("expected stored created_at to remain populated after reinvite")
		}
	})

	t.Run("AddCollaborator_DefaultPermission", func(t *testing.T) {
		h := testharness.New(t)
		ctx := h.Svc.Ctx

		// Create repo
		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "defaultpermtest",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)
		repoID := mustHarnessRepoID(t, h, "testuser/defaultpermtest")

		// Create user
		user := db.User{Login: "defaultuser", Name: "Default User", Type: db.TypeUser}
		if err := h.Svc.DB.Create(&user).Error; err != nil {
			t.Fatalf("failed to create user: %v", err)
		}

		// Add collaborator without specifying permission (should default to "read")
		w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/defaultpermtest/collaborators/defaultuser", map[string]any{})
		assertStatusCode(t, w, http.StatusCreated)

		body := testharness.DecodeJSON(t, w)
		if body["permissions"] != "read" {
			t.Fatalf("expected default permissions 'read', got %v", body["permissions"])
		}

		// Verify in DB
		invs, err := h.Svc.ListRepoInvitations(ctx, repoID)
		if err != nil {
			t.Fatalf("ListRepoInvitations failed: %v", err)
		}
		if len(invs) != 1 {
			t.Fatalf("expected 1 invitation, got %d", len(invs))
		}
		if invs[0].Permissions != "read" {
			t.Fatalf("expected DB permissions 'read', got %q", invs[0].Permissions)
		}
	})

	t.Run("AddCollaborator_MalformedJSONRejected", func(t *testing.T) {
		h := testharness.New(t)
		ctx := h.Svc.Ctx

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "invalidcollabbody",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)
		repoID := mustHarnessRepoID(t, h, "testuser/invalidcollabbody")

		user := db.User{Login: "invalidjsonuser", Name: "Invalid JSON User", Type: db.TypeUser}
		if err := h.Svc.DB.Create(&user).Error; err != nil {
			t.Fatalf("failed to create user: %v", err)
		}

		w = h.DoREST(t, "PUT", "/api/v3/repos/testuser/invalidcollabbody/collaborators/invalidjsonuser", strings.NewReader(`{"permission":`))
		assertStatusCode(t, w, http.StatusUnprocessableEntity)
		body := testharness.DecodeJSON(t, w)
		if body["message"] != "invalid body" {
			t.Fatalf("expected invalid body message, got %v", body["message"])
		}

		invs, err := h.Svc.ListRepoInvitations(ctx, repoID)
		if err != nil {
			t.Fatalf("ListRepoInvitations failed: %v", err)
		}
		if len(invs) != 0 {
			t.Fatalf("expected no invitations after malformed body, got %d", len(invs))
		}
	})

	t.Run("RemoveCollaborator_RepoNotFound", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoREST(t, "DELETE", "/api/v3/repos/nonexistent/repo/collaborators/user", nil)
		assertStatusCode(t, w, http.StatusNotFound)
	})

	t.Run("RemoveCollaborator_UserNotFound", func(t *testing.T) {
		h := testharness.New(t)

		// Create repo
		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "removenousertest",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)

		// Try to remove non-existent user
		w = h.DoREST(t, "DELETE", "/api/v3/repos/testuser/removenousertest/collaborators/nonexistent", nil)
		assertStatusCode(t, w, http.StatusNotFound)
	})

	t.Run("RemoveCollaborator_Success", func(t *testing.T) {
		h := testharness.New(t)
		ctx := h.Svc.Ctx

		// Create repo
		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "removecollabtest",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)
		repoID := mustHarnessRepoID(t, h, "testuser/removecollabtest")

		// Create user and add as collaborator
		collabUser := db.User{Login: "removecollab", Name: "Remove Collab", Type: db.TypeUser}
		if err := h.Svc.DB.Create(&collabUser).Error; err != nil {
			t.Fatalf("failed to create collaborator: %v", err)
		}

		if err := h.Svc.AddCollaborator(ctx, repoID, collabUser.ID, "write"); err != nil {
			t.Fatalf("seed collaborator: %v", err)
		}

		// Verify collaborator exists
		isCollab, err := h.Svc.IsCollaborator(ctx, repoID, collabUser.ID)
		if err != nil {
			t.Fatalf("IsCollaborator failed: %v", err)
		}
		if !isCollab {
			t.Fatal("expected user to be collaborator")
		}

		// Remove collaborator
		w = h.DoREST(t, "DELETE", "/api/v3/repos/testuser/removecollabtest/collaborators/removecollab", nil)
		assertStatusCode(t, w, http.StatusNoContent)

		// Verify collaborator was removed
		isCollab, err = h.Svc.IsCollaborator(ctx, repoID, collabUser.ID)
		if err != nil {
			t.Fatalf("IsCollaborator failed: %v", err)
		}
		if isCollab {
			t.Fatal("expected user to NOT be collaborator after removal")
		}
	})

	t.Run("RemoveCollaborator_RequiresRepoAdmin", func(t *testing.T) {
		h := testharness.New(t)
		ctx := h.Svc.Ctx

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "removeadminonlytest",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)
		repoID := mustHarnessRepoID(t, h, "testuser/removeadminonlytest")

		collabUser := db.User{Login: "admincheck", Name: "Admin Check", Type: db.TypeUser}
		if err := h.Svc.DB.Create(&collabUser).Error; err != nil {
			t.Fatalf("failed to create collaborator: %v", err)
		}
		if err := h.Svc.AddCollaborator(ctx, repoID, collabUser.ID, "write"); err != nil {
			t.Fatalf("seed collaborator: %v", err)
		}
		_, outsiderToken := seedHarnessUser(t, h, "remove-outsider", false)

		w = h.DoRESTWithToken(t, "DELETE", "/api/v3/repos/testuser/removeadminonlytest/collaborators/admincheck", outsiderToken)
		assertStatusCode(t, w, http.StatusNotFound)

		isCollab, err := h.Svc.IsCollaborator(ctx, repoID, collabUser.ID)
		if err != nil {
			t.Fatalf("IsCollaborator failed: %v", err)
		}
		if !isCollab {
			t.Fatal("expected collaborator to remain after unauthorized delete")
		}
	})

	t.Run("RemoveCollaborator_RemovesPendingInvitation", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "removependingtest",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)
		repoID := mustHarnessRepoID(t, h, "testuser/removependingtest")

		user := db.User{Login: "pendinginvitee", Name: "Pending Invitee", Type: db.TypeUser}
		if err := h.Svc.DB.Create(&user).Error; err != nil {
			t.Fatalf("failed to create user: %v", err)
		}
		inv := db.RepositoryInvitation{
			RepositoryID: repoID,
			InviteeID:    user.ID,
			InviterID:    h.User.ID,
			Permissions:  "write",
		}
		if err := h.Svc.DB.Create(&inv).Error; err != nil {
			t.Fatalf("failed to create invitation: %v", err)
		}

		w = h.DoREST(t, "DELETE", "/api/v3/repos/testuser/removependingtest/collaborators/pendinginvitee", nil)
		assertStatusCode(t, w, http.StatusNoContent)

		var invitationCount int64
		if err := h.Svc.DB.Model(&db.RepositoryInvitation{}).
			Where("repository_id = ? AND invitee_id = ?", repoID, user.ID).
			Count(&invitationCount).Error; err != nil {
			t.Fatalf("count invitations: %v", err)
		}
		if invitationCount != 0 {
			t.Fatalf("expected pending invitation to be removed, got %d", invitationCount)
		}
	})

	t.Run("RemoveCollaborator_NoContent", func(t *testing.T) {
		h := testharness.New(t)

		// Create repo
		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "removenotcollabtest",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)

		// Create user but don't add as collaborator
		user := db.User{Login: "notcollab", Name: "Not Collab", Type: db.TypeUser}
		if err := h.Svc.DB.Create(&user).Error; err != nil {
			t.Fatalf("failed to create user: %v", err)
		}

		// Try to remove user who is not a collaborator - should still return 204
		w = h.DoREST(t, "DELETE", "/api/v3/repos/testuser/removenotcollabtest/collaborators/notcollab", nil)
		assertStatusCode(t, w, http.StatusNoContent)
	})
}

// TestGetRepoInvitations covers the GET /api/v3/repos/{owner}/{repo}/invitations handler.
func TestGetRepoInvitations(t *testing.T) {
	t.Run("GetRepoInvitations_Unauthorized", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "repo",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)

		w = h.DoRESTNoAuth(t, "GET", "/api/v3/repos/testuser/repo/invitations")
		// Anonymous requests to admin-only endpoints get 404 (same as GitHub).
		assertStatusCode(t, w, http.StatusNotFound)
	})

	t.Run("GetRepoInvitations_RepoNotFound", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoREST(t, "GET", "/api/v3/repos/nonexistent/repo/invitations", nil)
		assertStatusCode(t, w, http.StatusNotFound)
	})

	t.Run("GetRepoInvitations_Empty", func(t *testing.T) {
		h := testharness.New(t)

		// Create repo
		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "emptyinvtest",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)

		// Get invitations (should be empty array)
		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/emptyinvtest/invitations", nil)
		assertStatusCode(t, w, http.StatusOK)

		body := testharness.DecodeJSONArray(t, w)
		if len(body) != 0 {
			t.Fatalf("expected empty array, got %d items", len(body))
		}
	})

	t.Run("GetRepoInvitations_RequiresRepoAdmin", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "invsadmintest",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)
		repoID := mustHarnessRepoID(t, h, "testuser/invsadmintest")

		invitee := db.User{Login: "invited-admin-test", Name: "Invited Admin Test", Type: db.TypeUser}
		if err := h.Svc.DB.Create(&invitee).Error; err != nil {
			t.Fatalf("failed to create invitee: %v", err)
		}
		inv := db.RepositoryInvitation{
			RepositoryID: repoID,
			InviteeID:    invitee.ID,
			InviterID:    h.User.ID,
			Permissions:  "read",
		}
		if err := h.Svc.DB.Create(&inv).Error; err != nil {
			t.Fatalf("failed to create invitation: %v", err)
		}
		_, outsiderToken := seedHarnessUser(t, h, "invitation-outsider", false)

		w = h.DoRESTWithToken(t, "GET", "/api/v3/repos/testuser/invsadmintest/invitations", outsiderToken)
		assertStatusCode(t, w, http.StatusNotFound)
	})

	t.Run("GetRepoInvitations_WithInvitations", func(t *testing.T) {
		h := testharness.New(t)

		// Create repo
		w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
			"name":      "invslisttest",
			"auto_init": true,
		})
		assertStatusCode(t, w, http.StatusCreated)
		repoID := mustHarnessRepoID(t, h, "testuser/invslisttest")

		// Create users
		user1 := db.User{Login: "invuser1", Name: "Inv User 1", Type: db.TypeUser}
		user2 := db.User{Login: "invuser2", Name: "Inv User 2", Type: db.TypeUser}
		if err := h.Svc.DB.Create(&user1).Error; err != nil {
			t.Fatalf("failed to create user1: %v", err)
		}
		if err := h.Svc.DB.Create(&user2).Error; err != nil {
			t.Fatalf("failed to create user2: %v", err)
		}

		// Create invitations
		inv1 := db.RepositoryInvitation{
			RepositoryID: repoID,
			InviteeID:    user1.ID,
			InviterID:    h.User.ID,
			Permissions:  "read",
		}
		inv2 := db.RepositoryInvitation{
			RepositoryID: repoID,
			InviteeID:    user2.ID,
			InviterID:    h.User.ID,
			Permissions:  "write",
		}
		if err := h.Svc.DB.Create(&inv1).Error; err != nil {
			t.Fatalf("failed to create inv1: %v", err)
		}
		if err := h.Svc.DB.Create(&inv2).Error; err != nil {
			t.Fatalf("failed to create inv2: %v", err)
		}

		// Get invitations
		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/invslisttest/invitations", nil)
		assertStatusCode(t, w, http.StatusOK)

		body := testharness.DecodeJSONArray(t, w)
		if len(body) != 2 {
			t.Fatalf("expected 2 invitations, got %d", len(body))
		}

		// Verify response schema for each invitation
		for i, item := range body {
			assertFieldPresent(t, item, "id", "number")
			assertFieldPresent(t, item, "repository", "object")
			assertFieldPresent(t, item, "invitee", "object")
			assertFieldPresent(t, item, "inviter", "object")
			assertFieldPresent(t, item, "permissions", "string")
			assertFieldPresent(t, item, "created_at", "string")
			assertFieldPresent(t, item, "url", "string")
			assertFieldPresent(t, item, "html_url", "string")
			_ = i
		}
	})
}

// TestListUserInvitations covers the GET /api/v3/user/repository_invitations handler.
func TestListUserInvitations(t *testing.T) {
	t.Run("ListUserInvitations_Unauthorized", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoRESTNoAuth(t, "GET", "/api/v3/user/repository_invitations")
		assertStatusCode(t, w, http.StatusUnauthorized)

		body := testharness.DecodeJSON(t, w)
		if body["message"] != "Requires authentication" {
			t.Fatalf("expected unauthorized message, got %v", body["message"])
		}
	})

	t.Run("ListUserInvitations_Empty", func(t *testing.T) {
		h := testharness.New(t)

		w := h.DoREST(t, "GET", "/api/v3/user/repository_invitations", nil)
		assertStatusCode(t, w, http.StatusOK)

		body := testharness.DecodeJSONArray(t, w)
		if len(body) != 0 {
			t.Fatalf("expected empty array, got %d items", len(body))
		}
	})

	t.Run("ListUserInvitations_WithInvitations", func(t *testing.T) {
		h := testharness.New(t)

		// Create repo
		repo := db.Repository{
			Name:          "userinvtest",
			FullName:      "testuser/userinvtest",
			OwnerID:       h.User.ID,
			DefaultBranch: "main",
		}
		if err := h.Svc.DB.Create(&repo).Error; err != nil {
			t.Fatalf("failed to create repo: %v", err)
		}

		// Create invitation to testuser
		inv := db.RepositoryInvitation{
			RepositoryID: repo.ID,
			InviteeID:    h.User.ID,
			InviterID:    h.User.ID,
			Permissions:  "admin",
		}
		if err := h.Svc.DB.Create(&inv).Error; err != nil {
			t.Fatalf("failed to create invitation: %v", err)
		}

		w := h.DoREST(t, "GET", "/api/v3/user/repository_invitations", nil)
		assertStatusCode(t, w, http.StatusOK)

		body := testharness.DecodeJSONArray(t, w)
		if len(body) != 1 {
			t.Fatalf("expected 1 invitation, got %d", len(body))
		}

		item := body[0]
		assertFieldPresent(t, item, "id", "number")
		assertFieldPresent(t, item, "repository", "object")
		assertFieldPresent(t, item, "invitee", "object")
		assertFieldPresent(t, item, "inviter", "object")
		assertFieldPresent(t, item, "permissions", "string")
		assertFieldPresent(t, item, "created_at", "string")
		assertFieldPresent(t, item, "url", "string")
		assertFieldPresent(t, item, "html_url", "string")

		if item["permissions"] != "admin" {
			t.Fatalf("expected permissions 'admin', got %v", item["permissions"])
		}
	})
}

func mustHarnessRepoID(t *testing.T, h *testharness.Harness, fullName string) uint {
	t.Helper()
	var repo db.Repository
	if err := h.Svc.DB.First(&repo, "full_name = ?", fullName).Error; err != nil {
		t.Fatalf("load repo %s: %v", fullName, err)
	}
	return repo.ID
}
