package service_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"gh-server/internal/service"
)

func TestVariableRepoAndEnvLifecycle(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "varuser", "varrepo")
	repo, err := svc.GetRepo(ctx, "varuser/varrepo")
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}
	repoID := repo.ID

	vars, err := svc.ListVariables(ctx, repoID, "")
	if err != nil {
		t.Fatalf("ListVariables(repo) failed: %v", err)
	}
	if len(vars) != 0 {
		t.Fatalf("expected 0 repo variables, got %d", len(vars))
	}

	if _, err := svc.CreateVariable(ctx, repo.OwnerID, &repoID, "", "VAR1", "value1"); err != nil {
		t.Fatalf("CreateVariable(repo) failed: %v", err)
	}

	got, err := svc.GetVariable(ctx, repoID, "", "VAR1")
	if err != nil {
		t.Fatalf("GetVariable(repo) failed: %v", err)
	}
	if got.Value != "value1" {
		t.Fatalf("expected VAR1=value1, got %q", got.Value)
	}

	if err := svc.UpdateVariable(ctx, repoID, "", "VAR1", "value2"); err != nil {
		t.Fatalf("UpdateVariable(repo) failed: %v", err)
	}
	got, err = svc.GetVariable(ctx, repoID, "", "VAR1")
	if err != nil {
		t.Fatalf("GetVariable(repo) after update failed: %v", err)
	}
	if got.Value != "value2" {
		t.Fatalf("expected VAR1=value2 after update, got %q", got.Value)
	}

	if _, err := svc.CreateVariable(ctx, repo.OwnerID, &repoID, "production", "VAR2", "env1"); err != nil {
		t.Fatalf("CreateVariable(env) failed: %v", err)
	}

	vars, err = svc.ListVariables(ctx, repoID, "production")
	if err != nil {
		t.Fatalf("ListVariables(env) failed: %v", err)
	}
	if len(vars) != 1 || vars[0].Name != "VAR2" {
		t.Fatalf("expected 1 env variable VAR2, got %v", vars)
	}

	vars, err = svc.ListVariables(ctx, repoID, "")
	if err != nil {
		t.Fatalf("ListVariables(repo) after env create failed: %v", err)
	}
	if len(vars) != 1 || vars[0].Name != "VAR1" {
		t.Fatalf("expected 1 repo variable VAR1, got %v", vars)
	}

	if err := svc.DeleteVariable(ctx, repoID, "production", "VAR2"); err != nil {
		t.Fatalf("DeleteVariable(env) failed: %v", err)
	}
	if _, err := svc.GetVariable(ctx, repoID, "production", "VAR2"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after env delete, got %v", err)
	}

	if err := svc.DeleteVariable(ctx, repoID, "", "VAR1"); err != nil {
		t.Fatalf("DeleteVariable(repo) failed: %v", err)
	}
	if _, err := svc.GetVariable(ctx, repoID, "", "VAR1"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after repo delete, got %v", err)
	}
}

func TestEnvironmentLifecycleAndCascadeDelete(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "envuser", "envrepo")
	repo, err := svc.GetRepo(ctx, "envuser/envrepo")
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}
	repoID := repo.ID

	env, created, err := svc.UpsertEnvironment(ctx, service.UpsertEnvironmentInput{
		RepositoryID: repoID,
		Name:         "production",
		ProtectionRules: []map[string]any{
			{"type": "wait_timer", "wait_timer": 30},
		},
		DeploymentBranchPolicy: &service.EnvironmentPolicy{
			ProtectedBranches: true,
		},
	})
	if err != nil {
		t.Fatalf("UpsertEnvironment(create) failed: %v", err)
	}
	if !created {
		t.Fatalf("expected initial upsert to create environment")
	}
	if env.Name != "production" {
		t.Fatalf("expected environment name production, got %q", env.Name)
	}

	if _, err := svc.CreateVariable(ctx, repo.OwnerID, &repoID, "production", "ENV_VAR", "value"); err != nil {
		t.Fatalf("CreateVariable(env) failed: %v", err)
	}
	if err := svc.UpsertSecret(ctx, service.UpsertSecretInput{
		OwnerID: repo.OwnerID,
		RepoID:  &repoID,
		Env:     "production",
		Name:    "ENV_SECRET",
		Value:   "secret",
	}); err != nil {
		t.Fatalf("UpsertSecret(env) failed: %v", err)
	}

	envs, err := svc.ListEnvironments(ctx, repoID)
	if err != nil {
		t.Fatalf("ListEnvironments failed: %v", err)
	}
	if len(envs) != 1 || envs[0].Name != "production" {
		t.Fatalf("expected production environment, got %+v", envs)
	}

	got, err := svc.GetEnvironment(ctx, repoID, "production")
	if err != nil {
		t.Fatalf("GetEnvironment failed: %v", err)
	}
	if got.ID != env.ID {
		t.Fatalf("expected same environment id %d, got %d", env.ID, got.ID)
	}

	if err := svc.DeleteEnvironment(ctx, repoID, "production"); err != nil {
		t.Fatalf("DeleteEnvironment failed: %v", err)
	}
	if _, err := svc.GetEnvironment(ctx, repoID, "production"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after environment delete, got %v", err)
	}
	if _, err := svc.GetVariable(ctx, repoID, "production", "ENV_VAR"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected env variable cascade delete, got %v", err)
	}
	if _, err := svc.GetSecret(ctx, repoID, "production", "ENV_SECRET"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected env secret cascade delete, got %v", err)
	}
	if err := svc.DeleteEnvironment(ctx, repoID, "production"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound deleting missing environment, got %v", err)
	}
}

