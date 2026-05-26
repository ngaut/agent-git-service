package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
)

// repoScope adds WHERE clauses for repository-scoped variables/secrets.
func repoScope(q *gorm.DB, repoID uint, env string) *gorm.DB {
	return q.Where("repository_id = ? AND environment = ?", repoID, env)
}

// orgScope adds WHERE clauses for organization-scoped variables/secrets.
func orgScope(q *gorm.DB, orgID uint) *gorm.DB {
	return q.Where("owner_id = ? AND repository_id IS NULL AND environment = ''", orgID)
}

// EnvironmentPolicy stores deployment branch policy flags.
type EnvironmentPolicy struct {
	ProtectedBranches    bool `json:"protected_branches"`
	CustomBranchPolicies bool `json:"custom_branch_policies"`
}

// UpsertEnvironmentInput holds parameters for creating or updating an environment.
type UpsertEnvironmentInput struct {
	RepositoryID           uint
	Name                   string
	ProtectionRules        []map[string]any
	DeploymentBranchPolicy *EnvironmentPolicy
}

func (in UpsertEnvironmentInput) Validate() error {
	if in.RepositoryID == 0 {
		return fmt.Errorf("%w: repository_id is required", ErrValidation)
	}
	if in.Name == "" {
		return fmt.Errorf("%w: name is required", ErrValidation)
	}
	if in.DeploymentBranchPolicy != nil {
		protected := in.DeploymentBranchPolicy.ProtectedBranches
		custom := in.DeploymentBranchPolicy.CustomBranchPolicies
		if protected == custom {
			return fmt.Errorf("%w: deployment_branch_policy must set exactly one of protected_branches or custom_branch_policies to true", ErrValidation)
		}
	}
	return nil
}

func marshalJSON(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// UpsertEnvironment creates or updates a repository environment.
func (s *Service) UpsertEnvironment(ctx context.Context, in UpsertEnvironmentInput) (db.Environment, bool, error) {
	if err := in.Validate(); err != nil {
		return db.Environment{}, false, err
	}
	protectionRulesJSON, err := marshalJSON(in.ProtectionRules)
	if err != nil {
		return db.Environment{}, false, err
	}
	deploymentPolicyJSON, err := marshalJSON(in.DeploymentBranchPolicy)
	if err != nil {
		return db.Environment{}, false, err
	}

	var (
		env     db.Environment
		created bool
	)
	err = s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("repository_id = ? AND name = ?", in.RepositoryID, in.Name).First(&env).Error
		if err != nil {
			if err != gorm.ErrRecordNotFound {
				return err
			}
			env = db.Environment{
				RepositoryID:         in.RepositoryID,
				Name:                 in.Name,
				ProtectionRulesJSON:  protectionRulesJSON,
				DeploymentPolicyJSON: deploymentPolicyJSON,
			}
			created = true
			return tx.Create(&env).Error
		}
		env.ProtectionRulesJSON = protectionRulesJSON
		env.DeploymentPolicyJSON = deploymentPolicyJSON
		return tx.Save(&env).Error
	})
	if err != nil {
		return db.Environment{}, false, err
	}
	return env, created, nil
}

// ListEnvironments retrieves repository environments.
func (s *Service) ListEnvironments(ctx context.Context, repoID uint) ([]db.Environment, error) {
	var envs []db.Environment
	if err := s.DBForCtx(ctx).Where("repository_id = ?", repoID).Order("name asc").Find(&envs).Error; err != nil {
		return nil, err
	}
	return envs, nil
}

// GetEnvironment retrieves a repository environment by name.
func (s *Service) GetEnvironment(ctx context.Context, repoID uint, name string) (db.Environment, error) {
	var env db.Environment
	if err := s.DBForCtx(ctx).Where("repository_id = ? AND name = ?", repoID, name).First(&env).Error; err != nil {
		return db.Environment{}, wrapErr(err)
	}
	return env, nil
}

