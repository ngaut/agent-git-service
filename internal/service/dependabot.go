package service

import (
	"context"

	"github.com/ngaut/agent-git-service/internal/db"
)

// ListDependabotAlerts returns all alerts for a repository.
func (s *Service) ListDependabotAlerts(ctx context.Context, repoID uint) ([]db.DependabotAlert, error) {
	var alerts []db.DependabotAlert
	err := s.DBForCtx(ctx).
		Where("repository_id = ?", repoID).
		Order("number DESC").
		Find(&alerts).Error
	return alerts, err
}

// GetDependabotAlert returns a specific Dependabot alert.
func (s *Service) GetDependabotAlert(ctx context.Context, repoID uint, alertNum int) (*db.DependabotAlert, error) {
	var alert db.DependabotAlert
	err := s.DBForCtx(ctx).
		Where("repository_id = ? AND number = ?", repoID, alertNum).
		First(&alert).Error
	if err != nil {
		return nil, wrapErr(err)
	}
	return &alert, nil
}

// GetDependabotAlertByID returns a specific Dependabot alert by ID.
func (s *Service) GetDependabotAlertByID(ctx context.Context, id uint) (db.DependabotAlert, error) {
	return getByID[db.DependabotAlert](s, ctx, id)
}

// UpdateDependabotAlert updates an alert (e.g. dismissing it).
func (s *Service) UpdateDependabotAlert(ctx context.Context, alert *db.DependabotAlert) error {
	return s.DBForCtx(ctx).Save(alert).Error
}
