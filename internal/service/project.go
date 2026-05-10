package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gh-server/internal/db"
	"gh-server/internal/randutil"

	"gorm.io/gorm"
)

// ─── Projects (ProjectV2) ─────────────────────────────────────────────────────

// GetProjectByOwnerNumber fetches a project by owner login and sequential number.
func (s *Service) GetProjectByOwnerNumber(ctx context.Context, ownerLogin string, number int32) (db.Project, error) {
	var proj db.Project
	err := s.DBForCtx(ctx).Where("owner_login = ? AND number = ?", ownerLogin, number).First(&proj).Error
	return proj, wrapErr(err)
}

// CreateProject creates a new ProjectV2 for the given owner login.
// Retries on duplicate key to handle concurrent number allocation.
func (s *Service) CreateProject(ctx context.Context, ownerLogin, title string) (db.Project, error) {
	const maxRetries = 5
	for attempt := 0; attempt < maxRetries; attempt++ {
		var maxNumber int32
		if err := s.DBForCtx(ctx).Model(&db.Project{}).Where("owner_login = ?", ownerLogin).
			Select("COALESCE(MAX(number), 0)").Row().Scan(&maxNumber); err != nil {
			return db.Project{}, fmt.Errorf("CreateProject: max number scan: %w", err)
		}
		proj := db.Project{
			OwnerLogin: ownerLogin,
			Number:     maxNumber + 1,
			Title:      title,
		}
		if err := s.DBForCtx(ctx).Create(&proj).Error; err != nil {
			if isDuplicateErr(err) {
				continue // retry with updated max
			}
			return db.Project{}, err
		}
		return proj, nil
	}
	return db.Project{}, fmt.Errorf("CreateProject: failed after %d retries due to concurrent number allocation", maxRetries)
}

// SearchProjects returns all projects belonging to the owner that match the given title query.
func (s *Service) SearchProjects(ctx context.Context, ownerLogin, titleQuery string) ([]db.Project, error) {
	var projects []db.Project
	q := s.DBForCtx(ctx).Where("owner_login = ?", ownerLogin)

	// gh CLI passes search qualifiers like "is:open" for listing projects.
	// We handle basic qualifiers by stripping them.
	titleQuery = strings.ReplaceAll(titleQuery, "is:open", "")
	titleQuery = strings.TrimSpace(titleQuery)

	if titleQuery != "" {
		q = q.Where("title LIKE ?", "%"+escapeLike(titleQuery)+"%")
	}
	if err := q.Limit(defaultListLimit).Find(&projects).Error; err != nil {
		return nil, err
	}
	return projects, nil
}

// ListProjectsForRepo returns projects explicitly linked to a repository.
func (s *Service) ListProjectsForRepo(ctx context.Context, repoID uint) ([]db.Project, error) {
	var projects []db.Project
	err := s.DBForCtx(ctx).
		Model(&db.Project{}).
		Joins("JOIN project_repo_links ON project_repo_links.project_id = projects.id").
		Where("project_repo_links.repository_id = ?", repoID).
		Order("projects.id asc").
		Find(&projects).Error
	return projects, err
}

// DeleteProject removes a project and all its child records by ID.
func (s *Service) DeleteProject(ctx context.Context, id uint) error {
	if _, err := s.GetProjectByID(ctx, id); err != nil {
		return err
	}
	return s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("project_id = ?", id).Delete(&db.ProjectItem{}).Error; err != nil {
			return err
		}
		if err := tx.Where("project_id = ?", id).Delete(&db.ProjectField{}).Error; err != nil {
			return err
		}
		if err := tx.Where("project_id = ?", id).Delete(&db.ProjectRepoLink{}).Error; err != nil {
			return err
		}
		return tx.Delete(&db.Project{}, id).Error
	})
}

// LinkProjectToRepo creates a link between a project and a repository.
func (s *Service) LinkProjectToRepo(ctx context.Context, projectID, repoID uint) error {
	if _, err := s.GetProjectByID(ctx, projectID); err != nil {
		return err
	}
	link := db.ProjectRepoLink{ProjectID: projectID, RepositoryID: repoID}
	return s.DBForCtx(ctx).FirstOrCreate(&link, link).Error
}

// UnlinkProjectFromRepo removes the link between a project and a repository.
func (s *Service) UnlinkProjectFromRepo(ctx context.Context, projectID, repoID uint) error {
	if _, err := s.GetProjectByID(ctx, projectID); err != nil {
		return err
	}
	return s.DBForCtx(ctx).
		Where("project_id = ? AND repository_id = ?", projectID, repoID).
		Delete(&db.ProjectRepoLink{}).Error
}

// GetProjectByID fetches a project by its internal DB ID.
func (s *Service) GetProjectByID(ctx context.Context, id uint) (db.Project, error) {
	var proj db.Project
	if err := s.DBForCtx(ctx).First(&proj, id).Error; err != nil {
		return proj, wrapErr(err)
	}
	return proj, nil
}

