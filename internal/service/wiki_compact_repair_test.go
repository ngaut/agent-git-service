package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

func TestCompactWikiHistory_LeavesCatalogUnchangedWhenRefUpdateFails(t *testing.T) {
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

	var beforeHead db.WikiRepoHead
	if err := svc.DB.Where("repository_id = (SELECT id FROM repositories WHERE full_name = ?)", full).Take(&beforeHead).Error; err != nil {
		t.Fatalf("load repo head before compact: %v", err)
	}
	var beforeChangesets int64
	if err := svc.DB.Model(&db.WikiChangeset{}).
		Where("repository_id = (SELECT id FROM repositories WHERE full_name = ?)", full).
		Count(&beforeChangesets).Error; err != nil {
		t.Fatalf("count changesets before compact: %v", err)
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

	var afterHead db.WikiRepoHead
	if err := svc.DB.Where("repository_id = (SELECT id FROM repositories WHERE full_name = ?)", full).Take(&afterHead).Error; err != nil {
		t.Fatalf("load repo head after compact failure: %v", err)
	}
	if afterHead.HeadChangesetID != beforeHead.HeadChangesetID {
		t.Fatalf("head changeset after failure = %d, want %d", afterHead.HeadChangesetID, beforeHead.HeadChangesetID)
	}
	var afterChangesets int64
	if err := svc.DB.Model(&db.WikiChangeset{}).
		Where("repository_id = (SELECT id FROM repositories WHERE full_name = ?)", full).
		Count(&afterChangesets).Error; err != nil {
		t.Fatalf("count changesets after compact failure: %v", err)
	}
	if afterChangesets != beforeChangesets {
		t.Fatalf("changeset count after failure = %d, want %d", afterChangesets, beforeChangesets)
	}

	history, err := svc.ListWikiPageHistory(ctx, full, "home")
	if err != nil {
		t.Fatalf("ListWikiPageHistory(after failure): %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history len after failure = %d, want 2", len(history))
	}
}

func TestCompactWikiHistory_ClearsStaleRefLockBeforeUpdate(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "wiki-compact-stale-lock-owner", Name: "wiki-compact-stale-lock-owner", Type: db.TypeUser}
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
		Name:       "wiki-compact-stale-lock",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	full := owner.Login + "/wiki-compact-stale-lock"

	first, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nFirst version.\n", "create home", "")
	if err != nil {
		t.Fatalf("PutWikiPage(create): %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nSecond version.\n", "update home", first.SHA); err != nil {
		t.Fatalf("PutWikiPage(update): %v", err)
	}

	repoPath, err := svc.Git.GetRepoPath(ctx, full+".wiki")
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	lockPath := filepath.Join(repoPath, "refs", "heads", "master.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("lock"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	stale := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	if _, err := svc.CompactWikiHistory(ctx, full); err != nil {
		t.Fatalf("CompactWikiHistory: %v", err)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock should be removed, stat err = %v", err)
	}
}

func TestCompactWikiHistory_RejectsFreshRefLockWithoutAdvancingCatalog(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "wiki-compact-fresh-lock-owner", Name: "wiki-compact-fresh-lock-owner", Type: db.TypeUser}
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
		Name:       "wiki-compact-fresh-lock",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	full := owner.Login + "/wiki-compact-fresh-lock"

	first, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nFirst version.\n", "create home", "")
	if err != nil {
		t.Fatalf("PutWikiPage(create): %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nSecond version.\n", "update home", first.SHA); err != nil {
		t.Fatalf("PutWikiPage(update): %v", err)
	}

	var beforeHead db.WikiRepoHead
	if err := svc.DB.Where("repository_id = (SELECT id FROM repositories WHERE full_name = ?)", full).Take(&beforeHead).Error; err != nil {
		t.Fatalf("load repo head before compact: %v", err)
	}

	repoPath, err := svc.Git.GetRepoPath(ctx, full+".wiki")
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	lockPath := filepath.Join(repoPath, "refs", "heads", "master.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("lock"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := svc.CompactWikiHistory(ctx, full); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("CompactWikiHistory err = %v, want ErrConflict", err)
	}
	var afterHead db.WikiRepoHead
	if err := svc.DB.Where("repository_id = (SELECT id FROM repositories WHERE full_name = ?)", full).Take(&afterHead).Error; err != nil {
		t.Fatalf("load repo head after compact conflict: %v", err)
	}
	if afterHead.HeadChangesetID != beforeHead.HeadChangesetID {
		t.Fatalf("head changeset after fresh-lock refusal = %d, want %d", afterHead.HeadChangesetID, beforeHead.HeadChangesetID)
	}
	history, err := svc.ListWikiPageHistory(ctx, full, "home")
	if err != nil {
		t.Fatalf("ListWikiPageHistory(after fresh-lock refusal): %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history len after fresh lock refusal = %d, want 2", len(history))
	}
}
