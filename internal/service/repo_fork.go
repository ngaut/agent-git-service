package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"
	"gorm.io/gorm"
)

// ResolveForkRepo finds the fork of baseFullName owned by forkOwner.
// It first tries forkOwner/baseName directly, then searches for any repo
// owned by forkOwner that is a fork of the base. Returns "" if not found.
func (s *Service) ResolveForkRepo(ctx context.Context, baseFullName, forkOwner string) string {
	parts := strings.SplitN(baseFullName, "/", 2)
	if len(parts) != 2 {
		return ""
	}
	directName := forkOwner + "/" + parts[1]
	if _, err := s.GetRepo(ctx, directName); err == nil {
		return directName
	}
	repos, _ := s.ListUserRepos(ctx, forkOwner)
	baseRepo, _ := s.GetRepo(ctx, baseFullName)
	for _, r := range repos {
		if r.Fork && r.ParentID != nil && *r.ParentID == baseRepo.ID {
			return r.FullName
		}
	}
	return ""
}

// ForkRepo creates a fork of a repository under the given owner.
func (s *Service) ForkRepo(ctx context.Context, sourceFullName, targetOwnerLogin, targetName string) (db.Repository, error) {
	src, err := s.GetRepo(ctx, sourceFullName)
	if err != nil {
		return db.Repository{}, err
	}
	targetOwner, err := s.GetUser(ctx, targetOwnerLogin)
	if err != nil {
		return db.Repository{}, err
	}
	targetsOrg := targetOwner.Type == db.TypeOrganization
	if targetsOrg {
		if err := s.requireOrgAdminForRepoTarget(ctx, targetOwner.ID); err != nil {
			return db.Repository{}, err
		}
	}

	name := src.Name
	if targetName != "" {
		name = targetName
	}

	in := CreateRepoInput{
		OwnerLogin:       targetOwnerLogin,
		Name:             name,
		Description:      src.Description,
		Private:          src.Private,
		DefaultBranch:    src.DefaultBranch,
		RequireOrgAdmin:  targetsOrg,
		SkipOrgBootstrap: targetsOrg,
	}
	fork, err := s.CreateRepo(ctx, in)
	if err != nil {
		return db.Repository{}, err
	}

	// Actually copy the git repository on disk. If this fails, clean up the DB record.
	if err := s.Git.Fork(ctx, src.FullName, fork.FullName); err != nil {
		if cleanupErr := s.cleanupForkRepo(ctx, fork.FullName, "git_fork_failed"); cleanupErr != nil {
			return db.Repository{}, errors.Join(err, fmt.Errorf("cleanup failed: %w", cleanupErr))
		}
		return db.Repository{}, fmt.Errorf("%w: %v", ErrValidation, err)
	}

	// Finalize fork metadata in one explicit DB transaction.
	if err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Model(&db.Repository{}).
			Where("id = ?", fork.ID).
			Updates(map[string]any{
				"fork":      true,
				"parent_id": src.ID,
			}).Error
	}); err != nil {
		slog.Error("ForkRepo: finalize metadata failed; compensating", "repo", fork.FullName, "error", err)
		if cleanupErr := s.cleanupForkRepo(ctx, fork.FullName, "db_finalize_failed"); cleanupErr != nil {
			return db.Repository{}, errors.Join(err, fmt.Errorf("cleanup failed: %w", cleanupErr))
		}
		return db.Repository{}, fmt.Errorf("%w: %v", ErrValidation, err)
	}

	if err := preloadRepoMinimal(s.DBForCtx(ctx)).First(&fork, fork.ID).Error; err != nil {
		return fork, wrapErr(err)
	}
	return fork, nil
}

func (s *Service) cleanupForkRepo(ctx context.Context, fullName, stage string) error {
	if err := s.DeleteRepo(ctx, fullName); err != nil {
		slog.Error("ForkRepo: cleanup repo failed", "repo", fullName, "stage", stage, "error", err)
		var gitErr error
		if s.Git != nil {
			gitErr = s.Git.Delete(ctx, fullName)
			if gitErr != nil {
				slog.Error("ForkRepo: cleanup git failed", "repo", fullName, "stage", stage, "error", gitErr)
			}
		}
		return errors.Join(err, gitErr)
	}
	return nil
}

// ListForks returns all repositories forked from the given repository ID.
func (s *Service) ListForks(ctx context.Context, repoID uint) ([]db.Repository, error) {
	var forks []db.Repository
	if err := s.DBForCtx(ctx).Preload("Owner").Where("parent_id = ?", repoID).Find(&forks).Error; err != nil {
		return nil, err
	}
	return forks, nil
}

// ForkCount returns the number of forks for a repository.
func (s *Service) ForkCount(ctx context.Context, repoID uint) int {
	var count int64
	if err := s.DBForCtx(ctx).Model(&db.Repository{}).Where("parent_id = ?", repoID).Count(&count).Error; err != nil {
		slog.Error("ForkCount", "error", err)
	}
	return int(count)
}
