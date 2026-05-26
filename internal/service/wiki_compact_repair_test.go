package service_test

import (
	"context"
	"errors"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

func TestCompactWikiHistory_RepairsCatalogWhenRefUpdateFails(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "wiki-compact-repair-owner", Name: "wiki-compact-repair-owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := svc.DB.Create(&db.User{
		Login: "wiki-bot",
		Name:  "Wiki Bot",
		Email: "gh-server@localhost",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed author user: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-compact-repair",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	full := owner.Login + "/wiki-compact-repair"

	first, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nFirst version.\n", "create home", "")
	if err != nil {
		t.Fatalf("PutWikiPage(create): %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nSecond version.\n", "update home", first.SHA); err != nil {
		t.Fatalf("PutWikiPage(update): %v", err)
	}

	forcedErr := errors.New("forced compact ref failure")
	service.SetTestWikiCompactRefUpdateFailureForTest(svc, func(repoFullName, commitSHA string) error {
		if repoFullName != full {
			t.Fatalf("repoFullName = %q, want %q", repoFullName, full)
		}
		if commitSHA == "" {
			t.Fatalf("commitSHA should not be empty")
		}
		return forcedErr
	})
	t.Cleanup(func() {
		service.SetTestWikiCompactRefUpdateFailureForTest(svc, nil)
	})

	if _, err := svc.CompactWikiHistory(ctx, full); !errors.Is(err, forcedErr) {
		t.Fatalf("CompactWikiHistory err = %v, want forced error", err)
	}

	history, err := svc.ListWikiPageHistory(ctx, full, "home")
	if err != nil {
		t.Fatalf("ListWikiPageHistory(after repair): %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history len after repair = %d, want 2", len(history))
	}
}
