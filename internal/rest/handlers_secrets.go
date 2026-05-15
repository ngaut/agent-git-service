package rest

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"gh-server/internal/crypto"
	"gh-server/internal/db"
	"gh-server/internal/rest/respond"
	"gh-server/internal/rest/transform"
	"gh-server/internal/service"
)

// ─── Repo Secrets ──────────────────────────────────────────────────────────

// ListRepoSecrets handles GET /repos/{owner}/{repo}/actions/secrets
func (d *Deps) ListRepoSecrets(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	d.listSecrets(w, r, repo.ID, "")
}

// GetRepoPublicKey handles GET /repos/{owner}/{repo}/actions/secrets/public-key
func (d *Deps) GetRepoPublicKey(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, 200, publicKeyJSON())
}

// GetRepoSecret handles GET /repos/{owner}/{repo}/actions/secrets/{name}
func (d *Deps) GetRepoSecret(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	name := pathParam(r, "name")
	secret, err := d.Svc.GetSecret(r.Context(), repo.ID, "", name)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.Secret(secret))
}

// CreateOrUpdateRepoSecret handles PUT /repos/{owner}/{repo}/actions/secrets/{name}
func (d *Deps) CreateOrUpdateRepoSecret(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	d.upsertSecret(w, r, &repo.ID, repo.OwnerID, "")
}

// DeleteRepoSecret handles DELETE /repos/{owner}/{repo}/actions/secrets/{name}
func (d *Deps) DeleteRepoSecret(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	d.deleteSecret(w, r, repo.ID, "")
}

// ─── Org Secrets ───────────────────────────────────────────────────────────

// ListOrgSecrets handles GET /orgs/{org}/actions/secrets
func (d *Deps) ListOrgSecrets(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	d.listOrgSecrets(w, r, org.ID)
}

// GetOrgPublicKey handles GET /orgs/{org}/actions/secrets/public-key
func (d *Deps) GetOrgPublicKey(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, 200, publicKeyJSON())
}

// CreateOrUpdateOrgSecret handles PUT /orgs/{org}/actions/secrets/{name}
func (d *Deps) CreateOrUpdateOrgSecret(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	d.upsertOrgSecret(w, r, org.ID)
}

// DeleteOrgSecret handles DELETE /orgs/{org}/actions/secrets/{name}
func (d *Deps) DeleteOrgSecret(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	d.deleteOrgSecret(w, r, org.ID)
}

// GetOrgSecret handles GET /orgs/{org}/actions/secrets/{name}
func (d *Deps) GetOrgSecret(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	name := pathParam(r, "name")
	s, err := d.Svc.GetOrgSecret(r.Context(), org.ID, name)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.OrgSecret(s, org.Login))
}

// GetOrgSecretRepos handles GET /orgs/{org}/actions/secrets/{name}/repositories
func (d *Deps) GetOrgSecretRepos(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	name := pathParam(r, "name")
	s, err := d.Svc.GetOrgSecret(r.Context(), org.ID, name)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	repos := d.selectedRepos(r, s)
	respond.JSON(w, 200, map[string]any{"total_count": len(repos), "repositories": repos})
}

