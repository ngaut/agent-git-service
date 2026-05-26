package service_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

func TestCompactWikiHistory_ReplacesReachableHistory_Issue1460(t *testing.T) {
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

	historyBefore, err := svc.ListWikiPageHistory(ctx, full, "home")
	if err != nil {
		t.Fatalf("ListWikiPageHistory(before): %v", err)
	}
	if len(historyBefore) != 2 {
		t.Fatalf("historyBefore len = %d, want 2", len(historyBefore))
	}
	oldRef := historyBefore[1].SHA

	result, err := svc.CompactWikiHistory(ctx, full)
	if err != nil {
		t.Fatalf("CompactWikiHistory: %v", err)
	}
	if result.Pages != 2 {
		t.Fatalf("result.Pages = %d, want 2", result.Pages)
	}
	if result.CommitsRemoved != 1 {
		t.Fatalf("result.CommitsRemoved = %d, want 1", result.CommitsRemoved)
	}
	if result.PreviousHead == result.NewHead {
		t.Fatalf("expected rewritten head commit, got same sha %q", result.NewHead)
	}

	historyAfter, err := svc.ListWikiPageHistory(ctx, full, "home")
	if err != nil {
		t.Fatalf("ListWikiPageHistory(after): %v", err)
	}
	if len(historyAfter) != 1 {
		t.Fatalf("historyAfter len = %d, want 1", len(historyAfter))
	}
	if historyAfter[0].SHA != result.NewHead {
		t.Fatalf("historyAfter[0].SHA = %q, want %q", historyAfter[0].SHA, result.NewHead)
	}
	if _, err := svc.GetWikiPageAtRef(ctx, full, "home", oldRef); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("GetWikiPageAtRef(oldRef) err = %v, want ErrNotFound", err)
	}

	repoPath, err := svc.Git.GetRepoPath(ctx, full+".wiki")
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-list", "--count", "master")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-list --count master: %v, output=%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "1" {
		t.Fatalf("git history depth = %q, want 1", strings.TrimSpace(string(out)))
	}
}