func TestUpsertEnvironmentRejectsInvalidDeploymentPolicy(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "envpolicyuser", "envpolicyrepo")
	repo, err := svc.GetRepo(ctx, "envpolicyuser/envpolicyrepo")
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}

	_, _, err = svc.UpsertEnvironment(ctx, service.UpsertEnvironmentInput{
		RepositoryID: repo.ID,
		Name:         "production",
		DeploymentBranchPolicy: &service.EnvironmentPolicy{
			ProtectedBranches:    true,
			CustomBranchPolicies: true,
		},
	})
	if !errors.Is(err, service.ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}

	_, _, err = svc.UpsertEnvironment(ctx, service.UpsertEnvironmentInput{
		RepositoryID:           repo.ID,
		Name:                   "staging",
		DeploymentBranchPolicy: &service.EnvironmentPolicy{},
	})
	if !errors.Is(err, service.ErrValidation) {
		t.Fatalf("expected ErrValidation for empty deployment policy, got %v", err)
	}
}

func TestVariableOrgLifecycle(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	org, err := svc.EnsureOrg(ctx, "varorg")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}

	vars, err := svc.ListOrgVariables(ctx, org.ID)
	if err != nil {
		t.Fatalf("ListOrgVariables failed: %v", err)
	}
	if len(vars) != 0 {
		t.Fatalf("expected 0 org variables, got %d", len(vars))
	}

	if _, err := svc.CreateVariable(ctx, org.ID, nil, "", "ORG_VAR", "org1"); err != nil {
		t.Fatalf("CreateVariable(org) failed: %v", err)
	}

	got, err := svc.GetOrgVariable(ctx, org.ID, "ORG_VAR")
	if err != nil {
		t.Fatalf("GetOrgVariable failed: %v", err)
	}
	if got.Value != "org1" {
		t.Fatalf("expected ORG_VAR=org1, got %q", got.Value)
	}

	if err := svc.UpdateOrgVariable(ctx, org.ID, "ORG_VAR", "org2"); err != nil {
		t.Fatalf("UpdateOrgVariable failed: %v", err)
	}
	got, err = svc.GetOrgVariable(ctx, org.ID, "ORG_VAR")
	if err != nil {
		t.Fatalf("GetOrgVariable after update failed: %v", err)
	}
	if got.Value != "org2" {
		t.Fatalf("expected ORG_VAR=org2 after update, got %q", got.Value)
	}

	vars, err = svc.ListOrgVariables(ctx, org.ID)
	if err != nil {
		t.Fatalf("ListOrgVariables after update failed: %v", err)
	}
	if len(vars) != 1 || vars[0].Name != "ORG_VAR" {
		t.Fatalf("expected 1 org variable ORG_VAR, got %v", vars)
	}

	if err := svc.DeleteOrgVariable(ctx, org.ID, "ORG_VAR"); err != nil {
		t.Fatalf("DeleteOrgVariable failed: %v", err)
	}
	if _, err := svc.GetOrgVariable(ctx, org.ID, "ORG_VAR"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after org delete, got %v", err)
	}
}

