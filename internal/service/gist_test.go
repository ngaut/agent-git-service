package service_test

import (
	"context"
	"testing"

	"gh-server/internal/db"
)

func TestGistCRUD(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.Create(&db.User{Login: "gistuser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "gistuser")

	// Create
	g := &db.Gist{
		ID:          "gist_abc123",
		OwnerID:     u.ID,
		Description: "test gist",
		Public:      true,
		Files:       `{"hello.go":{"content":"package main"}}`,
	}
	if err := svc.CreateGist(ctx, g); err != nil {
		t.Fatalf("CreateGist failed: %v", err)
	}

	// Get
	got, err := svc.GetGist(ctx, "gist_abc123")
	if err != nil {
		t.Fatalf("GetGist failed: %v", err)
	}
	if got.Description != "test gist" {
		t.Errorf("expected description 'test gist', got %s", got.Description)
	}
	if got.Owner.Login != "gistuser" {
		t.Errorf("expected owner gistuser, got %s", got.Owner.Login)
	}

	// Update description
	newDesc := "updated description"
	if err := svc.UpdateGist(ctx, &got, &newDesc, nil); err != nil {
		t.Fatalf("UpdateGist(desc) failed: %v", err)
	}
	if got.Description != "updated description" {
		t.Errorf("expected 'updated description', got %s", got.Description)
	}

	// Update files (add + delete)
	files := map[string]map[string]string{
		"world.py": {"content": "print('hello')"},
		"hello.go": nil, // delete
	}
	if err := svc.UpdateGist(ctx, &got, nil, files); err != nil {
		t.Fatalf("UpdateGist(files) failed: %v", err)
	}

	// List
	gists, err := svc.ListGistsByOwner(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListGistsByOwner failed: %v", err)
	}
	if len(gists) != 1 {
		t.Errorf("expected 1 gist, got %d", len(gists))
	}

	// Delete
	if err := svc.DeleteGist(ctx, "gist_abc123"); err != nil {
		t.Fatalf("DeleteGist failed: %v", err)
	}

	// Delete non-existent
	err = svc.DeleteGist(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for deleting non-existent gist")
	}

	// Get after delete
	_, err = svc.GetGist(ctx, "gist_abc123")
	if err == nil {
		t.Error("expected error after delete")
	}
}

// TestUpdateGist_MalformedFilesJSON verifies that UpdateGist with malformed or
// empty Files JSON falls back to an empty map and merges new files without panic.
func TestUpdateGist_MalformedFilesJSON(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.Create(&db.User{Login: "gistbaduser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "gistbaduser")

	cases := []struct {
		name         string
		initialFiles string
	}{
		{"empty string", ""},
		{"malformed JSON", `{not valid json`},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gistID := "gist_bad_" + string(rune('a'+i))
			g := &db.Gist{
				ID:          gistID,
				OwnerID:     u.ID,
				Description: "test",
				Public:      true,
				Files:       tc.initialFiles,
			}
			if err := svc.CreateGist(ctx, g); err != nil {
				t.Fatalf("CreateGist failed: %v", err)
			}

			got, err := svc.GetGist(ctx, gistID)
			if err != nil {
				t.Fatalf("GetGist failed: %v", err)
			}

			// UpdateGist should not panic; it should fall back to empty map
			// and merge the new file in.
			newFiles := map[string]map[string]string{
				"new.txt": {"content": "hello"},
			}
			if err := svc.UpdateGist(ctx, &got, nil, newFiles); err != nil {
				t.Fatalf("UpdateGist failed: %v", err)
			}

			// Verify the update was saved correctly.
			updated, err := svc.GetGist(ctx, gistID)
			if err != nil {
				t.Fatalf("GetGist after update failed: %v", err)
			}
			if updated.Files == "" {
				t.Fatal("expected non-empty Files after update")
			}
			// The Files JSON should contain the new file.
			if !containsStr(updated.Files, "new.txt") {
				t.Errorf("expected Files to contain new.txt, got %s", updated.Files)
			}
		})
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
