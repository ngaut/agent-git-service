package service

import (
	"context"
	"github.com/ngaut/agent-git-service/internal/db"
)

// ListRulesets retrieves all rulesets for a given repository.
func (s *Service) ListRulesets(ctx context.Context, repoFullName string) ([]db.Ruleset, error) {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}

	var rulesets []db.Ruleset
	if err := s.DBForCtx(ctx).Where("repository_id = ?", repo.ID).Find(&rulesets).Error; err != nil {
		return nil, err
	}
	return rulesets, nil
}

// CreateRuleset creates a ruleset for a repository.
func (s *Service) CreateRuleset(ctx context.Context, repoFullName string, rs *db.Ruleset) error {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return err
	}
	rs.RepositoryID = repo.ID
	return s.DBForCtx(ctx).Create(rs).Error
}

// GetRuleset retrieves a single ruleset by ID.
func (s *Service) GetRuleset(ctx context.Context, id uint) (db.Ruleset, error) {
	var rs db.Ruleset
	if err := s.DBForCtx(ctx).First(&rs, id).Error; err != nil {
		return rs, wrapErr(err)
	}
	return rs, nil
}

// ListBranchRulesets retrieves branch rulesets for a given repository.
func (s *Service) ListBranchRulesets(ctx context.Context, repoFullName string) ([]db.Ruleset, error) {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}

	var rulesets []db.Ruleset
	if err := s.DBForCtx(ctx).Where("repository_id = ? AND target = 'branch'", repo.ID).Find(&rulesets).Error; err != nil {
		return nil, err
	}
	return rulesets, nil
}