func TestSecretRepoAndEnvLifecycle(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "secuser", "secrepo")
	repo, err := svc.GetRepo(ctx, "secuser/secrepo")
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}
	repoID := repo.ID

	secrets, err := svc.ListSecrets(ctx, repoID, "")
	if err != nil {
		t.Fatalf("ListSecrets(repo) failed: %v", err)
	}
	if len(secrets) != 0 {
		t.Fatalf("expected 0 repo secrets, got %d", len(secrets))
	}

	if err := svc.UpsertSecret(ctx, service.UpsertSecretInput{
		OwnerID:    repo.OwnerID,
		RepoID:     &repoID,
		Env:        "",
		Name:       "SECRET1",
		Value:      "s1",
		Visibility: "private",
	}); err != nil {
		t.Fatalf("UpsertSecret(repo create) failed: %v", err)
	}

	got, err := svc.GetSecret(ctx, repoID, "", "SECRET1")
	if err != nil {
		t.Fatalf("GetSecret(repo) failed: %v", err)
	}
	if got.Value != "s1" {
		t.Fatalf("expected SECRET1=s1, got %q", got.Value)
	}

	if err := svc.UpsertSecret(ctx, service.UpsertSecretInput{
		OwnerID:         repo.OwnerID,
		RepoID:          &repoID,
		Env:             "",
		Name:            "SECRET1",
		Value:           "s2",
		Visibility:      "all",
		SelectedRepoIDs: "1,2",
	}); err != nil {
		t.Fatalf("UpsertSecret(repo update) failed: %v", err)
	}

	got, err = svc.GetSecret(ctx, repoID, "", "SECRET1")
	if err != nil {
		t.Fatalf("GetSecret(repo) after update failed: %v", err)
	}
	if got.Value != "s2" {
		t.Fatalf("expected SECRET1=s2 after update, got %q", got.Value)
	}
	if got.Visibility != "all" {
		t.Fatalf("expected visibility=all after update, got %q", got.Visibility)
	}
	if got.SelectedRepoIDs != "1,2" {
		t.Fatalf("expected selected_repo_ids=1,2 after update, got %q", got.SelectedRepoIDs)
	}

	if err := svc.UpsertSecret(ctx, service.UpsertSecretInput{
		OwnerID:         repo.OwnerID,
		RepoID:          &repoID,
		Env:             "",
		Name:            "SECRET1",
		Value:           "",
		Visibility:      "selected",
		SelectedRepoIDs: "3",
	}); err != nil {
		t.Fatalf("UpsertSecret(repo update no value) failed: %v", err)
	}

	got, err = svc.GetSecret(ctx, repoID, "", "SECRET1")
	if err != nil {
		t.Fatalf("GetSecret(repo) after update no value failed: %v", err)
	}
	if got.Value != "s2" {
		t.Fatalf("expected SECRET1 to retain value s2, got %q", got.Value)
	}
	if got.Visibility != "selected" {
		t.Fatalf("expected visibility=selected after update no value, got %q", got.Visibility)
	}
	if got.SelectedRepoIDs != "3" {
		t.Fatalf("expected selected_repo_ids=3 after update no value, got %q", got.SelectedRepoIDs)
	}

	if err := svc.UpsertSecret(ctx, service.UpsertSecretInput{
		OwnerID:    repo.OwnerID,
		RepoID:     &repoID,
		Env:        "production",
		Name:       "ENV_SECRET",
		Value:      "env1",
		Visibility: "private",
	}); err != nil {
		t.Fatalf("UpsertSecret(env create) failed: %v", err)
	}

	secrets, err = svc.ListSecrets(ctx, repoID, "production")
	if err != nil {
		t.Fatalf("ListSecrets(env) failed: %v", err)
	}
	if len(secrets) != 1 || secrets[0].Name != "ENV_SECRET" {
		t.Fatalf("expected 1 env secret ENV_SECRET, got %v", secrets)
	}

	got, err = svc.GetSecret(ctx, repoID, "production", "ENV_SECRET")
	if err != nil {
		t.Fatalf("GetSecret(env) failed: %v", err)
	}
	if got.Value != "env1" {
		t.Fatalf("expected ENV_SECRET=env1, got %q", got.Value)
	}

	if err := svc.DeleteSecret(ctx, repoID, "production", "ENV_SECRET"); err != nil {
		t.Fatalf("DeleteSecret(env) failed: %v", err)
	}
	if _, err := svc.GetSecret(ctx, repoID, "production", "ENV_SECRET"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after env secret delete, got %v", err)
	}

	if err := svc.DeleteSecret(ctx, repoID, "", "SECRET1"); err != nil {
		t.Fatalf("DeleteSecret(repo) failed: %v", err)
	}
	if _, err := svc.GetSecret(ctx, repoID, "", "SECRET1"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after repo secret delete, got %v", err)
	}
}

