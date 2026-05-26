package service_test

import (
	"context"
	"errors"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

func TestCompactWikiHistory_IsTemporarilyDisabled_Issue1470(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "wiki-compact-owner", Name: "wiki-compact-owner", Type: db.TypeUser}
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
		Name:       "wiki-compact",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	full := owner.Login + "/wiki-compact"

	first, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nFirst version.\n", "create home", "")
	if err != nil {
		t.Fatalf("PutWikiPage(create): %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nSecond version.\n", "update home", first.SHA); err != nil {
		t.Fatalf("PutWikiPage(update): %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "docs/setup", "# Setup\n\nCurrent setup.\n", "create setup", ""); err != nil {
		t.Fatalf("PutWikiPage(setup): %v", err)
	}

	if _, err := svc.CompactWikiHistory(ctx, full); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("CompactWikiHistory err = %v, want ErrConflict", err)
	}

	historyAfter, err := svc.ListWikiPageHistory(ctx, full, "home")
	if err != nil {
		t.Fatalf("ListWikiPageHistory(after): %v", err)
	}
	if len(historyAfter) != 2 {
		t.Fatalf("historyAfter len = %d, want 2", len(historyAfter))
	}
}
