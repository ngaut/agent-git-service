// Package service — release and release asset management.
package service

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"time"

	"gh-server/internal/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// isValidSHA returns true if s is a 40-character hex string (full SHA-1).
func isValidSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// ─── Releases ────────────────────────────────────────────────────────────────

// CreateReleaseInput holds parameters for creating a release.
type CreateReleaseInput struct {
	TagName         string
	Name            string
	Body            string
	Draft           bool
	PreRelease      bool
	TargetCommitish string
}

// CreateRelease creates a new release for a repository.
func (s *Service) CreateRelease(ctx context.Context, repoFullName string, in CreateReleaseInput) (db.Release, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return db.Release{}, err
	}
	u, err := s.GetCurrentUser(ctx)
	if err != nil {
		return db.Release{}, err
	}

	// Check for duplicate tag name within the repository.
	var existing db.Release
	if s.DBForCtx(ctx).Where("repository_id = ? AND tag_name = ?", rep.ID, in.TagName).First(&existing).Error == nil {
		return db.Release{}, fmt.Errorf("%w: release with tag %q already exists", ErrConflict, in.TagName)
	}

	// Create the backing git tag (required per issue #99: must create tag or fail).
	if s.Git == nil {
		return db.Release{}, fmt.Errorf("git service not available: cannot create tag %q", in.TagName)
	}
	target := in.TargetCommitish
	if target == "" {
		target = rep.DefaultBranch
	}
	// Resolve target to SHA (branch name first, then try as literal SHA).
	sha, err := s.Git.HeadSHA(ctx, repoFullName, target)
	if err != nil {
		if !isValidSHA(target) {
			return db.Release{}, fmt.Errorf("resolve target %q: %w", target, err)
		}
		sha = target
	}
	if err := s.Git.CreateTagIfNotExists(ctx, repoFullName, in.TagName, "Release "+in.TagName, sha); err != nil {
		return db.Release{}, fmt.Errorf("create tag %q: %w", in.TagName, err)
	}

	now := time.Now()
	rel := db.Release{
		RepositoryID: rep.ID,
		TagName:      in.TagName,
		Name:         in.Name,
		Body:         db.LargeText(in.Body),
		Draft:        in.Draft,
		PreRelease:   in.PreRelease,
		AuthorID:     u.ID,
		PublishedAt:  &now,
	}
	if err := s.DBForCtx(ctx).Create(&rel).Error; err != nil {
		return db.Release{}, err
	}
	preloadRelease(s.DBForCtx(ctx)).First(&rel, rel.ID)
	return rel, nil
}

// GetRelease fetches a release by ID with all associations.
func (s *Service) GetRelease(ctx context.Context, id uint) (db.Release, error) {
	var rel db.Release
	err := preloadRelease(s.DBForCtx(ctx)).First(&rel, id).Error
	return rel, wrapErr(err)
}

// ListReleases returns all releases for a repository, newest first.
func (s *Service) ListReleases(ctx context.Context, repoFullName string) ([]db.Release, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	var releases []db.Release
	if err := preloadRelease(s.DBForCtx(ctx)).
		Where("repository_id = ?", rep.ID).Order("created_at desc").Find(&releases).Error; err != nil {
		return nil, err
	}
	return releases, nil
}

// DeleteRelease removes a release and its assets by ID.
// It locks the release row first to prevent concurrent asset uploads from
// reintroducing FK constraint errors.
func (s *Service) DeleteRelease(ctx context.Context, id uint) error {
	return s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock the release row to block concurrent UploadReleaseAsset calls.
		var rel db.Release
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&rel, id).Error; err != nil {
			return wrapErr(err)
		}
		if err := tx.Where("release_id = ?", id).Delete(&db.ReleaseAsset{}).Error; err != nil {
			return err
		}
		return checkAffected(tx.Delete(&db.Release{}, id))
	})
}

// GetReleaseByTag fetches a release by tag name.
func (s *Service) GetReleaseByTag(ctx context.Context, repoFullName, tag string) (db.Release, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return db.Release{}, err
	}
	var rel db.Release
	err = preloadRelease(s.DBForCtx(ctx)).
		First(&rel, "repository_id = ? AND tag_name = ?", rep.ID, tag).Error
	return rel, wrapErr(err)
}

// GetLatestRelease fetches the most recently created release for a repository.
func (s *Service) GetLatestRelease(ctx context.Context, repoFullName string) (db.Release, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return db.Release{}, err
	}
	var rel db.Release
	err = preloadRelease(s.DBForCtx(ctx)).
		Where("repository_id = ?", rep.ID).Order("created_at DESC").First(&rel).Error
	return rel, wrapErr(err)
}

