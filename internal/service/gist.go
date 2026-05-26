package service

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/ngaut/agent-git-service/internal/db"
)

// CreateGist creates a new gist.
func (s *Service) CreateGist(ctx context.Context, gist *db.Gist) error {
	return s.DBForCtx(ctx).Create(gist).Error
}

// GetGist returns a gist by ID with the owner preloaded.
func (s *Service) GetGist(ctx context.Context, id string) (db.Gist, error) {
	var gist db.Gist
	err := s.DBForCtx(ctx).Preload("Owner").First(&gist, "id = ?", id).Error
	return gist, wrapErr(err)
}

// UpdateGist updates description and/or files on an existing gist.
// A nil file value in the files map means delete that file.
func (s *Service) UpdateGist(ctx context.Context, gist *db.Gist, description *string, files map[string]map[string]string) error {
	if description != nil {
		gist.Description = *description
	}
	if files != nil {
		var existing map[string]map[string]string
		if gist.Files != "" {
			if err := json.Unmarshal([]byte(gist.Files), &existing); err != nil {
				slog.Warn("failed to unmarshal gist Files", "error", err, "gist_id", gist.ID)
			}
		}
		if existing == nil {
			existing = make(map[string]map[string]string)
		}
		for name, content := range files {
			if content == nil {
				delete(existing, name)
			} else {
				existing[name] = content
			}
		}
		filesJSON, _ := json.Marshal(existing)
		gist.Files = string(filesJSON)
	}
	return s.DBForCtx(ctx).Save(gist).Error
}

// DeleteGist deletes a gist by ID. Returns ErrNotFound if it doesn't exist.
func (s *Service) DeleteGist(ctx context.Context, id string) error {
	res := s.DBForCtx(ctx).Delete(&db.Gist{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListGistsByOwner returns all gists for a user, newest first.
func (s *Service) ListGistsByOwner(ctx context.Context, ownerID uint) ([]db.Gist, error) {
	var gists []db.Gist
	err := s.DBForCtx(ctx).Preload("Owner").Where("owner_id = ?", ownerID).Order("created_at DESC").Find(&gists).Error
	return gists, err
}