func TestSecretOrgLifecycle(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	org, err := svc.EnsureOrg(ctx, "secorg")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}

	repo1, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: org.Login, Name: "orgrepo1"})
	if err != nil {
		t.Fatalf("CreateRepo(orgrepo1) failed: %v", err)
	}
	repo2, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: org.Login, Name: "orgrepo2"})
	if err != nil {
		t.Fatalf("CreateRepo(orgrepo2) failed: %v", err)
	}
	selected := fmt.Sprintf("%d,%d", repo1.ID, repo2.ID)

	secrets, err := svc.ListOrgSecrets(ctx, org.ID)
	if err != nil {
		t.Fatalf("ListOrgSecrets failed: %v", err)
	}
	if len(secrets) != 0 {
		t.Fatalf("expected 0 org secrets, got %d", len(secrets))
	}

	if err := svc.UpsertSecret(ctx, service.UpsertSecretInput{
		OwnerID:         org.ID,
		RepoID:          nil,
		Env:             "",
		Name:            "ORG_SECRET",
		Value:           "org1",
		Visibility:      "selected",
		SelectedRepoIDs: selected,
	}); err != nil {
		t.Fatalf("UpsertSecret(org create) failed: %v", err)
	}

	got, err := svc.GetOrgSecret(ctx, org.ID, "ORG_SECRET")
	if err != nil {
		t.Fatalf("GetOrgSecret failed: %v", err)
	}
	if got.Value != "org1" {
		t.Fatalf("expected ORG_SECRET=org1, got %q", got.Value)
	}
	if got.Visibility != "selected" {
		t.Fatalf("expected visibility=selected, got %q", got.Visibility)
	}
	if got.SelectedRepoIDs != selected {
		t.Fatalf("expected selected_repo_ids=%q, got %q", selected, got.SelectedRepoIDs)
	}

	secrets, err = svc.ListOrgSecrets(ctx, org.ID)
	if err != nil {
		t.Fatalf("ListOrgSecrets after create failed: %v", err)
	}
	if len(secrets) != 1 || secrets[0].Name != "ORG_SECRET" {
		t.Fatalf("expected 1 org secret ORG_SECRET, got %v", secrets)
	}

	newSelected := fmt.Sprintf("%d", repo2.ID)
	if err := svc.UpdateOrgSecretSelectedRepos(ctx, org.ID, "ORG_SECRET", newSelected); err != nil {
		t.Fatalf("UpdateOrgSecretSelectedRepos failed: %v", err)
	}

	got, err = svc.GetOrgSecret(ctx, org.ID, "ORG_SECRET")
	if err != nil {
		t.Fatalf("GetOrgSecret after selected repo update failed: %v", err)
	}
	if got.SelectedRepoIDs != newSelected {
		t.Fatalf("expected selected_repo_ids=%q after update, got %q", newSelected, got.SelectedRepoIDs)
	}
	if got.Visibility != "selected" {
		t.Fatalf("expected visibility to remain selected, got %q", got.Visibility)
	}

	if err := svc.DeleteOrgSecret(ctx, org.ID, "ORG_SECRET"); err != nil {
		t.Fatalf("DeleteOrgSecret failed: %v", err)
	}
	if _, err := svc.GetOrgSecret(ctx, org.ID, "ORG_SECRET"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after org secret delete, got %v", err)
	}
}

// -------- Scope Enforcement Tests --------