// SetOrgSecretRepos handles PUT /orgs/{org}/actions/secrets/{name}/repositories
func (d *Deps) SetOrgSecretRepos(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	name := pathParam(r, "name")
	var body struct {
		SelectedRepositoryIDs []uint `json:"selected_repository_ids"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	ids := make([]string, len(body.SelectedRepositoryIDs))
	for i, id := range body.SelectedRepositoryIDs {
		ids[i] = strconv.FormatUint(uint64(id), 10)
	}
	err := d.Svc.UpdateOrgSecretSelectedRepos(r.Context(), org.ID, name, strings.Join(ids, ","))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// ─── Env Secrets ───────────────────────────────────────────────────────────

// ListEnvSecrets handles GET /repos/{owner}/{repo}/environments/{env}/secrets
func (d *Deps) ListEnvSecrets(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	env := pathParam(r, "environment_name")
	d.listSecrets(w, r, repo.ID, env)
}

// GetEnvPublicKey handles GET /repos/{owner}/{repo}/environments/{env}/secrets/public-key
func (d *Deps) GetEnvPublicKey(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, 200, publicKeyJSON())
}

// CreateOrUpdateEnvSecret handles PUT /repos/{owner}/{repo}/environments/{env}/secrets/{name}
func (d *Deps) CreateOrUpdateEnvSecret(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	env := pathParam(r, "environment_name")
	d.upsertSecret(w, r, &repo.ID, repo.OwnerID, env)
}

// DeleteEnvSecret handles DELETE /repos/{owner}/{repo}/environments/{env}/secrets/{name}
func (d *Deps) DeleteEnvSecret(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	env := pathParam(r, "environment_name")
	d.deleteSecret(w, r, repo.ID, env)
}

// ─── User Codespaces Secrets ───────────────────────────────────────────────

// ListUserCodespacesSecrets handles GET /user/codespaces/secrets
func (d *Deps) ListUserCodespacesSecrets(w http.ResponseWriter, r *http.Request) {
	user, ok := d.currentUser(w, r)
	if !ok {
		return
	}
	secrets, err := d.Svc.ListOrgSecrets(r.Context(), user.ID)
	if err != nil {
		secrets = []db.Secret{}
	}
	out := make([]map[string]any, len(secrets))
	for i, s := range secrets {
		out[i] = transform.UserCodespacesSecret(s)
	}
	respond.JSON(w, 200, map[string]any{"total_count": len(out), "secrets": out})
}

// GetUserCodespacesPublicKey handles GET /user/codespaces/secrets/public-key
func (d *Deps) GetUserCodespacesPublicKey(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, 200, publicKeyJSON())
}

// GetUserCodespacesSecret handles GET /user/codespaces/secrets/{name}
func (d *Deps) GetUserCodespacesSecret(w http.ResponseWriter, r *http.Request) {
	_, secret, ok := d.currentUserCodespacesSecret(w, r)
	if !ok {
		return
	}
	respond.JSON(w, 200, transform.UserCodespacesSecret(secret))
}

// CreateOrUpdateUserCodespacesSecret handles PUT /user/codespaces/secrets/{name}
func (d *Deps) CreateOrUpdateUserCodespacesSecret(w http.ResponseWriter, r *http.Request) {
	user, ok := d.currentUser(w, r)
	if !ok {
		return
	}
	name := pathParam(r, "name")
	var body struct {
		EncryptedValue        string `json:"encrypted_value"`
		KeyID                 string `json:"key_id"`
		SelectedRepositoryIDs []any  `json:"selected_repository_ids"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}

	var value string
	if body.EncryptedValue != "" {
		decrypted, err := crypto.DecryptSecret(body.EncryptedValue)
		if err != nil {
			slog.Error("user codespaces secret decrypt", "error", err)
		} else {
			value = decrypted
		}
	}

	existingSecret, err := d.Svc.GetOrgSecret(r.Context(), user.ID, name)
	existing := err == nil
	selectedRepoIDs, ok := parseSelectedRepositoryIDs(body.SelectedRepositoryIDs)
	if !ok {
		respond.ValidationFailed(w, "invalid selected_repository_ids")
		return
	}
	if body.SelectedRepositoryIDs == nil && existing {
		selectedRepoIDs = splitSelectedRepositoryIDs(existingSecret.SelectedRepoIDs)
	}

	err = d.Svc.UpsertSecret(r.Context(), service.UpsertSecretInput{
		OwnerID:         user.ID,
		RepoID:          nil,
		Env:             "",
		Name:            name,
		Value:           value,
		Visibility:      "selected",
		SelectedRepoIDs: strings.Join(selectedRepoIDs, ","),
	})
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	if existing {
		respond.NoContent(w)
	} else {
		respond.JSON(w, http.StatusCreated, map[string]any{})
	}
}

