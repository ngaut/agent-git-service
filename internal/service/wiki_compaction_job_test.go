package service_test

import (
	"context"
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

	started := make(chan string, 1)
	continueCh := make(chan string, 1)
	service.SetTestWikiCompactionJobStartedForTest(svc, func(jobID string) {
		started <- jobID
	})
	service.SetTestWikiCompactionJobContinueForTest(svc, func(jobID string) {
		continueCh <- jobID
	})

	job, err := svc.StartWikiCompaction(service.ContextWithUser(ctx, owner), full)
	if err != nil {
		t.Fatalf("StartWikiCompaction err = %v", err)
	}
	if job.ID != staleJob.ID {
		t.Fatalf("job.ID = %q, want stale job %q", job.ID, staleJob.ID)
	}
	select {
	case startedJobID := <-started:
		if startedJobID != staleJob.ID {
			t.Fatalf("started job id = %q, want %q", startedJobID, staleJob.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stale job restart")
	}
	select {
	case continuedJobID := <-continueCh:
		if continuedJobID != staleJob.ID {
			t.Fatalf("continued job id = %q, want %q", continuedJobID, staleJob.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stale job worker continuation")
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

	started := make(chan string, 1)
	service.SetTestWikiCompactionJobStartedForTest(svc, func(jobID string) {
		started <- jobID
	})

	job, err := svc.StartWikiCompaction(service.ContextWithUser(ctx, owner), full)
	if err != nil {
		t.Fatalf("StartWikiCompaction err = %v", err)
	}
	if job.ID != freshJob.ID {
		t.Fatalf("job.ID = %q, want fresh job %q", job.ID, freshJob.ID)
	}
	select {
	case startedJobID := <-started:
		t.Fatalf("fresh running job should not restart, but started %q", startedJobID)
	case <-time.After(200 * time.Millisecond):
	}
}