// GetReleaseByTagInRepo fetches a release by repo ID and tag (used for archive downloads).
func (s *Service) GetReleaseByTagInRepo(ctx context.Context, repoFullName, tag string) (db.Release, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return db.Release{}, err
	}
	var rel db.Release
	err = s.DBForCtx(ctx).Preload("Assets").Where("repository_id = ? AND tag_name = ?", rep.ID, tag).First(&rel).Error
	return rel, wrapErr(err)
}

// GetReleaseForArchive fetches a release by ID with assets preloaded (for archive download).
func (s *Service) GetReleaseForArchive(ctx context.Context, id uint) (db.Release, error) {
	var rel db.Release
	err := s.DBForCtx(ctx).Preload("Assets").First(&rel, id).Error
	return rel, wrapErr(err)
}

// UpdateReleaseInput holds optional fields for updating a release.
type UpdateReleaseInput struct {
	TagName    *string
	Name       *string
	Body       *string
	Draft      *bool
	PreRelease *bool
}

// UpdateRelease applies partial updates to a release.
func (s *Service) UpdateRelease(ctx context.Context, id uint, in UpdateReleaseInput) (db.Release, error) {
	var rel db.Release
	if err := s.DBForCtx(ctx).First(&rel, id).Error; err != nil {
		return rel, wrapErr(err)
	}
	if in.TagName != nil {
		rel.TagName = *in.TagName
	}
	if in.Name != nil {
		rel.Name = *in.Name
	}
	if in.Body != nil {
		rel.Body = db.LargeText(*in.Body)
	}
	if in.Draft != nil {
		rel.Draft = *in.Draft
	}
	if in.PreRelease != nil {
		rel.PreRelease = *in.PreRelease
	}
	if err := s.DBForCtx(ctx).Save(&rel).Error; err != nil {
		return rel, err
	}
	preloadRelease(s.DBForCtx(ctx)).First(&rel, rel.ID)
	return rel, nil
}

// GenerateReleaseNotes generates release notes from git log between tags.
func (s *Service) GenerateReleaseNotes(ctx context.Context, repoFullName, tagName, previousTag string) (string, string, error) {
	if s.Git == nil {
		return tagName, "", nil
	}
	body, err := s.Git.LogBetweenTags(ctx, repoFullName, previousTag, tagName)
	if err != nil {
		// If tags don't exist yet, return empty body
		body = ""
	}
	if body == "" {
		body = "No changes."
	}
	name := tagName
	return name, "## What's Changed\n" + body + "\n", nil
}

// ─── Release Assets ───────────────────────────────────────────────────────────

// UploadReleaseAsset stores a release asset body and returns the saved record.
func (s *Service) UploadReleaseAsset(ctx context.Context, releaseID uint, name, label, contentType string, body io.Reader) (db.ReleaseAsset, error) {
	// Limit memory reading to 100MB to prevent out-of-memory panics.
	// For a production deployment needing larger assets, a dedicated blob store (S3/disk) is required.
	const maxAssetSize = 100 * 1024 * 1024
	lr := io.LimitReader(body, maxAssetSize+1)
	content, err := io.ReadAll(lr)
	if err != nil {
		return db.ReleaseAsset{}, err
	}
	if len(content) > maxAssetSize {
		return db.ReleaseAsset{}, fmt.Errorf("%w: asset too large (max 100MB for in-memory DB storage)", ErrValidation)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	asset := db.ReleaseAsset{
		ReleaseID:   releaseID,
		Name:        name,
		Label:       label,
		ContentType: contentType,
		Size:        int64(len(content)),
		Content:     content,
	}
	if err := s.DBForCtx(ctx).Create(&asset).Error; err != nil {
		return db.ReleaseAsset{}, err
	}
	// TiDB may not populate asset.ID via LastInsertId — re-fetch to get real ID.
	if asset.ID == 0 {
		slog.Info("UploadReleaseAsset: re-fetching asset ID", "releaseID", releaseID, "name", name)
		s.DBForCtx(ctx).Where("release_id = ? AND name = ?", releaseID, name).Last(&asset)
	}
	return asset, nil
}

// GetReleaseAsset fetches a single release asset by ID.
func (s *Service) GetReleaseAsset(ctx context.Context, id uint) (db.ReleaseAsset, error) {
	var asset db.ReleaseAsset
	err := s.DBForCtx(ctx).First(&asset, id).Error
	return asset, wrapErr(err)
}

// ListReleaseAssets returns all assets for a release.
func (s *Service) ListReleaseAssets(ctx context.Context, releaseID uint) ([]db.ReleaseAsset, error) {
	var assets []db.ReleaseAsset
	if err := s.DBForCtx(ctx).Where("release_id = ?", releaseID).Find(&assets).Error; err != nil {
		return nil, err
	}
	return assets, nil
}

// DeleteReleaseAsset removes a release asset by ID.
func (s *Service) DeleteReleaseAsset(ctx context.Context, id uint) error {
	return deleteByID[db.ReleaseAsset](s, ctx, id)
}