// DeleteEnvironment deletes an environment and any env-scoped variables and secrets.
func (s *Service) DeleteEnvironment(ctx context.Context, repoID uint, name string) error {
	return s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		if err := checkAffected(tx.Where("repository_id = ? AND name = ?", repoID, name).Delete(&db.Environment{})); err != nil {
			return err
		}
		if err := tx.Where("repository_id = ? AND environment = ?", repoID, name).Delete(&db.Variable{}).Error; err != nil {
			return err
		}
		if err := tx.Where("repository_id = ? AND environment = ?", repoID, name).Delete(&db.Secret{}).Error; err != nil {
			return err
		}
		return nil
	})
}

// -------- Variables --------

// ListVariables retrieves variables for a given repository and environment.
func (s *Service) ListVariables(ctx context.Context, repoID uint, env string) ([]db.Variable, error) {
	var vars []db.Variable
	if err := repoScope(s.DBForCtx(ctx), repoID, env).Find(&vars).Error; err != nil {
		return nil, err
	}
	return vars, nil
}

// ListOrgVariables retrieves variables for a given organization.
func (s *Service) ListOrgVariables(ctx context.Context, orgID uint) ([]db.Variable, error) {
	var vars []db.Variable
	if err := orgScope(s.DBForCtx(ctx), orgID).Find(&vars).Error; err != nil {
		return nil, err
	}
	return vars, nil
}

// CreateVariable creates a new variable.
func (s *Service) CreateVariable(ctx context.Context, ownerID uint, repoID *uint, env, name, value string) (db.Variable, error) {
	v := db.Variable{OwnerID: ownerID, RepositoryID: repoID, Environment: env, Name: name, Value: value}
	if err := s.DBForCtx(ctx).Create(&v).Error; err != nil {
		return db.Variable{}, err
	}
	return v, nil
}

// GetVariable retrieves a specific variable by name.
func (s *Service) GetVariable(ctx context.Context, repoID uint, env, name string) (db.Variable, error) {
	var v db.Variable
	if err := repoScope(s.DBForCtx(ctx), repoID, env).Where("name = ?", name).First(&v).Error; err != nil {
		return db.Variable{}, wrapErr(err)
	}
	return v, nil
}

// GetOrgVariable retrieves a specific organization variable by name.
func (s *Service) GetOrgVariable(ctx context.Context, orgID uint, name string) (db.Variable, error) {
	var v db.Variable
	if err := orgScope(s.DBForCtx(ctx), orgID).Where("name = ?", name).First(&v).Error; err != nil {
		return db.Variable{}, wrapErr(err)
	}
	return v, nil
}

// UpdateVariable updates the value of a specific variable.
func (s *Service) UpdateVariable(ctx context.Context, repoID uint, env, name, value string) error {
	return checkAffected(repoScope(s.DBForCtx(ctx).Model(&db.Variable{}), repoID, env).Where("name = ?", name).Update("value", value))
}

// UpdateOrgVariable updates the value of a specific organization variable.
func (s *Service) UpdateOrgVariable(ctx context.Context, orgID uint, name, value string) error {
	return checkAffected(orgScope(s.DBForCtx(ctx).Model(&db.Variable{}), orgID).Where("name = ?", name).Update("value", value))
}

// DeleteVariable deletes a specific variable.
func (s *Service) DeleteVariable(ctx context.Context, repoID uint, env, name string) error {
	return checkAffected(repoScope(s.DBForCtx(ctx), repoID, env).Where("name = ?", name).Delete(&db.Variable{}))
}

// DeleteOrgVariable deletes a specific organization variable.
func (s *Service) DeleteOrgVariable(ctx context.Context, orgID uint, name string) error {
	return checkAffected(orgScope(s.DBForCtx(ctx), orgID).Where("name = ?", name).Delete(&db.Variable{}))
}

// -------- Secrets --------

// ListSecrets retrieves secrets for a given repository and environment.
func (s *Service) ListSecrets(ctx context.Context, repoID uint, env string) ([]db.Secret, error) {
	var secrets []db.Secret
	if err := repoScope(s.DBForCtx(ctx), repoID, env).Find(&secrets).Error; err != nil {
		return nil, err
	}
	return secrets, nil
}