func TestVariableScopeEnforcement(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "scopeuser", "scoperepo")
	repo, err := svc.GetRepo(ctx, "scopeuser/scoperepo")
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}
	repoID := repo.ID

	// Create variables at different scopes
	if _, err := svc.CreateVariable(ctx, repo.OwnerID, &repoID, "", "REPO_VAR", "repo_value"); err != nil {
		t.Fatalf("CreateVariable(repo) failed: %v", err)
	}
	if _, err := svc.CreateVariable(ctx, repo.OwnerID, &repoID, "production", "PROD_VAR", "prod_value"); err != nil {
		t.Fatalf("CreateVariable(production) failed: %v", err)
	}
	if _, err := svc.CreateVariable(ctx, repo.OwnerID, &repoID, "staging", "STAGING_VAR", "staging_value"); err != nil {
		t.Fatalf("CreateVariable(staging) failed: %v", err)
	}

	// Verify repo-level list only returns repo-level variable
	vars, err := svc.ListVariables(ctx, repoID, "")
	if err != nil {
		t.Fatalf("ListVariables(repo) failed: %v", err)
	}
	if len(vars) != 1 || vars[0].Name != "REPO_VAR" {
		t.Fatalf("expected only REPO_VAR at repo scope, got %v", vars)
	}

	// Verify production scope only returns production variable
	vars, err = svc.ListVariables(ctx, repoID, "production")
	if err != nil {
		t.Fatalf("ListVariables(production) failed: %v", err)
	}
	if len(vars) != 1 || vars[0].Name != "PROD_VAR" {
		t.Fatalf("expected only PROD_VAR at production scope, got %v", vars)
	}

	// Verify staging scope only returns staging variable
	vars, err = svc.ListVariables(ctx, repoID, "staging")
	if err != nil {
		t.Fatalf("ListVariables(staging) failed: %v", err)
	}
	if len(vars) != 1 || vars[0].Name != "STAGING_VAR" {
		t.Fatalf("expected only STAGING_VAR at staging scope, got %v", vars)
	}

	// Verify GetVariable enforces scope - repo var not found in production
	if _, err := svc.GetVariable(ctx, repoID, "production", "REPO_VAR"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for repo var in production scope, got %v", err)
	}

	// Verify GetVariable enforces scope - production var not found in repo scope
	if _, err := svc.GetVariable(ctx, repoID, "", "PROD_VAR"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for production var in repo scope, got %v", err)
	}

	// Verify UpdateVariable enforces scope - cannot update repo var via production scope
	if err := svc.UpdateVariable(ctx, repoID, "production", "REPO_VAR", "hacked"); err == nil {
		t.Fatalf("expected error when updating repo var via production scope, got nil")
	}

	// Verify repo var value unchanged
	got, err := svc.GetVariable(ctx, repoID, "", "REPO_VAR")
	if err != nil {
		t.Fatalf("GetVariable(repo) failed: %v", err)
	}
	if got.Value != "repo_value" {
		t.Fatalf("expected REPO_VAR unchanged, got %q", got.Value)
	}
}

func TestSecretScopeEnforcement(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "secscopeuser", "secscoperepo")
	repo, err := svc.GetRepo(ctx, "secscopeuser/secscoperepo")
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}
	repoID := repo.ID

	// Create secrets at different scopes
	if err := svc.UpsertSecret(ctx, service.UpsertSecretInput{
		OwnerID:    repo.OwnerID,
		RepoID:     &repoID,
		Env:        "",
		Name:       "REPO_SECRET",
		Value:      "repo_secret_value",
		Visibility: "private",
	}); err != nil {
		t.Fatalf("UpsertSecret(repo) failed: %v", err)
	}
	if err := svc.UpsertSecret(ctx, service.UpsertSecretInput{
		OwnerID:    repo.OwnerID,
		RepoID:     &repoID,
		Env:        "production",
		Name:       "PROD_SECRET",
		Value:      "prod_secret_value",
		Visibility: "private",
	}); err != nil {
		t.Fatalf("UpsertSecret(production) failed: %v", err)
	}

	// Verify repo-level list only returns repo-level secret
	secrets, err := svc.ListSecrets(ctx, repoID, "")
	if err != nil {
		t.Fatalf("ListSecrets(repo) failed: %v", err)
	}
	if len(secrets) != 1 || secrets[0].Name != "REPO_SECRET" {
		t.Fatalf("expected only REPO_SECRET at repo scope, got %v", secrets)
	}

	// Verify production scope only returns production secret
	secrets, err = svc.ListSecrets(ctx, repoID, "production")
	if err != nil {
		t.Fatalf("ListSecrets(production) failed: %v", err)
	}
	if len(secrets) != 1 || secrets[0].Name != "PROD_SECRET" {
		t.Fatalf("expected only PROD_SECRET at production scope, got %v", secrets)
	}

	// Verify GetSecret enforces scope - repo secret not found in production
	if _, err := svc.GetSecret(ctx, repoID, "production", "REPO_SECRET"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for repo secret in production scope, got %v", err)
	}

	// Verify GetSecret enforces scope - production secret not found in repo scope
	if _, err := svc.GetSecret(ctx, repoID, "", "PROD_SECRET"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for production secret in repo scope, got %v", err)
	}

	// Verify DeleteSecret enforces scope - cannot delete repo secret via production scope
	if err := svc.DeleteSecret(ctx, repoID, "production", "REPO_SECRET"); err == nil {
		t.Fatalf("expected error when deleting repo secret via production scope, got nil")
	}

	// Verify repo secret still exists
	if _, err := svc.GetSecret(ctx, repoID, "", "REPO_SECRET"); err != nil {
		t.Fatalf("expected REPO_SECRET to still exist, got error: %v", err)
	}
}