// UpdateProject updates a project's fields by ID.
func (s *Service) UpdateProject(ctx context.Context, id uint, updates map[string]any) (db.Project, error) {
	if _, err := s.GetProjectByID(ctx, id); err != nil {
		return db.Project{}, err
	}
	if len(updates) > 0 {
		if err := s.DBForCtx(ctx).Model(&db.Project{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return db.Project{}, err
		}
	}
	return s.GetProjectByID(ctx, id)
}

// ─── Project Fields ───────────────────────────────────────────────────────────

// ListProjectFields returns all fields for a project ordered by ID.
func (s *Service) ListProjectFields(ctx context.Context, projectID uint) ([]db.ProjectField, error) {
	if _, err := s.GetProjectByID(ctx, projectID); err != nil {
		return nil, err
	}
	var fields []db.ProjectField
	err := s.DBForCtx(ctx).Where("project_id = ?", projectID).Order("id asc").Find(&fields).Error
	return fields, err
}

// CreateProjectField creates a new field on a project.
func (s *Service) CreateProjectField(ctx context.Context, field *db.ProjectField) error {
	if _, err := s.GetProjectByID(ctx, field.ProjectID); err != nil {
		return err
	}
	return s.DBForCtx(ctx).Create(field).Error
}

// GetProjectField fetches a project field by ID.
func (s *Service) GetProjectField(ctx context.Context, id uint) (db.ProjectField, error) {
	f, err := getByID[db.ProjectField](s, ctx, id)
	if err != nil {
		return f, err
	}
	if _, err := s.GetProjectByID(ctx, f.ProjectID); err != nil {
		return db.ProjectField{}, err
	}
	return f, nil
}

// DeleteProjectField removes a project field by ID.
func (s *Service) DeleteProjectField(ctx context.Context, id uint) error {
	f, err := s.GetProjectField(ctx, id)
	if err != nil {
		return err
	}
	return deleteByID[db.ProjectField](s, ctx, f.ID)
}

// ─── Project Items ────────────────────────────────────────────────────────────

// ListProjectItems returns all items for a project ordered by ID.
func (s *Service) ListProjectItems(ctx context.Context, projectID uint) ([]db.ProjectItem, error) {
	if _, err := s.GetProjectByID(ctx, projectID); err != nil {
		return nil, err
	}
	var items []db.ProjectItem
	err := s.DBForCtx(ctx).Where("project_id = ?", projectID).Order("id asc").Find(&items).Error
	return items, err
}

// CreateProjectItem creates a new item in a project.
func (s *Service) CreateProjectItem(ctx context.Context, item *db.ProjectItem) error {
	if _, err := s.GetProjectByID(ctx, item.ProjectID); err != nil {
		return err
	}
	if item.Type == "DRAFT_ISSUE" && strings.TrimSpace(item.ContentID) == "" {
		// Draft issues are standalone project items, so synthesize a content ID
		// that keeps the storage-level uniqueness invariant without collapsing
		// distinct drafts in the same project.
		item.ContentID = "DraftIssue_" + randutil.Hex(16)
	}
	return s.DBForCtx(ctx).Create(item).Error
}

// FindOrCreateProjectItem finds an existing project item by (projectID, contentID, type) or creates a new one.
// This provides idempotent behavior for addProjectV2ItemById operations.
// Returns the existing item if found, or the newly created item.
func (s *Service) FindOrCreateProjectItem(ctx context.Context, projectID uint, contentID, itemType string) (db.ProjectItem, error) {
	if _, err := s.GetProjectByID(ctx, projectID); err != nil {
		return db.ProjectItem{}, err
	}

	// Try to find existing item
	var item db.ProjectItem
	err := s.DBForCtx(ctx).Where("project_id = ? AND content_id = ? AND type = ?", projectID, contentID, itemType).First(&item).Error
	if err == nil {
		// Item already exists - return it (idempotent)
		return item, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return db.ProjectItem{}, err
	}

	// Create new item
	item = db.ProjectItem{
		ProjectID: projectID,
		ContentID: contentID,
		Type:      itemType,
	}
	if err := s.DBForCtx(ctx).Create(&item).Error; err != nil {
		// Check for unique constraint violation (concurrent add)
		if err := s.DBForCtx(ctx).Where("project_id = ? AND content_id = ? AND type = ?", projectID, contentID, itemType).First(&item).Error; err == nil {
			return item, nil
		}
		return db.ProjectItem{}, err
	}
	return item, nil
}

// GetProjectItem fetches a project item by ID.
func (s *Service) GetProjectItem(ctx context.Context, id uint) (db.ProjectItem, error) {
	it, err := getByID[db.ProjectItem](s, ctx, id)
	if err != nil {
		return it, err
	}
	if _, err := s.GetProjectByID(ctx, it.ProjectID); err != nil {
		return db.ProjectItem{}, err
	}
	return it, nil
}

// DeleteProjectItem removes a project item by ID.
func (s *Service) DeleteProjectItem(ctx context.Context, id uint) error {
	it, err := s.GetProjectItem(ctx, id)
	if err != nil {
		return err
	}
	return deleteByID[db.ProjectItem](s, ctx, it.ID)
}

// UpdateProjectItem updates a project item's fields by ID and returns the updated item.
func (s *Service) UpdateProjectItem(ctx context.Context, id uint, updates map[string]any) (db.ProjectItem, error) {
	if _, err := s.GetProjectItem(ctx, id); err != nil {
		return db.ProjectItem{}, err
	}
	if len(updates) > 0 {
		if err := s.DBForCtx(ctx).Model(&db.ProjectItem{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return db.ProjectItem{}, err
		}
	}
	return s.GetProjectItem(ctx, id)
}

// FindProjectItemsByContentIDs returns project items matching the given content IDs, with Project preloaded.
func (s *Service) FindProjectItemsByContentIDs(ctx context.Context, contentIDs []string) ([]db.ProjectItem, error) {
	var items []db.ProjectItem
	err := s.DBForCtx(ctx).
		Where("content_id IN ?", contentIDs).
		Preload("Project").
		Find(&items).Error
	if err != nil {
		return nil, err
	}

	return items, nil
}
