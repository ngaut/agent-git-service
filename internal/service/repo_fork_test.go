package service_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
	"gorm.io/gorm"
)

func TestForkRepoGitFailureCompensatesDBAndGit(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "fork-src", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create source owner: %v", err)
	}
	if err := svc.DB.Create(&db.User{Login: "fork-target", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create target owner: %v", err)
	}
	src, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "fork-src",
		Name:          "repo",
		DefaultBranch: "main",
		AddReadme:     true,
	})
	if err != nil {
		t.Fatalf("create source repo: %v", err)
	}

	// Remove source git repo so git fork step fails deterministically.
	if err := svc.Git.Delete(ctx, src.FullName); err != nil {
		t.Fatalf("delete source git repo: %v", err)
	}

	targetFullName := "fork-target/repo"
	if _, err := svc.ForkRepo(ctx, src.FullName, "fork-target", ""); err == nil {
		t.Fatalf("expected ForkRepo to fail when source git repo is missing")
	}
	if _, err := svc.GetRepo(ctx, targetFullName); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected fork repo to be removed from DB, got err=%v", err)
	}
	if svc.Git.Exists(ctx, targetFullName) {
		t.Fatalf("expected fork repo git directory to be removed")
	}
}

func TestForkRepoDBFinalizeFailureCompensatesDBAndGit(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "fork-src", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create source owner: %v", err)
	}
	if err := svc.DB.Create(&db.User{Login: "fork-target", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create target owner: %v", err)
	}
	src, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "fork-src",
		Name:          "repo",
		DefaultBranch: "main",
		AddReadme:     true,
	})
	if err != nil {
		t.Fatalf("create source repo: %v", err)
	}

	const cbName = "test:fork_finalize_fail_once"
	var failOnce atomic.Bool
	failOnce.Store(true)
	if err := svc.DB.Callback().Update().Before("gorm:update").Register(cbName, func(tx *gorm.DB) {
		if tx.Statement.Table == "repositories" && failOnce.CompareAndSwap(true, false) {
			tx.AddError(errors.New("forced repository finalize failure"))
		}
	}); err != nil {
		t.Fatalf("register update callback: %v", err)
	}
	defer func() {
		_ = svc.DB.Callback().Update().Remove(cbName)
	}()

	targetFullName := "fork-target/repo"
	if _, err := svc.ForkRepo(ctx, src.FullName, "fork-target", ""); err == nil {
		t.Fatalf("expected ForkRepo to fail when DB finalize is injected to fail")
	}
	if failOnce.Load() {
		t.Fatalf("expected injected DB finalize failure to trigger")
	}
	if _, err := svc.GetRepo(ctx, targetFullName); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected fork repo to be removed from DB after compensation, got err=%v", err)
	}
	if svc.Git.Exists(ctx, targetFullName) {
		t.Fatalf("expected fork repo git directory to be removed after compensation")
	}
}

func TestForkRepoGitFailureCleanupFailureReturnsJoinedError(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "fork-src", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create source owner: %v", err)
	}
	if err := svc.DB.Create(&db.User{Login: "fork-target", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create target owner: %v", err)
	}
	src, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "fork-src",
		Name:          "repo",
		DefaultBranch: "main",
		AddReadme:     true,
	})
	if err != nil {
		t.Fatalf("create source repo: %v", err)
	}

	// Remove source git repo so git fork step fails.
	if err := svc.Git.Delete(ctx, src.FullName); err != nil {
		t.Fatalf("delete source git repo: %v", err)
	}

	// Inject a delete callback to make cleanup (DeleteRepo) fail.
	const cbName = "test:fork_cleanup_delete_fail"
	if err := svc.DB.Callback().Delete().Before("gorm:delete").Register(cbName, func(tx *gorm.DB) {
		if tx.Statement.Table == "repositories" || tx.Statement.Schema != nil && tx.Statement.Schema.Table == "repositories" {
			tx.AddError(errors.New("forced cleanup delete failure"))
		}
	}); err != nil {
		t.Fatalf("register delete callback: %v", err)
	}
	defer func() {
		_ = svc.DB.Callback().Delete().Remove(cbName)
	}()

	_, err = svc.ForkRepo(ctx, src.FullName, "fork-target", "")
	if err == nil {
		t.Fatal("expected ForkRepo to fail")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "cleanup failed") {
		t.Fatalf("expected error to mention cleanup failure, got: %v", err)
	}
}