func TestVariableOrgScopeEnforcement(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	org1, err := svc.EnsureOrg(ctx, "scopeorg1")
	if err != nil {
		t.Fatalf("EnsureOrg(org1) failed: %v", err)
	}
	org2, err := svc.EnsureOrg(ctx, "scopeorg2")
	if err != nil {
		t.Fatalf("EnsureOrg(org2) failed: %v", err)
	}

	// Create org variables
	if _, err := svc.CreateVariable(ctx, org1.ID, nil, "", "ORG1_VAR", "org1_value"); err != nil {
		t.Fatalf("CreateVariable(org1) failed: %v", err)
	}
	if _, err := svc.CreateVariable(ctx, org2.ID, nil, "", "ORG2_VAR", "org2_value"); err != nil {
		t.Fatalf("CreateVariable(org2) failed: %v", err)
	}

	// Verify org1 cannot see org2's variable
	vars, err := svc.ListOrgVariables(ctx, org1.ID)
	if err != nil {
		t.Fatalf("ListOrgVariables(org1) failed: %v", err)
	}
	if len(vars) != 1 || vars[0].Name != "ORG1_VAR" {
		t.Fatalf("expected only ORG1_VAR, got %v", vars)
	}

	// Verify GetOrgVariable enforces org scope
	if _, err := svc.GetOrgVariable(ctx, org1.ID, "ORG2_VAR"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for org2 var in org1 scope, got %v", err)
	}

	// Verify UpdateOrgVariable enforces org scope
	if err := svc.UpdateOrgVariable(ctx, org1.ID, "ORG2_VAR", "hacked"); err == nil {
		t.Fatalf("expected error when updating org2 var via org1 scope, got nil")
	}

	// Verify org2 var value unchanged
	got, err := svc.GetOrgVariable(ctx, org2.ID, "ORG2_VAR")
	if err != nil {
		t.Fatalf("GetOrgVariable(org2) failed: %v", err)
	}
	if got.Value != "org2_value" {
		t.Fatalf("expected ORG2_VAR unchanged, got %q", got.Value)
	}
}

func TestSecretOrgScopeEnforcement(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	org1, err := svc.EnsureOrg(ctx, "secscopeorg1")
	if err != nil {
		t.Fatalf("EnsureOrg(org1) failed: %v", err)
	}
	org2, err := svc.EnsureOrg(ctx, "secscopeorg2")
	if err != nil {
		t.Fatalf("EnsureOrg(org2) failed: %v", err)
	}

	// Create org secrets
	if err := svc.UpsertSecret(ctx, service.UpsertSecretInput{
		OwnerID:    org1.ID,
		RepoID:     nil,
		Env:        "",
		Name:       "ORG1_SECRET",
		Value:      "org1_secret",
		Visibility: "private",
	}); err != nil {
		t.Fatalf("UpsertSecret(org1) failed: %v", err)
	}
	if err := svc.UpsertSecret(ctx, service.UpsertSecretInput{
		OwnerID:    org2.ID,
		RepoID:     nil,
		Env:        "",
		Name:       "ORG2_SECRET",
		Value:      "org2_secret",
		Visibility: "private",
	}); err != nil {
		t.Fatalf("UpsertSecret(org2) failed: %v", err)
	}

	// Verify org1 cannot see org2's secret
	secrets, err := svc.ListOrgSecrets(ctx, org1.ID)
	if err != nil {
		t.Fatalf("ListOrgSecrets(org1) failed: %v", err)
	}
	if len(secrets) != 1 || secrets[0].Name != "ORG1_SECRET" {
		t.Fatalf("expected only ORG1_SECRET, got %v", secrets)
	}

	// Verify GetOrgSecret enforces org scope
	if _, err := svc.GetOrgSecret(ctx, org1.ID, "ORG2_SECRET"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for org2 secret in org1 scope, got %v", err)
	}

	// Verify DeleteOrgSecret enforces org scope
	if err := svc.DeleteOrgSecret(ctx, org1.ID, "ORG2_SECRET"); err == nil {
		t.Fatalf("expected error when deleting org2 secret via org1 scope, got nil")
	}

	// Verify org2 secret still exists
	if _, err := svc.GetOrgSecret(ctx, org2.ID, "ORG2_SECRET"); err != nil {
		t.Fatalf("expected ORG2_SECRET to still exist, got error: %v", err)
	}
}

