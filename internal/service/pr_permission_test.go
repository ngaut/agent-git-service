package service_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func TestCreatePRPermissionChecks(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "prperm-owner", "prperm-repo")

	repo, err := svc.GetRepo(ctx, "prperm-owner/prperm-repo")
	if err != nil {
		t.Fatalf("get repo: %v", err)
	}
	baseRef := repo.DefaultBranch
	if baseRef == "" {
		baseRef = "main"
	}

	writeUser := db.User{Login: "prperm-write", Name: "prperm-write", Type: db.TypeUser}
	if err := svc.DB.Create(&writeUser).Error; err != nil {
		t.Fatalf("create write user: %v", err)
	}
	if err := svc.AddCollaborator(ctx, repo.ID, writeUser.ID, "write"); err != nil {
		t.Fatalf("add write collaborator: %v", err)
	}

	readUser := db.User{Login: "prperm-read", Name: "prperm-read", Type: db.TypeUser}
	if err := svc.DB.Create(&readUser).Error; err != nil {
		t.Fatalf("create read user: %v", err)
	}
	if err := svc.AddCollaborator(ctx, repo.ID, readUser.ID, "read"); err != nil {
		t.Fatalf("add read collaborator: %v", err)
	}

	adminUser := db.User{Login: "prperm-admin", Name: "prperm-admin", Type: db.TypeUser, SiteAdmin: true}
	if err := svc.DB.Create(&adminUser).Error; err != nil {
		t.Fatalf("create site admin user: %v", err)
	}

	tests := []struct {
		name          string
		authorLogin   string
		expectSuccess bool
	}{
		{name: "owner_allowed", authorLogin: "prperm-owner", expectSuccess: true},
		{name: "write_collaborator_allowed", authorLogin: "prperm-write", expectSuccess: true},
		{name: "site_admin_allowed", authorLogin: "prperm-admin", expectSuccess: true},
		{name: "read_collaborator_denied", authorLogin: "prperm-read", expectSuccess: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.CreatePR(ctx, service.CreatePRInput{
				RepoFullName: "prperm-owner/prperm-repo",
				Title:        "permission test",
				Body:         "permission test",
				HeadRef:      fmt.Sprintf("feature-%s", tt.name),
				BaseRef:      baseRef,
				AuthorLogin:  tt.authorLogin,
			})

			if tt.expectSuccess {
				if err != nil {
					t.Fatalf("expected success, got error: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatal("expected forbidden error, got nil")
			}
			if !errors.Is(err, service.ErrForbidden) {
				t.Fatalf("expected ErrForbidden, got: %v", err)
			}
		})
	}
}
