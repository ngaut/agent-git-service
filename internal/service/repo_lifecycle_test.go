package service_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"gorm.io/gorm"
)

func TestRepoLifecycle_CreateDuplicateConflict(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	svc.DB.Create(&db.User{Login: "owner", Name: "owner", Type: db.TypeUser})

	in := service.CreateRepoInput{
		OwnerLogin:    "owner",
		Name:          "myrepo",
		DefaultBranch: "main",
	}

	if _, err := svc.CreateRepo(ctx, in); err != nil {
		t.Fatalf("first create: %v", err)
	}

	_, err := svc.CreateRepo(ctx, in)
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("expected ErrConflict on duplicate create, got: %v", err)
	}

	// Verify only one repo exists.
	var count int64
	svc.DB.Model(&db.Repository{}).Where("full_name = ?", "owner/myrepo").Count(&count)
	if count != 1 {
		t.Fatalf("expected exactly 1 repo, got %d", count)
	}
}

func TestRepoLifecycle_ForkSuccess(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	svc.DB.Create(&db.User{Login: "src-owner", Name: "src-owner", Type: db.TypeUser})
	svc.DB.Create(&db.User{Login: "fork-owner", Name: "fork-owner", Type: db.TypeUser})

	src, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "src-owner",
		Name:          "upstream",
		DefaultBranch: "main",
		AutoInit:      true,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}

	fork, err := svc.ForkRepo(ctx, src.FullName, "fork-owner", "")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}

	if !fork.Fork {
		t.Error("expected fork.Fork == true")
	}
	if fork.ParentID == nil || *fork.ParentID != src.ID {
		t.Errorf("expected ParentID == %d, got %v", src.ID, fork.ParentID)
	}
	if fork.Owner.Login != "fork-owner" {
		t.Errorf("expected owner fork-owner, got %s", fork.Owner.Login)
	}
	if fork.FullName != "fork-owner/upstream" {
		t.Errorf("expected full_name fork-owner/upstream, got %s", fork.FullName)
	}

	// Verify git directory exists and is non-empty.
	if !svc.Git.Exists(ctx, fork.FullName) {
		t.Error("expected fork git dir to exist")
	}
	if svc.IsRepoEmpty(ctx, fork.FullName) {
		t.Error("expected fork to be non-empty (inherited content)")
	}
}

func TestRepoLifecycle_TransferRepo(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	svc.DB.Create(&db.User{Login: "alice", Name: "alice", Type: db.TypeUser})
	svc.DB.Create(&db.User{Login: "bob", Name: "bob", Type: db.TypeUser})

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "alice",
		Name:          "proj",
		DefaultBranch: "main",
		AutoInit:      true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	transferred, err := svc.TransferRepo(ctx, "alice/proj", "bob")
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}

	if transferred.Owner.Login != "bob" {
		t.Errorf("expected owner bob, got %s", transferred.Owner.Login)
	}
	if transferred.FullName != "bob/proj" {
		t.Errorf("expected full_name bob/proj, got %s", transferred.FullName)
	}

	// Old name should redirect to new repo via repo_redirects.
	oldRepo, err := svc.GetRepo(ctx, "alice/proj")
	if err != nil {
		t.Errorf("expected old name to redirect, got: %v", err)
	}
	if oldRepo.ID != transferred.ID {
		t.Errorf("expected old name to redirect to same repo ID %d, got %d", transferred.ID, oldRepo.ID)
	}
	got, err := svc.GetRepo(ctx, "bob/proj")
	if err != nil {
		t.Fatalf("get transferred repo: %v", err)
	}
	if got.ID != transferred.ID {
		t.Errorf("expected same repo ID %d, got %d", transferred.ID, got.ID)
	}

	// Git path assertions.
	if !svc.Git.Exists(ctx, "bob/proj") {
		t.Error("expected new git path to exist")
	}
	if svc.Git.Exists(ctx, "alice/proj") {
		t.Error("expected old git path to be gone")
	}
}