// -------- Empty-Update Tests (No-Op/Clear Semantics) --------

func TestVariableEmptyUpdateSemantics(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "emptyuser", "emptyrepo")
	repo, err := svc.GetRepo(ctx, "emptyuser/emptyrepo")
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}
	repoID := repo.ID

	// Create a variable with initial value
	if _, err := svc.CreateVariable(ctx, repo.OwnerID, &repoID, "", "EMPTY_TEST", "initial_value"); err != nil {
		t.Fatalf("CreateVariable failed: %v", err)
	}

	// Update with empty string - should clear the value (intentional semantics)
	if err := svc.UpdateVariable(ctx, repoID, "", "EMPTY_TEST", ""); err != nil {
		t.Fatalf("UpdateVariable with empty value failed: %v", err)
	}

	got, err := svc.GetVariable(ctx, repoID, "", "EMPTY_TEST")
	if err != nil {
		t.Fatalf("GetVariable after empty update failed: %v", err)
	}
	if got.Value != "" {
		t.Fatalf("expected empty value after empty update, got %q", got.Value)
	}

	// Verify updating with same value is a no-op (idempotent)
	if err := svc.UpdateVariable(ctx, repoID, "", "EMPTY_TEST", ""); err != nil {
		t.Fatalf("UpdateVariable with same empty value failed: %v", err)
	}

	got, err = svc.GetVariable(ctx, repoID, "", "EMPTY_TEST")
	if err != nil {
		t.Fatalf("GetVariable after no-op update failed: %v", err)
	}
	if got.Value != "" {
		t.Fatalf("expected value to remain empty after no-op update, got %q", got.Value)
	}
}

func TestOrgVariableEmptyUpdateSemantics(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	org, err := svc.EnsureOrg(ctx, "emptyorg")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}

	// Create an org variable with initial value
	if _, err := svc.CreateVariable(ctx, org.ID, nil, "", "ORG_EMPTY_TEST", "org_initial"); err != nil {
		t.Fatalf("CreateVariable(org) failed: %v", err)
	}

	// Update with empty string - should clear the value
	if err := svc.UpdateOrgVariable(ctx, org.ID, "ORG_EMPTY_TEST", ""); err != nil {
		t.Fatalf("UpdateOrgVariable with empty value failed: %v", err)
	}

	got, err := svc.GetOrgVariable(ctx, org.ID, "ORG_EMPTY_TEST")
	if err != nil {
		t.Fatalf("GetOrgVariable after empty update failed: %v", err)
	}
	if got.Value != "" {
		t.Fatalf("expected empty value after empty update, got %q", got.Value)
	}
}

func TestSecretEmptyUpdateSemantics(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "secemptyuser", "secemptyrepo")
	repo, err := svc.GetRepo(ctx, "secemptyuser/secemptyrepo")
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}
	repoID := repo.ID

	// Create a secret with initial value
	if err := svc.UpsertSecret(ctx, service.UpsertSecretInput{
		OwnerID:    repo.OwnerID,
		RepoID:     &repoID,
		Env:        "",
		Name:       "EMPTY_SECRET",
		Value:      "secret_initial",
		Visibility: "private",
	}); err != nil {
		t.Fatalf("UpsertSecret(create) failed: %v", err)
	}

	// Upsert with empty value - should NOT clear the value (intentional: empty value means no change)
	if err := svc.UpsertSecret(ctx, service.UpsertSecretInput{
		OwnerID:    repo.OwnerID,
		RepoID:     &repoID,
		Env:        "",
		Name:       "EMPTY_SECRET",
		Value:      "",
		Visibility: "private",
	}); err != nil {
		t.Fatalf("UpsertSecret with empty value failed: %v", err)
	}

	got, err := svc.GetSecret(ctx, repoID, "", "EMPTY_SECRET")
	if err != nil {
		t.Fatalf("GetSecret after empty upsert failed: %v", err)
	}
	if got.Value != "secret_initial" {
		t.Fatalf("expected value to remain 'secret_initial' after empty upsert (no-op semantics), got %q", got.Value)
	}

	// Verify we can still update other fields without changing value
	if err := svc.UpsertSecret(ctx, service.UpsertSecretInput{
		OwnerID:    repo.OwnerID,
		RepoID:     &repoID,
		Env:        "",
		Name:       "EMPTY_SECRET",
		Value:      "",
		Visibility: "all",
	}); err != nil {
		t.Fatalf("UpsertSecret update visibility failed: %v", err)
	}

	got, err = svc.GetSecret(ctx, repoID, "", "EMPTY_SECRET")
	if err != nil {
		t.Fatalf("GetSecret after visibility update failed: %v", err)
	}
	if got.Value != "secret_initial" {
		t.Fatalf("expected value to remain 'secret_initial', got %q", got.Value)
	}
	if got.Visibility != "all" {
		t.Fatalf("expected visibility to be 'all', got %q", got.Visibility)
	}
}

