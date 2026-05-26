// Package service — user starring.
package service

import (
	"context"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm/clause"
)

// StarRepo stars a repository for the authenticated user.
func (s *Service) StarRepo(ctx context.Context, repoFullName, userLogin string) error {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return err
	}
	user, err := s.GetUser(ctx, userLogin)
	if err != nil {
		return err
	}
	star := db.Star{UserID: user.ID, RepositoryID: rep.ID}
	return s.DBForCtx(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&star).Error
}

// UnstarRepo removes a star from a repository for the authenticated user.
func (s *Service) UnstarRepo(ctx context.Context, repoFullName, userLogin string) error {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return err
	}
	user, err := s.GetUser(ctx, userLogin)
	if err != nil {
		return err
	}
	return s.DBForCtx(ctx).Where("user_id = ? AND repository_id = ?", user.ID, rep.ID).Delete(&db.Star{}).Error
}

// IsStarred checks whether the authenticated user has starred a repository.
func (s *Service) IsStarred(ctx context.Context, userID uint, repoFullName string) (bool, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return false, err
	}
	var count int64
	if err := s.DBForCtx(ctx).Model(&db.Star{}).
		Where("user_id = ? AND repository_id = ?", userID, rep.ID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ListStarred returns all repositories starred by a user.
func (s *Service) ListStarred(ctx context.Context, userID uint) ([]db.Repository, error) {
	var stars []db.Star
	if err := s.DBForCtx(ctx).
		Preload("Repository").Preload("Repository.Owner").
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Find(&stars).Error; err != nil {
		return nil, err
	}
	repos := make([]db.Repository, len(stars))
	for i, st := range stars {
		repos[i] = st.Repository
	}
	return repos, nil
}

// StarCount returns the number of stars for a repository.
func (s *Service) StarCount(ctx context.Context, repoID uint) int {
	var count int64
	s.DBForCtx(ctx).Model(&db.Star{}).Where("repository_id = ?", repoID).Count(&count)
	return int(count)
}