func TestRepoLifecycle_TransferRepo_UpdatesAutolink(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "alice", Name: "alice", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := svc.DB.Create(&db.User{Login: "bob", Name: "bob", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create new owner: %v", err)
	}

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "alice",
		Name:          "proj",
		DefaultBranch: "main",
		AutoInit:      true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	if err := svc.CreateAutolink(ctx, &db.Autolink{
		RepositoryFullName: "alice/proj",
		KeyPrefix:          "TICKET-",
		URLTemplate:        "https://example.com/tickets/<num>",
	}); err != nil {
		t.Fatalf("create autolink: %v", err)
	}

	_, err = svc.TransferRepo(ctx, "alice/proj", "bob")
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}

	autolinksNew, err := svc.ListAutolinks(ctx, "bob/proj")
	if err != nil {
		t.Fatalf("list autolinks new: %v", err)
	}
	if len(autolinksNew) != 1 {
		t.Fatalf("expected 1 autolink for new owner, got %d", len(autolinksNew))
	}
	if autolinksNew[0].RepositoryFullName != "bob/proj" {
		t.Errorf("expected autolink repository_full_name to be updated, got %q", autolinksNew[0].RepositoryFullName)
	}
	if autolinksNew[0].KeyPrefix != "TICKET-" {
		t.Errorf("expected autolink key_prefix to remain TICKET-, got %q", autolinksNew[0].KeyPrefix)
	}

	autolinksOld, err := svc.ListAutolinks(ctx, "alice/proj")
	if err != nil {
		t.Fatalf("list autolinks old: %v", err)
	}
	if len(autolinksOld) != 0 {
		t.Fatalf("expected 0 autolinks for old owner, got %d", len(autolinksOld))
	}
}

func TestRepoLifecycle_TransferRepo_RollbackOnUpdateFailure(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "alice", Name: "alice", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := svc.DB.Create(&db.User{Login: "bob", Name: "bob", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create new owner: %v", err)
	}

	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "alice",
		Name:          "proj",
		DefaultBranch: "main",
		AutoInit:      true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	if err := svc.CreateAutolink(ctx, &db.Autolink{
		RepositoryFullName: "alice/proj",
		KeyPrefix:          "TICKET-",
		URLTemplate:        "https://example.com/tickets/<num>",
	}); err != nil {
		t.Fatalf("create autolink: %v", err)
	}

	const cbName = "test:transfer_repo_update_fail"
	var failOnce atomic.Bool
	failOnce.Store(true)
	if err := svc.DB.Callback().Update().Before("gorm:update").Register(cbName, func(tx *gorm.DB) {
		if tx.Statement.Table == "repositories" && failOnce.CompareAndSwap(true, false) {
			tx.AddError(errors.New("forced transfer update failure"))
		}
	}); err != nil {
		t.Fatalf("register update callback: %v", err)
	}
	defer func() {
		_ = svc.DB.Callback().Update().Remove(cbName)
	}()

	if _, err := svc.TransferRepo(ctx, "alice/proj", "bob"); err == nil {
		t.Fatalf("expected TransferRepo to fail when update is forced to error")
	}
	if failOnce.Load() {
		t.Fatalf("expected injected update failure to trigger")
	}

	repo, err := svc.GetRepo(ctx, "alice/proj")
	if err != nil {
		t.Fatalf("expected repo to remain with original owner after rollback, got err=%v", err)
	}
	if repo.Owner.Login != "alice" {
		t.Fatalf("expected owner to remain alice after rollback, got %s", repo.Owner.Login)
	}
	if _, err := svc.GetRepo(ctx, "bob/proj"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected new owner repo to be absent after rollback, got err=%v", err)
	}

	if !svc.Git.Exists(ctx, "alice/proj") {
		t.Error("expected old git path to exist after rollback")
	}
	if svc.Git.Exists(ctx, "bob/proj") {
		t.Error("expected new git path to be removed after rollback")
	}

	autolinksOld, err := svc.ListAutolinks(ctx, "alice/proj")
	if err != nil {
		t.Fatalf("list autolinks old: %v", err)
	}
	if len(autolinksOld) != 1 {
		t.Fatalf("expected 1 autolink for old owner after rollback, got %d", len(autolinksOld))
	}
	autolinksNew, err := svc.ListAutolinks(ctx, "bob/proj")
	if err != nil {
		t.Fatalf("list autolinks new: %v", err)
	}
	if len(autolinksNew) != 0 {
		t.Fatalf("expected 0 autolinks for new owner after rollback, got %d", len(autolinksNew))
	}
}

func TestRepoLifecycle_DeleteDetachesForks(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	svc.DB.Create(&db.User{Login: "base-owner", Name: "base-owner", Type: db.TypeUser})
	svc.DB.Create(&db.User{Login: "forker", Name: "forker", Type: db.TypeUser})

	base, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "base-owner",
		Name:          "base",
		DefaultBranch: "main",
		AutoInit:      true,
	})
	if err != nil {
		t.Fatalf("create base: %v", err)
	}

	fork, err := svc.ForkRepo(ctx, base.FullName, "forker", "")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}

	// Delete the base repo.
	if err := svc.DeleteRepo(ctx, base.FullName); err != nil {
		t.Fatalf("delete base: %v", err)
	}

	// Base should be gone from DB and git.
	if _, err := svc.GetRepo(ctx, base.FullName); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("expected base to be not found, got: %v", err)
	}
	if svc.Git.Exists(ctx, base.FullName) {
		t.Error("expected base git dir to be removed")
	}

	// Fork should still exist but be detached.
	detached, err := svc.GetRepo(ctx, fork.FullName)
	if err != nil {
		t.Fatalf("get fork after base deletion: %v", err)
	}
	if detached.Fork {
		t.Error("expected detached fork to have Fork == false")
	}
	if detached.ParentID != nil {
		t.Errorf("expected detached fork ParentID == nil, got %v", detached.ParentID)
	}
}

