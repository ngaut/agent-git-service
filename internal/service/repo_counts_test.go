package service_test

import (
	"context"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func TestRepoCountBatch(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "owner", Type: db.TypeUser}
	stargazer := db.User{Login: "star1", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := svc.DB.Create(&stargazer).Error; err != nil {
		t.Fatalf("create stargazer: %v", err)
	}

	repoA, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: owner.Login, Name: "a"})
	if err != nil {
		t.Fatalf("create repoA: %v", err)
	}
	repoB, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: owner.Login, Name: "b"})
	if err != nil {
		t.Fatalf("create repoB: %v", err)
	}
	// repoC has no stars, no forks, no open issues — must show up as zero in
	// per-repo lookups and be absent from the batch map.
	repoC, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: owner.Login, Name: "c"})
	if err != nil {
		t.Fatalf("create repoC: %v", err)
	}

	// Seed: repoA has 2 stars + 1 fork + 2 open issues; repoB has 1 star + 0 forks + 1 closed issue.
	svc.DB.Create(&db.Star{UserID: owner.ID, RepositoryID: repoA.ID})
	svc.DB.Create(&db.Star{UserID: stargazer.ID, RepositoryID: repoA.ID})
	svc.DB.Create(&db.Star{UserID: stargazer.ID, RepositoryID: repoB.ID})

	parentID := repoA.ID
	svc.DB.Create(&db.Repository{OwnerID: owner.ID, Name: "a-fork", FullName: owner.Login + "/a-fork", ParentID: &parentID, Fork: true, DefaultBranch: "main"})

	svc.DB.Create(&db.Issue{RepositoryID: repoA.ID, Number: 1, State: db.StateOpen, AuthorID: owner.ID, Title: "open1"})
	svc.DB.Create(&db.Issue{RepositoryID: repoA.ID, Number: 2, State: db.StateOpen, AuthorID: owner.ID, Title: "open2"})
	svc.DB.Create(&db.Issue{RepositoryID: repoB.ID, Number: 1, State: db.StateClosed, AuthorID: owner.ID, Title: "closed1"})

	ids := []uint{repoA.ID, repoB.ID, repoC.ID}

	stars := svc.StarCountBatch(ctx, ids)
	if stars[repoA.ID] != 2 {
		t.Errorf("stars[A]=%d want 2", stars[repoA.ID])
	}
	if stars[repoB.ID] != 1 {
		t.Errorf("stars[B]=%d want 1", stars[repoB.ID])
	}
	if _, ok := stars[repoC.ID]; ok {
		t.Errorf("stars[C] unexpectedly present; missing repos should be absent from the batch map")
	}

	forks := svc.ForkCountBatch(ctx, ids)
	if forks[repoA.ID] != 1 {
		t.Errorf("forks[A]=%d want 1", forks[repoA.ID])
	}
	if _, ok := forks[repoB.ID]; ok {
		t.Errorf("forks[B] unexpectedly present (no forks)")
	}

	issues := svc.CountIssuesByRepoIDBatch(ctx, ids)
	if issues[repoA.ID] != 2 {
		t.Errorf("issues[A]=%d want 2", issues[repoA.ID])
	}
	// repoB's only issue is closed — the batch matches the single-repo method
	// which filters state='open', so the map should not contain repoB.
	if _, ok := issues[repoB.ID]; ok {
		t.Errorf("issues[B] unexpectedly present (no open issues)")
	}

	if got := svc.StarCountBatch(ctx, nil); got != nil {
		t.Errorf("StarCountBatch(nil) = %v, want nil", got)
	}
	if got := svc.StarCountBatch(ctx, []uint{}); got != nil {
		t.Errorf("StarCountBatch([]) = %v, want nil", got)
	}
}
