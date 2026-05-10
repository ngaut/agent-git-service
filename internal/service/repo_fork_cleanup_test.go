package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/gitstore"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupCleanupForkRepoService(t *testing.T) (*Service, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "gh-server-cleanup-")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.sqlite")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.Exec("PRAGMA busy_timeout = 5000").Error; err != nil {
		t.Fatalf("set busy_timeout: %v", err)
	}
	if err := gdb.Exec("PRAGMA journal_mode = WAL").Error; err != nil {
		t.Fatalf("set journal_mode: %v", err)
	}
	if err := gdb.AutoMigrate(&db.User{}, &db.Repository{}, &db.Label{}, &db.Issue{}, &db.Attachment{}); err != nil {
		t.Fatalf("auto-migrate: %v", err)
	}

	store, err := gitstore.New(tmpDir, gitstore.WithTenantIsolation())
	if err != nil {
		t.Fatalf("gitstore new: %v", err)
	}

	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}

	svc := &Service{
		DB:      gdb,
		Git:     store,
		BaseURL: "http://localhost:8080",
	}

	cleanup := func() {
		_ = sqlDB.Close()
		_ = os.RemoveAll(tmpDir)
	}

	return svc, cleanup
}

func TestCleanupForkRepo_JoinsDeleteAndGitErrors(t *testing.T) {
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
	if !strings.Contains(errMsg, "missing tenant") {
		t.Fatalf("expected git delete error to be included, got: %v", err)
	}
}