// -------- Not-Found Mapping Tests (Stable API Errors) --------

func TestVariableNotFoundErrors(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "notfounduser", "notfoundrepo")
	repo, err := svc.GetRepo(ctx, "notfounduser/notfoundrepo")
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}
	repoID := repo.ID

	// GetVariable on non-existent variable returns ErrNotFound
	if _, err := svc.GetVariable(ctx, repoID, "", "NONEXISTENT"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-existent repo variable, got %v", err)
	}

	if _, err := svc.GetVariable(ctx, repoID, "production", "NONEXISTENT"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-existent env variable, got %v", err)
	}

	// UpdateVariable on non-existent variable returns error (checkAffected returns error for 0 rows)
	if err := svc.UpdateVariable(ctx, repoID, "", "NONEXISTENT", "value"); err == nil {
		t.Fatalf("expected error when updating non-existent variable, got nil")
	}

	// DeleteVariable on non-existent variable returns error (checkAffected returns error for 0 rows)
	if err := svc.DeleteVariable(ctx, repoID, "", "NONEXISTENT"); err == nil {
		t.Fatalf("expected error when deleting non-existent variable, got nil")
	}
}

func TestOrgVariableNotFoundErrors(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	org, err := svc.EnsureOrg(ctx, "notfoundorg")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}

	// GetOrgVariable on non-existent variable returns ErrNotFound
	if _, err := svc.GetOrgVariable(ctx, org.ID, "NONEXISTENT"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-existent org variable, got %v", err)
	}

	// UpdateOrgVariable on non-existent variable returns error
	if err := svc.UpdateOrgVariable(ctx, org.ID, "NONEXISTENT", "value"); err == nil {
		t.Fatalf("expected error when updating non-existent org variable, got nil")
	}

	// DeleteOrgVariable on non-existent variable returns error
	if err := svc.DeleteOrgVariable(ctx, org.ID, "NONEXISTENT"); err == nil {
		t.Fatalf("expected error when deleting non-existent org variable, got nil")
	}
}

func TestSecretNotFoundErrors(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "secnotfounduser", "secnotfoundrepo")
	repo, err := svc.GetRepo(ctx, "secnotfounduser/secnotfoundrepo")
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}
	repoID := repo.ID

	// GetSecret on non-existent secret returns ErrNotFound
	if _, err := svc.GetSecret(ctx, repoID, "", "NONEXISTENT"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-existent repo secret, got %v", err)
	}

	if _, err := svc.GetSecret(ctx, repoID, "production", "NONEXISTENT"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-existent env secret, got %v", err)
	}

	// DeleteSecret on non-existent secret returns error (checkAffected returns error for 0 rows)
	if err := svc.DeleteSecret(ctx, repoID, "", "NONEXISTENT"); err == nil {
		t.Fatalf("expected error when deleting non-existent secret, got nil")
	}
}

func TestOrgSecretNotFoundErrors(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	org, err := svc.EnsureOrg(ctx, "secnotfoundorg")
	if err != nil {
		t.Fatalf("EnsureOrg failed: %v", err)
	}

	// GetOrgSecret on non-existent secret returns ErrNotFound
	if _, err := svc.GetOrgSecret(ctx, org.ID, "NONEXISTENT"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-existent org secret, got %v", err)
	}

	// DeleteOrgSecret on non-existent secret returns error
	if err := svc.DeleteOrgSecret(ctx, org.ID, "NONEXISTENT"); err == nil {
		t.Fatalf("expected error when deleting non-existent org secret, got nil")
	}

	// UpdateOrgSecretSelectedRepos on non-existent secret returns error
	if err := svc.UpdateOrgSecretSelectedRepos(ctx, org.ID, "NONEXISTENT", "1,2"); err == nil {
		t.Fatalf("expected error when updating non-existent org secret, got nil")
	}
}