func TestForkRepoDBFinalizeFailureCleanupFailureReturnsJoinedError(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "fork-src", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create source owner: %v", err)
	}
	if err := svc.DB.Create(&db.User{Login: "fork-target", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create target owner: %v", err)
	}
	src, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "fork-src",
		Name:          "repo",
		DefaultBranch: "main",
		AddReadme:     true,
	})
	if err != nil {
		t.Fatalf("create source repo: %v", err)
	}

	// Inject update callback to fail finalize (first update on repositories),
	// then inject delete callback to fail cleanup.
	const updateCB = "test:fork_finalize_fail_once2"
	var failOnce atomic.Bool
	failOnce.Store(true)
	if err := svc.DB.Callback().Update().Before("gorm:update").Register(updateCB, func(tx *gorm.DB) {
		if tx.Statement.Table == "repositories" && failOnce.CompareAndSwap(true, false) {
			tx.AddError(errors.New("forced finalize failure"))
		}
	}); err != nil {
		t.Fatalf("register update callback: %v", err)
	}
	defer func() {
		_ = svc.DB.Callback().Update().Remove(updateCB)
	}()

	const deleteCB = "test:fork_cleanup_delete_fail2"
	if err := svc.DB.Callback().Delete().Before("gorm:delete").Register(deleteCB, func(tx *gorm.DB) {
		if tx.Statement.Table == "repositories" || tx.Statement.Schema != nil && tx.Statement.Schema.Table == "repositories" {
			tx.AddError(errors.New("forced cleanup delete failure"))
		}
	}); err != nil {
		t.Fatalf("register delete callback: %v", err)
	}
	defer func() {
		_ = svc.DB.Callback().Delete().Remove(deleteCB)
	}()

	_, err = svc.ForkRepo(ctx, src.FullName, "fork-target", "")
	if err == nil {
		t.Fatal("expected ForkRepo to fail")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "forced finalize failure") {
		t.Fatalf("expected error to contain original finalize failure, got: %v", err)
	}
	if !strings.Contains(errMsg, "cleanup failed") {
		t.Fatalf("expected error to mention cleanup failure, got: %v", err)
	}
}

func TestResolveForkRepo_EnforcesForkAndParent(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	for _, login := range []string{"base-owner", "fork-owner", "other-owner"} {
		if err := svc.DB.Create(&db.User{Login: login, Type: db.TypeUser}).Error; err != nil {
			t.Fatalf("create user %s: %v", login, err)
		}
	}

	base, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "base-owner",
		Name:          "upstream",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create base repo: %v", err)
	}
	otherBase, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "other-owner",
		Name:          "other",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create other base repo: %v", err)
	}

	// Direct name match but not a fork.
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "fork-owner",
		Name:          base.Name,
		DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("create non-fork matching name repo: %v", err)
	}

	orphan, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "fork-owner",
		Name:          "orphan",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create orphan repo: %v", err)
	}
	if err := svc.DB.Model(&db.Repository{}).Where("id = ?", orphan.ID).
		Updates(map[string]any{"fork": true, "parent_id": nil}).Error; err != nil {
		t.Fatalf("mark orphan fork: %v", err)
	}

	wrongParent, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "fork-owner",
		Name:          "wrong-parent",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create wrong parent repo: %v", err)
	}
	if err := svc.DB.Model(&db.Repository{}).Where("id = ?", wrongParent.ID).
		Updates(map[string]any{"fork": true, "parent_id": otherBase.ID}).Error; err != nil {
		t.Fatalf("mark wrong-parent fork: %v", err)
	}

	directName := "fork-owner/" + base.Name
	const cbName = "test:resolve_fork_repo_block_direct"
	if err := svc.DB.Callback().Query().After("gorm:query").Register(cbName, func(tx *gorm.DB) {
		if tx.Statement == nil {
			return
		}
		if tx.Statement.Table != "repositories" && (tx.Statement.Schema == nil || tx.Statement.Schema.Table != "repositories") {
			return
		}
		for _, v := range tx.Statement.Vars {
			if s, ok := v.(string); ok && s == directName {
				tx.AddError(errors.New("forced direct repo lookup failure"))
				return
			}
		}
	}); err != nil {
		t.Fatalf("register query callback: %v", err)
	}
	defer func() {
		_ = svc.DB.Callback().Query().Remove(cbName)
	}()

	if got := svc.ResolveForkRepo(ctx, base.FullName, "fork-owner"); got != "" {
		t.Fatalf("expected no matching fork, got %q", got)
	}

	correct, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "fork-owner",
		Name:          "correct-fork",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create correct fork repo: %v", err)
	}
	if err := svc.DB.Model(&db.Repository{}).Where("id = ?", correct.ID).
		Updates(map[string]any{"fork": true, "parent_id": base.ID}).Error; err != nil {
		t.Fatalf("mark correct fork: %v", err)
	}

	if got := svc.ResolveForkRepo(ctx, base.FullName, "fork-owner"); got != correct.FullName {
		t.Fatalf("expected fork %q, got %q", correct.FullName, got)
	}
}

