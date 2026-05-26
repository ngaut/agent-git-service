package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func TestCompactWikiHistory_GitProjectionFailureLeavesRetryablePendingProjection_Issue1472(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "wiki-compact-projection-owner", Name: "wiki-compact-projection-owner", Type: db.TypeUser}
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
		Name:       "wiki-compact-projection",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	full := owner.Login + "/wiki-compact-projection"

	first, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nFirst version.\n", "create home", "")
	if err != nil {
		t.Fatalf("PutWikiPage(create): %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n\nSecond version.\n", "update home", first.SHA); err != nil {
		t.Fatalf("PutWikiPage(update): %v", err)
	}

	service.SetTestWikiCompactRefUpdateFailureForTest(svc, func(repoFullName, commitSHA string) error {
		return errors.New("synthetic projection failure")
	})

	if _, err := svc.CompactWikiHistory(ctx, full); err == nil {
		t.Fatal("CompactWikiHistory with projection failure succeeded, want error")
	}

	var changesets []db.WikiChangeset
	if err := svc.DB.
		Where("repository_id = (SELECT id FROM repositories WHERE full_name = ?)", full).
		Order("changeset_id ASC").
		Find(&changesets).Error; err != nil {
		t.Fatalf("load changesets: %v", err)
	}
	if len(changesets) != 3 {
		t.Fatalf("changeset count = %d, want 3", len(changesets))
	}
	if changesets[0].SupersededByChangesetID == nil || changesets[1].SupersededByChangesetID == nil {
		t.Fatalf("pre-compact changesets should remain superseded after failed projection: %+v", changesets)
	}
	if changesets[2].SupersededByChangesetID != nil {
		t.Fatalf("compact changeset should remain live after failed projection: %+v", changesets[2])
	}
	if changesets[2].SynthFormatVer != 0 {
		t.Fatalf("compact changeset synth_format_ver = %d, want 0 when projection ref update fails", changesets[2].SynthFormatVer)
	}

	service.SetTestWikiCompactRefUpdateFailureForTest(svc, nil)

	retryResult, err := svc.CompactWikiHistory(ctx, full)
	if err != nil {
		t.Fatalf("CompactWikiHistory(retry pending projection): %v", err)
	}
	if retryResult.NewHead != changesets[2].SynthCommitSHA {
		t.Fatalf("retry NewHead = %q, want existing compact sha %q", retryResult.NewHead, changesets[2].SynthCommitSHA)
	}

	history, err := svc.ListWikiPageHistory(ctx, full, "home")
	if err != nil {
		t.Fatalf("ListWikiPageHistory(after retry): %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history len after retry = %d, want 1", len(history))
	}

	var latestChangesets []db.WikiChangeset
	if err := svc.DB.
		Where("repository_id = (SELECT id FROM repositories WHERE full_name = ?)", full).
		Order("changeset_id ASC").
		Find(&latestChangesets).Error; err != nil {
		t.Fatalf("reload changesets: %v", err)
	}
	if len(latestChangesets) != 3 {
		t.Fatalf("changeset count after retry = %d, want 3", len(latestChangesets))
	}
	if latestChangesets[2].SynthFormatVer != 1 {
		t.Fatalf("compact changeset synth_format_ver after retry = %d, want 1", latestChangesets[2].SynthFormatVer)
	}
}