// ListOrgSecrets retrieves secrets for a given organization.
func (s *Service) ListOrgSecrets(ctx context.Context, orgID uint) ([]db.Secret, error) {
	var secrets []db.Secret
	if err := orgScope(s.DBForCtx(ctx), orgID).Find(&secrets).Error; err != nil {
		return nil, err
	}
	return secrets, nil
}

// UpsertSecretInput holds parameters for creating or updating a secret.
type UpsertSecretInput struct {
	OwnerID         uint
	RepoID          *uint
	Env             string
	Name            string
	Value           string
	Visibility      string
	SelectedRepoIDs string
}

// Validate ensures required fields are present.
func (in UpsertSecretInput) Validate() error {
	if in.OwnerID == 0 {
		return fmt.Errorf("%w: owner_id is required", ErrValidation)
	}
	if in.Name == "" {
		return fmt.Errorf("%w: name is required", ErrValidation)
	}
	return nil
}

// UpsertSecret creates or updates a secret.
func (s *Service) UpsertSecret(ctx context.Context, in UpsertSecretInput) error {
	if err := in.Validate(); err != nil {
		return err
	}
	var secret db.Secret
	query := s.DBForCtx(ctx).Where("owner_id = ? AND environment = ? AND name = ?", in.OwnerID, in.Env, in.Name)
	if in.RepoID != nil {
		query = query.Where("repository_id = ?", *in.RepoID)
	} else {
		query = query.Where("repository_id IS NULL")
	}

	err := query.First(&secret).Error
	if err != nil {
		secret = db.Secret{
			OwnerID:         in.OwnerID,
			RepositoryID:    in.RepoID,
			Environment:     in.Env,
			Name:            in.Name,
			Value:           in.Value,
			Visibility:      in.Visibility,
			SelectedRepoIDs: in.SelectedRepoIDs,
		}
		return s.DBForCtx(ctx).Create(&secret).Error
	}

	updates := map[string]any{
		"visibility":        in.Visibility,
		"selected_repo_ids": in.SelectedRepoIDs,
	}
	if in.Value != "" {
		updates["value"] = in.Value
	}
	return s.DBForCtx(ctx).Model(&secret).Updates(updates).Error
}

// GetSecret retrieves a specific secret by name.
func (s *Service) GetSecret(ctx context.Context, repoID uint, env, name string) (db.Secret, error) {
	var sec db.Secret
	if err := repoScope(s.DBForCtx(ctx), repoID, env).Where("name = ?", name).First(&sec).Error; err != nil {
		return db.Secret{}, ErrNotFound
	}
	return sec, nil
}

// GetOrgSecret retrieves a specific organization secret by name.
func (s *Service) GetOrgSecret(ctx context.Context, orgID uint, name string) (db.Secret, error) {
	var sec db.Secret
	if err := orgScope(s.DBForCtx(ctx), orgID).Where("name = ?", name).First(&sec).Error; err != nil {
		return db.Secret{}, ErrNotFound
	}
	return sec, nil
}

// DeleteSecret deletes a specific secret.
func (s *Service) DeleteSecret(ctx context.Context, repoID uint, env, name string) error {
	return checkAffected(repoScope(s.DBForCtx(ctx), repoID, env).Where("name = ?", name).Delete(&db.Secret{}))
}

// DeleteOrgSecret deletes a specific organization secret.
func (s *Service) DeleteOrgSecret(ctx context.Context, orgID uint, name string) error {
	return checkAffected(orgScope(s.DBForCtx(ctx), orgID).Where("name = ?", name).Delete(&db.Secret{}))
}

// UpdateOrgSecretSelectedRepos updates the selected repositories for a given organization secret.
func (s *Service) UpdateOrgSecretSelectedRepos(ctx context.Context, orgID uint, name, selectedRepoIDs string) error {
	return checkAffected(orgScope(s.DBForCtx(ctx).Model(&db.Secret{}), orgID).Where("name = ?", name).Update("selected_repo_ids", selectedRepoIDs))
}