// DeleteUserCodespacesSecret handles DELETE /user/codespaces/secrets/{name}
func (d *Deps) DeleteUserCodespacesSecret(w http.ResponseWriter, r *http.Request) {
	user, ok := d.currentUser(w, r)
	if !ok {
		return
	}
	name := pathParam(r, "name")
	if err := d.Svc.DeleteOrgSecret(r.Context(), user.ID, name); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// GetUserCodespacesSecretRepos handles GET /user/codespaces/secrets/{name}/repositories
func (d *Deps) GetUserCodespacesSecretRepos(w http.ResponseWriter, r *http.Request) {
	_, secret, ok := d.currentUserCodespacesSecret(w, r)
	if !ok {
		return
	}
	repos := d.selectedRepos(r, secret)
	respond.JSON(w, 200, map[string]any{"total_count": len(repos), "repositories": repos})
}

// SetUserCodespacesSecretRepos handles PUT /user/codespaces/secrets/{name}/repositories
func (d *Deps) SetUserCodespacesSecretRepos(w http.ResponseWriter, r *http.Request) {
	user, secret, ok := d.currentUserCodespacesSecret(w, r)
	if !ok {
		return
	}
	var body struct {
		SelectedRepositoryIDs []any `json:"selected_repository_ids"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	selectedRepoIDs, ok := parseSelectedRepositoryIDs(body.SelectedRepositoryIDs)
	if !ok {
		respond.ValidationFailed(w, "invalid selected_repository_ids")
		return
	}
	if err := d.Svc.UpdateOrgSecretSelectedRepos(r.Context(), user.ID, secret.Name, strings.Join(selectedRepoIDs, ",")); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// AddUserCodespacesSecretRepo handles PUT /user/codespaces/secrets/{name}/repositories/{repository_id}
func (d *Deps) AddUserCodespacesSecretRepo(w http.ResponseWriter, r *http.Request) {
	d.updateUserCodespacesSecretRepo(w, r, true)
}

// RemoveUserCodespacesSecretRepo handles DELETE /user/codespaces/secrets/{name}/repositories/{repository_id}
func (d *Deps) RemoveUserCodespacesSecretRepo(w http.ResponseWriter, r *http.Request) {
	d.updateUserCodespacesSecretRepo(w, r, false)
}

// ─── Secret internals ──────────────────────────────────────────────────────

func (d *Deps) listSecrets(w http.ResponseWriter, r *http.Request, repoID uint, env string) {
	secrets, err := d.Svc.ListSecrets(r.Context(), repoID, env)
	if err != nil {
		secrets = []db.Secret{}
	}
	out := make([]map[string]any, len(secrets))
	for i, s := range secrets {
		out[i] = transform.Secret(s)
	}
	respond.JSON(w, 200, map[string]any{"total_count": len(out), "secrets": out})
}

func (d *Deps) listOrgSecrets(w http.ResponseWriter, r *http.Request, orgID uint) {
	secrets, err := d.Svc.ListOrgSecrets(r.Context(), orgID)
	if err != nil {
		secrets = []db.Secret{}
	}
	out := make([]map[string]any, len(secrets))
	for i, s := range secrets {
		out[i] = transform.OrgSecret(s, pathParam(r, "org"))
	}
	respond.JSON(w, 200, map[string]any{"total_count": len(out), "secrets": out})
}

func (d *Deps) upsertSecret(w http.ResponseWriter, r *http.Request, repoID *uint, ownerID uint, env string) {
	name := pathParam(r, "name")
	var body struct {
		EncryptedValue string `json:"encrypted_value"`
		KeyID          string `json:"key_id"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}

	var value string
	if body.EncryptedValue != "" {
		decrypted, err := crypto.DecryptSecret(body.EncryptedValue)
		if err != nil {
			slog.Error("secret decrypt", "error", err)
		} else {
			value = decrypted
		}
	}

	var existing bool
	if repoID != nil {
		_, err := d.Svc.GetSecret(r.Context(), *repoID, env, name)
		existing = err == nil
	} else {
		_, err := d.Svc.GetOrgSecret(r.Context(), ownerID, name)
		existing = err == nil
	}

	err := d.Svc.UpsertSecret(r.Context(), service.UpsertSecretInput{
		OwnerID: ownerID,
		RepoID:  repoID,
		Env:     env,
		Name:    name,
		Value:   value,
	})
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	if existing {
		respond.NoContent(w)
	} else {
		respond.JSON(w, http.StatusCreated, map[string]any{})
	}
}

func (d *Deps) upsertOrgSecret(w http.ResponseWriter, r *http.Request, orgID uint) {
	name := pathParam(r, "name")
	var body struct {
		EncryptedValue        string `json:"encrypted_value"`
		KeyID                 string `json:"key_id"`
		Visibility            string `json:"visibility"`
		SelectedRepositoryIDs []uint `json:"selected_repository_ids"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}

	var value string
	if body.EncryptedValue != "" {
		decrypted, err := crypto.DecryptSecret(body.EncryptedValue)
		if err != nil {
			slog.Error("org secret decrypt", "error", err)
		} else {
			value = decrypted
		}
	}

	visibility := body.Visibility
	if visibility == "" {
		visibility = "private"
	}
	ids := make([]string, len(body.SelectedRepositoryIDs))
	for i, id := range body.SelectedRepositoryIDs {
		ids[i] = strconv.FormatUint(uint64(id), 10)
	}

	_, err := d.Svc.GetOrgSecret(r.Context(), orgID, name)
	existing := err == nil

	err = d.Svc.UpsertSecret(r.Context(), service.UpsertSecretInput{
		OwnerID:         orgID,
		RepoID:          nil,
		Env:             "",
		Name:            name,
		Value:           value,
		Visibility:      visibility,
		SelectedRepoIDs: strings.Join(ids, ","),
	})
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	if existing {
		respond.NoContent(w)
	} else {
		respond.JSON(w, http.StatusCreated, map[string]any{})
	}
}

func (d *Deps) deleteSecret(w http.ResponseWriter, r *http.Request, repoID uint, env string) {
	name := pathParam(r, "name")
	if err := d.Svc.DeleteSecret(r.Context(), repoID, env, name); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

func (d *Deps) deleteOrgSecret(w http.ResponseWriter, r *http.Request, orgID uint) {
	name := pathParam(r, "name")
	if err := d.Svc.DeleteOrgSecret(r.Context(), orgID, name); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

func publicKeyJSON() map[string]any {
	return map[string]any{
		"key_id": crypto.PublicKeyID(),
		"key":    crypto.PublicKeyBase64(),
	}
}

func (d *Deps) selectedRepos(r *http.Request, s db.Secret) []map[string]any {
	if s.SelectedRepoIDs == "" {
		return []map[string]any{}
	}
	ids := strings.Split(s.SelectedRepoIDs, ",")
	repos := make([]map[string]any, 0, len(ids))
	for _, idStr := range ids {
		repo, err := d.Svc.GetRepoByID(r.Context(), strings.TrimSpace(idStr))
		if err == nil {
			repos = append(repos, map[string]any{
				"id":        repo.ID,
				"name":      repo.Name,
				"full_name": repo.FullName,
			})
		}
	}
	return repos
}

func (d *Deps) currentUser(w http.ResponseWriter, r *http.Request) (db.User, bool) {
	user, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return db.User{}, false
	}
	return user, true
}

func (d *Deps) currentUserCodespacesSecret(w http.ResponseWriter, r *http.Request) (db.User, db.Secret, bool) {
	user, ok := d.currentUser(w, r)
	if !ok {
		return db.User{}, db.Secret{}, false
	}
	name := pathParam(r, "name")
	secret, err := d.Svc.GetOrgSecret(r.Context(), user.ID, name)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return db.User{}, db.Secret{}, false
	}
	return user, secret, true
}

func (d *Deps) updateUserCodespacesSecretRepo(w http.ResponseWriter, r *http.Request, add bool) {
	user, secret, ok := d.currentUserCodespacesSecret(w, r)
	if !ok {
		return
	}
	repoID := strings.TrimSpace(pathParam(r, "repository_id"))
	if _, err := strconv.ParseUint(repoID, 10, 64); err != nil || repoID == "" {
		respond.ValidationFailed(w, "invalid repository_id")
		return
	}
	ids := updateSelectedRepositoryID(secret.SelectedRepoIDs, repoID, add)
	if err := d.Svc.UpdateOrgSecretSelectedRepos(r.Context(), user.ID, secret.Name, ids); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

func parseSelectedRepositoryIDs(values []any) ([]string, bool) {
	if len(values) == 0 {
		return []string{}, true
	}
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		var id string
		switch v := value.(type) {
		case float64:
			if v <= 0 || v != float64(uint64(v)) {
				return nil, false
			}
			id = strconv.FormatUint(uint64(v), 10)
		case string:
			id = strings.TrimSpace(v)
			if id == "" {
				return nil, false
			}
			if _, err := strconv.ParseUint(id, 10, 64); err != nil {
				return nil, false
			}
		default:
			return nil, false
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, true
}

func updateSelectedRepositoryID(raw, id string, add bool) string {
	ids := splitSelectedRepositoryIDs(raw)
	out := make([]string, 0, len(ids)+1)
	found := false
	for _, existing := range ids {
		if existing == id {
			found = true
			if !add {
				continue
			}
		}
		out = append(out, existing)
	}
	if add && !found {
		out = append(out, id)
	}
	return strings.Join(out, ",")
}

func splitSelectedRepositoryIDs(raw string) []string {
	if raw == "" {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
