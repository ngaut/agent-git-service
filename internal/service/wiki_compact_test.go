package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

func TestCompactWikiHistory_SupersedesOldHistory_Issue1472(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "wiki-compact-owner", Name: "wiki-compact-owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-compact",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	full := owner.Login + "/wiki-compact"

	if err := svc.DB.Create(&db.User{
		Login: "wiki-bot",
		Name:  "Wiki Bot",
		Email: "gh-server@localhost",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed author user: %v", err)
	}
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

	result, err := svc.CompactWikiHistory(ctx, full)
	if err != nil {
		t.Fatalf("CompactWikiHistory: %v", err)
	}
	if result.Pages != 2 {
		t.Fatalf("result.Pages = %d, want 2", result.Pages)
	}

	historyAfter, err := svc.ListWikiPageHistory(ctx, full, "home")
	if err != nil {
		t.Fatalf("ListWikiPageHistory(after): %v", err)
	}
	if len(historyAfter) != 1 {
		t.Fatalf("historyAfter len = %d, want 1", len(historyAfter))
	}

	var revisions []db.WikiPageRevision
	if err := svc.DB.
		Where("page_id = (SELECT page_id FROM wiki_pages WHERE repository_id = (SELECT id FROM repositories WHERE full_name = ?) AND slug_ci_v1 = ?) ", full, "home").
		Order("revision_id ASC").
		Find(&revisions).Error; err != nil {
		t.Fatalf("load revisions: %v", err)
	}
	if len(revisions) != 3 {
		t.Fatalf("revision count = %d, want 3", len(revisions))
	}
	if revisions[0].SupersededByChangesetID == nil || revisions[1].SupersededByChangesetID == nil {
		t.Fatalf("expected pre-compact revisions to be superseded: %+v", revisions)
	}
	if revisions[2].SupersededByChangesetID != nil {
		t.Fatalf("expected compact revision to remain live: %+v", revisions[2])
	}

	var latestChangeset db.WikiChangeset
	if err := svc.DB.
		Where("repository_id = (SELECT id FROM repositories WHERE full_name = ?)", full).
		Order("changeset_id DESC").
		Take(&latestChangeset).Error; err != nil {
		t.Fatalf("load latest changeset: %v", err)
	}
	if latestChangeset.Source != string(wikicatalog.SourceCompact) {
		t.Fatalf("latest changeset source = %q, want %q", latestChangeset.Source, wikicatalog.SourceCompact)
	}
	if latestChangeset.SynthFormatVer != 1 {
		t.Fatalf("latest changeset synth_format_ver = %d, want 1", latestChangeset.SynthFormatVer)
	}
}

func TestCompactWikiHistory_ReadRefreshDoesNotReplayMasterAfterCompact_Issue1472(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "wiki-compact-refresh-owner", Name: "wiki-compact-refresh-owner", Type: db.TypeUser}
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
		Name:       "wiki-compact-refresh",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	full := owner.Login + "/wiki-compact-refresh"

	first, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nFirst version.\n", "create home", "")
	if err != nil {
		t.Fatalf("PutWikiPage(create): %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nSecond version.\n", "update home", first.SHA); err != nil {
		t.Fatalf("PutWikiPage(update): %v", err)
	}

	started := make(chan string, 1)
	svc.SetWikiBackgroundMigrationStartedHookForTest(func(repoFullName string) {
		started <- repoFullName
	})

	if _, err := svc.CompactWikiHistory(ctx, full); err != nil {
		t.Fatalf("CompactWikiHistory: %v", err)
	}
	if _, err := svc.ListWikiPageHistory(ctx, full, "home"); err != nil {
		t.Fatalf("ListWikiPageHistory(after compact): %v", err)
	}

	select {
	case repoFullName := <-started:
		t.Fatalf("background migration unexpectedly started for %q after compact", repoFullName)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestCompactWikiHistory_SupersedesDeletedPageHistory_Issue1472(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "wiki-compact-deleted-owner", Name: "wiki-compact-deleted-owner", Type: db.TypeUser}
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
		Name:       "wiki-compact-deleted",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	full := owner.Login + "/wiki-compact-deleted"

	livePage, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nCurrent version.\n", "create home", "")
	if err != nil {
		t.Fatalf("PutWikiPage(home): %v", err)
	}
	deletedPage, err := svc.PutWikiPage(ctx, full, "docs/old", "# Old\n\nRetired page.\n", "create old", "")
	if err != nil {
		t.Fatalf("PutWikiPage(old): %v", err)
	}
	if err := svc.DeleteWikiPage(ctx, full, "docs/old", "delete old"); err != nil {
		t.Fatalf("DeleteWikiPage(old): %v", err)
	}

	if _, err := svc.CompactWikiHistory(ctx, full); err != nil {
		t.Fatalf("CompactWikiHistory: %v", err)
	}

	history, err := svc.ListWikiPageHistory(ctx, full, "docs/old")
	if err != nil && !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("ListWikiPageHistory(deleted page): %v", err)
	}
	if err == nil && len(history) != 0 {
		t.Fatalf("deleted page history len after compact = %d, want 0", len(history))
	}

	var deletedRevisions []db.WikiPageRevision
	if err := svc.DB.
		Where("page_id = (SELECT page_id FROM wiki_pages WHERE repository_id = (SELECT id FROM repositories WHERE full_name = ?) AND slug_ci_v1 = ?) ", full, "docs/old").
		Order("revision_id ASC").
		Find(&deletedRevisions).Error; err != nil {
		t.Fatalf("load deleted page revisions: %v", err)
	}
	if len(deletedRevisions) != 2 {
		t.Fatalf("deleted revision count = %d, want 2", len(deletedRevisions))
	}
	for i, revision := range deletedRevisions {
		if revision.SupersededByChangesetID == nil {
			t.Fatalf("deleted revision %d was not superseded: %+v", i, revision)
		}
	}
	if _, err := svc.GetWikiPage(ctx, full, "home"); err != nil {
		t.Fatalf("GetWikiPage(live page after compact): %v", err)
	}
	if livePage.SHA == "" || deletedPage.SHA == "" {
		t.Fatal("expected seeded page SHAs to be populated")
	}
}
