package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"
	searchsvc "github.com/ngaut/agent-git-service/internal/service/search"

	"gorm.io/gorm"
)

// GitDiskUsageKB returns the repository's disk usage in kilobytes.
func (s *Service) GitDiskUsageKB(ctx context.Context, fullName string) int {
	if s.Git == nil {
		return 0
	}
	return s.Git.DiskUsageKB(ctx, fullName)
}

// IsRepoEmpty reports whether the repository has no commits.
func (s *Service) IsRepoEmpty(ctx context.Context, fullName string) bool {
	if s.Git == nil {
		return false
	}
	return s.Git.IsEmpty(ctx, fullName)
}

// RenameRepo renames a repository.
func (s *Service) RenameRepo(ctx context.Context, fullName, newName string) (db.Repository, error) {
	rep, err := s.GetRepo(ctx, fullName)
	if err != nil {
		return db.Repository{}, err
	}
	oldFull := rep.FullName
	newFull := rep.Owner.Login + "/" + newName
	// Rename git directory first (more likely to fail).
	oldPath, err := s.Git.GetRepoPath(ctx, oldFull)
	if err != nil {
		return rep, fmt.Errorf("get old repo path: %w", err)
	}
	newPath, err := s.Git.GetRepoPath(ctx, newFull)
	if err != nil {
		return rep, fmt.Errorf("get new repo path: %w", err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return rep, fmt.Errorf("rename git dir: %w", err)
	}
	// Then update DB. If this fails, roll back the filesystem rename.
	if err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		var collaboratorUserIDs []uint
		if err := tx.Model(&db.Collaborator{}).
			Where("repository_id = ?", rep.ID).
			Pluck("user_id", &collaboratorUserIDs).Error; err != nil {
			return err
		}
		if err := ensureRepoRedirectTx(tx, oldFull, rep.ID); err != nil {
			return err
		}
		if err := tx.Model(&db.Repository{}).Where("id = ?", rep.ID).Updates(map[string]any{"name": newName, "full_name": newFull}).Error; err != nil {
			return err
		}
		// Update entities that use RepositoryFullName as a string key.
		if err := tx.Model(&db.Autolink{}).Where("repository_full_name = ?", oldFull).Update("repository_full_name", newFull).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		if rbErr := os.Rename(newPath, oldPath); rbErr != nil {
			slog.Error("RenameRepo: rollback", "from", newPath, "to", oldPath, "error", rbErr)
		}
		return rep, err
	}

	RepoCacheInvalidate(ctx, rep.ID)
	var fresh db.Repository
	if err := preloadRepoFull(s.DBForCtx(ctx)).First(&fresh, rep.ID).Error; err != nil {
		return fresh, wrapErr(err)
	}
	repoCacheSet(ctx, fresh)
	return fresh, nil
}

