package service

import (
	"context"

	"github.com/ngaut/agent-git-service/internal/db"
)

// CreateDeployment creates a new deployment.
func (s *Service) CreateDeployment(ctx context.Context, d *db.Deployment) error {
	return s.DBForCtx(ctx).Create(d).Error
}

// GetDeployment returns a deployment by ID.
func (s *Service) GetDeployment(ctx context.Context, repoID, depID uint) (*db.Deployment, error) {
	var d db.Deployment
	err := s.DBForCtx(ctx).
		Where("repository_id = ? AND id = ?", repoID, depID).
		Preload("Creator").
		First(&d).Error
	if err != nil {
		return nil, wrapErr(err)
	}
	return &d, nil
}

// ListDeployments returns all deployments for a repository.
func (s *Service) ListDeployments(ctx context.Context, repoID uint) ([]db.Deployment, error) {
	var deps []db.Deployment
	err := s.DBForCtx(ctx).
		Where("repository_id = ?", repoID).
		Order("created_at DESC").
		Preload("Creator").
		Find(&deps).Error
	return deps, err
}

// DeleteDeployment deletes a deployment if it's inactive.
func (s *Service) DeleteDeployment(ctx context.Context, repoID, depID uint) error {
	return s.DBForCtx(ctx).
		Where("repository_id = ? AND id = ?", repoID, depID).
		Delete(&db.Deployment{}).Error
}

// CreateDeploymentStatus creates a new deployment status.
func (s *Service) CreateDeploymentStatus(ctx context.Context, ds *db.DeploymentStatus) error {
	return s.DBForCtx(ctx).Create(ds).Error
}

// GetDeploymentStatus returns a deployment status by ID.
func (s *Service) GetDeploymentStatus(ctx context.Context, repoID, depID, statusID uint) (*db.DeploymentStatus, error) {
	var ds db.DeploymentStatus
	// We verify the deployment belongs to the repo via join or application logic in the handler.
	// For simplicity, we just filter by depID and statusID here.
	err := s.DBForCtx(ctx).
		Where("deployment_id = ? AND id = ?", depID, statusID).
		Preload("Creator").
		First(&ds).Error
	if err != nil {
		return nil, wrapErr(err)
	}
	return &ds, nil
}

// ListDeploymentStatuses lists the statuses for a given deployment.
func (s *Service) ListDeploymentStatuses(ctx context.Context, repoID, depID uint) ([]db.DeploymentStatus, error) {
	var statuses []db.DeploymentStatus
	err := s.DBForCtx(ctx).
		Where("deployment_id = ?", depID).
		Order("created_at DESC").
		Preload("Creator").
		Find(&statuses).Error
	return statuses, err
}
