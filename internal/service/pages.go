// Package service — GitHub Pages config + build-history bookkeeping.
//
// v1 is intentionally a bookkeeping-only surface: the schema captures
// the config and a build-trigger log so gh-web's UI can read/write
// state, but nothing in this codebase actually publishes hosted
// content. Callers that depend on Pages serving must wire an external
// build pipeline; this layer just records intent.
package service

import (
	"context"
	"strings"

	"gh-server/internal/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// validPagesBuildType lists the build_type values the API accepts. Set
// here rather than parsed at the boundary so service callers (tests,
// future REST endpoints) get the same vocabulary.
var validPagesBuildType = map[string]bool{
	"legacy":   true,
	"workflow": true,
}

// PagesBuildStatus values. Matches GitHub's vocabulary.
const (
	PagesBuildStatusQueued   = "queued"
	PagesBuildStatusBuilding = "building"
	PagesBuildStatusBuilt    = "built"
	PagesBuildStatusErrored  = "errored"
)

// PagesSource describes where Pages would build from.
type PagesSource struct {
	Branch string
	Path   string
}

// EnablePagesInput captures the POST /pages request body.
type EnablePagesInput struct {
	Source        PagesSource
	BuildType     string // "legacy" or "workflow"; default "legacy"
	HTTPSEnforced bool
}

// UpdatePagesInput captures the PUT /pages request body. Pointer
// fields distinguish "absent" from "set to zero value" so PUT can be
// a partial update without an explicit field-mask.
type UpdatePagesInput struct {
	CNAME         *string
	HTTPSEnforced *bool
	Source        *PagesSource
	BuildType     *string
}

// GetPagesConfig returns the Pages config for a repo, or ErrNotFound
// when Pages has never been enabled.
func (s *Service) GetPagesConfig(ctx context.Context, repoID uint) (db.PagesConfig, error) {
	var c db.PagesConfig
	if err := s.DBForCtx(ctx).Where("repository_id = ?", repoID).First(&c).Error; err != nil {
		return c, wrapErr(err)
	}
	return c, nil
}

// EnablePages creates the Pages config for a repo. Returns ErrConflict
// when Pages is already enabled. Insert uses ON CONFLICT DO NOTHING +
// RowsAffected so two concurrent enables race cleanly: the loser sees
// ErrConflict (mapped to 409) instead of a unique-constraint 500.
func (s *Service) EnablePages(ctx context.Context, repoID uint, in EnablePagesInput) (db.PagesConfig, error) {
	if repoID == 0 {
		return db.PagesConfig{}, ErrValidation
	}
	branch := strings.TrimSpace(in.Source.Branch)
	if branch == "" {
		return db.PagesConfig{}, ErrValidation
	}
	path := strings.TrimSpace(in.Source.Path)
	if path == "" {
		path = "/"
	}
	buildType := strings.TrimSpace(in.BuildType)
	if buildType == "" {
		buildType = "legacy"
	}
	if !validPagesBuildType[buildType] {
		return db.PagesConfig{}, ErrValidation
	}

	cfg := db.PagesConfig{
		RepositoryID:  repoID,
		SourceBranch:  branch,
		SourcePath:    path,
		BuildType:     buildType,
		HTTPSEnforced: in.HTTPSEnforced,
	}
	result := s.DBForCtx(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "repository_id"}},
		DoNothing: true,
	}).Create(&cfg)
	if result.Error != nil {
		return db.PagesConfig{}, result.Error
	}
	if result.RowsAffected == 0 {
		return db.PagesConfig{}, ErrConflict
	}
	return cfg, nil
}

// UpdatePages applies a partial update to an existing Pages config.
// Returns ErrNotFound when Pages isn't enabled.
func (s *Service) UpdatePages(ctx context.Context, repoID uint, in UpdatePagesInput) error {
	cfg, err := s.GetPagesConfig(ctx, repoID)
	if err != nil {
		return err
	}
	if in.CNAME != nil {
		cfg.CNAME = strings.TrimSpace(*in.CNAME)
	}
	if in.HTTPSEnforced != nil {
		cfg.HTTPSEnforced = *in.HTTPSEnforced
	}
	if in.Source != nil {
		if b := strings.TrimSpace(in.Source.Branch); b != "" {
			cfg.SourceBranch = b
		}
		if p := strings.TrimSpace(in.Source.Path); p != "" {
			cfg.SourcePath = p
		}
	}
	if in.BuildType != nil {
		if b := strings.TrimSpace(*in.BuildType); b != "" {
			if !validPagesBuildType[b] {
				return ErrValidation
			}
			cfg.BuildType = b
		}
	}
	return s.DBForCtx(ctx).Save(&cfg).Error
}

// DisablePages deletes the Pages config and ALL build history for a
// repo. Returns ErrNotFound when Pages isn't enabled.
func (s *Service) DisablePages(ctx context.Context, repoID uint) error {
	if _, err := s.GetPagesConfig(ctx, repoID); err != nil {
		return err
	}
	return s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("repository_id = ?", repoID).Delete(&db.PagesBuild{}).Error; err != nil {
			return err
		}
		return tx.Where("repository_id = ?", repoID).Delete(&db.PagesConfig{}).Error
	})
}

// RecordPagesBuild logs a manual build trigger. v1 leaves status as
// "queued"; an external worker may later advance it. Returns
// ErrNotFound when Pages isn't enabled, so the handler can map to 404
// rather than silently recording an orphan build.
func (s *Service) RecordPagesBuild(ctx context.Context, repoID uint, pusherLogin, commitSHA string) (db.PagesBuild, error) {
	if _, err := s.GetPagesConfig(ctx, repoID); err != nil {
		return db.PagesBuild{}, err
	}
	build := db.PagesBuild{
		RepositoryID: repoID,
		Status:       PagesBuildStatusQueued,
		PusherLogin:  pusherLogin,
		CommitSHA:    commitSHA,
	}
	if err := s.DBForCtx(ctx).Create(&build).Error; err != nil {
		return db.PagesBuild{}, err
	}
	return build, nil
}

// ListPagesBuilds returns build history for a repo, newest first.
// Bounded to a reasonable page size to keep responses small.
func (s *Service) ListPagesBuilds(ctx context.Context, repoID uint, perPage int) ([]db.PagesBuild, error) {
	if perPage <= 0 {
		perPage = 30
	}
	if perPage > 100 {
		perPage = 100
	}
	var out []db.PagesBuild
	err := s.DBForCtx(ctx).
		Where("repository_id = ?", repoID).
		Order("created_at DESC, id DESC").
		Limit(perPage).
		Find(&out).Error
	return out, err
}