func TestForkRepo_CustomNameCopiesMetadata(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "src-owner", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create source owner: %v", err)
	}
	if err := svc.DB.Create(&db.User{Login: "fork-owner", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create fork owner: %v", err)
	}

	src, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "src-owner",
		Name:          "source",
		Description:   "source description",
		Private:       true,
		DefaultBranch: "dev",
		AddReadme:     true,
	})
	if err != nil {
		t.Fatalf("create source repo: %v", err)
	}

	fork, err := svc.ForkRepo(ctx, src.FullName, "fork-owner", "custom-name")
	if err != nil {
		t.Fatalf("fork repo: %v", err)
	}
	if fork.Name != "custom-name" {
		t.Fatalf("expected fork name custom-name, got %q", fork.Name)
	}
	if fork.FullName != "fork-owner/custom-name" {
		t.Fatalf("expected fork full name fork-owner/custom-name, got %q", fork.FullName)
	}
	if fork.Description != src.Description {
		t.Fatalf("expected fork description %q, got %q", src.Description, fork.Description)
	}
	if fork.Private != src.Private {
		t.Fatalf("expected fork private %v, got %v", src.Private, fork.Private)
	}
	if fork.DefaultBranch != src.DefaultBranch {
		t.Fatalf("expected fork default branch %q, got %q", src.DefaultBranch, fork.DefaultBranch)
	}
	if !fork.Fork {
		t.Fatalf("expected fork.Fork to be true")
	}
	if fork.ParentID == nil || *fork.ParentID != src.ID {
		t.Fatalf("expected fork ParentID %d, got %v", src.ID, fork.ParentID)
	}
	if fork.Owner.Login != "fork-owner" {
		t.Fatalf("expected fork owner fork-owner, got %q", fork.Owner.Login)
	}
}

func TestListForks_RespectsContextDBScope(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	baseOwner := db.User{Login: "base-owner", Type: db.TypeUser}
	forkOwner1 := db.User{Login: "fork-owner1", Type: db.TypeUser}
	forkOwner2 := db.User{Login: "fork-owner2", Type: db.TypeUser}
	for _, u := range []*db.User{&baseOwner, &forkOwner1, &forkOwner2} {
		if err := svc.DB.Create(u).Error; err != nil {
			t.Fatalf("create user %s: %v", u.Login, err)
		}
	}

	base, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    baseOwner.Login,
		Name:          "upstream",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create base repo: %v", err)
	}
	fork1, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    forkOwner1.Login,
		Name:          "upstream",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create fork1 repo: %v", err)
	}
	fork2, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    forkOwner2.Login,
		Name:          "upstream",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create fork2 repo: %v", err)
	}
	if err := svc.DB.Model(&db.Repository{}).Where("id = ?", fork1.ID).
		Updates(map[string]any{"fork": true, "parent_id": base.ID}).Error; err != nil {
		t.Fatalf("mark fork1: %v", err)
	}
	if err := svc.DB.Model(&db.Repository{}).Where("id = ?", fork2.ID).
		Updates(map[string]any{"fork": true, "parent_id": base.ID}).Error; err != nil {
		t.Fatalf("mark fork2: %v", err)
	}

	forks, err := svc.ListForks(ctx, base.ID)
	if err != nil {
		t.Fatalf("list forks: %v", err)
	}
	if len(forks) != 2 {
		t.Fatalf("expected 2 forks, got %d", len(forks))
	}

	scopedDB := svc.DB.Where("owner_id = ?", forkOwner1.ID)
	scopedCtx := service.ContextWithDB(ctx, scopedDB)
	scopedForks, err := svc.ListForks(scopedCtx, base.ID)
	if err != nil {
		t.Fatalf("list forks (scoped): %v", err)
	}
	if len(scopedForks) != 1 {
		t.Fatalf("expected 1 scoped fork, got %d", len(scopedForks))
	}
	if scopedForks[0].Owner.Login != forkOwner1.Login {
		t.Fatalf("expected fork owner %q, got %q", forkOwner1.Login, scopedForks[0].Owner.Login)
	}
}

func TestForkCount_RespectsContextDBScope(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	baseOwner := db.User{Login: "count-base-owner", Type: db.TypeUser}
	forkOwner1 := db.User{Login: "count-fork-owner1", Type: db.TypeUser}
	forkOwner2 := db.User{Login: "count-fork-owner2", Type: db.TypeUser}
	for _, u := range []*db.User{&baseOwner, &forkOwner1, &forkOwner2} {
		if err := svc.DB.Create(u).Error; err != nil {
			t.Fatalf("create user %s: %v", u.Login, err)
		}
	}

	base, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    baseOwner.Login,
		Name:          "upstream",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create base repo: %v", err)
	}
	fork1, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    forkOwner1.Login,
		Name:          "upstream",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create fork1 repo: %v", err)
	}
	fork2, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    forkOwner2.Login,
		Name:          "upstream",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create fork2 repo: %v", err)
	}
	if err := svc.DB.Model(&db.Repository{}).Where("id = ?", fork1.ID).
		Updates(map[string]any{"fork": true, "parent_id": base.ID}).Error; err != nil {
		t.Fatalf("mark fork1: %v", err)
	}
	if err := svc.DB.Model(&db.Repository{}).Where("id = ?", fork2.ID).
		Updates(map[string]any{"fork": true, "parent_id": base.ID}).Error; err != nil {
		t.Fatalf("mark fork2: %v", err)
	}

	if count := svc.ForkCount(ctx, base.ID); count != 2 {
		t.Fatalf("expected fork count 2, got %d", count)
	}

	scopedDB := svc.DB.Where("owner_id = ?", forkOwner1.ID)
	scopedCtx := service.ContextWithDB(ctx, scopedDB)
	if count := svc.ForkCount(scopedCtx, base.ID); count != 1 {
		t.Fatalf("expected scoped fork count 1, got %d", count)
	}
}