func TestRepoLifecycle_DeleteRenamedRepoRemovesRedirects(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	svc.DB.Create(&db.User{Login: "owner", Name: "owner", Type: db.TypeUser})

	created, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "owner",
		Name:          "rename-me",
		DefaultBranch: "main",
		AutoInit:      true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	oldFullName := created.FullName

	renamed, err := svc.RenameRepo(ctx, oldFullName, "renamed")
	if err != nil {
		t.Fatalf("rename repo: %v", err)
	}

	var redirectsBefore int64
	if err := svc.DB.Model(&db.RepoRedirect{}).Where("repo_id = ?", renamed.ID).Count(&redirectsBefore).Error; err != nil {
		t.Fatalf("count redirects before delete: %v", err)
	}
	if redirectsBefore == 0 {
		t.Fatalf("expected redirect row for renamed repo id=%d", renamed.ID)
	}

	if err := svc.DeleteRepo(ctx, renamed.FullName); err != nil {
		t.Fatalf("delete renamed repo: %v", err)
	}

	var redirectsAfter int64
	if err := svc.DB.Model(&db.RepoRedirect{}).Where("repo_id = ? OR old_full_name = ?", renamed.ID, oldFullName).Count(&redirectsAfter).Error; err != nil {
		t.Fatalf("count redirects after delete: %v", err)
	}
	if redirectsAfter != 0 {
		t.Fatalf("expected redirects to be removed after delete, got %d", redirectsAfter)
	}
}

func TestRepoLifecycle_DeleteRemovesIssueEvents(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	svc.DB.Create(&db.User{Login: "owner", Name: "owner", Type: db.TypeUser})

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "owner",
		Name:          "with-events",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: repo.FullName,
		Title:        "Issue with events",
		Body:         "body",
		AuthorLogin:  "owner",
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	var eventCount int64
	if err := svc.DB.Model(&db.IssueEvent{}).Where("issue_id = ?", issue.ID).Count(&eventCount).Error; err != nil {
		t.Fatalf("count issue events before delete: %v", err)
	}
	if eventCount == 0 {
		t.Fatal("expected issue events to exist before repo deletion")
	}

	if err := svc.DeleteRepo(ctx, repo.FullName); err != nil {
		t.Fatalf("delete repo: %v", err)
	}

	if err := svc.DB.Model(&db.IssueEvent{}).Where("issue_id = ?", issue.ID).Count(&eventCount).Error; err != nil {
		t.Fatalf("count issue events after delete: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("expected issue events to be removed, got %d", eventCount)
	}
}

func TestRepoLifecycle_DeleteNotFound(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	err := svc.DeleteRepo(context.Background(), "ghost/repo")
	if !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

func TestRepoLifecycle_EmptinessAndDiskUsage(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	svc.DB.Create(&db.User{Login: "user", Name: "user", Type: db.TypeUser})

	// Unseeded repo should be empty.
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "user",
		Name:          "empty-repo",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create empty repo: %v", err)
	}
	if !svc.IsRepoEmpty(ctx, "user/empty-repo") {
		t.Error("expected unseeded repo to be empty")
	}

	// Seeded repo should be non-empty.
	_, err = svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "user",
		Name:          "seeded-repo",
		DefaultBranch: "main",
		AutoInit:      true,
	})
	if err != nil {
		t.Fatalf("create seeded repo: %v", err)
	}
	if svc.IsRepoEmpty(ctx, "user/seeded-repo") {
		t.Error("expected seeded repo to be non-empty")
	}

	// Disk usage of seeded repo should be > 0.
	usage := svc.GitDiskUsageKB(ctx, "user/seeded-repo")
	if usage <= 0 {
		t.Errorf("expected disk usage > 0 for seeded repo, got %d", usage)
	}

	// Disk usage of nonexistent repo should be 0.
	usage = svc.GitDiskUsageKB(ctx, "nonexistent/repo")
	if usage != 0 {
		t.Errorf("expected disk usage 0 for nonexistent repo, got %d", usage)
	}
}
