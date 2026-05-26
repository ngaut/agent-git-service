package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

func TestStartWikiCompaction_RestartsStaleRunningJob_Issue1462(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "wiki-compact-job-owner", Name: "wiki-compact-job-owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if _, err := svc.CreateRepo(service.ContextWithUser(ctx, owner), service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-compact-job-restart",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	full := owner.Login + "/wiki-compact-job-restart"

	page, err := svc.PutWikiPage(service.ContextWithUser(ctx, owner), full, "home", "# Home\n\nFirst version.\n", "create home", "")
	if err != nil {
		t.Fatalf("PutWikiPage(create): %v", err)
	}
	if _, err := svc.PutWikiPage(service.ContextWithUser(ctx, owner), full, "home", "# Home\n\nSecond version.\n", "update home", page.SHA); err != nil {
		t.Fatalf("PutWikiPage(update): %v", err)
	}

	rep, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	staleStartedAt := time.Now().UTC().Add(-10 * time.Minute)
	staleJob := db.WikiCompactionJob{
		ID:            "stale-running-job",
		RepositoryID:  rep.ID,
		Status:        service.WikiCompactionJobRunning,
		RequestedByID: &owner.ID,
		StartedAt:     &staleStartedAt,
	}
	if err := svc.DB.Create(&staleJob).Error; err != nil {
		t.Fatalf("create stale job: %v", err)
	}

	if _, err := svc.StartWikiCompaction(service.ContextWithUser(ctx, owner), full); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("StartWikiCompaction err = %v, want ErrConflict", err)
	}
}

func TestStartWikiCompaction_DoesNotRestartFreshRunningJob_Issue1462(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "wiki-compact-job-owner-fresh", Name: "wiki-compact-job-owner-fresh", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if _, err := svc.CreateRepo(service.ContextWithUser(ctx, owner), service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-compact-job-fresh",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	full := owner.Login + "/wiki-compact-job-fresh"

	page, err := svc.PutWikiPage(service.ContextWithUser(ctx, owner), full, "home", "# Home\n\nFirst version.\n", "create home", "")
	if err != nil {
		t.Fatalf("PutWikiPage(create): %v", err)
	}
	if _, err := svc.PutWikiPage(service.ContextWithUser(ctx, owner), full, "home", "# Home\n\nSecond version.\n", "update home", page.SHA); err != nil {
		t.Fatalf("PutWikiPage(update): %v", err)
	}

	rep, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	freshStartedAt := time.Now().UTC().Add(-1 * time.Minute)
	freshJob := db.WikiCompactionJob{
		ID:            "fresh-running-job",
		RepositoryID:  rep.ID,
		Status:        service.WikiCompactionJobRunning,
		RequestedByID: &owner.ID,
		StartedAt:     &freshStartedAt,
		UpdatedAt:     time.Now().UTC(),
	}
	if err := svc.DB.Create(&freshJob).Error; err != nil {
		t.Fatalf("create fresh job: %v", err)
	}

	if _, err := svc.StartWikiCompaction(service.ContextWithUser(ctx, owner), full); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("StartWikiCompaction err = %v, want ErrConflict", err)
	}
}