// TransferRepo transfers a repository to a new owner.
func (s *Service) TransferRepo(ctx context.Context, fullName, newOwnerLogin string) (db.Repository, error) {
	rep, err := s.GetRepo(ctx, fullName)
	if err != nil {
		return db.Repository{}, err
	}
	if err := s.requireRepoPermission(ctx, rep.ID, RepoPermissionAdmin); err != nil {
		return db.Repository{}, err
	}
	oldFull := rep.FullName
	newOwner, err := s.GetUser(ctx, newOwnerLogin)
	if err != nil {
		return rep, fmt.Errorf("service: transfer repo: new owner: %w", err)
	}
	if newOwner.Type == db.TypeOrganization {
		if err := s.requireOrgAdminForRepoTarget(ctx, newOwner.ID); err != nil {
			return db.Repository{}, err
		}
	}

	newFull := newOwner.Login + "/" + rep.Name
	if newFull == oldFull {
		return rep, fmt.Errorf("%w: repository %s already exists", ErrConflict, newFull)
	}
	var existing db.Repository
	if err := s.DBForCtx(ctx).Select("id").First(&existing, "full_name = ?", newFull).Error; err == nil && existing.ID != rep.ID {
		return rep, fmt.Errorf("%w: repository %s already exists", ErrConflict, newFull)
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return rep, fmt.Errorf("check destination repo: %w", err)
	}

	// Rename git directory first
	oldPath, err := s.Git.GetRepoPath(ctx, oldFull)
	if err != nil {
		return rep, fmt.Errorf("get old repo path: %w", err)
	}
	newPath, err := s.Git.GetRepoPath(ctx, newFull)
	if err != nil {
		return rep, fmt.Errorf("get new repo path: %w", err)
	}

	// Ensure parent directory exists for new path
	if err := os.MkdirAll(newPath[:strings.LastIndex(newPath, "/")], 0750); err != nil {
		return rep, fmt.Errorf("ensure parent dir: %w", err)
	}
	if _, err := os.Stat(newPath); err == nil {
		return rep, fmt.Errorf("%w: repository %s already exists", ErrConflict, newFull)
	} else if !errors.Is(err, os.ErrNotExist) {
		return rep, fmt.Errorf("stat new repo path: %w", err)
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		if _, statErr := os.Stat(newPath); statErr == nil {
			return rep, fmt.Errorf("%w: repository %s already exists", ErrConflict, newFull)
		}
		return rep, fmt.Errorf("transfer git dir: %w", err)
	}

	// Then update DB. If this fails, roll back the filesystem rename.
	if err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		var collaboratorUserIDs []uint
		if err := tx.Model(&db.Collaborator{}).
			Where("repository_id = ?", rep.ID).
			Pluck("user_id", &collaboratorUserIDs).Error; err != nil {
			return err
		}
		if err := ensureRepoRedirectTx(tx, oldFull, rep.ID); err != nil {
			return err
		}
		// Use Model(&db.Repository{}) instead of Model(&rep) to avoid
		// GORM skipping foreign-key columns when the source struct has loaded associations.
		if err := tx.Model(&db.Repository{}).Where("id = ?", rep.ID).Updates(map[string]any{
			"owner_id":  newOwner.ID,
			"full_name": newFull,
		}).Error; err != nil {
			return err
		}
		// Update entities that use RepositoryFullName as a string key.
		if err := tx.Model(&db.Autolink{}).Where("repository_full_name = ?", oldFull).Update("repository_full_name", newFull).Error; err != nil {
			return err
		}
		if rep.Owner.Type == db.TypeOrganization {
			for _, userID := range collaboratorUserIDs {
				if err := syncOutsideCollaboratorForOrgTx(tx, rep.OwnerID, userID); err != nil {
					return err
				}
			}
		}
		if newOwner.Type == db.TypeOrganization {
			for _, userID := range collaboratorUserIDs {
				if err := syncOutsideCollaboratorForOrgTx(tx, newOwner.ID, userID); err != nil {
					return err
				}
			}
			if err := s.ensureOrgRepoGovernanceTx(ctx, tx, newOwner.ID, rep.ID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		if rbErr := os.Rename(newPath, oldPath); rbErr != nil {
			slog.Error("TransferRepo: rollback", "from", newPath, "to", oldPath, "error", rbErr)
		}
		if errors.Is(err, gorm.ErrDuplicatedKey) || isDuplicateErr(err) {
			return rep, fmt.Errorf("%w: repository %s already exists", ErrConflict, newFull)
		}
		return rep, err
	}

	RepoCacheInvalidate(ctx, rep.ID)
	var fresh db.Repository
	if err := preloadRepoFull(s.DBForCtx(ctx)).First(&fresh, rep.ID).Error; err != nil {
		return fresh, wrapErr(err)
	}
	repoCacheSet(ctx, fresh)
	return fresh, nil
}

// ListReposByOwnerID returns all repositories owned by a user, ordered by updated_at desc.
func (s *Service) ListReposByOwnerID(ctx context.Context, ownerID uint) ([]db.Repository, error) {
	var repos []db.Repository
	err := s.DBForCtx(ctx).Preload("Owner").Preload("Labels").
		Where("owner_id = ?", ownerID).Order("updated_at DESC").Limit(defaultListLimit).Find(&repos).Error
	return repos, err
}

// SearchRepos performs a GitHub-style repository search using qualifiers.
// Returns an empty slice (never nil) when query is blank or no results.
func (s *Service) SearchRepos(ctx context.Context, query string) ([]db.Repository, error) {
	return searchsvc.SearchRepos(ctx, searchsvc.RepoSearchDeps{
		DBForCtx:         s.DBForCtx,
		UserFromContext:  UserFromContext,
		DefaultListLimit: defaultListLimit,
	}, query)
}

// SearchReposGQL handles GraphQL search(type: REPOSITORY) queries.
// The query string uses GitHub search syntax: "user:X language:Y topic:Z sort:updated-desc".
func (s *Service) SearchReposGQL(ctx context.Context, query string) ([]db.Repository, error) {
	return searchsvc.SearchReposGQL(ctx, searchsvc.RepoSearchDeps{
		DBForCtx:         s.DBForCtx,
		UserFromContext:  UserFromContext,
		DefaultListLimit: defaultListLimit,
	}, query)
}

// ListAllRepos returns all repositories (capped at 1000 for safety).
func (s *Service) ListAllRepos(ctx context.Context) ([]db.Repository, error) {
	var repos []db.Repository
	err := s.DBForCtx(ctx).Preload("Owner").Limit(1000).Find(&repos).Error
	return repos, err
}

// --- Autolinks ---

// ListAutolinks returns all autolinks for a repository.
func (s *Service) ListAutolinks(ctx context.Context, repoFullName string) ([]db.Autolink, error) {
	var autolinks []db.Autolink
	err := s.DBForCtx(ctx).Where("repository_full_name = ?", repoFullName).Find(&autolinks).Error
	return autolinks, err
}

// CreateAutolink creates a new autolink for a repository.
func (s *Service) CreateAutolink(ctx context.Context, a *db.Autolink) error {
	var existing db.Autolink
	if s.DBForCtx(ctx).Where("repository_full_name = ? AND key_prefix = ?", a.RepositoryFullName, a.KeyPrefix).First(&existing).Error == nil {
		return fmt.Errorf("%w: autolink with prefix %q already exists", ErrConflict, a.KeyPrefix)
	}
	return s.DBForCtx(ctx).Create(a).Error
}

// GetAutolink returns a single autolink by ID.
func (s *Service) GetAutolink(ctx context.Context, id uint) (db.Autolink, error) {
	return getByID[db.Autolink](s, ctx, id)
}

// DeleteAutolink deletes an autolink by ID.
func (s *Service) DeleteAutolink(ctx context.Context, id uint) error {
	return deleteByID[db.Autolink](s, ctx, id)
}
