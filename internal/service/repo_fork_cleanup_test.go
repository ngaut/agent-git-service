package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"gorm.io/gorm"
)

func setupCleanupForkRepoService(t *testing.T) (*Service, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "gh-server-cleanup-")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}

	gdb, dbCleanup := openMigratedServiceTestDB(t)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatalf("gitstore new: %v", err)
	}

	svc := &Service{
		DB:      gdb,
		Git:     store,
		BaseURL: "http://localhost:8080",
	}

	cleanup := func() {
		dbCleanup()
		_ = os.RemoveAll(tmpDir)
	}

	return svc, cleanup
}

func TestCleanupForkRepo_ReturnsDeleteError(t *testing.T) {
	svc, cleanup := setupCleanupForkRepoService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "cleanup-owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	repo := db.Repository{
		Name:          "repo",
		FullName:      "cleanup-owner/repo",
		OwnerID:       owner.ID,
		DefaultBranch: "main",
	}
	if err := svc.DB.Create(&repo).Error; err != nil {
		t.Fatalf("create repo: %v", err)
	}

	const cbName = "test:cleanup_fork_repo_update_fail"
	if err := svc.DB.Callback().Update().Before("gorm:update").Register(cbName, func(tx *gorm.DB) {
		if tx.Statement.Table == "repositories" || (tx.Statement.Schema != nil && tx.Statement.Schema.Table == "repositories") {
			tx.AddError(errors.New("forced update failure"))
		}
	}); err != nil {
		t.Fatalf("register update callback: %v", err)
	}
	defer func() {
		_ = svc.DB.Callback().Update().Remove(cbName)
	}()

	err := svc.cleanupForkRepo(ctx, repo.FullName, "test")
	if err == nil {
		t.Fatal("expected cleanupForkRepo to return error")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "forced update failure") {
		t.Fatalf("expected delete error to be included, got: %v", err)
	}
}
